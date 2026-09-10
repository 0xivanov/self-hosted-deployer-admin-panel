// Synthetic lab helper: prepares source and plan, never runs uploaded commands.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/projectarchive"
	"io"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) != 4 {
		return fmt.Errorf("usage: node-preflight ZIP SHA256 PRIVATE_JOB_ROOT")
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		return err
	}
	defer f.Close()
	source, err := io.ReadAll(io.LimitReader(f, projectarchive.MaxCompressed+1))
	if err != nil {
		return err
	}
	plan, err := nodebuild.Prepare(context.Background(), source, os.Args[2], nodebuild.Settings{Architecture: "arm64"})
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(os.Args[3])
	if err != nil {
		return err
	}
	defer root.Close()
	extracted, err := nodebuild.ExtractSource(context.Background(), source, os.Args[2], root)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Plan   nodebuild.Plan
		Source nodebuild.Source
	}{plan, extracted})
}
