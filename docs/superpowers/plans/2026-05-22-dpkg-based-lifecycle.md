# dpkg-based Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Pivot the exporter's `--check-dependencies`, `--deploy-as-systemd-service`, `--update`, and `--uninstall` flags from a `go install` model to a Debian-package (`dpkg`) model.

**Architecture:** Keep the `deploy` package. `deps.go` shrinks to a single downloader check. `systemd.go` drops all `go install` logic and points `ExecStart` at the running binary (`os.Executable()`). A new `release.go` implements `--update` via the GitHub Releases API + `dpkg -i`. `--uninstall` removes the service only (dpkg owns the binary).

**Tech Stack:** Go 1.24, stdlib only (`net/http`, `encoding/json`, `os/exec`), systemd, dpkg.

Spec: `docs/superpowers/specs/2026-05-22-dpkg-based-lifecycle-design.md`

## File Structure

- `deploy/deps.go` — rewrite: downloader-only check; keep `Status`/`CheckResult`/`AllPassed`/`PrintResults`; add `binaryName` const; keep `Module` const.
- `deploy/deps_test.go` — rewrite: drop go/network tests; keep `AllPassed`/`PrintResults`; assert single downloader result.
- `deploy/release.go` — NEW: GitHub release client, `.deb` asset selection, download, `dpkg`-based `UpdateService`, `binaryVersion`.
- `deploy/release_test.go` — NEW: `debArch`, `pickDebAsset`, `latestRelease` (stubbed HTTP).
- `deploy/systemd.go` — rework `ServiceConfig` (drop go-install fields, add `ExecPath`), unit template (`ExecStart={{.ExecPath}}`), `InstallService` (no go install), `UninstallService` (no binary removal, dpkg hint); remove old `UpdateService`, `goInstall`, `findGo`, `installedVersion`.
- `deploy/systemd_test.go` — update for new `ServiceConfig`/template.
- `main.go` — drop `--service-version`/`--install-dir` flags; update flag help and `ServiceConfig` construction.
- `README.md` — update "Bootstrapping on a Host".

Note: the package is a single compilation unit, so an intermediate task must leave it compiling. Tasks are ordered so each ends green. Task 2 adds the new `--update` (which only needs `ServiceName`) before Task 3 reworks `ServiceConfig`, so nothing references removed fields between tasks.

---

### Task 1: Rewrite `--check-dependencies` to a downloader-only check

**Files:**
- Modify: `deploy/deps.go` (full rewrite)
- Test: `deploy/deps_test.go` (full rewrite)

- [ ] **Step 1: Rewrite `deploy/deps.go`**

Replace the entire file with:

