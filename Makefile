BINARY  := tfdry
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags="-s -w -X github.com/mchv/tfdry/output.Version=$(VERSION)"

# Tool versions are pinned in the install targets so contributors can
# bootstrap a clean environment with `make tools` before running
# `make verify`. CI installs the same versions in its runner.
# Bumps are a manual edit here — Dependabot's `gomod` ecosystem only
# tracks `go.mod` / `go.sum`, not Makefile variables, so it can't open
# PRs against these pins. Re-pin to the latest stable versions during
# release-prep (or whenever a relevant upstream fix lands).
GOFUMPT_VERSION       := v0.10.0
GOLANGCI_LINT_VERSION := v2.12.2
GOVULNCHECK_VERSION   := v1.4.0

.PHONY: help build test verify tools fmt fmt-check lint vet vuln check-no-markers cross-build bench bench-save bench-compare bench-pivot bench-e2e bench-baseline bench-jsonv2 clean

help: ## Show this help (list of available targets).
	@awk 'BEGIN {FS = ":.*## "; printf "Usage: make <target>\n\nTargets:\n"} \
	/^[a-zA-Z0-9_-]+:.*## / {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}' \
	$(MAKEFILE_LIST)

build: ## Build the tfdry binary into ./tfdry.
	CGO_ENABLED=0 go build -trimpath $(LDFLAGS) -o $(BINARY) .

test: ## Run unit tests across all packages.
	go test ./...

# ----- Verification pipeline ------------------------------------------------
# `make verify` is the canonical pre-PR check. Mirrors what CI will run
# in PR B1's pipeline. Composed of fine-grained sub-targets so contributors
# can run pieces in isolation when debugging a specific finding.

verify: fmt-check vet lint check-no-markers test-race vuln cross-build ## Run the full pre-PR verification pipeline.

tools: ## Install the dev tools used by `make verify` (gofumpt, golangci-lint, govulncheck) into GOPATH/bin.
	go install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

fmt: ## Apply gofumpt formatting in place. Use this to fix `make fmt-check` failures.
	@command -v gofumpt >/dev/null 2>&1 || { \
		echo "gofumpt not found in PATH. Run 'make tools' first."; \
		exit 1; \
	}
	gofumpt -w .

fmt-check: ## Verify gofumpt formatting is clean. Fails with a diff if not.
	@command -v gofumpt >/dev/null 2>&1 || { \
		echo "gofumpt not found in PATH. Run 'make tools' first."; \
		exit 1; \
	}
	@out=$$(gofumpt -l . 2>&1); \
	if [ -n "$$out" ]; then \
		echo "Files need gofumpt formatting:"; \
		echo "$$out"; \
		echo ""; \
		echo "Run 'make fmt' to fix."; \
		exit 1; \
	fi

vet: ## Run go vet.
	go vet ./...

lint: ## Run golangci-lint with the project's config.
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not found in PATH. Run 'make tools' first."; \
		exit 1; \
	}
	golangci-lint run ./...

test-race: ## Run tests with the race detector + a fresh build (no cache).
	go test ./... -race -count=1

vuln: ## Run govulncheck against the project's call graph.
	@command -v govulncheck >/dev/null 2>&1 || { \
		echo "govulncheck not found in PATH. Run 'make tools' first."; \
		exit 1; \
	}
	govulncheck ./...

check-no-markers: ## Refuse C##/G## review-finding markers in .go source.
	@# PR A4 scrubbed every C##/G## review marker from inline comments and
	@# test assertion strings while preserving the underlying reasoning.
	@# This guard exists so the markers can't sneak back in via a future
	@# PR's review-reply notes or copy/paste of a finding name into code.
	@#
	@# The scan is intentionally narrow and conservatively portable:
	@#  * Uses `find -type f -name "*.go" -exec grep -wnE ...` rather than
	@#    GNU-grep's `--include` flag, which isn't POSIX. `-w` (word-
	@#    boundary match) is POSIX whereas `\b` is a GNU extension.
	@#  * Excludes lines containing `nosec` or `gosec` — those are
	@#    legitimate gosec suppression annotations (`//nosec G104`) that
	@#    happen to match the marker pattern but reference real linter
	@#    rule codes, not historical review findings.
	@#  * `.git/` is excluded by `-type f -name "*.go"` since git stores
	@#    blobs, not loose .go files.
	@#
	@# When a contributor needs to reference a historical review finding,
	@# the right place is the commit message body or the PR description,
	@# not source code. See PR A4 (#5) for the full rationale.
	@hits=$$(find . -type f -name "*.go" -exec grep -wnE '[CG][0-9]{1,3}' {} + 2>/dev/null | grep -vE 'nosec|gosec'); \
	if [ -n "$$hits" ]; then \
		echo "Found C##/G## review-finding markers in .go source — these must be scrubbed:"; \
		echo "$$hits"; \
		echo ""; \
		echo "Rewrite the comment to describe the property without naming the marker,"; \
		echo "or move the historical context into the commit message body."; \
		exit 1; \
	fi

# cross-build writes to a per-target file inside an OS-temp dir rather than
# /dev/null so the rule works on Windows hosts too (where /dev/null doesn't
# exist). The file is removed immediately after the build succeeds — we only
# care about exit code, not the artifact.
CROSS_BUILD_DIR := $(shell mktemp -d 2>/dev/null || echo .tfdry-cross)

cross-build: ## Build for every supported target to catch GOOS/GOARCH issues.
	@mkdir -p $(CROSS_BUILD_DIR)
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64  go build -o $(CROSS_BUILD_DIR)/tfdry-darwin-arm64  .
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64  go build -o $(CROSS_BUILD_DIR)/tfdry-linux-amd64   .
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64  go build -o $(CROSS_BUILD_DIR)/tfdry-linux-arm64   .
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64  go build -o $(CROSS_BUILD_DIR)/tfdry-windows-amd64.exe .
	@rm -rf $(CROSS_BUILD_DIR)

# ----- Benchmarks -----------------------------------------------------------

bench: ## Run all Go benchmarks once with allocation stats.
	go test ./... -bench=. -benchtime=5s -benchmem -count=1 -run='^$$'

bench-save: ## Save 6-run benchmarks to FILE for later comparison (FILE=before.txt).
	go test ./... -bench=. -benchtime=5s -benchmem -count=6 -run='^$$' | tee $(FILE)

bench-compare: ## Compare two saved benchmark files with benchstat (OLD=… NEW=…).
	benchstat $(OLD) $(NEW)

bench-pivot: ## Pivot saved benchmarks across a sub-name dimension (FILE=… COL=files).
	benchstat -col /$(COL) $(FILE)

bench-e2e: ## End-to-end benchmarks vs terraform fmt/validate, in a container.
	mkdir -p bench/results
	docker build -f bench/Dockerfile --build-arg TFDRY_VERSION=$(VERSION) -t tfdry-bench .
	docker run --rm --user $(shell id -u):$(shell id -g) -v "$(PWD)/bench/results:/out" tfdry-bench

bench-baseline: ## A/B compare HEAD against a baseline ref via hyperfine (BASELINE=ref, optional).
	bench/baseline.sh $(BASELINE)

bench-jsonv2: ## A/B compare default build vs GOEXPERIMENT=jsonv2 build (human + --json paths).
	EXPERIMENT=jsonv2 LABEL=jsonv2-human bench/baseline.sh
	EXPERIMENT=jsonv2 LABEL=jsonv2-json ARGS=--json bench/baseline.sh

clean: ## Remove the binary and bench/results.
	rm -f $(BINARY)
	rm -rf bench/results
