# Plan: Tiny Memory Footprint for GitLab Process History Exporter

## Overview

Dramatically reduce the memory consumption and heap allocation rate of `gitlab-procs-exporter` without degrading any monitoring functionality. The exporter must retain its complete Prometheus metrics coverage (`gitlab_process_*`), Kubernetes pod requests metrics, REST APIs (`/api/processes`, `/api/history`), and embedded SPA dashboard, while dropping heap allocations by >90% and stabilizing resident memory.

## Context

- `main.go` — Scraper loop periodically polls all PIDs via `shirou/gopsutil`. Currently, it re-reads `/proc/<pid>/environ`, `/proc/<pid>/cmdline`, and `/proc/<pid>/cgroup` on every scrape cycle, creating hundreds of thousands of heap string allocations and maps every minute.
- `exporter/history.go` — `HistoryStore` maintains a 10-minute sliding window of `ProcessSample` slices. `pruneExpiredSamples` and `pruneAll` allocate fresh slices on every single sample addition and every scrape cycle. Each sample carries a copy of the process's environment map.
- `exporter/collector.go` — `ProcessCollector.Collect()` runs on every Prometheus scrape. `shannonEntropy()` allocates a map on the heap for every token-shaped environment variable checked for secrets. `Collect()` re-sorts environ keys, re-scrubs secrets, and re-computes label hashes for every active process on every scrape.

## Hard Constraints (read before every iteration)

- Each task below is atomic. Execute tasks from top to bottom.
- Run the specified verification step after each task and verify tests are green before marking `- [ ]` as `- [x]`.
- All code must cross-compile cleanly on Linux for Windows: `GOOS=windows go build ./...` must succeed after every task touching Go files.
- Existing Linux and macOS functionality and tests must not regress: `go test ./...` and `golangci-lint run` must remain clean.
- Zero breaking changes to exported Prometheus metrics, labels, `/api/processes`, or `/api/history`.
- No new external third-party Go dependencies.
- Follow project conventions: one `*_test.go` per implementation file, gofmt clean, no Claude/Anthropic attribution in commit messages.
- Final gate: `make test`, `make lint`, and `make build-windows` all green.
- When every box is `[x]` and the final gate passes, output `ALL_TASKS_DONE`.

---

## Implementation Steps

### Task 1: Zero-allocation Shannon entropy calculation in secret detection

**Files:**
- Modify: `exporter/collector.go`
- Modify: `exporter/collector_test.go`

- [x] 1. In `exporter/collector.go`, refactor `shannonEntropy(v string) float64` to use a stack-allocated ASCII frequency buffer `[128]int` rather than `make(map[rune]float64)`. Since `isTokenCharset` restricts inputs to ASCII alphanumerics and `+`, `/`, `=`, `-`, `_`, non-ASCII runes are rejected beforehand; non-ASCII fallback or array indexing safely avoids any heap allocation.
- [x] 2. In `exporter/collector_test.go`, add unit tests comparing entropy values of various tokens to verify exact numerical equivalence with the previous implementation, and add `BenchmarkShannonEntropy` demonstrating zero heap allocations per operation (`0 B/op, 0 allocs/op`).
- [x] 3. Run verification: `go test -v ./exporter/ -run TestShannonEntropy` and `golangci-lint run`.

### Task 2: In-place zero-allocation pruning in HistoryStore

**Files:**
- Modify: `exporter/history.go`
- Modify: `exporter/history_test.go`

