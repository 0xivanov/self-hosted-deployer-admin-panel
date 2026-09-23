// registry-image-check reads registry metadata only; it never deploys or pulls layers.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/registryimage"
	"io"
	"os"
)

func run() error {
	image := flag.String("image", "", "Docker Hub/GHCR image with explicit tag or sha256 digest")
	credentials := flag.String("credentials", "", "optional private JSON file with username and password (registry token)")
	flag.Parse()
	if flag.NArg() != 0 || *image == "" {
		return registryimage.ErrReference
	}
	var c registryimage.Credentials
	if *credentials != "" {
		f, err := os.Open(*credentials)
		if err != nil {
			return errors.New("private registry credentials file unavailable")
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
			return errors.New("registry credentials must be in a private regular file, at most 16 KiB")
		}
		var data struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		d := json.NewDecoder(io.LimitReader(f, 16385))
		d.DisallowUnknownFields()
		if d.Decode(&data) != nil {
			return registryimage.ErrCredentials
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			return registryimage.ErrCredentials
		}
		if data.Username == "" || data.Password == "" {
			return registryimage.ErrCredentials
		}
		c = registryimage.Credentials{Username: data.Username, Password: data.Password}
	}
	candidate, err := registryimage.Resolve(context.Background(), *image, c)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(candidate)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
