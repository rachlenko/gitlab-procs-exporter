package exporter

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"unicode/utf8"
)

// MaxLabelBytes is the published contract for every label value this exporter
// emits: anything longer than its limit is truncated informatively (see
// truncateWithFingerprint) rather than dropped.
//
// The bound is limit+maxMarkerLen, NOT limit: a truncated value carries a
// marker appended past the cut, worst case 49 bytes (see maxMarkerLen). Size a
// downstream label_value_length_limit against limit+maxMarkerLen, never against
// the raw limit — Prometheus rejects the WHOLE scrape when a label value
// exceeds that limit, so getting this wrong turns one long value into total
// data loss. The margin dominates for the small limits: ci_job_id at 32 yields
// a worst case of 81.
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
	// replaces the value body instead of following it and carries the length
	// alone — no fingerprint, see environTruncMarker for why.
	maxEnvironMarkerLen = len("[TRUNCATED;len=") + truncMaxLenDigits + len("]")
	// minLabelBytes is the floor for an operator-supplied limit. Below the
	// marker's own worst-case size, truncation stops bounding anything: the
	// result is longer than the limit, and what survives is mostly marker rather
	// than data. Two of the built-in defaults (the numeric ci_job_id /
	// ci_pipeline_id, at 32) sit below this floor on purpose — they bound values
	// that are ~7 bytes in practice and never truncate — but an operator has no
	// such context, so overrides are held to the floor.
	minLabelBytes = maxMarkerLen
	// maxLabelBytesCeiling is the ceiling for an operator-supplied limit, and it
	// guards the failure mode that is strictly worse than truncation. A bounded
	// value reaches limit+maxMarkerLen, and this exporter is deployed behind a
	// downstream label_value_length_limit of maxEnvironBytes — the environ
	// ceiling that README and deploy/k8s/servicemonitor.yaml both fix as the
	// global one. Above this ceiling a single long cmdline produces a label value
	// Prometheus rejects, and it rejects the WHOLE scrape rather than the one
	// value: every metric from the host disappears. An operator raising a limit
	// is trying to lose less data, so silently trading truncated cmdlines for
	// total data loss is the opposite of what they asked for, and it fails the
	// load instead.
	maxLabelBytesCeiling = maxEnvironBytes - maxMarkerLen
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
// was configuring. An over-ceiling limit is dropped because its failure mode is
// worse still — see maxLabelBytesCeiling, it costs the whole scrape. An unknown
// name is dropped for the same reason it cannot be applied: it would widen the
// table past the published contract.
//
// Dropping is the fail-safe here, not the intended path: LoadConfig rejects all
// three cases outright, so a config that reached this function with one is a
// hand-built *Config, not an operator's file.
func mergedMaxLabelBytes(overrides map[string]int) map[string]int {
	out := make(map[string]int, len(MaxLabelBytes))
	for label, limit := range MaxLabelBytes {
		out[label] = limit
	}
	for label, limit := range overrides {
		if _, ok := out[label]; !ok || limit < minLabelBytes || limit > maxLabelBytesCeiling {
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
//	<prefix>…[len=<N>;sha256=<first 12 hex of the SALTED digest of original>]
//
// The fingerprint is what keeps distinct over-long values distinct: two command
// lines identical up to and past the cut still land on separate series, where a
// bare "[TRUNCATED]" collapses both into one. The trade-off is cardinality —
// distinct over-long values stay distinct instead of collapsing into one series.
//
// The digest is SALTED (see fingerprintSalt), and that is load-bearing rather
// than incidental. "cmdline" is in this table and is the one label here that
// receives NO secret redaction anywhere: it is raw argv, and argv routinely
// carries credentials (--token=, -p<pass>, an inline --config=<json>). An
// UNSALTED sha256 of the original would publish, on an endpoint every scraper
// can read, a verifiable commitment to the bytes PAST the cut — exactly the
// bytes the limit hides. That is the unrate-limited offline confirmation oracle
// environTruncMarker refuses to publish, and it is worse here: the prefix is
// handed over too, so only the tail has to be guessed.
//
// Truncation is deterministic within one exporter process: identical input
// always yields an identical label value, or the series would churn on every
// scrape. It is deliberately NOT deterministic across processes — a restart
// re-rolls the salt, so a truncated value's series churns once per restart and
// the same value renders differently on two hosts. That is the price of closing
// the oracle, and only values long enough to be cut pay it.
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
// included — carrying only the original length.
//
// environ is the one label composed of arbitrary, operator-unknown key/value
// pairs, and being over-long is itself a secret signal there: isSensitivePair
// only recognises token-shaped values (isTokenCharset rejects anything with
// braces, quotes, colons or newlines), so a JSON service-account blob, a PEM
// body or a connection string falls through unless its KEY hits the denylist.
// Emitting a prefix of those would publish credential material to every
// scraper, which is why an over-long environ value gets no prefix at all.
//
// It carries no fingerprint either, and that is the same decision rather than a
// separate one. The values reaching here are exactly the ones the heuristics
// could NOT classify, so they must be assumed to be credential material, and
// /metrics is readable by every scraper. Refusing the body but publishing
// anything derived from it gives back part of what dropping the body was
// protecting.
//
// Note this is NOT the offline-oracle argument, which truncateWithFingerprint
// closes for every label by salting the digest. A salted digest is still an
// EQUALITY oracle within one exporter process, and on an assumed-secret value
// that alone leaks: a scraper can see that two processes hold the same
// credential, or watch the digest change and learn exactly when one rotated.
// Length alone supports neither.
//
// Cost of dropping it: two distinct over-long values of the same length now
// render identically, so environ loses the distinguishability that
// truncateWithFingerprint preserves. That is a real loss for debugging and a
// cardinality WIN, and it is the right trade only here — the labels in the
// MaxLabelBytes table are described rather than hidden, so keeping both prefix
// and (salted) fingerprint costs them nothing they were not already publishing.
//
// The result is always shorter than maxEnvironValueLen for any value that
// reaches it, so it can only shrink the joined label.
func environTruncMarker(s string) string {
	return "[TRUNCATED;len=" + strconv.Itoa(len(s)) + "]"
}

// fingerprintSalt is rolled once per exporter process and prefixed into every
// digest truncateWithFingerprint publishes. It is never exposed: not in a
// label, not in a metric, not in a log line. The moment it is, the offline
// confirmation oracle it exists to close is back.
//
// It is a process-lifetime value on purpose. A persisted or derived salt (from
// the hostname, the boot id, a config field) would be recoverable by the same
// attacker who reads /metrics, which makes it decoration rather than a salt.
var fingerprintSalt = mustFingerprintSalt()

// mustFingerprintSalt draws the salt, failing the process if it cannot. This
// fails CLOSED deliberately: the only alternative to a real salt is a
// predictable one, and a predictable salt silently restores the very oracle the
// salt removes — a truncated cmdline would again carry a confirmable commitment
// to the credential past the cut, with nothing in the exposition to say so.
func mustFingerprintSalt() []byte {
	salt := make([]byte, sha256.Size)
	if _, err := rand.Read(salt); err != nil {
		panic("exporter: cannot draw the label fingerprint salt: " + err.Error())
	}
	return salt
}

// fingerprint returns the leading hex chars of the salted digest of s. The
// salt is hashed FIRST so the digest commits to the salt as well as the value;
// appending it would leave the construction open to a length-extension shortcut
// on the value.
func fingerprint(s string) string {
	h := sha256.New()
	h.Write(fingerprintSalt)
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))[:truncFingerprintLen]
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
