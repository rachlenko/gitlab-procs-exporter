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
* Any boundary that emits process environ outside the Prometheus label path (notably the `/api/processes` and `/api/history` JSON handlers) MUST route it through `exporter.RedactEnviron()`; secret scrubbing is not exclusive to the collector. Both `RedactEnviron` and `scrubEnviron` share the `isSensitivePair()` predicate so they can't disagree on what counts as a secret.
* Keep process handlers lightweight: persist `*process.Process` pointers inside scraping caches to avoid reading Jiffies baseline tables repetitively. Look up cached pointers via `liveProcess()`, which evicts stale entries on PID reuse.
* Any temporary testing files MUST be cleaned up via deferred closures.
