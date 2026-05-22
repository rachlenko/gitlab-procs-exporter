package deploy

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestParseGoVersion(t *testing.T) {
	cases := []struct {
		in            string
		maj, min, pat int
		wantErr       bool
	}{
		{"go1.24.2", 1, 24, 2, false},
		{"go1.24", 1, 24, 0, false},
		{"1.24.0", 1, 24, 0, false},
		{"go1.24rc1", 1, 24, 0, false},
		{"go1.21beta1", 1, 21, 0, false},
		{"go2.0.0", 2, 0, 0, false},
		{"", 0, 0, 0, true},
		{"goX.Y", 0, 0, 0, true},
	}
	for _, c := range cases {
		maj, min, pat, err := parseGoVersion(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseGoVersion(%q): expected error, got nil", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseGoVersion(%q): unexpected error: %v", c.in, err)
			continue
		}
		if maj != c.maj || min != c.min || pat != c.pat {
			t.Errorf("parseGoVersion(%q) = %d.%d.%d, want %d.%d.%d", c.in, maj, min, pat, c.maj, c.min, c.pat)
		}
	}
}

func TestMeetsMinGo(t *testing.T) {
	// Minimum is 1.24.0.
	cases := []struct {
		maj, min, pat int
		want          bool
	}{
		{1, 24, 0, true},
		{1, 24, 5, true},
		{1, 25, 0, true},
		{2, 0, 0, true},
		{1, 23, 9, false},
		{1, 0, 0, false},
		{0, 99, 0, false},
	}
	for _, c := range cases {
		if got := meetsMinGo(c.maj, c.min, c.pat); got != c.want {
			t.Errorf("meetsMinGo(%d,%d,%d) = %v, want %v", c.maj, c.min, c.pat, got, c.want)
		}
	}
}

func TestAllPassed(t *testing.T) {
	if !AllPassed([]CheckResult{{Status: StatusOK}, {Status: StatusWarn}}) {
		t.Error("AllPassed should be true when only OK/WARN present")
	}
	if AllPassed([]CheckResult{{Status: StatusOK}, {Status: StatusFail}}) {
		t.Error("AllPassed should be false when a FAIL is present")
	}
	if !AllPassed(nil) {
		t.Error("AllPassed(nil) should be true")
	}
}

func TestPrintResults(t *testing.T) {
	var buf bytes.Buffer
	PrintResults(&buf, []CheckResult{
		{Name: "go toolchain", Status: StatusOK, Detail: "go1.24.2"},
		{Name: "git", Status: StatusFail, Detail: "not found"},
	})
	out := buf.String()
	for _, want := range []string{"go toolchain", "git", "✓", "✗", "Missing required dependencies"} {
		if !strings.Contains(out, want) {
			t.Errorf("PrintResults output missing %q\n---\n%s", want, out)
		}
	}
}

func TestCheckDependenciesNoNetwork(t *testing.T) {
	// Stub the network probe so the test is hermetic, and confirm the
	// reachability checks surface as warnings rather than failures.
	orig := httpProbe
	t.Cleanup(func() { httpProbe = orig })
	httpProbe = func(host string) error { return errors.New("stubbed: offline") }

	results := CheckDependencies()
	if len(results) == 0 {
		t.Fatal("CheckDependencies returned no results")
	}
	for _, r := range results {
		if strings.HasPrefix(r.Name, "network:") && r.Status == StatusFail {
			t.Errorf("network check %q should be a warning, not a failure", r.Name)
		}
	}
}
