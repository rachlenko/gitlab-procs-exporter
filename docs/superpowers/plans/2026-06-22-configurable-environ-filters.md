# Configurable environ redaction filters — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let operators extend `gitlab_process_info` environ scrubbing with their own key-name substrings, supplied via a `--config` YAML file; without the flag, behaviour is unchanged.

**Architecture:** A new `exporter/config.go` loads+normalizes a YAML config (`redact_key_substrings`). `ProcessCollector` gains a variadic constructor param carrying those substrings; the environ scrub loop is extracted into a `scrubEnviron` method that redacts on built-in `IsSecretKey`, the configured substrings (`keyInExtra`), or `IsSecretValue`. `main` loads the config (fail-fast) and passes the substrings in.

**Tech Stack:** Go 1.24, `go.yaml.in/yaml/v2` (already in `go.sum` as indirect; promoted to a direct require — no network fetch), stdlib `os`/`strings`, `github.com/prometheus/client_golang`.

## Global Constraints

- Go version floor: `go 1.24.0`. The only dependency-graph change is promoting `go.yaml.in/yaml/v2` from indirect to direct (it is already in `go.sum`). No other new modules.
- One `*_test.go` file per implementation file.
- Temp test files via `t.TempDir()`.
- Config flag: `--config <path>`, optional. Omitted → built-in denylist only (current behaviour). Present but unreadable/malformed → error logged and process exits non-zero (fail-fast).
- YAML field name exactly: `redact_key_substrings` (a list of strings).
- Configured substrings are normalized: `strings.TrimSpace` then `strings.ToLower`, empties dropped. Matching is case-insensitive `strings.Contains`, augmenting the built-in denylist.
- `NewProcessCollector` takes the extra substrings as a **variadic** param so existing call sites (incl. `main_test.go` and `collector_test.go`) compile unchanged.
- `IsSecretKey` / `IsSecretValue` built-in logic is NOT modified.

---

### Task 1: YAML config loader

**Files:**
- Create: `exporter/config.go`
- Test: `exporter/config_test.go`
- Modify: `go.mod`, `go.sum` (promote `go.yaml.in/yaml/v2` to a direct require via `go mod tidy`)

**Interfaces:**
- Produces: `type Config struct { RedactKeySubstrings []string }`; `LoadConfig(path string) (*Config, error)`; `normalizeSubstrings(in []string) []string` (package-internal helper, reused by Task 2). Consumed by Task 2 (`normalizeSubstrings`) and Task 3 (`LoadConfig`).

- [ ] **Step 1: Write the failing tests**

Create `exporter/config_test.go`:

```go
package exporter

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "redact_key_substrings:\n  - Vault\n  - \"  Internal_Token  \"\n  - \"\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"vault", "internal_token"} // trimmed, lowercased, empty dropped
	if len(cfg.RedactKeySubstrings) != len(want) {
		t.Fatalf("got %v, want %v", cfg.RedactKeySubstrings, want)
	}
	for i := range want {
		if cfg.RedactKeySubstrings[i] != want[i] {
			t.Errorf("index %d: got %q, want %q", i, cfg.RedactKeySubstrings[i], want[i])
		}
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadConfigMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("redact_key_substrings: [unterminated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Error("expected error for malformed YAML")
	}
}

func TestLoadConfigEmptyField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(path, []byte("# no rules here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.RedactKeySubstrings) != 0 {
		t.Errorf("expected empty slice, got %v", cfg.RedactKeySubstrings)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./exporter/ -run TestLoadConfig -v`
Expected: FAIL — undefined: `LoadConfig`.

- [ ] **Step 3: Write minimal implementation**

Create `exporter/config.go`:

```go
package exporter

import (
	"fmt"
	"os"
	"strings"

	yaml "go.yaml.in/yaml/v2"
)

// Config is the on-disk YAML configuration for the exporter.
type Config struct {
	RedactKeySubstrings []string `yaml:"redact_key_substrings"`
}

// LoadConfig reads and parses the YAML config file at path. The returned
// Config's RedactKeySubstrings are normalized: trimmed, lowercased, and with
// empty entries removed.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	cfg.RedactKeySubstrings = normalizeSubstrings(cfg.RedactKeySubstrings)
	return &cfg, nil
}

// normalizeSubstrings trims and lowercases each entry, dropping empties.
func normalizeSubstrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
```

