package githubdeploy

import (
	"archive/zip"
	"bytes"
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/projectarchive"
)

type archiveEntry struct {
	name, body string
	mode       fs.FileMode
}

func repositoryZIP(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func TestPrepareRepositoryArchive(t *testing.T) {
	data := repositoryZIP(t, archiveEntry{"repo-commit/README.md", "outside", 0}, archiveEntry{"repo-commit/site/index.html", "hello", 0}, archiveEntry{"repo-commit/site/style.css", "body{}", 0})
	out, m, err := PrepareArchive(context.Background(), data, "site", "static")
	if err != nil || m.Files != 2 || m.Bytes != 11 {
		t.Fatalf("manifest %+v: %v", m, err)
	}
	checked, err := projectarchive.Validate(context.Background(), out, "static")
	if err != nil || checked != m {
		t.Fatal("normalized archive differs from ordinary upload validation", err)
	}
}
func TestPrepareRepositoryArchiveRejectsUnsafeInput(t *testing.T) {
	cases := map[string][]archiveEntry{
		"multiple roots": {{"one/index.html", "ok", 0}, {"two/extra", "no", 0}},
		"traversal":      {{"repo/../index.html", "no", 0}},
		"symlink":        {{"repo/index.html", "secret", fs.ModeSymlink | 0777}},
		"case collision": {{"repo/index.html", "ok", 0}, {"repo/INDEX.HTML", "no", 0}},
		"secrets":        {{"repo/index.html", "ok", 0}, {"repo/.env", "no", 0}},
		"expanded limit": {{"repo/index.html", strings.Repeat("x", projectarchive.MaxFile+1), 0}},
		"not enclosed":   {{"index.html", "no", 0}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := PrepareArchive(context.Background(), repositoryZIP(t, entries...), "", "static"); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
	data := repositoryZIP(t, archiveEntry{"repo/index.html", "ok", 0})
	for _, dir := range []string{"../repo", "/repo", "missing", "site\\nested"} {
		if _, _, err := PrepareArchive(context.Background(), data, dir, "static"); err == nil {
			t.Fatalf("directory %q accepted", dir)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := PrepareArchive(ctx, data, "", "static"); err != context.Canceled {
		t.Fatal(err)
	}
}
func TestPrepareNodeRepositoryArchive(t *testing.T) {
	data := repositoryZIP(t, archiveEntry{"repo/package.json", `{"scripts":{"start":"node app.js"}}`, 0}, archiveEntry{"repo/package-lock.json", `{"lockfileVersion":3}`, 0}, archiveEntry{"repo/app.js", "", 0})
	_, m, err := PrepareArchive(context.Background(), data, ".", "node")
	if err != nil || m.Files != 3 {
		t.Fatalf("%+v %v", m, err)
	}
}
