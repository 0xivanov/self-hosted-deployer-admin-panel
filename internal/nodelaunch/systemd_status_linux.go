//go:build linux

package nodelaunch

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"runtime"
	"time"
)

type boundedStatus struct{ bytes.Buffer }

func (b *boundedStatus) Write(data []byte) (int, error) {
	if b.Len()+len(data) > 16384 {
		return 0, ErrServiceStatus
	}
	return b.Buffer.Write(data)
}

// InspectSystemd reads one exact generated unit through a fixed local command.
// It never starts, stops or reloads anything. This is unit metadata, not verified
// artifact/process identity or complete retirement evidence.
func InspectSystemd(ctx context.Context, a Assignment) (SystemdState, error) {
	var out SystemdState
	unit, err := Render(a)
	if err != nil {
		return out, err
	}
	if os.Geteuid() != 0 || a.Architecture != runtime.GOARCH {
		return out, ErrAssignment
	}
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(readCtx, "/usr/bin/systemctl", "show", "--all", "--no-pager", "--property=Id,LoadState,ActiveState,SubState,MainPID,ControlPID,Job,ControlGroup,FragmentPath,UnitFileState,NeedDaemonReload", unit.Name)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	var output boundedStatus
	cmd.Stdout = &output
	if err = cmd.Run(); err != nil {
		return out, err
	}
	if err = readCtx.Err(); err != nil {
		return out, err
	}
	out, err = parseSystemdState(output.Bytes(), unit.Name)
	if err != nil {
		return out, err
	}
	out.ObservedAt = time.Now()
	return out, nil
}
