# Get the latest commit branch, hash, and date
TAG=$(shell git describe --tags --abbrev=0 --exact-match 2>/dev/null)
BRANCH=$(if $(TAG),$(TAG),$(shell git rev-parse --abbrev-ref HEAD 2>/dev/null))
HASH=$(shell git rev-parse --short=7 HEAD 2>/dev/null)
TIMESTAMP=$(shell git log -1 --format=%ct HEAD 2>/dev/null | xargs -I{} date -u -r {} +%Y%m%dT%H%M%S)
GIT_REV=$(shell printf "%s-%s-%s" "$(BRANCH)" "$(HASH)" "$(TIMESTAMP)")
REV=$(if $(filter --,$(GIT_REV)),latest,$(GIT_REV))

all: fmt lint test build

# Compile binary with revision ldflags
build:
	go build -ldflags "-X main.revision=$(REV) -s -w" -o .bin/gitlab-procs-exporter

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

# Format go files and group imports
fmt:
	gofmt -s -w $$(find . -type f -name "*.go" -not -path "./vendor/*" -not -path "**/mocks/*")
	goimports -w $$(find . -type f -name "*.go" -not -path "./vendor/*" -not -path "**/mocks/*")

# Run tests with race conditions only
race:
	go test -race -timeout=60s ./...

version:
	@echo "branch: $(BRANCH), hash: $(HASH), timestamp: $(TIMESTAMP)"
	@echo "revision: $(REV)"

.PHONY: all build test lint fmt race version