- [ ] **Step 4: Promote the yaml dependency and run tests**

Run: `go mod tidy && go test ./exporter/ -run TestLoadConfig -v`
Expected: `go mod tidy` moves `go.yaml.in/yaml/v2` out of the `// indirect` group in `go.mod` (no network fetch — it is already in `go.sum`); tests PASS.

Verify the require is now direct:
Run: `grep 'go.yaml.in/yaml/v2' go.mod`
Expected: a line WITHOUT the `// indirect` suffix.

- [ ] **Step 5: Commit**

```bash
git add exporter/config.go exporter/config_test.go go.mod go.sum
git commit -m "feat(exporter): add YAML config loader for environ redaction rules"
```

---

### Task 2: Collector accepts configured key substrings

**Files:**
- Modify: `exporter/collector.go` (struct ~12-22, `NewProcessCollector` ~24-61, `Collect` env loop ~88-101)
- Test: `exporter/collector_test.go`

**Interfaces:**
- Consumes: `normalizeSubstrings` (Task 1).
- Produces: `NewProcessCollector(store *HistoryStore, extraKeySubstrings ...string) *ProcessCollector`; unexported `(*ProcessCollector) keyInExtra(key string) bool`; unexported `(*ProcessCollector) scrubEnviron(environ map[string]string) string`. Consumed by Task 3 (variadic constructor).

- [ ] **Step 1: Write the failing tests**

Append to `exporter/collector_test.go`:

```go
func TestProcessCollectorKeyInExtra(t *testing.T) {
	store := NewHistoryStore()
	// Constructor must normalize: mixed case and surrounding spaces.
	pc := NewProcessCollector(store, "Vault", "  Internal_Token  ")
	if !pc.keyInExtra("VAULT_ADDR") {
		t.Error("expected VAULT_ADDR to match configured 'vault'")
	}
	if !pc.keyInExtra("MY_INTERNAL_TOKEN_X") {
		t.Error("expected MY_INTERNAL_TOKEN_X to match 'internal_token'")
	}
	if pc.keyInExtra("CI_JOB_NAME") {
		t.Error("did not expect CI_JOB_NAME to match")
	}
	if NewProcessCollector(store).keyInExtra("ANYTHING") {
		t.Error("no extras configured: nothing should match")
	}
}

func TestScrubEnvironConfiguredKey(t *testing.T) {
	pc := NewProcessCollector(NewHistoryStore(), "vault")
	out := pc.scrubEnviron(map[string]string{
		"VAULT_ADDR":  "https://vault.example:8200", // key not in built-in denylist; value not secret-shaped
		"CI_JOB_NAME": "build",
	})
	if !strings.Contains(out, "VAULT_ADDR=[REDACTED]") {
		t.Errorf("expected VAULT_ADDR redacted via configured substring, got %q", out)
	}
	if strings.Contains(out, "vault.example") {
		t.Errorf("configured-secret value leaked: %q", out)
	}
	if !strings.Contains(out, "CI_JOB_NAME=build") {
		t.Errorf("expected CI_JOB_NAME to pass through, got %q", out)
	}
}

func TestScrubEnvironBuiltinStillWorks(t *testing.T) {
	pc := NewProcessCollector(NewHistoryStore()) // no extras
	out := pc.scrubEnviron(map[string]string{"API_KEY": "abc", "USER": "gitlab"})
	if !strings.Contains(out, "API_KEY=[REDACTED]") {
		t.Errorf("expected built-in denylist to redact API_KEY, got %q", out)
	}
	if !strings.Contains(out, "USER=gitlab") {
		t.Errorf("expected USER to pass through, got %q", out)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./exporter/ -run 'TestProcessCollectorKeyInExtra|TestScrubEnviron' -v`
