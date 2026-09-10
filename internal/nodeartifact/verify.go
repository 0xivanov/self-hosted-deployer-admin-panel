package nodeartifact

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
)

// VerifySealed checks every installed path, byte, symlink target and permission
// against the trusted archive. Extra/missing paths and writable files are denied.
// The root and tree must be protected from concurrent mutation by the trusted
// runtime agent. This is not an ownership, durability or process-isolation check.
func VerifySealed(ctx context.Context, data []byte, expected string, root *os.Root) (Manifest, error) {
	if root == nil {
		return Manifest{}, ErrArtifact
	}
	manifest, entries, err := inspect(ctx, data, expected)
	if err != nil {
		return Manifest{}, err
	}
	seen := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if name == "." {
			if !info.IsDir() || info.Mode().Perm() != 0555 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
				return ErrArtifact
			}
			return nil
		}
		item, ok := entries[name]
		if !ok || info.Mode().Type() != item.kind {
			return ErrArtifact
		}
		seen++
		if item.kind == os.ModeSymlink {
			target, err := root.Readlink(name)
			if err != nil {
				return err
			}
			if target != item.target {
				return ErrArtifact
			}
			return nil
		}
		permission := os.FileMode(0444)
		if item.kind == os.ModeDir || item.file.Mode().Perm()&0111 != 0 {
			permission = 0555
		}
		if info.Mode().Perm() != permission || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return ErrArtifact
		}
		if item.kind == os.ModeDir {
			return nil
		}
		if info.Size() != int64(item.file.UncompressedSize64) {
			return ErrArtifact
		}
		installed, err := root.Open(name)
		if err != nil {
			return err
		}
		actual := sha256.New()
		n, readErr := io.Copy(actual, io.LimitReader(contextReader{ctx, installed}, MaxFile+1))
		if err = errors.Join(readErr, installed.Close()); err != nil {
			return err
		}
		if n != info.Size() {
			return ErrArtifact
		}
		original, err := item.file.Open()
		if err != nil {
			return err
		}
		wanted := sha256.New()
		_, readErr = io.Copy(wanted, contextReader{ctx, original})
		if err = errors.Join(readErr, original.Close()); err != nil {
			return err
		}
		if string(actual.Sum(nil)) != string(wanted.Sum(nil)) {
			return ErrArtifact
		}
		return nil
	})
	if err != nil {
		return Manifest{}, err
	}
	if seen != len(entries) {
		return Manifest{}, ErrArtifact
	}
	return manifest, nil
}
