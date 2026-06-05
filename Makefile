BINARY  := tfdry
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags="-s -w -X github.com/mchv/tfdry/output.Version=$(VERSION)"

.PHONY: help build test bench bench-save bench-compare bench-pivot bench-e2e bench-baseline bench-jsonv2 clean

help: ## Show this help (list of available targets).
	@awk 'BEGIN {FS = ":.*## "; printf "Usage: make <target>\n\nTargets:\n"} \
	/^[a-zA-Z0-9_-]+:.*## / {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}' \
	$(MAKEFILE_LIST)

build: ## Build the tfdry binary into ./tfdry.
	CGO_ENABLED=0 go build -trimpath $(LDFLAGS) -o $(BINARY) .

test: ## Run unit tests across all packages.
	go test ./...

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
