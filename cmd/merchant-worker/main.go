// merchant-worker refreshes existing merchant accounts and checkout sessions.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/merchantbilling"
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
	config := flag.String("config", "", "Private merchant JSON with mode, secret_key and countries")
	merchantConfig := flag.String("merchant-config", "", "Private Stripe Connect settings for the selected merchant mode")
	merchantMode := flag.String("merchant-mode", "", "merchant payments: test or live")
	origin := flag.String("origin", "", "Exact HTTPS customer portal origin")
	once := flag.Bool("once", false, "Refresh one bounded batch then exit")
	batch := flag.Int("batch", 50, "Maximum accounts and orders per pass, 1 to 100")
	interval := flag.Duration("interval", 30*time.Second, "Pause between passes, 10 seconds to 2 minutes")
	flag.Parse()
	if flag.NArg() != 0 || *database == "" || *batch < 1 || *batch > 100 || *interval < 10*time.Second || *interval > 2*time.Minute {
		return errors.New("database, config, origin and valid batch/interval are required")
	}
	selectedMode, selectedConfig, err := resolveMerchantWorkerSettings(*merchantMode, *config, *merchantConfig)
	if err != nil {
		return err
	}
	u, err := url.Parse(*origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("origin must be an exact HTTPS origin")
	}
	info, err := os.Lstat(selectedConfig)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return errors.New("merchant configuration must be a private regular file up to 16 KiB")
	}
	file, err := os.Open(selectedConfig)
	if err != nil {
		return errors.New("merchant configuration unavailable")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("merchant configuration changed while opening")
	}
	var cfg struct {
		Mode      string   `json:"mode"`
		SecretKey string   `json:"secret_key"`
		Countries []string `json:"countries"`
	}
	decoder := json.NewDecoder(io.LimitReader(file, 16385))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cfg) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("invalid merchant configuration JSON")
	}
	selectedMode, err = resolveMerchantWorkerMode(*merchantMode, cfg.Mode)
	if err != nil {
		return err
	}
	constructor := merchantbilling.NewTestClient
	if selectedMode == "live" {
		constructor = merchantbilling.NewLiveClient
	}
	client, err := constructor(cfg.SecretKey, *origin+"/merchant/return", *origin+"/merchant/refresh", cfg.Countries)
	if err != nil {
		return errors.New("invalid merchant configuration")
	}
	defer client.Close()
	// Refuse accidental creation of a new, empty database from a mistyped path.
	dbInfo, err := os.Lstat(*database)
	if err != nil || !dbInfo.Mode().IsRegular() {
		return errors.New("existing portal database required")
	}
	store, err := portal.OpenWithPaymentModes(*database, "test", selectedMode)
	if err != nil {
		return errors.New("portal database unavailable")
	}
	defer store.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var accountCursor, orderCursor, refundCursor string
	for {
		eventDone, eventFailures, eventErr := store.ProcessMerchantEvents(ctx, client, *batch)
		if ctx.Err() != nil {
			return nil
		}
		if eventErr != nil {
			return errors.New("merchant event processing failed; inspect private records")
		}
		result, err := store.MaintainMerchants(ctx, client, accountCursor, orderCursor, *batch)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return errors.New("merchant maintenance failed; inspect private portal records")
		}
		refunds, err := store.MaintainMerchantRefunds(ctx, client, refundCursor, *batch)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return errors.New("merchant refund maintenance failed; inspect private records")
		}
		refundCursor = refunds.Cursor
		accountCursor, orderCursor = result.AccountCursor, result.OrderCursor
		fmt.Printf("Merchant refresh: accounts=%d orders=%d events=%d refunds=%d failures=%d\n", result.AccountsChecked, result.OrdersChecked, eventDone, refunds.Checked, result.Failures+eventFailures+refunds.Failures)
		if *once {
			if result.Failures+eventFailures+refunds.Failures > 0 {
				return errors.New("some merchant updates need retry")
			}
			return nil
		}
		timer := time.NewTimer(*interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func resolveMerchantWorkerSettings(mode, legacyConfig, config string) (string, string, error) {
	if mode != "" && mode != "test" && mode != "live" {
		return "", "", errors.New("merchant mode must be test or live")
	}
	if legacyConfig != "" && config != "" {
		return "", "", errors.New("conflicting merchant configuration flags")
	}
	if mode == "" {
		mode = "test"
	}
	if config == "" {
		config = legacyConfig
	}
	if config == "" {
		return "", "", errors.New("merchant configuration is required")
	}
	if mode == "live" && config == "" {
		return "", "", errors.New("live merchant mode requires merchant configuration")
	}
	return mode, config, nil
}

func resolveMerchantWorkerMode(flagMode, fileMode string) (string, error) {
	for _, mode := range []string{flagMode, fileMode} {
		if mode != "" && mode != "test" && mode != "live" {
			return "", errors.New("merchant mode must be test or live")
		}
	}
	if flagMode != "" && fileMode != "" && flagMode != fileMode {
		return "", errors.New("merchant configuration mode conflicts with flag")
	}
	if flagMode != "" {
		return flagMode, nil
	}
	if fileMode != "" {
		return fileMode, nil
	}
	return "test", nil
}
