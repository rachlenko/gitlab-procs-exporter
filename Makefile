# Get the latest commit branch, hash, and date
TAG=$(shell git describe --tags --abbrev=0 --exact-match 2>/dev/null)
BRANCH=$(if $(TAG),$(TAG),$(shell git rev-parse --abbrev-ref HEAD 2>/dev/null))
HASH=$(shell git rev-parse --short=7 HEAD 2>/dev/null)
# Formatted by git itself rather than date(1): `date -r <epoch>` is BSD syntax,
# and on GNU coreutils -r means "this file's mtime", so the old pipeline failed
# with "No such file or directory" on every Linux build. It failed QUIETLY --
# TIMESTAMP came out empty and the stamped revision kept a dangling separator
# (v0.0.21-79148a7-), so released binaries reported a truncated version.
TIMESTAMP=$(shell TZ=UTC git log -1 --date=format-local:%Y%m%dT%H%M%S --format=%cd HEAD 2>/dev/null)
GIT_REV=$(shell printf "%s-%s-%s" "$(BRANCH)" "$(HASH)" "$(TIMESTAMP)")
REV=$(if $(filter --,$(GIT_REV)),latest,$(GIT_REV))

all: fmt lint test build

# Compile binary with revision ldflags. Also builds the jobreport-web single
# self-contained binary so `make build` yields both runtime artifacts.
build: build-jobreport-web
	go build -ldflags "-X main.revision=$(REV) -s -w" -o .bin/gitlab-procs-exporter

# Compile the jobreport CLI (one-shot top-N report / GitLab job-log parser).
build-jobreport:
	go build -ldflags "-s -w" -o .bin/jobreport ./cmd/jobreport

# Compile the jobreport-web server (embedded htmx UI; self-execs as jobreport).
build-jobreport-web:
	go build -ldflags "-s -w" -o .bin/jobreport-web ./cmd/jobreport-web

# Cross-compile a static jobreport for Linux runners (handy for CI images).
build-jobreport-linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w" -o .bin/jobreport-linux-amd64 ./cmd/jobreport

# Run tests with coverage and exclude mocks
test:
	go clean -testcache
	go test -race -coverprofile=coverage.out ./...
	@if [ -f coverage.out ]; then \
		grep -v "_mock.go" coverage.out | grep -v "mocks" > coverage_no_mocks.out 2>/dev/null || true; \
		go tool cover -func=coverage_no_mocks.out; \
		rm -f coverage.out coverage_no_mocks.out; \
	fi

# Run golangci-lint
lint:
	golangci-lint run --max-issues-per-linter=0 --max-same-issues=0

# Install the developer tools fmt depends on. They are not vendored and not
# needed to build or test, so this is a separate opt-in step.
tools:
	go install golang.org/x/tools/cmd/goimports@latest

# Format go files and group imports.
# The goimports check runs BEFORE gofmt on purpose: without it a machine missing
# goimports still gets gofmt applied and then fails, leaving the tree
# half-formatted and the error ("goimports: No such file or directory") saying
# nothing about how to fix it.
fmt:
	@command -v goimports >/dev/null 2>&1 || { \
	  echo "error: goimports not found; run 'make tools'"; \
	  echo "       (it installs to $$(go env GOPATH)/bin — make sure that is on PATH)"; \
	  exit 1; \
	}
	gofmt -s -w $$(find . -type f -name "*.go" -not -path "./vendor/*" -not -path "**/mocks/*")
	goimports -w $$(find . -type f -name "*.go" -not -path "./vendor/*" -not -path "**/mocks/*")

# Run tests with race conditions only
race:
	go test -race -timeout=60s ./...

version:
	@echo "branch: $(BRANCH), hash: $(HASH), timestamp: $(TIMESTAMP)"
	@echo "revision: $(REV)"

# Latest semver tag, used to auto-bump the patch version for `make release`.
LATEST_TAG=$(shell git describe --tags --abbrev=0 2>/dev/null)

# Tag and push the next version, then let CI (.github/workflows/release.yml)
# build the release artifacts. By default it bumps the patch of the latest tag;
# override with an explicit version, e.g. `make release VERSION=v1.2.0`.
# Guards: clean working tree, on `main`, and the tag must not already exist.
release: test
	@test -z "$$(git status --porcelain)" || { echo "error: working tree not clean; commit first"; exit 1; }
	@branch=$$(git rev-parse --abbrev-ref HEAD); \
	 [ "$$branch" = "main" ] || { echo "error: must be on main (currently on $$branch)"; exit 1; }
	@version="$(VERSION)"; \
	 if [ -z "$$version" ]; then \
	   latest="$(LATEST_TAG)"; \
	   if [ -z "$$latest" ]; then \
	     version="v0.0.1"; \
	   else \
	     v=$${latest#v}; \
	     major=$$(echo "$$v" | cut -d. -f1); \
	     minor=$$(echo "$$v" | cut -d. -f2); \
	     patch=$$(echo "$$v" | cut -d. -f3); \
	     version="v$$major.$$minor.$$((patch + 1))"; \
	   fi; \
	 fi; \
	 case "$$version" in \
	   v[0-9]*.[0-9]*.[0-9]*) ;; \
	   *) echo "error: VERSION '$$version' must look like vX.Y.Z"; exit 1;; \
	 esac; \
	 if git rev-parse "$$version" >/dev/null 2>&1; then \
	   echo "error: tag $$version already exists"; exit 1; \
	 fi; \
	 echo "==> releasing $$version (from $(LATEST_TAG))"; \
	 git push origin main; \
	 git tag -a "$$version" -m "Release $$version"; \
	 git push origin "$$version"; \
	 echo "==> pushed $$version; CI will build the release artifacts"

.PHONY: all build build-jobreport build-jobreport-web build-jobreport-linux test lint fmt tools race version release
