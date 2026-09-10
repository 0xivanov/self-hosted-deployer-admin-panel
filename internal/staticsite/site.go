// Package staticsite serves immutable static releases on a separate content
// origin. It never executes project files and requires no portal credentials.
package staticsite

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/projectarchive"
)

const MaxReleases = 20
const MaxStoredBytes int64 = 100 << 20

type Site struct {
	root     string
	mu       sync.RWMutex
	current  *release
	revision int64
	lock     *os.File
	closed   bool
}
type release struct {
	id    string
	files map[string]*zip.File
}

// Open takes a directory dedicated to one site. Only the runtime service user
// may write here. Customers never supply filesystem paths or the Host binding.
func Open(root string) (*Site, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("site directory must be private")
	}
	fd, err := unix.Open(filepath.Join(root, ".owner.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	var lock *os.File
	if err == nil {
		lock = os.NewFile(uintptr(fd), ".owner.lock")
	}
	if err != nil {
		return nil, err
	}
	lockInfo, statErr := lock.Stat()
	if statErr != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm()&0077 != 0 {
		lock.Close()
		return nil, errors.New("invalid runtime owner lock")
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("site already has a runtime owner")
	}
	s := &Site{root: root, lock: lock}
	success := false
	defer func() {
		if !success {
			s.Close()
		}
	}()
	pointer, err := readPrivate(filepath.Join(root, "active"), 256)
	if errors.Is(err, os.ErrNotExist) {
		success = true
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	id := string(pointer)
	if len(pointer) != 64 {
		var state activeState
		if err = json.Unmarshal(pointer, &state); err != nil || state.Revision < 1 {
			return nil, errors.New("invalid active release state")
		}
		id = state.Release
		s.revision = state.Revision
	}
	r, err := s.load(context.Background(), id)
	if err != nil {
		return nil, fmt.Errorf("load active release: %w", err)
	}
	s.current = r
	success = true
	return s, nil
}

// Publish validates before changing the active pointer. Source ZIPs are retained
// unchanged for rollback; file contents are read from ZIPs without extraction.
// One process owns a site's release mutations.
type activeState struct {
	Release  string `json:"release"`
	Revision int64  `json:"revision"`
}

var ErrStaleRevision = errors.New("stale or conflicting runtime revision")

func (s *Site) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.current = nil
	if s.lock != nil {
		return s.lock.Close()
	}
	return nil
}
func (s *Site) Revision() int64 { s.mu.RLock(); defer s.mu.RUnlock(); return s.revision }

// PublishRevision fences delayed workers using the project publication revision.
// Reusing a revision is allowed only for the identical immutable archive.
func (s *Site) PublishRevision(ctx context.Context, revision int64, data []byte) (string, error) {
	if revision < 1 {
		return "", ErrStaleRevision
	}
	return s.publish(ctx, revision, data)
}
func (s *Site) Publish(ctx context.Context, data []byte) (string, error) {
	return s.publish(ctx, 0, data)
}
func (s *Site) publish(ctx context.Context, revision int64, data []byte) (string, error) {
	manifest, err := projectarchive.Validate(ctx, data, "static")
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("site is closed")
	}
	if s.revision > 0 && revision <= s.revision {
		if revision == s.revision && s.current != nil && s.current.id == manifest.SHA256 {
			dir, e := os.Open(s.root)
			if e != nil {
				return "", e
			}
			defer dir.Close()
			return manifest.SHA256, dir.Sync()
		}
		return "", ErrStaleRevision
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	target := filepath.Join(s.root, manifest.SHA256+".zip")
	existing, err := readPrivate(target, projectarchive.MaxCompressed)
	if errors.Is(err, os.ErrNotExist) {
		entries, readErr := os.ReadDir(s.root)
		if readErr != nil {
			return "", readErr
		}
		count := 0
		var stored int64
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".zip") {
				info, infoErr := entry.Info()
				if infoErr != nil {
					return "", infoErr
				}
				count++
				stored += info.Size()
			}
		}
		if count >= MaxReleases || stored+int64(len(data)) > MaxStoredBytes {
			return "", errors.New("site release storage limit reached")
		}
		err = atomicWrite(s.root, target, data)
	} else if err == nil && !bytes.Equal(existing, data) {
		err = errors.New("stored release integrity mismatch")
	}
	if err != nil {
		return "", err
	}
	r, err := s.load(ctx, manifest.SHA256)
	if err != nil {
		return "", err
	}
	if err = s.activate(r, revision); err != nil {
		return "", err
	}
	return r.id, nil
}

