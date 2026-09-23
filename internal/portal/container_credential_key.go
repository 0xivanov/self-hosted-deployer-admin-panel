package portal

import (
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
)

// LoadContainerCredentialKey is shared by the portal and fleet worker. Both
// processes need the same externally backed-up key, separate from the database.
func LoadContainerCredentialKey(path string) ([]byte, error) {
	fail := errors.New("container credential key must be an owner-private regular file containing a 32-byte hex key")
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return nil, fail
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fail
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !os.SameFile(before, stat) || !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 || stat.Size() > 128 {
		return nil, fail
	}
	owner, ok := stat.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Getuid() {
		return nil, fail
	}
	raw, err := io.ReadAll(io.LimitReader(file, 129))
	if err != nil || len(raw) > 128 {
		return nil, fail
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != 32 {
		return nil, fail
	}
	return key, nil
}
