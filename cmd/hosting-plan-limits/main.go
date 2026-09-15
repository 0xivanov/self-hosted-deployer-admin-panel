package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
	"io"
	"os"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("hosting-plan-limits", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	database := flags.String("database", "", "Existing private portal database")
	plan := flags.String("plan", "", "Configured hosting plan")
	projects := flags.Int64("projects", 0, "Maximum projects, 1 to 100")
	uploads := flags.Int64("uploads", 0, "Maximum retained source ZIPs, 1 to 20")
	storage := flags.Int64("upload-mib", 0, "Maximum source ZIP storage, 1 to 100 MiB")
	node := flags.String("node", "", "Explicit true or false")
	if err := flags.Parse(args); err != nil {
		return errors.New("invalid hosting limit arguments")
	}
	if flags.NArg() != 0 || *database == "" || *plan == "" || *projects < 1 || *projects > 100 || *uploads < 1 || *uploads > 20 || *storage < 1 || *storage > 100 || (*node != "true" && *node != "false") {
		return errors.New("database, plan, projects=1..100, uploads=1..20, upload-mib=1..100 and node=true|false are required")
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
	if err = store.ConfigureHostingLimits(context.Background(), *plan, portal.HostingPlanLimits{Projects: *projects, Uploads: *uploads, UploadBytes: *storage << 20, Node: *node == "true"}); err != nil {
		return errors.New("hosting plan limits could not be configured")
	}
	_, err = fmt.Fprintln(out, "Hosting plan limits saved for future checkouts. Existing checkout allowances were retained.")
	return err
}
