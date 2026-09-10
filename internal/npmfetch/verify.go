package npmfetch

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strings"
)

// VerifyBundle reopens a completed bundle using identities retained by the trusted
// worker. It reads every file through a confined root, rejects symlinks/extra files,
// and verifies both hashes. The caller must keep the directory immutable until
// consumption; verification does not grant permission to execute packages.
func VerifyBundle(ctx context.Context, jobs *os.Root, bundle Bundle, sourceSHA256 string) (Manifest, error) {
	fail := func() (Manifest, error) { return Manifest{}, ErrBundle }
	if jobs == nil || !hexID(sourceSHA256) || !hexID(bundle.ManifestSHA256) || !strings.HasPrefix(bundle.Directory, "dependencies-") || !hexID(strings.TrimPrefix(bundle.Directory, "dependencies-")) {
		return fail()
	}
	parent, err := jobs.Stat(".")
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return fail()
	}
	info, err := jobs.Lstat(bundle.Directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fail()
	}
	root, err := jobs.OpenRoot(bundle.Directory)
	if err != nil {
		return fail()
	}
	defer root.Close()
	raw, err := readPrivate(root, "bundle.json", 4<<20)
	if err != nil {
		return fail()
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != bundle.ManifestSHA256 {
		return fail()
	}
	var manifest Manifest
	if json.Unmarshal(raw, &manifest) != nil || manifest.SourceSHA256 != sourceSHA256 || manifest.Tarballs == nil || len(manifest.Tarballs) > MaxTarballs {
		return fail()
	}
	files := map[string]bool{"bundle.json": true}
	urls := map[string]bool{}
	total := int64(len(raw))
	for _, item := range manifest.Tarballs {
		if err = ctx.Err(); err != nil {
			return Manifest{}, err
		}
		if !validTarball(Tarball{item.URL, item.Integrity}) || urls[item.URL] || !strings.HasSuffix(item.File, ".tgz") || !hexID(strings.TrimSuffix(item.File, ".tgz")) || item.Bytes < 0 || item.Bytes > MaxTarballBytes {
			return fail()
		}
		urls[item.URL] = true
		data, e := readPrivate(root, item.File, MaxTarballBytes)
		if e != nil || int64(len(data)) != item.Bytes {
			return fail()
		}
		hash := sha256.Sum256(data)
		sri := sha512.Sum512(data)
		if hex.EncodeToString(hash[:])+".tgz" != item.File || "sha512-"+base64.StdEncoding.EncodeToString(sri[:]) != item.Integrity {
			return fail()
		}
		if !files[item.File] {
			total += item.Bytes
			files[item.File] = true
		}
		if total > MaxBundleBytes {
			return fail()
		}
	}
	dir, err := root.Open(".")
	if err != nil {
		return fail()
	}
	entries, err := dir.ReadDir(MaxTarballs + 2)
	dir.Close()
	if err != nil && err != io.EOF {
		return fail()
	}
	if len(entries) != len(files) {
		return fail()
	}
	for _, entry := range entries {
		if !files[entry.Name()] {
			return fail()
		}
	}
	if err = ctx.Err(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}
func hexID(v string) bool {
	b, err := hex.DecodeString(v)
	return err == nil && len(b) == 32 && hex.EncodeToString(b) == v
}
func readPrivate(root *os.Root, name string, limit int64) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > limit {
		return nil, ErrBundle
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) {
		return nil, ErrBundle
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, ErrBundle
	}
	return data, nil
}
