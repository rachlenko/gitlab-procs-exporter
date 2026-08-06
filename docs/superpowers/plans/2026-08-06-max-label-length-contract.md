# Explicit MAX_LABEL_LENGTH contract + informative truncation — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the currently implicit, partial size limits on emitted label values into one explicit, documented, tested contract that covers **every** label the exporter emits, and replace the information-destroying `[TRUNCATED]` marker with an informative one that preserves a traceable fingerprint of the original value.

**Why now:** `cmdline` and `environ` are bounded (`maxCmdlineBytes`, `maxEnvironBytes`). `name` and the four `ci_*` labels are **not bounded at all** — they pass through `sanitizeLabelValue()` only, which fixes UTF-8 validity but not length. Those five labels ride on **all 12 metrics**, so an oversized value there multiplies 12× into the TSDB index, whereas `cmdline`/`environ` appear on `gitlab_process_info` alone.

**Architecture:** A new `exporter/labelbound.go` owns the contract: a per-label limit table, a single `boundLabel()` truncator, and a `truncationsTotal` counter for self-observability. `collector.go` routes every label value through it. `boundCmdline` is absorbed into `boundLabel` and deleted. Limits are overridable via the existing `--config` YAML loader (`exporter/config.go`).

**Tech Stack:** Go 1.24, `github.com/prometheus/client_golang`, stdlib `crypto/sha256`, `unicode/utf8`, `strings`. No new modules.

---

## Measurement that motivates the limits

Taken from a live `/metrics` scrape (8,844 `gitlab_process_*` series, 1.6 MB) audited with
`prom_label_audit.py`. Numbers are **bytes per label value**.

| label | series | p50 | p90 | p99 | max | % of all label bytes | bounded today? |
|---|---|---|---|---|---|---|---|
| `environ` | 737 | 0 | 1463 | 2265 | 3763 | 33.8% | yes — 8192 |
| `name` | 8,844 | 13 | 25 | 29 | 39 | 15.2% | **no** |
| `cmdline` | 737 | 0 | 442 | 962 | **2059** | 8.4% | yes — 2048 (**cap is being hit**) |
| `pid` | 8,844 | 4 | 7 | 7 | 7 | 4.9% | n/a (numeric) |
| `ci_job_id` | 8,844 | 0 | 0 | 0 | 0 | 0% | **no** |
| `ci_job_name` | 8,844 | 0 | 0 | 0 | 0 | 0% | **no** |
| `ci_project_path` | 8,844 | 0 | 0 | 0 | 0 | 0% | **no** |
| `ci_pipeline_id` | 8,844 | 0 | 0 | 0 | 0 | 0% | **no** |

`cmdline` max is exactly `2048 + len("[TRUNCATED]")` — truncation already fires in
production-like conditions, and today it fires **silently**.

The `ci_*` zeros are an artefact of the sample host (a workstation, not a GitLab
runner). **Re-run the audit against a runner before freezing the `ci_*` limits** —
see "Validating the limits" below.

### The unit of the contract is the label value, not the whitespace token

A token-level audit of the same file (`token_length_audit.py`) reports `max_len = 489`,
because `cmdline` and `environ` contain spaces and get shredded into words. The real
maxima are 3,763 B (label value) and 4,139 B (series line) — the token audit
**understates the true maximum by 7.7×–8.5×** and cannot attribute a length to a label
name at all. Prometheus charges per label value and per series, never per word.
The contract is therefore defined in **bytes of a single label value**.

---

## Global Constraints

- Go version floor: `go 1.24.0`. No new modules.
- One `*_test.go` file per implementation file (`exporter/labelbound.go` → `exporter/labelbound_test.go`).
- Every limit is a **byte** count, never a rune count — Prometheus and the exposition format are byte-oriented.
- Truncation must always cut on a **rune boundary** and return valid UTF-8. `MustNewConstMetric` panics on invalid UTF-8 on the registry's gather goroutine, which crashes the whole exporter (see `CLAUDE.md`).
- Truncation must be **deterministic**: identical input always yields an identical label value, or the series churns on every scrape.
- Ordering is fixed and must not change: `sanitizeLabelValue()` first (make it valid UTF-8), then redact, then `boundLabel()`. Bounding invalid UTF-8 makes the rune walk-back meaningless.
- `environ_truncated` keeps its existing, narrow meaning: **the variable list is incomplete**. It is NOT set by per-value truncation. Do not overload it.
- Existing exported behaviour that must not regress: `IsSecretKey`, `IsSecretValue`, `isSensitivePair`, `RedactEnviron`, and the `[REDACTED]` marker.

