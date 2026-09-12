// node-artifact-export runs in the trusted controller against an immutable
// snapshot of a stopped builder. It must never mount live customer output.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodeartifact"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Artifact export failed:", err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) != 3 {
		return errors.New("usage: node-artifact-export STOPPED_BUILD_SNAPSHOT NEW_ARCHIVE_PATH")
	}
	root, err := os.OpenRoot(os.Args[1])
	if err != nil {
		return err
	}
	defer root.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	data, manifest, err := nodeartifact.Export(ctx, root)
	if err != nil {
		return err
	}
	parent, err := os.OpenRoot(filepath.Dir(os.Args[2]))
	if err != nil {
		return err
	}
	defer parent.Close()
	info, err := parent.Stat(".")
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("archive destination must be private")
	}
	name := filepath.Base(os.Args[2])
	f, err := parent.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			parent.Remove(name)
		}
	}()
	_, writeErr := f.Write(data)
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	dir, err := parent.Open(".")
	if err != nil {
		return err
	}
	err = errors.Join(dir.Sync(), dir.Close())
	if err != nil {
		return err
	}
	complete = true
	// Archive identity is not VM retirement evidence or permission to deploy.
	return json.NewEncoder(os.Stdout).Encode(manifest)
}
