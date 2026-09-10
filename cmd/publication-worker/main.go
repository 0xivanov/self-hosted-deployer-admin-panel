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

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/publisher"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/staticpublish"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	database := flag.String("database", "", "Private customer portal database")
	config := flag.String("assignment", "", "Private runtime assignment JSON")
	flag.Parse()
	if *database == "" || *config == "" {
		return errors.New("database and assignment are required")
	}
	info, err := os.Lstat(*config)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return errors.New("assignment must be a private regular file up to 16 KiB")
	}
	file, err := os.Open(*config)
	if err != nil {
		return errors.New("assignment unavailable")
	}
	defer file.Close()
	var assignment struct {
		Endpoint string `json:"endpoint"`
		Project  string `json:"project"`
		Token    string `json:"token"`
		CAFile   string `json:"ca_file"`
	}
	decoder := json.NewDecoder(io.LimitReader(file, 16385))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&assignment) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("invalid assignment JSON")
	}
	var roots *x509.CertPool
	if assignment.CAFile != "" {
		pem, err := os.ReadFile(assignment.CAFile)
		if err != nil {
			return errors.New("management CA unavailable")
		}
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return errors.New("invalid management CA")
		}
	}
	client, err := staticpublish.NewClient(assignment.Endpoint, assignment.Project, assignment.Token, roots)
	if err != nil {
		return err
	}
	defer client.Close()
	store, err := portal.Open(*database)
	if err != nil {
		return errors.New("portal database unavailable")
	}
	defer store.Close()
	worker, err := publisher.New(store, client)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	worker.Run(ctx, func(error) {
		fmt.Fprintln(os.Stderr, "Publication needs retry or reconciliation; inspect assigned project history.")
	})
	return nil
}
