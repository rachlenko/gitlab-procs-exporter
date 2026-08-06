package exporter

import (
	"fmt"
	"math"
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
	// overridden, and only with a limit between minLabelBytes and
	// maxLabelBytesCeiling; anything else fails the load. Absent entries keep
	// their default.
	MaxLabelBytes LabelByteLimits `yaml:"max_label_bytes"`
}

// LabelByteLimits is the type of Config.MaxLabelBytes. It is a named type only
// so it can carry the decoder below: yaml.v2 coerces a float or exponent scalar
// into an int field by TRUNCATION, so a plain map[string]int field accepts
// `ci_job_name: 512.9` as 512 and `1e3` as 1000 — the operator's limit silently
// replaced by a different one. That is the same silently-altered-setting failure
// every other check in this file fails the load over, so it fails the load too.
type LabelByteLimits map[string]int

// UnmarshalYAML decodes the mapping through interface{} so each scalar's YAML
// type survives long enough to be checked, and rejects anything that is not an
// integer. Entries are visited in sorted order, so a file with several bad
// values always names the same one.
func (l *LabelByteLimits) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var raw map[string]interface{}
	if err := unmarshal(&raw); err != nil {
		return err
	}
	out := make(LabelByteLimits, len(raw))
	for _, name := range sortedKeys(raw) {
		limit, ok := labelByteLimit(raw[name])
		if !ok {
			return fmt.Errorf("max_label_bytes[%q]: %v is not an integer byte count", name, raw[name])
		}
		out[name] = limit
	}
	*l = out
	return nil
}

// labelByteLimit converts one decoded scalar to an int, reporting whether it was
// an integer at all. yaml.v2 resolves an integer scalar to int when it fits and
// to int64/uint64 when it does not; those are clamped rather than rejected, so a
// 20-digit limit is reported as being over the ceiling — which it is — instead
// of as a non-integer, which it is not.
func labelByteLimit(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		switch {
		case n > math.MaxInt:
			return math.MaxInt, true
		case n < math.MinInt:
			return math.MinInt, true
		}
		return int(n), true
	case uint64:
		if n > uint64(math.MaxInt) {
			return math.MaxInt, true
		}
		return int(n), true
	}
	return 0, false
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
	// Strict: a misspelled TOP-LEVEL key is the one config typo that fails
	// silently and dangerously. `redact_key_substring` (singular) parses fine
	// under non-strict unmarshal, yields an empty list, and the exporter comes up
	// healthy while publishing every value the operator asked to have scrubbed.
	// A typo INSIDE max_label_bytes already aborts the load; this makes the key
	// itself just as loud, and matches the same rule the whole contract rests on:
	// a silently ignored setting is indistinguishable from one being applied.
	if err := yaml.UnmarshalStrict(data, &cfg); err != nil {
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
		case limit > maxLabelBytesCeiling:
			return fmt.Errorf(
				"max_label_bytes[%q]: %d is above the %d-byte ceiling; a cut value carries a "+
					"%d-byte marker past the limit, so anything higher can emit a label value over "+
					"the %d-byte label_value_length_limit this exporter is deployed with, and "+
					"Prometheus rejects the WHOLE scrape rather than the one value",
				name, limit, maxLabelBytesCeiling, maxMarkerLen, maxEnvironBytes)
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
func sortedKeys[V any](m map[string]V) []string {
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
