// Render a trusted lab assignment. Does not install or start services.
package main

import (
	"encoding/json"
	"fmt"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodelaunch"
	"os"
	"strings"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: node-runtime-unit OPERATION ARTIFACT_SHA256")
		os.Exit(1)
	}
	unit, err := nodelaunch.Render(nodelaunch.Assignment{UID: 60000, OperationID: os.Args[1], ProjectID: strings.Repeat("8", 64), RuntimeID: strings.Repeat("7", 64), ArtifactSHA256: os.Args[2], ToolchainSHA256: "5f4ddab610c1ab2016b3c227cebdbf6d9495161487e4739c7b90090595f465f7", Architecture: "arm64", ReleaseDirectory: "release-" + os.Args[2], Port: 31877})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err = json.NewEncoder(os.Stdout).Encode(unit); err != nil {
		os.Exit(1)
	}
}
