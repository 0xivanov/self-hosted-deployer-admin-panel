package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/fleetdeploy"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

func main() {
	config := flag.String("config", "", "private fleet worker configuration")
	flag.Parse()
	if *config == "" {
		fmt.Fprintln(os.Stderr, "-config is required")
		os.Exit(2)
	}
	cfg, err := fleetdeploy.LoadConfig(*config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	store, err := portal.Open(cfg.Database)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer store.Close()
	worker, err := fleetdeploy.New(store, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer worker.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	worker.Run(ctx, func(err error) { fmt.Fprintln(os.Stderr, err) })
}
