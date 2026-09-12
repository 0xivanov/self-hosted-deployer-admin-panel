//go:build integration

package noderuntime

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

func fixture() (Assignment, portal.NodeRuntimeRequest) {
	a := Assignment{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), "arm64"}
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	f, _ := z.Create("package.json")
	f.Write([]byte(`{"scripts":{"start":"node server.js"}}`))
	f, _ = z.Create("server.js")
	f.Write([]byte("console.log('test')"))
	z.Close()
	digest := sha256.Sum256(buf.Bytes())
	return a, portal.NodeRuntimeRequest{ProjectID: a.ProjectID, RuntimeID: a.RuntimeID, ToolchainSHA256: a.ToolchainSHA256, Architecture: a.Architecture, OperationID: strings.Repeat("d", 64), DeploymentID: strings.Repeat("e", 64), ReleaseID: strings.Repeat("f", 64), ArtifactSHA256: hex.EncodeToString(digest[:]), Revision: 1, ActivateBefore: time.Now().Add(time.Minute).Unix(), Archive: buf.Bytes()}
}
func TestAcceptedDeploymentSurvivesRestartWithoutDuplicateClaim(t *testing.T) {
	t.Parallel()
	a, request := fixture()
	path := filepath.Join(t.TempDir(), "private", "inbox.db")
	inbox, err := Open(path, a)
	if err != nil {
		t.Fatal(err)
	}
	defer inbox.Close()
	if err = inbox.SubmitNodeRuntime(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err = inbox.SubmitNodeRuntime(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	work, err := inbox.Claim(t.Context())
	if err != nil || work == nil || work.Request.OperationID != request.OperationID || !bytes.Equal(work.Request.Archive, request.Archive) {
		t.Fatal(work, err)
	}
	if err = inbox.Close(); err != nil {
		t.Fatal(err)
	}
	inbox, err = Open(path, a)
	if err != nil {
		t.Fatal(err)
	}
	defer inbox.Close()
	if work, err = inbox.Claim(t.Context()); err != nil || work != nil {
		t.Fatal("reclaimed running operation", work, err)
	}
	if work, err = inbox.Processing(t.Context()); err != nil || work == nil || work.Request.OperationID != request.OperationID {
		t.Fatal(work, err)
	}
	if err = inbox.Settle(t.Context(), request.OperationID); err != nil {
		t.Fatal(err)
	}
	if err = inbox.SubmitNodeRuntime(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if work, err = inbox.Claim(t.Context()); err != nil || work != nil {
		t.Fatal("settled request requeued", work, err)
	}
	request.OperationID = strings.Repeat("1", 64)
	request.DeploymentID = strings.Repeat("2", 64)
	request.Revision = 2
	if err = inbox.SubmitNodeRuntime(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if work, err = inbox.Claim(t.Context()); err != nil || work == nil || work.Request.Revision != 2 {
		t.Fatal(work, err)
	}
}
func TestInboxRejectsScopeExpiryAndIdentityChanges(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"runtime", "toolchain", "expired", "archive", "revision"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, request := fixture()
			inbox, err := Open(filepath.Join(t.TempDir(), "private", "inbox.db"), a)
			if err != nil {
				t.Fatal(err)
			}
			defer inbox.Close()
			switch name {
			case "runtime":
				request.RuntimeID = strings.Repeat("0", 64)
			case "toolchain":
				request.ToolchainSHA256 = strings.Repeat("0", 64)
			case "expired":
				request.ActivateBefore = time.Now().Add(-time.Minute).Unix()
			case "archive":
				request.Archive = []byte("corrupted")
			case "revision":
				request.Revision = 0
			}
			if err = inbox.SubmitNodeRuntime(t.Context(), request); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
	a, request := fixture()
	inbox, err := Open(filepath.Join(t.TempDir(), "private", "inbox.db"), a)
	if err != nil {
		t.Fatal(err)
	}
	defer inbox.Close()
	if err = inbox.SubmitNodeRuntime(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	request.ActivateBefore++
	if err = inbox.SubmitNodeRuntime(t.Context(), request); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	request.OperationID = strings.Repeat("1", 64)
	if err = inbox.SubmitNodeRuntime(t.Context(), request); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
