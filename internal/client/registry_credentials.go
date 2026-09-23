package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/registryimage"
)

var registryAppNamePattern = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)
var registryRevisionPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

const (
	maxRegistryUsername = 256
	maxRegistryPassword = 8192
)

type registryCredentialResult struct {
	AppName   string `json:"app_name"`
	Revision  string `json:"revision"`
	Registry  string `json:"registry"`
	CreatedAt string `json:"created_at"`
}

// CreateRegistryCredential stores a registry login through the authenticated
// deployer CLI without placing the login in process arguments or output.
func (c *CLI) CreateRegistryCredential(ctx context.Context, app, revision, registry string, creds registryimage.Credentials) error {
	if !validRegistryCredentialTarget(app, revision, registry) || !validRegistryLogin(creds) {
		return errors.New("invalid registry credential")
	}
	if c.directory == "" {
		return errors.New("registry credential operation unavailable")
	}
	payload, err := json.Marshal(struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{Username: creds.Username, Password: creds.Password})
	if err != nil || len(payload) > 16<<10 {
		return errors.New("invalid registry credential")
	}
	file, err := os.CreateTemp(c.directory, ".registry-credentials-*")
	if err != nil {
		return errors.New("registry credential operation failed")
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return errors.New("registry credential operation failed")
	}
	if _, err := file.Write(payload); err != nil {
		file.Close()
		return errors.New("registry credential operation failed")
	}
	if err := file.Close(); err != nil {
		return errors.New("registry credential operation failed")
	}
	data, err := c.run(ctx, "registry", "create", "--credentials", path, app, revision, registry)
	if err != nil {
		return errors.New("registry credential operation failed")
	}
	var result registryCredentialResult
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return errors.New("registry credential operation returned invalid metadata")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF || result.AppName != app || result.Revision != revision || result.Registry != registry {
		return errors.New("registry credential operation returned invalid metadata")
	}
	return nil
}

func validRegistryCredentialTarget(app, revision, registry string) bool {
	return len(app) <= 63 && registryAppNamePattern.MatchString(app) && registryRevisionPattern.MatchString(revision) && (registry == "docker.io" || registry == "ghcr.io")
}

func validRegistryLogin(creds registryimage.Credentials) bool {
	if len(creds.Username) == 0 || len(creds.Username) > maxRegistryUsername || len(creds.Password) == 0 || len(creds.Password) > maxRegistryPassword || strings.Contains(creds.Username, ":") {
		return false
	}
	for _, value := range []string{creds.Username, creds.Password} {
		if !utf8.ValidString(value) {
			return false
		}
		for _, char := range value {
			if char < 32 || char == 127 {
				return false
			}
		}
	}
	return true
}
