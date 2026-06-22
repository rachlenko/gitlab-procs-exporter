package exporter

import (
	"fmt"
	"math"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// ProcessCollector translates active processes in HistoryStore into Prometheus metrics.
type ProcessCollector struct {
	store *HistoryStore

	// Metric Descriptors
	cpuDesc     *prometheus.Desc
	rssDesc     *prometheus.Desc
	vmsDesc     *prometheus.Desc
	ioReadDesc  *prometheus.Desc
	ioWriteDesc *prometheus.Desc
	infoDesc    *prometheus.Desc
}

// NewProcessCollector creates and initializes a ProcessCollector.
func NewProcessCollector(store *HistoryStore) *ProcessCollector {
	commonLabels := []string{"pid", "name"}

	return &ProcessCollector{
		store: store,
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
			[]string{"pid", "name", "cmdline", "environ"}, nil,
		),
	}
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
		labels := []string{pidStr, p.Name}

		// Emit core stats
		ch <- prometheus.MustNewConstMetric(pc.cpuDesc, prometheus.CounterValue, p.CPUUsage, labels...)
		ch <- prometheus.MustNewConstMetric(pc.rssDesc, prometheus.GaugeValue, float64(p.MemoryRSS), labels...)
		ch <- prometheus.MustNewConstMetric(pc.vmsDesc, prometheus.GaugeValue, float64(p.MemoryVMS), labels...)
		ch <- prometheus.MustNewConstMetric(pc.ioReadDesc, prometheus.CounterValue, float64(p.IORead), labels...)
		ch <- prometheus.MustNewConstMetric(pc.ioWriteDesc, prometheus.CounterValue, float64(p.IOWrite), labels...)

		// Scrub environment variables for security before exposing via metrics
		var envPairs []string
		for k, v := range p.Environ {
			val := v
			if IsSecretKey(k) || IsSecretValue(v) {
				val = "[REDACTED]"
			}
			envPairs = append(envPairs, fmt.Sprintf("%s=%s", k, val))
		}
		envStr := strings.Join(envPairs, ", ")

		// Emit metadata info metric
		infoLabels := []string{pidStr, p.Name, p.CmdLine, envStr}
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