---

## The contract

```go
// MaxLabelBytes is the published contract for every label value this exporter
// emits: no label value ever exceeds its limit, and anything longer is
// truncated informatively rather than dropped.
var MaxLabelBytes = map[string]int{
    "name":            128,  // observed max 39; headroom for long kthread names
    "cmdline":         2048, // unchanged; ARG_MAX can reach 2 MB
    "ci_job_name":     256,  // parallel:matrix jobs embed matrix values
    "ci_project_path": 256,  // group/subgroup/.../project nesting
    "ci_job_id":       32,   // numeric
    "ci_pipeline_id":  32,   // numeric
}
```

`environ` is deliberately **not** in this table: it is a composed blob with its own
three-way bound (`maxEnvironVars` / `maxEnvironValueLen` / `maxEnvironBytes`) already
covered by tests. Task 4 only swaps its per-value marker.

`pid` and `environ_truncated` are exporter-generated and structurally bounded — no entry.

### Informative truncation marker

Replace `[TRUNCATED]` with a marker carrying the original length and a fingerprint:

```
<prefix cut at a rune boundary>…[len=<N>;sha256=<first 12 hex of sha256(original)>]
```

The fingerprint is what makes truncation reversible in practice: given a suspect
series you can hash candidate values and confirm the match, which a bare
`[TRUNCATED]` makes impossible.

> **Cardinality trade-off — decide this explicitly before implementing.**
> `[TRUNCATED]` *collapses* every over-long value to one string, which reduces
> series count. The fingerprint marker *preserves* distinctness, so cardinality
> stays at the pre-truncation level. This plan optimises for **size and
> traceability**, which is the stated problem. If a future incident is about
> cardinality rather than bytes, the collapsing behaviour must come back as an
> option — keep `boundLabel` easy to switch.

---

### Task 1: The bounding primitive

**Files:** Create `exporter/labelbound.go`; Test `exporter/labelbound_test.go`

**Interfaces produced** (consumed by Tasks 2–4):
- `func boundLabel(name, value string) string` — applies `MaxLabelBytes[name]`; unknown name ⇒ unchanged.
- `func truncateWithFingerprint(s string, max int) string` — the primitive.
- `var MaxLabelBytes map[string]int`

- [x] **Step 1: Write the failing tests** — `exporter/labelbound_test.go`:
  - value shorter than the limit passes through **byte-identical**
  - value of **exactly** the limit passes through unchanged (guard is `>`, not `>=`)
  - value of limit+1 is truncated, and the result is **valid UTF-8**
  - 3-byte runes (`strings.Repeat("世", …)`) against a limit that is **not** a multiple of 3, so the raw cut lands mid-rune and the walk-back must fire — mirrors the existing `TestBoundCmdline` reasoning
  - marker contains the **original** length, not the truncated length
  - identical input ⇒ identical output (determinism), twice in a row
  - two values sharing a long common prefix but differing past the cut produce **different** markers
  - unknown label name ⇒ unchanged, even when very long
  - empty string ⇒ empty string, no marker
  - **the bounded result including the marker never exceeds `max + maxMarkerLen`** — assert a hard ceiling; a marker appended past the limit is the classic way a "bounded" value stops being bounded
- [x] **Step 2: Run tests — MUST FAIL** (`go test ./exporter/ -run TestBoundLabel`)
- [x] **Step 3: Implement** `labelbound.go`. Hash the **original** string before cutting. Walk back with `utf8.RuneStart` exactly as `boundCmdline` does.
- [x] **Step 4: Run tests — MUST PASS**
- [x] **Step 5:** `make fmt && make lint` (gofmt clean + `go vet` clean; `goimports` and `golangci-lint` binaries are not installed in this environment, so `make fmt`/`make lint` abort on the missing tool)

