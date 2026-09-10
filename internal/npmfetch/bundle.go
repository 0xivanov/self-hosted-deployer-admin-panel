package npmfetch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"time"
)

const MaxBundleBytes = 100 << 20

var ErrBundle = errors.New("dependency bundle could not be completed")

type Downloader interface {
	Fetch(context.Context, Tarball) ([]byte, error)
}
type StoredTarball struct {
	URL, File, Integrity string
	Bytes                int64
}
type Manifest struct {
	SourceSHA256 string
	Tarballs     []StoredTarball
}
type Bundle struct{ Directory, ManifestSHA256 string }

// DownloadBundle creates one private source-bound bundle below an operator-owned
// job root. The manifest is committed last, after verified files are synced. No
// package is unpacked/executed. The caller owns successful retention and crash-
// orphan reconciliation; jobs must not share this root with customer processes.
func DownloadBundle(ctx context.Context, source []byte, expectedSHA256 string, jobs *os.Root, client Downloader) (Bundle, error) {
	return downloadBundle(ctx, source, expectedSHA256, jobs, client, MaxBundleBytes)
}
func downloadBundle(ctx context.Context, source []byte, expectedSHA256 string, jobs *os.Root, client Downloader, maxBytes int64) (result Bundle, err error) {
	if jobs == nil || client == nil {
		return result, ErrBundle
	}
	info, err := jobs.Stat(".")
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return result, ErrBundle
	}
	set, err := FromSource(ctx, source, expectedSHA256)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err = ctx.Err(); err != nil {
		return result, err
	}
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		return result, err
	}
	directory := "dependencies-" + hex.EncodeToString(random[:])
	if err = jobs.Mkdir(directory, 0700); err != nil {
		return result, err
	}
	complete := false
	defer func() {
		if !complete {
			err = errors.Join(err, jobs.RemoveAll(directory))
			result = Bundle{}
		}
	}()
	root, err := jobs.OpenRoot(directory)
	if err != nil {
		return result, err
	}
	defer root.Close()
	manifest := Manifest{SourceSHA256: set.SourceSHA256, Tarballs: []StoredTarball{}}
	var total int64
	written := map[string]bool{}
	for _, tarball := range set.Tarballs {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		payload, e := client.Fetch(ctx, tarball)
		if e != nil {
			return result, e
		}
		if len(payload) > MaxTarballBytes {
			return result, ErrBundle
		}
		integrity := sha512.Sum512(payload)
		if "sha512-"+base64.StdEncoding.EncodeToString(integrity[:]) != tarball.Integrity {
			return result, ErrBundle
		}
		hash := sha256.Sum256(payload)
		name := hex.EncodeToString(hash[:]) + ".tgz"
		if !written[name] {
			if int64(len(payload)) > maxBytes-total {
				return result, ErrBundle
			}
			total += int64(len(payload))
			if err = writeSynced(root, name, payload); err != nil {
				return result, err
			}
			written[name] = true
		}
		manifest.Tarballs = append(manifest.Tarballs, StoredTarball{tarball.URL, name, tarball.Integrity, int64(len(payload))})
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return result, err
	}
	if int64(len(raw)) > maxBytes-total {
		return result, ErrBundle
	}
	if err = writeSynced(root, "bundle.pending", raw); err != nil {
		return result, err
	}
	if err = root.Rename("bundle.pending", "bundle.json"); err != nil {
		return result, err
	}
	if err = syncDirectory(root); err != nil {
		return result, err
	}
	if err = syncDirectory(jobs); err != nil {
		return result, err
	}
	hash := sha256.Sum256(raw)
	complete = true
	return Bundle{directory, hex.EncodeToString(hash[:])}, nil
}
func writeSynced(root *os.Root, name string, data []byte) error {
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}
func syncDirectory(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
