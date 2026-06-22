package exporter

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
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
	hasInfoMetric := false
	hasRedactedEnv := false

	for m := range metricChan {
		metricCount++
		// Since we cannot easily inspect internal private fields of prometheus.Metric directly without reflection,
		// we can format the metric using its string representation or Write interface
		descStr := m.Desc().String()
		if strings.Contains(descStr, "gitlab_process_info") {
			hasInfoMetric = true
		}
	}

	if metricCount != 6 {
		t.Errorf("expected 6 active process metrics emitted, got %d", metricCount)
	}

	if !hasInfoMetric {
		t.Error("expected to find gitlab_process_info metric in collected metrics")
	}

	// Direct verification of environmental redaction
	active := store.GetActiveProcesses()
	if len(active) == 0 {
		t.Fatal("expected at least one active process")
	}
	p := active[0]

	// Test the redaction logic directly as implemented in collector.go
	var envPairs []string
	for k, v := range p.Environ {
		val := v
		if IsSecretKey(k) {
			val = "[REDACTED]"
			if k == "DB_PASSWORD" {
				hasRedactedEnv = true
			}
		}
		envPairs = append(envPairs, fmt.Sprintf("%s=%s", k, val))
	}

	if !hasRedactedEnv {
		t.Error("expected DB_PASSWORD to be redacted in environment variables string list")
	}

	for _, pair := range envPairs {
		if strings.Contains(pair, "unsafe-pwd-here") {
			t.Error("sensitive password value leaked into environment variables list!")
		}
	}
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
	out := pc.scrubEnviron(map[string]string{
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
	out := pc.scrubEnviron(map[string]string{"API_KEY": "abc", "USER": "gitlab"})
	if !strings.Contains(out, "API_KEY=[REDACTED]") {
		t.Errorf("expected built-in denylist to redact API_KEY, got %q", out)
	}
	if !strings.Contains(out, "USER=gitlab") {
		t.Errorf("expected USER to pass through, got %q", out)
	}
}
