package nodeartifact

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"sort"
)

// Export packages a stopped builder's output tree. The trusted controller must
// first stop all customer execution and expose an immutable, read-only snapshot
// through root. This function validates bytes and paths, not VM retirement or
// build provenance. It never follows symlinks or executes build output.
func Export(ctx context.Context, root *os.Root) ([]byte, Manifest, error) {
	if root == nil {
		return nil, Manifest{}, ErrArtifact
	}
	output := &boundedArchive{}
	z := zip.NewWriter(output)
	entries := 0
	var expanded int64
	var walk func(string) error
	walk = func(directory string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := root.Open(directory)
		if err != nil {
			return err
		}
		// Bound directory enumeration before sorting; a malicious directory may
		// contain far more entries than we can retain in the final archive.
		children, readErr := f.ReadDir(MaxEntries - entries + 1)
		closeErr := f.Close()
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if len(children) > MaxEntries-entries {
			return ErrArtifact
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			if err = ctx.Err(); err != nil {
				return err
			}
			name := path.Join(directory, child.Name())
			entries++
			if entries > MaxEntries || !safeName(name) {
				return ErrArtifact
			}
			info, err := root.Lstat(name)
			if err != nil {
				return err
			}
			kind := info.Mode().Type()
			if kind != 0 && kind != os.ModeDir && kind != os.ModeSymlink {
				return ErrArtifact
			}
			header := &zip.FileHeader{Name: name, Method: zip.Deflate}
			switch kind {
			case os.ModeDir:
				header.Name += "/"
				header.SetMode(os.ModeDir | 0755)
			case os.ModeSymlink:
				header.SetMode(os.ModeSymlink | 0777)
			default:
				if info.Size() < 0 || info.Size() > MaxFile || expanded+info.Size() > MaxExpanded {
					return ErrArtifact
				}
				mode := os.FileMode(0644)
				if info.Mode().Perm()&0111 != 0 {
					mode = 0755
				}
				header.SetMode(mode)
			}
			w, err := z.CreateHeader(header)
			if err != nil {
				return err
			}
			switch kind {
			case os.ModeDir:
				if err = walk(name); err != nil {
					return err
				}
			case os.ModeSymlink:
				target, err := root.Readlink(name)
				if err != nil {
					return err
				}
				if !safeTarget(target) || expanded+int64(len(target)) > MaxExpanded {
					return ErrArtifact
				}
				if _, err = io.WriteString(w, target); err != nil {
					return err
				}
				expanded += int64(len(target))
			default:
				file, err := root.Open(name)
				if err != nil {
					return err
				}
				actual, err := file.Stat()
				if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) || actual.Size() != info.Size() {
					file.Close()
					return ErrArtifact
				}
				n, copyErr := io.Copy(w, io.LimitReader(contextReader{ctx, file}, info.Size()+1))
				closeErr := file.Close()
				if copyErr != nil || closeErr != nil {
					return errors.Join(copyErr, closeErr)
				}
				if n != info.Size() {
					return ErrArtifact
				}
				expanded += n
			}
		}
		return nil
	}
	if err := walk("."); err != nil {
		return nil, Manifest{}, err
	}
	if err := z.Close(); err != nil {
		return nil, Manifest{}, err
	}
	data := output.Bytes()
	sum := sha256.Sum256(data)
	manifest, err := Validate(ctx, data, hex.EncodeToString(sum[:]))
	if err != nil {
		return nil, Manifest{}, err
	}
	return data, manifest, nil
}

type boundedArchive struct{ bytes.Buffer }

func (b *boundedArchive) Write(p []byte) (int, error) {
	if len(p) > MaxCompressed-b.Len() {
		return 0, ErrArtifact
	}
	return b.Buffer.Write(p)
}
