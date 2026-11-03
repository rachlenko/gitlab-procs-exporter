//go:build !linux && !windows

package exporter

// SelfIO returns empty IOStats on unsupported platforms.
func SelfIO(procRoot string, pid int32) (IOStats, error) {
	return IOStats{}, nil
}
