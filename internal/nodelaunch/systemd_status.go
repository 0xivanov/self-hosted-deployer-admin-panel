package nodelaunch

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

var ErrServiceStatus = errors.New("invalid or incomplete Node service status")

type SystemdState struct {
	Unit, LoadState, ActiveState, SubState         string
	MainPID, ControlPID                            uint32
	Job, ControlGroup, FragmentPath, UnitFileState string
	NeedDaemonReload                               bool
	ObservedAt                                     time.Time
}

// UnitStopped reports only systemd's unit/job state. It does not prove the UID or
// cgroup is empty, a listener is gone, routes are detached, or starts are fenced.
// It must never alone authorize reuse of a runtime slot.
func (s SystemdState) UnitStopped() bool {
	return (s.LoadState == "loaded" || s.LoadState == "masked" || s.LoadState == "not-found") && (s.ActiveState == "inactive" && s.SubState == "dead" || s.ActiveState == "failed" && s.SubState == "failed") && s.MainPID == 0 && s.ControlPID == 0 && (s.Job == "" || s.Job == "0") && !s.NeedDaemonReload
}
func parseSystemdState(data []byte, unit string) (SystemdState, error) {
	var out SystemdState
	if len(data) == 0 || len(data) > 16384 {
		return out, ErrServiceStatus
	}
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || len(value) > 1024 || strings.ContainsAny(value, "\r\x00") {
			return out, ErrServiceStatus
		}
		if _, seen := fields[key]; seen {
			return out, ErrServiceStatus
		}
		fields[key] = value
	}
	required := []string{"Id", "LoadState", "ActiveState", "SubState", "MainPID", "ControlPID", "Job", "ControlGroup", "FragmentPath", "UnitFileState", "NeedDaemonReload"}
	if len(fields) != len(required) {
		return out, ErrServiceStatus
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return out, ErrServiceStatus
		}
	}
	if fields["Id"] != unit {
		return out, ErrServiceStatus
	}
	switch fields["LoadState"] {
	case "loaded", "masked", "not-found", "error", "bad-setting":
	default:
		return out, ErrServiceStatus
	}
	switch fields["ActiveState"] {
	case "active", "inactive", "failed", "activating", "deactivating", "reloading", "maintenance", "refreshing":
	default:
		return out, ErrServiceStatus
	}
	if fields["SubState"] == "" || len(fields["SubState"]) > 64 {
		return out, ErrServiceStatus
	}
	pid := func(value string) (uint32, error) {
		n, err := strconv.ParseUint(value, 10, 32)
		if err != nil || strconv.FormatUint(n, 10) != value {
			return 0, ErrServiceStatus
		}
		return uint32(n), nil
	}
	main, err := pid(fields["MainPID"])
	if err != nil {
		return out, err
	}
	control, err := pid(fields["ControlPID"])
	if err != nil {
		return out, err
	}
	if fields["ControlGroup"] != "" && fields["ControlGroup"] != "/system.slice/"+unit {
		return out, ErrServiceStatus
	}
	switch fields["FragmentPath"] {
	case "", "/dev/null", "/run/systemd/system/" + unit, "/etc/systemd/system/" + unit:
	default:
		return out, ErrServiceStatus
	}
	if fields["NeedDaemonReload"] != "yes" && fields["NeedDaemonReload"] != "no" {
		return out, ErrServiceStatus
	}
	out = SystemdState{Unit: unit, LoadState: fields["LoadState"], ActiveState: fields["ActiveState"], SubState: fields["SubState"], MainPID: main, ControlPID: control, Job: fields["Job"], ControlGroup: fields["ControlGroup"], FragmentPath: fields["FragmentPath"], UnitFileState: fields["UnitFileState"], NeedDaemonReload: fields["NeedDaemonReload"] == "yes"}
	return out, nil
}
