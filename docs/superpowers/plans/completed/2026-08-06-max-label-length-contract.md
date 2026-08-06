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

- [x] Document each label, its limit, and the marker format as part of the **input-data contract**.
      New `README.md` "Label size contract" section: a per-label limit table (incl. which metrics
      each rides on), the marker format with an exposition example, the determinism and
      `limit + 49` ceiling guarantees, the cardinality trade-off, and the
      `gitlab_exporter_label_truncations_total` counter with a PromQL example. The stale
      `[TRUNCATED]` descriptions of `cmdline` and per-value `environ` were corrected, the
      per-process metrics intro now names the four `ci_*` labels it had omitted entirely, and
      `max_label_bytes` is documented under "Configuration file" (fail-fast rules and the
      49-byte floor, incl. why the built-in numeric defaults of 32 sit below it).
      One correction worth flagging: `environ`'s per-value cuts go through
      `truncateWithFingerprint` directly, not `boundLabel`, so they do **not** increment the
      counter — the README says so rather than implying the counter covers every cut.
- [x] Carry over Task 4's changelog callout — there is no `CHANGELOG` file in this repo, so the
      `environ_truncated`-flips-sooner behaviour change belongs in the `README.md` notes here.
      Added as a blockquote under "Hardened environ scrubbing", noting it is visible in existing
      dashboards and alerts on that label.
- [x] Add to `CLAUDE.md`, next to the existing `sanitizeLabelValue` rule: *every label value
      MUST pass `sanitizeLabelValue()` then `boundLabel()` before reaching
      `MustNewConstMetric`.* The existing rule covers UTF-8 validity but not length.
      Also pins the fixed ordering, that a new label needs a `MaxLabelBytes` entry or it silently
      passes through unbounded, that limits are byte counts, and the `environ` exception.
- [x] Recommend defence in depth in the scrape config — the exporter's cap is self-imposed
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
      Placed in the contract section and cross-referenced from "2. Prometheus Configuration".
      The caveat spells out that `maxEnvironBytes` is a build-time constant and is *not*
      reachable via `max_label_bytes`, so going below 8192 requires a rebuild.
      `config.example.yaml` already carried the full `max_label_bytes` block from Task 6; it
      gained a pointer to the README section so the two do not drift into separate contracts.

---

### Task 8: Full verification

- [x] `make fmt && make lint && make test` — `gofmt -s -l` reports no files and `go vet ./...`
      is clean; `go clean -testcache && go test -race -coverprofile=… ./...` passes on every
      package. `make fmt`/`make lint` themselves still abort in this environment on the missing
      `goimports`/`golangci-lint` binaries, so the underlying checks were run directly.
- [x] Confirm coverage ≥ 80% on `exporter/` — **92.1% of statements** (`-race`, whole-module run).
      Other packages for the record: root 47.3%, `cmd/jobreport-web` 69.6%, `internal/jobreport`
      56.9%, `deploy` 29.3% — all untouched by this plan.
- [x] Run the exporter and re-audit its `/metrics`; assert **no** label value exceeds its
      limit and that `p99`/`max` for `name` and `ci_*` are now inside the table.
      `prom_label_audit.py` is not vendored in this repo, so the audit was reproduced with an
      equivalent throwaway parser (unescapes each label value, measures **bytes**, compares
      against `MaxLabelBytes` + `maxMarkerLen`) and deleted afterwards. Live scrape of 8,892
      `gitlab_process_*` series on this host:

      | label | count | p50 | p90 | p99 | max | limit | within |
      |---|---|---|---|---|---|---|---|
      | `name` | 8,892 | 13 | 24 | 33 | 39 | 128 | yes |
      | `cmdline` | 741 | 0 | 441 | 1033 | 2081 | 2048 (+49 marker) | yes |
      | `environ` | 741 | 0 | 1463 | 2265 | 3763 | 8192 | yes |
      | `ci_job_id` / `ci_job_name` / `ci_pipeline_id` / `ci_project_path` | 8,892 | 0 | 0 | 0 | 0 | 32/256/32/256 | yes |

      Result: **PASS — no label value exceeds its contract limit.** The `cmdline` max of 2081 is
      a truncated value (2047-byte rune-aligned prefix + 34-byte marker), i.e. inside the stated
      `limit + maxMarkerLen` ceiling rather than over the limit. Truncation is now visible in the
      exposition: 39 fingerprint markers such as `…[len=3105;sha256=8b4b84f66618]`, and
      `gitlab_exporter_label_truncations_total{label="cmdline"}` reached 3 while every other
      contract label sat at 0.
      **Caveat carried forward:** all four `ci_*` values are empty on this host (a workstation,
      not a GitLab runner), exactly as the plan's measurement table predicted — so this run
      confirms the `ci_*` labels are *routed through* the contract but does **not** validate the
      256/32-byte limits against real data. See "Validating the limits against production" below;
      that step still requires a runner and is not automatable here.
