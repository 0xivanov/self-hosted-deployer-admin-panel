//go:build linux && integration

package nodebuild

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/npmfetch"
)

type noGuestDependencies struct{}

func (noGuestDependencies) Fetch(context.Context, npmfetch.Tarball) ([]byte, error) {
	panic("unexpected dependency")
}

func TestGuestOfflineBuild(t *testing.T) {
	if os.Getenv("NODE_GUEST_TEST") != "1" {
		t.Skip("requires disposable Linux VM with isolated unprivileged service")
	}
	const pin = "5f4ddab610c1ab2016b3c227cebdbf6d9495161487e4739c7b90090595f465f7"
	var data bytes.Buffer
	z := zip.NewWriter(&data)
	for _, file := range []struct{ name, body string }{
		{"package.json", `{"name":"guest-fixture","version":"1.0.0","scripts":{"start":"node server.js","build":"node build.js"}}`},
		{"package-lock.json", `{"name":"guest-fixture","version":"1.0.0","lockfileVersion":3,"packages":{"":{"name":"guest-fixture","version":"1.0.0"}}}`},
		{"server.js", `console.log('fixture');`},
		{"build.js", `require('fs').writeFileSync('built.txt','offline guest works');`},
	} {
		w, err := z.Create(file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(file.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data.Bytes())
	sha := hex.EncodeToString(sum[:])
	plan, err := Prepare(t.Context(), data.Bytes(), sha, Settings{Architecture: "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	input := t.TempDir()
	if err = os.Chmod(input, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(input)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	bundle, err := npmfetch.DownloadBundle(t.Context(), data.Bytes(), sha, root, noGuestDependencies{})
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(input, "source.zip")
	if err = os.WriteFile(archive, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err = os.Chmod(work, 0700); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	source, err := RunGuest(t.Context(), GuestRequest{Plan: plan, ToolchainSHA256: pin, SourcePath: archive, DependenciesPath: input, Bundle: bundle, WorkDirectory: work, NotAfter: time.Now().Add(55 * time.Second).Unix()}, &logs)
	if err != nil {
		t.Fatalf("%v: %s", err, logs.String())
	}
	built, err := os.ReadFile(filepath.Join(work, source.Directory, "built.txt"))
	if err != nil || string(built) != "offline guest works" {
		t.Fatal(string(built), err)
	}
}
