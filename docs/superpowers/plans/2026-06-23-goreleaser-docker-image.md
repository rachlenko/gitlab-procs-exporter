# GoReleaser container image build + publish — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** On each tagged release, GoReleaser builds and pushes a multi-arch (linux/amd64+arm64) alpine-based image to `ghcr.io/rachlenko/gitlab-procs-exporter`; every PR validates the image build without publishing.

**Architecture:** A `COPY`-only `Dockerfile.goreleaser` wraps the already-built static binary; `.goreleaser.yml` gains per-arch `dockers:` plus a `docker_manifests:` that fuses them into `:{{ .Tag }}` and `:latest`. `release.yml` adds ghcr login + buildx; `ci.yml` gains a `goreleaser release --snapshot` dry-run that builds the images without pushing.

**Tech Stack:** GoReleaser v2, Docker buildx, GitHub Actions, alpine base image.

## Global Constraints

- Registry: `ghcr.io/rachlenko/gitlab-procs-exporter`; auth via the built-in `GITHUB_TOKEN`.
- Architectures: linux/amd64 + linux/arm64, fused into one multi-arch manifest.
- Base image: `alpine:3.20`. Image ships only the `gitlab-procs-exporter` binary.
- Image tags: `:{{ .Tag }}` (e.g. `v0.0.14`) and `:latest`. `{{ .Version }}` (e.g. `0.0.14`) only in OCI labels.
- `Dockerfile.goreleaser` MUST stay `RUN`-free (no emulation needed for cross-arch).
- Docker is NOT available locally → local validation is `goreleaser check` + YAML parse only; image builds are exercised by the CI dry-run and the release.
- No `jobreport` in the image, no Docker Hub, no signing, no SBOM (YAGNI).

---

### Task 1: Dockerfile + GoReleaser docker config

**Files:**
- Create: `Dockerfile.goreleaser`
- Modify: `.goreleaser.yml` (append `dockers:` and `docker_manifests:` after the existing `nfpms:` block)

**Interfaces:**
- Produces: image refs `ghcr.io/rachlenko/gitlab-procs-exporter:{{ .Tag }}-amd64`, `…-arm64`, fused into `…:{{ .Tag }}` and `…:latest`. Consumed by Task 2 (release push) and Task 3 (snapshot build).

- [ ] **Step 1: Create the Dockerfile**

Create `Dockerfile.goreleaser`:

```dockerfile
# Built by GoReleaser: the prebuilt static binary is copied into the build
# context, so this stays RUN-free (cross-arch images need no emulation).
FROM alpine:3.20
COPY gitlab-procs-exporter /usr/bin/gitlab-procs-exporter
EXPOSE 8000
ENTRYPOINT ["/usr/bin/gitlab-procs-exporter"]
```

- [ ] **Step 2: Append the docker config to `.goreleaser.yml`**

Append to the end of `.goreleaser.yml` (after the `nfpms:` block):

```yaml
dockers:
  - id: amd64
    goos: linux
    goarch: amd64
    ids:
      - gitlab-procs-exporter
    dockerfile: Dockerfile.goreleaser
    use: buildx
    image_templates:
      - "ghcr.io/rachlenko/gitlab-procs-exporter:{{ .Tag }}-amd64"
    build_flag_templates:
      - "--platform=linux/amd64"
      - "--label=org.opencontainers.image.source=https://github.com/rachlenko/gitlab-procs-exporter"
      - "--label=org.opencontainers.image.version={{ .Version }}"
      - "--label=org.opencontainers.image.revision={{ .FullCommit }}"
      - "--label=org.opencontainers.image.licenses=MIT"
  - id: arm64
    goos: linux
    goarch: arm64
    ids:
      - gitlab-procs-exporter
    dockerfile: Dockerfile.goreleaser
    use: buildx
    image_templates:
      - "ghcr.io/rachlenko/gitlab-procs-exporter:{{ .Tag }}-arm64"
    build_flag_templates:
      - "--platform=linux/arm64"
      - "--label=org.opencontainers.image.source=https://github.com/rachlenko/gitlab-procs-exporter"
      - "--label=org.opencontainers.image.version={{ .Version }}"
      - "--label=org.opencontainers.image.revision={{ .FullCommit }}"
      - "--label=org.opencontainers.image.licenses=MIT"

docker_manifests:
  - name_template: "ghcr.io/rachlenko/gitlab-procs-exporter:{{ .Tag }}"
    image_templates:
      - "ghcr.io/rachlenko/gitlab-procs-exporter:{{ .Tag }}-amd64"
      - "ghcr.io/rachlenko/gitlab-procs-exporter:{{ .Tag }}-arm64"
  - name_template: "ghcr.io/rachlenko/gitlab-procs-exporter:latest"
    image_templates:
      - "ghcr.io/rachlenko/gitlab-procs-exporter:{{ .Tag }}-amd64"
      - "ghcr.io/rachlenko/gitlab-procs-exporter:{{ .Tag }}-arm64"
```

