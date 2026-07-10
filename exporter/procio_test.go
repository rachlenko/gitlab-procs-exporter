package exporter

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

// writeTaskIO lays out procRoot/<pid>/task/<tid>/io for each thread.
func writeTaskIO(t *testing.T, procRoot string, pid int32, threads map[string]string) {
	t.Helper()
	for tid, body := range threads {
		dir := filepath.Join(procRoot, strconv.Itoa(int(pid)), "task", tid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "io"), []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
}

// procIOFile renders a realistic /proc/<pid>/task/<tid>/io body.
func procIOFile(readBytes, writeBytes uint64) string {
	return "rchar: 100\n" +
		"wchar: 200\n" +
		"syscr: 3\n" +
		"syscw: 4\n" +
		"read_bytes: " + strconv.FormatUint(readBytes, 10) + "\n" +
		"write_bytes: " + strconv.FormatUint(writeBytes, 10) + "\n" +
		"cancelled_write_bytes: 0\n"
}

func TestSelfIOSumsLiveThreads(t *testing.T) {
	root := t.TempDir()
	writeTaskIO(t, root, 42, map[string]string{
		"42": procIOFile(1000, 2000),
		"57": procIOFile(30, 4000),
	})

	read, write, err := SelfIO(root, 42)
	if err != nil {
		t.Fatalf("SelfIO: %v", err)
	}
	if read != 1030 {
		t.Errorf("read = %d, want 1030", read)
	}
	if write != 6000 {
		t.Errorf("write = %d, want 6000", write)
	}
}

// The whole point of the function: a reaper's per-thread total must not include
// the bytes the kernel folded into /proc/<pid>/io when it reaped a child.
func TestSelfIOIgnoresReapedChildAccounting(t *testing.T) {
	root := t.TempDir()
	// pid 1 has one thread that has never written; the process-wide file (which
	// SelfIO must not consult) claims terabytes inherited from reaped children.
	writeTaskIO(t, root, 1, map[string]string{"1": procIOFile(26492928, 0)})
	pidDir := filepath.Join(root, "1")
	if err := os.WriteFile(filepath.Join(pidDir, "io"), []byte(procIOFile(1994568709120, 27786051437568)), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	read, write, err := SelfIO(root, 1)
	if err != nil {
		t.Fatalf("SelfIO: %v", err)
	}
	if read != 26492928 {
		t.Errorf("read = %d, want 26492928 (own thread only)", read)
	}
	if write != 0 {
		t.Errorf("write = %d, want 0: systemd issued no writes of its own", write)
	}
}

// A thread that exits between ReadDir and the open of its io file is routine on
// a busy node; it must not fail the scrape for the whole process.
func TestSelfIOSkipsThreadThatExitedMidWalk(t *testing.T) {
	root := t.TempDir()
	writeTaskIO(t, root, 7, map[string]string{"7": procIOFile(11, 22)})
	gone := filepath.Join(root, "7", "task", "8")
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	} // no io file inside

	read, write, err := SelfIO(root, 7)
	if err != nil {
		t.Fatalf("SelfIO: %v", err)
	}
	if read != 11 || write != 22 {
		t.Errorf("got read=%d write=%d, want 11/22", read, write)
	}
}

func TestSelfIOReportsNoLiveTask(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "9", "task", "9"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if _, _, err := SelfIO(root, 9); !errors.Is(err, ErrNoLiveTask) {
		t.Errorf("err = %v, want ErrNoLiveTask", err)
	}
}

func TestSelfIOMissingProcess(t *testing.T) {
	if _, _, err := SelfIO(t.TempDir(), 12345); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want os.ErrNotExist", err)
	}
}

// An unreadable thread would otherwise contribute 0 and silently understate the
// process; a partial sum is worse than an error the caller can drop.
func TestSelfIOUnreadableThreadIsAnError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("permission semantics are Linux-specific")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the mode bits")
	}
	root := t.TempDir()
	writeTaskIO(t, root, 3, map[string]string{
		"3": procIOFile(1, 1),
		"4": procIOFile(2, 2),
	})
	locked := filepath.Join(root, "3", "task", "4", "io")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o644) })

	if _, _, err := SelfIO(root, 3); !errors.Is(err, os.ErrPermission) {
		t.Errorf("err = %v, want os.ErrPermission", err)
	}
}

func TestParseProcIOMalformedValue(t *testing.T) {
	root := t.TempDir()
	writeTaskIO(t, root, 5, map[string]string{"5": "read_bytes: nonsense\n"})

	if _, _, err := SelfIO(root, 5); err == nil {
		t.Fatal("want an error on an unparseable counter, got nil")
	}
}

// Unknown keys and a blank trailing line must not derail the parse.
func TestParseProcIOToleratesUnknownKeys(t *testing.T) {
	root := t.TempDir()
	writeTaskIO(t, root, 6, map[string]string{
		"6": "some_future_field: 9\nread_bytes: 5\nwrite_bytes: 6\n\n",
	})

	read, write, err := SelfIO(root, 6)
	if err != nil {
		t.Fatalf("SelfIO: %v", err)
	}
	if read != 5 || write != 6 {
		t.Errorf("got read=%d write=%d, want 5/6", read, write)
	}
}
