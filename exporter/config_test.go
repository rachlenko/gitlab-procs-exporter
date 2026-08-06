package exporter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "redact_key_substrings:\n  - Vault\n  - \"  Internal_Token  \"\n  - \"\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"vault", "internal_token"} // trimmed, lowercased, empty dropped
	if len(cfg.RedactKeySubstrings) != len(want) {
		t.Fatalf("got %v, want %v", cfg.RedactKeySubstrings, want)
	}
	for i := range want {
		if cfg.RedactKeySubstrings[i] != want[i] {
			t.Errorf("index %d: got %q, want %q", i, cfg.RedactKeySubstrings[i], want[i])
		}
	}
}

func TestNormalizeSubstrings(t *testing.T) {
	got := normalizeSubstrings([]string{"  Vault  ", "TOKEN", "", "  ", "Api_Key"})
	want := []string{"vault", "token", "api_key"} // trimmed, lowercased, blanks dropped, order preserved
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d: got %q, want %q", i, got[i], want[i])
		}
	}
	if n := normalizeSubstrings(nil); len(n) != 0 {
		t.Errorf("nil input: expected empty slice, got %v", n)
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadConfigMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("redact_key_substrings: [unterminated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Error("expected error for malformed YAML")
	}
}

func TestLoadConfigEmptyField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(path, []byte("# no rules here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.RedactKeySubstrings) != 0 {
		t.Errorf("expected empty slice, got %v", cfg.RedactKeySubstrings)
	}
}

// writeConfig writes a YAML config into a fresh temp dir and returns its path.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// An override changes exactly one entry and leaves every other label at its
// default — a config knob that silently resets the rest of the contract would
// be worse than no knob at all.
func TestLoadConfigMaxLabelBytesOverridesOneEntry(t *testing.T) {
	// Arrange
	path := writeConfig(t, "max_label_bytes:\n  ci_job_name: 512\n")

	// Act
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	limits := mergedMaxLabelBytes(cfg.MaxLabelBytes)

	// Assert
	if limits["ci_job_name"] != 512 {
		t.Errorf("ci_job_name: got %d, want the override 512", limits["ci_job_name"])
	}
	for label, want := range MaxLabelBytes {
		if label == "ci_job_name" {
			continue
		}
		if limits[label] != want {
			t.Errorf("label %q: got %d, want the default %d", label, limits[label], want)
		}
	}
	if len(limits) != len(MaxLabelBytes) {
		t.Errorf("merged table has %d entries, want %d — an override must not add labels",
			len(limits), len(MaxLabelBytes))
	}
	// The package-level contract is shared by every collector in the process;
	// merging must copy, never mutate it in place.
	if MaxLabelBytes["ci_job_name"] != 256 {
		t.Errorf("mergedMaxLabelBytes mutated the package default: ci_job_name is now %d",
			MaxLabelBytes["ci_job_name"])
	}
}

// An empty / absent max_label_bytes leaves the whole contract at its defaults.
func TestLoadConfigMaxLabelBytesAbsent(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, "redact_key_substrings:\n  - vault\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.MaxLabelBytes) != 0 {
		t.Fatalf("expected no overrides, got %v", cfg.MaxLabelBytes)
	}
	limits := mergedMaxLabelBytes(cfg.MaxLabelBytes)
	for label, want := range MaxLabelBytes {
		if limits[label] != want {
			t.Errorf("label %q: got %d, want the default %d", label, limits[label], want)
		}
	}
}

