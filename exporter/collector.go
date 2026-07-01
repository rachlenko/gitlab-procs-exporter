package exporter

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// ciJobLabelKeys maps GitLab CI environment variables to dedicated metric
// labels. Promoting them out of the `environ` blob lets resource metrics be
// grouped/joined by job directly (no regexp over environ at query time).
// The order here defines label order everywhere it's used.
var ciJobLabelKeys = []struct{ env, label string }{
	{"CI_JOB_ID", "ci_job_id"},
	{"CI_JOB_NAME", "ci_job_name"},
	{"CI_PROJECT_PATH", "ci_project_path"},
	{"CI_PIPELINE_ID", "ci_pipeline_id"},
}

// ciJobLabelNames returns just the label names, in order.
func ciJobLabelNames() []string {
	names := make([]string, len(ciJobLabelKeys))
	for i, k := range ciJobLabelKeys {
		names[i] = k.label
	}
	return names
}

// ciJobLabelValues extracts the promoted CI label values from a process
// environ map. A missing variable yields "" — Prometheus treats an empty
// label value as absent, so non-CI processes simply carry no job identity.
func ciJobLabelValues(environ map[string]string) []string {
	vals := make([]string, len(ciJobLabelKeys))
	for i, k := range ciJobLabelKeys {
		vals[i] = environ[k.env]
	}
	return vals
}

// ProcessCollector translates active processes in HistoryStore into Prometheus metrics.
type ProcessCollector struct {
	store *HistoryStore

	// extraKeySubstrings are operator-configured key-name substrings that
	// augment the built-in IsSecretKey denylist (normalized: lowercase, trimmed).
	extraKeySubstrings []string

	// Metric Descriptors
	cpuDesc     *prometheus.Desc
	rssDesc     *prometheus.Desc
	vmsDesc     *prometheus.Desc
	ioReadDesc  *prometheus.Desc
	ioWriteDesc *prometheus.Desc
	infoDesc    *prometheus.Desc
}

// NewProcessCollector creates and initializes a ProcessCollector.
func NewProcessCollector(store *HistoryStore, extraKeySubstrings ...string) *ProcessCollector {
	commonLabels := append([]string{"pid", "name"}, ciJobLabelNames()...)

	return &ProcessCollector{
		store:              store,
		extraKeySubstrings: normalizeSubstrings(extraKeySubstrings),
		cpuDesc: prometheus.NewDesc(
			"gitlab_process_cpu_seconds_total",
			"Total user and system CPU time spent in seconds.",
			commonLabels, nil,
		),
		rssDesc: prometheus.NewDesc(
			"gitlab_process_resident_memory_bytes",
			"Resident set size (RSS) in bytes.",
			commonLabels, nil,
		),
		vmsDesc: prometheus.NewDesc(
			"gitlab_process_virtual_memory_bytes",
			"Virtual memory size (VMS) in bytes.",
			commonLabels, nil,
		),
		ioReadDesc: prometheus.NewDesc(
			"gitlab_process_io_read_bytes_total",
			"Total bytes read from disk.",
			commonLabels, nil,
		),
		ioWriteDesc: prometheus.NewDesc(
			"gitlab_process_io_write_bytes_total",
			"Total bytes written to disk.",
			commonLabels, nil,
		),
		infoDesc: prometheus.NewDesc(
			"gitlab_process_info",
			"Metadata about the process including cmdline and parsed environ variables (scrubbed for secrets).",
			append([]string{"pid", "name", "cmdline", "environ", "environ_truncated"}, ciJobLabelNames()...), nil,
		),
	}
}

