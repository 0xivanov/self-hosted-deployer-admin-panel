package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("hosting-policy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	database := flags.String("database", "", "Private customer portal database")
	billingMode := flags.String("billing-mode", "test", "Billing entitlement mode: test or live")
	workspace := flags.String("workspace", "", "Workspace identifier")
	legacyRequired := flags.String("require-test-subscription", "", "Legacy test-mode alias: explicit true or false")
	required := flags.String("require-subscription", "", "Explicit true or false")
	if err := flags.Parse(args); err != nil {
		return errors.New("invalid hosting policy arguments")
	}
	if *billingMode != "test" && *billingMode != "live" {
		return errors.New("billing-mode must be test or live")
	}
	legacySet := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "require-test-subscription" {
			legacySet = true
		}
	})
	currentSet := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "require-subscription" {
			currentSet = true
		}
	})
	if legacySet && *billingMode != "test" {
		return errors.New("require-test-subscription is only valid in test billing mode")
	}
	if legacySet && currentSet && *legacyRequired != *required {
		return errors.New("require-subscription flags conflict")
	}
	if !currentSet && legacySet {
		*required = *legacyRequired
	}
	if flags.NArg() != 0 || *database == "" || *workspace == "" || (*required != "true" && *required != "false") {
		return errors.New("database, workspace and require-subscription=true|false are required")
	}
	info, err := os.Lstat(*database)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("an existing private portal database is required")
	}
	store, err := portal.OpenWithBillingMode(*database, *billingMode)
	if err != nil {
		return errors.New("portal database unavailable")
	}
	defer store.Close()
	if err = store.ConfigureHostingPolicy(context.Background(), *workspace, *required == "true"); err != nil {
		return errors.New("hosting policy could not be configured")
	}
	_, err = fmt.Fprintln(out, "Hosting access policy saved.")
	return err
}