Expected: FAIL — `pc.keyInExtra` / `pc.scrubEnviron` undefined.

- [ ] **Step 3: Add the field, variadic constructor, and helpers**

In `exporter/collector.go`, add the field to the struct (after `infoDesc *prometheus.Desc`):

```go
	// extraKeySubstrings are operator-configured key-name substrings that
	// augment the built-in IsSecretKey denylist (normalized: lowercase, trimmed).
	extraKeySubstrings []string
```

Change the constructor signature line from
`func NewProcessCollector(store *HistoryStore) *ProcessCollector {`
to:

```go
func NewProcessCollector(store *HistoryStore, extraKeySubstrings ...string) *ProcessCollector {
```

and add the field to the returned struct literal (right after `store: store,`):

```go
		store:              store,
		extraKeySubstrings: normalizeSubstrings(extraKeySubstrings),
```

Add these two methods (e.g. just below `NewProcessCollector`, before `Describe`):

```go
// keyInExtra reports whether key matches any operator-configured substring.
func (pc *ProcessCollector) keyInExtra(key string) bool {
	k := strings.ToLower(key)
	for _, s := range pc.extraKeySubstrings {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// scrubEnviron renders the environ map as a comma-joined "k=v" string,
// redacting any pair whose key or value looks sensitive: the built-in
// denylist (IsSecretKey), operator-configured substrings (keyInExtra), or
// the value-shape heuristics (IsSecretValue).
func (pc *ProcessCollector) scrubEnviron(environ map[string]string) string {
	var envPairs []string
	for k, v := range environ {
		val := v
		if IsSecretKey(k) || pc.keyInExtra(k) || IsSecretValue(v) {
			val = "[REDACTED]"
		}
		envPairs = append(envPairs, fmt.Sprintf("%s=%s", k, val))
	}
	return strings.Join(envPairs, ", ")
}
```

- [ ] **Step 4: Use `scrubEnviron` in `Collect`**

In `Collect`, replace the inline scrub block (the `// Scrub environment variables...` comment, the `var envPairs []string` loop, and the `envStr := strings.Join(envPairs, ", ")` line) plus the `infoLabels` line with:

```go
		// Emit metadata info metric (environ scrubbed for secrets)
		infoLabels := []string{pidStr, p.Name, p.CmdLine, pc.scrubEnviron(p.Environ)}
		ch <- prometheus.MustNewConstMetric(pc.infoDesc, prometheus.GaugeValue, 1.0, infoLabels...)
```

(Delete the now-duplicated trailing `ch <- ...infoDesc...` line that previously followed `infoLabels`, so the metric is emitted exactly once.)

- [ ] **Step 5: Run the full exporter suite**

Run: `go test ./exporter/ -v`
Expected: PASS — the three new tests, plus the pre-existing `TestCollectorDescribeAndCollect` (still emits 6 metrics) and `TestIsSecretKey*`/`TestIsSecretValue` unaffected.

- [ ] **Step 6: Commit**

```bash
git add exporter/collector.go exporter/collector_test.go
git commit -m "feat(exporter): apply operator-configured key substrings in environ scrub"
```

---

### Task 3: Wire `--config` into main

**Files:**
- Modify: `main.go` (flag block ~31-48; collector construction ~97-104)

**Interfaces:**
- Consumes: `exporter.LoadConfig` (Task 1), `exporter.NewProcessCollector(store, extra...)` (Task 2).

- [ ] **Step 1: Add the `--config` flag**

In `main.go` `main()`, alongside the other `flag.*` declarations (before `flag.Parse()`), add:

```go
	configPath := flag.String("config", "",
		"Path to a YAML config file with extra environ redaction rules")
```

- [ ] **Step 2: Load the config (fail-fast) and pass substrings to the collector**

In `main.go`, locate the collector construction:

```go
	// Register Prometheus custom collector
	collector := exporter.NewProcessCollector(store)
	prometheus.MustRegister(collector)
```

Replace it with:

```go
	// Load optional config for extra environ redaction rules (fail-fast).
	var redactKeySubstrings []string
	if *configPath != "" {
		cfg, err := exporter.LoadConfig(*configPath)
		if err != nil {
			log.Fatalf("config: %v", err)
		}
		redactKeySubstrings = cfg.RedactKeySubstrings
	}

	// Register Prometheus custom collector
	collector := exporter.NewProcessCollector(store, redactKeySubstrings...)
	prometheus.MustRegister(collector)
```

- [ ] **Step 3: Build and run the full suite**

Run: `go build ./... && go test ./...`
Expected: build succeeds; all tests PASS (`main_test.go` is unaffected — it does not call `NewProcessCollector`).

- [ ] **Step 4: Smoke-test fail-fast and happy path manually**

Build once: `go build -o /tmp/gpe .`

Happy path (valid config, server starts then is killed by `timeout`):
```bash
printf 'redact_key_substrings:\n  - vault\n' > /tmp/gpe.yaml && \
  timeout 2 /tmp/gpe --config /tmp/gpe.yaml --port 0 ; echo "exit=$?"
```
Expected: no `config:` error in output; process runs until `timeout` kills it (`exit=124`).

Fail-fast (malformed config):
```bash
echo 'redact_key_substrings: [oops' > /tmp/gpe-bad.yaml && \
  /tmp/gpe --config /tmp/gpe-bad.yaml --port 0 ; echo "exit=$?"
```
Expected: prints `config: parse config ...` and `exit=1`.

Fail-fast (missing file):
```bash
/tmp/gpe --config /tmp/does-not-exist.yaml --port 0 ; echo "exit=$?"
```
Expected: prints `config: read config ...` and `exit=1`.

Clean up: `rm -f /tmp/gpe /tmp/gpe.yaml /tmp/gpe-bad.yaml`

- [ ] **Step 5: Commit**

```bash
git add main.go
git commit -m "feat: load --config YAML for extra environ redaction (fail-fast)"
```

---

### Task 4: Example config + README

**Files:**
- Create: `config.example.yaml`
- Modify: `README.md`

**Interfaces:** none (documentation only).

- [ ] **Step 1: Create the example config**

Create `config.example.yaml`:

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

- [ ] **Step 2: Document the flag and config in the README**

Append this section to `README.md`:

```markdown
## Configuration file

Pass `--config <path>` to supply a YAML file with extra environ redaction
rules. Without the flag, only the built-in secret denylist applies. If the
flag is given but the file is missing or malformed, the exporter logs the
error and exits (fail-fast).

```yaml
# config.example.yaml
redact_key_substrings:
  - vault
  - internal_token
```

`redact_key_substrings` is a list of case-insensitive substrings. Any process
environment variable whose **name** contains one of them is shown as
`NAME=[REDACTED]` in the `gitlab_process_info` metric, in addition to the
built-in denylist and the value-shape heuristics (token prefixes, JWTs, and
long high-entropy strings).

```bash
gitlab-procs-exporter --config /etc/gitlab-procs-exporter/config.yaml
```
```

- [ ] **Step 3: Commit**

```bash
git add config.example.yaml README.md
git commit -m "docs: document --config and redact_key_substrings"
```

---

## Self-Review notes

- **Spec coverage:** YAML loader + normalization + error paths (T1), variadic collector + `keyInExtra` + `scrubEnviron` wiring (T2), `--config` flag + fail-fast + pass-through (T3), example config + README (T4). `go.yaml.in/yaml/v2` promoted to direct in T1. All spec sections mapped.
- **Type consistency:** `LoadConfig(path) (*Config, error)` and `Config.RedactKeySubstrings []string` used identically in T1/T3. `normalizeSubstrings([]string) []string` defined in T1, reused in T2's constructor. `NewProcessCollector(store, extra...)` variadic in T2, called with `redactKeySubstrings...` in T3. `keyInExtra`/`scrubEnviron` defined and consumed within T2.
- **No placeholders:** every code step contains full code; every run step has an exact command and expected result. The `scrubEnviron` extraction (not in the original spec) is an explicit, justified refactor that keeps the end-to-end test dependency-free.
```
