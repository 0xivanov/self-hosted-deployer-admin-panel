// customer-portal is deliberately separate from the privileged operator console.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	demo := flag.Bool("demo", false, "local disposable demo with synthetic credentials")
	listen := flag.String("listen", "127.0.0.1:8791", "listener address")
	origin := flag.String("origin", "http://127.0.0.1:8791", "exact browser origin")
	database := flag.String("database", "", "private portal SQLite path; never the deployer database")
	cert := flag.String("tls-cert", "", "HTTPS certificate")
	key := flag.String("tls-key", "", "HTTPS private key")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	if *demo {
		host, _, err := net.SplitHostPort(*listen)
		if err != nil || host != "127.0.0.1" || *origin != "http://"+*listen {
			return errors.New("demo requires matching 127.0.0.1 listener and HTTP origin")
		}
		if *database != "" || *cert != "" || *key != "" {
			return errors.New("demo uses a disposable database and no TLS files")
		}
		dir, err := os.MkdirTemp("", "customer-portal-demo-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		*database = filepath.Join(dir, "portal.db")
	} else if *database == "" || *cert == "" || *key == "" {
		return errors.New("non-demo mode requires --database, --origin with HTTPS, --tls-cert and --tls-key")
	}
	store, err := portal.Open(*database)
	if err != nil {
		return err
	}
	defer store.Close()
	if *demo {
		_, token, err := store.Register(context.Background(), "demo@example.test", "demo-only-password", "Demo workspace")
		if err != nil {
			return err
		}
		if err = store.Verify(context.Background(), token); err != nil {
			return err
		}
		fmt.Println("Disposable demo login: demo@example.test / demo-only-password")
	}
	handler, err := portal.NewHTTP(store, portal.HTTPOptions{Origin: *origin, Development: *demo})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: handler, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	fmt.Println("Customer portal:", *origin)
	if *demo {
		err = server.Serve(listener)
	} else {
		err = server.ServeTLS(listener, *cert, *key)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
