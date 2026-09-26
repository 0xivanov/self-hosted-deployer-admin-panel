// namesilo-sandbox is an operator-only OTE exercise. It is never a production
// payment worker and deliberately has no endpoint or environment override.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/namesilo"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	config := flag.String("config", "", "private JSON containing secret_key and optional contact_id")
	operation := flag.String("operation", "", "registerDomain or renewDomain (sandbox only)")
	domain := flag.String("domain", "", "exact .com/.net sandbox domain")
	attempt := flag.String("attempt-file", "", "absolute persistent attempt file in a private existing directory")
	apply := flag.Bool("apply", false, "dispatch the sandbox mutation")
	flag.Parse()
	if !*apply || *domain == "" || !filepath.IsAbs(*attempt) {
		return errors.New("specify --apply, --domain and an absolute --attempt-file; OTE only")
	}
	parent, err := os.Stat(filepath.Dir(*attempt))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return errors.New("attempt directory must be private (0700)")
	}
	info, err := os.Lstat(*config)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return errors.New("configuration must be a private regular file")
	}
	f, err := os.Open(*config)
	if err != nil {
		return errors.New("configuration unavailable")
	}
	defer f.Close()
	var cfg struct {
		Key     string `json:"secret_key"`
		Contact string `json:"contact_id"`
	}
	dec := json.NewDecoder(io.LimitReader(f, 16385))
	dec.DisallowUnknownFields()
	if dec.Decode(&cfg) != nil || dec.Decode(new(any)) != io.EOF {
		return errors.New("invalid configuration")
	}
	writer, err := namesilo.NewSandboxWriter(cfg.Key)
	if err != nil {
		return err
	}
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	request := namesilo.SandboxAttempt{Operation: *operation, Domain: *domain}
	if *operation == "registerDomain" {
		request.ContactID = cfg.Contact
	}
	result, err := writer.ExecuteOnce(ctx, *attempt, request)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
