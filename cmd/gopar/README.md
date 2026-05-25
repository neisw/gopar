# gopar CLI

A command-line tool for managing PostgreSQL partitions and executing SQL migrations.

## Installation

```bash
go install github.com/neisw/gopar/cmd/gopar@latest
```

Or build from source:

```bash
cd cmd/gopar
go build -o gopar
```

## Usage

### Execute SQL Specs

The `sql` command allows you to define and execute SQL specifications for migrations, index creation, and other operations.

```bash
# Execute all SQL specs (from default built-in specs)
gopar sql --dsn "host=localhost user=postgres password=secret dbname=mydb port=5432 sslmode=disable"

# Execute specs from a JSON file
gopar sql --dsn "..." --spec-file config/specs/example_sql_specs.json

# Dry run to preview SQL without executing
gopar sql --dsn "..." --spec-file config/specs/example_sql_specs.json --dry-run

# Execute specific specs only from a file
gopar sql --dsn "..." --spec-file config/specs/example_sql_specs.json --specs create_partition_function

# Execute built-in specs selectively
gopar sql --dsn "..." --specs example_index_concurrent,example_composite_index

# Enable debug logging
gopar sql --dsn "..." --spec-file config/specs/example_sql_specs.json --log-level debug
```

### Defining Custom SQL Specs

#### Option 1: JSON Spec Files (Recommended)

Create a JSON file with your SQL specifications:

```json
[
  {
    "name": "my_custom_index",
    "description": "Create an index on my_table",
    "concurrent": true,
    "query": "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_my_custom\n    ON my_table (column1, column2)"
  },
  {
    "name": "my_migration",
    "description": "Add a new column",
    "concurrent": false,
    "query": "ALTER TABLE my_table ADD COLUMN IF NOT EXISTS new_column TEXT"
  }
]
```

Then execute:

```bash
gopar sql --dsn "..." --spec-file my_custom_specs.json
```

See `config/specs/` for example spec files.

#### Option 2: Code-Based Specs

Edit the `getDefaultSpecs()` function in `sql.go` to define built-in SQL specifications:

```go
func getDefaultSpecs() []SQLSpec {
    return []SQLSpec{
        {
            Name:        "my_custom_index",
            Description: "Create an index on my_table",
            Concurrent:  true,  // Use CONCURRENTLY to avoid locking
            Query: `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_my_custom
                ON my_table (column1, column2)`,
        },
        {
            Name:        "my_migration",
            Description: "Add a new column",
            Concurrent:  false,  // Run in a transaction
            Query: `ALTER TABLE my_table ADD COLUMN IF NOT EXISTS new_column TEXT`,
        },
    }
}
```

## Testing

### Configuration

Tests require a PostgreSQL database connection string set via the `GOPAR_TEST_DSN` environment variable:

```bash
export GOPAR_TEST_DSN="host=localhost user=postgres password=postgres dbname=testdb port=5432 sslmode=disable"
```

If `GOPAR_TEST_DSN` is not set, database tests will be skipped.

### Running Tests

```bash
# Run all tests (database tests will be skipped if GOPAR_TEST_DSN is not set)
go test -v -timeout 0

# Run specific test with DSN
GOPAR_TEST_DSN="host=localhost user=postgres..." go test -run Test_CreatePartitionIndexes -v -timeout 0

# Run backfill test
GOPAR_TEST_DSN="host=localhost user=postgres..." go test -run Test_BackfillDenormalizedColumns -v -timeout 0

# Run CLI integration tests
go test -run Test_SQLCommand -v
```

### SQL Specification Files

Test SQL operations are defined in JSON files located in `config/specs/`:

- `example_index_specs.json` - Index creation specifications
- `example_backfill_specs.json` - Batch backfill operations
- `example_sql_specs.json` - General SQL migrations and DDL

These JSON files are loaded by tests using the `loadSpecsFromJSON()` helper function. See `config/specs/README.md` for file format details.

### Test Examples

There are two types of tests:

#### Direct SQL Execution Tests (`sql_test.go`)

These tests directly execute SQL against the database for development and validation:

1. **Test_CreatePartitionIndexes**: Creates indexes using `CREATE INDEX CONCURRENTLY`
2. **Test_BackfillDenormalizedColumns**: Demonstrates batch backfilling to avoid long locks
3. **Test_ExecuteSQLSpecs**: Shows how to execute SQL specs using the `executeSpec()` function

#### CLI Integration Tests (`sql_integration_test.go`)

These tests invoke the actual CLI command, testing the full command flow:

1. **Test_SQLCommand_DryRun**: Tests dry-run mode via CLI with built-in specs
2. **Test_SQLCommand_WithEnvDSN**: Tests CLI with DSN from environment variable
3. **Test_SQLCommand_SelectiveSpecs**: Tests running specific built-in specs via CLI
4. **Test_SQLCommand_ExecuteWithRealDB**: Tests execution with custom injected specs
5. **Test_SQLCommand_WithCustomIndexSpecs**: Tests index creation via CLI with custom specs
6. **Test_SQLCommand_WithSpecFile**: Tests loading specs from `config/specs/example_sql_specs.json`
7. **Test_SQLCommand_WithSpecFileAndSelectiveSpecs**: Tests loading from file and running specific specs
8. **Test_SQLCommand_WithIndexSpecFile**: Tests loading index specs from `config/specs/example_index_specs.json`
9. **Test_SQLCommand_WithRealDBAndSpecFile**: Tests actual execution with spec file (requires DB)

#### Using Custom Specs in Tests

You can inject custom SQL specs for testing:

```go
customSpecs = []SQLSpec{
    {
        Name:        "test_my_migration",
        Description: "Test migration",
        Concurrent:  false,
        Query:       "ALTER TABLE ...",
    },
}
defer func() { customSpecs = nil }()

// Then execute via CLI
testRoot := &cobra.Command{Use: "gopar"}
testRoot.AddCommand(NewSQLCommand())
testRoot.SetArgs([]string{"sql", "--dsn", dsn, "--dry-run"})
testRoot.Execute()
```

## Features

- **Concurrent Index Creation**: Uses `CREATE INDEX CONCURRENTLY` to avoid locking tables
- **Batch Processing**: Automatic batching for large backfill operations with progress tracking
- **Dry Run Mode**: Preview SQL without executing
- **Selective Execution**: Run specific specs by name
- **Transaction Control**: Specs can run in or outside transactions
- **Progress Logging**: Shows execution time and rows affected for each spec/batch
- **Configurable Log Levels**: Control verbosity with `--log-level` flag

## SQL Spec Structure

Each SQL spec has the following fields:

- `Name`: Unique identifier for the spec
- `Description`: Human-readable description
- `Query`: The SQL statement to execute
- `Concurrent`: If `true`, runs outside a transaction (required for `CONCURRENTLY` operations)
- `BatchSize`: (Optional) When > 0, executes in batches until no rows are affected. Use `$1` in the query as a placeholder for the batch size.

## Version

Check the CLI version:

```bash
gopar version
```
