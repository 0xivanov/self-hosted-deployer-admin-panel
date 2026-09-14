//go:build integration && (darwin || linux)

package nodepipeline

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/npmfetch"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

const integrationID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type pipelineFixture struct {
	directory string
	root      *os.Root
	request   portal.NodeExecutionRequest
	archive   []byte
	artifact  string
	snapshot  string
	exportID  string
}

func TestReadNodeArtifactCompletedPipeline(t *testing.T) {
	t.Parallel()
	fixture := newPipelineFixture(t)
	got, data, err := (Reader{Root: fixture.root, Request: fixture.request}).ReadNodeArtifact(t.Context(), fixture.request.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, fixture.archive) || got.ExecutionID != fixture.request.ExecutionID || got.ProjectID != fixture.request.ProjectID || got.ArtifactSHA256 != fixture.artifact || got.DependencyManifestSHA256 != fixture.request.Bundle.ManifestSHA256 {
		t.Fatalf("unexpected observation: %+v", got)
	}
}

func TestInspectNodeExecutionFailedPipeline(t *testing.T) {
	t.Parallel()
	f := newPipelineFixture(t)
	if err := writeJSON(filepath.Join(f.directory, "result.json"), map[string]any{"ExecutionID": f.request.ExecutionID, "BuildID": f.request.BuildID, "ProjectID": f.request.ProjectID, "SourceSHA256": f.request.Plan.SourceSHA256, "ToolchainSHA256": f.request.ToolchainSHA256, "Architecture": f.request.Plan.Architecture, "DependencyManifestSHA256": f.request.Bundle.ManifestSHA256, "ArtifactSHA256": "", "SnapshotSHA256": f.snapshot, "ExportOperationID": f.exportID, "Outcome": "failed"}); err != nil {
		t.Fatal(err)
	}
	obs, err := (Reader{Root: f.root, Request: f.request}).InspectNodeExecution(t.Context(), f.request.ExecutionID)
	if err != nil || obs.Outcome != "failed" || !obs.Retired {
		t.Fatalf("unexpected failed observation: %+v, %v", obs, err)
	}
	if _, _, err = (Reader{Root: f.root, Request: f.request}).ReadNodeArtifact(t.Context(), f.request.ExecutionID); err == nil {
		t.Fatal("failed pipeline returned an artifact")
	}
}

func TestFailedPipelineRequiresBothStoppedVMsAndReleasedLock(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"build", "export", "active controller"} {
		t.Run(name, func(t *testing.T) {
			f := newPipelineFixture(t)
			var outcome result
			if err := decode(f.root, "result.json", &outcome); err != nil {
				t.Fatal(err)
			}
			outcome.Outcome = "failed"
			outcome.ArtifactSHA256 = ""
			if err := writeJSON(filepath.Join(f.directory, "result.json"), outcome); err != nil {
				t.Fatal(err)
			}
			if name == "active controller" {
				lock, err := os.Open(f.directory)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
				if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(filepath.Join(f.directory, name, "stopped.json")); err != nil {
				t.Fatal(err)
			}
			if _, err := (Reader{Root: f.root, Request: f.request}).InspectNodeExecution(t.Context(), f.request.ExecutionID); err == nil {
				t.Fatal("unconfirmed failed execution retired")
			}
		})
	}
}

