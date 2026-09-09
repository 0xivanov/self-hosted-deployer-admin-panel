// Package appconfig preserves complete configurations without owning backend validation.
package appconfig

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"regexp"
)

type Config map[string]any

var ValidName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func Parse(data []byte) (Config, error) {
	var decoded map[string]any
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	cfg := Config(decoded)
	if !ValidName.MatchString(cfg.Name()) || cfg.Image() == "" {
		return nil, fmt.Errorf("provide a valid app name and image")
	}
	return cfg, nil
}
func (c Config) Name() string  { s, _ := c["name"].(string); return s }
func (c Config) Image() string { s, _ := c["image"].(string); return s }
func (c Config) Domain() string {
	m, _ := c["routing"].(map[string]any)
	s, _ := m["domain"].(string)
	return s
}
func (c Config) StateMode() string {
	m, _ := c["state"].(map[string]any)
	s, _ := m["mode"].(string)
	if s == "" {
		return "stateless"
	}
	return s
}
func (c Config) Replicas() int {
	m, _ := c["deploy"].(map[string]any)
	switch n := m["replicas"].(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 1
}
