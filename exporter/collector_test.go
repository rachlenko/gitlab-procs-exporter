package exporter

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestIsSecretKey(t *testing.T) {
	secrets := []string{
		"DB_PASSWORD", "secret_token", "API_KEY", "aws_access_key_id",
		"auth_bearer", "ldap_pwd", "redis_url", "SIGNATURE_VAL",
	}

	for _, s := range secrets {
		if !IsSecretKey(s) {
			t.Errorf("expected key %q to be marked as secret", s)
		}
	}

	nonSecrets := []string{
		"PATH", "USER", "HOME", "SHELL", "GITLAB_WORKER_ID", "PROCESS_NAME",
	}

	for _, ns := range nonSecrets {
		if IsSecretKey(ns) {
			t.Errorf("expected key %q to NOT be marked as secret", ns)
		}
	}
}

func TestCollectorDescribeAndCollect(t *testing.T) {
	store := NewHistoryStore()
	now := time.Now()

	// Add an active process with normal and secret environment variables
	sample := ProcessSample{
		Timestamp:  now,
		PID:        4567,
		Name:       "sidekiq-worker",
		CmdLine:    "sidekiq -c 10",
		Environ:    map[string]string{"DB_PASSWORD": "unsafe-pwd-here", "USER": "gitlab"}, //nolint:gosec // G101: fake secret to exercise redaction
		CPUUsage:   45.2,
		CPUSeconds: 123.5,
		MemoryRSS:  200 * 1024 * 1024,
		MemoryVMS:  400 * 1024 * 1024,
		IORead:     15000,
		IOWrite:    9500,
		CreateTime: 200,
		IsActive:   true,
	}
	store.AddSample(sample)

	collector := NewProcessCollector(store)

	// Test Describe
	descChan := make(chan *prometheus.Desc, 10)
	collector.Describe(descChan)
	close(descChan)

	descCount := 0
	for range descChan {
		descCount++
	}
	if descCount != 6 {
		t.Errorf("expected 6 metric descriptors, got %d", descCount)
	}

	// Test Collect
	metricChan := make(chan prometheus.Metric, 10)
	collector.Collect(metricChan)
	close(metricChan)

	metricCount := 0
	var infoLabels map[string]string

	for m := range metricChan {
		metricCount++
		descStr := m.Desc().String()
		if strings.Contains(descStr, "gitlab_process_info") {
			infoLabels = readMetricLabels(t, m)
		}
		if strings.Contains(descStr, "gitlab_process_cpu_seconds_total") {
			var dtoMetric dto.Metric
			if err := m.Write(&dtoMetric); err != nil {
				t.Fatalf("failed to write cpu metric: %v", err)
			}
			if got := dtoMetric.GetCounter().GetValue(); got != 123.5 {
				t.Errorf("cpu counter must emit cumulative CPUSeconds (123.5), got %v — emitting the percent gauge value breaks rate()", got)
			}
		}
	}

	if metricCount != 6 {
		t.Errorf("expected 6 active process metrics emitted, got %d", metricCount)
	}

	if infoLabels == nil {
		t.Fatal("expected to find gitlab_process_info metric in collected metrics")
	}

	// Verify redaction against what the collector ACTUALLY emitted, not a
	// re-implementation of the scrubbing logic.
	if got := infoLabels["environ"]; !strings.Contains(got, "DB_PASSWORD=[REDACTED]") {
		t.Errorf("expected DB_PASSWORD redacted in emitted environ label, got %q", got)
	}
	if got := infoLabels["environ"]; strings.Contains(got, "unsafe-pwd-here") {
		t.Errorf("sensitive password value leaked into emitted environ label: %q", got)
	}
	if got := infoLabels["environ"]; !strings.Contains(got, "USER=gitlab") {
		t.Errorf("expected USER to pass through in emitted environ label, got %q", got)
	}
	// A small, complete environ must report environ_truncated="0".
	if got := infoLabels["environ_truncated"]; got != "0" {
		t.Errorf("expected environ_truncated=%q for a complete environ, got %q", "0", got)
	}
}