```go
// Package deploy implements the host-side operations exposed by the
// gitlab-procs-exporter binary through its --check-dependencies,
// --deploy-as-systemd-service, --update, and --uninstall flags: verifying
// prerequisites, installing the exporter as a systemd service, updating it
// from the latest GitHub release .deb, and removing the service.
package deploy

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
)

// Module is the canonical repository path (used to build the GitHub API URL).
const Module = "github.com/rachlenko/gitlab-procs-exporter"

// binaryName is the installed binary / systemd unit base name and the .deb
// asset-name prefix.
const binaryName = "gitlab-procs-exporter"

// Status is the outcome of a single dependency check.
type Status int

const (
	// StatusOK means the requirement is satisfied.
	StatusOK Status = iota
	// StatusWarn means the requirement is non-fatal but worth attention.
	StatusWarn
	// StatusFail means a required dependency is missing or unusable.
	StatusFail
)

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusWarn:
		return "WARN"
	default:
		return "FAIL"
	}
}

func (s Status) mark() string {
	switch s {
	case StatusOK:
		return "✓"
	case StatusWarn:
		return "!"
	default:
		return "✗"
	}
}

// CheckResult records the outcome of one prerequisite check.
type CheckResult struct {
	Name   string
	Status Status
	Detail string
}

// CheckDependencies verifies the prerequisites for the dpkg-based lifecycle.
// On a Debian/systemd host, dpkg and systemd are part of the base system and
// root is enforced when an operation runs, so the only non-guaranteed
// dependency is a downloader (curl or wget) used by --update to fetch the
// release .deb.
func CheckDependencies() []CheckResult {
	if _, err := exec.LookPath("curl"); err == nil {
		return []CheckResult{{Name: "downloader", Status: StatusOK, Detail: "curl found"}}
	}
	if _, err := exec.LookPath("wget"); err == nil {
		return []CheckResult{{Name: "downloader", Status: StatusOK, Detail: "wget found"}}
	}
	return []CheckResult{{
		Name:   "downloader",
		Status: StatusFail,
		Detail: "need curl or wget for --update; install with: apt-get install -y curl",
	}}
}

// AllPassed reports whether no result is StatusFail (warnings are allowed).
func AllPassed(results []CheckResult) bool {
	for _, r := range results {
		if r.Status == StatusFail {
			return false
		}
	}
	return true
}

// PrintResults writes a human-readable report and summary to w.
func PrintResults(w io.Writer, results []CheckResult) {
	bw := bufio.NewWriter(w)
	defer bw.Flush()

	fmt.Fprintf(bw, "Checking prerequisites for %s\n\n", binaryName)
	for _, r := range results {
		fmt.Fprintf(bw, "  %s %-20s %s\n", r.Status.mark(), r.Name, r.Detail)
	}
	fmt.Fprintln(bw)
	if AllPassed(results) {
		fmt.Fprintln(bw, "All required dependencies satisfied.")
	} else {
		fmt.Fprintln(bw, "Missing required dependencies — resolve the ✗ items above.")
	}
}
```

- [ ] **Step 2: Rewrite `deploy/deps_test.go`**

Replace the entire file with:

```go
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
```

- [ ] **Step 3: Run the deploy tests to verify they pass**

Run: `go test ./deploy/ -run 'TestAllPassed|TestPrintResults|TestCheckDependenciesShape' -v`
Expected: PASS (the dev/CI host has curl or wget, so `TestCheckDependenciesShape` sees one OK result).

- [ ] **Step 4: Build to confirm the package still compiles**

Run: `go build ./...`
Expected: success (systemd.go still uses the old `ServiceConfig`; only deps.go changed).

- [ ] **Step 5: Commit**

```bash
gofmt -w deploy/deps.go deploy/deps_test.go
git add deploy/deps.go deploy/deps_test.go
git commit -m "deploy: reduce --check-dependencies to a downloader check"
```

---

### Task 2: Replace `--update` with a dpkg/GitHub-release implementation

**Files:**
- Create: `deploy/release.go`
- Test: `deploy/release_test.go`
- Modify: `deploy/systemd.go` (remove old `UpdateService` and `installedVersion`)

- [ ] **Step 1: Write the failing tests in `deploy/release_test.go`**

