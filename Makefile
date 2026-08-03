# lp10 — terminal player for the Arylic LP10 (Go).
# Run `make` (or `make help`) to list targets.

BINARY      := lp10
INSTALL_DIR := $(HOME)/.bin
# Release build: strip symbols/DWARF (-s -w) and local paths (-trimpath).
RELEASE     := -trimpath -ldflags "-s -w"

.DEFAULT_GOAL := help
.PHONY: help build run test cover install generate

help: ## List the targets (the default goal)
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-8s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Compile the binary into ./lp10
	go build -o $(BINARY) .

run: ## Launch the live TUI (needs a terminal + Keychain item)
	go run .

test: ## Vet and run the test suite
	go vet ./...
	go test ./...

generate: ## Regenerate embedded files (remote_loop.sh from remote_loop.src.sh)
	go generate ./...

cover: ## Merged unit + integration coverage of the shipped packages -> coverage.out
	@set -eu; \
	covdir=$$(mktemp -d); \
	unit=$$(mktemp); \
	intg=$$(mktemp); \
	merged=$$(mktemp); \
	out=$$(mktemp ./.coverage.out.XXXXXX); \
	cleanup() { rm -rf "$$covdir" "$$unit" "$$intg" "$$merged" "$$out"; }; \
	trap cleanup EXIT HUP INT TERM; \
	LP10_COVERDIR="$$covdir" go test ./... -coverpkg=./... -coverprofile="$$unit" >/dev/null; \
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
