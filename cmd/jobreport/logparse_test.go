package main

import (
	"os"
	"testing"
)

const sampleURL = "https://builds.ds-in.net/artifacts/e6/fc/e6fcc0253ed7a328a10eb6e2e1ad6abcad60c374c64dbac4b76da610085b43d8/2026_06_01/126748622/37661665/job.log?X-Amz-Expires=600"

func loadFixture(t *testing.T) JobInfo {
	t.Helper()
	raw, err := os.ReadFile("testdata/job.log")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return parseJobLog(raw, sampleURL)
}

func TestParseIdentity(t *testing.T) {
	j := loadFixture(t)
	checks := map[string]string{
		"JobID":     j.JobID,
		"TraceID":   j.TraceID,
		"Date":      j.Date,
		"JobSlug":   j.JobSlug,
		"Pipeline":  j.Pipeline,
		"Runner":    j.Runner,
		"Shard":     j.Shard,
		"SystemID":  j.SystemID,
		"Executor":  j.Executor,
		"Namespace": j.Namespace,
	}
	want := map[string]string{
		"JobID":     "126748622",
		"TraceID":   "37661665",
		"Date":      "2026_06_01",
		"JobSlug":   "int-lin-mssql-audit-dict-pg",
		"Pipeline":  "353851",
		"Runner":    "vsphere_kuber_runner_with_shm",
		"Shard":     "glrtr-jv",
		"SystemID":  "r_7QrQIkzZ6BcG",
		"Executor":  "kubernetes",
		"Namespace": "ci-linux-tests",
	}
	for k, w := range want {
		if checks[k] != w {
			t.Errorf("%s = %q, want %q", k, checks[k], w)
		}
	}
	if j.Pod == "" {
		t.Error("Pod is empty")
	}
}

func TestParseWindow(t *testing.T) {
	j := loadFixture(t)
	if j.StartEpoch != 1780290253 {
		t.Errorf("StartEpoch = %d, want 1780290253", j.StartEpoch)
	}
	if j.EndEpoch != 1780292695 {
		t.Errorf("EndEpoch = %d, want 1780292695", j.EndEpoch)
	}
	if len(j.Phases) == 0 {
		t.Error("no phases parsed")
	}
}

func TestParseResultAndCrash(t *testing.T) {
	j := loadFixture(t)
	if !j.Failed {
		t.Error("expected Failed = true")
	}
	if j.ExitCode != "1" {
		t.Errorf("ExitCode = %q, want 1", j.ExitCode)
	}
	if !j.CrashKill {
		t.Error("expected CrashKill = true")
	}
	if !contains(j.KilledNames, "AppFirewallCore") || !contains(j.KilledNames, "AppBackendService") {
		t.Errorf("KilledNames missing expected entries: %v", j.KilledNames)
	}
	if !contains(j.KillPIDs, "936") {
		t.Errorf("KillPIDs missing 936: %v", j.KillPIDs)
	}
	if j.CoreCmdline == "" {
		t.Error("CoreCmdline empty")
	}
	if len(j.CrashFrames) == 0 {
		t.Error("no crash frames parsed")
	}
}

func TestStripANSI(t *testing.T) {
	in := "\x1b[0KRunning \x1b[32;1mwith\x1b[0;m runner"
	if got := stripANSI(in); got != "Running with runner" {
		t.Errorf("stripANSI = %q", got)
	}
}

func TestFetchLogLocalFile(t *testing.T) {
	want, err := os.ReadFile("testdata/job.log")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	// Plain path and file:// URL must both read the local fixture, not hit the network.
	for _, src := range []string{"testdata/job.log", "file://testdata/job.log"} {
		got, err := fetchLog(src)
		if err != nil {
			t.Fatalf("fetchLog(%q): %v", src, err)
		}
		if len(got) != len(want) {
			t.Errorf("fetchLog(%q) read %d bytes, want %d", src, len(got), len(want))
		}
	}
	if _, err := fetchLog("testdata/does-not-exist.log"); err == nil {
		t.Error("fetchLog on missing file: expected error, got nil")
	}
}