```go
package deploy

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDebArch(t *testing.T) {
	cases := map[string]struct {
		want    string
		wantErr bool
	}{
		"amd64": {"amd64", false},
		"arm64": {"arm64", false},
		"386":   {"", true},
		"ppc64": {"", true},
	}
	for in, c := range cases {
		got, err := debArch(in)
		if c.wantErr {
			if err == nil {
				t.Errorf("debArch(%q): expected error", in)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("debArch(%q) = %q,%v; want %q,nil", in, got, err, c.want)
		}
	}
}

func TestPickDebAsset(t *testing.T) {
	rel := ghRelease{
		TagName: "v0.0.4",
		Assets: []ghAsset{
			{Name: "gitlab-procs-exporter_0.0.4_linux_amd64.deb", DownloadURL: "https://x/amd64.deb"},
			{Name: "gitlab-procs-exporter_0.0.4_linux_arm64.deb", DownloadURL: "https://x/arm64.deb"},
			{Name: "gitlab-procs-exporter_0.0.4_linux_amd64.rpm", DownloadURL: "https://x/amd64.rpm"},
			{Name: "gitlab-procs-exporter_0.0.4_checksums.txt", DownloadURL: "https://x/sums"},
		},
	}
	a, err := pickDebAsset(rel, "amd64")
	if err != nil {
		t.Fatalf("pickDebAsset amd64: %v", err)
	}
	if a.DownloadURL != "https://x/amd64.deb" {
		t.Errorf("amd64 picked %q", a.DownloadURL)
	}
	if _, err := pickDebAsset(rel, "386"); err == nil {
		t.Error("expected error for unsupported arch")
	}
	empty := ghRelease{TagName: "v9", Assets: []ghAsset{{Name: "other_linux_amd64.deb"}}}
	if _, err := pickDebAsset(empty, "amd64"); err == nil {
		t.Error("expected error when no matching asset prefix")
	}
}

func TestLatestRelease(t *testing.T) {
	orig := httpGet
	t.Cleanup(func() { httpGet = orig })
	const body = `{"tag_name":"v1.2.3","assets":[{"name":"gitlab-procs-exporter_1.2.3_linux_amd64.deb","browser_download_url":"https://x/a.deb"}]}`
	httpGet = func(url string) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}
	rel, err := latestRelease()
	if err != nil {
		t.Fatalf("latestRelease: %v", err)
	}
	if rel.TagName != "v1.2.3" || len(rel.Assets) != 1 {
		t.Errorf("decoded unexpectedly: %+v", rel)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail to compile**

Run: `go test ./deploy/ -run 'TestDebArch|TestPickDebAsset|TestLatestRelease' -v`
Expected: build error — `undefined: debArch`, `ghRelease`, `pickDebAsset`, `httpGet`, `latestRelease`.

- [ ] **Step 3: Create `deploy/release.go`**

```go
package deploy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// releaseAPIURL returns the GitHub "latest release" API endpoint for the repo.
func releaseAPIURL() string {
	repo := strings.TrimPrefix(Module, "github.com/")
	return "https://api.github.com/repos/" + repo + "/releases/latest"
}

// ghRelease is the subset of the GitHub release API we consume.
type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
}

// httpGet performs the release-API GET; it is a package variable so tests can
// stub it without touching the network.
var httpGet = func(url string) (*http.Response, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	return client.Do(req)
}

