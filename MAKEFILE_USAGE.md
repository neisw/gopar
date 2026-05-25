# Makefile Usage Guide

This document provides detailed information about using the Makefile for the gopar project.

## Quick Start

```bash
# Display all available targets
make help

# Build the CLI
make build

# Run tests
make test

# Build and run with example
make run
```

## Common Targets

### Building

- **`make build`** - Build the gopar CLI binary for your current platform
  - Output: `build/gopar`
  - Includes version information from git

- **`make build-all`** - Build for all platforms (Linux, macOS, Windows)
  - Creates binaries: `build/gopar_unix`, `build/gopar_darwin`, `build/gopar.exe`

- **`make install`** - Install gopar to `$GOPATH/bin`
  - Makes the binary available system-wide

### Testing

- **`make test`** - Run all tests
  - Skips integration tests if `GOPAR_TEST_DSN` is not set
  
- **`make test-unit`** - Run only unit tests (no database required)
  - Fast tests that don't need PostgreSQL
  
- **`make test-integration`** - Run integration tests
  - Requires `GOPAR_TEST_DSN` environment variable
  - Example: `GOPAR_TEST_DSN='host=localhost user=postgres...' make test-integration`

- **`make test-coverage`** - Generate test coverage report
  - Creates `coverage.html` with visual coverage report

### Code Quality

- **`make fmt`** - Format all Go source files
  - Uses `go fmt`

- **`make vet`** - Run `go vet` static analysis
  - Detects suspicious constructs

- **`make lint`** - Run golangci-lint (requires installation)
  - Comprehensive linting

- **`make check`** - Run fmt, vet, and tests
  - Quick quality check before committing

- **`make ci`** - Run CI checks (fmt, vet, test-coverage)
  - Full CI pipeline simulation

### Running

- **`make run`** - Build and run with example SQL specs
  - Executes `example_sql_specs.json` in dry-run mode

- **`make run-prow`** - Build and run with Prow backfill specs
  - Executes `prow_job_runs_backfill.json` in dry-run mode

### Utilities

- **`make clean`** - Remove build artifacts and coverage files
  - Cleans `build/` and `coverage/` directories

- **`make spec-files`** - List all SQL spec files
  - Shows all `.json` files in `config/specs/`

- **`make examples`** - Show example commands
  - Displays common usage examples

- **`make version`** - Display version information
  - Shows version, commit, and build date

## Environment Variables

### GOPAR_TEST_DSN

PostgreSQL connection string for integration tests.

```bash
export GOPAR_TEST_DSN="host=localhost user=postgres password=secret dbname=testdb port=5432 sslmode=disable"
make test-integration
```

Or inline:

```bash
GOPAR_TEST_DSN="host=localhost user=postgres..." make test-integration
```

### VERSION, COMMIT, BUILD_DATE

Override version information (normally auto-detected from git):

```bash
VERSION=1.0.0 COMMIT=abc123 BUILD_DATE=2024-01-01 make build
```

## Examples

### Development Workflow

```bash
# 1. Format code
make fmt

# 2. Run quality checks
make check

# 3. Build
make build

# 4. Test the binary
./build/gopar sql --dsn "..." --spec-file config/specs/example_sql_specs.json --dry-run
```

### Testing Workflow

```bash
# Run unit tests (fast, no DB needed)
make test-unit

# Set up database for integration tests
export GOPAR_TEST_DSN="host=localhost user=postgres password=postgres dbname=testdb"

# Run integration tests
make test-integration

# Generate coverage report
make test-coverage
# Open coverage.html in browser
```

### Release Workflow

```bash
# Clean previous builds
make clean

# Build for all platforms
make build-all

# Verify builds
ls -lh build/

# Test version information
./build/gopar version
```

### CI/CD Integration

```bash
# In CI pipeline
make ci

# Or step by step:
make fmt
make vet
make test
make build
```

## Custom Targets

### Running with Custom Specs

```bash
# Build first
make build

# Run with your custom spec file
./build/gopar sql \
  --dsn "host=localhost user=postgres..." \
  --spec-file /path/to/my_custom_specs.json \
  --dry-run

# Execute specific specs
./build/gopar sql \
  --dsn "host=localhost user=postgres..." \
  --spec-file config/specs/prow_job_runs_backfill.json \
  --specs backfill_prow_job_runs \
  --dry-run
```

### Batch Backfill Execution

```bash
# Build
make build

# Execute batch backfill (with real DSN, no dry-run)
./build/gopar sql \
  --dsn "host=prod-db user=app password=secret dbname=production" \
  --spec-file config/specs/prow_job_runs_backfill.json \
  --log-level info

# Output will show batch progress:
# info: Executing spec backfill_prow_job_runs in batches (batch size: 500000)
# info: backfill_prow_job_runs batch 1: 500000 rows updated (500000 total) in 2.3s
# info: backfill_prow_job_runs batch 2: 500000 rows updated (1000000 total) in 2.1s
# ...
```

## Troubleshooting

### "golangci-lint not found"

The `make lint` target requires golangci-lint to be installed:

```bash
# Install golangci-lint
# See: https://golangci-lint.run/usage/install/

# macOS
brew install golangci-lint

# Linux
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin
```

### Integration Tests Skipped

Integration tests require `GOPAR_TEST_DSN` environment variable:

```bash
# Set the environment variable
export GOPAR_TEST_DSN="host=localhost user=postgres password=postgres dbname=testdb"

# Run integration tests
make test-integration
```

### Build Fails

Ensure dependencies are downloaded:

```bash
make deps
make tidy
make build
```

## Tips

- Use `make help` to see all available targets
- Use `make examples` for quick reference
- Run `make check` before committing
- Use `make test-unit` for quick feedback during development
- Use `make test-coverage` to identify untested code
- Use `make spec-files` to see available SQL spec files
