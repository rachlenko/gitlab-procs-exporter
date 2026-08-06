package exporter

import (
	"fmt"
	"os"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v2"
)

// Config is the on-disk YAML configuration for the exporter.
type Config struct {
	RedactKeySubstrings []string `yaml:"redact_key_substrings"`

	// MaxLabelBytes overrides individual entries of the published
	// MaxLabelBytes contract. Only labels already in that table may be
	// overridden, and only with a limit of at least minLabelBytes; anything
	// else fails the load. Absent entries keep their default.
	MaxLabelBytes map[string]int `yaml:"max_label_bytes"`
}

// LoadConfig reads and parses the YAML config file at path. The returned
// Config's RedactKeySubstrings are normalized: trimmed, lowercased, and with
// empty entries removed, and its MaxLabelBytes overrides are validated.
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
	if err := validateMaxLabelBytes(cfg.MaxLabelBytes); err != nil {
		return nil, fmt.Errorf("config %q: %w", path, err)
	}
	return &cfg, nil
}

// validateMaxLabelBytes rejects operator overrides that cannot work, failing
// the whole load rather than dropping the bad entry: an override that is
// silently ignored is indistinguishable from a limit that is being applied,
// which is exactly the failure mode this contract exists to remove.
func validateMaxLabelBytes(overrides map[string]int) error {
	// Sorted so a config with several bad entries always reports the same one.
	for _, name := range sortedKeys(overrides) {
		if _, ok := MaxLabelBytes[name]; !ok {
			return fmt.Errorf("max_label_bytes: unknown label %q (the contract covers: %s)",
				name, strings.Join(contractLabelNames(), ", "))
		}
		switch limit := overrides[name]; {
		case limit <= 0:
			return fmt.Errorf("max_label_bytes[%q]: %d is not a positive byte count", name, limit)
		case limit < minLabelBytes:
			return fmt.Errorf(
				"max_label_bytes[%q]: %d is below the %d-byte truncation marker, so a cut value "+
					"would be longer than the limit and almost entirely marker", name, limit, minLabelBytes)
		}
	}
	return nil
}

// contractLabelNames lists the overridable labels, sorted, for error messages —
// a rejected typo is only actionable if it says what the alternatives are.
func contractLabelNames() []string {
	return sortedKeys(MaxLabelBytes)
}

// sortedKeys returns m's keys in a stable order, so error messages naming one
// entry of a map don't vary between runs.
func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
