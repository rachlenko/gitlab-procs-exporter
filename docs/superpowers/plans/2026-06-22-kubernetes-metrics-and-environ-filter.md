# Kubernetes job-resource metrics + hardened environ filtering — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When the exporter runs inside a Kubernetes cluster, expose `kuber_cpu_request{job_name}` and `kuber_memory_request{job_name}` for GitLab CI job pods, and harden the existing environ exposure against secret leakage.

**Architecture:** A DaemonSet instance reads node-local pod resource requests from the kubelet read-only API (`/pods`), links each running process to its pod via the pod UID in `/proc/<pid>/cgroup`, and derives `job_name` from the process's `CI_JOB_NAME` environ var. An isolated `KubeCollector` joins active processes (from `HistoryStore`) with a `KubeStore` (pod UID → requests) and is registered only when in-cluster. Separately, `gitlab_process_info` environ scrubbing gains an expanded key denylist plus value-based secret redaction.

**Tech Stack:** Go 1.24, `github.com/prometheus/client_golang` (incl. `prometheus/testutil`), stdlib `net/http`/`crypto/tls`/`encoding/json`. No `client-go`.

## Global Constraints

- Go version floor: `go 1.24.0` (from `go.mod`). No new module dependencies.
- One `*_test.go` file per implementation file (`kube.go` → `kube_test.go`, etc.).
- Temporary test files MUST be cleaned up via `defer`/`t.TempDir()`.
- Use `sync.RWMutex` for any shared store (mirrors `HistoryStore`).
- kube metric names exactly: `kuber_cpu_request`, `kuber_memory_request`; sole label `job_name`. CPU in cores (float), memory in bytes (float).
- job_name source env key: `CI_JOB_NAME`. Pod-link source: `/proc/<pid>/cgroup`.
- Out of scope: dashboard/JSON-API for kube metrics, extra labels, limits/usage metrics.

---

### Task 1: Add `PodUID` to `ProcessSample`

**Files:**
- Modify: `exporter/history.go` (struct `ProcessSample`, ~lines 10-24)
- Test: `exporter/history_test.go`

**Interfaces:**
- Produces: `ProcessSample.PodUID string` (JSON `pod_uid,omitempty`). Consumed by Task 7 (collector) and Task 9 (scrape populates it).

- [ ] **Step 1: Write the failing test**

Add to `exporter/history_test.go`:

```go
func TestProcessSamplePodUID(t *testing.T) {
	hs := NewHistoryStore()
	hs.AddSample(ProcessSample{
		Timestamp: time.Now(),
		PID:       4242,
		Name:      "ruby",
		PodUID:    "abc-123",
		IsActive:  true,
	})
	active := hs.GetActiveProcesses()
	if len(active) != 1 {
		t.Fatalf("expected 1 active process, got %d", len(active))
	}
	if active[0].PodUID != "abc-123" {
		t.Errorf("expected PodUID %q, got %q", "abc-123", active[0].PodUID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./exporter/ -run TestProcessSamplePodUID -v`
Expected: FAIL — `unknown field 'PodUID' in struct literal`.

- [ ] **Step 3: Add the field**

In `exporter/history.go`, inside `ProcessSample`, add after the `Name` field:

```go
	PodUID     string            `json:"pod_uid,omitempty"` // Kubernetes pod UID (empty outside a cluster)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./exporter/ -run TestProcessSamplePodUID -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add exporter/history.go exporter/history_test.go
git commit -m "feat(exporter): add PodUID to ProcessSample"
```

---

### Task 2: Kubernetes quantity parser

**Files:**
- Create: `exporter/kube.go`
- Test: `exporter/kube_test.go`

**Interfaces:**
- Produces: `ParseCPUQuantity(s string) (float64, error)` → cores; `ParseMemoryQuantity(s string) (float64, error)` → bytes. Consumed by Task 5 (`parsePodList`).

- [ ] **Step 1: Write the failing test**

Create `exporter/kube_test.go`:

```go
package exporter

import "testing"

func TestParseCPUQuantity(t *testing.T) {
	cases := map[string]float64{
		"500m": 0.5, "250m": 0.25, "1": 1, "2": 2, "1500m": 1.5, "": 0,
	}
	for in, want := range cases {
		got, err := ParseCPUQuantity(in)
		if err != nil {
			t.Errorf("ParseCPUQuantity(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseCPUQuantity(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseMemoryQuantity(t *testing.T) {
	cases := map[string]float64{
		"512Mi": 536870912, "1Gi": 1073741824, "128M": 128000000,
		"1000": 1000, "2Ki": 2048, "": 0,
	}
	for in, want := range cases {
		got, err := ParseMemoryQuantity(in)
		if err != nil {
			t.Errorf("ParseMemoryQuantity(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseMemoryQuantity(%q) = %v, want %v", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./exporter/ -run 'TestParse(CPU|Memory)Quantity' -v`
