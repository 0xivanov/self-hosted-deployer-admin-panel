package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodepipeline"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/npmfetch"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

type pipelineConfig struct {
	TemplateDirectory     string `json:"TemplateDirectory"`
	DependenciesDirectory string `json:"DependenciesDirectory"`
	Launcher              string `json:"Launcher"`
	Importer              string `json:"Importer"`
}

type localSubmitter struct {
	executions *os.Root
	project    string
	job        string
}

func (s localSubmitter) SubmitNodeExecution(ctx context.Context, request portal.NodeExecutionRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if request.ProjectID != s.project || request.BuildID != s.job || !validID(request.ExecutionID) || len(request.Archive) == 0 {
		return errors.New("execution identity mismatch")
	}
	if err := s.executions.Mkdir(request.ExecutionID, 0700); err != nil {
		return err
	}
	parent, err := s.executions.Open(".")
	if err != nil {
		return err
	}
	err = errors.Join(parent.Sync(), parent.Close())
	if err != nil {
		return err
	}
	root, err := s.executions.OpenRoot(request.ExecutionID)
	if err != nil {
		return err
	}
	defer root.Close()
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if err = writeSynced(root, "source.zip", request.Archive); err != nil {
		return err
	}
	return writeSynced(root, "request.json", raw)
}

func writeSynced(root *os.Root, name string, data []byte) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err == nil {
		err = closeErr
	}
	ok = err == nil
	return err
}

func privateRegular(path string, max int64, operatorReadable bool) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || (max > 0 && info.Size() > max) {
		return nil, errors.New("private regular file required")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint32(stat.Uid) != uint32(os.Getuid()) || stat.Nlink != 1 {
		return nil, errors.New("operator-owned regular file required")
	}
	if operatorReadable {
		if info.Mode().Perm()&0022 != 0 || info.Mode().Perm()&0400 == 0 {
			return nil, errors.New("operator-readable pipeline file required")
		}
	} else if info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("private regular file required")
	}
	return info, nil
}

func privateDirectory(path string, exact bool) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 || (exact && info.Mode().Perm() != 0700) {
		return errors.New("private directory required")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint32(stat.Uid) != uint32(os.Getuid()) {
		return errors.New("operator-owned directory required")
	}
	return nil
}

func readConfig(path string) (pipelineConfig, error) {
	var config pipelineConfig
	if _, err := privateRegular(path, 16384, false); err != nil {
		return config, err
	}
	f, err := os.Open(path)
	if err != nil {
		return config, err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 16385))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&config); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return config, errors.New("invalid pipeline configuration")
	}
	for _, path := range []string{config.TemplateDirectory, config.DependenciesDirectory} {
		if !filepath.IsAbs(path) {
			return config, errors.New("pipeline paths must be absolute")
		}
		if err = privateDirectory(path, false); err != nil {
			return config, err
		}
	}
	for _, path := range []string{config.Launcher, config.Importer} {
		if !filepath.IsAbs(path) {
			return config, errors.New("pipeline executables must be absolute")
		}
		info, e := privateRegular(path, 0, false)
		if e != nil || info.Mode()&0111 == 0 {
			return config, errors.New("pipeline executable required")
		}
	}
	return config, nil
}

func validID(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}

func readRequest(root *os.Root) (portal.NodeExecutionRequest, error) {
	var request portal.NodeExecutionRequest
	f, err := root.Open("request.json")
	if err != nil {
		return request, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return request, errors.New("invalid execution request")
	}
	decoder := json.NewDecoder(io.LimitReader(f, 16385))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return request, errors.New("invalid execution request")
	}
	return request, nil
}

func run() error {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return errors.New("local Node builds require macOS arm64")
	}
	database := flag.String("database", "", "Private customer portal database")
	project := flag.String("project", "", "Node project ID")
	toolchain := flag.String("toolchain", "", "Pinned toolchain digest")
	configPath := flag.String("pipeline-config", "", "Private pipeline configuration JSON")
	scriptPath := flag.String("pipeline-script", "", "Pipeline Python script")
	executionsPath := flag.String("executions-directory", "", "Private execution directory")
	resume := flag.String("resume", "", "Resume an existing execution")
	flag.Parse()
	if *database == "" || *project == "" || *toolchain == "" || *configPath == "" || *scriptPath == "" || *executionsPath == "" {
		return errors.New("database, project, toolchain, pipeline-config, pipeline-script, and executions-directory are required")
	}
	if !validID(*project) || !validID(*toolchain) {
		return errors.New("project and toolchain must be SHA-256 identifiers")
	}
	canonical, err := filepath.EvalSymlinks(*executionsPath)
	if err != nil || !filepath.IsAbs(*executionsPath) || canonical != *executionsPath || !filepath.IsAbs(*configPath) || !filepath.IsAbs(*scriptPath) {
		return errors.New("absolute canonical execution directory and absolute configuration/script paths required")
	}
	if err := privateDirectory(*executionsPath, true); err != nil {
		return err
	}
	if _, err := privateRegular(*scriptPath, 0, true); err != nil {
		return err
	}
	config, err := readConfig(*configPath)
	if err != nil {
		return err
	}
	executions, err := os.OpenRoot(*executionsPath)
	if err != nil {
		return err
	}
	defer executions.Close()
	store, err := portal.Open(*database)
	if err != nil {
		return errors.New("portal database unavailable")
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var request portal.NodeExecutionRequest
	if *resume != "" {
		if !validID(*resume) {
			return errors.New("invalid execution ID")
		}
		root, e := executions.OpenRoot(*resume)
		if e != nil {
			return e
		}
		request, err = readRequest(root)
		root.Close()
		if err != nil || request.ExecutionID != *resume || request.ProjectID != *project || request.ToolchainSHA256 != *toolchain {
			return errors.New("execution request does not match assignment")
		}
	} else {
		jobs, e := os.OpenRoot(config.DependenciesDirectory)
		if e != nil {
			return e
		}
		defer jobs.Close()
		prepared, e := store.PrepareNodeBuild(ctx, *project, *toolchain, "arm64", jobs, npmfetch.NewClient())
		if e != nil {
			return e
		}
		if prepared == nil || prepared.Claim == nil || prepared.Bundle == nil {
			return errors.New("no queued Node build available")
		}
		claim := prepared.Claim
		executor := localSubmitter{executions: executions, project: claim.Job.ProjectID, job: claim.Job.ID}
		if e = store.DispatchNodeBuild(ctx, claim.Job.ID, claim.ExecutionID, claim.Lease, jobs, executor); e != nil {
			return e
		}
		// Dispatch persisted the complete request; read it back as the immutable
		// request used by the pipeline and evidence reader.
		root, e := executions.OpenRoot(claim.ExecutionID)
		if e != nil {
			return e
		}
		request, e = readRequest(root)
		root.Close()
		if e != nil {
			return e
		}
	}

	executionPath := filepath.Join(*executionsPath, request.ExecutionID)
	if *resume == "" {
		command := exec.CommandContext(ctx, "/usr/bin/python3", *scriptPath, *configPath, executionPath)
		if err = command.Run(); err != nil {
			return errors.New("Node build pipeline failed")
		}
	}
	root, err := executions.OpenRoot(request.ExecutionID)
	if err != nil {
		return err
	}
	defer root.Close()
	release, err := store.RetainNodeRelease(ctx, nodepipeline.Reader{Root: root, Request: request}, *project, request.BuildID)
	if err != nil {
		return err
	}
	if release == nil {
		return errors.New("Node build did not produce a release")
	}
	return json.NewEncoder(os.Stdout).Encode(release)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
