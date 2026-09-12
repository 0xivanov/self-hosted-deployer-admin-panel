//go:build linux && integration

package noderuntime

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodelaunch"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/noderuntimeapi"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

func freeAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	l.Close()
	return address
}
func TestRuntimeServiceAcceptsAndServesNodeArchive(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires provisioned disposable Linux root runtime")
	}
	pin := "5f4ddab610c1ab2016b3c227cebdbf6d9495161487e4739c7b90090595f465f7"
	if _, err := os.Stat("/opt/deployer-node/toolchains/" + pin + "/bin/node"); err != nil {
		t.Skip("trusted Node fixture toolchain not provisioned")
	}
	directory := t.TempDir()
	fixtureTLS := httptest.NewTLSServer(http.NotFoundHandler())
	certificate := fixtureTLS.Certificate()
	key, err := x509.MarshalPKCS8PrivateKey(fixtureTLS.TLS.Certificates[0].PrivateKey)
	fixtureTLS.Close()
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(directory, "cert.pem"), filepath.Join(directory, "key.pem")
	if err = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
	management, content := freeAddress(t), freeAddress(t)
	assignment := Assignment{ProjectID: strings.Repeat("a", 64), RuntimeID: strings.Repeat("b", 64), ToolchainSHA256: pin, Architecture: "arm64"}
	cfg := Config{Assignment: assignment, StateDirectory: filepath.Join(directory, "state"), Slots: []nodelaunch.Slot{{UID: 60000, Port: 31877}, {UID: 60001, Port: 31878}}, ContentHost: "site.example.test", ContentListen: content, ContentCertificate: certPath, ContentKey: keyPath, HealthPath: "/health", ManagementHost: management, ManagementListen: management, ManagementCertificate: certPath, ManagementKey: keyPath, ManagementToken: strings.Repeat("c", 64)}
	var entropy [32]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		t.Fatal(err)
	}
	operation := hex.EncodeToString(entropy[:])
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	f, _ := z.Create("package.json")
	f.Write([]byte(`{"scripts":{"start":"node server.js"}}`))
	f, _ = z.Create("server.js")
	f.Write([]byte(`require('http').createServer((req,res)=>res.end('runtime feature works')).listen(Number(process.env.PORT),'127.0.0.1')`))
	z.Close()
	digest := sha256.Sum256(archive.Bytes())
	request := portal.NodeRuntimeRequest{ProjectID: assignment.ProjectID, RuntimeID: assignment.RuntimeID, OperationID: operation, DeploymentID: operation, ReleaseID: operation, ArtifactSHA256: hex.EncodeToString(digest[:]), ToolchainSHA256: pin, Architecture: "arm64", Revision: 1, ActivateBefore: time.Now().Add(time.Minute).Unix(), Archive: archive.Bytes()}
	runCtx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Run(runCtx, cfg, nil) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
		pool, err := nodelaunch.OpenPool(filepath.Join(cfg.StateDirectory, "pool.db"), nodelaunch.PoolConfig{ProjectID: assignment.ProjectID, RuntimeID: assignment.RuntimeID, ToolchainSHA256: pin, Architecture: "arm64", Slots: cfg.Slots})
		if err != nil {
			t.Error(err)
			return
		}
		defer pool.Close()
		reserved, err := pool.Lookup(context.Background(), operation)
		if err != nil {
			t.Error(err)
			return
		}
		root, err := os.OpenRoot(filepath.Join(cfg.StateDirectory, "control"))
		if err != nil {
			t.Error(err)
			return
		}
		defer root.Close()
		gate, err := nodelaunch.OpenControlGate(root)
		if err != nil {
			t.Error(err)
			return
		}
		if _, err = gate.RetireSystemd(context.Background(), reserved.Assignment); err != nil {
			t.Error(err)
			return
		}
		unit, _ := nodelaunch.Render(reserved.Assignment)
		os.Remove("/run/systemd/system/" + unit.Name)
	}()
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	client, err := noderuntimeapi.NewClient("https://"+management, assignment.ProjectID, assignment.RuntimeID, cfg.ManagementToken, roots)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		connection, e := net.DialTimeout("tcp", management, 100*time.Millisecond)
		if e == nil {
			connection.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runtime listener did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err = client.SubmitNodeRuntime(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(15 * time.Second)
	for {
		observation, e := client.InspectNodeRuntime(t.Context(), assignment.RuntimeID, assignment.ProjectID, operation)
		if e == nil && observation.Healthy && observation.Settled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runtime did not become live: %v", e)
		}
		time.Sleep(100 * time.Millisecond)
	}
	browser := fixtureTLS.Client()
	browser.Timeout = 3 * time.Second
	req, err := http.NewRequest("GET", "https://"+content+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = cfg.ContentHost
	response, err := browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 || string(body) != "runtime feature works" {
		t.Fatal(response.StatusCode, string(body), err)
	}
}
