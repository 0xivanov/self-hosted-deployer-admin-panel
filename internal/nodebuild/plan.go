// Package nodebuild prepares immutable Node build instructions without executing
// uploaded code. A qualified VM executor remains a separate security boundary.
package nodebuild

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/projectarchive"
)

var ErrPlan = errors.New("unsupported Node build configuration")

// Settings are trusted runtime assignment choices, not arbitrary command input.
type Settings struct {
	Architecture string
	SkipBuild    bool
}

type Command struct {
	Program string   `json:"program"`
	Args    []string `json:"args"`
}

type Plan struct {
	Version      int       `json:"version"`
	SourceSHA256 string    `json:"source_sha256"`
	OS           string    `json:"os"`
	Architecture string    `json:"architecture"`
	NodeMajor    int       `json:"node_major"`
	Steps        []Command `json:"steps"`
	Start        Command   `json:"start"`
	Port         int       `json:"port"`
}

// Prepare revalidates the source before deriving commands. The runner must check
// SourceSHA256 again before extraction and use the same pinned Linux toolchain and
// architecture for build and runtime. A plan is neither permission to execute nor
// proof of VM isolation. All npm hooks and scripts are untrusted code.
func Prepare(ctx context.Context, source []byte, expectedSHA256 string, settings Settings) (Plan, error) {
	if settings.Architecture != "amd64" && settings.Architecture != "arm64" {
		return Plan{}, ErrPlan
	}
	manifest, err := projectarchive.Validate(ctx, source, "node")
	if err != nil {
		return Plan{}, err
	}
	if manifest.SHA256 != expectedSHA256 {
		return Plan{}, errors.New("Node source digest does not match the assigned upload")
	}
	archive, err := zip.NewReader(bytes.NewReader(source), int64(len(source)))
	if err != nil {
		return Plan{}, err
	}
	var pkg struct {
		Scripts        map[string]string `json:"scripts"`
		PackageManager string            `json:"packageManager"`
	}
	for _, file := range archive.File {
		if file.Name != "package.json" {
			continue
		}
		reader, e := file.Open()
		if e != nil {
			return Plan{}, e
		}
		data, e := io.ReadAll(io.LimitReader(reader, projectarchive.MaxFile+1))
		closeErr := reader.Close()
		if e != nil || closeErr != nil || len(data) > projectarchive.MaxFile || json.Unmarshal(data, &pkg) != nil {
			return Plan{}, ErrPlan
		}
		break
	}
	if pkg.PackageManager != "" && !strings.HasPrefix(pkg.PackageManager, "npm@") {
		return Plan{}, errors.New("Node builds currently require npm and package-lock.json")
	}
	if err = ctx.Err(); err != nil {
		return Plan{}, err
	}
	p := Plan{Version: 1, SourceSHA256: manifest.SHA256, OS: "linux", Architecture: settings.Architecture, NodeMajor: 24, Port: 3000}
	// engine-strict makes incompatible package engine declarations fail explicitly.
	// Dependency hooks stay enabled inside the isolated builder for native modules.
	p.Steps = []Command{{Program: "npm", Args: []string{"ci", "--include=dev", "--engine-strict", "--no-audit", "--no-fund"}}}
	if !settings.SkipBuild && strings.TrimSpace(pkg.Scripts["build"]) != "" {
		p.Steps = append(p.Steps, Command{Program: "npm", Args: []string{"run", "build"}})
	}
	p.Steps = append(p.Steps, Command{Program: "npm", Args: []string{"prune", "--omit=dev", "--ignore-scripts", "--offline", "--no-audit", "--no-fund"}})
	p.Start = Command{Program: "npm", Args: []string{"start", "--ignore-scripts"}}
	return p, nil
}