// Every rejected override must fail the load, not be dropped: a silently
// ignored typo is indistinguishable from a limit that works.
func TestLoadConfigMaxLabelBytesRejected(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		wantSubs string
	}{
		{
			name:     "unknown label name",
			yaml:     "max_label_bytes:\n  ci_job_nam: 512\n",
			wantSubs: "ci_job_nam",
		},
		{
			name:     "label bounded elsewhere is still unknown here",
			yaml:     "max_label_bytes:\n  environ: 512\n",
			wantSubs: "environ",
		},
		{
			name:     "zero",
			yaml:     "max_label_bytes:\n  name: 0\n",
			wantSubs: "name",
		},
		{
			name:     "negative",
			yaml:     "max_label_bytes:\n  name: -1\n",
			wantSubs: "name",
		},
		{
			name:     "below the truncation marker",
			yaml:     fmt.Sprintf("max_label_bytes:\n  name: %d\n", minLabelBytes-1),
			wantSubs: "name",
		},
		{
			name:     "above the scrape-limit ceiling",
			yaml:     fmt.Sprintf("max_label_bytes:\n  cmdline: %d\n", maxLabelBytesCeiling+1),
			wantSubs: "cmdline",
		},
		{
			// The case an operator actually reaches for: "truncation is losing my
			// cmdlines, raise the limit". Accepting it trades truncated values for
			// a rejected scrape — every metric from the host, not just this label.
			name:     "raised far past the ceiling",
			yaml:     "max_label_bytes:\n  cmdline: 65536\n",
			wantSubs: "rejects the WHOLE scrape",
		},
		{
			name:     "not a number",
			yaml:     "max_label_bytes:\n  name: wide\n",
			wantSubs: "parse config",
		},
		{
			// yaml.v2 coerces a float into an int field by TRUNCATION, so a
			// map[string]int field would accept this as 512 — the operator asked
			// for one limit and a different one would apply, silently.
			name:     "fractional",
			yaml:     "max_label_bytes:\n  ci_job_name: 512.9\n",
			wantSubs: "not an integer byte count",
		},
		{
			// Same coercion, and this one does not even look like a mistake:
			// 1e3 reads as "1000 bytes" but is a float scalar in YAML.
			name:     "exponent",
			yaml:     "max_label_bytes:\n  ci_job_name: 1e3\n",
			wantSubs: "not an integer byte count",
		},
		{
			name:     "boolean",
			yaml:     "max_label_bytes:\n  name: true\n",
			wantSubs: "not an integer byte count",
		},
		{
			// Wider than int64, so yaml hands back a uint64. It IS an integer, so
			// it must be reported as over the ceiling — the actionable reason —
			// rather than as a non-integer, which would send the operator looking
			// for a typo that isn't there.
			name:     "integer wider than int64",
			wantSubs: "ceiling",
			yaml:     "max_label_bytes:\n  cmdline: 10000000000000000000\n",
		},
		{
			// The dangerous typo: singular, parses fine under non-strict
			// unmarshal, and yields NO redaction filters on a healthy-looking pod.
			name:     "misspelled top-level redact key",
			yaml:     "redact_key_substring:\n  - vault\n",
			wantSubs: "redact_key_substring",
		},
		{
			name:     "misspelled top-level max_label_bytes key",
			yaml:     "max_label_byte:\n  name: 512\n",
			wantSubs: "max_label_byte",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadConfig(writeConfig(t, tt.yaml))
			if err == nil {
				t.Fatalf("expected an error for %s", tt.name)
			}
			if !strings.Contains(err.Error(), tt.wantSubs) {
				t.Errorf("error %q does not mention %q — the operator has to be told which entry is wrong",
					err, tt.wantSubs)
			}
		})
	}
}

// The floor is exactly minLabelBytes: one byte below is rejected above, and the
// value itself must be accepted, or the boundary is off by one.
func TestLoadConfigMaxLabelBytesAcceptsTheFloor(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t,
		fmt.Sprintf("max_label_bytes:\n  name: %d\n", minLabelBytes)))
	if err != nil {
		t.Fatalf("unexpected error at the floor: %v", err)
	}
	if got := mergedMaxLabelBytes(cfg.MaxLabelBytes)["name"]; got != minLabelBytes {
		t.Errorf("name: got %d, want %d", got, minLabelBytes)
	}
}

