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
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("MkdirAll(%s): %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "io"), []byte(body), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
}

// procIOFile renders a realistic /proc/<pid>/task/<tid>/io body.
func procIOFile(st IOStats) string {
	u := strconv.FormatUint
	return "rchar: 100\n" +
		"wchar: 200\n" +
		"syscr: " + u(st.ReadSyscalls, 10) + "\n" +
		"syscw: " + u(st.WriteSyscalls, 10) + "\n" +
		"read_bytes: " + u(st.ReadBytes, 10) + "\n" +
		"write_bytes: " + u(st.WriteBytes, 10) + "\n" +
		"cancelled_write_bytes: 0\n"
}

func TestSelfIOSumsLiveThreads(t *testing.T) {
	root := t.TempDir()
	writeTaskIO(t, root, 42, map[string]string{
		"42": procIOFile(IOStats{ReadBytes: 1000, WriteBytes: 2000, ReadSyscalls: 7, WriteSyscalls: 11}),
		"57": procIOFile(IOStats{ReadBytes: 30, WriteBytes: 4000, ReadSyscalls: 3, WriteSyscalls: 4}),
	})

	got, err := SelfIO(root, 42)
	if err != nil {
		t.Fatalf("SelfIO: %v", err)
	}
	want := IOStats{ReadBytes: 1030, WriteBytes: 6000, ReadSyscalls: 10, WriteSyscalls: 15}
	if got != want {
		t.Errorf("SelfIO = %+v, want %+v", got, want)
	}
}

// The whole point of the function: a reaper's per-thread total must not include
// the accounting the kernel folded into /proc/<pid>/io when it reaped a child.
func TestSelfIOIgnoresReapedChildAccounting(t *testing.T) {
	root := t.TempDir()
	// pid 1 has one thread that has never written; the process-wide file (which
	// SelfIO must not consult) claims terabytes inherited from reaped children.
	own := IOStats{ReadBytes: 26492928, WriteBytes: 0, ReadSyscalls: 4211, WriteSyscalls: 0}
	writeTaskIO(t, root, 1, map[string]string{"1": procIOFile(own)})
	reaped := IOStats{ReadBytes: 1994568709120, WriteBytes: 27786051437568, ReadSyscalls: 20912506972, WriteSyscalls: 17180756343}
	if err := os.WriteFile(filepath.Join(root, "1", "io"), []byte(procIOFile(reaped)), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := SelfIO(root, 1)
	if err != nil {
		t.Fatalf("SelfIO: %v", err)
	}
	if got != own {
		t.Errorf("SelfIO = %+v, want %+v: systemd issued no writes of its own", got, own)
	}
}

// A thread that exits between ReadDir and the open of its io file is routine on
// a busy node; it must not fail the scrape for the whole process.
func TestSelfIOSkipsThreadThatExitedMidWalk(t *testing.T) {
	root := t.TempDir()
	live := IOStats{ReadBytes: 11, WriteBytes: 22, ReadSyscalls: 1, WriteSyscalls: 2}
	writeTaskIO(t, root, 7, map[string]string{"7": procIOFile(live)})
	if err := os.MkdirAll(filepath.Join(root, "7", "task", "8"), 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	} // no io file inside

	got, err := SelfIO(root, 7)
	if err != nil {
		t.Fatalf("SelfIO: %v", err)
	}
	if got != live {
		t.Errorf("SelfIO = %+v, want %+v", got, live)
	}
}

func TestSelfIOReportsNoLiveTask(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "9", "task", "9"), 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if _, err := SelfIO(root, 9); !errors.Is(err, ErrNoLiveTask) {
		t.Errorf("err = %v, want ErrNoLiveTask", err)
	}
}

func TestSelfIOMissingProcess(t *testing.T) {
	if _, err := SelfIO(t.TempDir(), 12345); !errors.Is(err, os.ErrNotExist) {
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
		"3": procIOFile(IOStats{ReadBytes: 1, WriteBytes: 1}),
		"4": procIOFile(IOStats{ReadBytes: 2, WriteBytes: 2}),
	})
	locked := filepath.Join(root, "3", "task", "4", "io")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o600) })

	if _, err := SelfIO(root, 3); !errors.Is(err, os.ErrPermission) {
		t.Errorf("err = %v, want os.ErrPermission", err)
	}
}

func TestParseProcIOMalformedValue(t *testing.T) {
	root := t.TempDir()
	writeTaskIO(t, root, 5, map[string]string{"5": "read_bytes: nonsense\n"})

	if _, err := SelfIO(root, 5); err == nil {
		t.Fatal("want an error on an unparseable counter, got nil")
	}
}

// Unknown keys and a blank trailing line must not derail the parse.
func TestParseProcIOToleratesUnknownKeys(t *testing.T) {
	root := t.TempDir()
	writeTaskIO(t, root, 6, map[string]string{
		"6": "some_future_field: 9\nread_bytes: 5\nwrite_bytes: 6\nsyscr: 2\nsyscw: 3\n\n",
	})

	got, err := SelfIO(root, 6)
	if err != nil {
		t.Fatalf("SelfIO: %v", err)
	}
	want := IOStats{ReadBytes: 5, WriteBytes: 6, ReadSyscalls: 2, WriteSyscalls: 3}
	if got != want {
		t.Errorf("SelfIO = %+v, want %+v", got, want)
	}
}
