package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeFakeSelf writes an executable shell script that echoes its arguments,
// one per line, and exits 0. It stands in for the re-exec'd self binary so the
// tests can inspect exactly which arguments runReport constructs.
func writeFakeSelf(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-self.sh")
	script := "#!/bin/sh\nfor a in \"$@\"; do echo \"$a\"; done\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { //nolint:gosec // test fixture script must be executable
		t.Fatalf("write fake self: %v", err)
	}
	return path
}

func TestRunReport_ArgsAndOutput(t *testing.T) {
	self := writeFakeSelf(t)

	cases := []struct {
		name     string
		jobID    string
		window   string
		wantArgs []string
	}{
		{
			name:     "prom only",
			wantArgs: []string{"report", "-prom", "https://prom.test/"},
		},
		{
			name:     "with job id",
			jobID:    "123",
			wantArgs: []string{"report", "-prom", "https://prom.test/", "-job-id", "123"},
		},
		{
			name:     "with window",
			window:   "1767312000..1767317400",
			wantArgs: []string{"report", "-prom", "https://prom.test/", "-window", "1767312000..1767317400"},
		},
		{
			name:     "with job id and window",
			jobID:    "456",
			window:   "1..2",
			wantArgs: []string{"report", "-prom", "https://prom.test/", "-job-id", "456", "-window", "1..2"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runReport(self, "https://prom.test/", tc.jobID, tc.window)
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", tc.name, err)
			}
			// The fake self echoes each received arg on its own line, in order, so
			// the output lines are exactly the constructed argument slice. Assert
			// the full ordered slice to catch flag/value ordering or adjacency bugs.
			lines := strings.Split(strings.TrimSpace(out), "\n")
			if !reflect.DeepEqual(lines, tc.wantArgs) {
				t.Errorf("%s: args = %v, want %v", tc.name, lines, tc.wantArgs)
			}
		})
	}
}

func TestRunReport_InvalidJobID(t *testing.T) {
	self := writeFakeSelf(t)
	out, err := runReport(self, "https://prom.test/", "abc", "")
	if err == nil {
		t.Fatalf("invalid job id: want error, got nil (output %q)", out)
	}
	if out != "" {
		t.Errorf("invalid job id: want no output (validation before exec), got %q", out)
	}
}

func TestRunReport_EmptyPromURL(t *testing.T) {
	self := writeFakeSelf(t)
	if _, err := runReport(self, "", "123", ""); err == nil {
		t.Fatalf("empty prom URL: want error, got nil")
	}
}

func TestRunReport_InvalidPromURL(t *testing.T) {
	self := writeFakeSelf(t)
	// A non-http(s) prom value must be rejected before exec, with no output.
	// (A bare host without scheme is NOT invalid — it defaults to https — so use an
	// explicitly non-http scheme here.)
	out, err := runReport(self, "ftp://prom.test/", "123", "")
	if err == nil {
		t.Fatalf("invalid prom URL: want error, got nil (output %q)", out)
	}
	if out != "" {
		t.Errorf("invalid prom URL: want no output (validation before exec), got %q", out)
	}
}
