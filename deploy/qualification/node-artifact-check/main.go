// Guest fixture helper. Validates/stages a built archive; never starts its code.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodeartifact"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if runtime.GOOS != "linux" || os.Getuid() == 0 || len(os.Args) != 4 {
		return fmt.Errorf("guest usage: node-artifact-check ZIP SHA256 PRIVATE_ROOT")
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
	root, err := os.OpenRoot(os.Args[3])
	if err != nil {
		return err
	}
	defer root.Close()
	release, err := nodeartifact.Extract(context.Background(), data, os.Args[2], root)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(release)
}
