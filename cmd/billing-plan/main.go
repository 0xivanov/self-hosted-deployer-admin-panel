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
	flags := flag.NewFlagSet("billing-plan", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	database := flags.String("database", "", "Private customer portal database")
	plan := flags.String("plan", "", "Hosting plan identifier")
	price := flags.String("price", "", "Matching test Stripe Price ID")
	enabled := flags.String("enabled", "", "Explicit true or false")
	if err := flags.Parse(args); err != nil {
		return errors.New("invalid billing plan arguments")
	}
	if flags.NArg() != 0 || *database == "" || *plan == "" || *price == "" || (*enabled != "true" && *enabled != "false") {
		return errors.New("database, plan, price and enabled=true|false are required")
	}
	// Refuse accidental creation of a new database from a mistyped path.
	info, err := os.Lstat(*database)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("an existing private portal database is required")
	}
	store, err := portal.Open(*database)
	if err != nil {
		return errors.New("portal database unavailable")
	}
	defer store.Close()
	if err = store.ConfigureBillingPlan(context.Background(), *plan, *price, *enabled == "true"); err != nil {
		return errors.New("billing plan could not be configured")
	}
	_, err = fmt.Fprintln(out, "Billing plan configuration saved. Existing checkout prices were retained.")
	return err
}
