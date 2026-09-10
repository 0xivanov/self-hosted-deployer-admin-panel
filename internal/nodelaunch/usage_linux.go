//go:build linux

package nodelaunch

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

func kernelRoot(path string, magic int64) (*os.Root, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	file, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	var info unix.Statfs_t
	err = unix.Fstatfs(int(file.Fd()), &info)
	file.Close()
	if err != nil || int64(info.Type) != magic {
		root.Close()
		return nil, ErrRuntimeUsage
	}
	return root, nil
}
func readKernelFile(ctx context.Context, root *os.Root, name string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrRuntimeUsage
	}
	return data, ctx.Err()
}
func uidProcesses(ctx context.Context, proc *os.Root, uid int) (bool, error) {
	directory, err := proc.Open(".")
	if err != nil {
		return false, err
	}
	defer directory.Close()
	count := 0
	for {
		if err = ctx.Err(); err != nil {
			return false, err
		}
		entries, readErr := directory.ReadDir(256)
		for _, entry := range entries {
			if _, err = strconv.ParseUint(entry.Name(), 10, 32); err != nil {
				continue
			}
			count++
			if count > 65536 {
				return false, ErrRuntimeUsage
			}
			data, e := readKernelFile(ctx, proc, entry.Name()+"/status", 65536)
			if errors.Is(e, os.ErrNotExist) || errors.Is(e, unix.ESRCH) {
				continue
			}
			if e != nil {
				return false, e
			}
			used, e := statusUsesUID(data, uid)
			if e != nil {
				return false, e
			}
			if used {
				return true, nil
			}
		}
		if errors.Is(readErr, io.EOF) {
			return false, nil
		}
		if readErr != nil {
			return false, readErr
		}
	}
}

// InspectRuntimeUsage reads the controller's actual procfs/cgroup-v2 namespace.
// It must run on the dedicated runtime host with complete process/network
// visibility. It does not establish namespace coverage or fence future changes.
// All three results are observations, not standalone permission to reuse a slot.
func InspectRuntimeUsage(ctx context.Context, a Assignment) (RuntimeUsage, error) {
	var out RuntimeUsage
	unit, err := Render(a)
	if err != nil {
		return out, err
	}
	if os.Geteuid() != 0 || a.Architecture != runtime.GOARCH {
		return out, ErrAssignment
	}
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	proc, err := kernelRoot("/proc", unix.PROC_SUPER_MAGIC)
	if err != nil {
		return out, err
	}
	defer proc.Close()
	cgroup, err := kernelRoot("/sys/fs/cgroup", unix.CGROUP2_SUPER_MAGIC)
	if err != nil {
		return out, err
	}
	defer cgroup.Close()
	out.UIDProcessesPresent, err = uidProcesses(readCtx, proc, a.UID)
	if err != nil {
		return RuntimeUsage{}, err
	}
	events, err := readKernelFile(readCtx, cgroup, "system.slice/"+unit.Name+"/cgroup.events", 4096)
	if errors.Is(err, os.ErrNotExist) {
		out.CgroupPopulated = false
	} else if err != nil {
		return RuntimeUsage{}, err
	} else {
		out.CgroupPopulated, err = populatedCgroup(events)
		if err != nil {
			return RuntimeUsage{}, err
		}
	}
	for _, table := range []struct {
		name   string
		digits int
	}{{"net/tcp", 8}, {"net/tcp6", 32}} {
		data, e := readKernelFile(readCtx, proc, table.name, 4<<20)
		if e != nil {
			return RuntimeUsage{}, e
		}
		present, e := hasTCPListener(data, a.Port, table.digits)
		if e != nil {
			return RuntimeUsage{}, e
		}
		out.TCPListenerPresent = out.TCPListenerPresent || present
	}
	if err = readCtx.Err(); err != nil {
		return RuntimeUsage{}, err
	}
	out.ObservedAt = time.Now()
	return out, nil
}