- [ ] **Step 3: Validate the GoReleaser config**

Run: `goreleaser check`
Expected: `1 configuration file(s) validated` with no errors (it parses the new `dockers`/`docker_manifests` and resolves the `ids` references).

- [ ] **Step 4: Sanity-check the Dockerfile references the right binary**

Run: `grep -c 'COPY gitlab-procs-exporter /usr/bin/gitlab-procs-exporter' Dockerfile.goreleaser`
Expected: `1` (the binary name matches the `builds:` `binary:` and the docker `ids:`).

- [ ] **Step 5: Commit**

```bash
git add Dockerfile.goreleaser .goreleaser.yml
git commit -m "build: add GoReleaser multi-arch docker image + manifest config"
```

---

### Task 2: Release workflow — ghcr login + buildx

**Files:**
- Modify: `.github/workflows/release.yml`

**Interfaces:**
- Consumes: the docker config from Task 1 (GoReleaser pushes the images during `release --clean`).

- [ ] **Step 1: Replace `.github/workflows/release.yml` with the image-enabled version**

Overwrite `.github/workflows/release.yml` with:

```yaml
name: Release

on:
  push:
    tags:
      - 'v*'

permissions:
  contents: write
  packages: write

jobs:
  goreleaser:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v6
        with:
          fetch-depth: 0

      - name: Set up Go
        uses: actions/setup-go@v6
        with:
          go-version: '1.24'

      - name: Set up QEMU
        uses: docker/setup-qemu-action@v3

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Log in to GitHub Container Registry
        uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Run GoReleaser
        uses: goreleaser/goreleaser-action@v7
        with:
          distribution: goreleaser
          version: latest
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

- [ ] **Step 2: Validate the YAML parses and has the required keys**

Run:
```bash
python3 -c "import yaml,sys; d=yaml.safe_load(open('.github/workflows/release.yml')); \
assert d['permissions']['packages']=='write', 'missing packages: write'; \
steps=[s.get('uses','') for s in d['jobs']['goreleaser']['steps']]; \
assert any('docker/login-action' in s for s in steps), 'missing ghcr login'; \
assert any('docker/setup-buildx-action' in s for s in steps), 'missing buildx'; \
print('release.yml OK:', steps)"
```
Expected: prints `release.yml OK: [...]` listing the actions (checkout, setup-go, qemu, buildx, login, goreleaser) — no assertion error.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/release.yml
git commit -m "ci(release): add ghcr login + buildx so GoReleaser publishes the image"
```

---

### Task 3: CI dry-run job — build images without publishing

**Files:**
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: the docker config from Task 1 (`goreleaser release --snapshot` builds the images locally, no push).

- [ ] **Step 1: Add a `release-dry-run` job to `.github/workflows/ci.yml`**

Append this job under `jobs:` in `.github/workflows/ci.yml` (after the existing `lint-and-test` job, at the same indentation level as `lint-and-test`):

