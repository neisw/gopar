# Makefile for gopar - PostgreSQL partition management library and CLI

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
GOFMT=$(GOCMD) fmt
GOVET=$(GOCMD) vet

# Binary names
BINARY_NAME=gopar
BINARY_UNIX=$(BINARY_NAME)_unix
BINARY_DARWIN=$(BINARY_NAME)_darwin
BINARY_WINDOWS=$(BINARY_NAME).exe

# Directories
CMD_DIR=./cmd/gopar
BUILD_DIR=./build
COVERAGE_DIR=./coverage

# Version information
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT?=$(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE?=$(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

# Linker flags for version injection
LDFLAGS=-ldflags "-X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuildDate=$(BUILD_DATE)"

.PHONY: all build clean test test-all test-unit test-integration test-coverage \
	install uninstall fmt vet lint run help e2e \
	build-linux build-darwin build-windows build-all

# Default target
all: build

## help: Display this help message
help:
	@echo "gopar - PostgreSQL partition management library and CLI"
	@echo ""
	@echo "Usage:"
	@echo "  make [target]"
	@echo ""
	@echo "Targets:"
	@grep -E '^## [a-zA-Z_-]+:' $(MAKEFILE_LIST) | sed 's/## /  /' | column -t -s ':'
	@echo ""
	@echo "Environment Variables:"
	@echo "  GOPAR_TEST_DSN    PostgreSQL DSN for integration tests"
	@echo "  VERSION           Version string (default: git describe)"
	@echo "  COMMIT            Git commit hash (default: git rev-parse)"
	@echo "  BUILD_DATE        Build timestamp (default: current UTC time)"

## build: Build the gopar CLI binary
build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	cd $(CMD_DIR) && $(GOBUILD) $(LDFLAGS) -o ../../$(BUILD_DIR)/$(BINARY_NAME) -v

## build-linux: Build for Linux amd64
build-linux:
	@echo "Building for Linux amd64..."
	@mkdir -p $(BUILD_DIR)
	cd $(CMD_DIR) && GOOS=linux GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o ../../$(BUILD_DIR)/$(BINARY_UNIX) -v

## build-darwin: Build for macOS amd64
build-darwin:
	@echo "Building for macOS amd64..."
	@mkdir -p $(BUILD_DIR)
	cd $(CMD_DIR) && GOOS=darwin GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o ../../$(BUILD_DIR)/$(BINARY_DARWIN) -v

## build-windows: Build for Windows amd64
build-windows:
	@echo "Building for Windows amd64..."
	@mkdir -p $(BUILD_DIR)
	cd $(CMD_DIR) && GOOS=windows GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o ../../$(BUILD_DIR)/$(BINARY_WINDOWS) -v

## build-all: Build for all platforms (Linux, macOS, Windows)
build-all: build-linux build-darwin build-windows
	@echo "Built binaries for all platforms in $(BUILD_DIR)/"

## install: Install the gopar CLI to GOPATH/bin
install:
	@echo "Installing $(BINARY_NAME) to $(GOPATH)/bin..."
	cd $(CMD_DIR) && $(GOCMD) install $(LDFLAGS)

## uninstall: Remove the gopar CLI from GOPATH/bin
uninstall:
	@echo "Removing $(BINARY_NAME) from $(GOPATH)/bin..."
	@rm -f $(GOPATH)/bin/$(BINARY_NAME)

## clean: Remove build artifacts and coverage files
clean:
	@echo "Cleaning..."
	cd $(CMD_DIR) && $(GOCLEAN)
	@rm -rf $(BUILD_DIR)
	@rm -rf $(COVERAGE_DIR)
	@rm -f coverage.out coverage.html

## test: Run all tests (unit and integration if GOPAR_TEST_DSN is set)
test:
	@echo "Running tests..."
	cd $(CMD_DIR) && $(GOTEST) -v -timeout 30s ./...

## test-all: Run all tests with timeout
test-all:
	@echo "Running all tests with extended timeout..."
	cd $(CMD_DIR) && $(GOTEST) -v -timeout 10m ./...

## test-unit: Run only unit tests (no database required)
test-unit:
	@echo "Running unit tests..."
	cd $(CMD_DIR) && $(GOTEST) -v -short ./...

## test-integration: Run integration tests (requires GOPAR_TEST_DSN)
test-integration:
	@echo "Running integration tests (requires GOPAR_TEST_DSN)..."
	@if [ -z "$(GOPAR_TEST_DSN)" ]; then \
		echo "Error: GOPAR_TEST_DSN environment variable is not set"; \
		echo "Example: make test-integration GOPAR_TEST_DSN='host=localhost user=postgres...'"; \
		exit 1; \
	fi
	cd $(CMD_DIR) && GOPAR_TEST_DSN="$(GOPAR_TEST_DSN)" $(GOTEST) -v -timeout 0 \
		-run "Test_CreatePartitionIndexes|Test_BackfillDenormalizedColumns|Test_ExecuteSQLSpecs" ./...

## e2e: Run e2e tests (starts PostgreSQL container, runs partition lifecycle tests)
e2e:
	./scripts/e2e.sh

## test-coverage: Run tests with coverage report
test-coverage:
	@echo "Running tests with coverage..."
	@mkdir -p $(COVERAGE_DIR)
	cd $(CMD_DIR) && $(GOTEST) -v -coverprofile=../../coverage.out -covermode=atomic ./...
	cd $(CMD_DIR) && $(GOCMD) tool cover -html=../../coverage.out -o ../../coverage.html
	@echo "Coverage report generated: coverage.html"

## test-bench: Run benchmark tests
test-bench:
	@echo "Running benchmark tests..."
	cd $(CMD_DIR) && $(GOTEST) -bench=. -benchmem ./...

## fmt: Format all Go source files
fmt:
	@echo "Formatting Go source files..."
	$(GOFMT) ./...

## vet: Run go vet on all packages
vet:
	@echo "Running go vet..."
	$(GOVET) ./...

## lint: Run golangci-lint (requires golangci-lint installed)
lint:
	./hack/go-lint.sh run ./...

## tidy: Tidy and verify go.mod
tidy:
	@echo "Tidying go.mod..."
	$(GOMOD) tidy
	$(GOMOD) verify

## run: Build and run the CLI with example dry-run
run: build
	@echo "Running $(BINARY_NAME) with example spec file..."
	$(BUILD_DIR)/$(BINARY_NAME) sql \
		--dsn "host=localhost user=postgres dbname=testdb" \
		--spec-file config/specs/example_sql_specs.json \
		--dry-run

## run-prow: Build and run with prow backfill specs (dry-run)
run-prow: build
	@echo "Running $(BINARY_NAME) with Prow backfill specs..."
	$(BUILD_DIR)/$(BINARY_NAME) sql \
		--dsn "host=localhost user=postgres dbname=testdb" \
		--spec-file config/specs/prowjobruns/001_prow_job_runs_backfill.json \
		--dry-run

## version: Display version information
version: build
	@$(BUILD_DIR)/$(BINARY_NAME) version

## deps: Download dependencies
deps:
	@echo "Downloading dependencies..."
	$(GOMOD) download

## check: Run fmt, vet, and tests
check: fmt vet test
	@echo "All checks passed!"

## ci: Run CI checks (fmt, vet, lint, test-coverage)
ci: fmt vet test-coverage
	@echo "CI checks complete!"

## spec-files: List all SQL spec files
spec-files:
	@echo "SQL Specification Files:"
	@find config/specs -name "*.json" -type f | sort

## examples: Show example commands
examples:
	@echo "Example Commands:"
	@echo ""
	@echo "1. Build the CLI:"
	@echo "   make build"
	@echo ""
	@echo "2. Run with dry-run:"
	@echo "   make run"
	@echo ""
	@echo "3. Run tests:"
	@echo "   make test"
	@echo ""
	@echo "4. Run with custom DSN and spec file:"
	@echo "   ./build/gopar sql --dsn 'host=localhost...' --spec-file path/to/specs.json"
	@echo ""
	@echo "5. Execute specific specs:"
	@echo "   ./build/gopar sql --dsn '...' --spec-file ... --specs spec1,spec2"
	@echo ""
	@echo "6. Run integration tests:"
	@echo "   GOPAR_TEST_DSN='host=localhost...' make test-integration"
