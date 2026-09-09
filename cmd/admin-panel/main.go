package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
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
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	address, err := adminui.Address(*port)
	if err != nil {
		return err
	}
	options := adminui.Options{Host: address, AllowWrites: *writes || *demo, Demo: *demo}
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
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	fmt.Printf("Admin panel: http://%s\nEnvironment: %s (%s)\n", address, options.Environment, options.Endpoint)
	if !options.AllowWrites {
		fmt.Println("Read-only. Restart with --allow-writes to deploy or restore configurations.")
	}
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
