# Kubernetes job-resource metrics + hardened environ filtering — Design

Date: 2026-06-22
Status: Approved pending user review

## Goal

When `gitlab-procs-exporter` runs inside a Kubernetes cluster (as a DaemonSet),
expose per-GitLab-CI-job resource-request metrics alongside the existing
`gitlab_process_*` metrics:

- `kuber_cpu_request{job_name="..."}` — pod CPU request, in cores (float).
- `kuber_memory_request{job_name="..."}` — pod memory request, in bytes.

When not in a cluster, behaviour is unchanged — no new metrics, no errors.

Additionally, harden the existing environ exposure (`gitlab_process_info`) to
minimize the risk of leaking tokens/secrets.

## Non-goals (YAGNI)

- No dashboard / JSON-API surface for the kube metrics (Prometheus `/metrics` only).
- No extra labels beyond `job_name` (no pod / namespace / pid).
- No limits / usage metrics — only requests, as specified.
- No official `client-go` dependency.

## Decisions (from brainstorming)

| Axis | Decision |
|------|----------|
| Deployment topology | DaemonSet — one instance per node, node-local data. |
| Resource-request source | kubelet read-only API `GET https://$HOST_IP:10250/pods`. |
| job_name source | `CI_JOB_NAME` from process environ (already scraped). |
| process → pod link | pod UID parsed from `/proc/<pid>/cgroup`. |
| Metric labels | `job_name` only. |
| environ filter | Expanded key denylist + value-based secret redaction. |

## Architecture (Approach A — isolated kube collector)

All Kubernetes logic is self-contained in the `exporter/` package and gated by
in-cluster detection. The hot `/proc` path is untouched except for one cheap
cgroup read per pid.

```
                         ┌─────────────────────────┐
   /proc scrape (main) ─►│ HistoryStore            │  (+ ProcessSample.PodUID)
                         │  active processes w/     │
                         │  environ + PodUID        │
                         └──────────┬──────────────┘
                                    │ join on PodUID
   kubelet /pods ──► KubeStore ─────┤
   (kube scraper)   podUID→requests │
                                    ▼
                         ┌─────────────────────────┐
                         │ KubeCollector.Collect    │──► kuber_cpu_request{job_name}
                         │  job_name = environ      │──► kuber_memory_request{job_name}
                         └─────────────────────────┘
```

### Components

**1. In-cluster detection — `exporter/kube.go`**
- `InCluster() bool`: true iff `KUBERNETES_SERVICE_HOST != ""` AND the service
  account token file `/var/run/secrets/kubernetes.io/serviceaccount/token`
  exists and is readable.

**2. kubelet client — `exporter/kube.go`**
- Resolves kubelet address: env `HOST_IP` (← Downward API `status.hostIP`),
  fallback `NODE_NAME`, fallback `127.0.0.1`. Port `10250`.
- `GET https://<addr>:10250/pods` with `Authorization: Bearer <SA token>`.
- TLS `InsecureSkipVerify` by default (kubelet uses a node-local serving cert);
  togglable via a flag `--kubelet-insecure` (default true) wired from main.
- Parses a minimal subset of the `v1.PodList` JSON:
  `items[].metadata.uid`, `items[].spec.containers[].resources.requests.{cpu,memory}`.
- Per pod, CPU request = sum of container CPU requests; memory request = sum of
  container memory requests (a job pod usually has one build container; summing
  is correct and robust to helper/svc containers).
- Returns `[]KubePodInfo{ UID string; CPURequest float64; MemRequest float64 }`.

**3. Kubernetes quantity parser — `exporter/kube.go`**
- CPU: `"500m" → 0.5`, `"1" → 1`, `"250m" → 0.25`, `"2" → 2`.
- Memory: binary (`Ki/Mi/Gi/Ti/Pi/Ei`) and decimal (`k/M/G/T/P/E`) suffixes,
  plain bytes → `uint64`/`float64` bytes. e.g. `512Mi → 536870912`, `1Gi`, `128M`.
- Small self-contained parser (no `k8s.io/apimachinery`).

