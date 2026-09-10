// Trusted lab helper to fetch verified fixture tarballs without executing them.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	type item struct {
		File      string
		Integrity string
	}
	bundle := struct {
		SourceSHA256 string
		Tarballs     []item
	}{SourceSHA256: set.SourceSHA256, Tarballs: []item{}}
	client := npmfetch.NewClient()
	total := 0
	for _, t := range set.Tarballs {
		payload, e := client.Fetch(ctx, t)
		if e != nil {
			return e
		}
		total += len(payload)
		if total > 50<<20 {
			return fmt.Errorf("fixture download budget exceeded")
		}
		sum := sha256.Sum256(payload)
		name := hex.EncodeToString(sum[:]) + ".tgz"
		out, e := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, e = out.Write(payload)
		closeErr := out.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		bundle.Tarballs = append(bundle.Tarballs, item{name, t.Integrity})
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	return root.WriteFile("bundle.json", raw, 0600)
}
