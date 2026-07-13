package exporter

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrNoLiveTask means pid had no readable per-thread I/O file: every thread
// exited while the directory was being walked, i.e. the process is gone.
var ErrNoLiveTask = errors.New("no live task with readable io accounting")

// IOStats is one process's I/O accounting.
//
// ReadBytes/WriteBytes are storage-layer bytes: what actually crossed to (or
// from) the block device. ReadSyscalls/WriteSyscalls are the raw read(2) and
// write(2) call counts (/proc's syscr and syscw) — they are NOT device IOPS.
// A write() may be absorbed by the page cache and later merged with others into
// a single block operation, or split across several; the kernel does not
// attribute block operations back to the process that dirtied the page. These
// counts are the closest per-process proxy available from /proc, and they are
// what a process's I/O *call rate* looks like, not what the disk does.
type IOStats struct {
	ReadBytes     uint64
	WriteBytes    uint64
	ReadSyscalls  uint64
	WriteSyscalls uint64
}

// add accumulates one thread's counters.
func (s *IOStats) add(o IOStats) {
	s.ReadBytes += o.ReadBytes
	s.WriteBytes += o.WriteBytes
	s.ReadSyscalls += o.ReadSyscalls
	s.WriteSyscalls += o.WriteSyscalls
}

// SelfIO returns the I/O issued by pid's own threads.
//
// /proc/<pid>/io covers the thread group *plus* signal->ioac, and the kernel
// folds a child's accounting into its parent's signal->ioac when the parent
// reaps it (wait_task_zombie). That fold is recursive, so a long-lived reaper —
// pid 1, or a CI job's shell — accumulates the I/O of every process that ever
// exited beneath it. Such a process dominates any topk() over the process-wide
// counter while never having touched the disk itself. /proc/<pid>/task/<tid>/io
// is per-thread and carries no signal->ioac, so summing it over the live
// threads attributes the I/O to whoever actually issued it.
//
// The tradeoff is the mirror image: I/O by a thread or a child that has already
// exited belongs to no live task and is invisible here, so a process that lives
// and dies between two scrapes contributes nothing at all. Neither view is
// complete on its own — callers wanting the total that reached the disk must
// keep reading the process-wide counter.
func SelfIO(procRoot string, pid int32) (IOStats, error) {
	taskDir := filepath.Join(procRoot, strconv.Itoa(int(pid)), "task")
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		return IOStats{}, err
	}

	var total IOStats
	live := false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		st, err := parseProcIO(filepath.Join(taskDir, e.Name(), "io"))
		if err != nil {
			// A thread that exits mid-walk is expected; anything else (EACCES
			// above all) would leave us summing an arbitrary subset of the
			// threads, which is worse than reporting nothing.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return IOStats{}, err
		}
		total.add(st)
		live = true
	}
	if !live {
		return IOStats{}, fmt.Errorf("pid %d: %w", pid, ErrNoLiveTask)
	}
	return total, nil
}

// parseProcIO extracts the counters we export from a /proc io accounting file.
// Unknown keys are skipped so a kernel that grows the format stays readable; a
// key present twice would be a kernel bug and the last wins.
func parseProcIO(path string) (IOStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return IOStats{}, err
	}
	defer f.Close()

	var st IOStats
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), ": ")
		if !ok {
			continue
		}
		var target *uint64
		switch key {
		case "read_bytes":
			target = &st.ReadBytes
		case "write_bytes":
			target = &st.WriteBytes
		case "syscr":
			target = &st.ReadSyscalls
		case "syscw":
			target = &st.WriteSyscalls
		default:
			continue
		}
		n, err := strconv.ParseUint(val, 10, 64)
		if err != nil {
			return IOStats{}, fmt.Errorf("%s: parsing %q: %w", path, key, err)
		}
		*target = n
	}
	if err := sc.Err(); err != nil {
		return IOStats{}, fmt.Errorf("%s: %w", path, err)
	}
	return st, nil
}
