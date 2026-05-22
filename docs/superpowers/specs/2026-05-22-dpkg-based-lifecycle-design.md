# Design: dpkg-based lifecycle for gitlab-procs-exporter

Date: 2026-05-22
Status: Approved (pending spec review)

## Summary

The exporter is distributed as a Debian package (`.deb`) attached to each
GitHub release (already produced by goreleaser's `nfpms`, installing the
binary to `/usr/bin`). This change pivots the binary's self-management flags
away from the `go install` model and onto a `dpkg`/GitHub-releases model:

- `--check-dependencies` — drop all go-install checks; verify only that a
  downloader (`curl` or `wget`) is present.
- `--deploy-as-systemd-service` — drop the `go install` build step; point the
  unit's `ExecStart` at the running binary (`os.Executable()`); enable + start.
- `--update` — replace `go install @latest` with: query the GitHub Releases
  API for the latest tag, download the matching `.deb`, `dpkg -i` it, restart
  the service.
- `--uninstall` — remove the service only; do **not** delete the dpkg-owned
  binary (print a `dpkg -r` hint instead).

Non-goals: hosting an APT repository; changing the release pipeline; changing
the exporter's runtime behavior (metrics, dashboard, scraping).

## Context

- `v0.0.4` release assets confirmed to include
  `gitlab-procs-exporter_0.0.4_linux_amd64.deb` and `..._arm64.deb`.
- goreleaser `nfpms` installs the binary to `/usr/bin/gitlab-procs-exporter`
  with no maintainer scripts — so the deb installs only the binary, and
  `--deploy-as-systemd-service` remains responsible for the systemd unit.
- Current code lives in package `deploy`: `deps.go` (checks), `systemd.go`
  (install/update/uninstall/render). Tests: `deps_test.go`, `systemd_test.go`.

## Approach

Refactor in place within the `deploy` package and add one new file
`release.go` for the GitHub-releases client, deb download, and `dpkg` calls.
Reuse `renderUnitFile`, `runCmd`, `findGo`→(removed), and the Linux/root
guards. Keep pure logic in small testable functions.

## Detailed design

### 1. `--check-dependencies` (deps.go)

`CheckDependencies()` returns a single `CheckResult`:

- **downloader**: `StatusOK` if `exec.LookPath("curl")` or `exec.LookPath("wget")`
  succeeds; otherwise `StatusFail` with detail
  `"need curl or wget for --update; install with: apt-get install -y curl"`.

Remove: go-toolchain check + `parseGoVersion`/`meetsMinGo`, git check, CA-bundle
check, install-dir/PATH checks, network probes, and their helpers
(`goEnv`, `goBinDir`, `dirState`, `dirOnPath`, `caBundlePresent`, `httpProbe`,
`currentUser`, the min-Go constants). Keep `Status`, `CheckResult`,
`AllPassed`, `PrintResults`. `PrintResults` header changes from
"go install ..." to "Checking prerequisites for gitlab-procs-exporter".

Retained package constants (repo/asset identity, no longer go-install
specific): `Module = "github.com/rachlenko/gitlab-procs-exporter"` (used to
build the API URL via its `owner/repo` suffix) and a new
`binaryName = "gitlab-procs-exporter"` (asset-name prefix + default ExecPath).

### 2. `--deploy-as-systemd-service` (systemd.go)

`ServiceConfig` loses `Module`, `Version`, `InstallDir` and gains
`ExecPath string` (the binary path written to `ExecStart`).

`InstallService(w, cfg)`:
1. Guards: Linux only; root (`os.Geteuid()==0`); `systemctl` present.
2. Resolve `cfg.ExecPath` if empty: `os.Executable()` → `filepath.EvalSymlinks`;
   fall back to `/usr/bin/gitlab-procs-exporter`.
3. Validate `cfg.ServiceUser` exists when non-root (unchanged).
4. `writeUnit(w, cfg)` (rewritten template uses `cfg.ExecPath`).
5. `daemon-reload` → `enable --now <unit>` → status.

Removed: `os.MkdirAll(InstallDir)`, `goInstall`, `findGo`. The
`installedVersion` helper is renamed `binaryVersion(path)` and retained for
logging the binary's `--version` (used by `--update`).

`renderUnitFile` template: `ExecStart={{.ExecPath}} --port={{.Port}} --interval={{.Interval}}`.
Hardening directives unchanged. `binaryPath()` is replaced by `ExecPath`.

### 3. `--update` (release.go, new)

