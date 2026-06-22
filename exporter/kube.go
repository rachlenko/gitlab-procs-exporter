package exporter

import (
	"fmt"
	"os"
	"regexp"
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
