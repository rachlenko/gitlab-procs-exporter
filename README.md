# GitLab Process History Exporter

[![CI Status](https://github.com/rachlenko/gitlab-procs-exporter/actions/workflows/ci.yml/badge.svg)](https://github.com/rachlenko/gitlab-procs-exporter/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/rachlenko/gitlab-procs-exporter)](https://goreportcard.com/report/github.com/rachlenko/gitlab-procs-exporter)
[![Go Reference](https://pkg.go.dev/badge/github.com/rachlenko/gitlab-procs-exporter.svg)](https://pkg.go.dev/github.com/rachlenko/gitlab-procs-exporter)
[![Docker Image](https://img.shields.io/badge/docker%20image-compatible-blue.svg?logo=docker)](https://github.com/rachlenko/gitlab-procs-exporter/pkgs/container/gitlab-procs-exporter)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A highly optimized, cross-platform Prometheus exporter designed to track and retain Linux process-level telemetries (CPU, memory, and I/O) along with command-line arguments and environment variables inside an in-memory sliding-window history store.

It serves standard `/metrics` for Prometheus scraping, provides REST APIs (`/api/processes` and `/api/history`) for JSON timeline retrieval, and embeds a premium glassmorphic dark-mode single-page dashboard at `/`.

---

## Historical Context & CI/CD Diagnostics

Although named `gitlab-procs-exporter` for historical reasons, this tool is fully generic and highly effective across any CI/CD pipeline infrastructure (e.g., GitLab CI, GitHub Actions, Jenkins, etc.).

### Diagnosing Hung CI Jobs
The primary motivation behind this project is to diagnose stuck or hung CI/CD pipeline jobs that fail to complete for unclear reasons. When a job hits its timeout limit, developers often lack high-resolution, granular system diagnostics (CPU, Memory, Disk I/O, environment states) at the exact moment of failure.

To solve this, before the pipeline runner forcefully terminates the job, the workflow can programmatically query the Prometheus server. Thanks to the fact that standard environment variables like `CI_JOB_ID` and `CI_JOB_NAME` are injected directly into the running process environment (and thus tracked by this exporter), the query will precisely isolate and return the high-resolution timeline telemetry for only the specific stuck process out of all concurrently running runner processes. Once this diagnostic packet is archived or queried, the job can be safely terminated.

> [!NOTE]
> **Operational Storage Warning**: Transmitting high-resolution process telemetry to Prometheus introduces a larger data footprint. Operating teams must ensure that their Prometheus storage retention and compression policies are correctly configured to prevent high-cardinality storage growth or overflow. Managing Prometheus TSDB storage retention is outside the scope of this project.

---

## Features

*   **Sliding 10-Minute History Store**: Caches metrics and metadata (even for processes that have exited) for exactly 10 minutes, solving a major blind spot in transient process tracking.
*   **Security Redaction Engine**: Automatically redacts environment variable values containing sensitive terms like `key`, `pass`, `token`, `secret`, `url`, `api`, etc., before exposing them — both on the `/metrics` endpoint and in the `/api/processes` / `/api/history` JSON responses.
*   **Bounded Label Contract**: Every emitted label value is byte-capped by an explicit, documented, operator-overridable contract (see [Label size contract](#label-size-contract)), with a deterministic truncation marker that carries the original length and a `sha256` fingerprint, and a counter so a cut is never silent.
*   **Self-Contained Executable**: Embeds the frontend Single Page Application (SPA) dashboard within the compiled Go binary using the standard Go `embed` directive.
*   **Cross-Platform Telemetry**: Utilizes `gopsutil` to parse `/proc` natively on Linux and system APIs on macOS.
*   **`jobreport` CLI & `jobreport-web` UI**: A companion one-shot CLI ([`cmd/jobreport`](cmd/jobreport/README.md)) renders top-N process tables straight from Prometheus, and a single self-contained htmx web app ([`cmd/jobreport-web`](cmd/jobreport-web/README.md)) runs it from the browser against a chosen Prometheus URL, job id, and UTC time window.

---

## 1. Running the Exporter

### Install from the `.deb` release
Download the `.deb` for your architecture from the
[latest release](https://github.com/rachlenko/gitlab-procs-exporter/releases/latest)
and install it (the binary lands in `/usr/bin`):
```bash
curl -fsSLO https://github.com/rachlenko/gitlab-procs-exporter/releases/latest/download/gitlab-procs-exporter_<ver>_linux_amd64.deb
sudo dpkg -i gitlab-procs-exporter_<ver>_linux_amd64.deb
# (dpkg -i needs a local file, not a URL; `sudo apt-get install -y ./<file>.deb` also works and resolves deps)
gitlab-procs-exporter --version
```
The dashboard SPA is embedded in the binary, so no extra assets are needed.

### Local Compilation
```bash
go build -o gitlab-procs-exporter
```

### Bootstrapping on a Host
The binary manages its own systemd service.

*   **Check dependencies** — verifies a downloader (`curl` or `wget`) is present
    (needed by `--update`):
    ```bash
    gitlab-procs-exporter --check-dependencies
    ```
*   **Install as a systemd service** (Linux, run as root). Writes a unit whose
    `ExecStart` points at the installed binary, then enables and starts it:
    ```bash
    sudo gitlab-procs-exporter --deploy-as-systemd-service --port=8000 --interval=1m
    ```
    Tunable: `--service-name`, `--service-user` (default `root`, required to read
    every process's environment and I/O), `--port`, `--interval`.

*   **Update** (Linux, run as root). Downloads the latest release `.deb` from
    GitHub, installs it with `dpkg`, and restarts the service:
    ```bash
    sudo gitlab-procs-exporter --update
    ```

*   **Uninstall** (Linux, run as root). Stops/disables the service, removes its
    unit file, and removes the dpkg package (the binary) via `dpkg -r`:
    ```bash
    sudo gitlab-procs-exporter --uninstall
    ```

### Changing the Collection Frequency
You can control the interval at which the background scraper sweeps system processes using the `--interval` command-line flag. 

*   **High Resolution (1 Minute Interval)**:
    ```bash
    ./gitlab-procs-exporter --port=8000 --interval=1m
    ```
*   **Low Resolution / Resource Saving (5 Minute Interval)**:
    ```bash
    ./gitlab-procs-exporter --port=8000 --interval=5m
    ```

*Note: Accessing full I/O statistics and other users' environment variables requires root privileges. Run with `sudo` in production:*
```bash
sudo ./gitlab-procs-exporter --port=8000 --interval=1m
```

### `jobreport-web` — browser UI for on-demand reports

[`jobreport-web`](cmd/jobreport-web/README.md) is a separate, single self-contained
binary (HTML/htmx/CSS embedded via `go:embed`; the report engine runs by the binary
self-exec'ing). Install it straight from the module with `go install`:

```bash
# Latest release tag (>= v0.0.11, the first tag that contains the package):
go install github.com/rachlenko/gitlab-procs-exporter/cmd/jobreport-web@latest

# Or pin to the main branch / a specific commit:
go install github.com/rachlenko/gitlab-procs-exporter/cmd/jobreport-web@main

# Run it (ensure $(go env GOPATH)/bin is on your PATH), then open http://localhost:8088
jobreport-web -addr :8088
```

From a checkout you can also `go install ./cmd/jobreport-web` or `make build`
(produces `.bin/jobreport-web`). See its [README](cmd/jobreport-web/README.md) for
flags (`-addr`, `-store`, `-debug`) and the internal-tool/SSRF caveat.

---

## Metrics reference

Everything below is served on `GET /metrics` (default port `8000`) in the
Prometheus text exposition format.

### Per-process metrics (always exported)

One series **per active process**, refreshed every `--interval`. All carry the
labels `pid` (process id), `name` (process comm) and the four `ci_*` labels
(`ci_job_id`, `ci_job_name`, `ci_project_path`, `ci_pipeline_id`, read from the
process environment and empty for non-CI processes). Every one of those values
is size-bounded — see [Label size contract](#label-size-contract).

| Metric | Type | Unit | Meaning |
|--------|------|------|---------|
| `gitlab_process_cpu_seconds_total` | counter | seconds | Cumulative user+system CPU time consumed by the process. |
| `gitlab_process_resident_memory_bytes` | gauge | bytes | Resident set size (RSS). |
| `gitlab_process_virtual_memory_bytes` | gauge | bytes | Virtual memory size (VMS). |
| `gitlab_process_io_read_bytes_total` | counter | bytes | Bytes read from disk by the process **and every descendant it has reaped**. |
| `gitlab_process_io_write_bytes_total` | counter | bytes | Bytes written to disk by the process **and every descendant it has reaped**. |
| `gitlab_process_self_io_read_bytes_total` | counter | bytes | Bytes read from disk by the process's own threads. |
| `gitlab_process_self_io_write_bytes_total` | counter | bytes | Bytes written to disk by the process's own threads. |
| `gitlab_process_io_read_syscalls_total` | counter | syscalls | `read(2)` calls by the process **and every descendant it has reaped**. |
| `gitlab_process_io_write_syscalls_total` | counter | syscalls | `write(2)` calls by the process **and every descendant it has reaped**. |
| `gitlab_process_self_io_read_syscalls_total` | counter | syscalls | `read(2)` calls by the process's own threads. |
| `gitlab_process_self_io_write_syscalls_total` | counter | syscalls | `write(2)` calls by the process's own threads. |
| `gitlab_process_info` | gauge | `1` | Metadata-only series; the value is always `1` and the data lives in its labels. |

#### Which I/O metric to use

Linux folds a child's I/O accounting into its parent when the parent reaps it,
and the fold is recursive. `/proc/<pid>/io` — the source of the two
`gitlab_process_io_*` counters — therefore reports, for any long-lived reaper,
the I/O of every process that ever exited beneath it. On a CI node `pid 1` and
the job shells top a `topk()` over write bytes while never having touched the
disk; summing the counter across processes double-counts, because bytes sit in
a live child's counter and, once it exits, in its parent's too.

The `gitlab_process_self_io_*` counters sum `/proc/<pid>/task/<tid>/io` over the
live threads instead, which carries no such inheritance. Use them to answer
**who is doing the I/O**:

```promql
topk(10, sum by (name) (rate(gitlab_process_self_io_write_bytes_total[5m])))
```

Their blind spot is the mirror image: a process that lives and dies between two
scrapes is never sampled, and the bytes it wrote appear in no `self_` series at
all. On a busy CI node that can be most of the traffic. So for **how much I/O
happened in total** — a whole job, a whole node — keep using the process-wide
counter on the root of the tree (the job's shell, or `pid 1`), which by then has
absorbed everything below it. Never `sum()` either family across a process tree.

#### Syscalls are not IOPS

`*_io_{read,write}_syscalls_total` expose `/proc`'s `syscr` / `syscw`: the number
of `read(2)` and `write(2)` calls the process made. They are **not** block-device
IOPS. The page cache merges and splits those calls on the way to the disk, and
the kernel never attributes a block operation back to the process whose page it
is flushing — a `write()` that returns instantly may cost the disk nothing now
and one operation later, via `kworker`. Read them as a process's *I/O call rate*
(chatty small reads vs. few large ones), and take true device IOPS from
node-exporter's `node_disk_{reads,writes}_completed_total`.

`gitlab_process_info` carries three extra labels beyond `pid`/`name`:

- `cmdline` — the process command line, capped at 2048 bytes (see
  [Label size contract](#label-size-contract)).
- `environ` — the process environment as a single `KEY=VALUE, KEY2=VALUE2`
  string, **sorted by key** (stable across scrapes). Secret-looking entries are
  rendered as `KEY=[REDACTED]` and the whole label is size-bounded (see
  [Hardened environ scrubbing](#hardened-environ-scrubbing)).
- `environ_truncated` — `"1"` when the emitted `environ` variable **list is
  incomplete** (one or more variables were dropped entirely because there were
  more than 100 variables or the total-size ceiling was reached), otherwise
  `"0"`. It is **not** set by per-value `[REDACTED]` or per-value truncation —
  those keep the variable present, so the list is still complete.

Example exposition:

```
# HELP gitlab_process_resident_memory_bytes Resident set size (RSS) in bytes.
# TYPE gitlab_process_resident_memory_bytes gauge
gitlab_process_resident_memory_bytes{pid="4567",name="sidekiq"} 2.097152e+08
# HELP gitlab_process_info Metadata about the process ... (scrubbed for secrets).
# TYPE gitlab_process_info gauge
gitlab_process_info{pid="4567",name="sidekiq",cmdline="sidekiq -c 10",environ="CI_JOB_NAME=build, DB_PASSWORD=[REDACTED], HOME=/root",environ_truncated="0"} 1
```

### Label size contract

Every label value the exporter emits is bounded, and the bound is part of the
**input-data contract**: no matter what a scraped process puts in its `comm`,
its argv or its environment, the value that reaches Prometheus stays within the
limit below. This is the exporter's own cap — Prometheus's
`label_value_length_limit` defaults to `0` (unlimited), so nothing downstream
enforces it for you.

| Label | Limit (bytes) | On | Notes |
|---|---|---|---|
| `name` | 128 | all 12 per-process metrics | Observed max 39; headroom for long kthread names. |
| `ci_job_name` | 256 | all 12 per-process metrics | `parallel:matrix` jobs embed matrix values. |
| `ci_project_path` | 256 | all 12 per-process metrics | `group/subgroup/…/project` nesting. |
| `ci_job_id` | 32 | all 12 per-process metrics | Numeric. |
| `ci_pipeline_id` | 32 | all 12 per-process metrics | Numeric. |
| `cmdline` | 2048 | `gitlab_process_info` | `ARG_MAX` can reach 2 MB; this cap **is** hit in practice. |
| `environ` | 8192 | `gitlab_process_info` | Composed blob with its own three-way bound — see [Hardened environ scrubbing](#hardened-environ-scrubbing). |
| `job_name` | 256 | `kuber_cpu_request`, `kuber_memory_request` | In-cluster only. Same `CI_JOB_NAME` source as `ci_job_name`, so it shares that limit and follows a `ci_job_name` override. Its cuts are counted under `label="ci_job_name"`, not under a `job_name` series — that is the limit being applied, and a separate series would imply a separate limit to tune. |

> **The four `ci_*` limits are estimates, not measurements.** They were derived
> on an audit host that was running no CI jobs, so every `ci_*` value observed
> was empty. Job names and project paths are long-tailed (`parallel:matrix`
> values, deep subgroup nesting), so re-audit against a real runner over a
> 7-day window and raise them via `max_label_bytes` if
> `gitlab_exporter_label_truncations_total{label=~"ci_.*"}` moves. The `name`,
> `cmdline` and `environ` limits *are* measured against production data.

Limits are **bytes of a single label value**, never runes: Prometheus charges
per label value and per series, and its index memory is byte-oriented.
`pid` and `environ_truncated` are exporter-generated and structurally bounded,
so they carry no entry.

The five labels on all 12 metrics are the expensive ones — an oversized value
there multiplies 12× into the TSDB index, whereas `cmdline`/`environ` ride on
`gitlab_process_info` alone.

#### Truncation marker

A value over its limit is cut **on a rune boundary** (the result is always valid
UTF-8) and gets a marker carrying the **original** byte length and a fingerprint
of the **original** value:

```
<surviving prefix>…[len=<N>;sha256=<first 12 hex chars of sha256(original)>]
```

```
# a 4096-byte cmdline cut at the 2048-byte limit (surviving prefix elided here)
gitlab_process_info{name="ruby",cmdline="bundle exec sidekiq …[len=4096;sha256=3f70d00f41ba]",…} 1
```

The fingerprint is what makes truncation reversible in practice: given a suspect
series you can hash candidate values and confirm the match, which a bare
`[TRUNCATED]` marker made impossible. Truncation is **deterministic** — identical
input always yields an identical label value, so a long-lived process does not
churn series across scrapes. A truncated value is at most
`limit + 49` bytes (49 = the marker's worst case).

**Cardinality trade-off:** the marker *preserves* distinctness, so two different
over-long values stay two series. The old `[TRUNCATED]` marker *collapsed* them
into one. This is deliberate — the contract optimises for size and traceability,
not for collapsing cardinality.

#### Observing truncation

Every cut increments a counter, so truncation is never silent:

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `gitlab_exporter_label_truncations_total` | counter | `label` | Label values cut by the contract, by label name. |

**Read it as a yes/no signal, not a count of distinct values.** The counter is
incremented per *gather*: a scrape that cuts N label values adds N, so a single
long-lived process with an over-long `cmdline` keeps adding on every scrape, the
rate scales with scrape frequency, and a second Prometheus (or a `curl /metrics`
while debugging) inflates it. "Is anything being cut, and which label" is the
question it answers well.

The six overridable contract labels are pre-initialised at `0`, so an absent
series means "this exporter does not instrument that label", not "nothing was
truncated"; an alert on it cannot silently never fire. Two labels are bounded
but not counted here: `environ`, which is bounded by its own three-way rule
rather than by the contract table, and the in-cluster `job_name`, which rides on
a separate collector.

```promql
# Labels being cut on this host, and how often
rate(gitlab_exporter_label_truncations_total[1h]) > 0
```

A sustained non-zero rate means the limit for that label is too low for this
host's data; raise it with `max_label_bytes` (see
[Configuration file](#configuration-file)).

#### Defence in depth in the scrape config

The cap above is self-imposed, and a label added in a future version could miss
the table. Bound it from the Prometheus side too:

```yaml
scrape_configs:
  - job_name: gitlab-procs
    label_value_length_limit: 8192
    label_limit: 32
```

Under the **Prometheus Operator** — the recommended in-cluster path, see
[deploy/k8s/README.md](deploy/k8s/README.md) — the equivalent fields live on the
`ServiceMonitor` itself and are spelled differently:

```yaml
spec:
  labelValueLengthLimit: 8192
  labelLimit: 32
```

⚠️ `label_value_length_limit` / `labelValueLengthLimit` must stay **at or above**
the exporter's `environ` ceiling of 8192. Set it lower and Prometheus rejects
the **whole scrape**, not just the offending label. That ceiling
(`maxEnvironBytes`) is a build-time constant and is *not* overridable via
`max_label_bytes`, so lowering the scrape limit below 8192 requires a rebuild.

### Kubernetes job-resource metrics (only in-cluster)

Exported **only** when the exporter runs inside a Kubernetes cluster (see
[Kubernetes job-resource metrics](#kubernetes-job-resource-metrics) for the
preconditions). One series **per unique `job_name`** seen on the node.

| Metric | Type | Unit | Label | Meaning |
|--------|------|------|-------|---------|
| `kuber_cpu_request` | gauge | cores | `job_name` | Sum of the job pod's container CPU **requests** (e.g. `500m` → `0.5`). |
| `kuber_memory_request` | gauge | bytes | `job_name` | Sum of the job pod's container memory **requests** (e.g. `512Mi` → `5.36870912e+08`). |

`job_name` is the value of the `CI_JOB_NAME` environment variable of the job's
process — **not** the Prometheus `job` label (which Prometheus injects from your
scrape config). Filter with `job_name="build"`, never `job="build"`.

```
# HELP kuber_cpu_request CPU request of the GitLab CI job pod, in cores.
# TYPE kuber_cpu_request gauge
kuber_cpu_request{job_name="build"} 0.5
# HELP kuber_memory_request Memory request of the GitLab CI job pod, in bytes.
# TYPE kuber_memory_request gauge
kuber_memory_request{job_name="build"} 5.36870912e+08
```

---

## 2. Prometheus Configuration (`prometheus.yml`)

Add the exporter as a static target in your Prometheus configuration. Align the scraping interval (`scrape_interval`) with your exporter's internal collection frequency.

### Example: 1-Minute Scrape Period
```yaml
global:
  scrape_interval: 1m         # Scrape targets every 1 minute by default
  evaluation_interval: 1m

scrape_configs:
  - job_name: 'gitlab-procs-exporter'
    static_configs:
      - targets: ['GITLAB_WORKER_IP_ADDRESS:8000'] # Replace with your exporter IP/hostname
```

### Example: 5-Minute Scrape Period
```yaml
global:
  scrape_interval: 5m         # Scrape targets every 5 minutes by default
  evaluation_interval: 5m

scrape_configs:
  - job_name: 'gitlab-procs-exporter'
    static_configs:
      - targets: ['GITLAB_WORKER_IP_ADDRESS:8000']
```

Consider adding `label_value_length_limit: 8192` and `label_limit: 32` to the
scrape job as defence in depth — the exporter's own size caps are self-imposed.
See [Label size contract](#label-size-contract) for the limits and the caveat
about setting `label_value_length_limit` too low.

---

## 3. Remote PromQL Queries

You can execute these queries in the Prometheus Web UI or Grafana to query the last 10 minutes of historical metrics for processes matching a specific `pid` (e.g., `CI_JOB_ID`) and `name` (e.g., `CI_JOB_NAME`):

| Target Metric | PromQL Expression (Last 10m Timeline) | Output Unit |
| :--- | :--- | :--- |
| **CPU Usage** | `rate(gitlab_process_cpu_seconds_total{pid="CI_JOB_ID", name="CI_JOB_NAME"}[10m])` | Cores utilized |
| **Memory (RSS)** | `gitlab_process_resident_memory_bytes{pid="CI_JOB_ID", name="CI_JOB_NAME"}[10m]` | Bytes |
| **Memory (VMS)** | `gitlab_process_virtual_memory_bytes{pid="CI_JOB_ID", name="CI_JOB_NAME"}[10m]` | Bytes |
| **Disk Read Rate** | `rate(gitlab_process_self_io_read_bytes_total{pid="CI_JOB_ID", name="CI_JOB_NAME"}[10m])` | Bytes / Sec |
| **Disk Write Rate**| `rate(gitlab_process_self_io_write_bytes_total{pid="CI_JOB_ID", name="CI_JOB_NAME"}[10m])` | Bytes / Sec |

### Querying the Prometheus HTTP API via `curl`

To extract these values programmatically in a JSON timeline from your desktop terminal:

```bash
# Query Memory RSS History over the last 10 minutes
curl -G -s "http://PROMETHEUS_IP_ADDRESS:9090/api/v1/query" \
  --data-urlencode "query=gitlab_process_resident_memory_bytes{pid=\"CI_JOB_ID\",name=\"CI_JOB_NAME\"}[10m]" | json_pp
```

---

## 4. Alerting & Alertmanager Configuration

To get notified on critical process performance issues or abnormal terminations, implement these Prometheus alert rules and Alertmanager routing templates.

### A. Prometheus Alert Rules (`alert_rules.yml`)
Save this file alongside `prometheus.yml` and reference it in the `rule_files` section of your configuration.

```yaml
groups:
  - name: gitlab-process-alerts
    rules:
      # 1. Alert if a critical CI/GitLab process consumes more than 2 full cores for over 2 minutes
      - alert: HighProcessCPUUsage
        expr: rate(gitlab_process_cpu_seconds_total{name=~"puma|sidekiq|gitaly"}[1m]) > 2.0
        for: 2m
        labels:
          severity: warning
        annotations:
          summary: "High CPU usage on process {{ $labels.name }} (PID: {{ $labels.pid }})"
          description: "Process {{ $labels.name }} is using {{ $value | printf \"%.2f\" }} cores on {{ $labels.instance }}."

      # 2. Alert if a process leaks or exceeds 4GB of physical RAM
      - alert: ProcessMemoryExhausted
        expr: gitlab_process_resident_memory_bytes > 4294967296
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "Process {{ $labels.name }} (PID: {{ $labels.pid }}) exceeded 4GB RAM"
          description: "Process {{ $labels.name }} has a Resident Set Size (RSS) of {{ $value | humanize1024Bytes }}."

      # 3. Alert if a process experiences heavy Disk I/O load (exceeding 100MB/s).
      #    The self_ series are required here: the process-wide counters would
      #    page on pid 1, which inherits the I/O of everything it reaps.
      - alert: ExtremeProcessDiskIO
        expr: (rate(gitlab_process_self_io_read_bytes_total[1m]) + rate(gitlab_process_self_io_write_bytes_total[1m])) > 104857600
        for: 1m
        labels:
          severity: warning
        annotations:
          summary: "High Disk I/O throughput on {{ $labels.name }} (PID: {{ $labels.pid }})"
          description: "Combined read/write speed for {{ $labels.name }} is {{ $value | humanize1024Bytes }}/s."

      # 4. Alert if the label size contract is cutting values on this host.
      #    Treat as yes/no: the counter is incremented per gather, so the rate
      #    scales with scrape frequency and with the number of scrapers.
      #    See "Label size contract" for raising the limit via max_label_bytes.
      - alert: LabelValuesBeingTruncated
        expr: rate(gitlab_exporter_label_truncations_total[1h]) > 0
        for: 1h
        labels:
          severity: info
        annotations:
          summary: "Label {{ $labels.label }} is being truncated on {{ $labels.instance }}"
          description: "The {{ $labels.label }} limit is too low for this host's data; affected series carry a fingerprint marker instead of their full value."

      # 5. Alert if a monitored process crashes or unexpectedly exits (disappears from live metrics)
      - alert: CriticalProcessExited
        expr: absent(gitlab_process_info{name=~"sidekiq|gitaly|puma"}) == 1
        for: 30s
        labels:
          severity: page
        annotations:
          summary: "Critical process has terminated!"
          description: "A critical GitLab process matching the target query has exited or disappeared on {{ $labels.instance }}."
```

### B. Alertmanager Configuration Template (`alertmanager.yml`)
Use this configuration template to route alerts received from Prometheus to notification channels like Slack or Email:

```yaml
global:
  resolve_timeout: 5m

route:
  group_by: ['alertname', 'instance', 'name']
  group_wait: 30s
  group_interval: 5m
  repeat_interval: 4h
  receiver: 'slack-notifications'
  
  routes:
    # Route critical severity alerts to PagerDuty or emergency channels
    - match:
        severity: critical
      receiver: 'pagerduty-urgent'

receivers:
- name: 'slack-notifications'
  slack_configs:
  - api_url: 'https://hooks.slack.com/services/'
    channel: '#gitlab-alerts'
    send_resolved: true
    title: '[[.Status | toUpper]] - [{{ .CommonLabels.severity | toUpper }}] {{ .CommonAnnotations.summary }}'
    text: '{{ range .Alerts }}{{ .Annotations.description }}\n{{ end }}'

- name: 'pagerduty-urgent'
  pagerduty_configs:
  - service_key: 'YOUR_PAGERDUTY_API_SERVICE_KEY'
    send_resolved: true
```

## Configuration file

Pass `--config <path>` to supply a YAML file with extra environ redaction
rules and per-label size limits. Without the flag, the built-in secret denylist
and the default [label size contract](#label-size-contract) apply. If the
flag is given but the file is missing or malformed, the exporter logs the
error and exits (fail-fast).

```yaml
# config.example.yaml
redact_key_substrings:
  - vault
  - internal_token

max_label_bytes:
  ci_job_name: 512
  ci_project_path: 512
```

`redact_key_substrings` is a list of case-insensitive substrings. Any process
environment variable whose **name** contains one of them is shown as
`NAME=[REDACTED]` in the `gitlab_process_info` metric, in addition to the
built-in denylist and the value-shape heuristics (token prefixes, JWTs, and
long high-entropy strings).

```bash
gitlab-procs-exporter --config /etc/gitlab-procs-exporter/config.yaml
```

When installing the systemd service, pass `--config` alongside
`--deploy-as-systemd-service` and the path is baked into the unit's
`ExecStart`:

```bash
sudo gitlab-procs-exporter --deploy-as-systemd-service \
  --config /etc/gitlab-procs-exporter/config.yaml
```

### Adding sensitive-data filters

`redact_key_substrings` is how you extend redaction beyond the built-ins. Each
entry is a **case-insensitive substring matched against the variable name**; any
match renders that variable as `NAME=[REDACTED]` in `gitlab_process_info`. To
add a filter, list the substrings that identify your sensitive variables:

```yaml
redact_key_substrings:
  - vault            # hides VAULT_ADDR, VAULT_TOKEN, MY_VAULT_KEY, …
  - internal_token   # hides INTERNAL_TOKEN, SVC_INTERNAL_TOKEN, …
  - _pat             # hides GITHUB_PAT, GL_PAT, …
```

Matching is substring, not exact, so keep entries **specific**: a short fragment
like `id` would also hide `BUILD_ID` / `CI_PIPELINE_ID`. Over-redaction is
fail-safe — it only ever hides values, never leaks them — but it can remove
variables you wanted to keep, so prefer the longest unambiguous fragment.
Values that merely *look* like secrets (token prefixes such as `glpat-`/`ghp_`,
JWTs, long high-entropy strings) are already redacted by the built-in value
heuristics regardless of this list, so you only need entries for names the
built-ins miss.

### Overriding label size limits

`max_label_bytes` raises or lowers individual entries of the
[label size contract](#label-size-contract). Omitted labels keep their default:

```yaml
max_label_bytes:
  ci_job_name: 512     # long parallel:matrix job names on this runner
  name: 64             # this host has short comms; save index memory
```

Validation is **fail-fast** — a bad entry aborts startup rather than being
ignored, because a silently dropped override is a limit that quietly never
applies. Rejected:

- an **unknown label name**, including a typo (`ci_jobname`) and `environ`
  (bounded separately, not here);
- a **non-integer** value;
- a value **`<= 0`**;
- a value **below 49 bytes**, the worst-case size of the truncation marker.
  Under that floor a cut value ends up *longer* than its limit and is almost
  entirely marker. (The built-in `ci_job_id` / `ci_pipeline_id` defaults of 32
  sit below the floor on purpose: they bound ~7-byte numeric values and never
  truncate. An override has no such context, so it is held to the floor.)
- a value **above 8143 bytes**, the ceiling. A cut value carries its 49-byte
  marker *past* the limit, so anything higher can emit a label value over the
  **8192-byte** `label_value_length_limit` this exporter is deployed with — and
  Prometheus rejects the **whole scrape**, not the one value. Raising a limit is
  meant to lose *less* data; trading truncated cmdlines for every metric on the
  host is the opposite, so it fails the load instead.
- an **unknown top-level key**, including a typo (`redact_key_substring`,
  `max_label_byte`). This one matters most: a misspelled `redact_key_substrings`
  would otherwise parse cleanly, yield *no* redaction filters, and bring the
  exporter up healthy while publishing every value you asked to have scrubbed.

The error names the offending entry, so a rejected config tells you which key to
fix.

## Kubernetes job-resource metrics

When the exporter runs **inside a Kubernetes cluster** (deployed as a
DaemonSet), it additionally exports the resource requests of GitLab CI job
pods scheduled on the same node:

| Metric | Type | Unit | Labels |
|--------|------|------|--------|
| `kuber_cpu_request` | gauge | cores | `job_name` |
| `kuber_memory_request` | gauge | bytes | `job_name` |

`job_name` is taken from the `CI_JOB_NAME` environment variable of the job's
process. The exporter links a process to its pod via the pod UID in
`/proc/<pid>/cgroup`, and reads the pod's resource requests from the node-local
kubelet API (`https://$HOST_IP:10250/pods`). Outside a cluster these metrics are
simply absent and the exporter behaves exactly as before.

### How `HOST_IP` and the `nodes/proxy` RBAC fit together

The in-cluster flow is:

1. The exporter detects it is in a cluster (`KUBERNETES_SERVICE_HOST` is set and
   the ServiceAccount token file exists).
2. It enumerates the node's processes from `/proc` and reads `CI_JOB_NAME` and
   the owning pod UID (from `/proc/<pid>/cgroup`) for each.
3. It calls the **node-local kubelet** read-only API at
   `https://$HOST_IP:10250/pods` — using `HOST_IP` to address *this* node's
   kubelet — to read each pod's resource requests, presenting the
   ServiceAccount token as a `Bearer` credential.
4. The kubelet authorizes that token (Webhook auth, the kubeadm default) by
   asking the API server whether the SA may `get` the `nodes/proxy` resource.
   Without that permission the kubelet returns `401/403` and no kube metrics
   appear.

So **`HOST_IP` decides *which* kubelet to talk to**, and **`nodes/proxy` decides
*whether the kubelet answers***. Both are required.

### Complete DaemonSet manifest

This is the minimum that makes `kuber_cpu_request` / `kuber_memory_request`
appear. Note `hostPID: true` and running as root — without them the container
only sees its own PID namespace and cannot read other pods' `/proc/<pid>/environ`
or cgroup, so `job_name` and the pod link would be empty.

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: gitlab-procs-exporter
  namespace: monitoring
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: gitlab-procs-exporter-kubelet
rules:
  # Lets the kubelet authorize the SA token for GET https://<node>:10250/pods.
  - apiGroups: [""]
    resources: ["nodes/proxy"]
    verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: gitlab-procs-exporter-kubelet
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: gitlab-procs-exporter-kubelet
subjects:
  - kind: ServiceAccount
    name: gitlab-procs-exporter
    namespace: monitoring
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: gitlab-procs-exporter
  namespace: monitoring
  labels:
    app: gitlab-procs-exporter
spec:
  selector:
    matchLabels:
      app: gitlab-procs-exporter
  template:
    metadata:
      labels:
        app: gitlab-procs-exporter
      annotations:                       # optional: scrape via prometheus.io/* relabeling
        prometheus.io/scrape: "true"
        prometheus.io/port: "8000"
        prometheus.io/path: "/metrics"
    spec:
      serviceAccountName: gitlab-procs-exporter
      hostPID: true                      # see every process on the node, not just our own
      containers:
        - name: exporter
          # Multi-arch image published to ghcr.io on each release (amd64 + arm64).
          image: ghcr.io/rachlenko/gitlab-procs-exporter:v0.0.15
          args: ["--port=8000", "--interval=10s"]
          securityContext:
            runAsUser: 0                 # root — required to read other processes' environ/cgroup
            # privileged: true           # use instead of runAsUser if your PSP/PSA needs it
          ports:
            - name: metrics
              containerPort: 8000
          env:
            - name: HOST_IP              # which kubelet to query: this node's IP
              valueFrom:
                fieldRef:
                  fieldPath: status.hostIP
            # - name: NODE_NAME          # optional fallback if HOST_IP is unset
            #   valueFrom:
            #     fieldRef:
            #       fieldPath: spec.nodeName
```

Apply with `kubectl apply -f daemonset.yaml`, then verify on one pod:

```bash
kubectl -n monitoring exec ds/gitlab-procs-exporter -- \
  sh -c 'wget -qO- localhost:8000/metrics | grep kuber_'
```

### Supplying `--config` (redaction filters and label limits) in Kubernetes

Both config-file knobs work in-cluster — the redaction filters from
[Adding sensitive-data filters](#adding-sensitive-data-filters) and the
`max_label_bytes` overrides from [Label size contract](#label-size-contract).
Ship the config as a `ConfigMap`, mount it into the DaemonSet, and point
`--config` at the mounted path.

Add a ConfigMap alongside the manifest above:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: gitlab-procs-exporter-config
  namespace: monitoring
data:
  config.yaml: |
    redact_key_substrings:
      - vault
      - internal_token
    # Optional — see "Label size contract" for the overridable names and the
    # 49-byte floor. Omitted labels keep their default.
    # max_label_bytes:
    #   ci_job_name: 512
```

Then extend the DaemonSet's container with the flag + a read-only mount, and
declare the volume (the rest of the container spec — `securityContext`, `ports`,
`env` — stays as shown earlier):

```yaml
      containers:
        - name: exporter
          image: ghcr.io/rachlenko/gitlab-procs-exporter:v0.0.15
          args:
            - "--port=8000"
            - "--interval=10s"
            - "--config=/etc/gitlab-procs-exporter/config.yaml"
          volumeMounts:
            - name: config
              mountPath: /etc/gitlab-procs-exporter
              readOnly: true
      volumes:
        - name: config
          configMap:
            name: gitlab-procs-exporter-config
```

The config is read **once at startup** and is **fail-fast**: a missing or
malformed file makes the pod exit (visible as `CrashLoopBackOff` / in
`kubectl logs`). An **unknown top-level key**, an **unknown label name** under
`max_label_bytes`, a non-positive value, or a value **outside 49–8143 bytes** is
equally fatal — that is the most likely new cause of `CrashLoopBackOff` after a
ConfigMap edit, and the error in `kubectl logs` names the offending key. After editing the ConfigMap,
restart the DaemonSet so the new config takes effect:

```bash
kubectl -n monitoring rollout restart ds/gitlab-procs-exporter
```

### TLS

The node-local kubelet uses a self-signed serving certificate, so TLS
verification is **skipped by default**. Set `--kubelet-insecure=false` only if
your kubelet presents a CA-trusted certificate.

### Security / SSRF caveat

The exporter connects to the kubelet address derived from `HOST_IP` (or
`NODE_NAME`). Run it only with a trusted Downward-API-provided node address.

### Hardened environ scrubbing

The `gitlab_process_info` metric exposes process environment variables. Values
are redacted when the **key** looks sensitive (expanded denylist: tokens, certs,
SSH/GPG, JWT, sessions, cookies, DSNs, …) **or** when the **value** looks like a
secret (known token prefixes such as `glpat-`/`ghp_`/`AKIA`, JWTs, and long
high-entropy strings) — even if the key name is innocuous.

The `environ` label is also **size-bounded** so a single process carrying its
config in the environment (tens of KB) can't emit a value large enough to bloat
or fail the scrape:

- at most **100 variables** (sorted by key) are emitted;
- any single value longer than **256 bytes** is replaced **entirely** by a
  marker carrying the original byte length and nothing else —
  `[TRUNCATED;len=768]` — so the label stays valid UTF-8 and "too long" stays
  distinguishable from "secret";
- `[REDACTED]` **wins over truncation**, so a value already known to be a secret
  is named as one rather than reported as a length;
- the joined label is capped at a hard **8192-byte** ceiling, stopping at a
  variable boundary once it would be exceeded.

> **Why `environ` drops the body while `name`/`cmdline`/`ci_*` keep a prefix.**
> `environ` is the one label composed of arbitrary key/value pairs the exporter
> knows nothing about, and **length alone is a secret signal there**. The secret
> heuristics only recognise token-*shaped* values, so a JSON service-account
> key, a PEM body or a connection string — braces, quotes, colons, newlines —
> matches neither the key denylist nor the value heuristics. A 256-byte prefix
> of one of those is credential material published to every scraper, so an
> over-long environ value contributes no body at all. The other bounded labels
> have a known, non-secret shape and keep their prefix.
>
> **It carries no fingerprint either**, and that follows from the same premise.
> The values that reach this path are exactly the ones the heuristics could not
> classify, so they have to be assumed to be credential material — and an
> unsalted `sha256` prefix of a secret, published on an endpoint every scraper
> can read, is an unrate-limited offline oracle: an attacker who can guess a
> structured, low-entropy value (a connection string off a known template, a
> templated internal URL) confirms the guess against the digest. Refusing the
> body but publishing a verifiable commitment to it would hand back most of what
> refusing the body was protecting. The cost is that two distinct over-long
> values of the same length now render identically, so `environ` gives up the
> distinguishability that `name`/`cmdline`/`ci_*` keep — which is also a
> cardinality saving, and the right trade only where the value may be a secret.

When the variable **list** is left incomplete — variables dropped because there
were more than 100 or because the byte ceiling was hit — the companion
`environ_truncated` label is set to `"1"`. Per-value `[REDACTED]` substitutions
and per-value truncation keep the variable present and do **not** set that flag.

> **Behaviour change:** the marker is ~20 bytes typical against 11 for the old
> bare `[TRUNCATED]`, so a process with many over-long environment values reaches
> the 8192-byte ceiling marginally sooner and `environ_truncated` can flip to
> `"1"` where it previously did not. Because the marker replaces the value rather
> than following a prefix, it is far smaller than the ~305-byte worst case a
> prefix-plus-marker scheme would produce, and it is always shorter than the
> 256-byte per-value cap it enforces.

The `cmdline` label is size-bounded too, but on the prefix-keeping side: it is
capped at **2048 bytes** and cut on a rune boundary with the
[truncation marker](#truncation-marker), so a process with an enormous argv
(`ARG_MAX` can reach 2 MB) can't bloat the scrape. That bound is an entry in the
[label size contract](#label-size-contract) and increments
`gitlab_exporter_label_truncations_total{label="cmdline"}` when it fires.
`environ`'s per-value cuts are **not** counted there — the counter tracks whole
label values against the contract table, and `environ` is bounded by its own
three-way rule instead.

Every label value sourced from `/proc` (`name`, `cmdline`, `environ`, and the
`ci_job_*` values) is also **UTF-8-sanitized**: invalid bytes are replaced with
the Unicode replacement character (U+FFFD). Without this a process carrying
binary bytes in its name or environment would panic `MustNewConstMetric` on the
registry's gather goroutine and crash the exporter, so you may occasionally see
`` in these labels.

**The JSON API applies the same secret redaction.** The `/api/processes` and
`/api/history` responses run every `environ` value through the identical rules
(built-in key denylist, operator `redact_key_substrings`, and value-shape
heuristics) before encoding, so secrets are never returned raw on the JSON path
either.
