package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// PrometheusStore persists the list of known Prometheus base URLs as a JSON array
// on disk. It is intentionally simple: the file is small and read/written whole.
type PrometheusStore struct{}

// Load reads the URL list from path. A missing file is not an error: it returns an
// empty slice. Any other read or decode failure is returned.
func (PrometheusStore) Load(path string) ([]string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is an operator-supplied config file by design
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("read store %s: %w", path, err)
	}
	if len(data) == 0 {
		return []string{}, nil
	}
	var urls []string
	if err := json.Unmarshal(data, &urls); err != nil {
		return nil, fmt.Errorf("decode store %s: %w", path, err)
	}
	return urls, nil
}

// Add normalizes rawURL to a canonical http(s) URL, appends it to the stored list
// (deduping exact matches), writes the file atomically (temp file + rename), and
// returns the updated list. A non-http(s) or unparseable URL is rejected.
func (s PrometheusStore) Add(path, rawURL string) ([]string, error) {
	normalized, err := normalizeURL(rawURL)
	if err != nil {
		return nil, err
	}

	urls, err := s.Load(path)
	if err != nil {
		return nil, err
	}
	for _, u := range urls {
		if u == normalized {
			return urls, nil // already present, nothing to write
		}
	}
	urls = append(urls, normalized)

	if err := writeJSONAtomic(path, urls); err != nil {
		return nil, err
	}
	return urls, nil
}

// normalizeURL canonicalizes a user-entered Prometheus URL. A missing scheme
// defaults to https (matching the jobreport engine's newPromClient, which also
// accepts scheme-less hosts and is what $PROMETHEUS_URL commonly holds), so a
// user can type a bare host like "prometheus.example.net" or "localhost:9090".
// It returns the scheme-qualified URL (trailing slash preserved; the engine trims
// it when querying) and errors if the result is not an http(s) URL with a host.
func normalizeURL(rawURL string) (string, error) {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return "", fmt.Errorf("prometheus URL is required")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid URL %q: scheme must be http or https", rawURL)
	}
	if u.Host == "" {
		return "", fmt.Errorf("invalid URL %q: missing host", rawURL)
	}
	return s, nil
}

// writeJSONAtomic marshals v as indented JSON and writes it to path atomically by
// writing to a temp file in the same directory and renaming it into place.
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode store: %w", err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".jobreport-web-urls-*.json") //nolint:gosec // G304: dir derives from the operator-supplied store path
	if err != nil {
		return fmt.Errorf("create temp store in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp store: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp store to %s: %w", path, err)
	}
	return nil
}
