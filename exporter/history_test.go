package exporter

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestNewHistoryStore(t *testing.T) {
	hs := NewHistoryStore()
	if hs == nil {
		t.Fatal("expected non-nil HistoryStore")
	}
	if len(hs.processes) != 0 {
		t.Errorf("expected empty processes map, got %d items", len(hs.processes))
	}
	if len(hs.activeKeys) != 0 {
		t.Errorf("expected empty activeKeys map, got %d items", len(hs.activeKeys))
	}
}

func TestAddSample(t *testing.T) {
	hs := NewHistoryStore()
	now := time.Now()

	sample := ProcessSample{
		Timestamp:  now,
		PID:        1001,
		Name:       "worker-process",
		CmdLine:    "./worker --daemon",
		Environ:    map[string]string{"ENV": "production"},
		CPUUsage:   12.5,
		MemoryRSS:  1024 * 1024 * 50,
		MemoryVMS:  1024 * 1024 * 100,
		IORead:     5000,
		IOWrite:    2000,
		CreateTime: 123456789,
		IsActive:   true,
	}

	hs.AddSample(sample)

	key := fmt.Sprintf("%d-%d", sample.PID, sample.CreateTime)
	samples, ok := hs.processes[key]
	if !ok {
		t.Fatalf("expected key %q to exist in processes map", key)
	}

	if len(samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(samples))
	}

	saved := samples[0]
	if saved.PID != sample.PID || saved.Name != sample.Name || saved.CPUUsage != sample.CPUUsage {
		t.Errorf("saved sample data mismatch: %+v", saved)
	}

	activeKey, ok := hs.activeKeys[sample.PID]
	if !ok || activeKey != key {
		t.Errorf("activeKeys key mapping mismatch: got %q, expected %q", activeKey, key)
	}
}

func TestMarkInactive(t *testing.T) {
	hs := NewHistoryStore()
	now := time.Now()

	sample1 := ProcessSample{Timestamp: now, PID: 1001, Name: "proc-1", CreateTime: 100, IsActive: true}
	sample2 := ProcessSample{Timestamp: now, PID: 1002, Name: "proc-2", CreateTime: 200, IsActive: true}

	hs.AddSample(sample1)
	hs.AddSample(sample2)

	// Scrape only sees PID 1001 as active
	activeMap := map[int32]bool{1001: true}
	hs.MarkInactive(activeMap)

	// Check sample1 is still active
	key1 := "1001-100"
	s1 := hs.processes[key1]
	if len(s1) == 0 || !s1[len(s1)-1].IsActive {
		t.Error("expected process 1001 to remain active")
	}

	// Check sample2 was marked inactive
	key2 := "1002-200"
	s2 := hs.processes[key2]
	if len(s2) == 0 || s2[len(s2)-1].IsActive {
		t.Error("expected process 1002 to be marked inactive")
	}

	// Check sample2 has been removed from activeKeys list
	if _, active := hs.activeKeys[1002]; active {
		t.Error("expected process 1002 to be deleted from activeKeys map")
	}
}

func TestGetActiveProcesses(t *testing.T) {
	hs := NewHistoryStore()
	now := time.Now()

	sample1 := ProcessSample{Timestamp: now, PID: 1001, Name: "proc-1", CreateTime: 100, IsActive: true}
	sample2 := ProcessSample{Timestamp: now, PID: 1002, Name: "proc-2", CreateTime: 200, IsActive: true}

	hs.AddSample(sample1)
	hs.AddSample(sample2)

	active := hs.GetActiveProcesses()
	if len(active) != 2 {
		t.Fatalf("expected 2 active processes, got %d", len(active))
	}

	// Mark 1002 inactive
	hs.MarkInactive(map[int32]bool{1001: true})
	active = hs.GetActiveProcesses()
	if len(active) != 1 {
		t.Fatalf("expected 1 active process after mark inactive, got %d", len(active))
	}
	if active[0].PID != 1001 {
		t.Errorf("expected PID 1001 to be active, got %d", active[0].PID)
	}
}

