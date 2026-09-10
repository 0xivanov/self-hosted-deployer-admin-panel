// Package staticruntime runs separate content and management HTTPS listeners.
package staticruntime

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/staticpublish"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/staticsite"
)

type Config struct {
	Root             string `json:"root"`
	Project          string `json:"project"`
	Token            string `json:"token"`
	ContentListen    string `json:"content_listen"`
	ContentHost      string `json:"content_host"`
	ManagementListen string `json:"management_listen"`
	ManagementHost   string `json:"management_host"`
	ContentCert      string `json:"content_cert"`
	ContentKey       string `json:"content_key"`
	ManagementCert   string `json:"management_cert"`
	ManagementKey    string `json:"management_key"`
	SPA              bool   `json:"spa"`
}

func Load(path string) (Config, error) {
	var cfg Config
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return cfg, errors.New("runtime config must be a private regular file up to 16 KiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return cfg, errors.New("runtime config unavailable")
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 16385))
	d.DisallowUnknownFields()
	if d.Decode(&cfg) != nil || d.Decode(&struct{}{}) != io.EOF {
		return Config{}, errors.New("invalid runtime configuration")
	}
	return cfg, nil
}
func keyPair(cert, key string) (tls.Certificate, error) {
	info, err := os.Lstat(key)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return tls.Certificate{}, errors.New("runtime TLS key must be private")
	}
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return tls.Certificate{}, errors.New("runtime TLS certificate unavailable")
	}
	return pair, nil
}
func Validate(cfg Config) error {
	if cfg.Root == "" || cfg.ContentHost == "" || cfg.ManagementHost == "" || cfg.ContentHost == cfg.ManagementHost {
		return errors.New("distinct content and management hosts and private storage are required")
	}
	ip, _, err := net.SplitHostPort(cfg.ManagementListen)
	if err != nil {
		return errors.New("management listener must use an explicit private or loopback IP")
	}
	parsed := net.ParseIP(ip)
	if parsed == nil || (!parsed.IsLoopback() && !parsed.IsPrivate()) {
		return errors.New("management listener must use an explicit private or loopback IP")
	}
	if _, _, err = net.SplitHostPort(cfg.ContentListen); err != nil {
		return errors.New("invalid content listener")
	}
	return nil
}

// Run loads credentials before binding, then opens both listeners before serving.
// ready reports bound addresses for local orchestration, without secrets.
func Run(ctx context.Context, cfg Config, ready func(string, string)) error {
	if err := Validate(cfg); err != nil {
		return err
	}
	contentPair, err := keyPair(cfg.ContentCert, cfg.ContentKey)
	if err != nil {
		return err
	}
	managementPair, err := keyPair(cfg.ManagementCert, cfg.ManagementKey)
	if err != nil {
		return err
	}
	site, err := staticsite.Open(cfg.Root)
	if err != nil {
		return err
	}
	defer site.Close()
	content, err := site.Handler(cfg.ContentHost, cfg.SPA)
	if err != nil {
		return err
	}
	management, err := staticpublish.Handler(site, cfg.ManagementHost, cfg.Project, cfg.Token)
	if err != nil {
		return err
	}
	contentListener, err := net.Listen("tcp", cfg.ContentListen)
	if err != nil {
		return errors.New("content listener unavailable")
	}
	defer contentListener.Close()
	managementListener, err := net.Listen("tcp", cfg.ManagementListen)
	if err != nil {
		return errors.New("management listener unavailable")
	}
	defer managementListener.Close()
	makeServer := func(handler http.Handler, pair tls.Certificate) *http.Server {
		return &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}}
	}
	servers := []*http.Server{makeServer(content, contentPair), makeServer(management, managementPair)}
	results := make(chan error, 2)
	go func() { results <- servers[0].ServeTLS(contentListener, "", "") }()
	go func() { results <- servers[1].ServeTLS(managementListener, "", "") }()
	if ready != nil {
		ready(contentListener.Addr().String(), managementListener.Addr().String())
	}
	consumed := 0
	var serveErr error
	select {
	case <-ctx.Done():
	case err = <-results:
		consumed++
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = errors.New("runtime listener stopped unexpectedly")
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, server := range servers {
		if err = server.Shutdown(shutdownCtx); err != nil {
			server.Close()
		}
	}
	for consumed < 2 {
		<-results
		consumed++
	}
	return serveErr
}
