//go:build linux && integration

package nodelaunch

import (
	"net"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
)

func TestRuntimeUsageFindsIndependentProcessAndListener(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root inside disposable Linux VM")
	}
	// One fixed fixture UID is reserved for this test; do not run copies together.
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	a := assignment()
	a.Architecture = runtime.GOARCH
	a.UID = 60002
	a.Port = listener.Addr().(*net.TCPAddr).Port
	before, err := InspectRuntimeUsage(t.Context(), a)
	if err != nil || before.UIDProcessesPresent || before.CgroupPopulated || !before.TCPListenerPresent {
		t.Fatal("independent root-owned listener not detected", before, err)
	}
	child := exec.Command("/bin/sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 60002, Gid: 60002, Groups: []uint32{}}}
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { child.Process.Kill(); child.Wait() }()
	live, err := InspectRuntimeUsage(t.Context(), a)
	if err != nil || !live.UIDProcessesPresent || live.CgroupPopulated || !live.TCPListenerPresent {
		t.Fatal("process outside service cgroup missed", live, err)
	}
	if err = child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	child.Wait()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	stopped, err := InspectRuntimeUsage(t.Context(), a)
	if err != nil || stopped.UIDProcessesPresent || stopped.CgroupPopulated || stopped.TCPListenerPresent {
		t.Fatal("usage remained after cleanup", stopped, err)
	}
}
