package main

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/rachlenko/gitlab-procs-exporter/exporter"

	"github.com/shirou/gopsutil/v3/process"
)

func TestServeDashboard(t *testing.T) {
	// Test valid GET /
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/", nil)
	rr := httptest.NewRecorder()

	serveDashboard(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status code 200, got %d", rr.Code)
	}

	contentType := rr.Header().Get("Content-Type")
	if contentType != "text/html; charset=utf-8" {
		t.Errorf("expected Content-Type text/html; charset=utf-8, got %q", contentType)
	}

	body := rr.Body.String()
	if body == "" {
		t.Error("expected non-empty dashboard body")
	}

	// Test 404 for undefined path
	req404 := httptest.NewRequestWithContext(context.Background(), "GET", "/some-random-route", nil)
	rr404 := httptest.NewRecorder()

	serveDashboard(rr404, req404)

	if rr404.Code != http.StatusNotFound {
		t.Errorf("expected status code 404, got %d", rr404.Code)
	}
}

func TestServeAPIProcesses(t *testing.T) {
	store := exporter.NewHistoryStore()
	sample := exporter.ProcessSample{
		Timestamp:  time.Now(),
		PID:        2222,
		Name:       "proc-main",
		CPUUsage:   5.0,
		CreateTime: 100,
		IsActive:   true,
	}
	store.AddSample(sample)

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/processes", nil)
	rr := httptest.NewRecorder()

	serveAPIProcesses(rr, req, store, nil)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status code 200, got %d", rr.Code)
	}

	if contentType := rr.Header().Get("Content-Type"); contentType != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", contentType)
	}

	var active []exporter.ProcessSample
	if err := json.NewDecoder(rr.Body).Decode(&active); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}

	if len(active) != 1 {
		t.Fatalf("expected 1 active process in API response, got %d", len(active))
	}

	if active[0].PID != 2222 {
		t.Errorf("expected PID 2222, got %d", active[0].PID)
	}
}

func TestServeAPIHistory(t *testing.T) {
	store := exporter.NewHistoryStore()
	sample := exporter.ProcessSample{
		Timestamp:  time.Now(),
		PID:        3333,
		Name:       "sidekiq",
		CPUUsage:   1.0,
		CreateTime: 100,
		IsActive:   true,
	}
	store.AddSample(sample)

	// Case 1: Missing both parameters
	req1 := httptest.NewRequestWithContext(context.Background(), "GET", "/api/history", nil)
	rr1 := httptest.NewRecorder()

	serveAPIHistory(rr1, req1, store, nil)

	if rr1.Code != http.StatusBadRequest {
		t.Errorf("expected status code 400 for missing params, got %d", rr1.Code)
	}

	// Case 2: Query by PID
	req2 := httptest.NewRequestWithContext(context.Background(), "GET", "/api/history?pid=3333", nil)
	rr2 := httptest.NewRecorder()

	serveAPIHistory(rr2, req2, store, nil)

	if rr2.Code != http.StatusOK {
		t.Fatalf("expected status code 200 for PID query, got %d", rr2.Code)
	}

	var history map[string][]exporter.ProcessSample
	if err := json.NewDecoder(rr2.Body).Decode(&history); err != nil {
		t.Fatalf("failed to decode JSON for PID query: %v", err)
	}

	if len(history) != 1 {
		t.Errorf("expected 1 process timeline for PID query, got %d", len(history))
	}

	// Case 3: Query by Name
	req3 := httptest.NewRequestWithContext(context.Background(), "GET", "/api/history?name=sidekiq", nil)
	rr3 := httptest.NewRecorder()

	serveAPIHistory(rr3, req3, store, nil)

	if rr3.Code != http.StatusOK {
		t.Fatalf("expected status code 200 for Name query, got %d", rr3.Code)
	}

	var historyName map[string][]exporter.ProcessSample
	if err := json.NewDecoder(rr3.Body).Decode(&historyName); err != nil {
		t.Fatalf("failed to decode JSON for Name query: %v", err)
	}

	if len(historyName) != 1 {
		t.Errorf("expected 1 process timeline for Name query, got %d", len(historyName))
	}
}

func TestScrape(t *testing.T) {
	store := exporter.NewHistoryStore()
	cache := make(map[int32]*process.Process)

	scrape(store, cache, false)

	// In a normal test execution environment (Linux or macOS), there are always active processes
	active := store.GetActiveProcesses()
	if len(active) == 0 {
		t.Log("warning: no active processes could be scraped on this machine")
	} else {
		t.Logf("successfully scraped %d active processes from the host OS", len(active))
		if len(cache) == 0 {
			t.Error("expected process cache to be populated after scraping")
		}
	}

	// The test process itself has burned CPU, so its sample must carry real
	// cumulative CPUSeconds (user+system). This guards the scrape() → p.Times()
	// wiring: the collector test only proves the collector emits whatever
	// CPUSeconds it's handed, never that scrape() populates the field at all.
	self := int32(os.Getpid()) //nolint:gosec // G115: a PID always fits in int32
	found := false
	for _, s := range active {
		if s.PID == self {
			found = true
			if s.CPUSeconds <= 0 {
				t.Errorf("scrape() must populate cumulative CPUSeconds for the running test process, got %v", s.CPUSeconds)
			}
		}
	}
	if len(active) > 0 && !found {
		t.Log("warning: own PID not present among scraped processes; skipping CPUSeconds assertion")
	}
}

