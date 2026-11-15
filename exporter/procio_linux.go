//go:build linux

package exporter

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

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
	f, err := os.Open(taskDir)
	if err != nil {
		return IOStats{}, err
	}
	entries, err := f.ReadDir(-1)
	_ = f.Close()
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

	var buf [512]byte
	n, err := f.Read(buf[:])
	if err != nil && !errors.Is(err, io.EOF) {
		return IOStats{}, fmt.Errorf("%s: %w", path, err)
	}

	data := buf[:n]
	if n == len(buf) {
		rest, readErr := io.ReadAll(f)
		if readErr != nil {
			return IOStats{}, fmt.Errorf("%s: %w", path, readErr)
		}
		data = append(buf[:n], rest...)
	}

	return parseIOBytes(data, path)
}

func parseIOBytes(data []byte, path string) (IOStats, error) {
	var st IOStats
	for len(data) > 0 {
		nl := bytes.IndexByte(data, '\n')
		var line []byte
		if nl >= 0 {
			line = data[:nl]
			data = data[nl+1:]
		} else {
			line = data
			data = nil
		}
		line = bytes.TrimRight(line, "\r")
		colon := bytes.Index(line, []byte(": "))
		if colon < 0 {
			continue
		}
		key := line[:colon]
		val := line[colon+2:]

		var target *uint64
		switch string(key) {
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

		n, err := parseUintBytes(val)
		if err != nil {
			return IOStats{}, fmt.Errorf("%s: parsing %q: %w", path, string(key), err)
		}
		*target = n
	}
	return st, nil
}

func parseUintBytes(b []byte) (uint64, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return 0, errors.New("empty integer")
	}
	var n uint64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, errors.New("invalid digit")
		}
		n = n*10 + uint64(c-'0')
	}
	return n, nil
}
