//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/noderuntime"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Node runtime stopped:", err)
		os.Exit(1)
	}
}
func run() error {
	file := flag.String("config", "", "Private Node runtime configuration JSON")
	flag.Parse()
	cfg, err := noderuntime.Load(*file)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return noderuntime.Run(ctx, cfg, func(error) {
		fmt.Fprintln(os.Stderr, "Node runtime operation needs reconciliation; inspect its saved state.")
	})
}