// latestRelease fetches and decodes the latest release metadata.
func latestRelease() (ghRelease, error) {
	resp, err := httpGet(releaseAPIURL())
	if err != nil {
		return ghRelease{}, fmt.Errorf("querying GitHub releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ghRelease{}, fmt.Errorf("GitHub releases API returned %s", resp.Status)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return ghRelease{}, fmt.Errorf("decoding release JSON: %w", err)
	}
	if rel.TagName == "" {
		return ghRelease{}, fmt.Errorf("release JSON had empty tag_name")
	}
	return rel, nil
}

// debArch maps a Go GOARCH value to the Debian package architecture used in
// the release asset names.
func debArch(goarch string) (string, error) {
	switch goarch {
	case "amd64":
		return "amd64", nil
	case "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("no .deb asset for architecture %q", goarch)
	}
}

// pickDebAsset selects the .deb asset matching the host architecture. It
// matches by prefix and suffix to avoid depending on the version segment (the
// tag is "v0.0.4" while the asset embeds "0.0.4").
func pickDebAsset(rel ghRelease, goarch string) (ghAsset, error) {
	arch, err := debArch(goarch)
	if err != nil {
		return ghAsset{}, err
	}
	prefix := binaryName + "_"
	suffix := "_linux_" + arch + ".deb"
	for _, a := range rel.Assets {
		if strings.HasPrefix(a.Name, prefix) && strings.HasSuffix(a.Name, suffix) {
			return a, nil
		}
	}
	return ghAsset{}, fmt.Errorf("no asset matching %s*%s in release %s", prefix, suffix, rel.TagName)
}

// downloadFile fetches url into a new temp file using curl or wget and returns
// its path. The caller is responsible for removing the file.
func downloadFile(w io.Writer, url string) (string, error) {
	f, err := os.CreateTemp("", binaryName+"-*.deb")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	path := f.Name()
	_ = f.Close()

	var cmd *exec.Cmd
	switch {
	case lookPath("curl") != "":
		cmd = exec.Command("curl", "-fsSL", "-o", path, url)
	case lookPath("wget") != "":
		cmd = exec.Command("wget", "-q", "-O", path, url)
	default:
		_ = os.Remove(path)
		return "", fmt.Errorf("neither curl nor wget found; run --check-dependencies")
	}
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Run(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("downloading %s: %w", url, err)
	}
	return path, nil
}

func lookPath(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

// UpdateService updates the exporter to the latest GitHub release: it
// downloads the matching .deb, installs it with dpkg (replacing the
// dpkg-managed binary in /usr/bin), and restarts the systemd service so the
// new binary takes effect. Requires Linux and root.
func UpdateService(w io.Writer, cfg ServiceConfig) error {
	cfg.setDefaults()
	logf := func(format string, a ...any) { fmt.Fprintf(w, "==> "+format+"\n", a...) }

	if runtime.GOOS != "linux" {
		return fmt.Errorf("update is only supported on Linux (this host is %s)", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root (try: sudo %s --update)", binaryName)
	}
	dpkg, err := exec.LookPath("dpkg")
	if err != nil {
		return fmt.Errorf("dpkg not found; this targets Debian-based systems: %w", err)
	}
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return fmt.Errorf("systemctl not found; this targets systemd-based systems: %w", err)
	}

	logf("querying latest release")
	rel, err := latestRelease()
	if err != nil {
		return err
	}
	asset, err := pickDebAsset(rel, runtime.GOARCH)
	if err != nil {
		return err
	}
	logf("latest release is %s; asset %s", rel.TagName, asset.Name)

	deb, err := downloadFile(w, asset.DownloadURL)
	if err != nil {
		return err
	}
	defer os.Remove(deb)

	logf("installing %s with dpkg", asset.Name)
	if err := runCmd(w, dpkg, "-i", deb); err != nil {
		return fmt.Errorf("dpkg -i failed: %w", err)
	}

	logf("restarting %s", cfg.ServiceName)
	if err := runCmd(w, systemctl, "restart", cfg.ServiceName+".service"); err != nil {
		return fmt.Errorf("systemctl restart: %w", err)
	}
	_ = runCmd(w, systemctl, "--no-pager", "--full", "status", cfg.ServiceName+".service")

	fmt.Fprintf(w, "\nDone. %s updated to %s (%s) and restarted.\n",
		cfg.ServiceName, rel.TagName, binaryVersion("/usr/bin/"+binaryName))
	return nil
}

// binaryVersion reports the --version output of the binary at path, or
// "unknown" if it cannot be run.
func binaryVersion(path string) string {
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}
```

- [ ] **Step 4: Remove the old `UpdateService` and `installedVersion` from `deploy/systemd.go`**

Delete the entire `func UpdateService(...)` block (the doc comment beginning "UpdateService installs the latest published version..." through its closing `}`) and the entire `func installedVersion(...)` block (doc comment "installedVersion reports..." through its closing `}`). Leave `goInstall`, `findGo`, `runCmd`, `writeUnit`, `removeIfPresent` untouched — they are still used by `InstallService`/`UninstallService` at this point.

- [ ] **Step 5: Run the new tests and the full package**

Run: `go test ./deploy/ -v`
Expected: PASS. `TestDebArch`, `TestPickDebAsset`, `TestLatestRelease` pass; previously-passing tests still pass.

- [ ] **Step 6: Build**

Run: `go build ./...`
Expected: success. `main.go` still calls `deploy.UpdateService` with `ServiceName` (and now-ignored fields), which compiles.

- [ ] **Step 7: Commit**

```bash
gofmt -w deploy/release.go deploy/release_test.go deploy/systemd.go
git add deploy/release.go deploy/release_test.go deploy/systemd.go
git commit -m "deploy: implement dpkg/GitHub-release based --update"
```

---

### Task 3: Rework `ServiceConfig`, unit rendering, deploy and uninstall; update main.go

**Files:**
- Modify: `deploy/systemd.go`
- Modify: `deploy/systemd_test.go`
- Modify: `main.go`

- [ ] **Step 1: Replace `ServiceConfig`, `setDefaults`, and remove `binaryPath` in `deploy/systemd.go`**

Replace the `ServiceConfig` struct (the `type ServiceConfig struct { ... }` block) and its `setDefaults` method and the `binaryPath` method with:

```go
// ServiceConfig describes the systemd service to install. Zero-value fields
// fall back to sensible defaults via setDefaults.
type ServiceConfig struct {
	ExecPath    string        // absolute path written to ExecStart (auto-resolved if empty)
	ServiceName string        // systemd unit name (without ".service")
	ServiceUser string        // User= the service runs as
	Port        int           // --port passed to the exporter
	Interval    time.Duration // --interval passed to the exporter
}

