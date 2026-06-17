package main

import (
	"context"
	"fmt"
	"log"
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
	// Normalize the target the same way the store does on add: the report form's
	// prom field comes straight from the client (htmx hx-include), so it is not
	// guaranteed to be one of the stored URLs. normalizeURL also defaults a missing
	// scheme to https, so a bare host works here just as it does on the CLI.
	normalized, err := normalizeURL(promURL)
	if err != nil {
		return "", err
	}
	if jobID != "" && !jobIDPattern.MatchString(jobID) {
		return "", fmt.Errorf("invalid job id %q: want digits only", jobID)
	}

	args := []string{"report", "-prom", normalized}
	if jobID != "" {
		args = append(args, "-job-id", jobID)
	}
	if window != "" {
		args = append(args, "-window", window)
	}

	ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
	defer cancel()

	log.Printf("report: exec %s %q", selfPath, args)   //nolint:gosec // G706: args logged with %q, which escapes any control characters
	cmd := exec.CommandContext(ctx, selfPath, args...) //nolint:gosec // G204: selfPath is this binary; args are validated (digits-only job id, normalized URL, no shell)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("report: exec exit: %v", err)
	}
	return string(out), err
}
