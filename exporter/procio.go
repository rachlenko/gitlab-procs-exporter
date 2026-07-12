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

// SelfIO returns the storage-layer bytes read and written by pid's own threads.
//
// /proc/<pid>/io covers the thread group *plus* signal->ioac, and the kernel
// folds a child's accounting into its parent's signal->ioac when the parent
// reaps it (wait_task_zombie). That fold is recursive, so a long-lived reaper —
// pid 1, or a CI job's shell — accumulates the I/O of every process that ever
// exited beneath it. Such a process dominates any topk() over the process-wide
// counter while never having touched the disk itself. /proc/<pid>/task/<tid>/io
// is per-thread and carries no signal->ioac, so summing it over the live
// threads attributes the bytes to whoever actually issued them.
//
// The tradeoff is the mirror image: bytes written by a thread or a child that
// has already exited belong to no live task and are invisible here, so a
// process that lives and dies between two scrapes contributes nothing at all.
// Neither view is complete on its own — callers wanting the total that reached
// the disk must keep reading the process-wide counter.
func SelfIO(procRoot string, pid int32) (read, write uint64, err error) {
	taskDir := filepath.Join(procRoot, strconv.Itoa(int(pid)), "task")
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		return 0, 0, err
	}

	live := false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		r, w, err := parseProcIO(filepath.Join(taskDir, e.Name(), "io"))
		if err != nil {
			// A thread that exits mid-walk is expected; anything else (EACCES
			// above all) would leave us summing an arbitrary subset of the
			// threads, which is worse than reporting nothing.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return 0, 0, err
		}
		read += r
		write += w
		live = true
	}
	if !live {
		return 0, 0, fmt.Errorf("pid %d: %w", pid, ErrNoLiveTask)
	}
	return read, write, nil
}

// parseProcIO extracts read_bytes and write_bytes from a /proc io accounting
// file. Unknown keys are skipped so a kernel that grows the format stays
// readable; a key present twice would be a kernel bug and the last wins.
func parseProcIO(path string) (read, write uint64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), ": ")
		if !ok {
			continue
		}
		var target *uint64
		switch key {
		case "read_bytes":
			target = &read
		case "write_bytes":
			target = &write
		default:
			continue
		}
		n, err := strconv.ParseUint(val, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("%s: parsing %q: %w", path, key, err)
		}
		*target = n
	}
	if err := sc.Err(); err != nil {
		return 0, 0, fmt.Errorf("%s: %w", path, err)
	}
	return read, write, nil
}
