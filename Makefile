# GoWe - CWL Workflow Engine
# https://github.com/wilke/GoWe
#
# Usage:
#   make              Build all binaries
#   make test         Run unit tests
#   make help         Show all targets
#   make V=1 ...      Verbose output

# ==============================================================================
# Variables
# ==============================================================================

GO           := go
GOFLAGS      ?=
VERSION      ?= $(shell git rev-parse HEAD 2>/dev/null || echo dev)
LDFLAGS      ?= -X main.Version=$(VERSION)
CGO_ENABLED  ?= 0

BINDIR       := ./bin
SCRIPTDIR    := ./scripts

# Map cmd directory name -> binary name
# cli -> gowe, server -> gowe-server, worker -> gowe-worker, rest unchanged
bin_name = $(if $(filter cli,$1),gowe,$(if $(filter server worker,$1),gowe-$1,$1))
# Map binary name -> cmd directory name
cmd_for = $(if $(filter gowe,$1),cli,$(if $(filter gowe-server,$1),server,$(if $(filter gowe-worker,$1),worker,$1)))

COMMANDS     := cli server worker cwl-runner scheduler gen-cwl-tools smoke-test verify-bvbrc
BINARIES     := $(foreach cmd,$(COMMANDS),$(BINDIR)/$(call bin_name,$(cmd)))

DOCKER_COMPOSE ?= docker compose
DOCKER_IMAGE   ?= gowe

# Verbose / quiet mode: use V=1 or VERBOSE=1
V       ?=
VERBOSE ?= $(V)
ifeq ($(VERBOSE),1)
  Q :=
  TEST_VERBOSE := -v
else
  Q := @
  TEST_VERBOSE :=
endif

# ==============================================================================
# .PHONY
# ==============================================================================

.PHONY: build install \
        test test-conformance test-all test-tier1 test-tier2 test-staging test-distributed \
        setup lint fmt vet \
        docker docker-compose-up docker-compose-down docker-test-up docker-test-down \
        apptainer apptainer-up apptainer-down \
        clean clean-all help FORCE

# ==============================================================================
# Default target
# ==============================================================================

.DEFAULT_GOAL := build

# ==============================================================================
# Core build targets
# ==============================================================================

## build: Build all binaries to ./bin/ (default)
build: $(BINARIES)

$(BINDIR)/%: FORCE
	$(Q)mkdir -p $(BINDIR)
	$(Q)echo "Building $*..."
	$(Q)CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $@ ./cmd/$(call cmd_for,$*)

FORCE:

## install: Install binaries to $$GOPATH/bin
install:
	$(Q)echo "Installing binaries..."
	$(Q)for cmd in $(COMMANDS); do \
		CGO_ENABLED=$(CGO_ENABLED) $(GO) install $(GOFLAGS) -ldflags '$(LDFLAGS)' ./cmd/$$cmd; \
	done

# ==============================================================================
# Testing targets
# ==============================================================================

## test: Run Go unit tests
test:
	$(Q)$(GO) test $(TEST_VERBOSE) ./...

## test-conformance: Run CWL conformance tests (84 required tests)
test-conformance: $(BINDIR)/cwl-runner
	$(Q)$(SCRIPTDIR)/run-conformance.sh required

## test-all: Run full test suite (all tiers, all modes)
test-all: build
	$(Q)$(SCRIPTDIR)/run-all-tests.sh $(if $(filter 1,$(VERBOSE)),-v)

## test-tier1: Run Tier 1 tests only (cwl-runner + unit)
test-tier1: $(BINDIR)/cwl-runner $(BINDIR)/gowe
	$(Q)$(SCRIPTDIR)/run-all-tests.sh -t 1 $(if $(filter 1,$(VERBOSE)),-v)

## test-tier2: Run Tier 2 tests only (server modes)
test-tier2: $(BINDIR)/gowe-server $(BINDIR)/gowe-worker $(BINDIR)/gowe
	$(Q)$(SCRIPTDIR)/run-all-tests.sh -t 2 $(if $(filter 1,$(VERBOSE)),-v)