func (c *ServiceConfig) setDefaults() {
	if c.ServiceName == "" {
		c.ServiceName = binaryName
	}
	if c.ServiceUser == "" {
		c.ServiceUser = "root"
	}
	if c.Port == 0 {
		c.Port = 8000
	}
	if c.Interval == 0 {
		c.Interval = time.Minute
	}
}

// resolveExecPath returns the path to write into ExecStart: the configured
// value, else the running executable (symlinks resolved), else the default
// /usr/bin/<binaryName> deb install location.
func resolveExecPath(configured string) string {
	if configured != "" {
		return configured
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			return resolved
		}
		return exe
	}
	return "/usr/bin/" + binaryName
}
```

(The `unitPath` method stays as-is.)

- [ ] **Step 2: Update the unit template's `ExecStart` line in `deploy/systemd.go`**

In the `unitTemplate` const, change:

```
ExecStart={{.BinaryPath}} --port={{.Port}} --interval={{.Interval}}
```
to:
```
ExecStart={{.ExecPath}} --port={{.Port}} --interval={{.Interval}}
```

- [ ] **Step 3: Update `renderUnitFile` data in `deploy/systemd.go`**

Replace the `data` struct literal in `renderUnitFile` with:

```go
	data := struct {
		Module      string
		ExecPath    string
		Port        int
		Interval    string
		ServiceUser string
	}{
		Module:      Module,
		ExecPath:    resolveExecPath(cfg.ExecPath),
		Port:        cfg.Port,
		Interval:    cfg.Interval.String(),
		ServiceUser: cfg.ServiceUser,
	}