Expected: FAIL — undefined: `ParseCPUQuantity`.

- [ ] **Step 3: Write minimal implementation**

Create `exporter/kube.go`:

```go
package exporter

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseCPUQuantity parses a Kubernetes CPU quantity into cores.
// "500m" -> 0.5, "2" -> 2. Empty string -> 0.
func ParseCPUQuantity(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if strings.HasSuffix(s, "m") {
		milli, err := strconv.ParseFloat(strings.TrimSuffix(s, "m"), 64)
		if err != nil {
			return 0, fmt.Errorf("cpu quantity %q: %w", s, err)
		}
		return milli / 1000, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("cpu quantity %q: %w", s, err)
	}
	return v, nil
}

var memSuffixes = []struct {
	suffix string
	mult   float64
}{
	{"Ei", 1 << 60}, {"Pi", 1 << 50}, {"Ti", 1 << 40}, {"Gi", 1 << 30},
	{"Mi", 1 << 20}, {"Ki", 1 << 10},
	{"E", 1e18}, {"P", 1e15}, {"T", 1e12}, {"G", 1e9}, {"M", 1e6}, {"k", 1e3},
}

// ParseMemoryQuantity parses a Kubernetes memory quantity into bytes.
// "512Mi" -> 536870912, "128M" -> 128000000. Empty string -> 0.
func ParseMemoryQuantity(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	for _, ms := range memSuffixes {
		if strings.HasSuffix(s, ms.suffix) {
			num, err := strconv.ParseFloat(strings.TrimSuffix(s, ms.suffix), 64)
			if err != nil {
				return 0, fmt.Errorf("memory quantity %q: %w", s, err)
			}
			return num * ms.mult, nil
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("memory quantity %q: %w", s, err)
	}
	return v, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./exporter/ -run 'TestParse(CPU|Memory)Quantity' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add exporter/kube.go exporter/kube_test.go
git commit -m "feat(exporter): add Kubernetes CPU/memory quantity parsers"
```

---

### Task 3: cgroup → pod UID parser

**Files:**
- Modify: `exporter/kube.go`
- Test: `exporter/kube_test.go`

**Interfaces:**
- Produces: `PodUIDFromCgroup(content string) string` (canonical dashed UUID, or `""`). Consumed by Task 9 (scrape).

- [ ] **Step 1: Write the failing test**

Append to `exporter/kube_test.go`:

```go
func TestPodUIDFromCgroup(t *testing.T) {
	cgroupfs := "12:cpuset:/kubepods/besteffort/pod1234abcd-12ab-34cd-56ef-1234567890ab/abc123def456\n"
	systemd := "0::/kubepods.slice/kubepods-burstable.slice/" +
		"kubepods-burstable-pod1234abcd_12ab_34cd_56ef_1234567890ab.slice/cri-containerd-xyz.scope\n"
	want := "1234abcd-12ab-34cd-56ef-1234567890ab"

	if got := PodUIDFromCgroup(cgroupfs); got != want {
		t.Errorf("cgroupfs: got %q, want %q", got, want)
	}
	if got := PodUIDFromCgroup(systemd); got != want {
		t.Errorf("systemd: got %q, want %q", got, want)
	}
	if got := PodUIDFromCgroup("0::/system.slice/sshd.service\n"); got != "" {
		t.Errorf("non-k8s: got %q, want empty", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./exporter/ -run TestPodUIDFromCgroup -v`
Expected: FAIL — undefined: `PodUIDFromCgroup`.

- [ ] **Step 3: Write minimal implementation**

Append to `exporter/kube.go` (add `regexp` to the import block):

```go
// podUIDRe matches the pod UID embedded in a cgroup path, in either cgroupfs
// ("pod<uuid>") or systemd ("pod<uuid>.slice") form. UID separators may be
// dashes or underscores.
var podUIDRe = regexp.MustCompile(`pod([0-9a-fA-F]{8}[-_][0-9a-fA-F]{4}[-_][0-9a-fA-F]{4}[-_][0-9a-fA-F]{4}[-_][0-9a-fA-F]{12})`)

// PodUIDFromCgroup extracts the Kubernetes pod UID from /proc/<pid>/cgroup
// content, returning the canonical dashed UUID, or "" if none is present.
func PodUIDFromCgroup(content string) string {
	m := podUIDRe.FindStringSubmatch(content)
	if m == nil {
		return ""
	}
	return strings.ReplaceAll(m[1], "_", "-")
}
```