- [x] 1. In `exporter/history.go`, optimize `AddSample`: check if the oldest sample in `hs.processes[key]` is already within the 10-minute window (`!samples[0].Timestamp.Before(cutoff)`). If so, skip pruning immediately without scanning or allocating.
- [x] 2. In `exporter/history.go`, refactor `pruneExpiredSamples(key)` and `pruneAll()` to prune expired entries in-place: locate the first non-expired index `k`, copy remaining elements with `copy(samples, samples[k:])`, zero out the tail slice (`[len(samples)-k:]`) to enable GC of referenced pointers, and reslice `hs.processes[key] = samples[:len(samples)-k]`. If all expired (`k == len(samples)`), delete the key from `hs.processes`.
- [x] 3. In `exporter/history_test.go`, add test cases validating in-place pruning boundary conditions: all expired, none expired, partial expiration, zeroed tail verification, and ensuring no memory leaks occur across thousands of rapid sample appends.
- [x] 4. Run verification: `go test -v ./exporter/ -run TestPrune` and `golangci-lint run`.

### Task 3: Process metadata caching in background scraper

**Files:**
- Modify: `main.go`
- Modify: `main_test.go`

- [x] 1. In `main.go`, replace `procCache map[int32]*process.Process` with `procCache map[int32]*procEntry`, where `procEntry` stores `proc *process.Process`, `name string`, `cmdline string`, `environ map[string]string`, `podUID string`, and `createTime int64`.
- [x] 2. In `main.go`, implement `liveProcessEntry(procCache, pid, inCluster, now)`: if `entry` exists and `entry.proc.IsRunning()` is true, return the cached entry. If the process is new or has been replaced (PID reuse), read `Name()`, `CreateTime()`, `Cmdline()`, `Environ()`, and `ProcessPodUID()` once, construct `procEntry`, and cache it.
- [x] 3. In `main.go`'s `scrape()`, reuse the cached `name`, `cmdline`, `environ`, `podUID`, and `createTime` from `procEntry`. Only query the dynamic counters (`Percent`, `Times`, `MemoryInfo`, `IOCounters`, `SelfIO`) on each scrape. Evict exited PIDs from `procCache` at the end of the scrape.
- [x] 4. In `main_test.go`, add unit tests verifying that `procCache` preserves and reuses metadata across scrapes, correctly evicts dead processes on PID reuse, and handles error conditions gracefully.
- [x] 5. Run verification: `go test -v ./...`, `golangci-lint run`, and `GOOS=windows go build ./...`.

### Task 4: Precomputed Prometheus info label caching in ProcessCollector

**Files:**
- Modify: `exporter/collector.go`
- Modify: `exporter/collector_test.go`

- [x] 1. In `exporter/collector.go`, introduce a thread-safe or gather-scoped label metadata cache `cachedProcessLabels` in `ProcessCollector` (keyed by `fmt.Sprintf("%d-%d", p.PID, p.CreateTime)` or process key) holding precomputed `ciVals`, `name`, `cmdline`, `environ`, `environTruncated`, and Prometheus label slices (`labels` and `infoLabels`).
- [x] 2. In `Collect()`, check if the active process has cached label slices. If cached, directly emit the metrics without re-sorting environment variables, re-scrubbing secrets, or re-hashing label values. If not cached (new process), compute the labels, record truncations, and cache them. Evict processes from the label cache that are no longer in `GetActiveProcesses()`.
- [x] 3. In `exporter/collector_test.go`, add tests verifying that `Collect()` emits identical metrics and label values across multiple scrapes, correctly handles label truncation counting on first scrape, and properly evicts inactive process label caches.
- [x] 4. Run verification: `go test -v ./exporter/...` and `golangci-lint run`.

### Task 5: API history memory optimization and final quality verification

**Files:**
- Modify: `main.go`
- Modify: `main_test.go`

- [x] 1. In `main.go`, optimize `serveAPIHistory`: instead of redacting `samples[i].Environ` for each sample individually in a loop (`O(N)` map allocations), redact `samples[len(samples)-1].Environ` once per matched process timeline and assign the redacted map to each sample in the returned timeline.
- [x] 2. Update `main_test.go` to verify `/api/history` output remains identical and fully redacted while allocating significantly less memory.
- [x] 3. Run full verification gate: `make test`, `make lint`, and `make build-windows`.
- [x] 4. Output `ALL_TASKS_DONE`.
