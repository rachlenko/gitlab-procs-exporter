package main

import (
	"os"
	"path/filepath"
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
		name        string
		jobID       string
		window      string
		wantArgs    []string
		notWantArgs []string
	}{
		{
			name:        "prom only",
			wantArgs:    []string{"report", "-prom", "https://prom.test/"},
			notWantArgs: []string{"-job-id", "-window"},
		},
		{
			name:        "with job id",
			jobID:       "123",
			wantArgs:    []string{"report", "-prom", "https://prom.test/", "-job-id", "123"},
			notWantArgs: []string{"-window"},
		},
		{
			name:        "with window",
			window:      "1767312000..1767317400",
			wantArgs:    []string{"report", "-prom", "https://prom.test/", "-window", "1767312000..1767317400"},
			notWantArgs: []string{"-job-id"},
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
			lines := strings.Split(strings.TrimSpace(out), "\n")
			for _, want := range tc.wantArgs {
				if !containsLine(lines, want) {
					t.Errorf("%s: expected arg %q in output %v", tc.name, want, lines)
				}
			}
			for _, notWant := range tc.notWantArgs {
				if containsLine(lines, notWant) {
					t.Errorf("%s: did not expect arg %q in output %v", tc.name, notWant, lines)
				}
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

// containsLine reports whether want appears as an exact element of lines.
func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}
