package exporter

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// fingerprintOf mirrors what the marker must carry so the tests assert the
// documented format rather than whatever the implementation happens to emit.
func fingerprintOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:truncFingerprintLen]
}

func TestBoundLabelPassesThroughUnderLimit(t *testing.T) {
	// Arrange
	limit := MaxLabelBytes["name"]
	value := strings.Repeat("a", limit-1)

	// Act
	got := boundLabelWith(MaxLabelBytes, "name", value, nil)

	// Assert
	if got != value {
		t.Errorf("value under the limit must pass through byte-identical, got %d bytes want %d", len(got), len(value))
	}
}

func TestBoundLabelPassesThroughAtExactLimit(t *testing.T) {
	// The guard must be `>`, not `>=`: a value of exactly the limit is legal
	// and truncating it would corrupt values that are within contract.
	limit := MaxLabelBytes["name"]
	value := strings.Repeat("a", limit)

	got := boundLabelWith(MaxLabelBytes, "name", value, nil)

	if got != value {
		t.Errorf("value of exactly %d bytes must pass through unchanged, got %q", limit, got)
	}
}

func TestBoundLabelTruncatesOverLimit(t *testing.T) {
	limit := MaxLabelBytes["name"]
	value := strings.Repeat("a", limit+1)

	got := boundLabelWith(MaxLabelBytes, "name", value, nil)

	if got == value {
		t.Fatal("value of limit+1 bytes must be truncated")
	}
	if !utf8.ValidString(got) {
		t.Errorf("truncated value is not valid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, markerFor(value)) {
		t.Errorf("expected marker suffix %q, got %q", markerFor(value), got)
	}
}

func TestBoundLabelUnknownNameUnchanged(t *testing.T) {
	// A label with no entry in the contract table is not this function's
	// business — silently bounding it would hide a missing table entry.
	value := strings.Repeat("z", 64*1024)

	got := boundLabelWith(MaxLabelBytes, "environ", value, nil)

	if got != value {
		t.Errorf("unknown label name must pass through unchanged, got %d bytes want %d", len(got), len(value))
	}
}

func TestBoundLabelEmptyString(t *testing.T) {
	if got := boundLabelWith(MaxLabelBytes, "name", "", nil); got != "" {
		t.Errorf("empty value must stay empty with no marker, got %q", got)
	}
	if got := truncateWithFingerprint("", 128); got != "" {
		t.Errorf("empty value must stay empty with no marker, got %q", got)
	}
}

func TestBoundLabelWalksBackOffPartialRune(t *testing.T) {
	// 3-byte runes against a limit that is not a multiple of 3, so the raw cut
	// lands MID-rune and the walk-back must fire. Without it the result is
	// invalid UTF-8 and MustNewConstMetric panics on the gather goroutine,
	// taking the whole exporter down. Mirrors the TestBoundCmdline reasoning.
	const max = 128
	if max%3 == 0 {
		t.Fatalf("test assumes max (%d) is not a multiple of 3", max)
	}
	value := strings.Repeat("世", max) // 3 bytes each, well over the limit

	got := truncateWithFingerprint(value, max)

	if !utf8.ValidString(got) {
		t.Errorf("truncated 3-byte-rune value is not valid UTF-8 (walk-back failed): %q", got)
	}
	body := strings.TrimSuffix(got, markerFor(value))
	if body == got {
		t.Fatalf("expected marker suffix %q, got %q", markerFor(value), got)
	}
	// The kept prefix must be strictly shorter than the naive cut, proving the
	// walk-back trimmed the partial rune rather than leaving it.
	if len(body) >= max {
		t.Errorf("walk-back did not trim partial rune: prefix is %d bytes, expected < %d", len(body), max)
	}
}

func TestBoundLabelMarkerCarriesOriginalLength(t *testing.T) {
	// The whole point of the informative marker: it reports the length of the
	// ORIGINAL value, not of the truncated one.
	const max = 64
	value := strings.Repeat("q", 5000)

	got := truncateWithFingerprint(value, max)

	if !strings.Contains(got, fmt.Sprintf("len=%d", len(value))) {
		t.Errorf("marker must report the original length %d, got %q", len(value), got)
	}
	if strings.Contains(got, fmt.Sprintf("len=%d", max)) {
		t.Errorf("marker must not report the truncated length, got %q", got)
	}
	if !strings.Contains(got, "sha256="+fingerprintOf(value)) {
		t.Errorf("marker must carry sha256(original) prefix %q, got %q", fingerprintOf(value), got)
	}
}

func TestBoundLabelIsDeterministic(t *testing.T) {
	// Identical input must yield an identical label value on every scrape, or
	// the series churns and the TSDB grows a new series per scrape.
	value := strings.Repeat("d", 4096)

	first := boundLabelWith(MaxLabelBytes, "name", value, nil)
	second := boundLabelWith(MaxLabelBytes, "name", value, nil)

	if first != second {
		t.Errorf("truncation must be deterministic: %q != %q", first, second)
	}
}

func TestBoundLabelDistinguishesSharedPrefixes(t *testing.T) {
	// Two values identical up to (and past) the cut must still produce
	// different labels — that is exactly what the fingerprint buys over a bare
	// [TRUNCATED], which collapses both into one indistinguishable series.
	prefix := strings.Repeat("p", 4096)
	a := prefix + "alpha"
	b := prefix + "beta"

	gotA := boundLabelWith(MaxLabelBytes, "name", a, nil)
	gotB := boundLabelWith(MaxLabelBytes, "name", b, nil)

	if gotA == gotB {
		t.Errorf("values differing past the cut must produce different markers, both gave %q", gotA)
	}
}

func TestBoundLabelNeverExceedsCeiling(t *testing.T) {
	// A marker appended past the limit is the classic way a "bounded" value
	// stops being bounded. Assert the hard ceiling for every label in the
	// contract, across 1-, 2- and 3-byte runes.
	runes := []string{"a", "ы", "世"}
	for label, max := range MaxLabelBytes {
		for _, r := range runes {
			value := strings.Repeat(r, max+16)

			got := boundLabelWith(MaxLabelBytes, label, value, nil)

			if ceiling := max + maxMarkerLen; len(got) > ceiling {
				t.Errorf("bounded %q is %d bytes, ceiling %d", label, len(got), ceiling)
			}
			if !utf8.ValidString(got) {
				t.Errorf("bounded %q with %q runes is not valid UTF-8: %q", label, r, got)
			}
		}
	}
}

func TestBoundLabelContractCoversEveryBoundedLabel(t *testing.T) {
	// Derived from the collectors' OWN label sets, not a hand-copied list: the
	// failure this guards is "someone adds a label and forgets the table entry",
	// and a hardcoded want[] only catches that if they also remember to edit the
	// test — the same act of remembering. Adding a label to infoLabelNames or
	// kubeLabelNames now fails here until it is either bounded or exempted below,
	// on purpose.
	//
	// Both registered collectors are covered: KubeCollector shares the registry
	// (and so the gather goroutine) with ProcessCollector, and it is the one that
	// already shipped an unbounded label once.
	exempt := map[string]string{
		"pid":               "exporter-generated decimal PID, structurally bounded",
		"environ":           "composed blob with its own maxEnvironVars/ValueLen/Bytes bound",
		"environ_truncated": `exporter-generated, always "0" or "1"`,
		"job_name": "same CI_JOB_NAME source as ci_job_name, so KubeCollector bounds it " +
			"under that entry rather than owning one — see kube_collector.go",
	}
	emittedLabels := append(infoLabelNames(), kubeLabelNames()...)
	for _, label := range emittedLabels {
		max, ok := MaxLabelBytes[label]
		if !ok {
			if _, ok := exempt[label]; ok {
				continue
			}
			t.Errorf("label %q is emitted but missing from the MaxLabelBytes contract, so it "+
				"passes through unbounded; add a limit or an explicit exemption", label)
			continue
		}
		if max <= 0 {
			t.Errorf("limit for %q is %d; a non-positive limit disables the bound", label, max)
		}
	}
	// An exemption that no longer names a real label is stale and would hide a
	// genuinely unbounded label if that name were reused.
	emitted := make(map[string]bool, len(emittedLabels))
	for _, label := range emittedLabels {
		emitted[label] = true
	}
	for label := range exempt {
		if !emitted[label] {
			t.Errorf("exemption for %q is stale: no such label is emitted", label)
		}
	}
}

// markerFor builds the exact marker the implementation must append for s.
func markerFor(s string) string {
	return fmt.Sprintf("%s[len=%d;sha256=%s]", truncEllipsis, len(s), fingerprintOf(s))
}
