# Plan: jobreport-web — htmx frontend, single self-contained binary

## Goal

Build a web service `cmd/jobreport-web` that serves an htmx UI for running the
existing jobreport against a chosen Prometheus URL, job id, and time window, and
shows the raw report output. Everything (HTML templates, htmx.js, CSS) is embedded
into ONE binary. The report is executed by the SAME binary re-exec'ing itself
(self-exec) as a `report` subcommand, so there is a single runtime artifact.

## Hard constraints (read before every iteration)

- Each task below is atomic. Do them top to bottom. After finishing a task, run
  its **Verify** step, and only then change its `- [ ]` to `- [x]`.
- The tree MUST build (`go build ./...`) and the touched package's tests MUST pass
  after every task. Never check a box if build/tests are red.
- No new third-party Go dependencies — stdlib only (`net/http`, `html/template`,
  `embed`, `os/exec`, `encoding/json`, `net/url`, `time`, `regexp`). htmx is a
  vendored static asset, not a Go module. This repo does NOT vendor Go deps.
- Never build a shell string for the report. Use `exec.Command` with separate args.
- Follow existing project conventions: one `*_test.go` per implementation file;
  `//nolint:gosec // Gxxx: <reason>` for intentional file/exec usage; gofmt clean.
- Final gate: `make build && make lint && make test` all green, AND a built
  `jobreport-web` serves `/` and produces a report fragment.
- When every box is `[x]` and the final gate passes, output `ALL_TASKS_DONE`.

---

## Phase A — Extract report logic into `internal/jobreport` (keep CLI green)

Goal: move the report logic out of `package main` so both `cmd/jobreport` and
`cmd/jobreport-web` can drive it, while the existing CLI behaves identically.

- [x] A1. Create dir `internal/jobreport/`. Move these files from `cmd/jobreport/`
      into it unchanged except the package clause: `logparse.go`, `meta.go`,
      `prom.go`, `render.go`, `url.go` (and their `_test.go` siblings). Change
      `package main` → `package jobreport` in every moved file. Move
      `cmd/jobreport/testdata/` → `internal/jobreport/testdata/` (tests read
      `testdata/job.log`). **Verify:** `go build ./internal/...` compiles.
- [x] A2. Move the CLI body out of `cmd/jobreport/main.go` into
      `internal/jobreport/cli.go` (`package jobreport`): the `config` type,
      `parseFlags`, `splitPositional`, `isLocalFile`, `run`, `report`,
      `renderRows`, `parseWindow`, `stepFor`, `fdate`, `envOr`, `envInt`, `sep`.
      Add the entry point:
      ```go
      // Main runs the jobreport CLI with argv (os.Args[1:]) and returns an exit code.
      func Main(argv []string) int {
          cfg, err := parseFlags(argv)
          if errors.Is(err, flag.ErrHelp) { return 0 }
          if err != nil { fmt.Fprintln(os.Stderr, "error:", err); return 2 }
          if err := run(cfg); err != nil { fmt.Fprintln(os.Stderr, "error:", err); return 1 }
          return 0
      }
      ```
      Move `cmd/jobreport/main_test.go` → `internal/jobreport/cli_test.go`
      (`package jobreport`). **Verify:** `go test ./internal/jobreport/` passes.
- [x] A3. Replace `cmd/jobreport/main.go` with a thin wrapper:
      ```go
      package main
      import ("os"; "github.com/rachlenko/gitlab-procs-exporter/internal/jobreport")
      func main() { os.Exit(jobreport.Main(os.Args[1:])) }
      ```
      Confirm the module path prefix matches `go.mod` (use the real module path).
      **Verify:** `go build ./cmd/jobreport && go test ./... ` green; running
      `go run ./cmd/jobreport -h` prints the usage as before.
- [x] A4. Confirm no symbol the web package will need stays unexported-and-unreachable.
      The web package only needs `jobreport.Main`. Everything else stays package-private.
      **Verify:** `make build && make test` green; commit Phase A.

## Phase B — Scaffold `cmd/jobreport-web` with self-exec dispatch

- [x] B1. Create `cmd/jobreport-web/main.go` (`package main`) that dispatches:
      if `len(os.Args) > 1 && os.Args[1] == "report"`, call
      `os.Exit(jobreport.Main(os.Args[2:]))` (the binary acts AS jobreport);
      otherwise parse web flags and start the server. Web flags (with env
      fallback): `-addr` (default `:8088`, env `JOBREPORT_WEB_ADDR`), `-store`
      (default `./jobreport-web-urls.json`, env `JOBREPORT_WEB_STORE`).
      **Verify:** `go build ./cmd/jobreport-web` compiles; running it with
      `report -h` prints jobreport usage (proves self-exec dispatch).
- [x] B2. Add `internal/jobreport` import and a minimal `server.go` with a
      `Server` struct holding the store path and self-binary path
      (`selfPath string`, default `os.Args[0]`), plus a `routes()` returning an
      `*http.ServeMux`. Stub the three handlers to return 200 for now.
      **Verify:** `go build ./cmd/jobreport-web` compiles.

## Phase C — Prometheus URL store (JSON persistence)

- [x] C1. `cmd/jobreport-web/store.go`: `PrometheusStore` with
      `Load(path) ([]string, error)` (missing file → empty slice, no error),
      `Add(path, url string) ([]string, error)` (validate http(s) via
      `url.Parse`, dedupe, append, write JSON atomically via temp file+rename).
      Annotate file reads/writes with `//nolint:gosec` + reason.
- [x] C2. `cmd/jobreport-web/store_test.go`: table tests — missing file returns
      empty; Add persists and is reloadable; duplicate URL is not added twice;
      non-http(s) URL is rejected with an error. **Verify:**
      `go test ./cmd/jobreport-web/` passes.

