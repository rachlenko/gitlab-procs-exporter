# Plan: Windows Operating System Support for GitLab Process History Exporter

## Overview

Extend `gitlab-procs-exporter` to fully support Microsoft Windows as a first-class operating system. The exporter must run either interactively from the command line (PowerShell/Command Prompt) or as a managed background Windows Service under the Windows Service Control Manager (SCM). It must collect all existing process metrics (`gitlab_process_*`), extract process environment variables and GitLab CI job metadata (`CI_JOB_ID`, `CI_PROJECT_PATH`, etc.) via Windows PEB inspection, and serve the Prometheus `/metrics` endpoint, REST APIs, and embedded SPA dashboard.

## Context

- `main.go` — Entrypoint, CLI flag parsing, scraper loop, and HTTP server.
- `exporter/procio.go` — Currently reads Linux `/proc/<pid>/task/<tid>/io` directly for per-process self-IO attribution.
- `deploy/systemd.go` — Currently provides Linux systemd service installation, uninstallation, and update.
- `.goreleaser.yml` — Currently builds binaries for `linux` and `darwin` only.
- `Makefile` — Currently compiles Linux/Darwin binaries with `build` and `build-jobreport-linux`.

## Hard Constraints (read before every iteration)

- Each task below is atomic. Execute tasks from top to bottom.
- Run the specified verification step after each task and verify tests are green before marking `- [ ]` as `- [x]`.
- All code must cross-compile cleanly on Linux for Windows: `GOOS=windows go build ./...` must succeed after every task touching Go files.
- Existing Linux and macOS functionality and tests must not regress: `go test ./...` and `golangci-lint run` must remain clean.
- No new external third-party Go dependencies — use `golang.org/x/sys/windows/svc` and `golang.org/x/sys/windows/svc/mgr` (already available via `golang.org/x/sys`).
- Follow project conventions: one `*_test.go` per implementation file, gofmt clean, no Claude/Anthropic attribution in commit messages.
- Final gate: `make test`, `make lint`, and `make build-windows` all green.
- When every box is `[x]` and the final gate passes, output `ALL_TASKS_DONE`.

---

## Implementation Steps

### Task 1: Refactor process I/O accounting with platform-specific implementations

**Files:**
- Create: `exporter/procio_linux.go`
- Create: `exporter/procio_windows.go`
- Create: `exporter/procio_other.go`
- Create: `exporter/procio_windows_test.go`
- Modify: `exporter/procio.go`

- [x] 1. Move Linux-specific `/proc` walking and parsing logic from `exporter/procio.go` into `exporter/procio_linux.go` with `//go:build linux` directive. Keep `IOStats` struct and shared constants/comments in `exporter/procio.go`.
- [x] 2. Implement `exporter/procio_windows.go` with `//go:build windows`: implement `SelfIO(procRoot string, pid int32) (IOStats, error)`. On Windows, process-level I/O counters (`GetProcessIoCounters`) measure the target process itself rather than reaped child processes, so `SelfIO` safely returns `IOStats{}, nil`.
- [x] 3. Implement `exporter/procio_other.go` with `//go:build !linux && !windows` returning `IOStats{}, nil`.
- [x] 4. Create `exporter/procio_windows_test.go` (and ensure `exporter/procio_test.go` has `//go:build linux` or tests shared logic) verifying `SelfIO` behavior on Windows.
- [x] 5. Run tests: verify `go test ./exporter/...` passes on Linux and `GOOS=windows go test -c ./exporter/...` compiles cleanly.

### Task 2: Platform abstraction for cgroup and container probing

**Files:**
- Create: `exporter/cgroup_linux.go`
- Create: `exporter/cgroup_windows.go`
- Create: `exporter/cgroup_other.go`
- Create: `exporter/cgroup_probe_test.go`
- Modify: `main.go`

- [ ] 1. Create `exporter/cgroup_linux.go` (`//go:build linux`) implementing `ProcessPodUID(pid int32) string` by reading `/proc/%d/cgroup` and calling `PodUIDFromCgroup`.
- [ ] 2. Create `exporter/cgroup_windows.go` (`//go:build windows`) and `exporter/cgroup_other.go` (`//go:build !linux && !windows`) implementing `ProcessPodUID(pid int32) string` returning `""`.
- [ ] 3. In `main.go`, replace the direct `os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))` with `exporter.ProcessPodUID(pid)`.
- [ ] 4. Add `exporter/cgroup_probe_test.go` asserting `ProcessPodUID` returns empty string safely on nonexistent processes or non-Linux platforms.
- [ ] 5. Run tests: verify `go test ./...` passes on Linux and `GOOS=windows go build ./...` compiles cleanly.

### Task 3: Windows Service Control Manager (SCM) support in deploy package

**Files:**
- Create: `deploy/service_windows.go`
- Create: `deploy/service_windows_test.go`
- Create: `deploy/service_stub.go`