func (s *Site) Rollback(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("site is closed")
	}
	if s.revision > 0 {
		return ErrStaleRevision
	}
	r, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	return s.activate(r, 0)
}

// Prune removes an explicitly selected inactive release. Callers must also
// enforce any external rollback retention policy before invoking this method.
func (s *Site) Prune(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("site is closed")
	}
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != 32 || strings.ToLower(id) != id {
		return errors.New("invalid release ID")
	}
	if s.current != nil && s.current.id == id {
		return errors.New("cannot delete the active release")
	}
	if err = os.Remove(filepath.Join(s.root, id+".zip")); err != nil {
		return err
	}
	dir, err := os.Open(s.root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (s *Site) Active() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.current == nil {
		return ""
	}
	return s.current.id
}
func (s *Site) activate(r *release, revision int64) error {
	data := []byte(r.id)
	if revision > 0 {
		data, _ = json.Marshal(activeState{Release: r.id, Revision: revision})
	}
	if err := atomicWrite(s.root, filepath.Join(s.root, "active"), data); err != nil {
		// A directory-sync error may happen after rename. Reconcile visible state
		// before returning the uncertain outcome so memory cannot serve an older
		// revision than disk. The worker must retry/observe before acknowledging.
		observed, readErr := readPrivate(filepath.Join(s.root, "active"), 256)
		if readErr == nil && bytes.Equal(observed, data) {
			s.current = r
			s.revision = revision
		}
		return err
	}
	s.current = r
	s.revision = revision
	return nil
}
func (s *Site) load(ctx context.Context, id string) (*release, error) {
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != 32 || strings.ToLower(id) != id {
		return nil, errors.New("invalid release ID")
	}
	data, err := readPrivate(filepath.Join(s.root, id+".zip"), projectarchive.MaxCompressed)
	if err != nil {
		return nil, err
	}
	manifest, err := projectarchive.Validate(ctx, data, "static")
	if err != nil {
		return nil, err
	}
	if manifest.SHA256 != id {
		return nil, errors.New("release integrity mismatch")
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	r := &release{id: id, files: map[string]*zip.File{}}
	for _, f := range z.File {
		if !f.Mode().IsDir() {
			r.files[f.Name] = f
		}
	}
	return r, nil
}
func readPrivate(name string, limit int64) ([]byte, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > limit {
		return nil, errors.New("invalid private release file")
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("release file too large")
	}
	return data, nil
}
func atomicWrite(root, target string, data []byte) error {
	f, err := os.CreateTemp(root, ".pending-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), target); err != nil {
		return err
	}
	dir, err := os.Open(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// Handler binds one site to one operator-assigned host. Deploy it on a separate
// registrable domain from the portal, with no portal routes or shared cookies.
// SPA fallback is opt-in and only applies to extensionless HTML navigation.
func (s *Site) Handler(host string, spa bool) (http.Handler, error) {
	if host == "" || strings.ContainsAny(host, "/\\@ \r\n\t") {
		return nil, errors.New("a content host is required")
	}
	slots := make(chan struct{}, 16)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-cache")
		if r.Host != host {
			http.Error(w, "Unknown site", http.StatusMisdirectedRequest)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", 405)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "Please retry shortly", 503)
			return
		}
		s.mu.RLock()
		current := s.current
		s.mu.RUnlock()
		if current == nil {
			http.Error(w, "Site is not published", 503)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") || path.Clean("/"+name) != "/"+strings.TrimSuffix(name, "/") && name != "" {
			http.NotFound(w, r)
			return
		}
		if name == "" || strings.HasSuffix(name, "/") {
			name += "index.html"
		}
		file := current.files[name]
		if file == nil && spa && path.Ext(name) == "" && strings.Contains(r.Header.Get("Accept"), "text/html") {
			file = current.files["index.html"]
		}
		if file == nil {
			http.NotFound(w, r)
			return
		}
		reader, err := file.Open()
		if err != nil {
			http.Error(w, "Content unavailable", 500)
			return
		}
		data, err := io.ReadAll(io.LimitReader(reader, projectarchive.MaxFile+1))
		reader.Close()
		if err != nil || len(data) > projectarchive.MaxFile {
			http.Error(w, "Content unavailable", 500)
			return
		}
		digest := sha256.Sum256(data)
		w.Header().Set("ETag", `"`+hex.EncodeToString(digest[:])+`"`)
		// ServeContent supplies range and conditional request semantics, without
		// directory listing, redirects, filesystem traversal or executable handlers.
		http.ServeContent(w, r, file.Name, file.Modified, bytes.NewReader(data))
	}), nil
}
