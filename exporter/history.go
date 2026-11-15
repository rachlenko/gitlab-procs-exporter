package exporter

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// formatProcessKey formats "<pid>-<createtime>" using a stack buffer to avoid heap allocations.
func formatProcessKey(pid int32, createTime int64) string {
	var buf [32]byte
	b := strconv.AppendInt(buf[:0], int64(pid), 10)
	b = append(b, '-')
	b = strconv.AppendInt(b, createTime, 10)
	return string(b)
}

func compactMap[M ~map[K]V, K comparable, V any](m M) M {
	compacted := make(M, len(m))
	for k, v := range m {
		compacted[k] = v
	}
	return compacted
}

// ProcessSample represents the state of a process at a specific point in time.
type ProcessSample struct {
	Timestamp  time.Time         `json:"timestamp"`
	PID        int32             `json:"pid"`
	Name       string            `json:"name"`
	PodUID     string            `json:"pod_uid,omitempty"` // Kubernetes pod UID (empty outside a cluster)
	CmdLine    string            `json:"cmdline"`
	Environ    map[string]string `json:"environ"`
	CPUUsage   float64           `json:"cpu_usage"`      // CPU percentage usage
	CPUSeconds float64           `json:"cpu_seconds"`    // Cumulative user+system CPU seconds
	MemoryRSS  uint64            `json:"memory_rss"`     // RSS in bytes
	MemoryVMS  uint64            `json:"memory_vms"`     // Virtual memory in bytes
	IORead     uint64            `json:"io_read_bytes"`  // Cumulative bytes read, including by reaped children
	IOWrite    uint64            `json:"io_write_bytes"` // Cumulative bytes written, including by reaped children
	// IOReadSyscalls and IOWriteSyscalls are read(2)/write(2) call counts — not
	// device IOPS; see IOStats. Like the byte counters above, they include every
	// descendant this process has reaped.
	IOReadSyscalls  uint64 `json:"io_read_syscalls"`
	IOWriteSyscalls uint64 `json:"io_write_syscalls"`
	// The Self* counters cover only this process's own threads; see SelfIO.
	IOReadSelf          uint64 `json:"io_read_bytes_self"`
	IOWriteSelf         uint64 `json:"io_write_bytes_self"`
	IOReadSyscallsSelf  uint64 `json:"io_read_syscalls_self"`
	IOWriteSyscallsSelf uint64 `json:"io_write_syscalls_self"`
	CreateTime          int64  `json:"create_time"` // Process start time (epoch milliseconds)
	IsActive            bool   `json:"is_active"`   // Whether the process is currently running
}

// HistoryStore maintains process telemetry history for the last 10 minutes.
type HistoryStore struct {
	mu sync.RWMutex
	// Maps process key ("pid-createtime") to historical samples
	processes map[string][]ProcessSample
	// Maps active PID to the current active process key
	activeKeys map[int32]string
}

// NewHistoryStore creates an initialized HistoryStore.
func NewHistoryStore() *HistoryStore {
	return &HistoryStore{
		processes:  make(map[string][]ProcessSample),
		activeKeys: make(map[int32]string),
	}
}

// AddSample appends a new sample to the store and prunes samples older than 10 minutes.
func (hs *HistoryStore) AddSample(sample ProcessSample) {
	hs.mu.Lock()
	defer hs.mu.Unlock()

	var key string
	if existingKey, ok := hs.activeKeys[sample.PID]; ok {
		if samples := hs.processes[existingKey]; len(samples) > 0 && samples[len(samples)-1].CreateTime == sample.CreateTime {
			key = existingKey
		}
	}
	if key == "" {
		key = formatProcessKey(sample.PID, sample.CreateTime)
		hs.activeKeys[sample.PID] = key
	}

	hs.processes[key] = append(hs.processes[key], sample)

	samples := hs.processes[key]
	cutoff := time.Now().Add(-10 * time.Minute)
	if !samples[0].Timestamp.Before(cutoff) {
		return
	}

	hs.pruneExpiredSamples(key)
}