---

### Task 2: Bound `name` and the `ci_*` labels

**Files:** Modify `exporter/collector.go`; Test `exporter/collector_test.go`

This is the highest-value task: these five labels are on all 12 metrics and are
currently unbounded.

- [x] **Step 1: Write the failing tests** — a process whose `Name` is 4 KB and whose environ
  carries a 4 KB `CI_JOB_NAME` and `CI_PROJECT_PATH`. Collect, then assert **for every
  one of the 12 metrics** that each label value is within its limit. Assert the
  `ci_*` values are bounded in `ciJobLabelValues` output too, not only on `gitlab_process_info`.
- [x] **Step 2: Run tests — MUST FAIL** (they are unbounded today)
- [x] **Step 3: Implement** — in `ciJobLabelValues`, wrap: `boundLabel(k.label, sanitizeLabelValue(environ[k.env]))`. In `Collect`, wrap `name`: `boundLabel("name", sanitizeLabelValue(p.Name))`. Order matters — sanitize first.
- [x] **Step 4: Run tests — MUST PASS**
- [x] **Step 5:** `make fmt && make lint` (gofmt clean + `go vet` clean; `goimports` and `golangci-lint` binaries are not installed in this environment, so `make fmt`/`make lint` abort on the missing tool)

---

### Task 3: Absorb `boundCmdline` into `boundLabel`

**Files:** Modify `exporter/collector.go`, `exporter/collector_test.go`

- [x] **Step 1:** Update `TestBoundCmdline` to call `boundLabel("cmdline", …)`. Keep every existing
  assertion — especially the 3-byte-rune walk-back case — so coverage does not regress.
  Add one new assertion: the marker now reports the original length.
  Added `TestCollectCmdlineUsesSharedContract`: the `Collect` call site must emit the shared
  contract's value, which is what actually pins the call site to `boundLabel`.
- [x] **Step 2: Run tests — MUST FAIL** (marker format changed — `Collect` emitted `…[TRUNCATED]`
  against the expected `…[len=4096;sha256=3f70d00f41ba]`)
- [x] **Step 3:** Delete `boundCmdline` and `maxCmdlineBytes`; move the limit into `MaxLabelBytes`. Update the `Collect` call site.
- [x] **Step 4: Run tests — MUST PASS** (`go test -race -cover ./exporter/` → 91.6% of statements)
- [x] **Step 5:** `make fmt && make lint` (gofmt clean + `go vet` clean; `goimports` and `golangci-lint` binaries are not installed in this environment, so `make fmt`/`make lint` abort on the missing tool)

---

### Task 4: Informative marker inside `environ` per-value truncation

**Files:** Modify `exporter/collector.go`, `exporter/collector_test.go`

- [x] **Step 1:** Update `TestScrubEnvironBounds` / `TestScrubEnvironAtLimits` / `TestScrubEnvironUTF8Safe`:
  an over-long value is replaced by the fingerprint marker, **`[REDACTED]` still wins over
  truncation for sensitive pairs** (order of the `switch` must not change), and the
  `maxEnvironBytes` ceiling still holds *with the longer marker in play*.
  `TestScrubEnvironUTF8Safe` now pins the rune walk-back directly: the value is byte-cut
  rather than replaced whole, so the surviving prefix must be a whole number of 3-byte runes.
  `TestScrubEnvironAtLimits` gained a limit+1 case asserting the pair stays within
  `maxEnvironValueLen + maxMarkerLen`.
- [x] **Step 2: Run tests — MUST FAIL** (marker format changed — `scrubEnviron` emitted
  `BIG=[TRUNCATED]` against the expected `BIG=xxx…[len=257;sha256=15eb95a462ee]`)
- [x] **Step 3:** In `scrubEnviron`, replace `val = environValueTruncMarker` with
  `val = truncateWithFingerprint(val, maxEnvironValueLen)`. Verify `environ_truncated`
  semantics are untouched. The length arm became the `switch`'s `default` (the guard now
  lives inside `truncateWithFingerprint`) and the now-unused `environValueTruncMarker`
  const was deleted.
