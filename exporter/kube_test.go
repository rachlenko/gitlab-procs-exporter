package exporter

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

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
