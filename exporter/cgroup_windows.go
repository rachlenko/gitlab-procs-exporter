//go:build windows

package exporter

// ProcessPodUID returns an empty string on Windows where /proc cgroup files
// do not exist.
func ProcessPodUID(pid int32) string {
	return ""
}
