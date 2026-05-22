// Package deploy implements the host-side operations exposed by the
// gitlab-procs-exporter binary through its --check-dependencies and
// --deploy-as-systemd-service flags: verifying that the toolchain and
// environment can run `go install`, and installing the exporter as a
// systemd service. The logic mirrors the standalone shell scripts it
// replaces, but ships inside the binary so a single self-contained
// executable can bootstrap itself on a fresh host.
package deploy

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Module is the canonical import path used by `go install`.
const Module = "github.com/rachlenko/gitlab-procs-exporter"

// Minimum Go toolchain, mirroring the `go 1.24.0` directive in go.mod.
const (
	minGoMajor = 1
	minGoMinor = 24
	minGoPatch = 0
)

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

// httpProbe is the function used to test network reachability. It is a
// package variable so tests can stub it without touching the network.
var httpProbe = func(host string) error {
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get("https://" + host + "/")
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// CheckDependencies runs every prerequisite check and returns the results
// in report order. It never panics and performs no destructive actions.
func CheckDependencies() []CheckResult {
	var out []CheckResult
	add := func(name string, st Status, detail string) {
		out = append(out, CheckResult{Name: name, Status: st, Detail: detail})
	}

	goPath, err := exec.LookPath("go")
	if err != nil {
		add("go toolchain", StatusFail, "not found on PATH; install golang-go or from https://go.dev/dl")
	} else {
		add(checkGoVersion(goPath))
	}

	if _, err := exec.LookPath("git"); err == nil {
		add("git", StatusOK, "found")
	} else {
		add("git", StatusFail, "not found; install with: apt-get install -y git")
	}

	if caBundlePresent() {
		add("CA certificates", StatusOK, "system trust store present")
	} else {
		add("CA certificates", StatusWarn, "no CA bundle found; HTTPS module downloads may fail (apt-get install -y ca-certificates)")
	}

	if goPath != "" {
		bindir := goBinDir(goPath)
		exists, writable := dirState(bindir)
		switch {
		case writable:
			add("binary install dir", StatusOK, "writable: "+bindir)
		case !exists:
			add("binary install dir", StatusWarn, "does not exist yet (go will create it): "+bindir)
		default:
			add("binary install dir", StatusWarn, "not writable by "+currentUser()+": "+bindir)
		}
		if dirOnPath(bindir) {
			add("PATH", StatusOK, "install dir is on PATH")
		} else {
			add("PATH", StatusWarn, "install dir not on PATH; add: export PATH=\"$PATH:"+bindir+"\"")
		}
	}

	for _, host := range []string{"proxy.golang.org", "github.com"} {
		if err := httpProbe(host); err == nil {
			add("network: "+host, StatusOK, "reachable")
		} else {
			add("network: "+host, StatusWarn, "unreachable: "+err.Error())
		}
	}

	return out
}

// checkGoVersion inspects the toolchain at goPath and returns a CheckResult.
func checkGoVersion(goPath string) (string, Status, string) {
	const name = "go toolchain"
	raw, err := exec.Command(goPath, "version").Output()
	if err != nil {
		return name, StatusWarn, "found at " + goPath + " but `go version` failed"
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 3 {
		return name, StatusWarn, "found but version output not understood: " + strings.TrimSpace(string(raw))
	}
	maj, min, pat, perr := parseGoVersion(fields[2])
	if perr != nil {
		return name, StatusWarn, "found but could not parse version: " + fields[2]
	}
	ver := strings.TrimPrefix(fields[2], "go")
	if meetsMinGo(maj, min, pat) {
		return name, StatusOK, fmt.Sprintf("%s (>= %d.%d.%d)", ver, minGoMajor, minGoMinor, minGoPatch)
	}
	return name, StatusFail, fmt.Sprintf("%s too old; need >= %d.%d.%d", ver, minGoMajor, minGoMinor, minGoPatch)
}

// parseGoVersion extracts major/minor/patch from a token like "go1.24.2",
// "1.24" or "go1.24rc1". Missing components default to 0.
func parseGoVersion(s string) (maj, min, pat int, err error) {
	s = strings.TrimSpace(strings.TrimPrefix(s, "go"))
	if s == "" || s[0] < '0' || s[0] > '9' {
		return 0, 0, 0, fmt.Errorf("cannot parse go version %q", s)
	}
	parts := strings.SplitN(s, ".", 3)
	leadingInt := func(in string) int {
		n := 0
		for _, r := range in {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		return n
	}
	get := func(i int) int {
		if i >= len(parts) {
			return 0
		}
		return leadingInt(parts[i])
	}
	return get(0), get(1), get(2), nil
}

// meetsMinGo reports whether maj.min.pat satisfies the minimum toolchain.
func meetsMinGo(maj, min, pat int) bool {
	switch {
	case maj != minGoMajor:
		return maj > minGoMajor
	case min != minGoMinor:
		return min > minGoMinor
	default:
		return pat >= minGoPatch
	}
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

	fmt.Fprintf(bw, "Checking prerequisites for: go install %s@latest\n\n", Module)
	for _, r := range results {
		fmt.Fprintf(bw, "  %s %-20s %s\n", r.Status.mark(), r.Name, r.Detail)
	}
	fmt.Fprintln(bw)
	if AllPassed(results) {
		fmt.Fprintln(bw, "All required dependencies satisfied.")
	} else {
		fmt.Fprintln(bw, "Missing required dependencies — resolve the ✗ items above before installing.")
	}
}

// --- environment helpers --------------------------------------------------

func goEnv(goPath, key string) string {
	out, err := exec.Command(goPath, "env", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// goBinDir resolves where `go install` would place binaries: GOBIN if set,
// else the first GOPATH entry's bin, else ~/go/bin.
func goBinDir(goPath string) string {
	if v := goEnv(goPath, "GOBIN"); v != "" {
		return v
	}
	if v := goEnv(goPath, "GOPATH"); v != "" {
		return filepath.Join(filepath.SplitList(v)[0], "bin")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "go", "bin")
}

// dirState reports whether dir exists and whether it is writable, probing
// writability with a throwaway temp file that is removed immediately.
func dirState(dir string) (exists, writable bool) {
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return err == nil, false
	}
	f, err := os.CreateTemp(dir, ".write-probe")
	if err != nil {
		return true, false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true, true
}

func dirOnPath(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == dir {
			return true
		}
	}
	return false
}

func caBundlePresent() bool {
	for _, p := range []string{
		"/etc/ssl/certs/ca-certificates.crt", // Debian/Ubuntu
		"/etc/pki/tls/certs/ca-bundle.crt",   // RHEL/Fedora
		"/etc/ssl/cert.pem",                  // macOS/BSD
	} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	if fi, err := os.Stat("/etc/ssl/certs"); err == nil && fi.IsDir() {
		return true
	}
	return false
}

func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return fmt.Sprintf("uid=%d", os.Getuid())
}