## test-staging: Run staging backend tests
test-staging:
	$(Q)$(SCRIPTDIR)/run-staging-tests.sh $(if $(filter 1,$(VERBOSE)),-v)

## test-distributed: Run distributed mode tests
test-distributed: $(BINDIR)/gowe
	$(Q)$(SCRIPTDIR)/run-conformance-distributed.sh

# ==============================================================================
# Setup and development targets
# ==============================================================================

## setup: Run environment setup script
setup:
	$(Q)$(SCRIPTDIR)/setup-env.sh

## lint: Run static analysis (go vet + golangci-lint if available)
lint:
	$(Q)echo "Running go vet..."
	$(Q)$(GO) vet ./...
	$(Q)if command -v golangci-lint >/dev/null 2>&1; then \
		echo "Running golangci-lint..."; \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed, skipping (install: https://golangci-lint.run/welcome/install/)"; \
	fi

## fmt: Format Go source files
fmt:
	$(Q)echo "Formatting Go source files..."
	$(Q)gofmt -w -s $$(find . -name '*.go' -not -path './vendor/*' -not -path './.claude/*')

## vet: Run go vet
vet:
	$(Q)$(GO) vet ./...

# ==============================================================================
# Docker targets
# ==============================================================================

## docker: Build all Docker images
docker:
	$(Q)echo "Building server image..."
	$(Q)docker build -t $(DOCKER_IMAGE)-server -f Dockerfile .
	$(Q)echo "Building worker image..."
	$(Q)docker build -t $(DOCKER_IMAGE)-worker -f Dockerfile.worker .
	$(Q)echo "Building cwl-runner image..."
	$(Q)docker build -t $(DOCKER_IMAGE)-cwl-runner -f Dockerfile.cwl-runner .

## docker-compose-up: Start distributed test environment
docker-compose-up:
	$(Q)$(DOCKER_COMPOSE) -f docker-compose.yml up -d --build

## docker-compose-down: Stop distributed test environment
docker-compose-down:
	$(Q)$(DOCKER_COMPOSE) -f docker-compose.yml down -v

## docker-test-up: Start test service containers (MinIO, Shock)
docker-test-up:
	$(Q)$(DOCKER_COMPOSE) -f docker-compose.test.yml up -d

## docker-test-down: Stop test service containers
docker-test-down:
	$(Q)$(DOCKER_COMPOSE) -f docker-compose.test.yml down -v

# ==============================================================================
# Apptainer targets
# ==============================================================================

## apptainer: Build all Apptainer SIF images
apptainer: build
	$(Q)deploy/apptainer/build-sif.sh --no-build

## apptainer-up: Start Apptainer service stack (MongoDB + Shock + GoWe Server)
apptainer-up:
	$(Q)deploy/apptainer/start-services.sh

## apptainer-down: Stop Apptainer service stack
apptainer-down:
	$(Q)deploy/apptainer/stop-services.sh

# ==============================================================================
# Cleanup targets
# ==============================================================================

## clean: Remove build artifacts and test outputs
clean:
	$(Q)echo "Cleaning build artifacts..."
	$(Q)rm -rf $(BINDIR)
	$(Q)rm -rf ./tmp
	$(Q)rm -f conformance-results.txt
	$(Q)rm -f docker-compose.override.yml
	$(Q)rm -rf badges/
	$(Q)rm -rf cwl-output/
	$(Q)echo "Done."

## clean-all: Remove all artifacts including reports and Go cache
clean-all: clean
	$(Q)rm -rf ./reports
	$(Q)$(GO) clean -cache -testcache

# ==============================================================================
# Help
# ==============================================================================

## help: Show available make targets
help:
	@echo "GoWe - CWL Workflow Engine"
	@echo ""
	@echo "Usage: make [target] [V=1]"
	@echo ""
	@echo "Targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | \
		sed 's/^## //' | \
		awk -F': ' '{printf "  \033[36m%-24s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "Examples:"
	@echo "  make                    Build all binaries"
	@echo "  make test               Run unit tests"
	@echo "  make test-all V=1       Run full suite (verbose)"
	@echo "  make docker             Build all Docker images"
	@echo "  make clean              Remove build artifacts"
