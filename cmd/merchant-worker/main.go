// merchant-worker refreshes existing test merchant accounts and checkout sessions.
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
	config := flag.String("config", "", "Private test merchant JSON with secret_key and countries")
	origin := flag.String("origin", "", "Exact HTTPS customer portal origin")
	once := flag.Bool("once", false, "Refresh one bounded batch then exit")
	batch := flag.Int("batch", 50, "Maximum accounts and orders per pass, 1 to 100")
	interval := flag.Duration("interval", 30*time.Second, "Pause between passes, 10 seconds to 2 minutes")
	flag.Parse()
	if flag.NArg() != 0 || *database == "" || *config == "" || *batch < 1 || *batch > 100 || *interval < 10*time.Second || *interval > 2*time.Minute {
		return errors.New("database, config, origin and valid batch/interval are required")
	}
	u, err := url.Parse(*origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("origin must be an exact HTTPS origin")
	}
	info, err := os.Lstat(*config)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return errors.New("merchant configuration must be a private regular file up to 16 KiB")
	}
	file, err := os.Open(*config)
	if err != nil {
		return errors.New("merchant configuration unavailable")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("merchant configuration changed while opening")
	}
	var cfg struct {
		SecretKey string   `json:"secret_key"`
		Countries []string `json:"countries"`
	}
	decoder := json.NewDecoder(io.LimitReader(file, 16385))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cfg) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("invalid merchant configuration JSON")
	}
	client, err := merchantbilling.NewTestClient(cfg.SecretKey, *origin+"/merchant/return", *origin+"/merchant/refresh", cfg.Countries)
	if err != nil {
		return errors.New("invalid test merchant configuration")
	}
	defer client.Close()
	// Refuse accidental creation of a new, empty database from a mistyped path.
	dbInfo, err := os.Lstat(*database)
	if err != nil || !dbInfo.Mode().IsRegular() {
		return errors.New("existing portal database required")
	}
	store, err := portal.Open(*database)
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