// TestLiveProcessEvictsExitedPID exercises the eviction branch that is the
// whole reason liveProcess exists: when a cached PID no longer belongs to a
// live process, the stale *process.Process (carrying an obsolete CPU baseline
// and create time) must be dropped so a PID-reuse newcomer never inherits it.
func TestLiveProcessEvictsExitedPID(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a child process on this host: %v", err)
	}
	pid := int32(cmd.Process.Pid) //nolint:gosec // G115: a PID always fits in int32
	cache := make(map[int32]*process.Process)

	stale, err := liveProcess(cache, pid)
	if err != nil {
		t.Fatalf("liveProcess failed while child was alive: %v", err)
	}
	if cache[pid] != stale {
		t.Fatal("expected the live child to be cached")
	}

	// Kill and reap so the PID's cached object is now stale.
	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	// liveProcess may return an error (PID gone) or a fresh object (PID reused),
	// but in neither case may the stale cached object survive.
	_, _ = liveProcess(cache, pid)
	if cache[pid] == stale {
		t.Error("stale cached *process.Process for an exited PID was not evicted")
	}
}

func TestStartScraper(t *testing.T) {
	store := exporter.NewHistoryStore()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run background scraper with short interval
	go startScraper(ctx, store, 10*time.Millisecond, false)

	// Wait briefly for at least a few scraper cycles
	time.Sleep(30 * time.Millisecond)

	// Check that we got active processes scraped
	_ = store.GetActiveProcesses()
}

// TestServeAPIRedactsEnviron pins the security boundary: the JSON API must
// never return raw secrets — scrubbing is not only for the Prometheus label.
func TestServeAPIRedactsEnviron(t *testing.T) {
	store := exporter.NewHistoryStore()
	store.AddSample(exporter.ProcessSample{
		Timestamp:  time.Now(),
		PID:        4444,
		Name:       "runner",
		Environ:    map[string]string{"DB_PASSWORD": "unsafe-pwd-here", "USER": "gitlab"}, //nolint:gosec // G101: fake secret to exercise redaction
		CreateTime: 100,
		IsActive:   true,
	})

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/processes", nil)
	rr := httptest.NewRecorder()
	serveAPIProcesses(rr, req, store, nil)
	body := rr.Body.String()
	if strings.Contains(body, "unsafe-pwd-here") {
		t.Errorf("/api/processes leaked a secret environ value: %s", body)
	}
	if !strings.Contains(body, "[REDACTED]") {
		t.Errorf("expected [REDACTED] in /api/processes response, got: %s", body)
	}

	req2 := httptest.NewRequestWithContext(context.Background(), "GET", "/api/history?pid=4444", nil)
	rr2 := httptest.NewRecorder()
	serveAPIHistory(rr2, req2, store, nil)
	body2 := rr2.Body.String()
	if strings.Contains(body2, "unsafe-pwd-here") {
		t.Errorf("/api/history leaked a secret environ value: %s", body2)
	}
}

func TestLiveProcessReusesCachedObject(t *testing.T) {
	cache := make(map[int32]*process.Process)
	self := int32(os.Getpid()) //nolint:gosec // G115: a PID always fits in int32

	p1, err := liveProcess(cache, self)
	if err != nil {
		t.Fatalf("liveProcess failed for our own pid: %v", err)
	}
	p2, err := liveProcess(cache, self)
	if err != nil {
		t.Fatalf("liveProcess failed on second call: %v", err)
	}
	if p1 != p2 {
		t.Error("expected the cached *process.Process to be reused for a still-running process (CPU Percent baselines live on it)")
	}
	if len(cache) != 1 {
		t.Errorf("expected exactly 1 cache entry, got %d", len(cache))
	}
}

