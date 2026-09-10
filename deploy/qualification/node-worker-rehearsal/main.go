// Synthetic guest-only qualification of the portal's Node worker path.
// This executor accepts one exact checked-in fixture, never customer projects.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/npmfetch"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

const sourceDigest = "a24bf064e7c4a6f2d50edc8875ac468a5c41dcd5f6a64cdea2144082c09e5739" // checked below against the fixture, not customer input
const toolchainDigest = "5f4ddab610c1ab2016b3c227cebdbf6d9495161487e4739c7b90090595f465f7"
const base = "/opt/node-positive-lab"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type fixtureDownloader struct{ manifest npmfetch.Manifest }

func (d fixtureDownloader) Fetch(ctx context.Context, t npmfetch.Tarball) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, p := range d.manifest.Tarballs {
		if p.URL == t.URL && p.Integrity == t.Integrity && filepath.Base(p.File) == p.File {
			return os.ReadFile(filepath.Join(base, "packages", p.File))
		}
	}
	return nil, errors.New("dependency is outside the trusted fixture")
}

type fixtureExecutor struct {
	root     *os.Root
	source   []byte
	manifest npmfetch.Manifest
	calls    int
	report   json.RawMessage
}

func (e *fixtureExecutor) SubmitNodeExecution(ctx context.Context, r portal.NodeExecutionRequest) error {
	e.calls++
	if !bytes.Equal(r.Archive, e.source) || r.ToolchainSHA256 != toolchainDigest || r.Plan.SourceSHA256 != sourceDigest || r.NotAfter <= time.Now().Unix() {
		return errors.New("non-fixture or expired execution rejected")
	}
	plan, err := nodebuild.Prepare(ctx, e.source, sourceDigest, nodebuild.Settings{Architecture: "arm64"})
	if err != nil || !reflect.DeepEqual(r.Plan, plan) {
		return errors.New("unexpected fixture build plan")
	}
	verified, err := npmfetch.VerifyBundle(ctx, e.root, r.Bundle, sourceDigest)
	if err != nil {
		return err
	}
	// The restricted service consumes the root-owned staged fixture. Prove that
	// these are exactly the inputs prepared and dispatched by the portal worker.
	if !reflect.DeepEqual(verified, e.manifest) {
		return errors.New("staged dependencies differ from worker bundle")
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/python3", "/tmp/node-positive-run.py", sourceDigest, "--dependency", "--execution-id", r.ExecutionID, "--not-after", strconv.FormatInt(r.NotAfter, 10))
	output := &boundedOutput{}
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.WaitDelay = 2 * time.Second
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("restricted guest execution: %w: %s", err, output.data.Bytes())
	}
	if output.exceeded {
		return errors.New("guest report exceeded qualification limit")
	}
	var report struct {
		RestrictedNode struct {
			Result string `json:"result"`
		} `json:"restricted_node"`
	}
	if err = json.Unmarshal(output.data.Bytes(), &report); err != nil || report.RestrictedNode.Result != "passed" {
		return errors.New("guest execution did not pass")
	}
	e.report = append([]byte(nil), output.data.Bytes()...)
	// Exercise the guest's retained attempt record too, bypassing the portal's
	// duplicate guard. The same ID must fail before any service can be started.
	repeat := exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	repeat.WaitDelay = 2 * time.Second
	rejected := &boundedOutput{}
	repeat.Stdout = rejected
	repeat.Stderr = rejected
	if err = repeat.Run(); err == nil || !bytes.Contains(rejected.data.Bytes(), []byte("FileExistsError")) {
		return errors.New("guest did not reject the repeated execution identity")
	}
	return nil
}

type boundedOutput struct {
	data     bytes.Buffer
	exceeded bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 256*1024 - b.data.Len()
	if len(p) > remaining {
		p = p[:remaining]
		b.exceeded = true
	}
	b.data.Write(p)
	return n, nil
}

func run() error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" || os.Getuid() == 0 {
		return errors.New("run only as the unprivileged ARM64 Linux lab user")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	source, err := os.ReadFile(base + "/source.zip")
	if err != nil {
		return err
	}
	sum := sha256.Sum256(source)
	if hex.EncodeToString(sum[:]) != sourceDigest {
		return errors.New("staged source is not the pinned dependency fixture")
	}
	toolchain, err := os.ReadFile(base + "/node.tar.xz")
	if err != nil {
		return err
	}
	sum = sha256.Sum256(toolchain)
	if hex.EncodeToString(sum[:]) != toolchainDigest {
		return errors.New("toolchain digest mismatch")
	}
	raw, err := os.ReadFile(base + "/packages/bundle.json")
	if err != nil {
		return err
	}
	var manifest npmfetch.Manifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "node-worker-qualification-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	s, err := portal.Open(filepath.Join(directory, "db", "portal.db"))
	if err != nil {
		return err
	}
	defer s.Close()
	a, verification, err := s.Register(ctx, "node-worker@example.test", "synthetic-node-worker-password", "Node worker lab")
	if err != nil {
		return err
	}
	if err = s.Verify(ctx, verification); err != nil {
		return err
	}
	session, err := s.Login(ctx, a.Email, "synthetic-node-worker-password")
	if err != nil {
		return err
	}
	project, err := s.CreateProject(ctx, session.Token, a.WorkspaceID, "Node worker fixture", "node")
	if err != nil {
		return err
	}
	upload, err := s.SaveUpload(ctx, session.Token, project.ID, source)
	if err != nil {
		return err
	}
	job, err := s.RequestNodeBuild(ctx, session.Token, project.ID, upload.ID, "node-worker-fixture-request", portal.NodeBuildAssignment{Settings: nodebuild.Settings{Architecture: "arm64"}, ToolchainSHA256: toolchainDigest})
	if err != nil {
		return err
	}
	prepared, err := s.PrepareNodeBuild(ctx, project.ID, toolchainDigest, "arm64", root, fixtureDownloader{manifest})
	if err != nil {
		return err
	}
	if prepared == nil || prepared.Bundle == nil || prepared.Claim.Job.ID != job.ID {
		return errors.New("worker failed to prepare assigned build")
	}
	executor := &fixtureExecutor{root: root, source: source, manifest: manifest}
	c := prepared.Claim
	if err = s.DispatchNodeBuild(ctx, job.ID, c.ExecutionID, c.Lease, root, executor); err != nil {
		return err
	}
	// Reopen the database to prove that a successful real run also cannot be
	// automatically submitted twice. No success/artifact activation is inferred.
	if err = s.Close(); err != nil {
		return err
	}
	reopened, err := portal.Open(filepath.Join(directory, "db", "portal.db"))
	if err != nil {
		return err
	}
	defer reopened.Close()
	if err = reopened.DispatchNodeBuild(ctx, job.ID, c.ExecutionID, c.Lease, root, executor); !errors.Is(err, portal.ErrBuildConflict) {
		return fmt.Errorf("restart redispatch was not rejected: %v", err)
	}
	if executor.calls != 1 {
		return errors.New("duplicate executor call")
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"worker_flow": "passed", "source_sha256": sourceDigest, "execution_id": c.ExecutionID, "executor_calls": executor.calls, "restart_redispatch_rejected": true, "guest_duplicate_rejected": true, "guest": executor.report})
}