Update the import block at the top of `exporter/kube.go` to include `"regexp"`:

```go
import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./exporter/ -run TestPodUIDFromCgroup -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add exporter/kube.go exporter/kube_test.go
git commit -m "feat(exporter): parse pod UID from cgroup path"
```

---

### Task 4: In-cluster detection

**Files:**
- Modify: `exporter/kube.go`
- Test: `exporter/kube_test.go`

**Interfaces:**
- Produces: `InCluster() bool`; package var `saTokenPath string` (overridable in tests). Consumed by Task 5 (client), Task 9 (main).

- [ ] **Step 1: Write the failing test**

Append to `exporter/kube_test.go` (add `os` and `path/filepath` to imports):

```go
func TestInCluster(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	orig := saTokenPath
	saTokenPath = tokenFile
	defer func() { saTokenPath = orig }()

	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	if !InCluster() {
		t.Error("expected InCluster() true when env + token present")
	}

	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	if InCluster() {
		t.Error("expected InCluster() false when env unset")
	}

	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	saTokenPath = filepath.Join(dir, "missing")
	if InCluster() {
		t.Error("expected InCluster() false when token missing")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./exporter/ -run TestInCluster -v`
Expected: FAIL — undefined: `saTokenPath` / `InCluster`.

- [ ] **Step 3: Write minimal implementation**

Append to `exporter/kube.go` (add `"os"` to imports):

```go
// saTokenPath is the in-cluster service account token path; var (not const)
// so tests can override it.
var saTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// InCluster reports whether the process is running inside a Kubernetes cluster.
func InCluster() bool {
	if os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
		return false
	}
	if _, err := os.Stat(saTokenPath); err != nil {
		return false
	}
	return true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./exporter/ -run TestInCluster -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add exporter/kube.go exporter/kube_test.go
git commit -m "feat(exporter): add in-cluster detection"
```

---

### Task 5: kubelet client + PodList parsing

**Files:**
- Modify: `exporter/kube.go`
- Test: `exporter/kube_test.go`

**Interfaces:**
- Produces: `type KubePodInfo struct { UID string; CPURequest float64; MemRequest float64 }`; `parsePodList(data []byte) ([]KubePodInfo, error)`; `type KubeletClient struct{ baseURL, token string; http *http.Client }`; `NewKubeletClient(insecure bool) (*KubeletClient, error)`; `(*KubeletClient) Pods() ([]KubePodInfo, error)`; `(*KubeletClient) BaseURL() string`. Consumed by Task 7 (collector via KubePodInfo), Task 9 (main).

- [ ] **Step 1: Write the failing test**

Append to `exporter/kube_test.go` (add `net/http`, `net/http/httptest` to imports):

```go
const samplePodList = `{
  "items": [
    {"metadata": {"uid": "pod-aaa"},
     "spec": {"containers": [
       {"resources": {"requests": {"cpu": "500m", "memory": "512Mi"}}},
       {"resources": {"requests": {"cpu": "250m", "memory": "256Mi"}}}
     ]}},
    {"metadata": {"uid": "pod-bbb"},
     "spec": {"containers": [
       {"resources": {"requests": {"cpu": "1", "memory": "1Gi"}}}
     ]}}
  ]
}`

func TestParsePodList(t *testing.T) {
	pods, err := parsePodList([]byte(samplePodList))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]KubePodInfo{}
	for _, p := range pods {
		got[p.UID] = p
	}
	if got["pod-aaa"].CPURequest != 0.75 {
		t.Errorf("pod-aaa CPU = %v, want 0.75", got["pod-aaa"].CPURequest)
	}
	if got["pod-aaa"].MemRequest != 805306368 { // 512Mi + 256Mi
		t.Errorf("pod-aaa Mem = %v, want 805306368", got["pod-aaa"].MemRequest)
	}
	if got["pod-bbb"].CPURequest != 1 {
		t.Errorf("pod-bbb CPU = %v, want 1", got["pod-bbb"].CPURequest)
	}
}

func TestKubeletClientPods(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pods" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(samplePodList))
	}))
	defer ts.Close()

	c := &KubeletClient{baseURL: ts.URL, token: "tok", http: ts.Client()}
	pods, err := c.Pods()
	if err != nil {
		t.Fatal(err)
	}
	if len(pods) != 2 {
		t.Fatalf("expected 2 pods, got %d", len(pods))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./exporter/ -run 'TestParsePodList|TestKubeletClientPods' -v`