func TestQueryHistory(t *testing.T) {
	hs := NewHistoryStore()
	now := time.Now()

	sample1 := ProcessSample{Timestamp: now, PID: 1001, Name: "Gitlab-Runner", CreateTime: 100, IsActive: true}
	sample2 := ProcessSample{Timestamp: now, PID: 1002, Name: "puma-server", CreateTime: 200, IsActive: true}

	hs.AddSample(sample1)
	hs.AddSample(sample2)

	// Search by PID
	resPID := hs.QueryHistory("pid", "1001")
	if len(resPID) != 1 {
		t.Fatalf("expected 1 result for PID query, got %d", len(resPID))
	}
	if _, ok := resPID["1001-100"]; !ok {
		t.Error("expected key '1001-100' in search results")
	}

	// Search by Name (case-insensitive & substring)
	resName := hs.QueryHistory("name", "runner")
	if len(resName) != 1 {
		t.Fatalf("expected 1 result for Name query 'runner', got %d", len(resName))
	}
	if _, ok := resName["1001-100"]; !ok {
		t.Error("expected key '1001-100' in search results")
	}

	// Search non-existent
	resNone := hs.QueryHistory("name", "nginx")
	if len(resNone) != 0 {
		t.Errorf("expected 0 results, got %d", len(resNone))
	}
}

func TestPruneExpiredSamples(t *testing.T) {
	hs := NewHistoryStore()
	now := time.Now()

	// Add historical samples
	key := "1001-100"
	hs.processes[key] = []ProcessSample{
		{Timestamp: now.Add(-15 * time.Minute), PID: 1001, CreateTime: 100}, // Expired
		{Timestamp: now.Add(-5 * time.Minute), PID: 1001, CreateTime: 100},  // Valid
		{Timestamp: now, PID: 1001, CreateTime: 100},                        // Valid
	}

	hs.pruneExpiredSamples(key)

	samples := hs.processes[key]
	if len(samples) != 2 {
		t.Fatalf("expected 2 valid samples remaining, got %d", len(samples))
	}

	for _, s := range samples {
		if s.Timestamp.Before(now.Add(-10 * time.Minute)) {
			t.Errorf("found expired sample remaining in cache: %s", s.Timestamp)
		}
	}
}

func TestPruneAll(t *testing.T) {
	hs := NewHistoryStore()
	now := time.Now()

	hs.processes["1001-100"] = []ProcessSample{
		{Timestamp: now.Add(-12 * time.Minute), PID: 1001, CreateTime: 100},
	}
	hs.processes["1002-200"] = []ProcessSample{
		{Timestamp: now.Add(-5 * time.Minute), PID: 1002, CreateTime: 200},
		{Timestamp: now, PID: 1002, CreateTime: 200},
	}

	hs.pruneAll()

	if _, ok := hs.processes["1001-100"]; ok {
		t.Error("expected process 1001 key to be completely pruned since all samples expired")
	}

	s2, ok := hs.processes["1002-200"]
	if !ok || len(s2) != 2 {
		t.Errorf("expected key '1002-200' to be preserved, got size %d", len(s2))
	}
}

func TestConcurrentSafety(t *testing.T) {
	hs := NewHistoryStore()
	var wg sync.WaitGroup
	workers := 20
	iterations := 50

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				pid := int32(1000 + workerID) //nolint:gosec // G115: workerID is bounded by the test loop
				sample := ProcessSample{
					Timestamp:  time.Now(),
					PID:        pid,
					Name:       fmt.Sprintf("worker-%d", workerID),
					CreateTime: 100,
				}
				hs.AddSample(sample)
				_ = hs.GetActiveProcesses()
				_ = hs.QueryHistory("name", "worker")
				hs.MarkInactive(map[int32]bool{pid: true})
			}
		}(i)
	}

	wg.Wait()
}
