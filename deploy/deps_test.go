package deploy

import (
	"bytes"
	"strings"
	"testing"
)

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
		{Name: "downloader", Status: StatusFail, Detail: "not found"},
	})
	out := buf.String()
	for _, want := range []string{"downloader", "✗", "Missing required dependencies"} {
		if !strings.Contains(out, want) {
			t.Errorf("PrintResults output missing %q\n---\n%s", want, out)
		}
	}
}

func TestCheckDependenciesShape(t *testing.T) {
	results := CheckDependencies()
	if len(results) != 1 {
		t.Fatalf("expected exactly one check, got %d: %+v", len(results), results)
	}
	if results[0].Name != "downloader" {
		t.Errorf("expected the single check to be 'downloader', got %q", results[0].Name)
	}
}