- [x] **Step 4: Run tests — MUST PASS** (`go test -race -cover ./...` → `exporter/` 91.6% of statements)
- [x] **Step 5:** `make fmt && make lint` (gofmt clean + `go vet` clean; `goimports` and `golangci-lint` binaries are not installed in this environment, so `make fmt`/`make lint` abort on the missing tool)

> **Watch the ceiling interaction:** the fingerprint marker is ~35 B versus 11 B for
> `[TRUNCATED]`. With many truncated values the joined label reaches `maxEnvironBytes`
> **sooner**, so more variables get dropped and `environ_truncated` flips to 1 more
> often. That is correct behaviour, but it is a visible change — call it out in the
> changelog.

---

### Task 5: Make truncation observable

**Files:** Modify `exporter/collector.go`; Test `exporter/collector_test.go`

Truncation is invisible today — `cmdline` is being cut in production and nothing
reports it. An explicit contract needs an explicit signal.

- [x] **Step 1: Write the failing test** — after collecting a process with an over-long
  `name`, `gitlab_exporter_label_truncations_total{label="name"}` is ≥ 1.
  `TestCollectCountsLabelTruncations` reads the counter out of `reg.Gather()`, never off the
  collector field, so a counter that is incremented but not emitted still fails. It also pins
  that an untruncated label (`cmdline`) is present at 0 and that every `MaxLabelBytes` label
  has a series. `TestLabelTruncationsAccumulateAcrossScrapes` pins counter (not gauge)
  semantics across two gathers.
- [x] **Step 2: Run tests — MUST FAIL** (`gitlab_exporter_label_truncations_total was not gathered`)
- [x] **Step 3:** Add a `*prometheus.CounterVec` on `ProcessCollector`, incremented from
  `boundLabel`. **It must be registered on the same registry and emitted from
  `Describe`/`Collect`** — a `CounterVec` created but never registered silently reports nothing.
  `boundLabel`/`ciJobLabelValues` gained a variadic `truncationObserver`, so the primitive stays
  a pure function usable without a registry while the collector passes itself. Children are
  pre-initialized at 0 for every contract label, and `Collect` emits the vec last so the
  current scrape's own cuts are already counted.
- [x] **Step 4: Run tests — MUST PASS** (`go test -race -cover ./exporter/` → 91.6% of statements)
- [x] **Step 5:** `make fmt && make lint` (gofmt clean + `go vet` clean; `goimports` and
  `golangci-lint` binaries are not installed in this environment, so `make fmt`/`make lint`
  abort on the missing tool)

---

### Task 6: Operator overrides via the existing config

**Files:** Modify `exporter/config.go`, `exporter/config_test.go`, `config.example.yaml`

