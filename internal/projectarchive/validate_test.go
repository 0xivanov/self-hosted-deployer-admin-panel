package projectarchive

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

type entry struct {
	name, body string
	mode       os.FileMode
}

func archive(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, item := range entries {
		header := &zip.FileHeader{Name: item.name, Method: zip.Deflate}
		if item.mode != 0 {
			header.SetMode(item.mode)
		}
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = file.Write([]byte(item.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
func TestValidProjects(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		files      []entry
	}{
		{"static", "static", []entry{{name: "index.html", body: "<h1>Hello</h1>"}, {name: "assets/style.css", body: "body{}"}}},
		{"node", "node", []entry{{name: "package.json", body: `{"scripts":{"start":"node server.js"}}`}, {name: "package-lock.json", body: `{"lockfileVersion":3}`}, {name: "server.js", body: "process.exit(0)"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Validate(context.Background(), archive(t, tc.files...), tc.kind)
			if err != nil || m.Files != len(tc.files) || len(m.SHA256) != 64 {
				t.Fatal(m, err)
			}
		})
	}
}
func TestRejectUnsafeArchives(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []entry
	}{
		{"traversal", []entry{{name: "../outside", body: "x"}}},
		{"absolute", []entry{{name: "/outside", body: "x"}}},
		{"windows path", []entry{{name: "C:\\outside", body: "x"}}},
		{"symlink", []entry{{name: "link", body: "/etc/passwd", mode: os.ModeSymlink | 0777}}},
		{"secret", []entry{{name: "src/.env.production", body: "SECRET=x"}}},
		{"case collision", []entry{{name: "index.html", body: "one"}, {name: "INDEX.html", body: "two"}}},
		{"parent file", []entry{{name: "a", body: "x"}, {name: "a/b", body: "x"}}},
		{"dependencies", []entry{{name: "node_modules/library/index.js", body: "x"}}},
		{"expansion", []entry{{name: "index.html", body: strings.Repeat("x", MaxFile+1)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Validate(context.Background(), archive(t, tc.files...), "static"); err == nil {
				t.Fatal("accepted unsafe archive")
			}
		})
	}
}
func TestMissingContractsAndCancellation(t *testing.T) {
	for _, kind := range []string{"static", "node", "invalid"} {
		t.Run(kind, func(t *testing.T) {
			if _, err := Validate(context.Background(), archive(t, entry{name: "README.md", body: "project"}), kind); err == nil {
				t.Fatal("accepted incomplete project")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Validate(ctx, archive(t, entry{name: "index.html", body: "hello"}), "static"); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestTotalExpandedLimit(t *testing.T) {
	files := []entry{{name: "index.html", body: "hello"}}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		files = append(files, entry{name: name, body: strings.Repeat("x", 7<<20)})
	}
	if _, err := Validate(context.Background(), archive(t, files...), "static"); err == nil {
		t.Fatal("accepted excessive total expansion")
	}
}
func TestCorruptionAndEntryCount(t *testing.T) {
	data := archive(t, entry{name: "index.html", body: "hello"})
	if _, err := Validate(context.Background(), data[:len(data)-10], "static"); err == nil {
		t.Fatal("accepted truncated archive")
	}
	files := make([]entry, MaxEntries+1)
	for i := range files {
		files[i] = entry{name: strings.Repeat("a", i%20+1) + string(rune(0x100+i)), body: "x"}
	}
	if _, err := Validate(context.Background(), archive(t, files...), "static"); err == nil {
		t.Fatal("accepted excessive entries")
	}
}