```

- [ ] **Step 4: Rewrite `InstallService` in `deploy/systemd.go`**

Replace the whole `func InstallService(...)` block with:

```go
// InstallService installs the exporter as a systemd service: it writes a unit
// file whose ExecStart points at the running binary, then daemon-reload /
// enable --now. The binary itself is expected to already be installed (e.g.
// via the .deb). Requires Linux and root privileges.
func InstallService(w io.Writer, cfg ServiceConfig) error {
	cfg.setDefaults()
	logf := func(format string, a ...any) { fmt.Fprintf(w, "==> "+format+"\n", a...) }

	if runtime.GOOS != "linux" {
		return fmt.Errorf("systemd deployment is only supported on Linux (this host is %s)", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root (try: sudo %s --deploy-as-systemd-service)", binaryName)
	}
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return fmt.Errorf("systemctl not found; this targets systemd-based systems: %w", err)
	}
	if cfg.ServiceUser != "root" {
		if _, err := exec.Command("id", "--", cfg.ServiceUser).CombinedOutput(); err != nil {
			return fmt.Errorf("service user %q does not exist (create it or use ServiceUser=root)", cfg.ServiceUser)
		}
	}

	logf("ExecStart -> %s", resolveExecPath(cfg.ExecPath))
	if err := writeUnit(w, cfg); err != nil {
		return err
	}

	logf("reloading systemd and enabling the service")
	if err := runCmd(w, systemctl, "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	if err := runCmd(w, systemctl, "enable", "--now", cfg.ServiceName+".service"); err != nil {
		return fmt.Errorf("systemctl enable --now: %w", err)
	}
	_ = runCmd(w, systemctl, "--no-pager", "--full", "status", cfg.ServiceName+".service")

	fmt.Fprintf(w, "\nDone. The exporter is running on port %d.\n", cfg.Port)
	fmt.Fprintf(w, "  systemctl status %s\n", cfg.ServiceName)
	fmt.Fprintf(w, "  journalctl -u %s -f\n", cfg.ServiceName)
	return nil
}
```

- [ ] **Step 5: Delete now-unused `goInstall` and `findGo` from `deploy/systemd.go`**

Delete the entire `func goInstall(...)` block and the entire `func findGo(...)` block. (`writeUnit`, `runCmd`, `removeIfPresent` remain.)

- [ ] **Step 6: Rewrite `UninstallService` in `deploy/systemd.go`**

Replace the whole `func UninstallService(...)` block with:

```go
// UninstallService removes the systemd service: it stops and disables the
// unit, removes the unit file, and reloads systemd. It is idempotent. It does
// NOT remove the binary, which is owned by dpkg — use `dpkg -r` for that.
// Requires Linux and root.
func UninstallService(w io.Writer, cfg ServiceConfig) error {
	cfg.setDefaults()
	logf := func(format string, a ...any) { fmt.Fprintf(w, "==> "+format+"\n", a...) }

	if runtime.GOOS != "linux" {
		return fmt.Errorf("systemd uninstall is only supported on Linux (this host is %s)", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root (try: sudo %s --uninstall)", binaryName)
	}
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return fmt.Errorf("systemctl not found; this targets systemd-based systems: %w", err)
	}

	unit := cfg.ServiceName + ".service"

	logf("stopping and disabling %s", unit)
	if err := runCmd(w, systemctl, "disable", "--now", unit); err != nil {
		logf("disable --now reported: %v (continuing)", err)
	}
	if err := removeIfPresent(w, cfg.unitPath()); err != nil {
		return err
	}
	logf("reloading systemd")
	_ = runCmd(w, systemctl, "daemon-reload")
	_ = runCmd(w, systemctl, "reset-failed", unit)

	fmt.Fprintf(w, "\nDone. The %s service has been removed.\n", cfg.ServiceName)
	fmt.Fprintf(w, "The binary is managed by dpkg; to remove the package run: dpkg -r %s\n", binaryName)
	return nil
}
```

- [ ] **Step 7: Update `deploy/systemd_test.go`**

Replace `TestServiceConfigDefaults` and `TestRenderUnitFile` with the versions below; keep `TestUnitPathUsesSystemdDir`, `TestInstallServiceRejectsNonLinux`, `TestUpdateServiceRejectsNonLinux`, `TestUninstallServiceRejectsNonLinux`, and `TestRemoveIfPresent` as-is.

```go
func TestServiceConfigDefaults(t *testing.T) {
	var c ServiceConfig
	c.setDefaults()
	if c.ServiceName != binaryName {
		t.Errorf("ServiceName = %q, want %q", c.ServiceName, binaryName)
	}
	if c.ServiceUser != "root" {
		t.Errorf("ServiceUser = %q", c.ServiceUser)
	}
	if c.Port != 8000 {
		t.Errorf("Port = %d, want 8000", c.Port)
	}
	if c.Interval != time.Minute {
		t.Errorf("Interval = %v, want 1m", c.Interval)
	}
}