Expected: FAIL — undefined: `parsePodList` / `KubeletClient`.

- [ ] **Step 3: Write minimal implementation**

Append to `exporter/kube.go` (extend imports with `"crypto/tls"`, `"encoding/json"`, `"io"`, `"net/http"`, `"time"`):

```go
// KubePodInfo holds the summed resource requests for one pod.
type KubePodInfo struct {
	UID        string
	CPURequest float64 // cores
	MemRequest float64 // bytes
}

type podList struct {
	Items []struct {
		Metadata struct {
			UID string `json:"uid"`
		} `json:"metadata"`
		Spec struct {
			Containers []struct {
				Resources struct {
					Requests struct {
						CPU    string `json:"cpu"`
						Memory string `json:"memory"`
					} `json:"requests"`
				} `json:"resources"`
			} `json:"containers"`
		} `json:"spec"`
	} `json:"items"`
}

// parsePodList parses a kubelet /pods response into per-pod summed requests.
func parsePodList(data []byte) ([]KubePodInfo, error) {
	var pl podList
	if err := json.Unmarshal(data, &pl); err != nil {
		return nil, fmt.Errorf("parse pod list: %w", err)
	}
	out := make([]KubePodInfo, 0, len(pl.Items))
	for _, item := range pl.Items {
		info := KubePodInfo{UID: item.Metadata.UID}
		for _, ctr := range item.Spec.Containers {
			cpu, err := ParseCPUQuantity(ctr.Resources.Requests.CPU)
			if err != nil {
				return nil, err
			}
			mem, err := ParseMemoryQuantity(ctr.Resources.Requests.Memory)
			if err != nil {
				return nil, err
			}
			info.CPURequest += cpu
			info.MemRequest += mem
		}
		out = append(out, info)
	}
	return out, nil
}

// KubeletClient queries the node-local kubelet read-only API.
type KubeletClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// kubeletAddr resolves the kubelet host: HOST_IP, then NODE_NAME, then loopback.
func kubeletAddr() string {
	if v := os.Getenv("HOST_IP"); v != "" {
		return v
	}
	if v := os.Getenv("NODE_NAME"); v != "" {
		return v
	}
	return "127.0.0.1"
}

// NewKubeletClient builds a client using the in-cluster SA token.
func NewKubeletClient(insecure bool) (*KubeletClient, error) {
	tok, err := os.ReadFile(saTokenPath)
	if err != nil {
		return nil, fmt.Errorf("read SA token: %w", err)
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}, //nolint:gosec // node-local kubelet cert
	}
	return &KubeletClient{
		baseURL: fmt.Sprintf("https://%s:10250", kubeletAddr()),
		token:   strings.TrimSpace(string(tok)),
		http:    &http.Client{Timeout: 5 * time.Second, Transport: tr},
	}, nil
}

// BaseURL returns the kubelet base URL (for logging).
func (c *KubeletClient) BaseURL() string { return c.baseURL }

// Pods fetches and parses the node's pod list from the kubelet.
func (c *KubeletClient) Pods() ([]KubePodInfo, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/pods", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kubelet /pods: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parsePodList(data)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./exporter/ -run 'TestParsePodList|TestKubeletClientPods' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add exporter/kube.go exporter/kube_test.go
git commit -m "feat(exporter): add kubelet /pods client and PodList parser"
```

---

### Task 6: KubeStore

**Files:**
- Modify: `exporter/kube.go`
- Test: `exporter/kube_test.go`

**Interfaces:**
- Produces: `type KubeStore struct{...}`; `NewKubeStore() *KubeStore`; `(*KubeStore) Replace(pods []KubePodInfo)`; `(*KubeStore) Get(uid string) (KubePodInfo, bool)`. Consumed by Task 7 (collector), Task 9 (scraper).

- [ ] **Step 1: Write the failing test**

Append to `exporter/kube_test.go`:

```go
func TestKubeStoreReplaceGet(t *testing.T) {
	ks := NewKubeStore()
	if _, ok := ks.Get("pod-aaa"); ok {
		t.Error("expected miss on empty store")
	}
	ks.Replace([]KubePodInfo{{UID: "pod-aaa", CPURequest: 0.5, MemRequest: 1024}})
	got, ok := ks.Get("pod-aaa")
	if !ok || got.CPURequest != 0.5 || got.MemRequest != 1024 {
		t.Errorf("unexpected Get result: %+v ok=%v", got, ok)
	}
	// Replace fully swaps the snapshot.
	ks.Replace([]KubePodInfo{{UID: "pod-bbb", CPURequest: 1}})
	if _, ok := ks.Get("pod-aaa"); ok {
		t.Error("expected pod-aaa gone after Replace")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./exporter/ -run TestKubeStoreReplaceGet -v`
