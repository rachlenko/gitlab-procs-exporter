//go:build !linux && !windows

package exporter

// ProcessPodUID returns an empty string on non-Linux and non-Windows platforms.
func ProcessPodUID(pid int32) string {
	return ""
}
