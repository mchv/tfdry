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
MISSPELL_VERSION      := v0.7.0

.PHONY: help build test test-race verify tools tools-fmt tools-lint tools-vuln tools-misspell fmt fmt-check tidy-check lint lint-prose vet vuln check-no-markers cross-build bench bench-save bench-compare bench-pivot bench-e2e bench-baseline bench-jsonv2 bench-corpus-fetch bench-corpus-extract bench-corpus-refresh bench-corpus-clean clean

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

verify: fmt-check tidy-check vet lint lint-prose check-no-markers test-race vuln cross-build ## Run the full pre-PR verification pipeline.

tools: tools-fmt tools-lint tools-vuln tools-misspell ## Install every dev tool used by `make verify` (gofumpt, golangci-lint, govulncheck, misspell) into GOPATH/bin.

tools-fmt: ## Install gofumpt only.
	go install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)

tools-lint: ## Install golangci-lint only.
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

tools-vuln: ## Install govulncheck only — used by the standalone scheduled vuln-scan workflow.
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

tools-misspell: ## Install misspell only — used by the lint-prose target to lint .md / Makefile / workflow prose.
	go install github.com/golangci/misspell/cmd/misspell@$(MISSPELL_VERSION)

fmt: ## Apply gofumpt formatting in place. Use this to fix `make fmt-check` failures.
	@command -v gofumpt >/dev/null 2>&1 || { \
		echo "gofumpt not found in PATH. Run 'make tools' first."; \
		exit 1; \
	}
	@# Verify we're inside a git worktree first. Without this guard, a
	@# `git ls-files` failure (git missing, or run in a tarball export)
	@# would produce empty output, xargs -r would then no-op, and the
	@# pipeline would silently succeed without formatting anything.
	@git rev-parse --is-inside-work-tree >/dev/null 2>&1 || { \
		echo "make fmt: not inside a git worktree (git ls-files is unavailable)."; \
		exit 1; \
	}
	@# git ls-files -co --exclude-standard: tracked (-c) + untracked (-o)
	@# not gitignored (--exclude-standard). Catches new .go files a developer
	@# has just created without `git add`ing, while still excluding fetched
	@# third-party Go sources under bench/attr-corpus/files/.
	@# xargs -r suppresses invocation on empty input — otherwise GNU xargs
	@# would run gofumpt with no args and it would block reading from stdin.
	@git ls-files -co --exclude-standard -z -- '*.go' | xargs -0 -r gofumpt -w

fmt-check: ## Verify gofumpt formatting is clean. Fails with a diff if not.
	@command -v gofumpt >/dev/null 2>&1 || { \
		echo "gofumpt not found in PATH. Run 'make tools' first."; \
		exit 1; \
	}
	@# See `fmt` above for the rationale. `fmt-check` is CI-critical so a
	@# silent pass here is worse than an explicit error.
	@git rev-parse --is-inside-work-tree >/dev/null 2>&1 || { \
		echo "make fmt-check: not inside a git worktree (git ls-files is unavailable)."; \
		exit 1; \
	}
	@out=$$(git ls-files -co --exclude-standard -z -- '*.go' | xargs -0 -r gofumpt -l 2>&1); \
	if [ -n "$$out" ]; then \
		echo "Files need gofumpt formatting:"; \
		echo "$$out"; \
		echo ""; \
		echo "Run 'make fmt' to fix."; \
		exit 1; \
	fi

tidy-check: ## Verify go.mod / go.sum are canonical. Fails with a diff if `go mod tidy` would change anything.
	@# `go mod tidy -diff` (Go 1.23+) is the read-only version of
	@# `go mod tidy`: prints the diff and exits non-zero if go.mod /
	@# go.sum aren't canonical, without rewriting them. Two reasons we
	@# want this in `make verify` rather than relying on the goreleaser
	@# `before.hooks` check alone:
	@#
	@#   1. Shift-left. PRs catch un-tidy state at review time rather
	@#      than waiting for a tag to fail. The goreleaser hook stays
	@#      as defense-in-depth at release time.
	@#   2. Artefact-vs-tag reproducibility. If un-tidy go.mod ever
	@#      slipped to main and got tagged, a contributor cloning the
	@#      tag and running `go build` would produce a slightly different
	@#      binary than the published one (because `go build` resolves
	@#      against go.sum, which would carry stale entries). Catching
	@#      it pre-merge eliminates that drift category entirely.
	@#
	@# The error path uses `printf` (not `echo`) because the captured
	@# diff output can contain leading dashes (`---`, `+++`) that some
	@# `echo` implementations treat as flags. `printf '%s\n'` is portable
	@# and treats every argument as literal text.
	@#
	@# The failure message stays mode-agnostic ("tidy-check failed")
	@# because non-zero exit from `go mod tidy -diff` covers several
	@# scenarios beyond "needs tidying":
	@#   * Un-tidy go.mod (the common case) → output is a unified diff
	@#   * Go < 1.23 lacking the -diff flag → "flag provided but not defined"
	@#   * Module download / proxy errors → network-shaped messages
	@# The captured output above tells the user which mode they hit; the
	@# hint below explains what to do for each.
	@if ! out=$$(go mod tidy -diff 2>&1); then \
		printf 'tidy-check failed:\n\n%s\n\n' "$$out"; \
		printf '%s\n' \
			'If the output above is a unified diff against go.mod / go.sum,' \
			"run 'go mod tidy' locally and commit the resulting go.mod / go.sum." \
			'' \
			"If it reports 'flag provided but not defined: -diff', your local Go" \
			'is older than 1.23 — upgrade to match the `go` line in go.mod.' \
			'' \
			'Other non-zero exits (e.g. module-download or proxy failures) are' \
			'reported verbatim above.'; \
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