// readMetricLabels writes a prometheus.Metric into its dto form and returns its
// label set as a name->value map, so tests can assert on what was actually
// emitted rather than re-deriving it.
func readMetricLabels(t *testing.T, m prometheus.Metric) map[string]string {
	t.Helper()
	var dtoMetric dto.Metric
	if err := m.Write(&dtoMetric); err != nil {
		t.Fatalf("failed to write metric: %v", err)
	}
	labels := make(map[string]string, len(dtoMetric.Label))
	for _, l := range dtoMetric.Label {
		labels[l.GetName()] = l.GetValue()
	}
	return labels
}

func TestIsSecretKeyExpanded(t *testing.T) {
	secrets := []string{
		"TLS_CERT", "SSH_KEY", "GPG_PASSPHRASE", "MY_JWT", "BEARER_HEADER",
		"ACCESS_GRANT", "SESSION_ID", "CSRF_COOKIE", "PASSWORD_SALT",
		"OTP_SEED", "WEBHOOK_URL", "DB_DSN", "PG_CONNECTION", "USER_PASSWD",
	}
	for _, s := range secrets {
		if !IsSecretKey(s) {
			t.Errorf("expected key %q to be marked as secret", s)
		}
	}

	// Keys containing "sas" that are NOT secrets — removed from denylist to
	// avoid over-redacting benign env vars like DATABASES_COUNT or ALIASES.
	benign := []string{"DATABASES_COUNT", "RELEASES_DIR", "INVASAS", "SASQUATCH_HOME"}
	for _, k := range benign {
		if IsSecretKey(k) {
			t.Errorf("expected key %q to NOT be marked as secret (over-redaction)", k)
		}
	}
}

func TestIsSecretValue(t *testing.T) {
	secretVals := []string{
		"glpat-abcdefghij1234567890",
		"ghp_0123456789abcdef0123456789abcdef0123",
		"AKIAIOSFODNN7EXAMPLE",
		"eyJhbGciOi.eyJzdWIiOiIxMjM0.SflKxwRJSM",
		"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0", // 40-char hex
	}
	for _, v := range secretVals {
		if !IsSecretValue(v) {
			t.Errorf("expected value %q to be redacted", v)
		}
	}
	plainVals := []string{"build", "main", "/usr/local/bin:/usr/bin", "true", "1", "ruby:3.2"}
	for _, v := range plainVals {
		if IsSecretValue(v) {
			t.Errorf("expected value %q to pass through", v)
		}
	}
}

func TestProcessCollectorKeyInExtra(t *testing.T) {
	store := NewHistoryStore()
	// Constructor must normalize: mixed case and surrounding spaces.
	pc := NewProcessCollector(store, "Vault", "  Internal_Token  ")
	if !pc.keyInExtra("VAULT_ADDR") {
		t.Error("expected VAULT_ADDR to match configured 'vault'")
	}
	if !pc.keyInExtra("MY_INTERNAL_TOKEN_X") {
		t.Error("expected MY_INTERNAL_TOKEN_X to match 'internal_token'")
	}
	if pc.keyInExtra("CI_JOB_NAME") {
		t.Error("did not expect CI_JOB_NAME to match")
	}
	if NewProcessCollector(store).keyInExtra("ANYTHING") {
		t.Error("no extras configured: nothing should match")
	}
}

func TestScrubEnvironConfiguredKey(t *testing.T) {
	pc := NewProcessCollector(NewHistoryStore(), "vault")
	out, _ := pc.scrubEnviron(map[string]string{
		"VAULT_ADDR":  "https://vault.example:8200", // key not in built-in denylist; value not secret-shaped
		"CI_JOB_NAME": "build",
	})
	if !strings.Contains(out, "VAULT_ADDR=[REDACTED]") {
		t.Errorf("expected VAULT_ADDR redacted via configured substring, got %q", out)
	}
	if strings.Contains(out, "vault.example") {
		t.Errorf("configured-secret value leaked: %q", out)
	}
	if !strings.Contains(out, "CI_JOB_NAME=build") {
		t.Errorf("expected CI_JOB_NAME to pass through, got %q", out)
	}
}