Expected: FAIL — undefined: `NewKubeStore`.

- [ ] **Step 3: Write minimal implementation**

Append to `exporter/kube.go` (add `"sync"` to imports):

```go
// KubeStore holds the latest snapshot of pod UID -> resource requests.
type KubeStore struct {
	mu   sync.RWMutex
	pods map[string]KubePodInfo
}

// NewKubeStore creates an empty KubeStore.
func NewKubeStore() *KubeStore {
	return &KubeStore{pods: make(map[string]KubePodInfo)}
}

// Replace atomically swaps the whole snapshot.
func (ks *KubeStore) Replace(pods []KubePodInfo) {
	next := make(map[string]KubePodInfo, len(pods))
	for _, p := range pods {
		next[p.UID] = p
	}
	ks.mu.Lock()
	ks.pods = next
	ks.mu.Unlock()
}

// Get returns the pod info for a UID.
func (ks *KubeStore) Get(uid string) (KubePodInfo, bool) {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	p, ok := ks.pods[uid]
	return p, ok
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./exporter/ -run TestKubeStoreReplaceGet -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add exporter/kube.go exporter/kube_test.go
git commit -m "feat(exporter): add thread-safe KubeStore snapshot"
```

---

### Task 7: KubeCollector

**Files:**
- Create: `exporter/kube_collector.go`
- Test: `exporter/kube_collector_test.go`

**Interfaces:**
- Consumes: `HistoryStore.GetActiveProcesses()`, `ProcessSample.PodUID`, `ProcessSample.Environ`, `KubeStore.Get`, `KubePodInfo`.
- Produces: `NewKubeCollector(store *HistoryStore, kube *KubeStore) *KubeCollector` implementing `prometheus.Collector`. Consumed by Task 9 (main registration).

- [ ] **Step 1: Write the failing test**

Create `exporter/kube_collector_test.go`:

```go
package exporter

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestKubeCollector(t *testing.T) {
	store := NewHistoryStore()
	store.AddSample(ProcessSample{
		Timestamp: time.Now(),
		PID:       100,
		Name:      "ruby",
		PodUID:    "pod-aaa",
		Environ:   map[string]string{"CI_JOB_NAME": "build"},
		IsActive:  true,
	})
	// Process without pod UID or job name must not emit anything.
	store.AddSample(ProcessSample{
		Timestamp: time.Now(),
		PID:       101,
		Name:      "bash",
		Environ:   map[string]string{},
		IsActive:  true,
	})

	ks := NewKubeStore()
	ks.Replace([]KubePodInfo{{UID: "pod-aaa", CPURequest: 0.5, MemRequest: 1048576}})

	c := NewKubeCollector(store, ks)

	expected := `
# HELP kuber_cpu_request CPU request of the GitLab CI job pod, in cores.
# TYPE kuber_cpu_request gauge
kuber_cpu_request{job_name="build"} 0.5
# HELP kuber_memory_request Memory request of the GitLab CI job pod, in bytes.
# TYPE kuber_memory_request gauge
kuber_memory_request{job_name="build"} 1.048576e+06
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected)); err != nil {
		t.Error(err)
	}
}

