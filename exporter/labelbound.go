package exporter

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"unicode/utf8"
)

// MaxLabelBytes is the published contract for every label value this exporter
// emits: no label value ever exceeds its limit, and anything longer is
// truncated informatively (see truncateWithFingerprint) rather than dropped.
//
// Limits are BYTE counts, never rune counts — Prometheus and the exposition
// format are byte-oriented, and so is the index memory a label value costs.
//
// "environ" is deliberately absent: it is a composed blob with its own
// three-way bound (maxEnvironVars / maxEnvironValueLen / maxEnvironBytes).
// "pid" and "environ_truncated" are exporter-generated and structurally
// bounded, so they need no entry either.
var MaxLabelBytes = map[string]int{
	"name":            128,  // observed max 39; headroom for long kthread names
	"cmdline":         2048, // ARG_MAX can reach 2MB; this cap is already being hit
	"ci_job_name":     256,  // parallel:matrix jobs embed matrix values
	"ci_project_path": 256,  // group/subgroup/.../project nesting
	"ci_job_id":       32,   // numeric
	"ci_pipeline_id":  32,   // numeric
}

const (
	// truncEllipsis separates the surviving prefix from the marker.
	truncEllipsis = "…"
	// truncFingerprintLen is how many hex chars of sha256(original) the marker
	// carries. 12 hex chars = 48 bits: enough that a collision between two
	// values sharing a prefix is not a practical concern, short enough that the
	// marker stays small next to the limits above.
	truncFingerprintLen = 12
	// truncMaxLenDigits budgets for the decimal original length in the marker.
	// 20 digits covers any int64, so maxMarkerLen is a true upper bound rather
	// than one that holds only for values below some size.
	truncMaxLenDigits = 20
	// maxMarkerLen is the worst-case marker size. The bounded result is
	// therefore never longer than limit+maxMarkerLen — a marker appended past
	// the limit is the classic way a "bounded" value stops being bounded, so
	// the ceiling is stated here and asserted in the tests.
	maxMarkerLen = len(truncEllipsis) + len("[len=") + truncMaxLenDigits +
		len(";sha256=") + truncFingerprintLen + len("]")
	// minLabelBytes is the floor for an operator-supplied limit. Below the
	// marker's own worst-case size, truncation stops bounding anything: the
	// result is longer than the limit, and what survives is mostly marker rather
	// than data. Two of the built-in defaults (the numeric ci_job_id /
	// ci_pipeline_id, at 32) sit below this floor on purpose — they bound values
	// that are ~7 bytes in practice and never truncate — but an operator has no
	// such context, so overrides are held to the floor.
	minLabelBytes = maxMarkerLen
)

// mergedMaxLabelBytes returns the effective limit table: a copy of the
// published MaxLabelBytes contract with the operator's overrides applied.
//
// It copies rather than mutating: MaxLabelBytes is package state shared by
// every collector in the process, and an override belongs to the one collector
// the config was loaded for. Overrides are expected to have passed
// validateMaxLabelBytes already, so unknown names cannot enlarge the table.
func mergedMaxLabelBytes(overrides map[string]int) map[string]int {
	out := make(map[string]int, len(MaxLabelBytes))
	for label, limit := range MaxLabelBytes {
		out[label] = limit
	}
	for label, limit := range overrides {
		if _, ok := out[label]; ok {
			out[label] = limit
		}
	}
	return out
}

// truncateWithFingerprint caps a valid-UTF-8 value at max bytes, cutting at a
// rune boundary and appending a marker that carries the original byte length
// and a fingerprint of the original value:
//
//	<prefix>…[len=<N>;sha256=<first 12 hex of sha256(original)>]
//
// The fingerprint is what makes truncation reversible in practice: given a
// suspect series you can hash candidate values and confirm the match, which a
// bare "[TRUNCATED]" makes impossible. The trade-off is cardinality — distinct
// over-long values stay distinct instead of collapsing into one series.
//
// Truncation is deterministic: identical input always yields an identical
// label value, or the series would churn on every scrape.
//
// Input must already be sanitized (see sanitizeLabelValue). A max of zero or
// less means "no limit configured" and passes the value through; operator
// overrides reject such limits at config-load time.
func truncateWithFingerprint(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	// Hash the ORIGINAL, before cutting — the fingerprint has to identify the
	// value that was lost, not the prefix that survived.
	sum := sha256.Sum256([]byte(s))
	fingerprint := hex.EncodeToString(sum[:])[:truncFingerprintLen]

	// Walk back off a partial rune: MustNewConstMetric panics on invalid UTF-8,
	// and that panic fires on the registry's gather goroutine, crashing the
	// whole exporter.
	i := max
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + truncEllipsis + "[len=" + strconv.Itoa(len(s)) +
		";sha256=" + fingerprint + "]"
}

// truncationObserver is notified every time boundLabel actually cuts a value.
// It exists so the counter can live on ProcessCollector — which is what owns
// registration, and an unregistered counter reports nothing — while boundLabel
// stays a pure function usable without a registry.
type truncationObserver interface {
	observeTruncation(label string)
}

// boundLabel applies the default MaxLabelBytes contract to one label value.
// Use boundLabelWith when a collector carries operator overrides.
func boundLabel(name, value string, obs ...truncationObserver) string {
	return boundLabelWith(MaxLabelBytes, name, value, obs...)
}

// boundLabelWith applies a limit table to one label value. A label with no
// entry in the table passes through unchanged — silently bounding it would
// hide the fact that the table is missing an entry.
//
// Ordering is fixed and must not change: sanitizeLabelValue first (make the
// value valid UTF-8), then redact, then bound. Bounding invalid UTF-8 makes the
// rune walk-back meaningless.
//
// Callers that can observe truncation should pass one: cutting a label value
// is a silent, lossy event, and cmdline proves it happens in production
// unannounced.
func boundLabelWith(limits map[string]int, name, value string, obs ...truncationObserver) string {
	max, ok := limits[name]
	if !ok {
		return value
	}
	bounded := truncateWithFingerprint(value, max)
	if bounded != value {
		for _, o := range obs {
			o.observeTruncation(name)
		}
	}
	return bounded
}
