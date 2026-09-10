package nodeartifact

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"sort"
)

type Release struct {
	Directory string
	Manifest  Manifest
}

// Extract stages a new private release, without starting it or changing any
// active release pointer. The assigned root must be operator-controlled and
// inaccessible to running customer code. Callers own retained release cleanup.
func Extract(ctx context.Context, data []byte, expected string, releases *os.Root) (result Release, err error) {
	if releases == nil {
		return result, ErrArtifact
	}
	info, err := releases.Stat(".")
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return result, ErrArtifact
	}
	manifest, entries, err := inspect(ctx, data, expected)
	if err != nil {
		return result, err
	}
	var entropy [32]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		return result, err
	}
	name := "release-" + hex.EncodeToString(entropy[:])
	if err = releases.Mkdir(name, 0700); err != nil {
		return result, err
	}
	complete := false
	defer func() {
		if !complete {
			err = errors.Join(err, releases.RemoveAll(name))
			result = Release{}
		}
	}()
	root, err := releases.OpenRoot(name)
	if err != nil {
		return result, err
	}
	defer root.Close()
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		item := entries[n]
		if item.kind == os.ModeSymlink {
			continue
		}
		if item.kind == os.ModeDir {
			if err = root.MkdirAll(n, 0700); err != nil {
				return result, err
			}
			continue
		}
		if err = root.MkdirAll(path.Dir(n), 0700); err != nil {
			return result, err
		}
		mode := os.FileMode(0600)
		if item.file.Mode().Perm()&0111 != 0 {
			mode = 0700
		}
		out, e := root.OpenFile(n, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if e != nil {
			return result, e
		}
		in, e := item.file.Open()
		if e != nil {
			out.Close()
			return result, e
		}
		count, copyErr := io.Copy(out, io.LimitReader(contextReader{ctx, in}, MaxFile+1))
		readErr := in.Close()
		syncErr := out.Sync()
		closeErr := out.Close()
		if e = errors.Join(copyErr, readErr, syncErr, closeErr); e != nil {
			return result, e
		}
		if count != int64(item.file.UncompressedSize64) {
			return result, ErrArtifact
		}
	}
	// Links are created only after all regular files, so they can never redirect
	// writes. Validation allows only internal links ending at regular files.
	for _, n := range names {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		if item := entries[n]; item.kind == os.ModeSymlink {
			if err = root.Symlink(item.target, n); err != nil {
				return result, err
			}
		}
	}
	// Sync deepest directories before their parents. An active pointer is the
	// responsibility of a later health-checked activation transaction.
	for i := len(names) - 1; i >= 0; i-- {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		if entries[names[i]].kind == os.ModeDir {
			if err = syncDirectory(root, names[i]); err != nil {
				return result, err
			}
		}
	}
	if err = syncDirectory(root, "."); err != nil {
		return result, err
	}
	if err = syncDirectory(releases, "."); err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	complete = true
	return Release{Directory: name, Manifest: manifest}, nil
}
func syncDirectory(root *os.Root, name string) error {
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
