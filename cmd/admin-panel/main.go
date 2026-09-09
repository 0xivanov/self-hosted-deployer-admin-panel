package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/adminui"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/client"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	port := flag.Int("port", 8787, "local browser port")
	demo := flag.Bool("demo", false, "use simulated apps; never connect to live infrastructure")
	writes := flag.Bool("allow-writes", false, "enable deployments and session rollback")
	executable := flag.String("deployer", "deployer", "path to the deployer CLI")
	config := flag.String("config", "", "path to private deployer config")
	selected := flag.String("context", "", "saved customer context")
	publicURL := flag.String("public-url", "", "HTTPS origin for remote access, e.g. https://192.0.2.1:8787")
	cert := flag.String("tls-cert", "", "TLS certificate file for remote access")
	key := flag.String("tls-key", "", "TLS private key file for remote access")
	authFile := flag.String("auth-file", "", "private JSON file containing username and password")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	address, err := adminui.Address(*port)
	if err != nil {
		return err
	}
	options := adminui.Options{Host: address, AllowWrites: *writes || *demo, Demo: *demo}
	displayURL := "http://" + address
	if *publicURL != "" {
		u, e := url.Parse(*publicURL)
		if e != nil || u.Scheme != "https" || u.Hostname() == "" {
			return errors.New("public URL must use HTTPS and include a hostname")
		}
		if *cert == "" || *key == "" || *authFile == "" {
			return errors.New("remote access requires --tls-cert, --tls-key and --auth-file")
		}
		info, e := os.Lstat(*authFile)
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("auth file must be a private regular file (0600)")
		}
		data, e := os.ReadFile(*authFile)
		if e != nil {
			return e
		}
		var credentials struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if e = json.Unmarshal(data, &credentials); e != nil {
			return errors.New("invalid auth JSON")
		}
		options.Host, options.PublicURL = u.Host, *publicURL
		options.Username, options.Password = credentials.Username, credentials.Password
		address, displayURL = net.JoinHostPort("0.0.0.0", fmt.Sprint(*port)), *publicURL
	} else if *cert != "" || *key != "" || *authFile != "" {
		return errors.New("remote access flags require --public-url")
	}
	var backend adminui.Backend
	if *demo {
		backend = adminui.NewDemo()
		options.Environment = "Demo environment"
		options.Endpoint = "Simulated locally"
		options.Identity = "local-demo"
	} else {
		c, err := client.New(*executable, *config, *selected)
		if err != nil {
			return err
		}
		defer c.Close()
		backend = c
		options.Environment = c.Environment
		options.Endpoint = c.Endpoint
		options.Identity = c.Identity
	}
	handler, err := adminui.New(backend, options)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: handler, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	fmt.Printf("Admin panel: %s\nEnvironment: %s (%s)\n", displayURL, options.Environment, options.Endpoint)
	if !options.AllowWrites {
		fmt.Println("Read-only. Restart with --allow-writes to deploy or restore configurations.")
	}
	if *publicURL != "" {
		err = server.ServeTLS(listener, *cert, *key)
	} else {
		err = server.Serve(listener)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