// The ceiling is exactly maxLabelBytesCeiling: one byte above is rejected in the
// table above, and the value itself must be accepted, or the boundary is off by
// one. The ceiling also has to leave real room for the marker, so a value cut at
// it still fits the downstream label_value_length_limit.
func TestLoadConfigMaxLabelBytesAcceptsTheCeiling(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t,
		fmt.Sprintf("max_label_bytes:\n  cmdline: %d\n", maxLabelBytesCeiling)))
	if err != nil {
		t.Fatalf("unexpected error at the ceiling: %v", err)
	}
	limits := mergedMaxLabelBytes(cfg.MaxLabelBytes)
	if got := limits["cmdline"]; got != maxLabelBytesCeiling {
		t.Fatalf("cmdline: got %d, want %d", got, maxLabelBytesCeiling)
	}
	got := boundLabelWith(limits, "cmdline", strings.Repeat("x", maxLabelBytesCeiling*2), nil)
	if len(got) > maxEnvironBytes {
		t.Errorf("a value cut at the ceiling is %d bytes, over the %d-byte scrape limit "+
			"the ceiling exists to stay under", len(got), maxEnvironBytes)
	}
}

// Several bad entries must always name the same one. Map iteration order is
// randomized per run, so without the sort a config with two mistakes reports a
// different error each restart and an operator fixing "the" error chases the
// other one. Every other rejection case has exactly one bad entry and so cannot
// catch a dropped sort.
func TestLoadConfigMaxLabelBytesReportsDeterministicEntry(t *testing.T) {
	const yaml = "max_label_bytes:\n  zzz_unknown: 512\n  aaa_unknown: 512\n"

	// One file, reloaded: the path is part of the message, so a fresh temp dir
	// per iteration would differ for reasons that have nothing to do with sorting.
	path := writeConfig(t, yaml)

	var first string
	for i := 0; i < 20; i++ {
		_, err := LoadConfig(path)
		if err == nil {
			t.Fatal("expected an error for two unknown labels")
		}
		if i == 0 {
			first = err.Error()
			continue
		}
		if err.Error() != first {
			t.Fatalf("error varies between runs:\n  %s\n  %s", first, err)
		}
	}
	// Sorted order means the lexicographically smallest bad name is reported.
	if !strings.Contains(first, "aaa_unknown") {
		t.Errorf("expected the sorted-first bad entry, got %q", first)
	}
}

// mergedMaxLabelBytes must not trust its caller. NewProcessCollectorWithConfig
// is exported and takes any *Config, so a hand-built one can carry a limit that
// LoadConfig would have rejected — and the failure mode is fail-OPEN:
// truncateWithFingerprint reads max <= 0 as "no limit configured" and passes the
// value through, silently disabling the bound the operator was configuring.
func TestMergedMaxLabelBytesIgnoresUnvalidatedOverrides(t *testing.T) {
	tests := map[string]int{
		"zero":             0,
		"negative":         -1,
		"below the marker": minLabelBytes - 1,
		// The ceiling branch fails the other way — not fail-open, but a value the
		// downstream label_value_length_limit rejects, which costs the WHOLE
		// scrape. LoadConfig rejects it earlier, so this is the only test that
		// reaches mergedMaxLabelBytes' own ceiling check.
		"above the ceiling": maxLabelBytesCeiling + 1,
	}
	for name, limit := range tests {
		t.Run(name, func(t *testing.T) {
			got := mergedMaxLabelBytes(map[string]int{"cmdline": limit})["cmdline"]
			if got != MaxLabelBytes["cmdline"] {
				t.Errorf("cmdline limit is %d, want the %d default — an unusable override "+
					"must not disable the bound", got, MaxLabelBytes["cmdline"])
			}
		})
	}
}

// The merge copies: MaxLabelBytes is package state shared by every collector in
// the process, so applying one collector's overrides must not change what
// another one enforces.
func TestMergedMaxLabelBytesLeavesContractUntouched(t *testing.T) {
	want := MaxLabelBytes["name"]
	merged := mergedMaxLabelBytes(map[string]int{"name": 512})
	if merged["name"] != 512 {
		t.Fatalf("override not applied: got %d", merged["name"])
	}
	if got := MaxLabelBytes["name"]; got != want {
		t.Errorf("mergedMaxLabelBytes mutated the package contract: name is %d, want %d", got, want)
	}
}
