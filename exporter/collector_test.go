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
		Timestamp:           now,
		PID:                 4567,
		Name:                "sidekiq-worker",
		CmdLine:             "sidekiq -c 10",
		Environ:             map[string]string{"DB_PASSWORD": "unsafe-pwd-here", "USER": "gitlab"}, //nolint:gosec // G101: fake secret to exercise redaction
		CPUUsage:            45.2,
		CPUSeconds:          123.5,
		MemoryRSS:           200 * 1024 * 1024,
		MemoryVMS:           400 * 1024 * 1024,
		IORead:              15000,
		IOWrite:             9500,
		IOReadSyscalls:      120,
		IOWriteSyscalls:     80,
		IOReadSelf:          15000,
		IOWriteSelf:         9500,
		IOReadSyscallsSelf:  120,
		IOWriteSyscallsSelf: 80,
		CreateTime:          200,
		IsActive:            true,
	}
	store.AddSample(sample)

	collector := NewProcessCollector(store)

	// Test Describe
	descChan := make(chan *prometheus.Desc, 24)
	collector.Describe(descChan)
	close(descChan)

	descCount := 0
	sawTruncationDesc := false
	for d := range descChan {
		if strings.Contains(d.String(), "gitlab_exporter_label_truncations_total") {
			sawTruncationDesc = true
			continue
		}
		descCount++
	}
	if descCount != 12 {
		t.Errorf("expected 12 process metric descriptors, got %d", descCount)
	}
	// The truncation counter is only ever scraped if it is described as well as
	// collected — a CounterVec missing from Describe is dropped by a pedantic
	// registry and reports nothing.
	if !sawTruncationDesc {
		t.Error("gitlab_exporter_label_truncations_total was not described")
	}

	// Test Collect
	metricChan := make(chan prometheus.Metric, 24)
	collector.Collect(metricChan)
	close(metricChan)

	metricCount := 0
	var infoLabels map[string]string
	sawCPU := false

	for m := range metricChan {
		descStr := m.Desc().String()
		if strings.Contains(descStr, "gitlab_exporter_label_truncations_total") {
			continue // self-observability, not a per-process series
		}
		metricCount++
		if strings.Contains(descStr, "gitlab_process_info") {
			infoLabels = readMetricLabels(t, m)
		}
		if strings.Contains(descStr, "gitlab_process_cpu_seconds_total") {
			sawCPU = true
			var dtoMetric dto.Metric
			if err := m.Write(&dtoMetric); err != nil {
				t.Fatalf("failed to write cpu metric: %v", err)
			}
			if got := dtoMetric.GetCounter().GetValue(); got != 123.5 {
				t.Errorf("cpu counter must emit cumulative CPUSeconds (123.5), got %v — emitting the percent gauge value breaks rate()", got)
			}
		}
	}

	if metricCount != 12 {
		t.Errorf("expected 12 active process metrics emitted, got %d", metricCount)
	}

	// Guard against the value assertion above silently no-op'ing: if the counter
	// were renamed or dropped, the branch never runs and the test would still
	// pass without ever checking the emitted value.
	if !sawCPU {
		t.Error("gitlab_process_cpu_seconds_total counter was never emitted")
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
	if !keyMatchesAny("VAULT_ADDR", pc.extraKeySubstrings) {
		t.Error("expected VAULT_ADDR to match configured 'vault'")
	}
	if !keyMatchesAny("MY_INTERNAL_TOKEN_X", pc.extraKeySubstrings) {
		t.Error("expected MY_INTERNAL_TOKEN_X to match 'internal_token'")
	}
	if keyMatchesAny("CI_JOB_NAME", pc.extraKeySubstrings) {
		t.Error("did not expect CI_JOB_NAME to match")
	}
	if keyMatchesAny("ANYTHING", NewProcessCollector(store).extraKeySubstrings) {
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

	// Over-long value keeps a prefix plus the informative marker carrying the
	// ORIGINAL length and a fingerprint — the same contract boundLabel applies.
	longVal := strings.Repeat("x", maxEnvironValueLen+1)
	out, trunc := pc.scrubEnviron(map[string]string{"BIG": longVal})
	if want := "BIG=" + truncateWithFingerprint(longVal, maxEnvironValueLen); out != want {
		t.Errorf("expected over-long value truncated to %q, got %q", want, out)
	}
	if !strings.Contains(out, fmt.Sprintf("[len=%d;sha256=", len(longVal))) {
		t.Errorf("marker must report the original length %d, got %q", len(longVal), out)
	}
	if trunc {
		t.Error("single over-long value should not set the truncated flag (nothing dropped)")
	}

	// [REDACTED] must keep winning over truncation: an over-long value under a
	// sensitive key leaks a prefix and a fingerprint of the secret if the switch
	// arms are ever reordered.
	out, _ = pc.scrubEnviron(map[string]string{"API_KEY": longVal})
	if out != "API_KEY=[REDACTED]" {
		t.Errorf("redaction must win over truncation for sensitive pairs, got %q", out)
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

	// Same ceiling, but with the marker in play: the fingerprint marker is ~35
	// bytes against 11 for the old "[TRUNCATED]", so every truncated pair is
	// bigger and the ceiling is reached sooner. It must still hold exactly.
	marked := make(map[string]string, maxEnvironVars)
	for i := 0; i < maxEnvironVars; i++ {
		marked[fmt.Sprintf("K%04d", i)] = strings.Repeat("z", maxEnvironValueLen+1)
	}
	out, trunc = pc.scrubEnviron(marked)
	if len(out) > maxEnvironBytes {
		t.Errorf("environ label %d bytes exceeds ceiling %d with the marker in play", len(out), maxEnvironBytes)
	}
	if !trunc {
		t.Error("expected truncated flag when the byte ceiling is hit with markers")
	}
	if !strings.Contains(out, ";sha256=") {
		t.Errorf("expected fingerprint markers in the joined label, got %q", out)
	}
	if !utf8.ValidString(out) {
		t.Errorf("environ label is not valid UTF-8: %q", out)
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

	// One byte over the length limit: the marker fires, and the whole pair stays
	// within maxEnvironValueLen+maxMarkerLen — a marker appended past the limit
	// is how a "bounded" value stops being bounded.
	overVal := strings.Repeat("x", maxEnvironValueLen+1)
	out, trunc = pc.scrubEnviron(map[string]string{"OK": overVal})
	val := strings.TrimPrefix(out, "OK=")
	if !strings.HasSuffix(val, "]") || !strings.Contains(val, ";sha256=") {
		t.Errorf("value of maxEnvironValueLen+1 bytes must carry the marker, got %q", out)
	}
	if len(val) > maxEnvironValueLen+maxMarkerLen {
		t.Errorf("truncated value %d bytes exceeds ceiling %d", len(val), maxEnvironValueLen+maxMarkerLen)
	}
	if trunc {
		t.Error("per-value truncation must not set the truncated flag (variable list is complete)")
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

// TestScrubEnvironUTF8Safe verifies the cut never lands mid-rune: the value is
// now byte-cut (not replaced whole), so the rune walk-back is what keeps the
// label valid UTF-8 — and MustNewConstMetric panics on invalid UTF-8.
func TestScrubEnvironUTF8Safe(t *testing.T) {
	pc := NewProcessCollector(NewHistoryStore())
	// "世" is 3 bytes and maxEnvironValueLen is not a multiple of 3, so the raw
	// cut lands mid-rune and the walk-back must fire.
	multiByte := strings.Repeat("世", maxEnvironValueLen)
	out, _ := pc.scrubEnviron(map[string]string{"WIDE": multiByte})
	if want := "WIDE=" + truncateWithFingerprint(multiByte, maxEnvironValueLen); out != want {
		t.Errorf("expected over-long multi-byte value truncated to %q, got %q", want, out)
	}
	if !utf8.ValidString(out) {
		t.Errorf("environ label is not valid UTF-8: %q", out)
	}
	// The marker reports the original byte length, not the surviving prefix.
	if !strings.Contains(out, fmt.Sprintf("[len=%d;sha256=", len(multiByte))) {
		t.Errorf("marker must report the original length %d, got %q", len(multiByte), out)
	}
	prefix, _, found := strings.Cut(strings.TrimPrefix(out, "WIDE="), truncEllipsis)
	if !found {
		t.Fatalf("expected the truncation marker in %q", out)
	}
	if len(prefix)%3 != 0 {
		t.Errorf("prefix %d bytes is not a whole number of 3-byte runes", len(prefix))
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

// TestCollectSurvivesInvalidUTF8 pins the crash mode: MustNewConstMetric
// panics on invalid UTF-8 label values, and that panic happens on the
// registry's gather goroutine — one binary environ would kill the exporter.
func TestCollectSurvivesInvalidUTF8(t *testing.T) {
	store := NewHistoryStore()
	store.AddSample(ProcessSample{
		Timestamp:  time.Now(),
		PID:        7777,
		Name:       "bad\xffname",
		CmdLine:    "run \xfe--flag",
		Environ:    map[string]string{"WEIRD\xff": "va\xfdlue", "CI_JOB_NAME": "job\xff"},
		CreateTime: 300,
		IsActive:   true,
	})

	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(NewProcessCollector(store))
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather failed on invalid UTF-8 input: %v", err)
	}
	if len(mfs) == 0 {
		t.Fatal("expected metrics to be gathered")
	}
}

func TestSanitizeLabelValue(t *testing.T) {
	if got := sanitizeLabelValue("plain-value"); got != "plain-value" {
		t.Errorf("valid string must pass through unchanged, got %q", got)
	}
	got := sanitizeLabelValue("abc\xff\xfedef")
	if !utf8.ValidString(got) {
		t.Errorf("sanitized value is not valid UTF-8: %q", got)
	}
	if !strings.HasPrefix(got, "abc") || !strings.HasSuffix(got, "def") {
		t.Errorf("sanitizing must preserve the valid bytes around the bad ones, got %q", got)
	}
}

// TestBoundCmdline covers the cmdline label now that its dedicated truncator
// (boundCmdline/maxCmdlineBytes) has been absorbed into the shared
// boundLabel + MaxLabelBytes contract. Every assertion of the original test is
// kept — especially the 3-byte-rune walk-back — so coverage of the panic this
// guards against does not regress.
func TestBoundCmdline(t *testing.T) {
	maxCmdline := MaxLabelBytes["cmdline"]

	if got := boundLabel("cmdline", "short cmd"); got != "short cmd" {
		t.Errorf("short cmdline must pass through unchanged, got %q", got)
	}
	// 2-byte runes, twice the limit: the cut must land on a rune boundary.
	long := strings.Repeat("ы", maxCmdline)
	got := boundLabel("cmdline", long)
	if ceiling := maxCmdline + maxMarkerLen; len(got) > ceiling {
		t.Errorf("bounded cmdline is %d bytes, limit %d", len(got), ceiling)
	}
	if !utf8.ValidString(got) {
		t.Errorf("bounded cmdline is not valid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, markerFor(long)) {
		t.Errorf("expected visible truncation marker suffix %q, got tail %q", markerFor(long), got[len(got)-40:])
	}
	// The marker reports the ORIGINAL length, not the truncated one — that is
	// what the fingerprint marker buys over the old bare [TRUNCATED].
	if !strings.Contains(got, fmt.Sprintf("len=%d", len(long))) {
		t.Errorf("marker must report the original length %d, got tail %q", len(long), got[len(got)-40:])
	}

	// 3-byte runes: the cmdline limit is not a multiple of 3, so the raw cut at
	// the limit lands MID-rune and the walk-back loop must fire. This is
	// the case 2-byte runes (which align to the even limit) never exercise;
	// without the walk-back the result would be invalid UTF-8 and re-introduce
	// the MustNewConstMetric panic this bounding exists to prevent.
	if maxCmdline%3 == 0 {
		t.Fatalf("test assumes the cmdline limit (%d) is not a multiple of 3", maxCmdline)
	}
	multi := strings.Repeat("世", maxCmdline) // 3 bytes each
	gotMulti := boundLabel("cmdline", multi)
	if !utf8.ValidString(gotMulti) {
		t.Errorf("bounded 3-byte-rune cmdline is not valid UTF-8 (walk-back failed): %q", gotMulti)
	}
	if !strings.HasSuffix(gotMulti, markerFor(multi)) {
		t.Errorf("expected truncation marker suffix on 3-byte-rune cmdline, got tail %q", gotMulti[len(gotMulti)-40:])
	}
	// The kept prefix must be strictly shorter than the naive cut, proving the
	// walk-back trimmed the partial rune rather than leaving it.
	if body := strings.TrimSuffix(gotMulti, markerFor(multi)); len(body) >= maxCmdline {
		t.Errorf("walk-back did not trim partial rune: prefix is %d bytes, expected < %d", len(body), maxCmdline)
	}
}

// name and the four ci_* labels ride on ALL 12 metrics, so an oversized value
// there multiplies 12x into the TSDB index — unlike cmdline/environ, which
// appear on gitlab_process_info alone. Every one of them must be bounded, and
// bounded on every metric, not just on the info metric.
func TestCollectBoundsNameAndCILabels(t *testing.T) {
	store := NewHistoryStore()
	store.AddSample(ProcessSample{
		Timestamp: time.Now(),
		PID:       9182,
		Name:      strings.Repeat("n", 4096),
		CmdLine:   "runner exec",
		Environ: map[string]string{
			"CI_JOB_NAME":     strings.Repeat("j", 4096),
			"CI_PROJECT_PATH": strings.Repeat("g/", 2048),
			"CI_JOB_ID":       strings.Repeat("1", 4096),
			"CI_PIPELINE_ID":  strings.Repeat("2", 4096),
		},
		CreateTime: 400,
		IsActive:   true,
	})

	collector := NewProcessCollector(store)
	metricChan := make(chan prometheus.Metric, 24)
	collector.Collect(metricChan)
	close(metricChan)

	metricCount := 0
	checked := map[string]int{}
	for m := range metricChan {
		if strings.Contains(m.Desc().String(), "gitlab_exporter_label_truncations_total") {
			continue // self-observability, not a per-process series
		}
		metricCount++
		labels := readMetricLabels(t, m)
		for name, limit := range MaxLabelBytes {
			val, ok := labels[name]
			if !ok {
				continue
			}
			checked[name]++
			if len(val) > limit+maxMarkerLen {
				t.Errorf("%s: label %q is %d bytes, ceiling is %d (limit %d + marker %d)",
					m.Desc().String(), name, len(val), limit+maxMarkerLen, limit, maxMarkerLen)
			}
			if !utf8.ValidString(val) {
				t.Errorf("label %q is not valid UTF-8 after bounding: %q", name, val)
			}
		}
	}

	if metricCount != 12 {
		t.Fatalf("expected 12 metrics, got %d", metricCount)
	}
	// name and the ci_* labels must have been seen on all 12 metrics; cmdline
	// only on gitlab_process_info. Without this the loop above could silently
	// check nothing.
	for _, name := range []string{"name", "ci_job_id", "ci_job_name", "ci_project_path", "ci_pipeline_id"} {
		if checked[name] != 12 {
			t.Errorf("label %q was checked on %d metrics, expected all 12", name, checked[name])
		}
	}
	if checked["cmdline"] != 1 {
		t.Errorf("label %q was checked on %d metrics, expected 1 (gitlab_process_info)", "cmdline", checked["cmdline"])
	}
}

// The cmdline emitted by Collect must come from the shared contract, not from
// a private truncator of its own — one label with its own marker format is how
// the contract quietly stops being one contract.
func TestCollectCmdlineUsesSharedContract(t *testing.T) {
	// Arrange: 2-byte runes at twice the limit, so truncation certainly fires.
	cmdline := strings.Repeat("ы", MaxLabelBytes["cmdline"])
	store := NewHistoryStore()
	store.AddSample(ProcessSample{
		Timestamp:  time.Now(),
		PID:        9183,
		Name:       "runner",
		CmdLine:    cmdline,
		CreateTime: 400,
		IsActive:   true,
	})

	// Act
	collector := NewProcessCollector(store)
	metricChan := make(chan prometheus.Metric, 24)
	collector.Collect(metricChan)
	close(metricChan)

	// Assert
	seen := false
	for m := range metricChan {
		val, ok := readMetricLabels(t, m)["cmdline"]
		if !ok {
			continue
		}
		seen = true
		if want := boundLabel("cmdline", cmdline); val != want {
			t.Errorf("emitted cmdline %q, want the shared-contract value %q", val, want)
		}
	}
	if !seen {
		t.Fatal("no metric carried a cmdline label")
	}
}

// The ci_* values must be bounded at their source, so every caller of
// ciJobLabelValues inherits the contract rather than only gitlab_process_info.
func TestCIJobLabelValuesBounded(t *testing.T) {
	vals := ciJobLabelValues(map[string]string{
		"CI_JOB_ID":       strings.Repeat("1", 4096),
		"CI_JOB_NAME":     strings.Repeat("j", 4096),
		"CI_PROJECT_PATH": strings.Repeat("g/", 2048),
		"CI_PIPELINE_ID":  strings.Repeat("2", 4096),
	})
	names := ciJobLabelNames()
	if len(vals) != len(names) {
		t.Fatalf("got %d values for %d label names", len(vals), len(names))
	}
	for i, name := range names {
		limit, ok := MaxLabelBytes[name]
		if !ok {
			t.Fatalf("label %q has no entry in MaxLabelBytes — the contract must cover every ci_* label", name)
		}
		if len(vals[i]) > limit+maxMarkerLen {
			t.Errorf("ciJobLabelValues %q is %d bytes, ceiling is %d", name, len(vals[i]), limit+maxMarkerLen)
		}
		if len(vals[i]) <= limit {
			t.Errorf("ciJobLabelValues %q was not truncated at all (%d bytes) — a 4KB input must be cut", name, len(vals[i]))
		}
	}

	// Sanitizing must still happen first: bounding invalid UTF-8 makes the rune
	// walk-back meaningless.
	bad := ciJobLabelValues(map[string]string{"CI_JOB_NAME": "job\xffname"})
	for _, v := range bad {
		if !utf8.ValidString(v) {
			t.Errorf("ciJobLabelValues emitted invalid UTF-8: %q", v)
		}
	}
}

// truncationCounts pulls gitlab_exporter_label_truncations_total out of a
// gathered set as label -> value. It reads the GATHERED families rather than
// the collector's own field on purpose: a CounterVec that is created but never
// emitted from Describe/Collect silently reports nothing, and only the
// registry can tell the two apart.
func truncationCounts(t *testing.T, mfs []*dto.MetricFamily) map[string]float64 {
	t.Helper()
	const name = "gitlab_exporter_label_truncations_total"
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		out := make(map[string]float64, len(mf.GetMetric()))
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "label" {
					out[lp.GetValue()] = m.GetCounter().GetValue()
				}
			}
		}
		return out
	}
	t.Fatalf("%s was not gathered — the counter must be emitted from Describe/Collect", name)
	return nil
}

// Truncation was invisible: cmdline is cut in production and nothing reported
// it. An explicit contract needs an explicit signal, so every cut boundLabel
// makes increments a counter keyed by label name.
func TestCollectCountsLabelTruncations(t *testing.T) {
	store := NewHistoryStore()
	store.AddSample(ProcessSample{
		Timestamp:  time.Now(),
		PID:        9184,
		Name:       strings.Repeat("n", 4096),
		CmdLine:    "runner exec", // short: must NOT count as a truncation
		CreateTime: 400,
		IsActive:   true,
	})

	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(NewProcessCollector(store))
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	got := truncationCounts(t, mfs)
	if got["name"] < 1 {
		t.Errorf("gitlab_exporter_label_truncations_total{label=\"name\"} is %v, want >= 1", got["name"])
	}
	// The series for an untruncated label must still exist at 0, or operators
	// cannot tell "nothing was truncated" from "this label is not instrumented".
	if v, ok := got["cmdline"]; !ok || v != 0 {
		t.Errorf("expected {label=\"cmdline\"} present and 0, got %v (present=%v)", v, ok)
	}
	// Every label in the contract must be represented up front.
	for label := range MaxLabelBytes {
		if _, ok := got[label]; !ok {
			t.Errorf("no truncation series for contract label %q", label)
		}
	}
}

// The counter is a counter: it accumulates across scrapes rather than being
// reset or re-created per Collect, or a truncation seen between two scrapes
// would vanish from rate() entirely.
func TestLabelTruncationsAccumulateAcrossScrapes(t *testing.T) {
	store := NewHistoryStore()
	store.AddSample(ProcessSample{
		Timestamp:  time.Now(),
		PID:        9185,
		Name:       strings.Repeat("n", 4096),
		CreateTime: 400,
		IsActive:   true,
	})

	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(NewProcessCollector(store))

	first, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	second, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	before := truncationCounts(t, first)["name"]
	after := truncationCounts(t, second)["name"]
	if after <= before {
		t.Errorf("counter did not advance across scrapes: %v then %v", before, after)
	}
}

// A reaper — pid 1, a job shell — carries its dead children's I/O in the
// process-wide counter while having issued none of it. The two families of
// series must stay distinct, or topk() over write bytes ranks reapers.
func TestCollectSeparatesReapedIOFromSelfIO(t *testing.T) {
	store := NewHistoryStore()
	store.AddSample(ProcessSample{
		Timestamp:           time.Now(),
		PID:                 1,
		Name:                "systemd",
		IORead:              1994568709120,
		IOWrite:             27786051437568, // inherited from every process ever reaped
		IOReadSyscalls:      20912506972,
		IOWriteSyscalls:     17180756343, // likewise inherited
		IOReadSelf:          26492928,
		IOWriteSelf:         0, // systemd itself never wrote a byte
		IOReadSyscallsSelf:  4211,
		IOWriteSyscallsSelf: 0,
		CreateTime:          1,
		IsActive:            true,
	})

	reg := prometheus.NewRegistry()
	if err := reg.Register(NewProcessCollector(store)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	got := make(map[string]float64, len(mfs))
	for _, mf := range mfs {
		if len(mf.GetMetric()) != 1 {
			continue
		}
		got[mf.GetName()] = mf.GetMetric()[0].GetCounter().GetValue()
	}

	want := map[string]float64{
		"gitlab_process_io_write_bytes_total":         27786051437568,
		"gitlab_process_self_io_write_bytes_total":    0,
		"gitlab_process_io_read_bytes_total":          1994568709120,
		"gitlab_process_self_io_read_bytes_total":     26492928,
		"gitlab_process_io_write_syscalls_total":      17180756343,
		"gitlab_process_self_io_write_syscalls_total": 0,
		"gitlab_process_io_read_syscalls_total":       20912506972,
		"gitlab_process_self_io_read_syscalls_total":  4211,
	}
	for name, wantVal := range want {
		if v, ok := got[name]; !ok {
			t.Errorf("%s not exported", name)
		} else if v != wantVal {
			t.Errorf("%s = %v, want %v", name, v, wantVal)
		}
	}
}
