//go:build integration

package integration

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/staticruntime"
)

type child struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
	log  string
}

func start(t *testing.T, root, binary string, args ...string) *child {
	t.Helper()
	log, err := os.CreateTemp(root, "process-")
	if err != nil {
		t.Fatal(err)
	}
	c := &child{cmd: exec.Command(binary, args...), done: make(chan struct{}), log: log.Name()}
	c.cmd.Stdout = log
	c.cmd.Stderr = log
	if err = c.cmd.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	go func() { c.err = c.cmd.Wait(); log.Close(); close(c.done) }()
	t.Cleanup(func() { c.stop(t) })
	return c
}
func (c *child) stop(t *testing.T) {
	t.Helper()
	select {
	case <-c.done:
		return
	default:
	}
	c.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-c.done:
		if c.err != nil {
			log, _ := os.ReadFile(c.log)
			t.Errorf("process shutdown: %v: %s", c.err, log)
		}
	case <-time.After(12 * time.Second):
		c.cmd.Process.Kill()
		<-c.done
		t.Error("process shutdown timed out")
	}
}
func (c *child) check(t *testing.T) {
	t.Helper()
	select {
	case <-c.done:
		log, _ := os.ReadFile(c.log)
		t.Fatalf("process exited: %v: %s", c.err, log)
	default:
	}
}
func address(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := l.Addr().String()
	l.Close()
	return a
}
func saveJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func certs(t *testing.T, root string) (string, string, *x509.CertPool) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cp, kp := filepath.Join(root, "tls.crt"), filepath.Join(root, "tls.key")
	if err = os.WriteFile(cp, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(kp, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600); err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return cp, kp, pool
}
func TestStaticPublishingProcesses(t *testing.T) {
	// Deliberately runs real entrypoints and TLS, rather than sharing in-memory
	// Store/Site objects across the portal, worker and runtime boundaries.
	root := t.TempDir()
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	binaries := map[string]string{}
	for _, name := range []string{"customer-portal", "publication-worker", "static-runtime"} {
		binary := filepath.Join(root, name)
		cmd := exec.Command("go", "build", "-race", "-o", binary, "./cmd/"+name)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v: %s", name, err, out)
		}
		binaries[name] = binary
	}
	cp, kp, roots := certs(t, root)
	dbpath := filepath.Join(root, "db", "portal.db")
	store, err := portal.Open(dbpath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	a, verification, err := store.Register(ctx, "process@example.test", "synthetic-process-password", "process workspace")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Verify(ctx, verification); err != nil {
		t.Fatal(err)
	}
	session, err := store.Login(ctx, a.Email, "synthetic-process-password")
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, session.Token, a.WorkspaceID, "process site", "static")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	portalAddress, managementAddress, contentAddress := address(t), address(t), address(t)
	_, contentPort, _ := net.SplitHostPort(contentAddress)
	contentHost := "localhost:" + contentPort
	origin := "https://" + portalAddress
	contentOrigin := "https://" + contentHost
	config := staticruntime.Config{Root: filepath.Join(root, "site"), Project: project.ID, Token: strings.Repeat("b", 64), ContentListen: contentAddress, ContentHost: contentHost, ManagementListen: managementAddress, ManagementHost: managementAddress, ContentCert: cp, ContentKey: kp, ManagementCert: cp, ManagementKey: kp}
	runtimeFile, workerFile, sitesFile := filepath.Join(root, "runtime.json"), filepath.Join(root, "worker.json"), filepath.Join(root, "sites.json")
	saveJSON(t, runtimeFile, config)
	saveJSON(t, workerFile, map[string]string{"project": project.ID, "endpoint": "https://" + managementAddress, "token": config.Token, "ca_file": cp})
	saveJSON(t, sitesFile, map[string]string{project.ID: contentOrigin})
	runtime := start(t, root, binaries["static-runtime"], "--config", runtimeFile)
	portalArgs := []string{"--database", dbpath, "--listen", portalAddress, "--origin", origin, "--tls-cert", cp, "--tls-key", kp, "--publication-sites", sitesFile}
	portalProcess := start(t, root, binaries["customer-portal"], portalArgs...)
	worker := start(t, root, binaries["publication-worker"], "--database", dbpath, "--assignment", workerFile)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}, Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	var csrf string
	request := func(method, url, typ string, body []byte) (int, []byte, error) {
		r, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		if method == "POST" {
			r.Header.Set("Origin", origin)
			r.Header.Set("X-CSRF-Token", csrf)
			r.Header.Set("Content-Type", typ)
		}
		response, err := client.Do(r)
		if err != nil {
			return 0, nil, err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		return response.StatusCode, data, err
	}
	wait := func(what string, check func() bool) {
		t.Helper()
		deadline := time.Now().Add(25 * time.Second)
		for time.Now().Before(deadline) {
			runtime.check(t)
			portalProcess.check(t)
			worker.check(t)
			if check() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("timed out: " + what)
	}
	wait("portal startup", func() bool {
		code, _, err := request("GET", origin+"/api/config", "", nil)
		return err == nil && code == 200
	})
	code, data, err := request("POST", origin+"/api/login", "application/json", []byte(`{"email":"process@example.test","password":"synthetic-process-password"}`))
	if err != nil || code != 200 {
		t.Fatal(code, string(data), err)
	}
	var login struct {
		CSRF string `json:"csrf"`
	}
	if err = json.Unmarshal(data, &login); err != nil {
		t.Fatal(err)
	}
	csrf = login.CSRF
	upload := func(text string) portal.Upload {
		t.Helper()
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		f, err := z.Create("index.html")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
		if err = z.Close(); err != nil {
			t.Fatal(err)
		}
		code, data, err := request("POST", origin+"/api/uploads?project="+project.ID, "application/zip", b.Bytes())
		if err != nil || code != 200 {
			t.Fatal(code, string(data), err)
		}
		var u portal.Upload
		if err = json.Unmarshal(data, &u); err != nil {
			t.Fatal(err)
		}
		return u
	}
	publish := func(u portal.Upload, key, text string) {
		t.Helper()
		payload, _ := json.Marshal(map[string]string{"project": project.ID, "upload": u.ID, "key": key})
		code, data, err := request("POST", origin+"/api/publications", "application/json", payload)
		if err != nil || code != 200 {
			t.Fatal(code, string(data), err)
		}
		var j portal.PublicationJob
		if err = json.Unmarshal(data, &j); err != nil {
			t.Fatal(err)
		}
		wait("publication acknowledgement", func() bool {
			code, data, err := request("GET", origin+"/api/publications?project="+project.ID, "", nil)
			var history struct {
				Active string `json:"active"`
			}
			return err == nil && code == 200 && json.Unmarshal(data, &history) == nil && history.Active == j.ID
		})
		code, data, err = request("GET", contentOrigin+"/", "", nil)
		if err != nil || code != 200 || string(data) != text {
			t.Fatal("wrong public release", code, string(data), err)
		}
	}
	first := upload("first process release")
	publish(first, "process-publish-first", "first process release")
	second := upload("second process release")
	publish(second, "process-publish-second", "second process release")
	client.CloseIdleConnections()
	runtime.stop(t)
	portalProcess.stop(t)
	runtime = start(t, root, binaries["static-runtime"], "--config", runtimeFile)
	portalProcess = start(t, root, binaries["customer-portal"], portalArgs...)
	wait("restart with persistent session and content", func() bool {
		code, _, err := request("GET", origin+"/api/session", "", nil)
		if err != nil || code != 200 {
			return false
		}
		code, data, err := request("GET", contentOrigin+"/", "", nil)
		return err == nil && code == 200 && string(data) == "second process release"
	})
	publish(first, "process-rollback-first", "first process release")
	t.Log("Verified HTTPS upload, two publications, portal/runtime restart, persistent session and rollback across three race-enabled processes")
}
