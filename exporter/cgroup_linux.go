//go:build linux

package exporter

import (
	"fmt"
	"os"
)

// ProcessPodUID extracts the Kubernetes pod UID for the given PID on Linux
// by reading /proc/<pid>/cgroup and calling PodUIDFromCgroup.
// Returns an empty string if reading fails or no pod UID is found.
func ProcessPodUID(pid int32) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return ""
	}
	return PodUIDFromCgroup(string(data))
}