func TestKubeCollectorDedupJobName(t *testing.T) {
	store := NewHistoryStore()
	store.AddSample(ProcessSample{
		Timestamp: time.Now(), PID: 1, Name: "a", PodUID: "pod-aaa",
		Environ: map[string]string{"CI_JOB_NAME": "test"}, IsActive: true,
	})
	store.AddSample(ProcessSample{
		Timestamp: time.Now(), PID: 2, Name: "b", PodUID: "pod-bbb",
		Environ: map[string]string{"CI_JOB_NAME": "test"}, IsActive: true,
	})
	ks := NewKubeStore()
	ks.Replace([]KubePodInfo{
		{UID: "pod-aaa", CPURequest: 0.5, MemRequest: 1},
		{UID: "pod-bbb", CPURequest: 0.9, MemRequest: 2},
	})
	c := NewKubeCollector(store, ks)
	// Same job_name on two pods must collapse to exactly one series per metric.
	if n := testutil.CollectAndCount(c, "kuber_cpu_request"); n != 1 {
		t.Errorf("expected 1 kuber_cpu_request series, got %d", n)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./exporter/ -run TestKubeCollector -v`
Expected: FAIL — undefined: `NewKubeCollector`.

- [ ] **Step 3: Write minimal implementation**

Create `exporter/kube_collector.go`:

```go
package exporter

import "github.com/prometheus/client_golang/prometheus"

// KubeCollector emits per-GitLab-CI-job pod resource requests. It joins active
// processes (which carry the pod UID and CI_JOB_NAME) with the KubeStore.
type KubeCollector struct {
	store *HistoryStore
	kube  *KubeStore

	cpuDesc *prometheus.Desc
	memDesc *prometheus.Desc
}

// NewKubeCollector creates a KubeCollector.
func NewKubeCollector(store *HistoryStore, kube *KubeStore) *KubeCollector {
	labels := []string{"job_name"}
	return &KubeCollector{
		store: store,
		kube:  kube,
		cpuDesc: prometheus.NewDesc(
			"kuber_cpu_request",
			"CPU request of the GitLab CI job pod, in cores.",
			labels, nil,
		),
		memDesc: prometheus.NewDesc(
			"kuber_memory_request",
			"Memory request of the GitLab CI job pod, in bytes.",
			labels, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (kc *KubeCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- kc.cpuDesc
	ch <- kc.memDesc
}

// Collect implements prometheus.Collector.
func (kc *KubeCollector) Collect(ch chan<- prometheus.Metric) {
	// Dedup by job_name: identical label sets would make Prometheus panic.
	byJob := make(map[string]KubePodInfo)
	for _, p := range kc.store.GetActiveProcesses() {
		if p.PodUID == "" {
			continue
		}
		jobName := p.Environ["CI_JOB_NAME"]
		if jobName == "" {
			continue
		}
		info, ok := kc.kube.Get(p.PodUID)
		if !ok {
			continue
		}
		byJob[jobName] = info // last-wins on collision
	}

	for jobName, info := range byJob {
		ch <- prometheus.MustNewConstMetric(kc.cpuDesc, prometheus.GaugeValue, info.CPURequest, jobName)
		ch <- prometheus.MustNewConstMetric(kc.memDesc, prometheus.GaugeValue, info.MemRequest, jobName)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./exporter/ -run TestKubeCollector -v`
Expected: PASS (both `TestKubeCollector` and `TestKubeCollectorDedupJobName`).

- [ ] **Step 5: Commit**

```bash
git add exporter/kube_collector.go exporter/kube_collector_test.go
git commit -m "feat(exporter): add KubeCollector for kuber_cpu/memory_request"
```

---

### Task 8: Hardened environ filtering

**Files:**
- Modify: `exporter/collector.go` (`IsSecretKey` ~lines 104-114; `Collect` env loop ~lines 88-96)
- Test: `exporter/collector_test.go`

**Interfaces:**
- Produces: expanded `IsSecretKey(string) bool`; new `IsSecretValue(string) bool`. Used internally by `ProcessCollector.Collect`.

- [ ] **Step 1: Write the failing test**

Append to `exporter/collector_test.go`:

```go
func TestIsSecretKeyExpanded(t *testing.T) {
	secrets := []string{
		"TLS_CERT", "SSH_KEY", "GPG_PASSPHRASE", "MY_JWT", "BEARER_HEADER",
		"ACCESS_GRANT", "SESSION_ID", "CSRF_COOKIE", "PASSWORD_SALT",
		"OTP_SEED", "WEBHOOK_URL", "DB_DSN", "PG_CONNECTION", "USER_PASSWD",
	}
	for _, s := range secrets {
		if !IsSecretKey(s) {
			t.Errorf("expected key %q to be marked as secret", s)
		}
	}
}

func TestIsSecretValue(t *testing.T) {
	secretVals := []string{
		"glpat-abcdefghij1234567890",
		"ghp_0123456789abcdef0123456789abcdef0123",
		"AKIAIOSFODNN7EXAMPLE",
		"eyJhbGciOi.eyJzdWIiOiIxMjM0.SflKxwRJSM",
		"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0", // 40-char hex
	}
	for _, v := range secretVals {
		if !IsSecretValue(v) {
			t.Errorf("expected value %q to be redacted", v)
		}
	}
	plainVals := []string{"build", "main", "/usr/local/bin:/usr/bin", "true", "1", "ruby:3.2"}
	for _, v := range plainVals {
		if IsSecretValue(v) {
			t.Errorf("expected value %q to pass through", v)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./exporter/ -run 'TestIsSecretKeyExpanded|TestIsSecretValue' -v`
Expected: FAIL — `IsSecretValue` undefined and several keys not yet matched.

- [ ] **Step 3: Write minimal implementation**

In `exporter/collector.go`, replace the `IsSecretKey` function (lines ~104-114) with the expanded list plus a new value-based check. Add `"math"` to the import block (alongside `"fmt"`, `"strings"`):

```go
// IsSecretKey checks if the key name suggests it holds sensitive credentials.
func IsSecretKey(key string) bool {
	k := strings.ToLower(key)
	secrets := []string{
		"key", "pass", "passwd", "token", "secret", "auth", "pwd", "db", "url",
		"private", "crypt", "credential", "signature", "api",
		"cert", "ssh", "gpg", "jwt", "bearer", "access", "cookie", "session",
		"salt", "otp", "webhook", "dsn", "connection", "client_secret", "sas",
	}
	for _, s := range secrets {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// tokenPrefixes are well-known secret/token prefixes.
var tokenPrefixes = []string{
	"glpat-", "gho_", "ghp_", "ghu_", "ghs_", "github_pat_",
	"AKIA", "xoxb-", "xoxp-", "xoxa-",
}

// IsSecretValue reports whether a value looks like a secret regardless of key.
func IsSecretValue(v string) bool {
	if v == "" {
		return false
	}
	for _, p := range tokenPrefixes {
		if strings.HasPrefix(v, p) {
			return true
		}
	}
	// JWT: "eyJ" header + two dot-separated segments.
	if strings.HasPrefix(v, "eyJ") && strings.Count(v, ".") == 2 {
		return true
	}
	// Long, high-entropy token-charset string.
	if len(v) >= 32 && isTokenCharset(v) && shannonEntropy(v) >= 3.5 {
		return true
	}
	return false
}

func isTokenCharset(v string) bool {
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '+' || r == '/' || r == '=' || r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

func shannonEntropy(v string) float64 {
	counts := make(map[rune]float64)
	for _, r := range v {
		counts[r]++
	}
	n := float64(len(v))
	var h float64
	for _, c := range counts {
		p := c / n
		h -= p * math.Log2(p)
	}
	return h
}
```

Then update the environ scrub loop in `Collect` (lines ~88-96) to also redact by value:

```go
		// Scrub environment variables for security before exposing via metrics
		var envPairs []string
		for k, v := range p.Environ {
			val := v
			if IsSecretKey(k) || IsSecretValue(v) {
				val = "[REDACTED]"
			}
			envPairs = append(envPairs, fmt.Sprintf("%s=%s", k, val))
		}
		envStr := strings.Join(envPairs, ", ")
```

- [ ] **Step 4: Run all exporter tests to verify pass + no regression**

Run: `go test ./exporter/ -v`
Expected: PASS, including the pre-existing `TestIsSecretKey` (its `nonSecrets` list — `PATH`, `USER`, `HOME`, `SHELL`, `GITLAB_WORKER_ID`, `PROCESS_NAME` — must still not match any denylist substring).

- [ ] **Step 5: Commit**

```bash
git add exporter/collector.go exporter/collector_test.go
git commit -m "feat(exporter): harden environ scrubbing with expanded denylist and value redaction"
```

---

### Task 9: Wire kube scraper + cgroup read into main

**Files:**
- Modify: `main.go` (flags ~lines 31-48; `scrape` ~lines 197-295; `startScraper` ~lines 182-195; `main` body ~lines 97-127)
- Modify: `main_test.go` (calls at lines 152, 170)

**Interfaces:**
- Consumes: `exporter.InCluster`, `exporter.NewKubeStore`, `exporter.NewKubeletClient`, `(*KubeletClient).Pods/BaseURL`, `exporter.NewKubeCollector`, `exporter.PodUIDFromCgroup`.
- Produces: `startKubeScraper(kubeStore *exporter.KubeStore, client *exporter.KubeletClient, interval time.Duration)`; updated `scrape(store, procCache, inCluster bool)` and `startScraper(store, interval, inCluster bool)` signatures.

- [ ] **Step 1: Add the kubelet-insecure flag**

In `main.go` `main()`, after the existing flag declarations (before `flag.Parse()`), add:

```go
	kubeletInsecure := flag.Bool("kubelet-insecure", true,
		"Skip TLS verification when querying the node-local kubelet (in-cluster only)")
```

- [ ] **Step 2: Thread `inCluster` through the scraper**

Change `startScraper` and `scrape` signatures and the cgroup read. Replace `startScraper` (lines ~182-195):

```go
// Background scraper with process object reuse for CPU calculation accuracy
func startScraper(store *exporter.HistoryStore, interval time.Duration, inCluster bool) {
	procCache := make(map[int32]*process.Process)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	scrape(store, procCache, inCluster)

	for range ticker.C {
		scrape(store, procCache, inCluster)
	}
}
```

Change the `scrape` signature line (was `func scrape(store *exporter.HistoryStore, procCache map[int32]*process.Process) {`) to:

```go
func scrape(store *exporter.HistoryStore, procCache map[int32]*process.Process, inCluster bool) {
```

Inside `scrape`, immediately before the `store.AddSample(exporter.ProcessSample{` call, add the cgroup read:

```go
		// Resolve the owning pod UID from cgroup (in-cluster only).
		podUID := ""
		if inCluster {
			if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid)); err == nil {
				podUID = exporter.PodUIDFromCgroup(string(data))
			}
		}
```

Then add `PodUID: podUID,` to the `ProcessSample` literal (right after the `Name: name,` line).

- [ ] **Step 3: Add the kube scraper function**

Add to `main.go` (e.g. after `startScraper`):

```go
// startKubeScraper polls the kubelet for node-local pod resource requests.
func startKubeScraper(kubeStore *exporter.KubeStore, client *exporter.KubeletClient, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	update := func() {
		pods, err := client.Pods()
		if err != nil {
			log.Printf("kube: scrape error: %v", err)
			return
		}
		kubeStore.Replace(pods)
	}

	update()
	for range ticker.C {
		update()
	}
}
```

- [ ] **Step 4: Wire it into `main()`**

In `main.go`, replace the block that starts the scraper and registers the collector (lines ~97-104) with:

```go
	store := exporter.NewHistoryStore()

	inCluster := exporter.InCluster()

	// Start background scraping thread
	go startScraper(store, *scrapeInterval, inCluster)

	// Register Prometheus custom collector
	collector := exporter.NewProcessCollector(store)
	prometheus.MustRegister(collector)

	// When running inside Kubernetes, also export per-job pod resource requests.
	if inCluster {
		kubeStore := exporter.NewKubeStore()
		client, err := exporter.NewKubeletClient(*kubeletInsecure)
		if err != nil {
			log.Printf("kube: disabled (kubelet client init failed: %v)", err)
		} else {
			go startKubeScraper(kubeStore, client, *scrapeInterval)
			prometheus.MustRegister(exporter.NewKubeCollector(store, kubeStore))
			log.Printf("kube: enabled, polling kubelet at %s", client.BaseURL())
		}
	}
```

- [ ] **Step 5: Update `main_test.go` call sites**

In `main_test.go`, change line 152 `scrape(store, cache)` to:

```go
	scrape(store, cache, false)
```

and line 170 `go startScraper(store, 10*time.Millisecond)` to:

```go
	go startScraper(store, 10*time.Millisecond, false)
```

- [ ] **Step 6: Build and test the whole module**

Run: `go build ./... && go test ./...`
Expected: build succeeds; all tests PASS.

- [ ] **Step 7: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: enable Kubernetes job-resource metrics when running in-cluster"
```

---

### Task 10: Docs — README, RBAC, DaemonSet env

**Files:**
- Modify: `README.md`

**Interfaces:** none (documentation only).

- [ ] **Step 1: Add a Kubernetes section to the README**

Append a section to `README.md` documenting the feature. Use this content:

```markdown
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
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: document Kubernetes job-resource metrics and hardened scrubbing"
```

---

## Self-Review notes

- **Spec coverage:** in-cluster detection (T4), kubelet client (T5), quantity parser (T2), cgroup→UID (T3), `ProcessSample.PodUID` (T1), KubeStore (T6), KubeCollector w/ dedup (T7), environ hardening (T8), main wiring + gated cgroup read (T9), README/RBAC/TLS (T10). All spec sections mapped.
- **Type consistency:** `KubePodInfo{UID, CPURequest float64, MemRequest float64}` used identically in T5/T6/T7. `KubeStore.Get` returns `(KubePodInfo, bool)` in T6 and is consumed that way in T7. `PodUIDFromCgroup(string) string` (T3) consumed in T9. `inCluster bool` param threaded through `startScraper`/`scrape` in T9 with both `main_test.go` call sites updated.
- **No placeholders:** every code step contains full code; every run step has an exact command and expected result.
```