func TestRenderUnitFile(t *testing.T) {
	c := ServiceConfig{
		ExecPath:    "/usr/bin/gitlab-procs-exporter",
		Port:        9100,
		Interval:    90 * time.Second,
		ServiceUser: "root",
	}
	out, err := renderUnitFile(c)
	if err != nil {
		t.Fatalf("renderUnitFile: %v", err)
	}
	wants := []string{
		"[Unit]",
		"[Service]",
		"[Install]",
		"Type=simple",
		"ExecStart=/usr/bin/gitlab-procs-exporter --port=9100 --interval=1m30s",
		"User=root",
		"Restart=on-failure",
		"WantedBy=multi-user.target",
		"Documentation=https://" + Module,
		"NoNewPrivileges=true",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("unit file missing %q\n---\n%s", w, out)
		}
	}
}
```

- [ ] **Step 8: Update flags in `main.go`**

Replace the flag block (currently the `checkDeps`/`deploySystemd`/`uninstall`/`update`/`serviceVersion`/`installDir`/`serviceName`/`serviceUser` declarations) with:

```go
	checkDeps := flag.Bool("check-dependencies", false,
		"Verify prerequisites (curl or wget) for --update, then exit")
	deploySystemd := flag.Bool("deploy-as-systemd-service", false,
		"Install the exporter as a systemd service (Linux, requires root), then exit")
	uninstall := flag.Bool("uninstall", false,
		"Stop/disable the systemd service and remove its unit file (Linux, requires root), then exit")
	update := flag.Bool("update", false,
		"Update to the latest release .deb via dpkg and restart the service (Linux, requires root), then exit")
	serviceName := flag.String("service-name", "gitlab-procs-exporter",
		"systemd unit name")
	serviceUser := flag.String("service-user", "root",
		"User the service runs as (root is required to read all processes' env/IO)")
```

- [ ] **Step 9: Update the deploy/uninstall/update handlers in `main.go`**

Replace the three `if *deploySystemd { ... }`, `if *uninstall { ... }`, `if *update { ... }` blocks with:

```go
	if *deploySystemd {
		err := deploy.InstallService(os.Stdout, deploy.ServiceConfig{
			ServiceName: *serviceName,
			ServiceUser: *serviceUser,
			Port:        *port,
			Interval:    *scrapeInterval,
		})
		if err != nil {
			log.Fatalf("deploy failed: %v", err)
		}
		return
	}

	if *uninstall {
		err := deploy.UninstallService(os.Stdout, deploy.ServiceConfig{
			ServiceName: *serviceName,
		})
		if err != nil {
			log.Fatalf("uninstall failed: %v", err)
		}
		return
	}

	if *update {
		err := deploy.UpdateService(os.Stdout, deploy.ServiceConfig{
			ServiceName: *serviceName,
		})
		if err != nil {
			log.Fatalf("update failed: %v", err)
		}
		return
	}
