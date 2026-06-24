# Kubernetes deploy templates

Example/template manifests that run `gitlab-procs-exporter` as a **DaemonSet**
(one pod per Linux node) and emit the in-cluster `kuber_cpu_request` /
`kuber_memory_request` metrics. See the project README sections
[Kubernetes job-resource metrics](../../README.md#kubernetes-job-resource-metrics)
and [Adding sensitive-data filters](../../README.md#adding-sensitive-data-filters).

## Files

| File | Purpose |
|------|---------|
| `namespace.yaml` | `monitoring` namespace. |
| `serviceaccount.yaml` | ServiceAccount the pods run as. |
| `rbac.yaml` | ClusterRole + binding granting `get nodes/proxy` (kubelet `/pods` authz). |
| `configmap.yaml` | `config.yaml` with `redact_key_substrings` (the redaction filters). |
| `daemonset.yaml` | The DaemonSet itself. |
| `kustomization.yaml` | Ties them together for `kubectl apply -k`. |

## Apply

```bash
kubectl apply -k deploy/k8s/

# Verify on one pod:
kubectl -n monitoring exec ds/gitlab-procs-exporter -- \
  sh -c 'wget -qO- localhost:8000/metrics | grep kuber_'
```

## Key parameters (and why)

- **`hostPID: true`** — shares the host PID namespace so `/proc` inside the
  container reflects the **node's** processes. This is how the exporter reads the
  host process table — there is **no bind-mount of `/proc`**.
- **`securityContext.runAsUser: 0`** — root is required to read every process's
  `/proc/<pid>/environ` and I/O counters.
- **`nodeSelector: kubernetes.io/os: linux`** + **`tolerations: [{operator: Exists}]`**
  — run one pod on every Linux node, including tainted / control-plane nodes.
- **`env.HOST_IP` (Downward API `status.hostIP`)** — the node IP used to reach
  *this* node's kubelet at `https://$HOST_IP:10250/pods`.
- **`rbac.yaml` (`get nodes/proxy`)** — lets the kubelet authorize the SA token
  for that request; without it the kube metrics stay empty.

## Customize the redaction filters

Edit `configmap.yaml` (`redact_key_substrings` — case-insensitive substrings of
the environment-variable **name**), then re-apply and restart so the change is
picked up (config is read once at startup, fail-fast):

```bash
kubectl apply -k deploy/k8s/
kubectl -n monitoring rollout restart ds/gitlab-procs-exporter
```

## Prometheus scraping

The DaemonSet pods carry `prometheus.io/scrape` annotations. If you run the
Prometheus Operator instead, add a `PodMonitor` selecting
`app: gitlab-procs-exporter` on port `metrics`.

## TLS

The node-local kubelet uses a self-signed cert, so TLS verification is skipped
by default. Add `--kubelet-insecure=false` to the container `args` only if your
kubelet presents a CA-trusted certificate.
