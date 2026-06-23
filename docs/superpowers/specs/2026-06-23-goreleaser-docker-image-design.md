# Build & publish a container image via GoReleaser — Design

Date: 2026-06-23
Status: Approved pending user review

## Goal

On every tagged release, GoReleaser builds a multi-arch (linux/amd64 +
linux/arm64) container image of the exporter and publishes it to GitHub
Container Registry (ghcr.io). Every PR validates the image build without
publishing.

## Decisions (from brainstorming)

| Axis | Decision |
|------|----------|
| Registry | `ghcr.io/rachlenko/gitlab-procs-exporter` (auth via the built-in `GITHUB_TOKEN`). |
| Architectures | linux/amd64 + linux/arm64, combined into one multi-arch manifest. |
| Base image | `alpine:3.20` (has `sh`/`wget`/CA certs; the binary is static, CGO disabled). |
| Image tags | `:{{ .Tag }}` (e.g. `v0.0.14`) and `:latest`. |
| Dockerfile | `COPY`-only (no `RUN`) so cross-arch images build without QEMU emulation. |
| Pre-merge validation | A CI dry-run job runs `goreleaser release --snapshot` (builds images, no publish). |
| Image contents | the `gitlab-procs-exporter` binary only (the daemon). |

## Non-goals (YAGNI)

- No `jobreport` binary in the image (the archives already ship it).
- No Docker Hub, no image signing (cosign), no SBOM.

## Architecture

GoReleaser already builds the static `gitlab-procs-exporter` binary for
linux/{amd64,arm64}. The new `dockers:` stanzas copy each prebuilt binary into
an alpine image (one per arch); `docker_manifests:` fuses the two into a single
multi-arch tag. The release workflow gains registry login + buildx; a CI job
exercises the same build path on PRs with `--snapshot` (no push).

```
goreleaser build (existing)  ──►  linux/amd64 binary ─┐
                              └─►  linux/arm64 binary ─┤
                                                       ▼
dockers:  ghcr.io/...:{{.Tag}}-amd64  ◄── Dockerfile.goreleaser (FROM alpine; COPY binary)
          ghcr.io/...:{{.Tag}}-arm64  ◄──┘
                                                       ▼
docker_manifests:  ghcr.io/...:{{.Tag}}  +  ghcr.io/...:latest   (multi-arch)
```

### Components

**1. `Dockerfile.goreleaser` (new)** — minimal, no `RUN`:

```dockerfile
FROM alpine:3.20
COPY gitlab-procs-exporter /usr/bin/gitlab-procs-exporter
EXPOSE 8000
ENTRYPOINT ["/usr/bin/gitlab-procs-exporter"]
```

GoReleaser copies the matching-arch prebuilt binary into the build context as
`gitlab-procs-exporter`; the `COPY` picks it up. The binary is fully static
(`CGO_ENABLED=0`), so it runs on alpine/musl.

**2. `.goreleaser.yml` — add `dockers:` and `docker_manifests:`**

```yaml
dockers:
  - id: amd64
    goos: linux
    goarch: amd64
    ids: [gitlab-procs-exporter]
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
    ids: [gitlab-procs-exporter]
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

(`{{ .Tag }}` → `v0.0.14`; `{{ .Version }}` → `0.0.14`, used only in labels.)

**3. `.github/workflows/release.yml` — registry login + buildx**

- Widen permissions: `contents: write` → `contents: write`, `packages: write`.
- Before the GoReleaser step, add:
  - `docker/setup-qemu-action@v3` (defensive; harmless for COPY-only),
  - `docker/setup-buildx-action@v3`,
  - `docker/login-action@v3` → `registry: ghcr.io`, `username: ${{ github.actor }}`,
    `password: ${{ secrets.GITHUB_TOKEN }}`.
- The existing GoReleaser step is unchanged (`release --clean`, `GITHUB_TOKEN`).

**4. `.github/workflows/ci.yml` — dry-run validation job**

Add a second job `release-dry-run` (parallel to `lint-and-test`) that runs on
push/PR:
- checkout (fetch-depth 0), set up Go 1.24,
- `docker/setup-buildx-action@v3` (GoReleaser's docker builds use `use: buildx`),
- `goreleaser/goreleaser-action@v7` with `args: release --snapshot --clean`.

`--snapshot` builds binaries, archives, nfpms, and the docker images locally on
the ubuntu runner (which has Docker) and does NOT push — so a broken Dockerfile
or docker config fails the PR instead of a release.

**5. `README.md` — point at the real image**

- In the DaemonSet manifest, replace the placeholder
  `your-registry/gitlab-procs-exporter:v0.0.13` with
  `ghcr.io/rachlenko/gitlab-procs-exporter:v0.0.14`.
- Remove the "No official container image is published …" comment; replace with
  a one-line note that the image is published to ghcr.io on each release.
- The existing "Docker Image" badge already points at the ghcr package and
  becomes valid after the first publish — no change.

## Error handling / rollout

- The image is published **only on tag push** (release.yml). The first image
  appears with the next release (`v0.0.14`).
- If the docker build is broken, the CI dry-run job fails the PR before merge;
  if something only fails at publish time, the release job fails and the tag can
  be re-cut.
- `packages: write` lets `GITHUB_TOKEN` push to the repo's ghcr namespace; no
  extra secrets are required.

## Testing / verification

- **Local:** `goreleaser check` validates `.goreleaser.yml` syntax (Docker is
  not available locally, so image builds cannot run here).
- **CI (PR):** the `release-dry-run` job builds the images via `--snapshot`
  (no push) — the real pre-merge gate.
- **Release:** tagging `v0.0.14` builds and pushes
  `ghcr.io/rachlenko/gitlab-procs-exporter:{v0.0.14,latest}` as a multi-arch
  manifest; verified with `docker manifest inspect` / pulling on both arches.

## Files touched

- New: `Dockerfile.goreleaser`.
- Modified: `.goreleaser.yml` (dockers + docker_manifests),
  `.github/workflows/release.yml` (permissions + buildx/login steps),
  `.github/workflows/ci.yml` (dry-run job), `README.md` (real image + note).

## Known limitations

- No QEMU-executed steps means the Dockerfile must stay `RUN`-free; adding a
  `RUN` later would require real emulation (QEMU is already wired in release.yml
  as a safeguard, but the CI dry-run would need it too).
- Image ships only the exporter; users wanting `jobreport` use the release
  archives.
