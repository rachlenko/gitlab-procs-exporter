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
*   **Security Redaction Engine**: Automatically redacts environment variable values containing sensitive terms like `key`, `pass`, `token`, `secret`, `url`, `api`, etc., before exposing them to metrics.
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

---

## 3. Remote PromQL Queries

You can execute these queries in the Prometheus Web UI or Grafana to query the last 10 minutes of historical metrics for processes matching a specific `pid` (e.g., `CI_JOB_ID`) and `name` (e.g., `CI_JOB_NAME`):

| Target Metric | PromQL Expression (Last 10m Timeline) | Output Unit |
| :--- | :--- | :--- |
| **CPU Usage** | `rate(gitlab_process_cpu_seconds_total{pid="CI_JOB_ID", name="CI_JOB_NAME"}[10m])` | Cores utilized |
| **Memory (RSS)** | `gitlab_process_resident_memory_bytes{pid="CI_JOB_ID", name="CI_JOB_NAME"}[10m]` | Bytes |
| **Memory (VMS)** | `gitlab_process_virtual_memory_bytes{pid="CI_JOB_ID", name="CI_JOB_NAME"}[10m]` | Bytes |
| **Disk Read Rate** | `rate(gitlab_process_io_read_bytes_total{pid="CI_JOB_ID", name="CI_JOB_NAME"}[10m])` | Bytes / Sec |
| **Disk Write Rate**| `rate(gitlab_process_io_write_bytes_total{pid="CI_JOB_ID", name="CI_JOB_NAME"}[10m])` | Bytes / Sec |

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

      # 3. Alert if a process experiences heavy Disk I/O load (exceeding 100MB/s)
      - alert: ExtremeProcessDiskIO
        expr: (rate(gitlab_process_io_read_bytes_total[1m]) + rate(gitlab_process_io_write_bytes_total[1m])) > 104857600
        for: 1m
        labels:
          severity: warning
        annotations:
          summary: "High Disk I/O throughput on {{ $labels.name }} (PID: {{ $labels.pid }})"
          description: "Combined read/write speed for {{ $labels.name }} is {{ $value | humanize1024Bytes }}/s."

      # 4. Alert if a monitored process crashes or unexpectedly exits (disappears from live metrics)
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

### Requirements

- **DaemonSet env** — expose the node IP via the Downward API:

  ```yaml
  env:
    - name: HOST_IP
      valueFrom:
        fieldRef:
          fieldPath: status.hostIP
  ```

- **RBAC** — the ServiceAccount needs read access to the kubelet:

  ```yaml
  rules:
    - apiGroups: [""]
      resources: ["nodes/proxy"]
      verbs: ["get"]
  ```

- **TLS** — the node-local kubelet uses a self-signed serving certificate, so
  TLS verification is skipped by default. Set `--kubelet-insecure=false` only if
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