```yaml
  release-dry-run:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout code
        uses: actions/checkout@v6
        with:
          fetch-depth: 0

      - name: Set up Go
        uses: actions/setup-go@v6
        with:
          go-version: '1.24'
          cache: true

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: GoReleaser snapshot (build images, no publish)
        uses: goreleaser/goreleaser-action@v7
        with:
          distribution: goreleaser
          version: latest
          args: release --snapshot --clean
```

- [ ] **Step 2: Validate the YAML parses and the job is wired correctly**

Run:
```bash
python3 -c "import yaml; d=yaml.safe_load(open('.github/workflows/ci.yml')); \
j=d['jobs']['release-dry-run']; \
steps=[s.get('uses','')+s.get('name','') for s in j['steps']]; \
assert any('docker/setup-buildx-action' in s for s in steps), 'missing buildx'; \
assert any('goreleaser-action' in s for s in steps), 'missing goreleaser'; \
args=[s.get('with',{}).get('args','') for s in j['steps'] if 'goreleaser-action' in s.get('uses','')][0]; \
assert '--snapshot' in args, 'snapshot must not publish'; \
print('ci.yml release-dry-run OK; args =', args)"
```
Expected: prints `ci.yml release-dry-run OK; args = release --snapshot --clean` — no assertion error. (`--snapshot` guarantees no push.)

- [ ] **Step 3: Confirm the existing job is untouched**

Run: `python3 -c "import yaml; d=yaml.safe_load(open('.github/workflows/ci.yml')); print(sorted(d['jobs'].keys()))"`
Expected: `['lint-and-test', 'release-dry-run']` (both jobs present).

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: add GoReleaser snapshot dry-run to validate the image build on PRs"
```

---

### Task 4: README — point at the published image

**Files:**
- Modify: `README.md`

**Interfaces:** none (documentation only).

- [ ] **Step 1: Replace the placeholder image and the "no image" note**

In `README.md`, inside the DaemonSet manifest, replace this block:

```yaml
        - name: exporter
          # No official container image is published — the releases ship .deb /
          # .rpm / tarballs. Build your own, e.g. a Debian base + the release
          # .deb, or COPY the linux binary from the release tarball.
          image: your-registry/gitlab-procs-exporter:v0.0.13
          args: ["--port=8000", "--interval=10s"]
```

with:

```yaml
        - name: exporter
          # Multi-arch image published to ghcr.io on each release (amd64 + arm64).
          image: ghcr.io/rachlenko/gitlab-procs-exporter:v0.0.14
          args: ["--port=8000", "--interval=10s"]
```

- [ ] **Step 2: Verify the README no longer claims no image exists**

Run:
```bash
grep -n 'No official container image is published' README.md && echo "STILL PRESENT (fix)" || echo "removed OK"
grep -n 'ghcr.io/rachlenko/gitlab-procs-exporter:v0.0.14' README.md
```
Expected: first line prints `removed OK`; second line shows the new image reference present.

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: reference the published ghcr.io image in the DaemonSet manifest"
```

---

## Self-Review notes

- **Spec coverage:** Dockerfile + dockers/docker_manifests (T1), release.yml ghcr login/buildx/permissions (T2), ci.yml snapshot dry-run (T3), README real image (T4). All spec components mapped.
- **Type/value consistency:** image base name `ghcr.io/rachlenko/gitlab-procs-exporter` and tag templates `{{ .Tag }}`/`{{ .Tag }}-amd64`/`-arm64`/`latest` are identical across T1's `dockers`/`docker_manifests` and the T4 README (`v0.0.14`). The Dockerfile `COPY` binary name matches the `builds:` `binary:` and the docker `ids:` (`gitlab-procs-exporter`). `--snapshot` in T3 guarantees no publish; `packages: write` in T2 enables publish.
- **No placeholders:** every step has full file content or an exact validation command with expected output. Verification is config-check + YAML-parse locally (Docker unavailable); real image builds run in the CI dry-run (T3) and at release.
```
