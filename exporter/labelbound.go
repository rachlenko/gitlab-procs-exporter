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
	// maxEnvironMarkerLen is the worst-case size of environTruncMarker, which
	// carries the same length+fingerprint payload as maxMarkerLen but replaces
	// the value body instead of following it.
	maxEnvironMarkerLen = len("[TRUNCATED;len=") + truncMaxLenDigits +
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
// the config was loaded for.
//
// It re-checks each override rather than trusting validateMaxLabelBytes to have
// run. LoadConfig validates, but NewProcessCollectorWithConfig is exported and
// accepts any *Config, and the failure mode of a non-positive limit is
// fail-OPEN: truncateWithFingerprint reads max <= 0 as "no limit configured"
// and passes the value through, silently disabling the very bound the operator
// was configuring. An unknown name is dropped for the same reason it cannot be
// applied — it would widen the table past the published contract.
func mergedMaxLabelBytes(overrides map[string]int) map[string]int {
	out := make(map[string]int, len(MaxLabelBytes))
	for label, limit := range MaxLabelBytes {
		out[label] = limit
	}
	for label, limit := range overrides {
		if _, ok := out[label]; !ok || limit < minLabelBytes {
			continue
		}
		out[label] = limit
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
	// Walk back off a partial rune: MustNewConstMetric panics on invalid UTF-8,
	// and that panic fires on the registry's gather goroutine, crashing the
	// whole exporter.
	i := max
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	// Hash the ORIGINAL, before cutting — the fingerprint has to identify the
	// value that was lost, not the prefix that survived.
	return s[:i] + truncEllipsis + "[len=" + strconv.Itoa(len(s)) +
		";sha256=" + fingerprint(s) + "]"
}

// environTruncMarker replaces an over-long environ value ENTIRELY — body
// included — while still carrying the original length and fingerprint.
//
// environ is the one label composed of arbitrary, operator-unknown key/value
// pairs, and length alone is a secret signal there: isSensitivePair only
// recognises token-shaped values (isTokenCharset rejects anything with braces,
// quotes, colons or newlines), so a JSON service-account blob, a PEM body or a
// connection string falls through unless its KEY hits the denylist. Emitting a
// prefix of those would publish credential material to every scraper, which is
// why an over-long environ value gets no prefix at all. Bounded labels with a
// known, non-secret shape (name, cmdline, ci_*) keep their prefix via
// truncateWithFingerprint.
//
// The result is always shorter than maxEnvironValueLen for any value that
// reaches it, so it can only shrink the joined label.
func environTruncMarker(s string) string {
	return "[TRUNCATED;len=" + strconv.Itoa(len(s)) +
		";sha256=" + fingerprint(s) + "]"
}

// fingerprint returns the leading hex chars of sha256(s) used by both markers.
func fingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:truncFingerprintLen]
}

// truncationObserver is notified every time boundLabelWith actually cuts a
// value. It exists so the counter can live on ProcessCollector — which is what
// owns registration, and an unregistered counter reports nothing — while
// boundLabelWith stays a pure function usable without a registry.
type truncationObserver interface {
	observeTruncation(label string)
}

// boundLabelWith applies a limit table to one label value. A label with no
// entry in the table passes through unchanged — silently bounding it would
// hide the fact that the table is missing an entry.
//
// Ordering is fixed and must not change: sanitizeLabelValue first (make the
// value valid UTF-8), then redact, then bound. Bounding invalid UTF-8 makes the
// rune walk-back meaningless — strings.ToValidUTF8 expands each bad byte to a
// 3-byte U+FFFD, so sanitizing after the cut pushes the result back past the
// limit.
//
// obs may be nil for callers with no registry (tests); every production path
// passes the collector, because cutting a label value is a silent, lossy event
// and cmdline proves it happens unannounced.
func boundLabelWith(limits map[string]int, name, value string, obs truncationObserver) string {
	max, ok := limits[name]
	if !ok {
		return value
	}
	bounded := truncateWithFingerprint(value, max)
	if bounded != value && obs != nil {
		obs.observeTruncation(name)
	}
	return bounded
}
