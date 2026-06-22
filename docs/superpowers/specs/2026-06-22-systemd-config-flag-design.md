# Add `--config` support to the systemd installer — Design

Date: 2026-06-22
Status: Approved pending user review

## Goal

When the exporter is installed as a systemd service via
`--deploy-as-systemd-service`, allow the operator to bake a `--config <path>`
into the generated unit's `ExecStart`, so the running service loads the YAML
redaction config without a manual unit edit or drop-in.

## Decision

Reuse the existing `--config` flag (added for runtime config loading). The
operator runs:

```
sudo gitlab-procs-exporter --deploy-as-systemd-service --config /etc/gitlab-procs-exporter/config.yaml
```

and the installer writes an `ExecStart` that includes `--config <path>`. No new
flag is introduced (DRY).

## Non-goals (YAGNI)

- No separate deploy-only flag.
- No install-time existence/validation check of the config file (the exporter
  already fail-fasts at startup if the file is missing/malformed).
- No path absolutization (the operator supplies an absolute path, consistent
  with how `ExecPath` and other fields are treated).

## Architecture

The change threads one optional field through the existing
`ServiceConfig → renderUnitFile → unitTemplate` path. When the field is empty,
the rendered unit is byte-for-byte identical to today's output.

### Components

**1. `deploy/systemd.go`**
- Add field to `ServiceConfig`:
  ```go
  ConfigPath  string        // --config passed to the exporter (omitted when empty)
  ```
- Extend the `ExecStart` line in `unitTemplate` with a conditional tail:
  ```
  ExecStart={{.ExecPath}} --port={{.Port}} --interval={{.Interval}}{{if .ConfigPath}} --config {{.ConfigPath}}{{end}}
  ```
- Add `ConfigPath string` to the anonymous data struct in `renderUnitFile` and
  populate it from `cfg.ConfigPath`.
- `setDefaults` is unchanged: empty `ConfigPath` is a valid "no config" state,
  so it has no default.

**2. `main.go`**
- In the `*deploySystemd` branch, add `ConfigPath: *configPath` to the
  `deploy.ServiceConfig{...}` literal. (`*configPath` is the existing
  `--config` flag value.)

**3. `deploy/systemd_test.go`**
- Two focused cases on `renderUnitFile`:
  - `ConfigPath` set → the rendered unit's `ExecStart` contains
    `--config /etc/gitlab-procs-exporter/config.yaml`.
  - `ConfigPath` empty → the rendered unit's `ExecStart` does NOT contain
    `--config` (no regression / no dangling flag).

## Data flow

```
gitlab-procs-exporter --deploy-as-systemd-service --config <path>
   │  main.go: ServiceConfig{..., ConfigPath: *configPath}
   ▼
deploy.InstallService → renderUnitFile(cfg)
   │  data.ConfigPath = cfg.ConfigPath
   ▼
unitTemplate: ExecStart=... {{if .ConfigPath}} --config {{.ConfigPath}}{{end}}
   ▼
/etc/systemd/system/<name>.service  (ExecStart carries --config <path>)
```

## Error handling

- Empty `ConfigPath` → no `--config` in `ExecStart` (unchanged behaviour).
- A non-existent/malformed config path is NOT checked at install time; the
  exporter logs and exits at service startup (existing fail-fast), which surfaces
  via `systemctl status` / journal.

## Testing

- `deploy/systemd_test.go` (extend, keeping one `*_test.go` per impl file):
  - render with `ConfigPath` set → assert `--config <path>` present in `ExecStart`.
  - render with `ConfigPath` empty → assert `--config` absent.
- Existing systemd tests must still pass (default render unchanged when
  `ConfigPath` is empty).

## Files touched

- Modified: `deploy/systemd.go` (field + template + render data),
  `main.go` (pass `ConfigPath`), `deploy/systemd_test.go` (two cases).
- Optionally: a one-line README note that `--config` is preserved into the unit
  when deploying.

## Known limitations

- The operator must supply an absolute config path; a relative path would be
  resolved by systemd against the service working directory (`/`).
- Updating the config path after install still requires re-running the
  installer (or editing the unit), same as other `ServiceConfig` fields.