```

- [ ] **Step 10: Format, vet, build, test**

Run:
```bash
gofmt -w main.go deploy/systemd.go deploy/systemd_test.go
go build ./... && go vet ./... && go test ./...
```
Expected: build + vet succeed; all tests PASS.

- [ ] **Step 11: Exercise the flags locally (non-Linux guards + check)**

Run:
```bash
go build -o /tmp/gpe . && /tmp/gpe --check-dependencies; echo "exit=$?"
/tmp/gpe --update 2>&1 | head -1   # macOS: "update is only supported on Linux"
/tmp/gpe --deploy-as-systemd-service 2>&1 | head -1
/tmp/gpe --help 2>&1 | grep -E 'service-version|install-dir' || echo "old flags gone (good)"
rm -f /tmp/gpe
```
Expected: `--check-dependencies` prints the downloader check and exits 0; `--update`/`--deploy` refuse on macOS; the removed flags do not appear.

- [ ] **Step 12: Commit**

```bash
git add main.go deploy/systemd.go deploy/systemd_test.go
git commit -m "deploy: drop go-install model; ExecStart from os.Executable, uninstall keeps binary"
```

---

### Task 4: Update README

**Files:**
- Modify: `README.md` (the "Bootstrapping on a Host" section)

- [ ] **Step 1: Replace the install + lifecycle docs**

Replace the "Install with `go install`" and "Bootstrapping on a Host" subsections so they read (keep surrounding sections):

```markdown
### Install from the `.deb` release
Download the `.deb` for your architecture from the
[latest release](https://github.com/rachlenko/gitlab-procs-exporter/releases/latest)
and install it (the binary lands in `/usr/bin`):
```bash
curl -fsSLO https://github.com/rachlenko/gitlab-procs-exporter/releases/latest/download/gitlab-procs-exporter_<ver>_linux_amd64.deb
sudo dpkg -i gitlab-procs-exporter_<ver>_linux_amd64.deb
# (dpkg -i needs a local file, not a URL; `sudo apt-get install -y ./<file>.deb` also works and resolves deps)
gitlab-procs-exporter --version
```

### Bootstrapping on a Host
The binary manages its own systemd service.

*   **Check dependencies** — verifies a downloader (`curl` or `wget`) is present
    (needed by `--update`):
    ```bash
    gitlab-procs-exporter --check-dependencies
    ```
*   **Install as a systemd service** (Linux, run as root). Writes a unit whose
    `ExecStart` points at the installed binary, then enables and starts it:
    ```bash
    sudo gitlab-procs-exporter --deploy-as-systemd-service --port=8000 --interval=1m
    ```
    Tunable: `--service-name`, `--service-user` (default `root`, required to read
    every process's environment and I/O), `--port`, `--interval`.
*   **Update** (Linux, run as root). Downloads the latest release `.deb` from
    GitHub, installs it with `dpkg`, and restarts the service:
    ```bash
    sudo gitlab-procs-exporter --update
    ```
*   **Uninstall** (Linux, run as root). Stops/disables the service and removes its
    unit file. The binary is managed by dpkg — remove it with `dpkg -r`:
    ```bash
    sudo gitlab-procs-exporter --uninstall
    sudo dpkg -r gitlab-procs-exporter   # to also remove the binary
    ```
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: document the dpkg-based install/update lifecycle"
```

---

### Task 5: Final verification

- [ ] **Step 1: Full gate**

Run:
```bash
gofmt -l main.go deploy/*.go      # expect no output
go build ./... && go vet ./... && go test ./...
```
Expected: gofmt clean; build, vet, and all tests pass.

- [ ] **Step 2: Confirm no go-install references remain in the deploy package**

Run: `grep -rn "go install\|goInstall\|GOBIN\|InstallDir\|findGo" deploy/ main.go`
Expected: no matches (the model is fully dpkg-based).

---

## Self-Review

**Spec coverage:**
- `--check-dependencies` → downloader-only: Task 1. ✓
- `--deploy` ExecStart via `os.Executable()`, no go install: Task 3 (Steps 1,4 + `resolveExecPath`). ✓
- `--update` GitHub API → pick `.deb` → download → `dpkg -i` → restart: Task 2. ✓
- `--uninstall` service-only + `dpkg -r` hint: Task 3 Step 6. ✓
- Drop `--service-version`/`--install-dir`: Task 3 Step 8. ✓
- Retain `Module`, add `binaryName`: Task 1. ✓
- `pickDebAsset` prefix+suffix match (v-prefix sidestep): Task 2 + `TestPickDebAsset`. ✓
- README: Task 4. ✓
- Tests for `debArch`/`pickDebAsset`/`latestRelease`/`renderUnitFile`/non-Linux guards: Tasks 2, 3. ✓

**Placeholder scan:** No TBD/TODO; all code blocks complete.

**Type consistency:** `ServiceConfig` fields (`ExecPath`, `ServiceName`, `ServiceUser`, `Port`, `Interval`) are consistent across `systemd.go`, `release.go` (uses only `ServiceName` + `setDefaults`), and `main.go`. `ghRelease`/`ghAsset` field names match the test literals. `binaryName`/`Module` consts referenced consistently. `runCmd`, `writeUnit`, `removeIfPresent`, `resolveExecPath`, `binaryVersion` all defined exactly once.

**Compilation between tasks:** Task 1 touches only deps.go (other files unchanged → compiles). Task 2 adds release.go and removes old `UpdateService`/`installedVersion`; `main.go` still calls `deploy.UpdateService(ServiceConfig{...})` which exists with the still-old struct → compiles. Task 3 changes the struct and all its consumers (systemd.go + main.go) together → compiles.
