package exporter

import (
	"errors"
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
