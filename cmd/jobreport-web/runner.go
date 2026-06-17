package main

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"time"
)

// reportTimeout bounds a single self-exec report run.
const reportTimeout = 90 * time.Second

// jobIDPattern restricts a non-empty job id to digits, so it can never inject
// extra flags or shell metacharacters into the report invocation.
var jobIDPattern = regexp.MustCompile(`^\d+$`)

// runReport executes the report subcommand by re-exec'ing the current binary
// (selfPath) with validated arguments. It returns the combined stdout+stderr so
// the caller can show the user both a successful report and any error text from
// jobreport or Prometheus.
//
// promURL is required. jobID, when non-empty, must be all digits. window, when
// non-empty, is passed through verbatim (it is produced by buildWindow). The run
// is bounded by reportTimeout. A non-zero exit is not treated as a hard failure:
// the captured output is returned alongside the exec error so the user sees it.
func runReport(selfPath, promURL, jobID, window string) (string, error) {
	if promURL == "" {
		return "", fmt.Errorf("prometheus URL is required")
	}
	if jobID != "" && !jobIDPattern.MatchString(jobID) {
		return "", fmt.Errorf("invalid job id %q: want digits only", jobID)
	}

	args := []string{"report", "-prom", promURL}
	if jobID != "" {
		args = append(args, "-job-id", jobID)
	}
	if window != "" {
		args = append(args, "-window", window)
	}

	ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, selfPath, args...) //nolint:gosec // G204: selfPath is this binary; args are validated (digits-only job id, no shell)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