- [x] **Step 1: Write the failing tests** — YAML `max_label_bytes: {ci_job_name: 512}` overrides
  one entry and leaves the rest at defaults; an unknown label name is a **fail-fast
  error** (silently ignoring a typo'd override is how a limit quietly never applies);
  a value `<= 0` is rejected; a value below the marker length is rejected.
  `TestLoadConfigMaxLabelBytesRejected` also pins that the error message names the offending
  entry, that `environ` is rejected like any other unknown name (it is bounded elsewhere, not
  here), and that a non-integer value fails the parse. `TestLoadConfigMaxLabelBytesAcceptsTheFloor`
  pins the boundary from the other side. In `collector_test.go`,
  `TestNewProcessCollectorWithConfigAppliesLabelLimits` gathers off a registry and asserts a
  *lowered* and a *raised* limit both take effect on all 12 metrics — a one-sided test would pass
  against a collector that ignored the config outright.
- [x] **Step 2: Run tests — MUST FAIL** (build failure: `unknown field MaxLabelBytes`,
  `undefined: NewProcessCollectorWithConfig`, `undefined: mergedMaxLabelBytes`)
- [x] **Step 3:** Add `MaxLabelBytes map[string]int` to `Config` with validation; apply it in
  `NewProcessCollector`. Keep the existing variadic-param compatibility approach so
  current call sites compile unchanged. Done via a sibling constructor
  `NewProcessCollectorWithConfig(store, *Config)` — Go has no second variadic, and this leaves
  every existing `NewProcessCollector(store, subs...)` call site untouched. The collector gained
  a per-instance `maxLabelBytes` table (`mergedMaxLabelBytes` copies the package contract rather
  than mutating shared state), and `boundLabel`/`ciJobLabelValues` grew `…With(limits, …)`
  variants so the pure default-table functions still exist for tests.
  **Floor asymmetry, deliberate:** overrides must be `>= minLabelBytes` (= `maxMarkerLen`, 49),
  while the built-in `ci_job_id`/`ci_pipeline_id` defaults are 32. Those bound ~7-byte numeric
  values and never truncate; an operator has no such context, so overrides are held to the floor.
  Documented in the `minLabelBytes` comment.
- [x] **Step 4: Run tests — MUST PASS** (`go test -race -cover ./...` → `exporter/` 92.1% of statements)
- [x] **Step 5:** `make fmt && make lint` (`gofmt -s` clean + `go vet ./...` clean; `goimports` and
  `golangci-lint` binaries are not installed in this environment, so `make fmt`/`make lint` abort
  on the missing tool)

---

### Task 7: Document the contract

**Files:** Modify `README.md`, `CLAUDE.md`, `config.example.yaml`

- [ ] Document each label, its limit, and the marker format as part of the **input-data contract**.
- [ ] Carry over Task 4's changelog callout — there is no `CHANGELOG` file in this repo, so the
      `environ_truncated`-flips-sooner behaviour change belongs in the `README.md` notes here.
- [ ] Add to `CLAUDE.md`, next to the existing `sanitizeLabelValue` rule: *every label value
      MUST pass `sanitizeLabelValue()` then `boundLabel()` before reaching
      `MustNewConstMetric`.* The existing rule covers UTF-8 validity but not length.
- [ ] Recommend defence in depth in the scrape config — the exporter's cap is self-imposed
      and a future label could miss the table:
      ```yaml
      scrape_configs:
        - job_name: gitlab-procs
          label_value_length_limit: 8192
          label_limit: 32
      ```
      Note the existing comment on `maxEnvironBytes`: an operator setting
      `label_value_length_limit` below 8192 must lower `maxEnvironBytes` too, or
      Prometheus rejects the whole scrape.

---

### Task 8: Full verification

- [ ] `make fmt && make lint && make test`
- [ ] Confirm coverage ≥ 80% on `exporter/`
- [ ] Run the exporter and re-audit its `/metrics`; assert **no** label value exceeds its
      limit and that `p99`/`max` for `name` and `ci_*` are now inside the table:
      ```
      python3 prom_label_audit.py expo metrics.txt --out series.ndjson
      python3 prom_label_audit.py audit series.ndjson --cap 256
      ```
- [ ] Diff series count before/after to confirm the cardinality trade-off documented above
      matches reality.

---

## Validating the limits against production

The `ci_*` limits in this plan are **estimates** — the audited host had no CI jobs, so
every `ci_*` value was empty. Before freezing them, pull real label sets from a runner
and re-run the audit:

```
python3 prom_label_audit.py fetch \
    --url https://prometheus.cluster-main.proxmox.ds-in.net/ \
    --match '{__name__=~"gitlab_process_.*"}' \
    --hours 168 --end <unix-ts> \
    --out prod_series.ndjson --tsdb-out prod_tsdb.json \
    --netrc-file ~/.prom-netrc
python3 prom_label_audit.py audit prod_series.ndjson --cap 256
```

A 7-day window matters: job names and project paths are long-tailed, and a 24-hour
sample will miss the outliers the contract exists to catch.

Cross-check against `prod_tsdb.json` → `memoryInBytesByLabelName`, which is
Prometheus's own accounting of which label costs the most index memory. If the audit
and the TSDB disagree on the ranking, trust the TSDB and re-derive the limits.
