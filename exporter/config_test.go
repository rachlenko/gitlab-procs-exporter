package exporter

import (
	"os"
	"path/filepath"
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
