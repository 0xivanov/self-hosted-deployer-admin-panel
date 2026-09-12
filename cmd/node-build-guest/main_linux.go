//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Guest build failed:", err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) != 2 {
		return errors.New("usage: node-build-guest PRIVATE_REQUEST_JSON (isolated guest only)")
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return errors.New("private request file required")
	}
	decoder := json.NewDecoder(io.LimitReader(f, 16385))
	decoder.DisallowUnknownFields()
	var request nodebuild.GuestRequest
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("invalid guest request")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	source, err := nodebuild.RunGuest(ctx, request, os.Stderr)
	if err != nil {
		return err
	}
	// This identifies the candidate build tree only. It is not trusted success,
	// retirement evidence or permission to activate a customer deployment.
	return json.NewEncoder(os.Stdout).Encode(source)
}