// MarkInactive marks processes that were not seen in the current scrape as inactive.
func (hs *HistoryStore) MarkInactive(activePids map[int32]bool) {
	hs.mu.Lock()
	defer hs.mu.Unlock()

	deletedPids := 0
	initialActiveCount := len(hs.activeKeys)
	for pid, key := range hs.activeKeys {
		if !activePids[pid] {
			// Process exited, mark as inactive in its latest sample
			samples := hs.processes[key]
			if len(samples) > 0 {
				samples[len(samples)-1].IsActive = false
			}
			delete(hs.activeKeys, pid)
			deletedPids++
		}
	}
	if deletedPids > 0 && len(hs.activeKeys)*2 < initialActiveCount {
		hs.activeKeys = compactMap(hs.activeKeys)
	}

	// Also prune all historical records older than 10 minutes globally
	hs.pruneAll()
}

// GetActiveProcesses returns a list of the most recent samples for all active processes.
func (hs *HistoryStore) GetActiveProcesses() []ProcessSample {
	hs.mu.RLock()
	defer hs.mu.RUnlock()

	active := make([]ProcessSample, 0, len(hs.activeKeys))
	for _, key := range hs.activeKeys {
		samples := hs.processes[key]
		if len(samples) > 0 {
			active = append(active, samples[len(samples)-1])
		}
	}
	return active
}

// QueryHistory returns the historical samples (up to 10 minutes) for a given query (PID or Name).
func (hs *HistoryStore) QueryHistory(queryType string, value string) map[string][]ProcessSample {
	hs.mu.RLock()
	defer hs.mu.RUnlock()

	result := make(map[string][]ProcessSample)

	for key, samples := range hs.processes {
		if len(samples) == 0 {
			continue
		}
		latest := samples[len(samples)-1]

		match := false
		if queryType == "pid" && strconv.Itoa(int(latest.PID)) == value {
			match = true
		} else if queryType == "name" && stringsContainsIgnoreCase(latest.Name, value) {
			match = true
		}

		if match {
			// Copy: the store mutates its own slices (MarkInactive flips
			// IsActive on the last element) after the read lock is released.
			out := make([]ProcessSample, len(samples))
			copy(out, samples)
			result[key] = out
		}
	}

	return result
}

// Helper to check substring case-insensitively
func stringsContainsIgnoreCase(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// Internal pruning for a single process key
func (hs *HistoryStore) pruneExpiredSamples(key string) {
	hs.pruneKey(key, time.Now().Add(-10*time.Minute))
}

// Internal global pruning
func (hs *HistoryStore) pruneAll() {
	cutoff := time.Now().Add(-10 * time.Minute)
	deletedKeys := 0
	initialCount := len(hs.processes)
	for key := range hs.processes {
		if hs.pruneKey(key, cutoff) {
			deletedKeys++
		}
	}
	if deletedKeys > 0 && len(hs.processes)*2 < initialCount {
		hs.processes = compactMap(hs.processes)
	}
}

// pruneKey prunes expired samples for key in-place using cutoff.
// Returns true if the key was deleted from hs.processes.
func (hs *HistoryStore) pruneKey(key string, cutoff time.Time) bool {
	samples := hs.processes[key]
	if len(samples) == 0 {
		delete(hs.processes, key)
		return true
	}

	if !samples[0].Timestamp.Before(cutoff) {
		return false
	}

	k := 0
	for k < len(samples) && samples[k].Timestamp.Before(cutoff) {
		k++
	}

	if k == len(samples) {
		for i := range samples {
			samples[i] = ProcessSample{}
		}
		delete(hs.processes, key)
		return true
	}

	copy(samples, samples[k:])
	tail := samples[len(samples)-k:]
	for i := range tail {
		tail[i] = ProcessSample{}
	}
	hs.processes[key] = samples[:len(samples)-k]
	return false
}
