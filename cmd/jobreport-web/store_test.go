package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrometheusStore_LoadMissingFile(t *testing.T) {
	var s PrometheusStore
	got, err := s.Load(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("Load missing file: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Load missing file: want empty slice, got %v", got)
	}
}

func TestPrometheusStore_LoadMalformedJSON(t *testing.T) {
	var s PrometheusStore
	path := filepath.Join(t.TempDir(), "urls.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write malformed store: %v", err)
	}
	if _, err := s.Load(path); err == nil {
		t.Fatalf("Load malformed JSON: want a decode error, got nil")
	}
}

func TestPrometheusStore_AddPersistsAndReloads(t *testing.T) {
	var s PrometheusStore
	path := filepath.Join(t.TempDir(), "urls.json")

	got, err := s.Add(path, "https://prom.example.test/")
	if err != nil {
		t.Fatalf("Add: unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != "https://prom.example.test/" {
		t.Fatalf("Add returned %v, want one stored URL", got)
	}

	reloaded, err := s.Load(path)
	if err != nil {
		t.Fatalf("Load after Add: unexpected error: %v", err)
	}
	if len(reloaded) != 1 || reloaded[0] != "https://prom.example.test/" {
		t.Fatalf("Load after Add returned %v, want the persisted URL", reloaded)
	}
}

func TestPrometheusStore_AddDedupes(t *testing.T) {
	var s PrometheusStore
	path := filepath.Join(t.TempDir(), "urls.json")

	if _, err := s.Add(path, "http://prom.example.test/"); err != nil {
		t.Fatalf("first Add: unexpected error: %v", err)
	}
	got, err := s.Add(path, "http://prom.example.test/")
	if err != nil {
		t.Fatalf("duplicate Add: unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("duplicate Add: want 1 URL, got %v", got)
	}
}

func TestPrometheusStore_Remove(t *testing.T) {
	var s PrometheusStore
	path := filepath.Join(t.TempDir(), "urls.json")
	if _, err := s.Add(path, "https://a.example/"); err != nil {
		t.Fatalf("Add a: %v", err)
	}
	if _, err := s.Add(path, "https://b.example/"); err != nil {
		t.Fatalf("Add b: %v", err)
	}

	got, err := s.Remove(path, "https://a.example/")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(got) != 1 || got[0] != "https://b.example/" {
		t.Fatalf("Remove returned %v, want only b", got)
	}
	// Persisted: reload shows only b.
	reloaded, _ := s.Load(path)
	if len(reloaded) != 1 || reloaded[0] != "https://b.example/" {
		t.Errorf("after Remove, store = %v, want [b]", reloaded)
	}
	// Removing an absent URL is a no-op, not an error.
	again, err := s.Remove(path, "https://missing.example/")
	if err != nil || len(again) != 1 {
		t.Errorf("Remove(absent) = %v, %v; want [b], nil", again, err)
	}
}

func TestPrometheusStore_RemoveSchemeless(t *testing.T) {
	var s PrometheusStore
	path := filepath.Join(t.TempDir(), "urls.json")
	if _, err := s.Add(path, "prom.example.net"); err != nil { // stored as https://prom.example.net
		t.Fatalf("Add: %v", err)
	}
	// Deleting by the bare host must match the normalized stored value.
	got, err := s.Remove(path, "prom.example.net")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Remove schemeless: store = %v, want empty", got)
	}
}

func TestPrometheusStore_AddRejectsNonHTTP(t *testing.T) {
	var s PrometheusStore
	path := filepath.Join(t.TempDir(), "urls.json")

	cases := []struct {
		name string
		url  string
	}{
		{"ftp scheme", "ftp://prom.example.test/"},
		{"file scheme", "file:///etc/passwd"},
		{"empty", ""},
		{"whitespace only", "   "},
		{"missing host", "http://"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Add(path, tc.url); err == nil {
				t.Fatalf("Add(%q): want error, got nil", tc.url)
			}
		})
	}
}

func TestPrometheusStore_AddSchemelessDefaultsHTTPS(t *testing.T) {
	var s PrometheusStore
	path := filepath.Join(t.TempDir(), "urls.json")

	// A bare host (no scheme) must be accepted and stored with https:// prepended,
	// matching the jobreport engine. This is what $PROMETHEUS_URL commonly holds.
	cases := map[string]string{
		"prometheus.example.net":   "https://prometheus.example.net",
		"localhost:9090":           "https://localhost:9090",
		"  prometheus.example.net": "https://prometheus.example.net", // trimmed
	}
	for in, want := range cases {
		got, err := s.Add(path, in)
		if err != nil {
			t.Fatalf("Add(%q): unexpected error: %v", in, err)
		}
		found := false
		for _, u := range got {
			if u == want {
				found = true
			}
		}
		if !found {
			t.Errorf("Add(%q): stored list %v does not contain %q", in, got, want)
		}
	}
}
