# lp10 — terminal player for the Arylic LP10 (Go).
# Run `make` (or `make help`) to list targets.

BINARY      := lp10
INSTALL_DIR := $(HOME)/.bin
# Release build: strip symbols/DWARF (-s -w) and local paths (-trimpath).
RELEASE     := -trimpath -ldflags "-s -w"

.DEFAULT_GOAL := help
.PHONY: help build run test install

help: ## List the targets (the default goal)
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-8s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Compile the binary into ./lp10
	go build -o $(BINARY) .

run: ## Launch the live TUI (needs a terminal + Keychain item)
	go run .

test: ## Vet and run the test suite
	go vet ./...
	go test ./...

install: ## Install a stripped release binary into ~/.bin
	@mkdir -p $(INSTALL_DIR)
	go build $(RELEASE) -o "$(INSTALL_DIR)/$(BINARY)" .
	@echo "installed $(INSTALL_DIR)/$(BINARY)"
