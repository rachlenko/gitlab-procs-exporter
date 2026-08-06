package exporter

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

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

// ciJobLabelValuesWith extracts the promoted CI label values from a process
// environ map. A missing variable yields "" — Prometheus treats an empty
// label value as absent, so non-CI processes simply carry no job identity.
//
// Bounding happens here rather than at the call sites so that every consumer
// of these values inherits the MaxLabelBytes contract: the ci_* labels ride on
// all 12 metrics, so an oversized value multiplies 12x into the TSDB index.
// Order is fixed: sanitize first (make it valid UTF-8), then bound.
//
// obs may be nil for callers without a collector (tests); a collector passes
// itself so the cuts are counted.
func ciJobLabelValuesWith(limits map[string]int, environ map[string]string, obs truncationObserver) []string {
	vals := make([]string, len(ciJobLabelKeys))
	for i, k := range ciJobLabelKeys {
		vals[i] = boundLabelWith(limits, k.label, sanitizeLabelValue(environ[k.env]), obs)
	}
	return vals
}

// ProcessCollector translates active processes in HistoryStore into Prometheus metrics.
type ProcessCollector struct {
	store *HistoryStore

	// extraKeySubstrings are operator-configured key-name substrings that
	// augment the built-in IsSecretKey denylist (normalized: lowercase, trimmed).
	extraKeySubstrings []string

	// maxLabelBytes is this collector's effective limit table: the published
	// MaxLabelBytes contract merged with any operator overrides. It is always
	// non-nil and is never the package map itself, so one configured collector
	// cannot change the limits another one applies.
	maxLabelBytes map[string]int

	// Metric Descriptors
	cpuDesc                 *prometheus.Desc
	rssDesc                 *prometheus.Desc
	vmsDesc                 *prometheus.Desc
	ioReadDesc              *prometheus.Desc
	ioWriteDesc             *prometheus.Desc
	ioReadSelfDesc          *prometheus.Desc
	ioWriteSelfDesc         *prometheus.Desc
	ioReadSyscallsDesc      *prometheus.Desc
	ioWriteSyscallsDesc     *prometheus.Desc
	ioReadSyscallsSelfDesc  *prometheus.Desc
	ioWriteSyscallsSelfDesc *prometheus.Desc
	infoDesc                *prometheus.Desc

	// truncations counts how often the MaxLabelBytes contract actually cut a
	// value, keyed by label name. Truncation is otherwise silent — cmdline is
	// being cut in production today and nothing says so. It is emitted through
	// Describe/Collect below rather than registered separately, so it rides on
	// whatever registry the collector itself is registered on.
	truncations *prometheus.CounterVec
}

// syscallHelp explains, once, that these counters are syscalls and not IOPS.
// A panel titled "IOPS" built on them would be quietly wrong, so say it here.
const syscallHelp = " These are read(2)/write(2) call counts, NOT block-device IOPS: the page cache " +
	"merges and splits them on the way to the disk, and the kernel does not attribute block operations " +
	"back to the process that dirtied the page. Use node_disk_*_completed_total for true device IOPS."

// NewProcessCollectorWithConfig creates a ProcessCollector from a loaded
// Config, applying both operator knobs: the extra redaction substrings and the
// MaxLabelBytes overrides. A nil cfg means "no config file" and yields the
// defaults — not "no limits".
//
// It is a separate entry point rather than another parameter on
// NewProcessCollector so the existing variadic call sites keep compiling
// unchanged. cfg.MaxLabelBytes is expected to have come through LoadConfig,
// which validates it; entries for unknown labels are ignored here rather than
// silently widening the table.
func NewProcessCollectorWithConfig(store *HistoryStore, cfg *Config) *ProcessCollector {
	if cfg == nil {
		return NewProcessCollector(store)
	}
	pc := NewProcessCollector(store, cfg.RedactKeySubstrings...)
	pc.maxLabelBytes = mergedMaxLabelBytes(cfg.MaxLabelBytes)
	return pc
}

