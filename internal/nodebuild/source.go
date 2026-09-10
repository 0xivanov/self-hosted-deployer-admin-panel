package nodebuild

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/projectarchive"
)

// Source is a completed private directory relative to the trusted job root.
// Callers own cleanup after use; nothing in the directory has been executed.
type Source struct {
	Directory string
	SHA256    string
}

// ExtractSource requires an operator-owned private root in the builder. It
// revalidates source identity before writing, creates a fresh directory, and
// confines all writes and cleanup to root handles. It must finish before any
// customer process can access the job root. The caller must retain the root and
// must not expose this path as an HTTP static directory.
func ExtractSource(ctx context.Context, source []byte, expectedSHA256 string, jobs *os.Root) (result Source, err error) {
	if jobs == nil {
		return result, errors.New("private build job root is required")
	}
	info, err := jobs.Stat(".")
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return result, errors.New("build job root must be private")
	}
	manifest, err := projectarchive.Validate(ctx, source, "node")
	if err != nil {
		return result, err
	}
	if manifest.SHA256 != expectedSHA256 {
		return result, errors.New("Node source digest does not match the assigned upload")
	}
	archive, err := zip.NewReader(bytes.NewReader(source), int64(len(source)))
	if err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	var entropy [32]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		return result, err
	}
	name := "source-" + hex.EncodeToString(entropy[:])
	if err = jobs.Mkdir(name, 0700); err != nil {
		return result, err
	}
	complete := false
	defer func() {
		if !complete {
			err = errors.Join(err, jobs.RemoveAll(name))
			result = Source{}
		}
	}()
	root, err := jobs.OpenRoot(name)
	if err != nil {
		return result, err
	}
	defer root.Close()
	for _, file := range archive.File {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		if file.FileInfo().IsDir() {
			if err = root.MkdirAll(file.Name, 0700); err != nil {
				return result, err
			}
			continue
		}
		if err = root.MkdirAll(path.Dir(file.Name), 0700); err != nil {
			return result, err
		}
		mode := os.FileMode(0600)
		// Preserve only owner executability for uploaded helper scripts. No setuid,
		// setgid, group/other permissions or archive ownership is imported.
		if file.Mode().Perm()&0111 != 0 {
			mode = 0700
		}
		out, e := root.OpenFile(file.Name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if e != nil {
			return result, e
		}
		in, e := file.Open()
		if e != nil {
			out.Close()
			return result, e
		}
		count, copyErr := io.Copy(out, io.LimitReader(contextReader{ctx, in}, projectarchive.MaxFile+1))
		readErr := in.Close()
		closeErr := out.Close()
		if e = errors.Join(copyErr, readErr, closeErr); e != nil {
			return result, e
		}
		if count > projectarchive.MaxFile {
			return result, errors.New("expanded source file exceeds limit")
		}
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	complete = true
	return Source{Directory: name, SHA256: manifest.SHA256}, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
