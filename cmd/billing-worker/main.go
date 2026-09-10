package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/hostingbilling"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	database := flag.String("database", "", "Private customer portal database")
	config := flag.String("config", "", "Private Stripe test configuration JSON")
	flag.Parse()
	if *database == "" || *config == "" {
		return errors.New("database and config are required")
	}
	info, err := os.Lstat(*config)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return errors.New("billing configuration must be a private regular file up to 16 KiB")
	}
	file, err := os.Open(*config)
	if err != nil {
		return errors.New("billing configuration unavailable")
	}
	defer file.Close()
	var cfg struct {
		SecretKey  string            `json:"secret_key"`
		SuccessURL string            `json:"success_url"`
		CancelURL  string            `json:"cancel_url"`
		Plans      map[string]string `json:"plans"`
	}
	decoder := json.NewDecoder(io.LimitReader(file, 16385))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cfg) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("invalid billing configuration JSON")
	}
	client, err := hostingbilling.NewTestClient(cfg.SecretKey, cfg.SuccessURL, cfg.CancelURL, cfg.Plans)
	if err != nil {
		return err
	}
	defer client.Close()
	store, err := portal.Open(*database)
	if err != nil {
		return errors.New("portal database unavailable")
	}
	defer store.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Plan rows are managed separately by the operator. This process must not
	// re-enable a disabled plan or change a persisted checkout's saved price.
	store.RunBillingWorker(ctx, client, func(error) {
		fmt.Fprintln(os.Stderr, "Billing work needs retry or reconciliation; inspect private billing records.")
	})
	return nil
}
