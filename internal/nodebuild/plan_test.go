package nodebuild

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

func archiveFixture(t *testing.T, pkg string) ([]byte, string) {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, f := range []struct{ name, body string }{{"package.json", pkg}, {"package-lock.json", `{"lockfileVersion":3}`}, {"server.js", "throw new Error('must not execute during planning')"}} {
		w, err := z.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b.Bytes())
	return b.Bytes(), hex.EncodeToString(sum[:])
}
func TestPrepareNodeBuild(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, pkg string
		skip      bool
		steps     int
	}{
		{"server", `{"scripts":{"start":"node server.js"}}`, false, 2},
		{"compiled app", `{"scripts":{"start":"node dist/server.js","build":"tsc"}}`, false, 3},
		{"explicit skip", `{"scripts":{"start":"node server.js","build":"exit 99"}}`, true, 2},
		{"untrusted hooks", `{"scripts":{"start":"$(exit 99)","preinstall":"exit 99","build":"exit 99"}}`, false, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, digest := archiveFixture(t, tc.pkg)
			p, err := Prepare(t.Context(), source, digest, Settings{Architecture: "arm64", SkipBuild: tc.skip})
			if err != nil {
				t.Fatal(err)
			}
			if p.SourceSHA256 != digest || p.Architecture != "arm64" || p.NodeMajor != 24 || len(p.Steps) != tc.steps || p.Start.Program != "npm" {
				t.Fatal(p)
			}
			// Script text belongs to package.json only, never generated shell/arguments.
			for _, step := range append(p.Steps, p.Start) {
				for _, arg := range step.Args {
					if arg == "exit 99" || arg == "$(exit 99)" {
						t.Fatal("script interpolated into worker arguments")
					}
				}
			}
		})
	}
}
func TestPrepareRejectsUnassignedOrUnsupportedSource(t *testing.T) {
	t.Parallel()
	source, digest := archiveFixture(t, `{"scripts":{"start":"node server.js"}}`)
	if _, err := Prepare(t.Context(), source, "", Settings{Architecture: "amd64"}); err == nil {
		t.Fatal("unassigned upload accepted")
	}
	if _, err := Prepare(t.Context(), source, digest, Settings{Architecture: "host"}); !errors.Is(err, ErrPlan) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Prepare(ctx, source, digest, Settings{Architecture: "amd64"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	source, digest = archiveFixture(t, `{"packageManager":"pnpm@10.0.0","scripts":{"start":"node server.js"}}`)
	if _, err := Prepare(t.Context(), source, digest, Settings{Architecture: "amd64"}); err == nil {
		t.Fatal("unsupported package manager accepted")
	}
}
