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

## Metrics reference

Everything below is served on `GET /metrics` (default port `8000`) in the
Prometheus text exposition format.

### Per-process metrics (always exported)

One series **per active process**, refreshed every `--interval`. All carry the
labels `pid` (process id) and `name` (process comm).

| Metric | Type | Unit | Meaning |
|--------|------|------|---------|
| `gitlab_process_cpu_seconds_total` | counter | seconds | Cumulative user+system CPU time consumed by the process. |
| `gitlab_process_resident_memory_bytes` | gauge | bytes | Resident set size (RSS). |
| `gitlab_process_virtual_memory_bytes` | gauge | bytes | Virtual memory size (VMS). |
| `gitlab_process_io_read_bytes_total` | counter | bytes | Cumulative bytes read from disk. |
| `gitlab_process_io_write_bytes_total` | counter | bytes | Cumulative bytes written to disk. |
| `gitlab_process_info` | gauge | `1` | Metadata-only series; the value is always `1` and the data lives in its labels. |

`gitlab_process_info` carries three extra labels beyond `pid`/`name`:

- `cmdline` — the full process command line.
- `environ` — the process environment as a single `KEY=VALUE, KEY2=VALUE2`
  string, **sorted by key** (stable across scrapes). Secret-looking entries are
  rendered as `KEY=[REDACTED]` and the whole label is size-bounded (see
  [Hardened environ scrubbing](#hardened-environ-scrubbing)).
- `environ_truncated` — `"1"` when the emitted `environ` variable **list is
  incomplete** (one or more variables were dropped entirely because there were
  more than 100 variables or the total-size ceiling was reached), otherwise
  `"0"`. It is **not** set by per-value `[REDACTED]` or `[TRUNCATED]`
  substitutions — those keep the variable present, so the list is still complete.

Example exposition:

```
# HELP gitlab_process_resident_memory_bytes Resident set size (RSS) in bytes.
# TYPE gitlab_process_resident_memory_bytes gauge
gitlab_process_resident_memory_bytes{pid="4567",name="sidekiq"} 2.097152e+08
# HELP gitlab_process_info Metadata about the process ... (scrubbed for secrets).
# TYPE gitlab_process_info gauge
gitlab_process_info{pid="4567",name="sidekiq",cmdline="sidekiq -c 10",environ="CI_JOB_NAME=build, DB_PASSWORD=[REDACTED], HOME=/root",environ_truncated="0"} 1
```

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

## Configuration file

Pass `--config <path>` to supply a YAML file with extra environ redaction
rules. Without the flag, only the built-in secret denylist applies. If the
flag is given but the file is missing or malformed, the exporter logs the
error and exits (fail-fast).

```yaml
# config.example.yaml
redact_key_substrings:
  - vault
  - internal_token
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

### Supplying `--config` (sensitive-data filters) in Kubernetes

The redaction filters from [Adding sensitive-data filters](#adding-sensitive-data-filters)
work in-cluster too: ship the config as a `ConfigMap`, mount it into the
DaemonSet, and point `--config` at the mounted path.

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
`kubectl logs`). After editing the ConfigMap, restart the DaemonSet so the new
filters take effect:

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
- any single value longer than **256 bytes** is replaced whole with
  `[TRUNCATED]` (distinct from `[REDACTED]` so "too long" is distinguishable from
  "secret"; never byte-cut, so the label stays valid UTF-8);
- the joined label is capped at a hard **8192-byte** ceiling, stopping at a
  variable boundary once it would be exceeded.

When the variable **list** is left incomplete — variables dropped because there
were more than 100 or because the byte ceiling was hit — the companion
`environ_truncated` label is set to `"1"`. Per-value `[REDACTED]`/`[TRUNCATED]`
substitutions keep the variable present and do **not** set that flag.
