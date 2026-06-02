# jobreport

A one-shot CLI, shipped alongside **gitlab-procs-exporter**, that renders the
exporter's **top-N process tables** (CPU / peak-RSS / IO, with full CI job
metadata) straight from Prometheus — plus a **job-URL mode** that parses a GitLab
job-log artifact and scopes the report to that job's window and node.

All connection details are **parameters** — there is no host, IP, or credential
baked into the binary. Every flag has an environment-variable fallback, so in a
GitLab pipeline you set the values once in the job environment and call
`jobreport` with no arguments.

## Parameters

| Flag       | Env var           | Default                  | Meaning                                                                 |
|------------|-------------------|--------------------------|------------------------------------------------------------------------|
| `-prom`    | `PROMETHEUS_URL`  | `http://localhost:9090`  | Prometheus base URL.                                                    |
| `-proc`    | `JOBREPORT_PROC`  | `all`                    | Process-name filter, or `all` for no filter.                           |
| `-node`    | `JOBREPORT_NODE`  | `all`                    | `node_ip` filter, or `all` for no filter.                              |
| `-window`  | `JOBREPORT_WINDOW`| `10m` (last 10 minutes)  | **Optional.** `10m\|1h\|2d` (relative) or `<startEpoch>..<endEpoch>` (absolute UTC). |
| `-top`     | `JOBREPORT_TOP`   | `5`                      | Rows per table.                                                         |
| `-url`     | `JOBREPORT_URL`   | —                        | GitLab `job.log` artifact URL; enables URL mode.                        |
| `-job-id`  | `CI_JOB_ID`       | —                        | GitLab job id; auto-resolves the runner node from `gitlab_process_info`.|

### The time window is automatic

`-window` is optional. When omitted it defaults to **now − 10 minutes → now**,
i.e. the report covers the **last 10 minutes** of data — matching the exporter's
10-minute sliding history buffer. In URL mode the window is *calculated
automatically* from the job log's `section_*` timestamps, so you never pass it.

## Usage

```sh
# Last 10 minutes, all processes, all nodes (uses $PROMETHEUS_URL or localhost):
jobreport

# Point at a specific Prometheus and widen the window:
jobreport -prom "$PROMETHEUS_URL" -window 1h -top 10

# Scope to one process / node / absolute epoch window:
jobreport -proc java -node 10.0.1.11 -window 1780258849..1780259356

# Auto-resolve the node for a CI job and report its last 10 minutes:
jobreport -job-id "$CI_JOB_ID"

# URL mode — parse the job log, then report on its exact window/node:
jobreport -url "$JOB_LOG_URL"
jobreport "$JOB_LOG_URL"          # bare URL is shorthand for -url
```

## Integrating with GitLab CI pipelines

Because every parameter falls back to an environment variable, and GitLab
exposes the job's identity through [predefined variables](https://docs.gitlab.com/ee/ci/variables/predefined_variables.html)
(`CI_JOB_ID`, `CI_JOB_NAME`, `CI_PROJECT_NAME`, `CI_PIPELINE_ID`, …), the binary
reads what it needs straight from the **job environment**.

Set `PROMETHEUS_URL` once as a project/group CI/CD variable, then add a job that
runs after your real work. `CI_JOB_ID` is provided automatically by GitLab, so
`jobreport` resolves *this job's* runner node and reports its **last 10 minutes**
with no window argument:

```yaml
# .gitlab-ci.yml
variables:
  # Set in Settings → CI/CD → Variables (do not hard-code an endpoint here).
  PROMETHEUS_URL: "$PROMETHEUS_URL"

stages:
  - build
  - report

build-app:
  stage: build
  script:
    - ./run-the-real-workload.sh

# Runs even if the build fails, so you capture the resource profile of failures.
process-report:
  stage: report
  when: always
  script:
    # CI_JOB_ID / PROMETHEUS_URL come from the job environment; the time window
    # defaults to the last 10 minutes — nothing else to pass.
    - jobreport -job-id "$CI_JOB_ID"
```

Common variations, all driven by the job environment:

```yaml
# Profile only the build stage's process by name, over the last hour:
- jobreport -proc "$CI_JOB_NAME" -window 1h

# After a failed job, parse its log artifact and report the exact window/node:
- jobreport -url "$JOB_LOG_URL"     # JOB_LOG_URL = presigned artifact link

# Pin the runner node explicitly when you export it from the runner:
- jobreport -node "$RUNNER_NODE_IP"
```

If `jobreport` isn't baked into the CI image, fetch the release binary in a
`before_script` (it is stdlib-only and statically linked):

```yaml
before_script:
  - curl -fsSL "$JOBREPORT_DOWNLOAD_URL" -o /usr/local/bin/jobreport
  - chmod +x /usr/local/bin/jobreport
```

## Build

From the repository root:

```sh
make build-jobreport          # -> .bin/jobreport
make build-jobreport-linux    # -> .bin/jobreport-linux-amd64 (static, for CI images)
```

Or directly: `go build -o jobreport ./cmd/jobreport`.

## Test

```sh
go test ./cmd/jobreport/
```

`testdata/job.log` is a failed-job fixture (k8s executor) used by the log-parser
tests.
