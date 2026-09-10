//go:build linux

// Trusted disposable-VM helper. Installs a verified fixture; never starts code.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodeartifact"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodelaunch"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) != 3 || os.Geteuid() != 0 {
		return fmt.Errorf("guest root usage: node-runtime-install ZIP SHA256")
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, nodeartifact.MaxCompressed+1))
	if err != nil {
		return err
	}
	root, err := os.OpenRoot("/opt/deployer-node/releases")
	if err != nil {
		return err
	}
	defer root.Close()
	release, err := nodelaunch.InstallRelease(context.Background(), data, os.Args[2], root)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(release)
}
