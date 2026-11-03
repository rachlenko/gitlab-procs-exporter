package exporter

import (
	"testing"
)

func TestProcessPodUID(t *testing.T) {
	// For negative or nonexistent PIDs, ProcessPodUID must safely return ""
	// without error or panic across platforms.
	testPIDs := []int32{-1, 0, 999999999}
	for _, pid := range testPIDs {
		if got := ProcessPodUID(pid); got != "" {
			t.Errorf("ProcessPodUID(%d) = %q, want empty string", pid, got)
		}
	}
}
