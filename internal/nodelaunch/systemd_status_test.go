package nodelaunch

import (
	"strings"
	"testing"
)

func serviceOutput() string {
	return "Id=deployer-node-test.service\nLoadState=loaded\nActiveState=inactive\nSubState=dead\nMainPID=0\nControlPID=0\nJob=\nControlGroup=\nFragmentPath=/run/systemd/system/deployer-node-test.service\nUnitFileState=static\nNeedDaemonReload=no\n"
}
func TestSystemdStoppedRequiresNoPendingJobOrControlProcess(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, from, to string
		stopped        bool
	}{
		{"inactive", "", "", true}, {"failed", "ActiveState=inactive\nSubState=dead", "ActiveState=failed\nSubState=failed", true},
		{"main_process", "MainPID=0", "MainPID=123", false}, {"control_process", "ControlPID=0", "ControlPID=12", false},
		{"queued_job", "Job=\n", "Job=23\n", false}, {"reload_needed", "NeedDaemonReload=no", "NeedDaemonReload=yes", false},
		{"stopping", "ActiveState=inactive", "ActiveState=deactivating", false}, {"restart_pending", "SubState=dead", "SubState=auto-restart", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := serviceOutput()
			if tc.from != "" {
				data = strings.Replace(data, tc.from, tc.to, 1)
			}
			s, err := parseSystemdState([]byte(data), "deployer-node-test.service")
			if err != nil || s.UnitStopped() != tc.stopped {
				t.Fatal(s, err)
			}
		})
	}
}
func TestSystemdRejectsUntrustedOrIncompleteProperties(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, from, to string }{
		{"wrong_unit", "Id=deployer-node-test.service", "Id=other.service"},
		{"missing_pid", "MainPID=0\n", ""}, {"negative_pid", "MainPID=0", "MainPID=-1"}, {"overflow", "MainPID=0", "MainPID=4294967296"},
		{"duplicate", "MainPID=0", "MainPID=0\nMainPID=1"}, {"unknown", "Job=", "Extra=\nJob="},
		{"unknown_state", "ActiveState=inactive", "ActiveState=unknown"},
		{"foreign_group", "ControlGroup=", "ControlGroup=/system.slice/other.service"},
		{"foreign_unit_file", "FragmentPath=/run/systemd/system/deployer-node-test.service", "FragmentPath=/tmp/other.service"},
		{"missing_reload", "NeedDaemonReload=no", "NeedDaemonReload="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := strings.Replace(serviceOutput(), tc.from, tc.to, 1)
			if _, err := parseSystemdState([]byte(data), "deployer-node-test.service"); err == nil {
				t.Fatal("invalid status accepted")
			}
		})
	}
}