func TestScrubEnvironBuiltinStillWorks(t *testing.T) {
	pc := NewProcessCollector(NewHistoryStore()) // no extras
	out, _ := pc.scrubEnviron(map[string]string{"API_KEY": "abc", "USER": "gitlab"})
	if !strings.Contains(out, "API_KEY=[REDACTED]") {
		t.Errorf("expected built-in denylist to redact API_KEY, got %q", out)
	}
	if !strings.Contains(out, "USER=gitlab") {
		t.Errorf("expected USER to pass through, got %q", out)
	}
}

func TestScrubEnvironDeterministicOrder(t *testing.T) {
	pc := NewProcessCollector(NewHistoryStore())
	// Keys chosen so none trips the built-in denylist or value heuristics.
	out, _ := pc.scrubEnviron(map[string]string{"ZED": "1", "ALPHA": "2", "MIKE": "3"})
	want := "ALPHA=2, MIKE=3, ZED=1" // keys sorted lexicographically for a stable label
	if out != want {
		t.Errorf("expected sorted order %q, got %q", want, out)
	}
}

func TestScrubEnvironBounds(t *testing.T) {
	pc := NewProcessCollector(NewHistoryStore())

	// Over-long value is replaced whole, not byte-cut, and flagged truncated.
	longVal := strings.Repeat("x", maxEnvironValueLen+1)
	out, trunc := pc.scrubEnviron(map[string]string{"BIG": longVal})
	if strings.Contains(out, "x") || !strings.Contains(out, "BIG="+environValueTruncMarker) {
		t.Errorf("expected over-long value replaced with %q, got %q", environValueTruncMarker, out)
	}
	if trunc {
		t.Error("single over-long value should not set the truncated flag (nothing dropped)")
	}

	// Redaction keeps the variable present, so it must NOT set the flag either.
	out, trunc = pc.scrubEnviron(map[string]string{"API_KEY": "abc", "USER": "gitlab"})
	if !strings.Contains(out, "API_KEY=[REDACTED]") {
		t.Errorf("expected API_KEY redacted, got %q", out)
	}
	if trunc {
		t.Error("redaction should not set environ_truncated (variable list is complete)")
	}

	// More than maxEnvironVars variables: excess dropped, flag set, and exactly
	// maxEnvironVars pairs survive (the small pairs stay well under the byte cap).
	many := make(map[string]string, maxEnvironVars+50)
	for i := 0; i < maxEnvironVars+50; i++ {
		many[fmt.Sprintf("K%04d", i)] = "v"
	}
	out, trunc = pc.scrubEnviron(many)
	if !trunc {
		t.Error("expected truncated flag when variable count exceeds the cap")
	}
	if got := len(strings.Split(out, ", ")); got != maxEnvironVars {
		t.Errorf("expected exactly %d pairs, got %d", maxEnvironVars, got)
	}

	// Hard byte ceiling: many medium values must never exceed maxEnvironBytes.
	big := make(map[string]string, maxEnvironVars)
	for i := 0; i < maxEnvironVars; i++ {
		big[fmt.Sprintf("K%04d", i)] = strings.Repeat("y", maxEnvironValueLen)
	}
	out, trunc = pc.scrubEnviron(big)
	if len(out) > maxEnvironBytes {
		t.Errorf("environ label %d bytes exceeds ceiling %d", len(out), maxEnvironBytes)
	}
	if !trunc {
		t.Error("expected truncated flag when byte ceiling is hit")
	}
}