## Phase D — UTC time-window builder

- [ ] D1. `cmd/jobreport-web/window.go`: `buildWindow(startDate, startHour,
      startMin, endDate, endHour, endMin string) (window string, err error)`.
      Rules: if ALL six are empty → return `""` (caller omits `-window`, jobreport
      defaults to 10m). If both start and end groups are fully provided → parse as
      UTC (`time.Date(..., time.UTC)`, date `2006-01-02`, hour/min ints) and
      return `"<startEpoch>..<endEpoch>"`. Partial input, bad format, or end ≤
      start → return a descriptive error.
- [ ] D2. `cmd/jobreport-web/window_test.go`: all-empty→`""`; full valid→correct
      epoch range (assert exact epochs for a known UTC datetime); partial→error;
      end-before-start→error; bad date→error. **Verify:**
      `go test ./cmd/jobreport-web/` passes.

## Phase E — Self-exec runner

- [ ] E1. `cmd/jobreport-web/runner.go`: `runReport(selfPath, promURL, jobID,
      window string) (output string, err error)`. Validate `jobID` matches
      `^\d+$` when non-empty (else error). Build args:
      `["report", "-prom", promURL]`, append `-job-id <id>` if set, append
      `-window <window>` if set. Run `exec.Command(selfPath, args...)` with a
      context timeout (~90s), capture combined stdout+stderr, return as string
      (return output even on non-zero exit so the user sees the error text).
      `//nolint:gosec` on the exec with reason (args are validated, no shell).
- [ ] E2. `cmd/jobreport-web/runner_test.go`: write a tiny fake script to a temp
      file (e.g. a shell script that echoes its args and exits 0), point
      `selfPath` at it, and assert: args are constructed correctly (job-id/window
      omitted when empty, included when set); output is captured; invalid job id
      (`abc`) returns an error before exec. **Verify:**
      `go test ./cmd/jobreport-web/` passes.

## Phase F — Embedded assets, templates, handlers

- [ ] F1. Add `cmd/jobreport-web/static/htmx.min.js` (vendored htmx ~1.9.x, a real
      file) and `cmd/jobreport-web/templates/{index.html,report.html,urls.html}`.
      `index.html`: Prometheus `<select id="prom">` populated from the store + a
      text input and "Add URL" button (`hx-post="/prometheus"`,
      `hx-target="#prom"`, `hx-swap="outerHTML"`); a job-id text input; a
      "time window (UTC)" block with start {date,hour,minute} and end
      {date,hour,minute} inputs; a "Report" button (`hx-post="/report"`,
      `hx-target="#results"`, `hx-swap="innerHTML"`, `hx-include` the form); and a
      `<div id="results">`. Load htmx from `/static/htmx.min.js`.
      `report.html`: a `<pre>{{.}}</pre>` fragment (auto-escaped).
      `urls.html`: the `<select id="prom">…<option>…</select>` fragment.
- [ ] F2. `cmd/jobreport-web/assets.go`: `//go:embed templates/*.html static/*`
      into an `embed.FS`; parse templates once at startup; expose helpers to
      render the index page, the urls fragment, and the report fragment.
      **Verify:** `go build ./cmd/jobreport-web` compiles (embed paths resolve).
- [ ] F3. Implement handlers in `server.go`:
      `GET /` → render index with stored URLs.
      `GET /static/...` → serve embedded static via `http.FileServer`.
      `POST /prometheus` → read `url` form field, `store.Add`, return urls.html
      fragment (or an error fragment on invalid URL).
      `POST /report` → read `prom`, `job_id`, six window fields; `buildWindow`;
      on window error return an error fragment; else `runReport` and return
      report.html with the raw output. Set security headers
      (`X-Content-Type-Options: nosniff`).
- [ ] F4. `cmd/jobreport-web/server_test.go` (httptest): `GET /` returns 200 and
      contains the form and the htmx script tag; `POST /prometheus` with a valid
      URL persists it and the response contains the new `<option>`; `POST /report`
      with a fake `selfPath` returns a `<pre>` containing the fake output; window
      validation error returns a visible error fragment, not a 500. **Verify:**
      `go test ./cmd/jobreport-web/` passes.

## Phase G — Integration, build target, docs, final gate

- [ ] G1. Add a Makefile target/rule so `make build` also builds
      `cmd/jobreport-web` into `.bin/jobreport-web` (mirror the existing jobreport
      build). **Verify:** `make build` produces `.bin/jobreport-web`.
- [ ] G2. Add `cmd/jobreport-web/README.md`: what it is, flags/env, how the
      single-binary self-exec works, the embedded-assets note, and the
      internal-tool/SSRF caveat (do not expose publicly; the backend connects to
      the user-supplied Prometheus URL). Link it from the top-level README.
- [ ] G3. Manual smoke test: start `.bin/jobreport-web -addr :8099` in the
      background; `curl -s localhost:8099/` shows the form; `curl -s -X POST
      localhost:8099/prometheus -d 'url=https://example.test/'` returns the
      updated dropdown and the store file now contains the URL; `curl -s -X POST
      localhost:8099/report -d 'prom=https://example.test/&job_id=123'` returns a
      `<pre>` (report or a clean Prometheus error — both acceptable, proves the
      pipeline runs). Stop the server. **Verify:** all three curls behave as
      described.
- [ ] G4. Final gate: `gofmt -l` clean for new dirs; `make build && make lint &&
      make test` all green. Update the top-level README feature list to mention
      jobreport-web. Then output `ALL_TASKS_DONE`.
