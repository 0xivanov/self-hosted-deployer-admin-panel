// Trusted lab helper to fetch verified fixture tarballs without executing them.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/npmfetch"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/projectarchive"
	"io"
	"os"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) != 4 {
		return fmt.Errorf("usage: node-prefetch ZIP SHA256 PRIVATE_OUTPUT")
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, projectarchive.MaxCompressed+1))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	set, err := npmfetch.FromSource(ctx, data, os.Args[2])
	if err != nil {
		return err
	}
	if len(set.Tarballs) > 10 {
		return fmt.Errorf("fixture package limit exceeded")
	}
	root, err := os.OpenRoot(os.Args[3])
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("private output required")
	}
	bundle, err := npmfetch.DownloadBundle(ctx, data, os.Args[2], root, npmfetch.NewClient())
	if err != nil {
		return err
	}
	if _, err = npmfetch.VerifyBundle(ctx, root, bundle, os.Args[2]); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(bundle)
}
