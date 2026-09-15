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
	workspace := flags.String("workspace", "", "Workspace identifier")
	required := flags.String("require-test-subscription", "", "Explicit true or false")
	if err := flags.Parse(args); err != nil {
		return errors.New("invalid hosting policy arguments")
	}
	if flags.NArg() != 0 || *database == "" || *workspace == "" || (*required != "true" && *required != "false") {
		return errors.New("database, workspace and require-test-subscription=true|false are required")
	}
	info, err := os.Lstat(*database)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("an existing private portal database is required")
	}
	store, err := portal.Open(*database)
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
