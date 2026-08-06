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
			name:     "not a number",
			yaml:     "max_label_bytes:\n  name: wide\n",
			wantSubs: "parse config",
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
