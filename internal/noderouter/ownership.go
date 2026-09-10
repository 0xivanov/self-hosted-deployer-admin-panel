package noderouter

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

var ErrServingOwned = errors.New("Node routing database already has an upstream owner")

// Only one Router may contact upstreams for a database. Read-only and retirement
// control handles do not take ownership. Never remove or replace the lock file
// while any router using this database can still exist.
func (r *Router) beginUpstream() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return os.ErrClosed
	}
	if r.owner == nil {
		fd, err := unix.Open(r.ownerPath, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
		if err != nil {
			return err
		}
		f := os.NewFile(uintptr(fd), r.ownerPath)
		var stat unix.Stat_t
		if err = unix.Fstat(fd, &stat); err != nil {
			f.Close()
			return err
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0077 != 0 || stat.Nlink != 1 {
			f.Close()
			return ErrInvalid
		}
		if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
			f.Close()
			if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
				return ErrServingOwned
			}
			return err
		}
		r.owner = f
	}
	r.upstreams++
	return nil
}

func (r *Router) endUpstream() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.upstreams--
	r.releaseOwnerLocked()
}

// Closing a handle cannot transfer ownership while a request, upgrade, or
// activation still runs. The last upstream operation releases the descriptor.
func (r *Router) releaseOwnerLocked() {
	if r.closed && r.upstreams == 0 && r.owner != nil {
		r.owner.Close()
		r.owner = nil
	}
}
