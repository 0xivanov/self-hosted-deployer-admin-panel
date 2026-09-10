// node-build-plan validates a source ZIP and prints instructions. It does not
// install dependencies, extract files, or run a build.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/projectarchive"
)

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("node-build-plan", flag.ContinueOnError)
	flags.SetOutput(errOut)
	source := flags.String("source", "", "source ZIP path")
	digest := flags.String("sha256", "", "expected uploaded source SHA-256")
	architecture := flags.String("architecture", "", "assigned Linux runtime architecture: amd64 or arm64")
	skip := flags.Bool("skip-build", false, "skip the optional package.json build script")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *source == "" || *digest == "" || *architecture == "" {
		return fmt.Errorf("source, sha256 and architecture are required")
	}
	f, err := os.Open(*source)
	if err != nil {
		return fmt.Errorf("source ZIP could not be opened")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > projectarchive.MaxCompressed {
		return fmt.Errorf("source must be a regular ZIP file no larger than 10 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, projectarchive.MaxCompressed+1))
	if err != nil {
		return fmt.Errorf("source ZIP could not be read")
	}
	plan, err := nodebuild.Prepare(ctx, data, *digest, nodebuild.Settings{Architecture: *architecture, SkipBuild: *skip})
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(plan)
}
func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
