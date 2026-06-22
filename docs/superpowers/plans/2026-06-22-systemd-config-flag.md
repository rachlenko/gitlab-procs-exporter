# `--config` support in the systemd installer — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When installing via `--deploy-as-systemd-service`, bake the existing `--config <path>` into the generated unit's `ExecStart` so the service loads the YAML redaction config.

**Architecture:** Thread one optional `ConfigPath` field through the existing `ServiceConfig → renderUnitFile → unitTemplate` path; a Go-template conditional appends `--config <path>` only when set. `main.go` passes the existing `--config` flag value. Empty path → unit is byte-for-byte unchanged.

**Tech Stack:** Go 1.24, `text/template`, stdlib only.

## Global Constraints

- Go 1.24; no new module dependencies.
- One `*_test.go` file per implementation file (`deploy/systemd.go` → `deploy/systemd_test.go`).
- Reuse the existing `--config` flag — do NOT add a new flag.
- Empty `ConfigPath` must render the unit byte-for-byte identical to today (no dangling `--config`).
- No install-time existence check of the config file (exporter fail-fasts at runtime).
- Follow TDD: failing test first, see it fail, implement, see it pass, commit.

---

### Task 1: Bake `--config` into the systemd unit's ExecStart

**Files:**
- Modify: `deploy/systemd.go` (`ServiceConfig` struct ~24-30; `unitTemplate` ExecStart line ~78; `renderUnitFile` data struct ~107-119)
- Modify: `main.go` (`*deploySystemd` `ServiceConfig` literal ~69-74)
- Test: `deploy/systemd_test.go`

**Interfaces:**
- Produces: `ServiceConfig.ConfigPath string` — when non-empty, `renderUnitFile` emits ` --config <path>` at the end of the `ExecStart` line.

- [ ] **Step 1: Write the failing tests**

Append to `deploy/systemd_test.go`:

```go
func TestRenderUnitFileWithConfigPath(t *testing.T) {
	c := ServiceConfig{
		ExecPath:    "/usr/bin/gitlab-procs-exporter",
		Port:        9100,
		Interval:    90 * time.Second,
		ServiceUser: "root",
		ConfigPath:  "/etc/gitlab-procs-exporter/config.yaml",
	}
	out, err := renderUnitFile(c)
	if err != nil {
		t.Fatalf("renderUnitFile: %v", err)
	}
	want := "ExecStart=/usr/bin/gitlab-procs-exporter --port=9100 --interval=1m30s --config /etc/gitlab-procs-exporter/config.yaml"
	if !strings.Contains(out, want) {
		t.Errorf("unit file missing %q\n---\n%s", want, out)
	}
}

func TestRenderUnitFileWithoutConfigPathOmitsFlag(t *testing.T) {
	c := ServiceConfig{
		ExecPath:    "/usr/bin/gitlab-procs-exporter",
		Port:        9100,
		Interval:    90 * time.Second,
		ServiceUser: "root",
	}
	out, err := renderUnitFile(c)
	if err != nil {
		t.Fatalf("renderUnitFile: %v", err)
	}
	if strings.Contains(out, "--config") {
		t.Errorf("unit file should not contain --config when ConfigPath is empty\n---\n%s", out)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./deploy/ -run 'TestRenderUnitFileWithConfigPath|TestRenderUnitFileWithoutConfigPathOmitsFlag' -v`
Expected: FAIL — `unknown field 'ConfigPath' in struct literal of type ServiceConfig`.

- [ ] **Step 3: Add the `ConfigPath` field**

In `deploy/systemd.go`, add to the `ServiceConfig` struct (after the `Interval` field):

```go
	ConfigPath  string        // --config passed to the exporter (omitted when empty)
```

- [ ] **Step 4: Extend the ExecStart template line**

In `deploy/systemd.go`, change the `ExecStart` line inside `unitTemplate` from:

```
ExecStart={{.ExecPath}} --port={{.Port}} --interval={{.Interval}}
```

to:

```
ExecStart={{.ExecPath}} --port={{.Port}} --interval={{.Interval}}{{if .ConfigPath}} --config {{.ConfigPath}}{{end}}
```

- [ ] **Step 5: Pass `ConfigPath` into the render data**

In `deploy/systemd.go` `renderUnitFile`, add the field to the anonymous data struct definition and its initializer. The struct becomes:

```go
	data := struct {
		Module      string
		ExecPath    string
		Port        int
		Interval    string
		ServiceUser string
		ConfigPath  string
	}{
		Module:      Module,
		ExecPath:    resolveExecPath(cfg.ExecPath),
		Port:        cfg.Port,
		Interval:    cfg.Interval.String(),
		ServiceUser: cfg.ServiceUser,
		ConfigPath:  cfg.ConfigPath,
	}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./deploy/ -run 'TestRenderUnitFile' -v`
Expected: PASS — both new tests AND the pre-existing `TestRenderUnitFile` (which has no `ConfigPath`, so its `ExecStart` is unchanged and still matches `--port=9100 --interval=1m30s`).

- [ ] **Step 7: Wire the existing `--config` flag into the installer**

In `main.go`, in the `*deploySystemd` branch, add `ConfigPath: *configPath,` to the `deploy.ServiceConfig{...}` literal so it reads:

```go
	if *deploySystemd {
		err := deploy.InstallService(os.Stdout, deploy.ServiceConfig{
			ServiceName: *serviceName,
			ServiceUser: *serviceUser,
			Port:        *port,
			Interval:    *scrapeInterval,
			ConfigPath:  *configPath,
		})
```

(`configPath` is the existing `--config` flag declared earlier in `main()`.)

- [ ] **Step 8: Build and run the full suite**

Run: `go build ./... && go test ./... && gofmt -l deploy/systemd.go deploy/systemd_test.go main.go && go vet ./...`
Expected: build succeeds; all tests PASS; `gofmt -l` prints nothing; `go vet` clean.

- [ ] **Step 9: Commit**

```bash
git add deploy/systemd.go deploy/systemd_test.go main.go
git commit -m "feat(deploy): bake --config into the systemd unit ExecStart"
```

---

### Task 2: Document the deploy-time `--config`

**Files:**
- Modify: `README.md`

**Interfaces:** none (documentation only).

- [ ] **Step 1: Add a note to the README**

In `README.md`, in the existing "Configuration file" section (added previously), append this note:

```markdown
When installing the systemd service, pass `--config` alongside
`--deploy-as-systemd-service` and the path is baked into the unit's
`ExecStart`:

```bash
sudo gitlab-procs-exporter --deploy-as-systemd-service \
  --config /etc/gitlab-procs-exporter/config.yaml
```
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: note --config is preserved into the systemd unit on deploy"
```

---

## Self-Review notes

- **Spec coverage:** `ConfigPath` field + conditional template + render data (T1 steps 3-6), `main.go` wiring (T1 step 7), two render tests incl. empty-path regression (T1 step 1), README note (T2). All spec sections mapped.
- **Type consistency:** `ServiceConfig.ConfigPath string` defined in T1 step 3, consumed in the template (step 4), render data (step 5), and `main.go` literal (step 7) — same name/type throughout. The empty-path test (step 1) guards the byte-for-byte-unchanged constraint.
- **No placeholders:** every code step contains the full edit; every run step has an exact command and expected result.
```
