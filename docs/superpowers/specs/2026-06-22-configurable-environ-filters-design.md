# Configurable environ redaction filters via YAML config — Design

Date: 2026-06-22
Status: Approved pending user review

## Goal

Let operators extend the `gitlab_process_info` environ scrubbing with their own
key-name substrings, supplied through a YAML configuration file selected with a
`--config` flag. Without the flag, behaviour is unchanged — only the built-in
denylist applies.

## Decisions (from brainstorming)

| Axis | Decision |
|------|----------|
| Delivery | YAML config file (not CLI list flags). |
| Rule type | Additional key-name **substrings** that augment `IsSecretKey` (same case-insensitive `strings.Contains` semantics). |
| Selection | `--config <path>` flag; optional. |
| Missing flag | No config loaded → built-in denylist only (current behaviour). |
| Broken/unreadable file when `--config` is given | Error + exit (fail-fast). |
| YAML library | `go.yaml.in/yaml/v2` (already in `go.sum` as indirect; promote to a direct require). |

## Non-goals (YAGNI)

- No regex rules, no allowlist, no value-based rules from config.
- No hot-reload.
- No default config-path auto-discovery (config is loaded only via explicit `--config`).
- `IsSecretKey` / `IsSecretValue` built-in logic is unchanged.

## Architecture

Config loading is isolated in a new `exporter/config.go`. The collector gains an
optional list of extra key-name substrings via a variadic constructor parameter,
so existing call sites compile unchanged. The built-in `IsSecretKey` and
`IsSecretValue` functions are untouched.

```
  --config path ──► exporter.LoadConfig(path) ──► *Config{RedactKeySubstrings}
                          │ (fail-fast on error in main)
                          ▼
   NewProcessCollector(store, cfg.RedactKeySubstrings...) ──► ProcessCollector
                          │ holds normalized extra substrings
                          ▼
   Collect: redact when IsSecretKey(k) || keyInExtra(k) || IsSecretValue(v)
```

### Components

**1. `exporter/config.go` + `exporter/config_test.go` (new)**

```go
// Config is the on-disk YAML configuration.
type Config struct {
    RedactKeySubstrings []string `yaml:"redact_key_substrings"`
}

// LoadConfig reads and parses the YAML config at path, normalizing the
// redaction substrings (trimmed, lowercased, empties dropped).
func LoadConfig(path string) (*Config, error)
```

- Reads the file with `os.ReadFile`; parse with `yaml.Unmarshal`.
- Normalization: for each entry in `RedactKeySubstrings`, `strings.TrimSpace`
  then `strings.ToLower`; drop empty results. (Lowercase matches how
  `IsSecretKey` lowercases the key before comparison, so the configured
  substrings compare consistently.)
- Returns an error on read failure or YAML parse failure. A file that parses but
  has no `redact_key_substrings` is valid and yields an empty slice.

**2. `exporter/collector.go` (modify)**

- Signature: `NewProcessCollector(store *HistoryStore, extraKeySubstrings ...string) *ProcessCollector`.
  Variadic, so `NewProcessCollector(store)` (incl. existing tests) is unaffected.
- Store the normalized substrings on the struct: `extraKeySubstrings []string`.
  The constructor normalizes again (trim+lower+drop-empty) so the collector is
  safe even when called directly in tests with raw strings.
- Add unexported helper `(pc *ProcessCollector) keyInExtra(key string) bool`:
  lowercases `key`, returns true if any configured substring is contained.
- In `Collect`, change the redaction condition to:
  `if IsSecretKey(k) || pc.keyInExtra(k) || IsSecretValue(v)`.
- `IsSecretKey`, `IsSecretValue`, and the helpers stay as-is.

**3. `main.go` (modify)**

- Add flag: `configPath := flag.String("config", "", "Path to a YAML config file with extra environ redaction rules")`.
- After `flag.Parse()` and the early-exit handlers, before constructing the
  collector: if `*configPath != ""`, call `exporter.LoadConfig(*configPath)`;
  on error `log.Fatalf("config: %v", err)` (fail-fast). Collect the resulting
  `RedactKeySubstrings` (empty when no flag).
- Construct as `exporter.NewProcessCollector(store, redactKeySubstrings...)`.

**4. `config.example.yaml` (new) + README section**

```yaml
# gitlab-procs-exporter configuration.
#
# Additional environ key-name substrings to redact in the gitlab_process_info
# metric. Case-insensitive, matched by substring ("contains"), and added on top
# of the built-in denylist. Values whose content looks like a secret are always
# redacted regardless of this list.
redact_key_substrings:
  - vault
  - internal_token
```

## Error handling

- `--config` omitted → no load, empty extra list, existing behaviour.
- `--config <path>` with unreadable file → `LoadConfig` returns the `os` error →
  `main` logs and exits non-zero.
- `--config <path>` with malformed YAML → `LoadConfig` returns the parse error →
  `main` logs and exits non-zero.
- Valid file, no `redact_key_substrings` key → empty slice, no error.
- Entries that are blank after trim are dropped (no error).

## Testing (one `*_test.go` per impl file; temp files via `t.TempDir`)

- `exporter/config_test.go`:
  - valid YAML with mixed-case + surrounding-space entries → parsed, normalized
    to trimmed lowercase, empties dropped.
  - missing file path → error.
  - malformed YAML → error.
  - file present but no `redact_key_substrings` → empty slice, no error.
- `exporter/collector_test.go` (extend):
  - `NewProcessCollector(store, "vault")` + a process with env `VAULT_ADDR=https://...`
    → the pair is `[REDACTED]` in `gitlab_process_info` (asserted via the
    rendered `environ` label or `prometheus/testutil`).
  - a non-matching key (e.g. `CI_JOB_NAME=build`) still passes through.
  - the built-in denylist (e.g. `API_KEY`) still redacts with no config.

## Files touched

- New: `exporter/config.go`, `exporter/config_test.go`, `config.example.yaml`.
- Modified: `exporter/collector.go` (variadic constructor + `keyInExtra` + Collect
  condition), `exporter/collector_test.go` (new cases), `main.go` (`--config`
  flag + fail-fast load + pass substrings), `go.mod`/`go.sum`
  (`go.yaml.in/yaml/v2` promoted to a direct require via `go mod tidy`),
  `README.md` (config section).

## Known limitations

- Substring matching can over-redact (e.g. a short configured substring matches
  unrelated keys) — same fail-safe trade-off as the built-in denylist; operators
  control their own list.
- Config is read once at startup; changing the file requires a restart.
