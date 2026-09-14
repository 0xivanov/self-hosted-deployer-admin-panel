//go:build darwin || linux

// Package nodepipeline reads controller-owned records from the isolated Mac
// build pipeline. Guest metadata alone is never retirement evidence.
package nodepipeline

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"syscall"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodeartifact"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

var ErrEvidence = errors.New("completed isolated build evidence unavailable")

// Reader requires a private operator-owned execution root and the original
// trusted dispatch request. Keep this root outside customer/guest access and
// preserve its permanent attempt records. Reader does not submit or retry work.
type Reader struct {
	Root    *os.Root
	Request portal.NodeExecutionRequest
}

type result struct {
	ExecutionID, BuildID, ProjectID, SourceSHA256, ToolchainSHA256, Architecture         string
	DependencyManifestSHA256, ArtifactSHA256, SnapshotSHA256, ExportOperationID, Outcome string
}
type receipt struct {
	Operation      string  `json:"operation"`
	ObservedAt     float64 `json:"observed_at"`
	Deadline       float64 `json:"deadline"`
	State          string  `json:"state"`
	NetworkDevices *int    `json:"network_devices"`
	StorageDevices *int    `json:"storage_devices"`
	BuildDisks     bool    `json:"build_disks"`
	ExportDisks    bool    `json:"export_disks"`
}

func validID(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func readPrivate(root *os.Root, name string, maximum int64) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maximum {
		return nil, ErrEvidence
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, ErrEvidence
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) {
		return nil, ErrEvidence
	}
	data, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return nil, ErrEvidence
	}
	return data, nil
}
func decode(root *os.Root, name string, target any) error {
	data, err := readPrivate(root, name, 16384)
	if err != nil {
		return err
	}
	if json.Unmarshal(data, target) != nil {
		return ErrEvidence
	}
	return nil
}

// InspectNodeExecution accepts terminal failure only after the same controller
// identity, permanent attempt, directory lock and two-VM retirement checks used
// for success. A failed build never returns an archive.
func (r Reader) InspectNodeExecution(ctx context.Context, id string) (portal.NodeExecutionObservation, error) {
	observation, _, err := r.read(ctx, id, true)
	return observation.NodeExecutionObservation, err
}

func (r Reader) ReadNodeArtifact(ctx context.Context, id string) (portal.NodeArtifactObservation, []byte, error) {
	return r.read(ctx, id, false)
}

