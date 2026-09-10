//go:build integration

package staticruntime

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/staticpublish"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/staticsite"
)

func certificate(t *testing.T) (string, string, *x509.CertPool) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "runtime test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"content.test", "management.test"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	certpath, keypath := filepath.Join(root, "tls.crt"), filepath.Join(root, "tls.key")
	if err = os.WriteFile(certpath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keypath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return certpath, keypath, roots
}
func TestSeparateRuntimeListenersAndShutdown(t *testing.T) {
	t.Parallel()
	cert, key, roots := certificate(t)
	cfg := Config{Root: filepath.Join(t.TempDir(), "site"), Project: strings.Repeat("a", 64), Token: strings.Repeat("b", 64), ContentListen: "127.0.0.1:0", ManagementListen: "127.0.0.1:0", ContentHost: "content.test", ManagementHost: "management.test", ContentCert: cert, ContentKey: key, ManagementCert: cert, ManagementKey: key}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	ready := make(chan [2]string, 1)
	go func() { done <- Run(ctx, cfg, func(c, m string) { ready <- [2]string{c, m} }) }()
	var addresses [2]string
	select {
	case addresses = <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("startup timeout")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	request := func(address, host, method, path string, data []byte, auth bool) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, "https://"+address+path, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		if auth {
			req.Header.Set("Authorization", "Bearer "+cfg.Token)
			req.Header.Set("X-Project-ID", cfg.Project)
			req.Header.Set("X-Publication-Revision", "1")
			req.Header.Set("Content-Type", "application/zip")
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, body
	}
	var buffer bytes.Buffer
	z := zip.NewWriter(&buffer)
	f, err := z.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("Live static fixture")); err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	if code, _ := request(addresses[0], cfg.ContentHost, "POST", "/publish", buffer.Bytes(), true); code != 405 {
		t.Fatal("content listener exposed management", code)
	}
	if code, _ := request(addresses[1], cfg.ManagementHost, "GET", "/status", nil, false); code != 403 {
		t.Fatal("management auth bypass", code)
	}
	code, body := request(addresses[1], cfg.ManagementHost, "POST", "/publish", buffer.Bytes(), true)
	if code != 200 {
		t.Fatal(code, string(body))
	}
	var state staticpublish.Status
	if err = json.Unmarshal(body, &state); err != nil || state.Revision != 1 {
		t.Fatal(state, err)
	}
	if code, body = request(addresses[0], cfg.ContentHost, "GET", "/", nil, false); code != 200 || string(body) != "Live static fixture" {
		t.Fatal(code, string(body))
	}
	if code, _ = request(addresses[0], cfg.ManagementHost, "GET", "/status", nil, true); code != 421 {
		t.Fatal("foreign management host routed on public listener", code)
	}
	client.CloseIdleConnections()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timeout")
	}
	reopened, err := staticsite.Open(cfg.Root)
	if err != nil {
		t.Fatal("shutdown retained runtime lock", err)
	}
	defer reopened.Close()
	if reopened.Active() != state.Release {
		t.Fatal("release not persisted")
	}
}
func TestConfigRejectsUnsafeManagementBindings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, address string }{
		{"wildcard", ":8792"}, {"all IPv4", "0.0.0.0:8792"}, {"all IPv6", "[::]:8792"}, {"public", "8.8.8.8:8792"}, {"DNS rebinding", "localhost:8792"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(Config{Root: "private", ContentHost: "site.test", ManagementHost: "management.test", ContentListen: "127.0.0.1:0", ManagementListen: tc.address}); err == nil {
				t.Fatal("unsafe management listener accepted")
			}
		})
	}
	file := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(file, []byte(`{"token":"secret"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(file); err == nil {
		t.Fatal("public config accepted")
	}
	if err := os.Chmod(file, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"unexpected":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(file); err == nil {
		t.Fatal("unknown config accepted")
	}
}