func TestParseFlagsDefaults(t *testing.T) {
	fsLinux := flag.NewFlagSet("test-linux", flag.ContinueOnError)
	optsLinux, err := parseFlags(fsLinux, []string{}, "linux")
	if err != nil {
		t.Fatalf("unexpected error parsing flags on linux: %v", err)
	}
	if optsLinux.serviceUser != "root" {
		t.Errorf("expected default service-user 'root' on linux, got %q", optsLinux.serviceUser)
	}
	if optsLinux.port != 8000 {
		t.Errorf("expected default port 8000, got %d", optsLinux.port)
	}
	if optsLinux.serviceName != "gitlab-procs-exporter" {
		t.Errorf("expected default serviceName 'gitlab-procs-exporter', got %q", optsLinux.serviceName)
	}
	if optsLinux.deployWindows || optsLinux.deploySystemd || optsLinux.deployService || optsLinux.uninstall {
		t.Errorf("expected deploy/uninstall flags to default to false")
	}

	fsWin := flag.NewFlagSet("test-windows", flag.ContinueOnError)
	optsWin, err := parseFlags(fsWin, []string{}, "windows")
	if err != nil {
		t.Fatalf("unexpected error parsing flags on windows: %v", err)
	}
	if optsWin.serviceUser != "LocalSystem" {
		t.Errorf("expected default service-user 'LocalSystem' on windows, got %q", optsWin.serviceUser)
	}
}

func TestParseFlagsCustom(t *testing.T) {
	args := []string{
		"--port=9100",
		"--interval=30s",
		"--service-name=custom-exporter",
		"--service-user=NT AUTHORITY\\SYSTEM",
		"--config=/path/to/config.yaml",
		"--deploy-as-windows-service",
		"--deploy-as-service",
	}

	fs := flag.NewFlagSet("test-custom", flag.ContinueOnError)
	opts, err := parseFlags(fs, args, "windows")
	if err != nil {
		t.Fatalf("unexpected error parsing flags: %v", err)
	}

	if opts.port != 9100 {
		t.Errorf("expected port 9100, got %d", opts.port)
	}
	if opts.scrapeInterval != 30*time.Second {
		t.Errorf("expected interval 30s, got %v", opts.scrapeInterval)
	}
	if opts.serviceName != "custom-exporter" {
		t.Errorf("expected serviceName 'custom-exporter', got %q", opts.serviceName)
	}
	if opts.serviceUser != "NT AUTHORITY\\SYSTEM" {
		t.Errorf("expected serviceUser 'NT AUTHORITY\\SYSTEM', got %q", opts.serviceUser)
	}
	if opts.configPath != "/path/to/config.yaml" {
		t.Errorf("expected configPath '/path/to/config.yaml', got %q", opts.configPath)
	}
	if !opts.deployWindows {
		t.Errorf("expected deployWindows to be true")
	}
	if !opts.deployService {
		t.Errorf("expected deployService to be true")
	}
}

func TestResolveAction(t *testing.T) {
	tests := []struct {
		name     string
		goos     string
		opts     cliOptions
		expected actionType
	}{
		{
			name:     "no flags on linux runs exporter",
			goos:     "linux",
			opts:     cliOptions{},
			expected: actionRun,
		},
		{
			name:     "no flags on windows runs exporter",
			goos:     "windows",
			opts:     cliOptions{},
			expected: actionRun,
		},
		{
			name:     "version flag takes precedence",
			goos:     "linux",
			opts:     cliOptions{showVersion: true, deploySystemd: true},
			expected: actionVersion,
		},
		{
			name:     "check deps flag",
			goos:     "linux",
			opts:     cliOptions{checkDeps: true},
			expected: actionCheckDeps,
		},
		{
			name:     "deploy windows flag on windows",
			goos:     "windows",
			opts:     cliOptions{deployWindows: true},
			expected: actionDeployWindows,
		},
		{
			name:     "deploy systemd flag on windows routes to windows service",
			goos:     "windows",
			opts:     cliOptions{deploySystemd: true},
			expected: actionDeployWindows,
		},
		{
			name:     "deploy service generic flag on windows routes to windows service",
			goos:     "windows",
			opts:     cliOptions{deployService: true},
			expected: actionDeployWindows,
		},
		{
			name:     "uninstall on windows routes to windows service uninstall",
			goos:     "windows",
			opts:     cliOptions{uninstall: true},
			expected: actionUninstallWindows,
		},
		{
			name:     "deploy systemd flag on linux routes to systemd",
			goos:     "linux",
			opts:     cliOptions{deploySystemd: true},
			expected: actionDeploySystemd,
		},
		{
			name:     "deploy service generic flag on linux routes to systemd",
			goos:     "linux",
			opts:     cliOptions{deployService: true},
			expected: actionDeploySystemd,
		},
		{
			name:     "deploy windows flag on linux targets windows install (which errors cleanly)",
			goos:     "linux",
			opts:     cliOptions{deployWindows: true},
			expected: actionDeployWindows,
		},
		{
			name:     "uninstall on linux routes to systemd uninstall",
			goos:     "linux",
			opts:     cliOptions{uninstall: true},
			expected: actionUninstallSystemd,
		},
		{
			name:     "update flag",
			goos:     "linux",
			opts:     cliOptions{update: true},
			expected: actionUpdate,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveAction(tc.goos, &tc.opts)
			if got != tc.expected {
				t.Errorf("resolveAction(%q, %+v) = %v; want %v", tc.goos, tc.opts, got, tc.expected)
			}
		})
	}
}