func TestReadNodeArtifactRejectsIncompleteOrClaimedEvidence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*pipelineFixture) error
	}{
		{"mismatched execution", func(f *pipelineFixture) error { f.request.ExecutionID = strings.Repeat("9", 64); return nil }},
		{"missing stop receipt", func(f *pipelineFixture) error { return os.Remove(filepath.Join(f.directory, "build", "stopped.json")) }},
		{"mismatched project", func(f *pipelineFixture) error {
			return writeJSON(filepath.Join(f.directory, "result.json"), map[string]any{"ExecutionID": f.request.ExecutionID, "BuildID": f.request.BuildID, "ProjectID": strings.Repeat("e", 64), "SourceSHA256": f.request.Plan.SourceSHA256, "ToolchainSHA256": f.request.ToolchainSHA256, "Architecture": f.request.Plan.Architecture, "DependencyManifestSHA256": f.request.Bundle.ManifestSHA256, "ArtifactSHA256": f.artifact, "SnapshotSHA256": f.snapshot, "ExportOperationID": f.exportID, "Outcome": "succeeded"})
		}},
		{"incorrect raw archive digest", func(f *pipelineFixture) error {
			file, err := os.OpenFile(filepath.Join(f.directory, "export", "output.disk"), os.O_RDWR, 0600)
			if err != nil {
				return err
			}
			defer file.Close()
			return corruptArchiveDigest(file)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newPipelineFixture(t)
			if err := tc.mutate(f); err != nil {
				t.Fatal(err)
			}
			if _, _, err := (Reader{Root: f.root, Request: f.request}).ReadNodeArtifact(t.Context(), f.request.ExecutionID); err == nil {
				t.Fatal("invalid pipeline evidence accepted")
			}
		})
	}

	t.Run("live exclusive flock", func(t *testing.T) {
		f := newPipelineFixture(t)
		fd, err := os.Open(f.directory)
		if err != nil {
			t.Fatal(err)
		}
		defer fd.Close()
		if err = syscall.Flock(int(fd.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		if _, _, err = (Reader{Root: f.root, Request: f.request}).ReadNodeArtifact(t.Context(), f.request.ExecutionID); err == nil {
			t.Fatal("active pipeline was read")
		}
	})
}

func newPipelineFixture(t *testing.T) *pipelineFixture {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	request := portal.NodeExecutionRequest{
		ExecutionID: integrationID, BuildID: strings.Repeat("b", 64), ProjectID: strings.Repeat("c", 64),
		Plan:            nodebuild.Plan{Version: 1, SourceSHA256: strings.Repeat("d", 64), OS: "linux", Architecture: "arm64", NodeMajor: 24, Steps: []nodebuild.Command{{Program: "npm", Args: []string{"ci"}}}, Start: nodebuild.Command{Program: "npm", Args: []string{"start"}}, Port: 3000},
		ToolchainSHA256: strings.Repeat("e", 64), Bundle: npmfetch.Bundle{Directory: "dependencies-" + strings.Repeat("f", 64), ManifestSHA256: strings.Repeat("1", 64)}, NotAfter: time.Now().Add(50 * time.Second).Unix(),
	}
	archive := validArchive(t)
	archiveSum := sha256.Sum256(archive)
	artifact := hex.EncodeToString(archiveSum[:])
	snapshot := strings.Repeat("2", 64)
	exportID := strings.Repeat("3", 64)
	for _, name := range []string{"build", "export"} {
		if err := os.Mkdir(filepath.Join(directory, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeJSON(filepath.Join(directory, "request.json"), request); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(directory, "pipeline-attempt.json"), map[string]any{"ExecutionID": integrationID}); err != nil {
		t.Fatal(err)
	}
	writeStageReceipts(t, directory, "build", integrationID, false, 3, request.NotAfter-1)
	writeStageReceipts(t, directory, "export", exportID, true, 4, request.NotAfter)
	if err := writeJSON(filepath.Join(directory, "result.json"), map[string]any{"ExecutionID": integrationID, "BuildID": request.BuildID, "ProjectID": request.ProjectID, "SourceSHA256": request.Plan.SourceSHA256, "ToolchainSHA256": request.ToolchainSHA256, "Architecture": request.Plan.Architecture, "DependencyManifestSHA256": request.Bundle.ManifestSHA256, "ArtifactSHA256": artifact, "SnapshotSHA256": snapshot, "ExportOperationID": exportID, "Outcome": "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "release.zip"), archive, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeExportDisk(filepath.Join(directory, "export", "output.disk"), archive, integrationID, request.Plan.SourceSHA256, snapshot, artifact); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return &pipelineFixture{directory: directory, root: root, request: request, archive: archive, artifact: artifact, snapshot: snapshot, exportID: exportID}
}

func validArchive(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	archive := zip.NewWriter(&data)
	file, err := archive.Create("package.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte(`{"scripts":{"start":"node server.js"}}`)); err != nil {
		t.Fatal(err)
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func writeJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func writeStageReceipts(t *testing.T, directory, stage, operation string, export bool, storage int, deadline int64) {
	t.Helper()
	base := filepath.Join(directory, stage)
	if err := writeJSON(filepath.Join(base, "attempt.json"), map[string]any{"operation": operation, "deadline": float64(deadline), "build_disks": !export, "export_disks": export}); err != nil {
		t.Fatal(err)
	}
	runningAt := time.Now().Add(-3 * time.Second)
	if !export {
		runningAt = time.Now().Add(-5 * time.Second)
	}
	if err := writeJSON(filepath.Join(base, "running.json"), map[string]any{"operation": operation, "observed_at": float64(runningAt.UnixNano()) / 1e9, "network_devices": 0, "storage_devices": storage, "build_disks": !export, "export_disks": export}); err != nil {
		t.Fatal(err)
	}
	stoppedAt := time.Now().Add(-2 * time.Second)
	if !export {
		stoppedAt = time.Now().Add(-4 * time.Second)
	}
	if err := writeJSON(filepath.Join(base, "stopped.json"), map[string]any{"operation": operation, "observed_at": float64(stoppedAt.UnixNano()) / 1e9, "state": "stopped", "build_disks": !export, "export_disks": export}); err != nil {
		t.Fatal(err)
	}
}

func writeExportDisk(path string, archive []byte, execution, source, snapshot, archiveSHA string) error {
	header, err := json.Marshal(map[string]any{"Version": 1, "ExecutionID": execution, "SourceSHA256": source, "SnapshotSHA256": snapshot, "ArchiveSHA256": archiveSHA, "ArchiveBytes": int64(len(archive))})
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err = file.Truncate(64 << 20); err != nil {
		return err
	}
	frame := make([]byte, 4096)
	copy(frame, header)
	if _, err = file.WriteAt(frame, 0); err != nil {
		return err
	}
	_, err = file.WriteAt(archive, 4096)
	return err
}

func corruptArchiveDigest(file *os.File) error {
	header := make([]byte, 4096)
	if _, err := file.ReadAt(header, 0); err != nil {
		return err
	}
	index := bytes.IndexByte(header, '{')
	if index != 0 {
		return os.ErrInvalid
	}
	var raw map[string]any
	if err := json.Unmarshal(bytes.TrimRight(header, "\x00"), &raw); err != nil {
		return err
	}
	raw["ArchiveSHA256"] = strings.Repeat("4", 64)
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	clear(header)
	copy(header, data)
	_, err = file.WriteAt(header, 0)
	return err
}
