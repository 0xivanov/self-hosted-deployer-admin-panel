package nodelaunch

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

var ErrRuntimeUsage = errors.New("incomplete or unsupported Linux runtime usage")

type RuntimeUsage struct {
	UIDProcessesPresent, CgroupPopulated, TCPListenerPresent bool
	ObservedAt                                               time.Time
}

func statusUsesUID(data []byte, uid int) (bool, error) {
	found, used := false, false
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		if found {
			return false, ErrRuntimeUsage
		}
		found = true
		values := strings.Fields(strings.TrimPrefix(line, "Uid:"))
		if len(values) != 4 {
			return false, ErrRuntimeUsage
		}
		for _, value := range values {
			n, err := strconv.ParseUint(value, 10, 32)
			if err != nil {
				return false, ErrRuntimeUsage
			}
			if n == uint64(uid) {
				used = true
			}
		}
	}
	if !found {
		return false, ErrRuntimeUsage
	}
	return used, nil
}
func populatedCgroup(data []byte) (bool, error) {
	found, populated := false, false
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return false, ErrRuntimeUsage
		}
		if fields[0] != "populated" {
			continue
		}
		if found || (fields[1] != "0" && fields[1] != "1") {
			return false, ErrRuntimeUsage
		}
		found = true
		populated = fields[1] == "1"
	}
	if !found {
		return false, ErrRuntimeUsage
	}
	return populated, nil
}
func hasTCPListener(data []byte, port, addressDigits int) (bool, error) {
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	header := strings.Fields(lines[0])
	if len(header) < 4 || header[0] != "sl" || header[1] != "local_address" || header[3] != "st" {
		return false, ErrRuntimeUsage
	}
	present := false
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			return false, ErrRuntimeUsage
		}
		address, rawPort, ok := strings.Cut(fields[1], ":")
		if !ok || len(address) != addressDigits || len(rawPort) != 4 || len(fields[3]) != 2 {
			return false, ErrRuntimeUsage
		}
		for _, ch := range address {
			if !strings.ContainsRune("0123456789ABCDEFabcdef", ch) {
				return false, ErrRuntimeUsage
			}
		}
		local, err := strconv.ParseUint(rawPort, 16, 16)
		if err != nil {
			return false, ErrRuntimeUsage
		}
		state, err := strconv.ParseUint(fields[3], 16, 8)
		if err != nil {
			return false, ErrRuntimeUsage
		}
		if int(local) == port && state == 0x0a {
			present = true
		} // Linux TCP_LISTEN.
	}
	return present, nil
}
