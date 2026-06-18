# jobreport-web

A tiny web UI, shipped alongside **gitlab-procs-exporter**, that runs
[`jobreport`](../jobreport/README.md) from the browser: pick a Prometheus URL,
type a job id, optionally choose a UTC time window, hit **Report**, and see the
raw report output rendered in place.

The frontend is [htmx](https://htmx.org) — small forms that POST and swap HTML
fragments, so there is no JavaScript build step and no SPA framework.

## Single self-contained binary

Everything ships in **one** binary:

* The HTML templates, `htmx.min.js`, and CSS are compiled in via Go's `embed`
  directive — there are no external assets to deploy.
* The report itself is produced by the **same** binary re-exec'ing itself
  (self-exec). When the first argument is `report`, the process behaves exactly
  as the `jobreport` CLI; otherwise it starts the web server. So a single file on
  disk is both the server and the report engine — there is nothing else to
  install next to it.

```bash
# Build it (also produced by `make build` → .bin/jobreport-web)
go build -o jobreport-web ./cmd/jobreport-web

# Run the server
./jobreport-web -addr :8088

# The exact same binary, invoked as `report`, IS jobreport:
./jobreport-web report -h
```

## Install with `go install`

Because it is a self-contained `package main` (assets embedded via `go:embed`),
`go install` builds and installs it straight to `$GOBIN` (or `$GOPATH/bin`):

```bash
# Pinned to the main branch (use this until a release tag includes jobreport-web):
go install github.com/rachlenko/gitlab-procs-exporter/cmd/jobreport-web@main

# Once a tag >= the first release containing this package exists, @latest works too:
go install github.com/rachlenko/gitlab-procs-exporter/cmd/jobreport-web@latest

# Then run it (ensure $GOBIN / $(go env GOPATH)/bin is on your PATH):
jobreport-web -addr :8088
```

Note: `@latest` resolves to the newest **semver tag**. The latest tag (`v0.0.10`)
predates this package, so `@latest` currently fails with "found (v0.0.10), but
does not contain package …/cmd/jobreport-web" — use `@main` (or `@<commit>`) until
a newer tag is cut. From a checkout, `go install ./cmd/jobreport-web` always works.

## Flags & environment

| Flag     | Env var               | Default                       | Meaning                                            |
|----------|-----------------------|-------------------------------|----------------------------------------------------|
| `-addr`  | `JOBREPORT_WEB_ADDR`  | `:8088`                       | HTTP listen address.                               |
| `-store` | `JOBREPORT_WEB_STORE` | `./jobreport-web-urls.json`   | Path to the Prometheus URL store (JSON) file.      |

The Prometheus URLs you add through the **Add URL** button are persisted to the
store file (written atomically) and reloaded into the dropdown on the next page
load.

## Routes

| Method & path     | Purpose                                                              |
|-------------------|---------------------------------------------------------------------|
| `GET /`           | The main page: Prometheus dropdown, job-id input, UTC window inputs. |
| `GET /static/...` | Embedded static assets (`htmx.min.js`, CSS).                         |
| `POST /prometheus`| Add a Prometheus URL to the store; returns the refreshed dropdown.   |
| `POST /report`    | Build the optional UTC window, run the report, return a `<pre>`.     |

Validation problems (bad URL, malformed window, bad job id) come back as a
visible error fragment with HTTP 200 so htmx shows the message in place rather
than a blank 500.

## Security caveat — internal tool only

> [!WARNING]
> **Do not expose jobreport-web on the public internet.** The server takes a
> user-supplied Prometheus URL and makes the backend connect to it — that is a
> server-side request forgery (SSRF) primitive by design. There is no
> authentication, and the report runs an exec of this binary on the host.

Run it on a trusted, access-controlled network (e.g. behind a VPN, an
authenticating reverse proxy, or bound to localhost) and only let operators you
trust reach it. The HTTP server intentionally omits read/write timeouts (`G114`)
because it is meant for this internal, single-operator use.
