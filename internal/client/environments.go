package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// CreateEnvironment stages an immutable environment without exposing values in
// command arguments, errors, or deployment configuration.
func (c *CLI) CreateEnvironment(ctx context.Context, app, revision string, values map[string]string) error {
	if len(app) > 63 || !registryAppNamePattern.MatchString(app) || !registryRevisionPattern.MatchString(revision) || !validEnvironmentValues(values) {
		return errors.New("invalid environment settings")
	}
	if c.directory == "" {
		return errors.New("environment operation unavailable")
	}
	if values == nil {
		values = map[string]string{}
	}
	payload, err := json.Marshal(values)
	if err != nil || len(payload) > 256<<10 {
		return errors.New("invalid environment settings")
	}
	file, err := os.CreateTemp(c.directory, ".environment-*")
	if err != nil {
		return errors.New("environment operation failed")
	}
	path := file.Name()
	defer os.Remove(path)
	if err = file.Chmod(0600); err != nil {
		file.Close()
		return errors.New("environment operation failed")
	}
	if _, err = file.Write(payload); err != nil {
		file.Close()
		return errors.New("environment operation failed")
	}
	if err = file.Close(); err != nil {
		return errors.New("environment operation failed")
	}
	data, err := c.run(ctx, "environment", "create", "--values", path, app, revision)
	if err != nil {
		return errors.New("environment operation failed")
	}
	var result struct {
		AppName   string   `json:"app_name"`
		Revision  string   `json:"revision"`
		Names     []string `json:"names"`
		CreatedAt string   `json:"created_at"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&result); err != nil {
		return errors.New("environment operation returned invalid metadata")
	}
	var trailing any
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	if err = decoder.Decode(&trailing); err != io.EOF || result.AppName != app || result.Revision != revision || !slices.Equal(names, result.Names) {
		return errors.New("environment operation returned invalid metadata")
	}
	return nil
}

func validEnvironmentValues(values map[string]string) bool {
	if len(values) > 64 {
		return false
	}
	total := 0
	for name, value := range values {
		if len(name) > 128 || !environmentNamePattern.MatchString(name) || len(value) > 8192 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return false
		}
		total += len(name) + len(value)
	}
	return total <= 32768
}
