package githubdeploy

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/projectarchive"
)

var ErrArchive = errors.New("repository archive is invalid or exceeds project limits")

// PrepareArchive removes GitHub's single enclosing directory and selects an
// optional repository subdirectory. It never extracts files or runs repository
// code. The output passes the same validation as an ordinary customer upload.
// The caller must first authenticate repository access and fetch an exact commit;
// archive contents and their directory name do not prove repository identity.
func PrepareArchive(ctx context.Context, data []byte, directory, kind string) ([]byte, projectarchive.Manifest, error) {
	var empty projectarchive.Manifest
	if err := ctx.Err(); err != nil {
		return nil, empty, err
	}
	if kind != "static" && kind != "node" {
		return nil, empty, ErrArchive
	}
	if directory == "." {
		directory = ""
	}
	if directory != "" && !archivePath(directory) {
		return nil, empty, ErrArchive
	}
	if len(data) == 0 || len(data) > projectarchive.MaxCompressed {
		return nil, empty, ErrArchive
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(archive.File) == 0 || len(archive.File) > projectarchive.MaxEntries {
		return nil, empty, ErrArchive
	}
	var out boundedArchive
	writer := zip.NewWriter(&out)
	root := ""
	var expanded int64
	for _, file := range archive.File {
		if err := ctx.Err(); err != nil {
			return nil, empty, err
		}
		name := strings.TrimSuffix(file.Name, "/")
		if !archivePath(name) {
			return nil, empty, ErrArchive
		}
		parts := strings.SplitN(name, "/", 2)
		if root == "" {
			root = parts[0]
		}
		if root != parts[0] {
			return nil, empty, ErrArchive
		}
		if len(parts) == 1 {
			if !file.Mode().IsDir() {
				return nil, empty, ErrArchive
			}
			continue
		}
		relative := parts[1]
		if directory != "" {
			if relative == directory && file.Mode().IsDir() {
				continue
			}
			if !strings.HasPrefix(relative, directory+"/") {
				continue
			}
			relative = strings.TrimPrefix(relative, directory+"/")
		}
		if file.Flags&1 != 0 || (!file.Mode().IsDir() && !file.Mode().IsRegular()) {
			return nil, empty, ErrArchive
		}
		header := &zip.FileHeader{Name: relative, Method: zip.Deflate}
		if file.Mode().IsDir() {
			header.Name += "/"
			header.SetMode(fs.ModeDir | 0755)
		} else {
			header.SetMode(0644)
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return nil, empty, ErrArchive
		}
		if file.Mode().IsDir() {
			continue
		}
		if file.UncompressedSize64 > projectarchive.MaxFile {
			return nil, empty, ErrArchive
		}
		reader, err := file.Open()
		if err != nil {
			return nil, empty, ErrArchive
		}
		n, copyErr := io.Copy(entry, io.LimitReader(contextArchiveReader{ctx, reader}, projectarchive.MaxFile+1))
		closeErr := reader.Close()
		if err := ctx.Err(); err != nil {
			return nil, empty, err
		}
		expanded += n
		if copyErr != nil || closeErr != nil || n > projectarchive.MaxFile || expanded > projectarchive.MaxExpanded {
			return nil, empty, ErrArchive
		}
	}
	if err := writer.Close(); err != nil {
		return nil, empty, ErrArchive
	}
	result := out.Bytes()
	manifest, err := projectarchive.Validate(ctx, result, kind)
	if err != nil {
		return nil, empty, err
	}
	return result, manifest, nil
}

func archivePath(name string) bool {
	return name != "." && len(name) <= 2048 && utf8.ValidString(name) && fs.ValidPath(name) && !strings.ContainsAny(name, "\\:") && !strings.ContainsFunc(name, unicode.IsControl)
}

type boundedArchive struct{ bytes.Buffer }

func (w *boundedArchive) Write(p []byte) (int, error) {
	if len(p) > projectarchive.MaxCompressed-w.Len() {
		return 0, ErrArchive
	}
	return w.Buffer.Write(p)
}

type contextArchiveReader struct {
	ctx context.Context
	io.Reader
}

func (r contextArchiveReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}
