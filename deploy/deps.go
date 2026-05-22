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