```
UpdateService(w, cfg) error
```
1. Guards: Linux; root; `dpkg` present; a downloader present.
2. `rel, err := latestRelease(httpGet)` — GET
   `https://api.github.com/repos/rachlenko/gitlab-procs-exporter/releases/latest`,
   decode `{ tag_name, assets:[{name, browser_download_url}] }`.
3. `asset, err := pickDebAsset(rel, runtime.GOARCH)` — choose the asset whose
   name has prefix `gitlab-procs-exporter_` and suffix `_linux_<arch>.deb`,
   where `<arch>` maps `amd64→amd64`, `arm64→arm64` (error on unsupported
   arch). Matching by prefix+suffix avoids depending on the version segment,
   which sidesteps the `v`-prefix mismatch between the tag (`v0.0.4`) and the
   asset's embedded version (`0.0.4`).
4. Download `asset.browser_download_url` to a temp file (`os.CreateTemp`,
   removed via defer) using `curl -fsSL -o` or `wget -qO`.
5. `dpkg -i <tmpfile>` (replaces `/usr/bin` binary in place).
6. `systemctl restart <unit>` so the running service swaps to the new binary;
   then status.
7. Log resolved tag and post-update version via a small `binaryVersion(path)`
   helper that runs `<path> --version` (returns "unknown" on error). This
   helper is retained from the current code for this purpose.

Pure, testable helpers: `pickDebAsset(rel, goarch)`, `debArch(goarch)`.
`latestRelease` takes an injectable HTTP getter (default `http.Get` with a
timeout) so it can be tested with a stub serving canned JSON.

`httpProbe`-style injection: a package var `httpGet func(string)(*http.Response,error)`
defaulting to a timed client; tests override it.

### 4. `--uninstall` (systemd.go)

`UninstallService(w, cfg)`:
1. Guards: Linux; root; `systemctl` present.
2. `disable --now <unit>` (best-effort).
3. Remove the unit file (`removeIfPresent`).
4. `daemon-reload` + `reset-failed`.
5. **Do not** remove the binary. Print:
   `"Binary is managed by dpkg; to remove the package run: dpkg -r gitlab-procs-exporter"`.

`removeIfPresent` is retained for the unit file.

### 5. main.go flag wiring

- Remove flags `--service-version`, `--install-dir`.
- Keep `--service-name`, `--service-user`, `--port`, `--interval`,
  `--check-dependencies`, `--deploy-as-systemd-service`, `--update`,
  `--uninstall`, `--version`.
- `ServiceConfig` construction drops `InstallDir`/`Version`; `--update` and
  `--deploy` both pass `ServiceName/ServiceUser/Port/Interval` (and `--deploy`
  may leave `ExecPath` empty to auto-resolve).

## Error handling

- All four operations return descriptive errors; `main` does `log.Fatalf`.
- Non-Linux and non-root produce clear refusals (existing pattern).
- `--update`: network/HTTP failures, missing matching asset, download failure,
  and non-zero `dpkg`/`systemctl` exits are wrapped with context.
- `--update` logs the resolved tag so a no-op (already latest) is visible.

## Testing

Unit tests (no root, no network, hermetic):
- `deps_test.go`: downloader check OK/FAIL via stubbed `LookPath` is awkward;
  instead assert `CheckDependencies()` returns exactly one result named
  "downloader" and `PrintResults`/`AllPassed` behavior. Keep `TestAllPassed`,
  `TestPrintResults`.
- `release_test.go`: `debArch` mapping (amd64/arm64/unsupported);
  `assetName`; `pickDebAsset` against sample JSON (match + no-match);
  `latestRelease` with a stubbed getter returning canned JSON.
- `systemd_test.go`: `renderUnitFile` includes `ExecStart=<ExecPath> ...`;
  `setDefaults`; non-Linux rejection for Install/Update/Uninstall;
  `removeIfPresent` idempotency.

## Documentation

Update README "Bootstrapping on a Host":
- Install: `curl -fsSLO <release deb>` then `sudo dpkg -i <file>` (note
  `dpkg -i` needs a local file, not a URL; `apt-get install -y ./<file>.deb`
  resolves deps if any).
- `--check-dependencies`: now just checks for curl/wget.
- `--deploy-as-systemd-service`: unchanged usage; ExecStart auto-points at the
  installed binary.
- `--update`: downloads the latest release `.deb` and `dpkg -i`s it, then
  restarts the service.
- `--uninstall`: removes the service; to remove the binary use `dpkg -r`.

## Rollout

Code change only; no new release required to land it, but the new `--update`
behavior is exercised against existing published `.deb` assets (v0.0.4+). A
follow-up tag (v0.0.5) would be the first build containing the dpkg-based
flags themselves.
