package exporter

import (
	"fmt"
	"os"
	"strings"

	yaml "go.yaml.in/yaml/v2"
)

// Config is the on-disk YAML configuration for the exporter.
type Config struct {
	RedactKeySubstrings []string `yaml:"redact_key_substrings"`
}

// LoadConfig reads and parses the YAML config file at path. The returned
// Config's RedactKeySubstrings are normalized: trimmed, lowercased, and with
// empty entries removed.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	cfg.RedactKeySubstrings = normalizeSubstrings(cfg.RedactKeySubstrings)
	return &cfg, nil
}

// normalizeSubstrings trims and lowercases each entry, dropping empties.
func normalizeSubstrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
