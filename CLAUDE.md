# GitLab Process History Exporter - Developer Guidelines

Prometheus process exporter with an active 10-minute sliding cache in-memory buffer and a premium dark-mode SPA dashboard.

## Build and Quality Verification
* Build binaries: `make build` (produces both `.bin/gitlab-procs-exporter` and `.bin/jobreport-web`; `make build-jobreport` / `make build-jobreport-web` build the companions individually)
* Format code: `make fmt`
* Run linters: `make lint`
* Run unit tests: `make test`

## Project Architecture
* `main.go` — Entrypoint, configuration setup, background scrapers, and embedded Web/REST servers.
* `exporter/history.go` — Sliding 10-minute `HistoryStore` buffer designed with thread-safe lock pools.
* `exporter/collector.go` — Custom Prometheus exporter metrics reporting system with credential filtering rules.
* `exporter/labelbound.go` — The published per-label size contract (`MaxLabelBytes`), the `truncateWithFingerprint` / `environTruncMarker` primitives and `boundLabelWith`.
* `exporter/config.go` — `--config` YAML loader with fail-fast validation of `redact_key_substrings` and `max_label_bytes`.
* `exporter/kube_collector.go` — In-cluster-only `kuber_*` job-resource metrics. Registered on the same registry as `ProcessCollector`, so a panic here takes the whole exporter down; its `job_name` label is `/proc`-sourced and gets the same sanitize+bound treatment.
* `dashboard/` — Front-end glassmorphic SPA utilizing Chart.js rendering timeline charts.
* `internal/jobreport/` — Shared jobreport engine (Prometheus queries, log parsing, table rendering). Entry point `Main(argv []string) int` drives both the CLI and the web UI.
* `cmd/jobreport/` — Thin wrapper binary; `main` delegates to `internal/jobreport.Main`.
* `cmd/jobreport-web/` — Self-contained htmx web UI for jobreport. Single binary that embeds templates + htmx via `go:embed` and self-execs: when `os.Args[1] == "report"` it behaves as the jobreport CLI, otherwise it serves HTTP. The server re-execs itself (resolved via `os.Executable()`) with a leading `report` arg to produce reports — never via a shell string; pass `exec.Command` separate, validated args (digits-only job id). Treat as an internal tool: no auth, the backend connects to the user-supplied Prometheus URL (SSRF caveat in its README).

## Commit & PR Conventions
* Generate commit messages with the local `git-camus` utility using its `claude-cli` provider (git-camus >= 0.5.0): `git-camus -p claude-cli -m "<summary>" -s` to preview, then `git-camus -p claude-cli -m "<summary>"` to commit. It calls the local `claude -p` CLI via the CLI's own login (strips `ANTHROPIC_API_KEY`/`ANTHROPIC_AUTH_TOKEN`) — no API key.
* Never add Claude/Anthropic attribution: no `Co-Authored-By: Claude …` trailer in commits, no "🤖 Generated with Claude Code" footer in PR bodies, and no "by Claude"-style mentions in code or doc comments.

## Code Style & Implementation Standards
* Keep one `*_test.go` file associated directly per implementation file.
* Use `sync.RWMutex` to isolate write threads in `HistoryStore` from foreground scrapes.
* Prevent metric leaks by filtering out dynamic sensitive tokens in `IsSecretKey()`.
* Every label value sourced from `/proc` (name, cmdline, environ, CI vars) MUST pass through `sanitizeLabelValue()` before reaching `MustNewConstMetric` — it panics on invalid UTF-8, and that panic fires on the registry's gather goroutine, crashing the whole exporter.
* That covers UTF-8 validity but not length: every label value MUST then pass `pc.boundLabel()` (the collector method, so the cut is counted) before reaching `MustNewConstMetric`. Order is fixed — `sanitizeLabelValue()`, then redact, then bound; bounding invalid UTF-8 makes the rune walk-back meaningless, because `strings.ToValidUTF8` expands each bad byte to a 3-byte U+FFFD and would re-inflate the value past the limit. A new label needs an entry in `MaxLabelBytes` (`exporter/labelbound.go`) or it silently passes through unbounded, and limits are BYTE counts, never rune counts. `environ` is the one exception: it is a composed blob bounded by `maxEnvironVars`/`maxEnvironValueLen`/`maxEnvironBytes` in `scrubEnviron` instead.
* In `scrubEnviron`, an over-long value is replaced WHOLE by `environTruncMarker()` — never truncated to a prefix. `environ` is the only label built from arbitrary operator-unknown pairs, so length alone is a secret signal there: `isSensitivePair` only recognises token-*shaped* values (`isTokenCharset` rejects braces, quotes, colons, newlines), so a JSON service-account key or PEM body matches nothing and a prefix would publish credential material. Labels with a known, non-secret shape (`name`, `cmdline`, `ci_*`) keep their prefix via `truncateWithFingerprint`.
* Wire a loaded `*exporter.Config` through `NewProcessCollectorWithConfig`. `NewProcessCollector` keeps the DEFAULT limit table, so passing a config to it compiles fine and silently drops every `max_label_bytes` override — exactly the silently-ignored-limit failure the contract exists to prevent.
* Any boundary that emits process environ outside the Prometheus label path (notably the `/api/processes` and `/api/history` JSON handlers) MUST route it through `exporter.RedactEnviron()`; secret scrubbing is not exclusive to the collector. Both `RedactEnviron` and `scrubEnviron` share the `isSensitivePair()` predicate so they can't disagree on what counts as a secret.
* Keep process handlers lightweight: persist `*process.Process` pointers inside scraping caches to avoid reading Jiffies baseline tables repetitively. Look up cached pointers via `liveProcess()`, which evicts stale entries on PID reuse.
* Any temporary testing files MUST be cleaned up via deferred closures.