// NewProcessCollector creates and initializes a ProcessCollector with the
// default label-size contract. See NewProcessCollectorWithConfig to apply
// operator overrides.
func NewProcessCollector(store *HistoryStore, extraKeySubstrings ...string) *ProcessCollector {
	commonLabels := append([]string{"pid", "name"}, ciJobLabelNames()...)

	truncations := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gitlab_exporter_label_truncations_total",
		Help: "Label values cut by the MaxLabelBytes contract, by label name. " +
			"Counted per gather, not per distinct value: a scrape that cuts N " +
			"label values adds N, so a single long-lived process keeps adding " +
			"on every scrape and the rate scales with scrape frequency and with " +
			"the number of scrapers. Read it as a yes/no signal, not a count of " +
			"distinct values. Non-zero means the limit for that label is too low " +
			"for this host's data, and the affected series carry a fingerprint " +
			"marker instead of their full value.",
	}, []string{"label"})
	// Initialize every contract label at zero so a scrape distinguishes "nothing
	// was truncated" from "this label is not instrumented" — an absent series
	// reads as the latter and makes an alert on it silently never fire.
	for label := range MaxLabelBytes {
		truncations.WithLabelValues(label)
	}

	return &ProcessCollector{
		store:              store,
		extraKeySubstrings: normalizeSubstrings(extraKeySubstrings),
		maxLabelBytes:      mergedMaxLabelBytes(nil),
		truncations:        truncations,
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
			"Total bytes read from disk by the process AND by every descendant it has reaped. "+
				"Reapers (pid 1, job shells) therefore report I/O they never issued; use "+
				"gitlab_process_self_io_read_bytes_total to attribute bytes to the process that issued them.",
			commonLabels, nil,
		),
		ioWriteDesc: prometheus.NewDesc(
			"gitlab_process_io_write_bytes_total",
			"Total bytes written to disk by the process AND by every descendant it has reaped. "+
				"Reapers (pid 1, job shells) therefore report I/O they never issued; use "+
				"gitlab_process_self_io_write_bytes_total to attribute bytes to the process that issued them.",
			commonLabels, nil,
		),
		ioReadSelfDesc: prometheus.NewDesc(
			"gitlab_process_self_io_read_bytes_total",
			"Bytes read from disk by the process's own threads, excluding anything it merely reaped. "+
				"Bytes read by an already-exited child are counted by neither this metric nor any "+
				"other series, so summing it across processes understates the node total.",
			commonLabels, nil,
		),
		ioWriteSelfDesc: prometheus.NewDesc(
			"gitlab_process_self_io_write_bytes_total",
			"Bytes written to disk by the process's own threads, excluding anything it merely reaped. "+
				"Bytes written by an already-exited child are counted by neither this metric nor any "+
				"other series, so summing it across processes understates the node total.",
			commonLabels, nil,
		),
		ioReadSyscallsDesc: prometheus.NewDesc(
			"gitlab_process_io_read_syscalls_total",
			"Read syscalls issued by the process AND by every descendant it has reaped."+syscallHelp,
			commonLabels, nil,
		),
		ioWriteSyscallsDesc: prometheus.NewDesc(
			"gitlab_process_io_write_syscalls_total",
			"Write syscalls issued by the process AND by every descendant it has reaped."+syscallHelp,
			commonLabels, nil,
		),
		ioReadSyscallsSelfDesc: prometheus.NewDesc(
			"gitlab_process_self_io_read_syscalls_total",
			"Read syscalls issued by the process's own threads, excluding anything it merely reaped."+syscallHelp,
			commonLabels, nil,
		),
		ioWriteSyscallsSelfDesc: prometheus.NewDesc(
			"gitlab_process_self_io_write_syscalls_total",
			"Write syscalls issued by the process's own threads, excluding anything it merely reaped."+syscallHelp,
			commonLabels, nil,
		),
		infoDesc: prometheus.NewDesc(
			"gitlab_process_info",
			"Metadata about the process including cmdline and parsed environ variables (scrubbed for secrets).",
			append([]string{"pid", "name", "cmdline", "environ", "environ_truncated"}, ciJobLabelNames()...), nil,
		),
	}
}