- [x] Diff series count before/after to confirm the cardinality trade-off documented above
      matches reality. The pre-change binary was built from the merge-base (`a9725d9`, `main`)
      in a temporary worktree and scraped alongside this branch's binary:

      | | before | after | delta |
      |---|---|---|---|
      | `gitlab_process_*` series | 8,892 | 8,892 | **0** |
      | `gitlab_process_info` series | 741 | 741 | **0** |
      | exposition bytes | 1,657,168 | 1,667,788 | +10,620 (+0.64%) |
      | `[TRUNCATED]` markers | 39 | 0 | −39 |
      | fingerprint markers | 0 | 39 | +39 |
      | `environ_truncated="1"` | 1 | 1 | 0 |

      **Cardinality cost measured at zero on this host.** The documented trade-off — fingerprints
      preserve distinctness where `[TRUNCATED]` collapsed values into one string — costs nothing
      here because every truncated value already rode a series made unique by `pid`, so collapsing
      the label never collapsed a series. The trade-off is real but only bites when two processes
      differ *solely* past the cut. The cost that did materialise is **bytes, not series**: +10.6 KB,
      ~272 B per truncated value, dominated by `environ` per-value truncation now keeping a
      256-byte prefix plus a 34-byte marker where it previously emitted a bare 11-byte
      `[TRUNCATED]`. Task 4's warning that the fatter marker would push `environ_truncated` to 1
      more often did **not** fire on this host (1 → 1); it remains plausible on hosts with many
      truncated variables and is documented in the README either way.

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

---

## Post-implementation review corrections

Applied after the plan's own tasks were complete; recorded here because two of
them changed decisions the plan had made.

1. **`environ` per-value truncation drops the body (reverses Task 4 for this
   label).** Task 4 replaced `[TRUNCATED]` with prefix + fingerprint everywhere.
   For `environ` that was an information-disclosure regression the plan did not
   weigh: on `main` an over-long value was hidden *whole*, so length alone acted
   as a second, independent secret heuristic. `isSensitivePair` only recognises
   token-*shaped* values, so a JSON service-account key, a PEM body or a
   connection string falls through, and the new prefix published its first 256
   bytes. `environ` now uses `environTruncMarker` — length and fingerprint, no
   body — while `name`/`cmdline`/`ci_*` keep their prefix as planned. Side
   effect: the marker is ~40 B rather than ~305 B, so Task 4's warning that
   `environ_truncated` would flip sooner is largely moot.

2. **`kuber_*`'s `job_name` was outside the contract.** The plan's goal was
   "every label the exporter emits", but `KubeCollector` emitted `CI_JOB_NAME`
   raw — no `sanitizeLabelValue`, no bound — and it shares a registry, and so a
   gather goroutine, with `ProcessCollector`. Invalid UTF-8 there would have
   crashed the exporter. Now sanitized and bounded at the `ci_job_name` limit.

3. Smaller fixes: `mergedMaxLabelBytes` no longer trusts its caller to have run
   `validateMaxLabelBytes` (the failure mode was fail-*open*); the dead
   `boundLabel`/`ciJobLabelValues` default-table wrappers were deleted along with
   the unreachable zero-value-collector fallbacks; the truncation counter's
   per-gather semantics are now stated in its `Help` and the README; tests were
   added for the sanitize-before-bound ordering, the `ci_*` observer wiring, and
   the validation-error determinism.

**Still open:** the `ci_*` limits remain unvalidated estimates — see the section
above. That caveat is now also carried in `README.md`'s label size contract
table so it stays visible outside this file.