# ----- Prose linting --------------------------------------------------------
# golangci-lint's `misspell` plugin only scans .go files, so prose drift
# in user-facing docs (README, CHANGELOG, ...) and in build/CI config
# comments (Makefile, .github/workflows/, .goreleaser.yaml) slipped
# through until this target. `misspell -locale UK -error` flags US
# spellings and exits non-zero, mirroring the Go-side convention.
#
# Files we scan: README.md, PERFORMANCE.md, CHANGELOG.md, CONTRIBUTING.md, SECURITY.md,
# SKILL.md, Makefile, the workflow YAMLs (except codeql.yml — see
# below), .github/dependabot.yml, and .goreleaser.yaml.
#
# Files we deliberately skip, with rationale:
#
#   * CODE_OF_CONDUCT.md — Contributor Covenant 2.1 verbatim text. We
#     ship the upstream wording unchanged so contributors recognise the
#     canonical document; rewriting US spellings inside a boilerplate
#     clause would muddy that contract for zero benefit.
#
#   * .github/workflows/codeql.yml — the step names come from CodeQL's
#     own product naming. They're identifiers in GitHub's UI / matrix
#     logs, not free prose.
#
#   * TODO.md — internal planning prose that intentionally references
#     US/UK drift pairs as worked examples (mentioning both spellings
#     of common words). Linting it would either require word-level
#     whitelists so broad they defeat the purpose, or per-line
#     suppression markers misspell doesn't support. Internal scratch
#     doc, low public visibility, acceptable to skip.
#
# Ignore-rules (`-i`): mirror .golangci.yml's Go-side list (identifier
# references that misspell would otherwise want to rewrite — notably
# the public `output.Sanitize` API and Go-stdlib-adjacent terms like
# `initialize`/`artifact`). Plus `defense` for the documented
# idiomatic exception (the security-engineering phrase that uses the
# US spelling everywhere even in UK-localised writing — see
# .golangci.yml's misspell block for the rationale). `defense` is
# broader than strictly necessary (would whitelist the word in every
# context, not just inside the phrase), but misspell only supports
# word-level ignores and the word is rare enough in tfdry's
# tech-focused prose that the false-negative risk is acceptable.
PROSE_FILES := \
	README.md \
	PERFORMANCE.md \
	CHANGELOG.md \
	CONTRIBUTING.md \
	SECURITY.md \
	SKILL.md \
	Makefile \
	bench/README.md \
	bench/attr-corpus/README.md \
	.github/workflows/ci.yml \
	.github/workflows/govulncheck.yml \
	.github/workflows/release.yml \
	.github/dependabot.yml \
	.goreleaser.yaml

PROSE_IGNORE := sanitize,sanitized,sanitizes,sanitizing,behavior,initialize,categorizes,unrecognized,artifact,artifacts,defense

lint-prose: ## Lint Markdown / Makefile / workflow / goreleaser prose for US→UK drift.
	@command -v misspell >/dev/null 2>&1 || { \
		echo "misspell not found in PATH. Run 'make tools' first."; \
		exit 1; \
	}
	@misspell -locale UK -error -i '$(PROSE_IGNORE)' $(PROSE_FILES)

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

bench-corpus-fetch: ## Download pinned Terraform tarballs into bench/attr-corpus/files/.
	bench/attr-corpus/fetch.sh

bench-corpus-extract: ## Walk bench/attr-corpus/files/ with hclsyntax, refresh values/.
	bench/attr-corpus/extract.sh

bench-corpus-refresh: bench-corpus-fetch bench-corpus-extract ## Fetch + extract in one step.

bench-corpus-clean: ## Remove bench/attr-corpus/files/ (keeps committed values/).
	rm -rf bench/attr-corpus/files

clean: ## Remove the binary and bench/results.
	rm -f $(BINARY)
	rm -rf bench/results
