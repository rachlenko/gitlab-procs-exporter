package exporter

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestNewHistoryStore(t *testing.T) {
	hs := NewHistoryStore()
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

func TestPruneInPlaceBoundaryConditions(t *testing.T) {
	now := time.Now()

	t.Run("all expired", func(t *testing.T) {
		hs := NewHistoryStore()
		key := "100-1"
		hs.processes[key] = []ProcessSample{
			{Timestamp: now.Add(-20 * time.Minute), PID: 100, CreateTime: 1},
			{Timestamp: now.Add(-15 * time.Minute), PID: 100, CreateTime: 1},
			{Timestamp: now.Add(-11 * time.Minute), PID: 100, CreateTime: 1},
		}

		hs.pruneExpiredSamples(key)

		if _, exists := hs.processes[key]; exists {
			t.Errorf("expected key %s to be deleted when all samples expired", key)
		}
	})

	t.Run("none expired", func(t *testing.T) {
		hs := NewHistoryStore()
		key := "100-2"
		hs.processes[key] = []ProcessSample{
			{Timestamp: now.Add(-8 * time.Minute), PID: 100, CreateTime: 2},
			{Timestamp: now.Add(-5 * time.Minute), PID: 100, CreateTime: 2},
			{Timestamp: now.Add(-1 * time.Minute), PID: 100, CreateTime: 2},
		}

		hs.pruneExpiredSamples(key)

		samples, exists := hs.processes[key]
		if !exists {
			t.Fatalf("expected key %s to remain present", key)
		}
		if len(samples) != 3 {
			t.Fatalf("expected 3 samples remaining, got %d", len(samples))
		}
		if samples[0].Timestamp.Before(now.Add(-10 * time.Minute)) {
			t.Errorf("unexpected expired sample: %+v", samples[0])
		}
	})

	t.Run("partial expiration", func(t *testing.T) {
		hs := NewHistoryStore()
		key := "100-3"
		t1 := now.Add(-25 * time.Minute)
		t2 := now.Add(-15 * time.Minute)
		t3 := now.Add(-5 * time.Minute)
		t4 := now.Add(-2 * time.Minute)
		t5 := now

		hs.processes[key] = []ProcessSample{
			{Timestamp: t1, PID: 100, Name: "s1", CreateTime: 3},
			{Timestamp: t2, PID: 100, Name: "s2", CreateTime: 3},
			{Timestamp: t3, PID: 100, Name: "s3", CreateTime: 3},
			{Timestamp: t4, PID: 100, Name: "s4", CreateTime: 3},
			{Timestamp: t5, PID: 100, Name: "s5", CreateTime: 3},
		}

		hs.pruneExpiredSamples(key)

		samples, exists := hs.processes[key]
		if !exists {
			t.Fatalf("expected key %s to exist", key)
		}
		if len(samples) != 3 {
			t.Fatalf("expected 3 valid samples, got %d", len(samples))
		}
		if samples[0].Name != "s3" || samples[1].Name != "s4" || samples[2].Name != "s5" {
			t.Errorf("unexpected sample names: got %q, %q, %q", samples[0].Name, samples[1].Name, samples[2].Name)
		}
	})
}

func TestPruneZeroedTail(t *testing.T) {
	now := time.Now()
	hs := NewHistoryStore()
	key := "42-100"

	// Allocate a slice with capacity 6
	samples := make([]ProcessSample, 6)
	samples[0] = ProcessSample{Timestamp: now.Add(-20 * time.Minute), PID: 42, Name: "p0", Environ: map[string]string{"K0": "V0"}}
	samples[1] = ProcessSample{Timestamp: now.Add(-15 * time.Minute), PID: 42, Name: "p1", Environ: map[string]string{"K1": "V1"}}
	samples[2] = ProcessSample{Timestamp: now.Add(-8 * time.Minute), PID: 42, Name: "p2", Environ: map[string]string{"K2": "V2"}}
	samples[3] = ProcessSample{Timestamp: now.Add(-5 * time.Minute), PID: 42, Name: "p3", Environ: map[string]string{"K3": "V3"}}
	samples[4] = ProcessSample{Timestamp: now.Add(-2 * time.Minute), PID: 42, Name: "p4", Environ: map[string]string{"K4": "V4"}}
	samples[5] = ProcessSample{Timestamp: now, PID: 42, Name: "p5", Environ: map[string]string{"K5": "V5"}}

	hs.processes[key] = samples

	hs.pruneExpiredSamples(key)

	pruned, ok := hs.processes[key]
	if !ok {
		t.Fatalf("expected key %s to exist", key)
	}
	if len(pruned) != 4 {
		t.Fatalf("expected 4 remaining samples, got %d", len(pruned))
	}
	if cap(pruned) != 6 {
		t.Fatalf("expected backing array capacity 6, got %d", cap(pruned))
	}

	// Verify valid elements were moved to front
	if pruned[0].Name != "p2" || pruned[1].Name != "p3" || pruned[2].Name != "p4" || pruned[3].Name != "p5" {
		t.Errorf("elements incorrectly copied: got %v", pruned)
	}

	// Inspect the tail beyond the resliced length up to capacity
	fullBackingArray := pruned[:cap(pruned)]
	for i := 4; i < 6; i++ {
		tailElem := fullBackingArray[i]
		if tailElem.PID != 0 || tailElem.Name != "" || tailElem.Environ != nil || !tailElem.Timestamp.IsZero() {
			t.Errorf("tail element at index %d was not zeroed out: %+v", i, tailElem)
		}
	}
}

func TestPruneRapidSampleAppendsMemory(t *testing.T) {
	hs := NewHistoryStore()
	now := time.Now()

	// Simulate rapid sample appends: initially populate historical samples
	key := "500-1"
	// Start with 50 expired samples (-20m to -11m) and 50 valid samples (-9m to -1m)
	initial := make([]ProcessSample, 0, 100)
	for i := 0; i < 50; i++ {
		initial = append(initial, ProcessSample{
			Timestamp:  now.Add(-20*time.Minute + time.Duration(i)*10*time.Second),
			PID:        500,
			CreateTime: 1,
			Name:       "rapid-worker",
			Environ:    map[string]string{"WORKER_ID": fmt.Sprintf("%d", i)},
		})
	}
	for i := 50; i < 100; i++ {
		initial = append(initial, ProcessSample{
			Timestamp:  now.Add(-9*time.Minute + time.Duration(i-50)*10*time.Second),
			PID:        500,
			CreateTime: 1,
			Name:       "rapid-worker",
			Environ:    map[string]string{"WORKER_ID": fmt.Sprintf("%d", i)},
		})
	}
	hs.processes[key] = initial
	hs.activeKeys[500] = key

	// Append 2,000 samples rapidly with recent timestamps
	for i := 0; i < 2000; i++ {
		hs.AddSample(ProcessSample{
			Timestamp:  now.Add(time.Duration(i) * time.Millisecond),
			PID:        500,
			CreateTime: 1,
			Name:       "rapid-worker",
			Environ:    map[string]string{"CYCLE": fmt.Sprintf("%d", i)},
		})
	}

	samples := hs.processes[key]
	// All remaining samples must be within the 10-minute window
	cutoff := now.Add(-10 * time.Minute)
	for _, s := range samples {
		if s.Timestamp.Before(cutoff) {
			t.Errorf("found expired sample after rapid appends: %s (cutoff: %s)", s.Timestamp, cutoff)
			break
		}
	}

	// Verify that AddSample when oldest sample is within 10-minute window performs 0 pruning allocations
	allocs := testing.AllocsPerRun(100, func() {
		hs.mu.Lock()
		defer hs.mu.Unlock()
		// Test pruneKey directly on a process where oldest sample is within window
		hs.pruneKey(key, time.Now().Add(-10*time.Minute))
	})
	if allocs > 0 {
		t.Errorf("expected 0 allocations when oldest sample is within window, got %f", allocs)
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

func TestProcessSamplePodUID(t *testing.T) {
	hs := NewHistoryStore()
	hs.AddSample(ProcessSample{
		Timestamp: time.Now(),
		PID:       4242,
		Name:      "ruby",
		PodUID:    "abc-123",
		IsActive:  true,
	})
	active := hs.GetActiveProcesses()
	if len(active) != 1 {
		t.Fatalf("expected 1 active process, got %d", len(active))
	}
	if active[0].PodUID != "abc-123" {
		t.Errorf("expected PodUID %q, got %q", "abc-123", active[0].PodUID)
	}
}

// TestQueryHistoryReturnsCopies guards against QueryHistory handing out the
// store's own backing arrays: MarkInactive mutates the last sample in place
// under the write lock, which would race with (and corrupt) a caller still
// reading the returned slice.
func TestQueryHistoryReturnsCopies(t *testing.T) {
	hs := NewHistoryStore()
	hs.AddSample(ProcessSample{
		Timestamp:  time.Now(),
		PID:        1,
		Name:       "bash",
		CreateTime: 42,
		IsActive:   true,
	})

	got := hs.QueryHistory("pid", "1")
	if len(got) != 1 {
		t.Fatalf("expected 1 timeline, got %d", len(got))
	}

	// Mutates the stored sample's IsActive in place; a shared slice would
	// expose the mutation to the previously returned result.
	hs.MarkInactive(map[int32]bool{})

	for key, samples := range got {
		if !samples[len(samples)-1].IsActive {
			t.Errorf("QueryHistory result for %q was mutated by MarkInactive: expected a copy", key)
		}
	}
}

func BenchmarkPruneExpiredSamplesInPlace(b *testing.B) {
	now := time.Now()
	hs := NewHistoryStore()
	key := "10-100"

	samples := make([]ProcessSample, 20)
	for j := 0; j < 10; j++ {
		samples[j] = ProcessSample{Timestamp: now.Add(-15 * time.Minute), PID: 10, CreateTime: 100}
	}
	for j := 10; j < 20; j++ {
		samples[j] = ProcessSample{Timestamp: now.Add(-5 * time.Minute), PID: 10, CreateTime: 100}
	}

	buf := make([]ProcessSample, 20)
	copy(buf, samples)
	hs.processes[key] = buf

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		hs.processes[key] = buf[:20]
		copy(buf, samples)
		hs.pruneExpiredSamples(key)
	}
}

func BenchmarkAddSampleWithinWindow(b *testing.B) {
	hs := NewHistoryStore()
	sample := ProcessSample{
		Timestamp:  time.Now(),
		PID:        42,
		CreateTime: 100,
		Name:       "test-proc",
	}
	hs.AddSample(sample)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		sample.Timestamp = time.Now()
		hs.AddSample(sample)
	}
}
