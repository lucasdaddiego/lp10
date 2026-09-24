# lp10 — terminal player for the Arylic LP10 (Go).
# Run `make` (or `make help`) to list targets.

BINARY      := lp10
INSTALL_DIR := $(HOME)/.bin
# Release build: strip symbols/DWARF (-s -w) and local paths (-trimpath).
RELEASE     := -trimpath -ldflags "-s -w"

# CI pins the toolchain to go.mod's `go` line (setup-go go-version-file), so the
# ci target runs under the same one: staticcheck in particular reads export data
# and refuses a newer local toolchain.
CI_GO       := go$(shell awk '/^go /{print $$2}' go.mod)

# BUSYBOX is the official static image of BusyBox v1.32.0, the device loop's
# shell, pinned by digest. .github/workflows/ci.yml pins the same one.
BUSYBOX     :=busybox:1.32.0-uclibc@sha256:0c1b9fc207ab03d86cf4f6b5ce1b5b7973ccd595202c36f1de31ad8d6885a6b1

.DEFAULT_GOAL := help
.PHONY: help build run test busybox ci cover install generate

help: ## List the targets (the default goal)
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-8s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Compile the binary into ./lp10
	go build -o $(BINARY) .

run: ## Launch the live TUI (needs a terminal + Keychain item)
	go run .

test: ## Vet and run the test suite
	go vet ./...
	go test ./...

# busybox runs the transport tests again where `sh` is the box's shell: they run
# the loop's fragments with `sh`, which is bash on a Mac and dash on a CI runner.
# The test binary is built for Linux on this machine's arch and runs in the
# BusyBox image, with the repo mounted read-only (a test reads
# remote_loop.src.sh) and no network. The binary goes to a temporary directory
# under the repo, so that one mount carries both.
busybox: ## Run the transport tests under BusyBox 1.32.0 ash, the device's shell (needs docker)
	@set -eu; \
	command -v docker >/dev/null 2>&1 || { echo "busybox: needs docker on PATH" >&2; exit 1; }; \
	tmp=$$(mktemp -d .busybox.XXXXXX); \
	cleanup() { rm -rf "$$tmp"; }; \
	trap cleanup EXIT HUP INT TERM; \
	CGO_ENABLED=0 GOOS=linux go test -c -o "$$tmp/transport.test" ./internal/transport; \
	docker run --rm --network none -v "$(CURDIR)":/src:ro -w /src/internal/transport \
		$(BUSYBOX) sh -c 'busybox | head -1; exec "$$@"' sh "/src/$$tmp/transport.test" -test.count=1 -test.timeout=2m

ci: ## Everything CI runs, in CI's order and toolchain: gofmt, vet, go fix -diff, staticcheck, govulncheck, race tests, busybox
	@unformatted="$$(gofmt -l .)"; if [ -n "$$unformatted" ]; then echo "These files need gofmt:"; echo "$$unformatted"; exit 1; fi
	GOTOOLCHAIN=$(CI_GO) go vet ./...
	GOTOOLCHAIN=$(CI_GO) go fix -diff ./...
	GOTOOLCHAIN=$(CI_GO) go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...
	GOTOOLCHAIN=$(CI_GO) go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
	GOTOOLCHAIN=$(CI_GO) go test -race ./...
	@if command -v docker >/dev/null 2>&1; then GOTOOLCHAIN=$(CI_GO) $(MAKE) --no-print-directory busybox; \
	else echo "busybox: skipped, docker is not on PATH (CI runs it on the Linux leg)"; fi

generate: ## Regenerate embedded files (remote_loop.sh from remote_loop.src.sh)
	go generate ./...

# cover prints go test's log only on a failure, and then without the passing
# packages' summary lines (the whole log if nothing else is left): the failing
# tests, panics and build errors show instead of a bare "make: *** [cover] Error 1".
cover: ## Merged unit + integration coverage of the shipped packages -> coverage.out
	@set -eu; \
	covdir=$$(mktemp -d); \
	unit=$$(mktemp); \
	intg=$$(mktemp); \
	merged=$$(mktemp); \
	log=$$(mktemp); \
	out=$$(mktemp ./.coverage.out.XXXXXX); \
	cleanup() { rm -rf "$$covdir" "$$unit" "$$intg" "$$merged" "$$log" "$$out"; }; \
	trap cleanup EXIT HUP INT TERM; \
	if ! LP10_COVERDIR="$$covdir" go test ./... -coverpkg=./... -coverprofile="$$unit" >"$$log" 2>&1; then \
		grep -vE '^ok[[:space:]]+[^[:space:]]+[[:space:]]+([0-9.]+s|\(cached\))|^[[:space:]]+[^[:space:]:]+[[:space:]]+coverage: [0-9.]+% of statements' "$$log" >&2 || cat "$$log" >&2; \
		exit 1; \
	fi; \
	go tool covdata textfmt -i="$$covdir" -o="$$intg"; \
	go run github.com/wadey/gocovmerge@v0.0.0-20160331181800-b5bfa59ec0ad "$$unit" "$$intg" > "$$merged"; \
	grep -vE '/internal/(e2e|fixtures|testutil)/|/cmd/fakessh/' "$$merged" > "$$out"; \
	report=$$(go tool cover -func="$$out"); \
	printf '%s\n' "$$report" | tail -1; \
	mv "$$out" coverage.out; \
	echo "shipped-package coverage (test scaffolding excluded); HTML: go tool cover -html=coverage.out"

install: ## Install a stripped release binary into ~/.bin
	@mkdir -p $(INSTALL_DIR)
	go build $(RELEASE) -o "$(INSTALL_DIR)/$(BINARY)" .
	@echo "installed $(INSTALL_DIR)/$(BINARY)"