// TestScrubEnvironAtLimits pins the exact boundary conditions so an off-by-one
// regression (> becoming >=) in either guard is caught.
func TestScrubEnvironAtLimits(t *testing.T) {
	pc := NewProcessCollector(NewHistoryStore())

	// A value of exactly maxEnvironValueLen passes through unchanged: the guard
	// is `len(val) > maxEnvironValueLen`.
	exactVal := strings.Repeat("x", maxEnvironValueLen)
	out, trunc := pc.scrubEnviron(map[string]string{"OK": exactVal})
	if out != "OK="+exactVal {
		t.Errorf("value of exactly %d bytes must pass through unchanged, got %q", maxEnvironValueLen, out)
	}
	if trunc {
		t.Error("value at the length limit must not set the truncated flag")
	}

	// Exactly maxEnvironVars variables: all present, flag NOT set. Guard is
	// `len(keys) > maxEnvironVars`.
	exact := make(map[string]string, maxEnvironVars)
	for i := 0; i < maxEnvironVars; i++ {
		exact[fmt.Sprintf("K%04d", i)] = "v"
	}
	out, trunc = pc.scrubEnviron(exact)
	if got := len(strings.Split(out, ", ")); got != maxEnvironVars {
		t.Errorf("expected all %d pairs at the count limit, got %d", maxEnvironVars, got)
	}
	if trunc {
		t.Error("exactly maxEnvironVars variables must not set the truncated flag")
	}

	// One over the count limit sets the flag.
	over := make(map[string]string, maxEnvironVars+1)
	for i := 0; i < maxEnvironVars+1; i++ {
		over[fmt.Sprintf("K%04d", i)] = "v"
	}
	if _, trunc = pc.scrubEnviron(over); !trunc {
		t.Error("maxEnvironVars+1 variables must set the truncated flag")
	}
}

// TestScrubEnvironEmpty covers the trivial inputs where the loop never runs.
func TestScrubEnvironEmpty(t *testing.T) {
	pc := NewProcessCollector(NewHistoryStore())
	for name, in := range map[string]map[string]string{
		"nil":   nil,
		"empty": {},
	} {
		out, trunc := pc.scrubEnviron(in)
		if out != "" || trunc {
			t.Errorf("%s environ: expected (\"\", false), got (%q, %v)", name, out, trunc)
		}
	}
}

// TestScrubEnvironUTF8Safe verifies the "replaced whole, never byte-cut" claim:
// an over-long multi-byte value is swapped for the marker and the label stays
// valid UTF-8.
func TestScrubEnvironUTF8Safe(t *testing.T) {
	pc := NewProcessCollector(NewHistoryStore())
	// "世" is 3 bytes; repeat past the byte limit.
	multiByte := strings.Repeat("世", maxEnvironValueLen)
	out, _ := pc.scrubEnviron(map[string]string{"WIDE": multiByte})
	if out != "WIDE="+environValueTruncMarker {
		t.Errorf("expected over-long multi-byte value replaced whole, got %q", out)
	}
	if !utf8.ValidString(out) {
		t.Errorf("environ label is not valid UTF-8: %q", out)
	}
}

func TestRedactEnviron(t *testing.T) {
	in := map[string]string{ //nolint:gosec // G101: fake secrets to exercise redaction
		"DB_PASSWORD": "unsafe-pwd-here",            // built-in denylist key
		"VAULT_ADDR":  "https://vault.example:8200", // operator-configured substring
		"MY_VAR":      "glpat-abcdefghij1234567890", // secret-shaped value
		"USER":        "gitlab",                     // benign
	}
	out := RedactEnviron(in, []string{"vault"})

	for _, k := range []string{"DB_PASSWORD", "VAULT_ADDR", "MY_VAR"} {
		if out[k] != "[REDACTED]" {
			t.Errorf("expected %s redacted, got %q", k, out[k])
		}
	}
	if out["USER"] != "gitlab" {
		t.Errorf("expected USER to pass through, got %q", out["USER"])
	}
	// The input map must not be modified.
	if in["DB_PASSWORD"] != "unsafe-pwd-here" {
		t.Error("RedactEnviron mutated its input map")
	}
}
