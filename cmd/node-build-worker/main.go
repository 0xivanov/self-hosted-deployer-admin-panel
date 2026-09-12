package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuildapi"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/npmfetch"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

type assignment struct {
	Endpoint     string `json:"endpoint"`
	Project      string `json:"project"`
	Token        string `json:"token"`
	CAFile       string `json:"ca_file"`
	Toolchain    string `json:"toolchain_sha256"`
	Architecture string `json:"architecture"`
	Dependencies string `json:"dependencies_directory"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	database := flag.String("database", "", "Private customer portal database")
	config := flag.String("assignment", "", "Private build executor assignment JSON")
	flag.Parse()
	if *database == "" || *config == "" {
		return errors.New("database and assignment are required")
	}
	info, err := os.Lstat(*config)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return errors.New("assignment must be a private regular file up to 16 KiB")
	}
	f, err := os.Open(*config)
	if err != nil {
		return errors.New("assignment unavailable")
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 16385))
	d.DisallowUnknownFields()
	var a assignment
	if d.Decode(&a) != nil || d.Decode(&struct{}{}) != io.EOF {
		return errors.New("invalid build assignment JSON")
	}
	var roots *x509.CertPool
	if a.CAFile != "" {
		pem, err := os.ReadFile(a.CAFile)
		if err != nil {
			return errors.New("executor CA unavailable")
		}
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return errors.New("invalid executor CA")
		}
	}
	client, err := nodebuildapi.NewClient(a.Endpoint, a.Project, a.Toolchain, a.Architecture, a.Token, roots)
	if err != nil {
		return err
	}
	defer client.Close()
	info, err = os.Lstat(a.Dependencies)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("private dependency directory required")
	}
	jobs, err := os.OpenRoot(a.Dependencies)
	if err != nil {
		return errors.New("dependency storage unavailable")
	}
	defer jobs.Close()
	s, err := portal.Open(*database)
	if err != nil {
		return errors.New("portal database unavailable")
	}
	defer s.Close()
	downloader := npmfetch.NewClient()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		_, err = s.WorkNodeBuild(ctx, a.Project, a.Toolchain, a.Architecture, jobs, downloader, client)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "Node build needs attention; inspect the assigned project and executor state.")
		}
		timer.Reset(5 * time.Second)
	}
}
