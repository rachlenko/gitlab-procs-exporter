package exporter

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
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
