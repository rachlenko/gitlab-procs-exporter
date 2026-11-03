//go:build windows

package exporter

// SelfIO returns the I/O issued by pid's own threads.
// On Windows, process-level I/O counters (GetProcessIoCounters) measure the target
// process itself rather than reaped child processes, so SelfIO safely returns
// IOStats{}, nil.
func SelfIO(procRoot string, pid int32) (IOStats, error) {
	return IOStats{}, nil
}
