package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/namesilo"
)

type sandboxRegistrar struct {
	reader                     *namesilo.Client
	writer                     *namesilo.SandboxWriter
	contact, directory, target string
}

func (r *sandboxRegistrar) Register(ctx context.Context, id, name string) error {
	if len(id) != 64 || strings.ContainsAny(id, "/\\.") {
		return errors.New("invalid sandbox order")
	}
	result, err := r.writer.ExecuteOnce(ctx, filepath.Join(r.directory, id+".jsonl"), namesilo.SandboxAttempt{Operation: "registerDomain", Domain: name, ContactID: r.contact})
	if err != nil {
		return err
	}
	if result.ReviewRequired {
		return errors.New("registrar fallback needs review")
	}
	info, err := r.reader.GetDomainInfo(ctx, name)
	if err != nil || info.RegistrantContactID != r.contact {
		return errors.New("sandbox ownership not confirmed")
	}
	return nil
}
func (r *sandboxRegistrar) Connect(ctx context.Context, id, name string) error {
	records, err := r.reader.DNSRecords(ctx, name)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.Type == "A" && (record.Host == name || record.Host == "@" || record.Host == "") {
			if record.Value == r.target {
				return nil
			}
			return errors.New("sandbox DNS conflict")
		}
	}
	// Persist DNS dispatch intent too. A lost response can be resolved by the read
	// above, but is never blindly submitted again after a restart.
	path := filepath.Join(r.directory, id+"-dns")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("sandbox DNS outcome needs review")
	}
	err = f.Sync()
	f.Close()
	if err != nil {
		return err
	}
	dir, err := os.Open(r.directory)
	if err != nil {
		return err
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return err
	}
	_, err = r.writer.AddDNSRecord(ctx, name, namesilo.DNSRecord{Type: "A", Host: "", Value: r.target, TTL: 3600})
	if err != nil {
		return err
	}
	records, err = r.reader.DNSRecords(ctx, name)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.Type == "A" && (record.Host == name || record.Host == "@" || record.Host == "") && record.Value == r.target {
			return nil
		}
	}
	return errors.New("sandbox DNS not confirmed")
}
