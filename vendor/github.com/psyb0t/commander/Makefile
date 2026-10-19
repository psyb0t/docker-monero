# Commander Makefile. Every Go command runs in the Dockerfile.dev image, so the
# host needs only Docker and Make.

MIN_TEST_COVERAGE ?= 85

DEV_IMAGE ?= commander-dev
UID := $(shell id -u)
GID := $(shell id -g)
DEV_RUN_EXTRA_ARGS ?=

# Dev containers may use every host thread except DEV_CPUS_RESERVED (never fewer
# than 1 thread for the container), which keeps the machine responsive during
# lint and tests. --cpus is the hard quota, and Go sizes GOMAXPROCS from it.
# Override the count with `make lint DEV_CPUS=4`, or drop the limit with
# `DEV_CPU_LIMIT_ARGS=`.
DEV_CPUS_RESERVED ?= 2
DEV_CPUS ?= $(shell cpus=$$(nproc 2>/dev/null || getconf _NPROCESSORS_ONLN); \
	cpus=$$((cpus - $(DEV_CPUS_RESERVED))); \
	[ "$$cpus" -ge 1 ] || cpus=1; \
	echo "$$cpus")
DEV_CPU_LIMIT_ARGS ?= --cpus=$(DEV_CPUS)

DEV_RUN := docker run --rm --init \
	--user $(UID):$(GID) \
	-e HOME=/tmp \
	-e GOPATH=/tmp/go \
	-e GOCACHE=/tmp/go-cache \
	-e GOMODCACHE=/tmp/go-mod-cache \
	-e MIN_TEST_COVERAGE=$(MIN_TEST_COVERAGE) \
	$(DEV_CPU_LIMIT_ARGS) \
	$(DEV_RUN_EXTRA_ARGS) \
	-v "$(CURDIR):/work" \
	-w /work \
	$(DEV_IMAGE)

.PHONY: all help dev-image dep lint lint-fix format test test-coverage sec

all: dep lint-fix test-coverage ## Run dep, lint-fix and test-coverage

help: ## Display this help message
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

dev-image: ## Build the development image used by every other target
	@docker build -q -t $(DEV_IMAGE) - < Dockerfile.dev >/dev/null

dep: dev-image ## Tidy and vendor dependencies
	@$(DEV_RUN) sh -ceu 'GOFLAGS=-mod=mod go mod tidy; go mod vendor'

lint: dev-image ## Lint all Go files (go fix diff and golangci-lint)
	@$(DEV_RUN) sh -ceu '\
		out=$$(go fix -diff ./... 2>&1) || true; \
		if [ -n "$$out" ]; then \
			echo "$$out"; \
			echo "go fix found issues. Run make lint-fix to apply."; \
			exit 1; \
		fi; \
		go tool golangci-lint run --timeout=30m0s ./...'

lint-fix: dev-image ## Lint all Go files and apply fixes
	@$(DEV_RUN) sh -ceu 'go fix ./...; go tool golangci-lint run --fix --timeout=30m0s ./...'

format: dev-image ## Format all Go files
	@$(DEV_RUN) go tool golangci-lint fmt ./...

test: dev-image ## Run all tests with the race detector
	@$(DEV_RUN) go test -race -count=1 ./...

test-coverage: dev-image ## Run tests with a coverage floor (MIN_TEST_COVERAGE), write coverage-percent.txt
	@$(DEV_RUN) bash scripts/make/test-coverage.sh

sec: dev-image ## Security scan (govulncheck and semgrep), writes sec.sarif
	@$(DEV_RUN) bash scripts/make/sec.sh