func (r Reader) read(ctx context.Context, id string, allowFailure bool) (portal.NodeArtifactObservation, []byte, error) {
	var empty portal.NodeArtifactObservation
	q := r.Request
	if r.Root == nil || id != q.ExecutionID || !validID(id) || !validID(q.ProjectID) || !validID(q.BuildID) || !validID(q.ToolchainSHA256) || !validID(q.Plan.SourceSHA256) || !validID(q.Bundle.ManifestSHA256) {
		return empty, nil, ErrEvidence
	}
	info, err := r.Root.Stat(".")
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return empty, nil, ErrEvidence
	}
	lock, err := r.Root.Open(".")
	if err != nil {
		return empty, nil, ErrEvidence
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) != nil {
		return empty, nil, ErrEvidence
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err = ctx.Err(); err != nil {
		return empty, nil, err
	}
	var stored portal.NodeExecutionRequest
	if decode(r.Root, "request.json", &stored) != nil {
		return empty, nil, ErrEvidence
	}
	q.Archive = nil
	if !reflect.DeepEqual(stored, q) {
		return empty, nil, ErrEvidence
	}
	if _, err = readPrivate(r.Root, "pipeline-attempt.json", 16384); err != nil {
		return empty, nil, ErrEvidence
	}
	var out result
	if decode(r.Root, "result.json", &out) != nil || (out.Outcome != "succeeded" && out.Outcome != "failed") || out.ExecutionID != id || out.ProjectID != q.ProjectID || out.BuildID != q.BuildID || out.SourceSHA256 != q.Plan.SourceSHA256 || out.ToolchainSHA256 != q.ToolchainSHA256 || out.Architecture != q.Plan.Architecture || out.DependencyManifestSHA256 != q.Bundle.ManifestSHA256 || (out.Outcome == "succeeded" && !validID(out.ArtifactSHA256)) || (out.Outcome == "failed" && out.ArtifactSHA256 != "") || !validID(out.SnapshotSHA256) || !validID(out.ExportOperationID) || out.ExportOperationID == id {
		return empty, nil, ErrEvidence
	}
	now := float64(time.Now().UnixNano()) / 1e9
	var buildStopped float64
	for _, stage := range []struct {
		name, id string
		count    int
		export   bool
	}{{"build", id, 3, false}, {"export", out.ExportOperationID, 4, true}} {
		var attempted, running, stopped receipt
		if decode(r.Root, stage.name+"/attempt.json", &attempted) != nil || decode(r.Root, stage.name+"/running.json", &running) != nil || decode(r.Root, stage.name+"/stopped.json", &stopped) != nil {
			return empty, nil, ErrEvidence
		}
		if attempted.Operation != stage.id || running.Operation != stage.id || stopped.Operation != stage.id || attempted.ExportDisks != stage.export || attempted.BuildDisks == stage.export || attempted.Deadline <= 0 || running.ExportDisks != stage.export || running.BuildDisks == stage.export || stopped.ExportDisks != stage.export || stopped.BuildDisks == stage.export || running.NetworkDevices == nil || *running.NetworkDevices != 0 || running.StorageDevices == nil || *running.StorageDevices != stage.count || stopped.State != "stopped" || running.ObservedAt <= 0 || stopped.ObservedAt < running.ObservedAt || stopped.ObservedAt > now {
			return empty, nil, ErrEvidence
		}
		if !stage.export {
			buildStopped = stopped.ObservedAt
			if attempted.Deadline > float64(q.NotAfter) {
				return empty, nil, ErrEvidence
			}
		} else if running.ObservedAt < buildStopped {
			return empty, nil, ErrEvidence
		}
	}
	if out.Outcome == "failed" {
		if !allowFailure {
			return empty, nil, ErrEvidence
		}
		observation := portal.NodeExecutionObservation{ProjectID: q.ProjectID, ExecutionID: id, SourceSHA256: q.Plan.SourceSHA256, ToolchainSHA256: q.ToolchainSHA256, Architecture: q.Plan.Architecture, Outcome: "failed", Retired: true, ObservedAt: time.Now()}
		return portal.NodeArtifactObservation{NodeExecutionObservation: observation, DependencyManifestSHA256: q.Bundle.ManifestSHA256}, nil, nil
	}
	// Validate the raw export again against trusted identities and compare its
	// bytes with the archive committed by the importer. Do not mount any disk.
	disk, err := r.Root.Open("export/output.disk")
	if err != nil {
		return empty, nil, ErrEvidence
	}
	defer disk.Close()
	di, err := disk.Stat()
	if err != nil || !di.Mode().IsRegular() || di.Mode().Perm()&0077 != 0 {
		return empty, nil, ErrEvidence
	}
	data, manifest, err := nodeartifact.ReadExport(ctx, disk, di.Size(), id, q.Plan.SourceSHA256, out.SnapshotSHA256)
	if err != nil || manifest.SHA256 != out.ArtifactSHA256 {
		return empty, nil, ErrEvidence
	}
	saved, err := readPrivate(r.Root, "release.zip", nodeartifact.MaxCompressed)
	if err != nil {
		return empty, nil, err
	}
	retained, err := nodeartifact.Validate(ctx, saved, manifest.SHA256)
	if err != nil || retained != manifest {
		return empty, nil, ErrEvidence
	}
	observation := portal.NodeExecutionObservation{ProjectID: q.ProjectID, ExecutionID: id, SourceSHA256: q.Plan.SourceSHA256, ToolchainSHA256: q.ToolchainSHA256, Architecture: q.Plan.Architecture, Outcome: "succeeded", Retired: true, ObservedAt: time.Now()}
	return portal.NodeArtifactObservation{NodeExecutionObservation: observation, ArtifactSHA256: manifest.SHA256, DependencyManifestSHA256: q.Bundle.ManifestSHA256}, data, nil
}