- [ ] 1. Create `deploy/service_windows.go` (`//go:build windows`) using `golang.org/x/sys/windows/svc` and `golang.org/x/sys/windows/svc/mgr`:
  - `IsWindowsService() bool`: calls `svc.IsWindowsService()`.
  - `RunWindowsService(serviceName string, runFn func(ctx context.Context) error) error`: implements `svc.Handler`, handles `svc.Stop` and `svc.Shutdown`, signals context cancellation to `runFn`, and manages status transitions (`StartPending`, `Running`, `StopPending`, `Stopped`).
  - `InstallWindowsService(w io.Writer, cfg ServiceConfig) error`: connects to Windows SCM via `mgr.Connect()`, creates the service with auto-start (`mgr.StartAutomatic`), display name "GitLab Process History Exporter", description, binary path, and arguments (`--port`, `--interval`, `--config`), and configures service restart on failure.
  - `UninstallWindowsService(w io.Writer, cfg ServiceConfig) error`: opens service in SCM, issues stop request if running, and deletes the service.
- [ ] 2. Create `deploy/service_stub.go` (`//go:build !windows`): stubs for `IsWindowsService` (returns `false`), `RunWindowsService`, `InstallWindowsService`, and `UninstallWindowsService` (returning descriptive errors that Windows is required).
- [ ] 3. Create `deploy/service_windows_test.go` verifying ServiceConfig default resolution and command-line argument formatting for Windows services.
- [ ] 4. Run tests: verify `go test ./deploy/...` passes on Linux and `GOOS=windows go test -c ./deploy/...` compiles cleanly.

### Task 4: Integrate Windows service execution and CLI flags in main.go

**Files:**
- Modify: `main.go`
- Modify: `main_test.go`

- [ ] 1. In `main.go`, add flags `--deploy-as-windows-service` (or support `--deploy-as-service` on both OSes) and adjust `--service-user` default on Windows to `LocalSystem` (or `NT AUTHORITY\SYSTEM`).
- [ ] 2. Update service deployment and uninstall dispatch: on Windows, `--deploy-as-windows-service` or `--deploy-as-systemd-service` calls `deploy.InstallWindowsService`, and `--uninstall` calls `deploy.UninstallWindowsService`.
- [ ] 3. In `main.go`, check `deploy.IsWindowsService()` before starting HTTP server:
  - If running as a Windows service: wrap HTTP server and scraper startup inside `deploy.RunWindowsService(*serviceName, ...)`, shutting down the HTTP server cleanly when SCM signals stop.
  - If running in console mode (interactive): continue running standard HTTP server with logger output.
- [ ] 4. Update tests in `main_test.go` to test flag parsing and behavior for Windows service flags.
- [ ] 5. Run tests: verify `go test ./...` passes on Linux and `GOOS=windows go build -o .bin/gitlab-procs-exporter.exe .` builds.

### Task 5: Windows cross-compilation and packaging in .goreleaser.yml and Makefile

**Files:**
- Modify: `.goreleaser.yml`
- Modify: `Makefile`

- [ ] 1. In `.goreleaser.yml`, add `windows` to `goos` under `builds` for `gitlab-procs-exporter`, `jobreport`, and `jobreport-web`.
- [ ] 2. In `.goreleaser.yml`, add a format override for `goos: windows` producing `.zip` archives.
- [ ] 3. In `Makefile`, add `build-windows` target:
  ```makefile
  build-windows:
  	GOOS=windows GOARCH=amd64 go build -ldflags "-X main.revision=$(REV) -s -w" -o .bin/gitlab-procs-exporter.exe
  	GOOS=windows GOARCH=amd64 go build -ldflags "-s -w" -o .bin/jobreport.exe ./cmd/jobreport
  	GOOS=windows GOARCH=amd64 go build -ldflags "-s -w" -o .bin/jobreport-web.exe ./cmd/jobreport-web
  ```
- [ ] 4. Run `make build-windows` and verify `.bin/gitlab-procs-exporter.exe`, `.bin/jobreport.exe`, and `.bin/jobreport-web.exe` are built.

### Task 6: Documentation and acceptance verification

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`

- [ ] 1. In `README.md`, add a dedicated "Windows Support" section detailing:
  - Interactive execution via Command Prompt / PowerShell.
  - Installing and managing the background Windows Service (`--deploy-as-windows-service`, `--uninstall`).
  - Required privileges (Administrator / `LocalSystem` with `SeDebugPrivilege` to inspect all process environments).
  - Example Prometheus scrape config for Windows runner hosts.
- [ ] 2. In `CLAUDE.md`, document the Windows service architecture and platform build tags (`procio_windows.go`, `service_windows.go`).
- [ ] 3. Run full verification: `make fmt && make lint && make test && make build && make build-windows`.