**4. KubeStore — `exporter/kube.go`**
- Holds latest `map[podUID]KubePodInfo` behind a `sync.RWMutex` (mirrors
  `HistoryStore`'s concurrency discipline).
- `Replace(pods []KubePodInfo)` and `Get(uid string) (KubePodInfo, bool)`.

**5. cgroup → pod UID — `exporter/kube.go`**
- `PodUIDFromCgroup(content string) string`: parses `/proc/<pid>/cgroup`.
  Supports cgroupfs (`.../pod<uid>/...`, uid with `-`) and systemd
  (`...pod<uid>.slice`, uid with `_`). Normalizes to canonical dashed UUID.
  Returns `""` when no pod UID found.

**6. ProcessSample change — `exporter/history.go`**
- Add `PodUID string \`json:"pod_uid,omitempty"\``. Backward compatible.

**7. Process scrape change — `main.go` `scrape()`**
- Only when `InCluster()`: read `/proc/<pid>/cgroup`, set `sample.PodUID`.
  Cheap single file read; skipped entirely outside a cluster.

**8. KubeCollector — `exporter/kube_collector.go`**
- Implements `prometheus.Collector` with two descs:
  `kuber_cpu_request` and `kuber_memory_request`, label set `[job_name]`.
- `Collect`: iterate `HistoryStore.GetActiveProcesses()`; for each with a
  non-empty `PodUID` and an `environ["CI_JOB_NAME"]`, look up
  `KubeStore.Get(PodUID)`; build `map[jobName]KubePodInfo`.
- Emit one series per unique `job_name`. **Dedup by `job_name`** to avoid
  duplicate-series panics (Prometheus rejects identical label sets). Collision
  (two pods, same job_name, same node) = last-wins — documented limitation.

**9. main.go integration**
- If `InCluster()`: start `go startKubeScraper(kubeStore, interval, cfg)` and
  `prometheus.MustRegister(NewKubeCollector(store, kubeStore))`.
- Else: no change. `startKubeScraper` polls kubelet each `interval`, parses,
  and calls `kubeStore.Replace(...)`; logs and skips on transient errors.

### Hardened environ filtering — `exporter/collector.go`

The only outward environ exposure is `gitlab_process_info`. Strengthen its
scrubbing (kube path only reads `CI_JOB_NAME`, never re-exposes other env).

- **Expanded key denylist** in `IsSecretKey()`: add `cert`, `ssh`, `gpg`,
  `jwt`, `bearer`, `access`, `cookie`, `session`, `salt`, `otp`, `pin`,
  `webhook`, `dsn`, `connection`, `passwd`, `client_secret`, `account`, `sas`.
- **New value-based redaction** `IsSecretValue(v string) bool`: redacts values
  that look like secrets regardless of key —
  - known token prefixes: `glpat-`, `gho_`, `ghp_`, `github_pat_`, `AKIA`,
    `xoxb-`/`xoxp-`, JWT (`eyJ` + two `.`-separated b64 segments);
  - long high-entropy strings: length ≥ 32 AND (base64/hex charset) AND Shannon
    entropy above a threshold.
- `Collect` redacts a pair to `[REDACTED]` when `IsSecretKey(k) || IsSecretValue(v)`.

## Error handling

- kubelet unreachable / 401 / 403 / parse error → log once per scrape, keep last
  good `KubeStore` snapshot, never crash the process or the `/proc` path.
- Missing `HOST_IP`/`NODE_NAME` → fallback chain; if all fail, kube scraper logs
  and the kube metrics simply stay empty.
- No pod UID in cgroup, or no matching pod in KubeStore → process contributes no
  kube series (normal for non-CI processes).

## Testing (one `*_test.go` per impl file; temp files cleaned via `defer`)

- `exporter/kube_test.go`:
  - quantity parser: CPU (`500m`,`1`,`250m`,`2`), memory (`512Mi`,`1Gi`,`128M`,plain).
  - `PodUIDFromCgroup`: cgroupfs and systemd formats, and a non-k8s cgroup → `""`.
  - `InCluster`: env set + temp token file present/absent (temp file via `t.TempDir`/`defer`).
  - kubelet client: `httptest.NewTLSServer` serving a sample `PodList` JSON →
    expected `[]KubePodInfo` (summed requests).
  - `KubeStore` Replace/Get concurrency-safe round-trip.
- `exporter/kube_collector_test.go`:
  - feed `HistoryStore` an active process with `PodUID` + `CI_JOB_NAME`, plus a
    matching `KubeStore` entry; assert emitted metrics via `prometheus/testutil`.
  - dedup: two pods same `job_name` → exactly one series.
- `exporter/collector_test.go` (extend): new denylist keys redacted;
  `IsSecretValue` redacts token-shaped values and passes through plain values
  (e.g. `CI_JOB_NAME=build` stays visible).
- `exporter/history_test.go` (extend): `PodUID` round-trips through a sample.

## Files touched

- New: `exporter/kube.go`, `exporter/kube_test.go`,
  `exporter/kube_collector.go`, `exporter/kube_collector_test.go`.
- Modified: `exporter/history.go` (+`PodUID`), `exporter/collector.go`
  (filter hardening), `main.go` (gated scraper + registration + cgroup read),
  plus the matching `*_test.go` extensions.
- Docs: README section on the kube metrics, required RBAC (`nodes/proxy`),
  DaemonSet env (`HOST_IP` via `status.hostIP`), and the SSRF/TLS caveat.

## Known limitations

- Same `job_name` on two pods on one node → last-wins (single series).
- kubelet `:10250` read API + `nodes/proxy` RBAC must be permitted by the cluster.
- TLS insecure by default for the node-local kubelet cert; togglable.
