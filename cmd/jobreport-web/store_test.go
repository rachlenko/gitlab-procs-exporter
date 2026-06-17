package main

import (
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

func TestPrometheusStore_AddRejectsNonHTTP(t *testing.T) {
	var s PrometheusStore
	path := filepath.Join(t.TempDir(), "urls.json")

	cases := []struct {
		name string
		url  string
	}{
		{"ftp scheme", "ftp://prom.example.test/"},
		{"file scheme", "file:///etc/passwd"},
		{"no scheme", "prom.example.test"},
		{"empty", ""},
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