// keyInExtra reports whether key matches any operator-configured substring.
func (pc *ProcessCollector) keyInExtra(key string) bool {
	k := strings.ToLower(key)
	for _, s := range pc.extraKeySubstrings {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// Bounds on the gitlab_process_info "environ" label so a single process can't
// emit an unbounded value and fail the whole Prometheus scrape. A process that
// carries its config in the environment (tens of KB) is the case these guard.
const (
	// maxEnvironVars caps how many variables (sorted by key) are emitted.
	maxEnvironVars = 100
	// maxEnvironValueLen caps a single value's length in bytes; longer values
	// are replaced with environValueTruncMarker whole (never byte-cut, so the
	// label stays valid UTF-8).
	maxEnvironValueLen = 256
	// maxEnvironBytes is a hard ceiling on the joined label, kept below the
	// typical Prometheus label_value_length_limit (10240) so we never trip it.
	// 100 vars * 256 bytes could otherwise reach ~28KB, so this is the backstop
	// that actually guarantees the scrape survives.
	maxEnvironBytes = 8192
)

// environValueTruncMarker replaces a value longer than maxEnvironValueLen. It's
// distinct from "[REDACTED]" so "too long" is distinguishable from "secret".
const environValueTruncMarker = "[TRUNCATED]"

// scrubEnviron renders the environ map as a comma-joined "k=v" string,
// redacting any pair whose key or value looks sensitive (IsSecretKey /
// keyInExtra / IsSecretValue) and bounding the total size (see maxEnviron*).
//
// The returned bool is the gitlab_process_info "environ_truncated" flag and
// means exactly one thing: the variable LIST is incomplete — one or more
// variables were entirely omitted, either because there were more than
// maxEnvironVars of them or because the maxEnvironBytes ceiling was reached.
// It is deliberately NOT set by [REDACTED] or by per-value [TRUNCATED]: those
// keep the variable present (only its value changes), so the list is complete.
func (pc *ProcessCollector) scrubEnviron(environ map[string]string) (string, bool) {
	// Sort keys so the gitlab_process_info "environ" label is stable across
	// scrapes (map iteration order is otherwise non-deterministic).
	keys := make([]string, 0, len(environ))
	for k := range environ {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	truncated := false
	if len(keys) > maxEnvironVars {
		keys = keys[:maxEnvironVars]
		truncated = true
	}

	var b strings.Builder
	for _, k := range keys {
		val := environ[k]
		switch {
		case IsSecretKey(k) || pc.keyInExtra(k) || IsSecretValue(val):
			val = "[REDACTED]"
		case len(val) > maxEnvironValueLen:
			val = environValueTruncMarker
		}
		pair := fmt.Sprintf("%s=%s", k, val)

		sep := 0
		if b.Len() > 0 {
			sep = len(", ")
		}
		// Stop at a pair boundary once the ceiling would be exceeded, so the
		// label is always valid UTF-8 and never over maxEnvironBytes.
		if b.Len()+sep+len(pair) > maxEnvironBytes {
			truncated = true
			break
		}
		if sep > 0 {
			b.WriteString(", ")
		}
		b.WriteString(pair)
	}
	return b.String(), truncated
}

// Describe implements the prometheus.Collector interface.
func (pc *ProcessCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- pc.cpuDesc
	ch <- pc.rssDesc
	ch <- pc.vmsDesc
	ch <- pc.ioReadDesc
	ch <- pc.ioWriteDesc
	ch <- pc.infoDesc
}

// Collect implements the prometheus.Collector interface.
func (pc *ProcessCollector) Collect(ch chan<- prometheus.Metric) {
	processes := pc.store.GetActiveProcesses()

	for _, p := range processes {
		pidStr := fmt.Sprintf("%d", p.PID)
		ciVals := ciJobLabelValues(p.Environ)
		labels := append([]string{pidStr, p.Name}, ciVals...)

		// Emit core stats
		ch <- prometheus.MustNewConstMetric(pc.cpuDesc, prometheus.CounterValue, p.CPUUsage, labels...)
		ch <- prometheus.MustNewConstMetric(pc.rssDesc, prometheus.GaugeValue, float64(p.MemoryRSS), labels...)
		ch <- prometheus.MustNewConstMetric(pc.vmsDesc, prometheus.GaugeValue, float64(p.MemoryVMS), labels...)
		ch <- prometheus.MustNewConstMetric(pc.ioReadDesc, prometheus.CounterValue, float64(p.IORead), labels...)
		ch <- prometheus.MustNewConstMetric(pc.ioWriteDesc, prometheus.CounterValue, float64(p.IOWrite), labels...)

		// Emit metadata info metric (environ scrubbed for secrets and bounded)
		environ, environTruncated := pc.scrubEnviron(p.Environ)
		truncatedLabel := "0"
		if environTruncated {
			truncatedLabel = "1"
		}
		infoLabels := append([]string{pidStr, p.Name, p.CmdLine, environ, truncatedLabel}, ciVals...)
		ch <- prometheus.MustNewConstMetric(pc.infoDesc, prometheus.GaugeValue, 1.0, infoLabels...)
	}
}

// IsSecretKey checks if the key name suggests it holds sensitive credentials.
func IsSecretKey(key string) bool {
	k := strings.ToLower(key)
	secrets := []string{
		"key", "pass", "passwd", "token", "secret", "auth", "pwd", "db", "url",
		"private", "crypt", "credential", "signature", "api",
		"cert", "ssh", "gpg", "jwt", "bearer", "access", "cookie", "session",
		"salt", "otp", "webhook", "dsn", "connection", "client_secret",
	}
	for _, s := range secrets {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// IsSecretValue reports whether a value looks like a secret regardless of key.
func IsSecretValue(v string) bool {
	// tokenPrefixes is read-only.
	tokenPrefixes := []string{
		"glpat-", "gho_", "ghp_", "ghu_", "ghs_", "github_pat_",
		"AKIA", "xoxb-", "xoxp-", "xoxa-",
	}
	if v == "" {
		return false
	}
	for _, p := range tokenPrefixes {
		if strings.HasPrefix(v, p) {
			return true
		}
	}
	// JWT: "eyJ" header + two dot-separated segments.
	if strings.HasPrefix(v, "eyJ") && strings.Count(v, ".") == 2 {
		return true
	}
	// Long, high-entropy token-charset string.
	if len(v) >= 32 && isTokenCharset(v) && shannonEntropy(v) >= 3.5 {
		return true
	}
	return false
}

func isTokenCharset(v string) bool {
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '+' || r == '/' || r == '=' || r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

func shannonEntropy(v string) float64 {
	counts := make(map[rune]float64)
	for _, r := range v {
		counts[r]++
	}
	n := float64(len(v))
	var h float64
	for _, c := range counts {
		p := c / n
		h -= p * math.Log2(p)
	}
	return h
}
