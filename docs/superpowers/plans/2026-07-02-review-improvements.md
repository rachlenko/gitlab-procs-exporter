# Post-Review Improvements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the correctness, security, and robustness gaps found while reviewing nevodchikov96's CI-label/environ-bounding commits (`ab9bb10`, `adc108b`) and the full project.

**Architecture:** All changes are small, local hardening fixes: copy-on-read in `HistoryStore`, secret redaction at the JSON API boundary (reusing the collector's existing scrub rules), true counter semantics for the CPU metric, UTF-8 sanitization + cmdline bounding for Prometheus label values, and a PID-reuse guard in the scrape cache. No new packages, no new dependencies.

**Tech Stack:** Go 1.24, `prometheus/client_golang`, `shirou/gopsutil/v3`. Tests use stdlib `testing` + `httptest` + `prometheus/client_model` (already imported).

## Global Constraints

- Go version: `1.24` (from `go.mod`); no new dependencies.
- Every change must keep `make fmt`, `make lint`, and `make test` green (`make test` runs `go test -race ./...`).
- One `*_test.go` file per implementation file (project convention — put new tests in the existing test file of the file you touched).
- Commit with the local `git-camus` utility: preview `git-camus -p claude-cli -m "<summary>" -s`, then commit `git-camus -p claude-cli -m "<summary>"`.
- NEVER add Claude/Anthropic attribution: no `Co-Authored-By: Claude …` trailer, no "Generated with Claude Code" footer, no "by Claude" comments. (Commits `ab9bb10`/`adc108b` violated this; do not repeat.)
- Use `sync.RWMutex` patterns already present in `HistoryStore`; do not restructure locking.
- The repo sits on `main`: create a working branch **before Task 1's commit** (`git checkout -b feat/review-hardening`) and open a PR at the end.

---

## Review Findings (context for the tasks)

### Scope analyzed

- nevodchikov96's commits: `ab9bb10` (promote `CI_JOB_ID`/`CI_JOB_NAME`/`CI_PROJECT_PATH`/`CI_PIPELINE_ID` to metric labels) and `adc108b` (bound the `gitlab_process_info` `environ` label), plus the follow-up review fix `d607e94`.
- Full project: `main.go`, `exporter/` (collector, history, config, kube), `dashboard/index.html`, README, CI workflows, Makefile.

### Verdict on the reviewed commits

The two commits are solid work: single ordered source of truth for label names/values (`ciJobLabelKeys`), correct empty-label semantics for non-CI processes, well-reasoned three-tier bounding of the environ label (count cap, per-value cap, byte ceiling at pair boundaries, never byte-cutting UTF-8), a precisely specified `environ_truncated` flag, and thorough boundary tests (`TestScrubEnvironBounds`, `TestScrubEnvironAtLimits`, `TestScrubEnvironUTF8Safe`). The `d607e94` follow-up already corrected the one factual error (the `label_value_length_limit` default claim). No functional bugs found *inside* the new code. The findings below are gaps the review surfaced *around* it.

### Findings, by severity

| # | Severity | Finding | Where | Fixed by |
|---|----------|---------|-------|----------|
| 1 | **High (security)** | `/api/processes` and `/api/history` return the **raw, unredacted** `Environ` map as JSON. All secret scrubbing lives only in the Prometheus label path (`scrubEnviron`). The dashboard's `[REDACTED / SECRET]` styling (`dashboard/index.html:817`) checks for values the API never produces, so `DB_PASSWORD`, tokens, etc. render verbatim in the browser on an unauthenticated endpoint. | `main.go:186-209` | Task 2 |
| 2 | **High (correctness)** | `gitlab_process_cpu_seconds_total` is a `Counter` whose value is `gopsutil.Percent(0)` — an instantaneous, non-monotonic **percent**. README line ~158 documents the caveat ("do not rate() it") yet README §3 and the alert-rules example still apply `rate()` to it, and the Grafana dashboard panel has to warn users off. Fix the metric instead of documenting around it. | `main.go:286`, `exporter/collector.go:208` | Task 3 |
| 3 | **Medium (crash)** | `MustNewConstMetric` **panics** on invalid UTF-8 label values. `name`, `cmdline`, `environ`, and the new `ci_job_*` values come from `/proc` and are arbitrary bytes. The panic happens on the registry's gather goroutine (not the HTTP handler), so one hostile/binary environ **crashes the whole exporter**. Also `cmdline` is unbounded — the exact label-size problem `adc108b` fixed for `environ` (ARG_MAX can reach 2 MB). | `exporter/collector.go:199-222` | Task 4 |
| 4 | **Medium (data race)** | `QueryHistory` returns the store's own `[]ProcessSample` slices. `MarkInactive` later mutates `samples[len-1].IsActive` in that same backing array under the write lock, while the JSON encoder reads it with no lock. (`GetActiveProcesses` is safe — it copies sample values under `RLock`.) | `exporter/history.go:112`, `exporter/history.go:66` | Task 1 |
| 5 | **Low (accuracy)** | `procCache` is keyed by PID only. If the OS reuses a PID between scrapes, the stale `*process.Process` yields the old process's CPU baseline / cached create time, corrupting `Percent`/`CreateTime` for the newcomer. | `main.go:245-267` | Task 5 |

### Reviewed and deliberately NOT planned (rejected or deferred)

- **`environ_truncated` as a label vs. separate metric** — a label flip creates a new `gitlab_process_info` series, but any environ change already does that on the `environ` label itself; a separate metric buys nothing. Keep as is.
- **`ci_job_id` fallback from cmdline** — on some runner fleets the job id appears only in the process cmdline, not environ (observed on the node3 jobreport deploy). Parsing cmdline is runner-config-specific guesswork; revisit only with a concrete runner config in hand.
- **Configurable CI label set** (`ciJobLabelKeys` from YAML config) — YAGNI until a second consumer needs different variables; changing label sets at deploy time also breaks series continuity.
- **Auth / listen-address flag for the HTTP server** — documented internal-tool posture (same stance as jobreport-web's SSRF caveat). Task 2 removes the actual secret exposure; binding/auth remains an operator concern (firewall). Revisit if it's ever exposed beyond the node.
- **Graceful shutdown, scraper self-metrics (`scrape_duration`, `scrape_errors_total`), configurable 10-minute window** — nice-to-haves, no current pain. Defer.
- **Process note (not code):** commits `ab9bb10`/`adc108b` carry a `Co-Authored-By: Claude Opus 4.8` trailer, which `CLAUDE.md` forbids. Nothing to change retroactively; future commits must comply (see Global Constraints).

---

## File Structure

No new files. Changes land in:

- `exporter/history.go` (+ `exporter/history_test.go`) — Task 1 copy-on-read; Task 3 new `CPUSeconds` field.
- `exporter/collector.go` (+ `exporter/collector_test.go`) — Task 2 `RedactEnviron`/`keyMatchesAny`; Task 3 counter value; Task 4 `sanitizeLabelValue`/`boundCmdline`.
- `main.go` (+ `main_test.go`) — Task 2 API redaction; Task 3 `p.Times()` scrape; Task 5 `liveProcess`.
- `README.md`, `deploy/k8s/dashboard-configmap.yaml` — Task 3 doc/dashboard truth-up.

---

### Task 1: `QueryHistory` returns copies (data-race fix)

**Files:**
- Modify: `exporter/history.go:111-113`
- Test: `exporter/history_test.go`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `QueryHistory(queryType, value string) map[string][]ProcessSample` — same signature, but the returned slices are now **copies**, safe to read/mutate after the lock is released. Task 2's history handler relies on this.

- [x] **Step 1: Write the failing test**

Append to `exporter/history_test.go`:

```go
// TestQueryHistoryReturnsCopies guards against QueryHistory handing out the
// store's own backing arrays: MarkInactive mutates the last sample in place
// under the write lock, which would race with (and corrupt) a caller still
// reading the returned slice.
func TestQueryHistoryReturnsCopies(t *testing.T) {
	hs := NewHistoryStore()
	hs.AddSample(ProcessSample{
		Timestamp:  time.Now(),
		PID:        1,
		Name:       "bash",
		CreateTime: 42,
		IsActive:   true,
	})

	got := hs.QueryHistory("pid", "1")
	if len(got) != 1 {
		t.Fatalf("expected 1 timeline, got %d", len(got))
	}

	// Mutates the stored sample's IsActive in place; a shared slice would
	// expose the mutation to the previously returned result.
	hs.MarkInactive(map[int32]bool{})

	for key, samples := range got {
		if !samples[len(samples)-1].IsActive {
			t.Errorf("QueryHistory result for %q was mutated by MarkInactive: expected a copy", key)
		}
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test -race ./exporter/ -run TestQueryHistoryReturnsCopies -v`
Expected: FAIL with "was mutated by MarkInactive: expected a copy"

- [x] **Step 3: Fix `QueryHistory` to copy**

In `exporter/history.go`, replace the match block (currently `result[key] = samples` at line ~112):

```go
		if match {
			// Copy: the store mutates its own slices (MarkInactive flips
			// IsActive on the last element) after the read lock is released.
			out := make([]ProcessSample, len(samples))
			copy(out, samples)
			result[key] = out
		}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `go test -race ./exporter/ -v`
Expected: PASS (all tests, including the new one)

- [x] **Step 5: Commit**

```bash
make fmt && make lint
git add exporter/history.go exporter/history_test.go
git-camus -p claude-cli -m "history: QueryHistory returns sample copies to fix read/write race" -s   # preview
git-camus -p claude-cli -m "history: QueryHistory returns sample copies to fix read/write race"
```

---

### Task 2: Redact environ at the JSON API boundary

**Files:**
- Modify: `exporter/collector.go:100-109` (extract `keyMatchesAny`, add `RedactEnviron`)
- Modify: `main.go:110-144, 186-209` (thread redact list into handlers, redact before encoding)
- Test: `exporter/collector_test.go`, `main_test.go`

**Interfaces:**
- Consumes: Task 1's copy semantics for `QueryHistory` (safe to mutate returned samples), but the code below is written to be safe even without it.
- Produces: `func RedactEnviron(environ map[string]string, extraKeySubstrings []string) map[string]string` (exported, `exporter` package) — returns a redacted **copy**; the input map is never modified. `extraKeySubstrings` must already be normalized (lowercase/trimmed — `LoadConfig` and `NewProcessCollector` both already do this via `normalizeSubstrings`).
- Produces: new handler signatures `serveAPIProcesses(w, r, store, redactKeySubstrings []string)` and `serveAPIHistory(w, r, store, redactKeySubstrings []string)`.

- [x] **Step 1: Write the failing unit test for `RedactEnviron`**

Append to `exporter/collector_test.go`:

```go
func TestRedactEnviron(t *testing.T) {
	in := map[string]string{
		"DB_PASSWORD": "unsafe-pwd-here",              // built-in denylist key
		"VAULT_ADDR":  "https://vault.example:8200",   // operator-configured substring
		"MY_VAR":      "glpat-abcdefghij1234567890",   // secret-shaped value
		"USER":        "gitlab",                       // benign
	}
	out := RedactEnviron(in, []string{"vault"})

	for _, k := range []string{"DB_PASSWORD", "VAULT_ADDR", "MY_VAR"} {
		if out[k] != "[REDACTED]" {
			t.Errorf("expected %s redacted, got %q", k, out[k])
		}
	}
	if out["USER"] != "gitlab" {
		t.Errorf("expected USER to pass through, got %q", out["USER"])
	}
	// The input map must not be modified.
	if in["DB_PASSWORD"] != "unsafe-pwd-here" {
		t.Error("RedactEnviron mutated its input map")
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./exporter/ -run TestRedactEnviron -v`
Expected: FAIL to compile — "undefined: RedactEnviron"

- [x] **Step 3: Implement `keyMatchesAny` + `RedactEnviron`**

In `exporter/collector.go`, replace `keyInExtra` (lines ~100-109) with:

```go
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

// keyInExtra reports whether key matches any operator-configured substring.
func (pc *ProcessCollector) keyInExtra(key string) bool {
	return keyMatchesAny(key, pc.extraKeySubstrings)
}

// RedactEnviron returns a copy of environ with every sensitive value replaced
// by "[REDACTED]", applying the same rules as the gitlab_process_info environ
// label: the built-in key denylist (IsSecretKey), operator-configured
// substrings (must already be normalized, see normalizeSubstrings), and the
// value-shape heuristics (IsSecretValue). Anything that leaves the process
// carrying an environ — the JSON API in particular — must go through here.
func RedactEnviron(environ map[string]string, extraKeySubstrings []string) map[string]string {
	out := make(map[string]string, len(environ))
	for k, v := range environ {
		if IsSecretKey(k) || keyMatchesAny(k, extraKeySubstrings) || IsSecretValue(v) {
			v = "[REDACTED]"
		}
		out[k] = v
	}
	return out
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./exporter/ -v`
Expected: PASS

- [x] **Step 5: Write the failing API test**

Append to `main_test.go` (add `"strings"` to its imports):

```go
// TestServeAPIRedactsEnviron pins the security boundary: the JSON API must
// never return raw secrets — scrubbing is not only for the Prometheus label.
func TestServeAPIRedactsEnviron(t *testing.T) {
	store := exporter.NewHistoryStore()
	store.AddSample(exporter.ProcessSample{
		Timestamp:  time.Now(),
		PID:        4444,
		Name:       "runner",
		Environ:    map[string]string{"DB_PASSWORD": "unsafe-pwd-here", "USER": "gitlab"}, //nolint:gosec // G101: fake secret to exercise redaction
		CreateTime: 100,
		IsActive:   true,
	})

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/processes", nil)
	rr := httptest.NewRecorder()
	serveAPIProcesses(rr, req, store, nil)
	body := rr.Body.String()
	if strings.Contains(body, "unsafe-pwd-here") {
		t.Errorf("/api/processes leaked a secret environ value: %s", body)
	}
	if !strings.Contains(body, "[REDACTED]") {
		t.Errorf("expected [REDACTED] in /api/processes response, got: %s", body)
	}

	req2 := httptest.NewRequestWithContext(context.Background(), "GET", "/api/history?pid=4444", nil)
	rr2 := httptest.NewRecorder()
	serveAPIHistory(rr2, req2, store, nil)
	body2 := rr2.Body.String()
	if strings.Contains(body2, "unsafe-pwd-here") {
		t.Errorf("/api/history leaked a secret environ value: %s", body2)
	}
}
```

- [x] **Step 6: Run test to verify it fails**

Run: `go test . -run TestServeAPIRedactsEnviron -v`
Expected: FAIL to compile — the handlers don't take a fourth argument yet. That's the intended signature change; proceed.

- [x] **Step 7: Thread the redact list through `main.go`**

In `main.go`, update the two handlers (lines ~186-209):

```go
func serveAPIProcesses(w http.ResponseWriter, r *http.Request, store *exporter.HistoryStore, redactKeySubstrings []string) {
	w.Header().Set("Content-Type", "application/json")
	active := store.GetActiveProcesses()
	for i := range active {
		active[i].Environ = exporter.RedactEnviron(active[i].Environ, redactKeySubstrings)
	}
	_ = json.NewEncoder(w).Encode(active)
}

func serveAPIHistory(w http.ResponseWriter, r *http.Request, store *exporter.HistoryStore, redactKeySubstrings []string) {
	w.Header().Set("Content-Type", "application/json")

	pidStr := r.URL.Query().Get("pid")
	nameStr := r.URL.Query().Get("name")

	var history map[string][]exporter.ProcessSample
	if pidStr != "" {
		history = store.QueryHistory("pid", pidStr)
	} else if nameStr != "" {
		history = store.QueryHistory("name", nameStr)
	} else {
		http.Error(w, `{"error": "Missing 'pid' or 'name' query parameter"}`, http.StatusBadRequest)
		return
	}

	// Rebuild each timeline with redacted environ copies; the store's own
	// samples are never touched.
	for key, samples := range history {
		redacted := make([]exporter.ProcessSample, len(samples))
		for i, s := range samples {
			s.Environ = exporter.RedactEnviron(s.Environ, redactKeySubstrings)
			redacted[i] = s
		}
		history[key] = redacted
	}

	_ = json.NewEncoder(w).Encode(history)
}
```

And in `main()` (lines ~139-144), pass the already-loaded `redactKeySubstrings` into the closures:

```go
	http.HandleFunc("/api/processes", func(w http.ResponseWriter, r *http.Request) {
		serveAPIProcesses(w, r, store, redactKeySubstrings)
	})
	http.HandleFunc("/api/history", func(w http.ResponseWriter, r *http.Request) {
		serveAPIHistory(w, r, store, redactKeySubstrings)
	})
```

(`redactKeySubstrings` is declared at `main.go:110` and populated from `LoadConfig`, which normalizes it — no extra normalization needed.)

- [x] **Step 8: Fix the two existing call sites in `main_test.go`**

`TestServeAPIProcesses` (line ~63): `serveAPIProcesses(rr, req, store)` → `serveAPIProcesses(rr, req, store, nil)`.
`TestServeAPIHistory` (lines ~103, ~113, ~132): `serveAPIHistory(rrN, reqN, store)` → `serveAPIHistory(rrN, reqN, store, nil)` (three call sites).

- [x] **Step 9: Run tests to verify they pass**

Run: `go test -race . ./exporter/ -v`
Expected: PASS, including `TestServeAPIRedactsEnviron`

- [x] **Step 10: Commit**

```bash
make fmt && make lint
git add exporter/collector.go exporter/collector_test.go main.go main_test.go
git-camus -p claude-cli -m "api: redact secret environ values in /api/processes and /api/history" -s
git-camus -p claude-cli -m "api: redact secret environ values in /api/processes and /api/history"
```

---

### Task 3: Make `gitlab_process_cpu_seconds_total` a true counter

**Files:**
- Modify: `exporter/history.go:18` (add `CPUSeconds` field)
- Modify: `main.go:285-289` (collect `p.Times()`)
- Modify: `exporter/collector.go:208` (emit `CPUSeconds`)
- Modify: `README.md` (metric table + delete the ¹ caveat), `deploy/k8s/dashboard-configmap.yaml:107,113`
- Test: `exporter/collector_test.go`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `ProcessSample.CPUSeconds float64` (JSON tag `cpu_seconds`) — cumulative user+system CPU seconds. `CPUUsage` (percent) is **kept** for the dashboard/JSON API; only the Prometheus counter switches to `CPUSeconds`.
- **Breaking metric change:** the exported value of `gitlab_process_cpu_seconds_total` changes meaning from "instantaneous percent" to "cumulative seconds". This is the fix — the name and type were already promising seconds. Dashboards that (incorrectly) charted the raw value must switch to `rate()`; the README's existing `rate()` examples become correct instead of wrong.

- [x] **Step 1: Write the failing test**

In `exporter/collector_test.go`, inside `TestCollectorDescribeAndCollect`:

1. Add `CPUSeconds: 123.5,` to the `sample` literal (line ~48, next to `CPUUsage: 45.2`).
2. In the `for m := range metricChan` loop (line ~81), add a value assertion:

```go
		if strings.Contains(descStr, "gitlab_process_cpu_seconds_total") {
			var dtoMetric dto.Metric
			if err := m.Write(&dtoMetric); err != nil {
				t.Fatalf("failed to write cpu metric: %v", err)
			}
			if got := dtoMetric.GetCounter().GetValue(); got != 123.5 {
				t.Errorf("cpu counter must emit cumulative CPUSeconds (123.5), got %v — emitting the percent gauge value breaks rate()", got)
			}
		}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./exporter/ -run TestCollectorDescribeAndCollect -v`
Expected: FAIL to compile first ("unknown field CPUSeconds") — add the struct field (Step 3, first edit) and re-run; then FAIL with "cpu counter must emit cumulative CPUSeconds (123.5), got 45.2".

- [x] **Step 3: Implement**

`exporter/history.go` — add the field to `ProcessSample` after `CPUUsage` (line ~18):

```go
	CPUUsage   float64           `json:"cpu_usage"`      // CPU percentage usage
	CPUSeconds float64           `json:"cpu_seconds"`    // Cumulative user+system CPU seconds
```

`main.go` — in `scrape()` after the `cpuUsage` block (line ~289):

```go
	// Cumulative CPU seconds (user+system) — feeds the _total counter.
	var cpuSeconds float64
	if times, err := p.Times(); err == nil && times != nil {
		cpuSeconds = times.User + times.System
	}
```

and add `CPUSeconds: cpuSeconds,` to the `exporter.ProcessSample{...}` literal in `store.AddSample` (line ~328, next to `CPUUsage: cpuUsage,`).

`exporter/collector.go` — in `Collect` (line ~208):

```go
		ch <- prometheus.MustNewConstMetric(pc.cpuDesc, prometheus.CounterValue, p.CPUSeconds, labels...)
```

- [x] **Step 4: Run tests to verify they pass**

Run: `go test -race . ./exporter/ -v`
Expected: PASS

- [x] **Step 5: Update docs and the Grafana dashboard**

`README.md`:

1. Metric table row (line ~138) — replace:
   `| `gitlab_process_cpu_seconds_total` | counter¹ | percent¹ | Per-process CPU usage as sampled by gopsutil. |`
   with:
   `| `gitlab_process_cpu_seconds_total` | counter | seconds | Cumulative user+system CPU time consumed by the process. |`
2. Delete the entire caveat paragraph starting `¹ **Caveat on \`gitlab_process_cpu_seconds_total\`:**` (line ~158, ends at "…and `rate()` works on them."). The `rate()` examples in §3 and the alert rules are now correct as written — leave them.

`deploy/k8s/dashboard-configmap.yaml`:

1. Line ~107 — replace:
   `"description": "gitlab_process_cpu_seconds_total carries an instantaneous CPU usage percent (do not rate it).",`
   with:
   `"description": "Per-process CPU cores consumed: rate() over the cumulative CPU-seconds counter.",`
2. Line ~113 — replace the expr:
   `"expr": "topk(10, gitlab_process_cpu_seconds_total)"`
   with:
   `"expr": "topk(10, rate(gitlab_process_cpu_seconds_total[5m]))"`
3. Check the surrounding panel title/axis unit in the same JSON block: if the title says "percent" or the unit is `percent`, change to "CPU cores" / unit `none` (Grafana `short`) to match the new expression.

- [x] **Step 6: Verify the full suite and the exposition manually**

Run: `make test`
Expected: PASS

Run: `go run . -port 18123 &` then `curl -s localhost:18123/metrics | grep -m3 gitlab_process_cpu_seconds_total; kill %1`
Expected: values are cumulative seconds (large, monotonically growing on re-curl), not small percent floats.

- [x] **Step 7: Commit**

```bash
make fmt && make lint
git add exporter/history.go exporter/collector.go main.go exporter/collector_test.go README.md deploy/k8s/dashboard-configmap.yaml
git-camus -p claude-cli -m "collector: emit real cumulative CPU seconds in gitlab_process_cpu_seconds_total (breaking: was a percent)" -s
git-camus -p claude-cli -m "collector: emit real cumulative CPU seconds in gitlab_process_cpu_seconds_total (breaking: was a percent)"
```

---

### Task 4: Sanitize and bound all label values (panic-proof the collector)

**Files:**
- Modify: `exporter/collector.go` (add `sanitizeLabelValue`, `boundCmdline`, `maxCmdlineBytes`; apply in `Collect`, `ciJobLabelValues`, `scrubEnviron`)
- Test: `exporter/collector_test.go`

**Interfaces:**
- Consumes: nothing from other tasks (independent of Tasks 1-3; if Task 3 landed, `Collect` already emits `p.CPUSeconds` — keep that).
- Produces: `sanitizeLabelValue(v string) string` and `boundCmdline(s string) string` (both unexported, `exporter` package). Every label value sourced from `/proc` (name, cmdline, environ keys/values, CI values) flows through `sanitizeLabelValue`.

- [x] **Step 1: Write the failing tests**

Append to `exporter/collector_test.go`:

```go
// TestCollectSurvivesInvalidUTF8 pins the crash mode: MustNewConstMetric
// panics on invalid UTF-8 label values, and that panic happens on the
// registry's gather goroutine — one binary environ would kill the exporter.
func TestCollectSurvivesInvalidUTF8(t *testing.T) {
	store := NewHistoryStore()
	store.AddSample(ProcessSample{
		Timestamp:  time.Now(),
		PID:        7777,
		Name:       "bad\xffname",
		CmdLine:    "run \xfe--flag",
		Environ:    map[string]string{"WEIRD\xff": "va\xfdlue", "CI_JOB_NAME": "job\xff"},
		CreateTime: 300,
		IsActive:   true,
	})

	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(NewProcessCollector(store))
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather failed on invalid UTF-8 input: %v", err)
	}
	if len(mfs) == 0 {
		t.Fatal("expected metrics to be gathered")
	}
}

func TestSanitizeLabelValue(t *testing.T) {
	if got := sanitizeLabelValue("plain-value"); got != "plain-value" {
		t.Errorf("valid string must pass through unchanged, got %q", got)
	}
	got := sanitizeLabelValue("abc\xff\xfedef")
	if !utf8.ValidString(got) {
		t.Errorf("sanitized value is not valid UTF-8: %q", got)
	}
	if !strings.HasPrefix(got, "abc") || !strings.HasSuffix(got, "def") {
		t.Errorf("sanitizing must preserve the valid bytes around the bad ones, got %q", got)
	}
}

func TestBoundCmdline(t *testing.T) {
	if got := boundCmdline("short cmd"); got != "short cmd" {
		t.Errorf("short cmdline must pass through unchanged, got %q", got)
	}
	// 2-byte runes, twice the limit: the cut must land on a rune boundary.
	long := strings.Repeat("ы", maxCmdlineBytes)
	got := boundCmdline(long)
	if len(got) > maxCmdlineBytes+len(environValueTruncMarker) {
		t.Errorf("bounded cmdline is %d bytes, limit %d", len(got), maxCmdlineBytes+len(environValueTruncMarker))
	}
	if !utf8.ValidString(got) {
		t.Errorf("bounded cmdline is not valid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, environValueTruncMarker) {
		t.Errorf("expected visible truncation marker suffix, got tail %q", got[len(got)-20:])
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./exporter/ -run 'TestCollectSurvivesInvalidUTF8|TestSanitizeLabelValue|TestBoundCmdline' -v`
Expected: compile FAIL on `sanitizeLabelValue`/`boundCmdline` (undefined). Note: once they compile as stubs, `TestCollectSurvivesInvalidUTF8` **panics** (not just fails) — that is the bug being fixed.

- [x] **Step 3: Implement**

In `exporter/collector.go`, add `"unicode/utf8"` to the imports, then:

Add `maxCmdlineBytes` inside the existing bounds `const` block (line ~114):

```go
	// maxCmdlineBytes caps the gitlab_process_info "cmdline" label; a process
	// with an enormous argv (ARG_MAX can reach 2MB) must not blow up the
	// scrape the way an unbounded environ could.
	maxCmdlineBytes = 2048
```

Add the two helpers after `environValueTruncMarker` (line ~132):

```go
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

// boundCmdline caps a valid-UTF-8 cmdline at maxCmdlineBytes, cutting at a
// rune boundary and appending environValueTruncMarker so truncation is
// visible. Input must already be sanitized (valid UTF-8).
func boundCmdline(s string) string {
	if len(s) <= maxCmdlineBytes {
		return s
	}
	i := maxCmdlineBytes
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + environValueTruncMarker
}
```

In `ciJobLabelValues` (line ~38), sanitize each value:

```go
		vals[i] = sanitizeLabelValue(environ[k.env])
```

In `scrubEnviron`'s loop (line ~160), sanitize the value **before** the length check (sanitizing can expand bytes 1→3, so checking first would undercount) and the key at pair render:

```go
	for _, k := range keys {
		val := sanitizeLabelValue(environ[k])
		switch {
		case IsSecretKey(k) || pc.keyInExtra(k) || IsSecretValue(val):
			val = "[REDACTED]"
		case len(val) > maxEnvironValueLen:
			val = environValueTruncMarker
		}
		pair := fmt.Sprintf("%s=%s", sanitizeLabelValue(k), val)
```

(the rest of the loop is unchanged — the byte ceiling now counts sanitized bytes, keeping the 8192 guarantee exact).

In `Collect` (line ~202), sanitize name and bound cmdline:

```go
	for _, p := range processes {
		pidStr := fmt.Sprintf("%d", p.PID)
		name := sanitizeLabelValue(p.Name)
		ciVals := ciJobLabelValues(p.Environ)
		labels := append([]string{pidStr, name}, ciVals...)
```

and in the info metric:

```go
		cmdline := boundCmdline(sanitizeLabelValue(p.CmdLine))
		infoLabels := append([]string{pidStr, name, cmdline, environ, truncatedLabel}, ciVals...)
```

- [x] **Step 4: Run tests to verify they pass**

Run: `go test -race ./exporter/ -v`
Expected: PASS — all new tests plus every existing `TestScrubEnviron*` boundary test (they use valid UTF-8, so behavior is unchanged for them).

- [x] **Step 5: Commit**

```bash
make fmt && make lint
git add exporter/collector.go exporter/collector_test.go
git-camus -p claude-cli -m "collector: sanitize label values to valid UTF-8 and bound cmdline (crash-proof the scrape)" -s
git-camus -p claude-cli -m "collector: sanitize label values to valid UTF-8 and bound cmdline (crash-proof the scrape)"
```

---

### Task 5: PID-reuse guard in the scrape cache

**Files:**
- Modify: `main.go:245-267`
- Test: `main_test.go`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `liveProcess(procCache map[int32]*process.Process, pid int32) (*process.Process, error)` (unexported, `main` package). `scrape()` keeps its signature.

- [ ] **Step 1: Write the failing test**

Append to `main_test.go` (add `"os"` to its imports):

```go
func TestLiveProcessReusesCachedObject(t *testing.T) {
	cache := make(map[int32]*process.Process)
	self := int32(os.Getpid())

	p1, err := liveProcess(cache, self)
	if err != nil {
		t.Fatalf("liveProcess failed for our own pid: %v", err)
	}
	p2, err := liveProcess(cache, self)
	if err != nil {
		t.Fatalf("liveProcess failed on second call: %v", err)
	}
	if p1 != p2 {
		t.Error("expected the cached *process.Process to be reused for a still-running process (CPU Percent baselines live on it)")
	}
	if len(cache) != 1 {
		t.Errorf("expected exactly 1 cache entry, got %d", len(cache))
	}
}
```

(PID reuse itself can't be reproduced deterministically in a unit test; the guard is exercised by `IsRunning` returning false for a stale entry, which this test's inverse — reuse for a live process — plus the existing `TestScrape` cover.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test . -run TestLiveProcessReusesCachedObject -v`
Expected: FAIL to compile — "undefined: liveProcess"

- [ ] **Step 3: Implement**

In `main.go`, add above `scrape()`:

```go
// liveProcess returns the cached *process.Process for pid, evicting the entry
// when the PID now belongs to a different process (the kernel reuses PIDs;
// gopsutil's IsRunning compares create times). Without this, the newcomer
// inherits the old process's CPU Percent baseline and cached create time.
func liveProcess(procCache map[int32]*process.Process, pid int32) (*process.Process, error) {
	if p, ok := procCache[pid]; ok {
		if running, err := p.IsRunning(); err == nil && running {
			return p, nil
		}
		delete(procCache, pid)
	}
	p, err := process.NewProcess(pid)
	if err != nil {
		return nil, err
	}
	procCache[pid] = p
	return p, nil
}
```

In `scrape()`, replace the cache-lookup block (lines ~257-267):

```go
		p, err := liveProcess(procCache, pid)
		if err != nil {
			continue // Process exited or inaccessible
		}
```

(One extra `/proc` create-time read per cached PID per scrape — negligible against the environ/IO reads already done per process.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race . -v`
Expected: PASS, including the existing `TestScrape` and `TestStartScraper`

- [ ] **Step 5: Commit**

```bash
make fmt && make lint
git add main.go main_test.go
git-camus -p claude-cli -m "scrape: evict process cache entries on PID reuse via IsRunning identity check" -s
git-camus -p claude-cli -m "scrape: evict process cache entries on PID reuse via IsRunning identity check"
```

---

## Final verification (after all tasks)

- [ ] Run: `make fmt && make lint && make test` — all green.
- [ ] Run: `make build` — both binaries build.
- [ ] Smoke: `go run . -port 18123 &`; `curl -s localhost:18123/api/processes | grep -c REDACTED` (> 0 expected on a real host); `curl -s localhost:18123/metrics | grep -m2 gitlab_process_cpu_seconds_total` (cumulative seconds); `kill %1`.
- [ ] Open a PR from `feat/review-hardening` to `main`. No Claude/Anthropic attribution anywhere (commits, PR body, comments).
