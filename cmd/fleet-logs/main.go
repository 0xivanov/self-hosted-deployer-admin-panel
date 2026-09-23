package main

import (
	"context"
	"flag"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/fleetlogs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	config := flag.String("config", "", "private fleet config")
	socket := flag.String("socket", "", "private Unix socket path")
	flag.Parse()
	if *config == "" || !filepath.IsAbs(*socket) {
		log.Fatal("config and absolute socket path required")
	}
	dir, err := os.Stat(filepath.Dir(*socket))
	if err != nil || !dir.IsDir() || dir.Mode().Perm()&0077 != 0 {
		log.Fatal("socket directory must be private")
	}
	// RuntimeDirectory is recreated by systemd; never unlink an existing listener.
	listener, err := net.Listen("unix", *socket)
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()
	if err = os.Chmod(*socket, 0600); err != nil {
		log.Fatal(err)
	}
	server := http.Server{Handler: fleetlogs.Handler(func(ctx context.Context, id string) (string, error) { return fleetlogs.ReadFleet(ctx, *config, id) }), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second, MaxHeaderBytes: 4096}
	log.Fatal(server.Serve(listener))
}