// boundLabel applies this collector's limit table and counts what it cuts.
func (pc *ProcessCollector) boundLabel(name, value string) string {
	return boundLabelWith(pc.maxLabelBytes, name, value, pc)
}

// observeTruncation implements truncationObserver: boundLabelWith calls it once
// per value it actually cut. CounterVec is safe for concurrent use, which
// matters because Collect runs on the registry's gather goroutine.
//
// No nil guard: the constructor always sets truncations, and Describe/Collect
// already dereference it unguarded. Guarding here and not there would leave the
// zero-value collector half-supported, which is worse than not supporting it.
func (pc *ProcessCollector) observeTruncation(label string) {
	pc.truncations.WithLabelValues(label).Inc()
}

// keyMatchesAny reports whether key (case-insensitively) contains any of the
// given normalized substrings.
func keyMatchesAny(key string, substrings []string) bool {
	k := strings.ToLower(key)
	for _, s := range substrings {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// isSensitivePair reports whether an environ variable must be redacted, by the
// key denylist (IsSecretKey), operator-configured substrings (must already be
// normalized, see normalizeSubstrings), or the value-shape heuristics
// (IsSecretValue). This is the single source of truth shared by the Prometheus
// label path (scrubEnviron) and the JSON API path (RedactEnviron) so the two
// can never disagree on what counts as a secret.
func isSensitivePair(key, val string, extraKeySubstrings []string) bool {
	return IsSecretKey(key) || keyMatchesAny(key, extraKeySubstrings) || IsSecretValue(val)
}

// RedactEnviron returns a copy of environ with every sensitive value replaced
// by "[REDACTED]", applying the same rules as the gitlab_process_info environ
// label (see isSensitivePair). Anything that leaves the process carrying an
// environ — the JSON API in particular — must go through here.
func RedactEnviron(environ map[string]string, extraKeySubstrings []string) map[string]string {
	out := make(map[string]string, len(environ))
	for k, v := range environ {
		if isSensitivePair(k, v, extraKeySubstrings) {
			v = "[REDACTED]"
		}
		out[k] = v
	}
	return out
}

// Bounds on the gitlab_process_info "environ" label so a single process can't
// emit an unbounded value and fail the whole Prometheus scrape. A process that
// carries its config in the environment (tens of KB) is the case these guard.
const (
	// maxEnvironVars caps how many variables (sorted by key) are emitted.
	maxEnvironVars = 100
	// maxEnvironValueLen caps a single value's length in bytes; a longer value
	// is replaced WHOLE by environTruncMarker, which carries the original
	// length and a fingerprint but none of the body. The marker is always
	// shorter than this cap, so a truncated value never exceeds it.
	maxEnvironValueLen = 256
	// maxEnvironBytes is a hard ceiling on the joined label. It's a conservative,
	// self-imposed cap that keeps label values small and predictable; 100 vars *
	// 256 bytes could otherwise reach ~28KB. This is the backstop that actually
	// bounds the label. Prometheus's label_value_length_limit defaults to 0
	// (unlimited), so this cap — not that limit — is what keeps the value small;
	// an operator who sets label_value_length_limit below 8192 must lower this too.
	maxEnvironBytes = 8192
)

// sanitizeLabelValue replaces invalid UTF-8 bytes with the Unicode
// replacement character. MustNewConstMetric panics on invalid UTF-8, and a
// panic in Collect happens on the registry's gather goroutine — it would
// crash the whole exporter. Every label value sourced from /proc (name,
// cmdline, environ, CI variables) must pass through here.
func sanitizeLabelValue(v string) string {
	if utf8.ValidString(v) {
		return v
	}
	return strings.ToValidUTF8(v, string(utf8.RuneError))
}

// scrubEnviron renders the environ map as a comma-joined "k=v" string,
// redacting any pair whose key or value looks sensitive (see isSensitivePair)
// and bounding the total size (see maxEnviron*).
//
// The returned bool is the gitlab_process_info "environ_truncated" flag and
// means exactly one thing: the variable LIST is incomplete — one or more
// variables were entirely omitted, either because there were more than
// maxEnvironVars of them or because the maxEnvironBytes ceiling was reached.
// It is deliberately NOT set by [REDACTED] or by per-value truncation: those
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
		val := sanitizeLabelValue(environ[k])
		switch {
		// Arm order is load-bearing: redaction must win so the marker never
		// carries a fingerprint of a known secret.
		case isSensitivePair(k, val, pc.extraKeySubstrings):
			val = "[REDACTED]"
		// Length alone is a secret signal in environ — see environTruncMarker
		// for why the body is dropped here rather than truncated to a prefix.
		case len(val) > maxEnvironValueLen:
			val = environTruncMarker(val)
		}
		pair := fmt.Sprintf("%s=%s", sanitizeLabelValue(k), val)

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
	ch <- pc.ioReadSelfDesc
	ch <- pc.ioWriteSelfDesc
	ch <- pc.ioReadSyscallsDesc
	ch <- pc.ioWriteSyscallsDesc
	ch <- pc.ioReadSyscallsSelfDesc
	ch <- pc.ioWriteSyscallsSelfDesc
	ch <- pc.infoDesc
	pc.truncations.Describe(ch)
}

// Collect implements the prometheus.Collector interface.
func (pc *ProcessCollector) Collect(ch chan<- prometheus.Metric) {
	processes := pc.store.GetActiveProcesses()

	for _, p := range processes {
		pidStr := fmt.Sprintf("%d", p.PID)
		name := pc.boundLabel("name", sanitizeLabelValue(p.Name))
		ciVals := ciJobLabelValuesWith(pc.maxLabelBytes, p.Environ, pc)
		labels := append([]string{pidStr, name}, ciVals...)

		// Emit core stats
		ch <- prometheus.MustNewConstMetric(pc.cpuDesc, prometheus.CounterValue, p.CPUSeconds, labels...)
		ch <- prometheus.MustNewConstMetric(pc.rssDesc, prometheus.GaugeValue, float64(p.MemoryRSS), labels...)
		ch <- prometheus.MustNewConstMetric(pc.vmsDesc, prometheus.GaugeValue, float64(p.MemoryVMS), labels...)
		ch <- prometheus.MustNewConstMetric(pc.ioReadDesc, prometheus.CounterValue, float64(p.IORead), labels...)
		ch <- prometheus.MustNewConstMetric(pc.ioWriteDesc, prometheus.CounterValue, float64(p.IOWrite), labels...)
		ch <- prometheus.MustNewConstMetric(pc.ioReadSelfDesc, prometheus.CounterValue, float64(p.IOReadSelf), labels...)
		ch <- prometheus.MustNewConstMetric(pc.ioWriteSelfDesc, prometheus.CounterValue, float64(p.IOWriteSelf), labels...)
		ch <- prometheus.MustNewConstMetric(pc.ioReadSyscallsDesc, prometheus.CounterValue, float64(p.IOReadSyscalls), labels...)
		ch <- prometheus.MustNewConstMetric(pc.ioWriteSyscallsDesc, prometheus.CounterValue, float64(p.IOWriteSyscalls), labels...)
		ch <- prometheus.MustNewConstMetric(pc.ioReadSyscallsSelfDesc, prometheus.CounterValue, float64(p.IOReadSyscallsSelf), labels...)
		ch <- prometheus.MustNewConstMetric(pc.ioWriteSyscallsSelfDesc, prometheus.CounterValue, float64(p.IOWriteSyscallsSelf), labels...)

		// Emit metadata info metric (environ scrubbed for secrets and bounded)
		environ, environTruncated := pc.scrubEnviron(p.Environ)
		truncatedLabel := "0"
		if environTruncated {
			truncatedLabel = "1"
		}
		cmdline := pc.boundLabel("cmdline", sanitizeLabelValue(p.CmdLine))
		infoLabels := append([]string{pidStr, name, cmdline, environ, truncatedLabel}, ciVals...)
		ch <- prometheus.MustNewConstMetric(pc.infoDesc, prometheus.GaugeValue, 1.0, infoLabels...)
	}

	// Emitted last so this scrape's own truncations are already counted.
	pc.truncations.Collect(ch)
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
