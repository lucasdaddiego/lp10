# lp10 — terminal player for the Arylic LP10 (Go).
# Run `make` (or `make help`) to list targets.

BINARY      := lp10
INSTALL_DIR ?= $(HOME)/.bin
# Release build: strip symbols/DWARF (-s -w) and local paths (-trimpath).
RELEASE     := -trimpath -ldflags "-s -w"

# CI pins the toolchain to go.mod's `go` line (setup-go go-version-file), so the
# ci target runs under the same one: staticcheck in particular reads export data
# and refuses a newer local toolchain.
CI_GO       := go$(shell awk '/^go /{print $$2}' go.mod)

.DEFAULT_GOAL := help
.PHONY: help build run test ci cover install

help: ## List the targets (the default goal)
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-8s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Compile the binary into ./lp10 (unstripped; replaces the ~/.bin-linked command)
	go build -o $(BINARY) .

run: ## Launch the live TUI (needs a terminal and the box on the LAN)
	go run .

test: ## Vet and run the test suite
	go vet ./...
	go test ./...

ci: ## Everything CI runs, in CI's order and toolchain: gofmt, vet, go fix -diff, staticcheck, govulncheck, race tests
	@unformatted="$$(gofmt -l .)"; if [ -n "$$unformatted" ]; then echo "These files need gofmt:"; echo "$$unformatted"; exit 1; fi
	GOTOOLCHAIN=$(CI_GO) go vet ./...
	GOTOOLCHAIN=$(CI_GO) go fix -diff ./...
	GOTOOLCHAIN=$(CI_GO) go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...
	GOTOOLCHAIN=$(CI_GO) go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
	GOTOOLCHAIN=$(CI_GO) go test -race ./...

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
	grep -vE '/internal/(e2e|testutil)/' "$$merged" > "$$out"; \
	report=$$(go tool cover -func="$$out"); \
	printf '%s\n' "$$report" | tail -1; \
	mv "$$out" coverage.out; \
	echo "shipped-package coverage (test scaffolding excluded); HTML: go tool cover -html=coverage.out"

install: ## Build a stripped release ./lp10 and link it from ~/.bin
	go build $(RELEASE) -o $(BINARY) .
	@mkdir -p "$(INSTALL_DIR)"
	ln -sfn "$(CURDIR)/$(BINARY)" "$(INSTALL_DIR)/$(BINARY)"
