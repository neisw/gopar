# SQL Specification Files

This directory contains JSON specification files used by the test suite and CLI. These files define SQL operations like index creation, data backfills, and general SQL migrations.

## File Formats

### Index Specs (`example_index_specs.json`)

Defines index creation specifications, typically for `CREATE INDEX CONCURRENTLY` operations.

```json
[
  {
    "name": "idx_example_table_column",
    "query": "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_example_table_column\n\t\tON example_table (column_name)"
  }
]
```

**Fields:**
- `name` (string): Unique identifier for the index
- `query` (string): SQL query to create the index

### Backfill Specs (`example_backfill_specs.json`, `prow_job_runs_backfill.json`)

Defines batch backfill operations for updating existing data.

```json
[
  {
    "name": "backfill_table_name",
    "description": "Description of what is being backfilled",
    "concurrent": false,
    "batchSize": 500000,
    "query": "WITH batch AS (\n  SELECT id FROM table WHERE condition LIMIT $1\n)\nUPDATE table SET column = value FROM batch WHERE ..."
  }
]
```

**Fields:**
- `name` (string): Unique identifier for the backfill operation
- `description` (string): Human-readable description
- `concurrent` (boolean): If `true`, runs outside a transaction (typically `false` for backfills)
- `batchSize` (int64): Number of rows to process per batch (optional, if omitted spec runs once)
- `query` (string): SQL UPDATE query with `$1` placeholder for batch size

**Batch Processing:**

When `batchSize` is specified (> 0), the spec is executed in a loop:
1. The query is executed with `$1` set to the batch size
2. The number of affected rows is logged
3. If rows were affected, repeat from step 1
4. Loop continues until no rows are affected
5. Final summary shows total rows updated and number of batches

**Benefits:**
- Prevents long-running locks on large tables
- Allows progress monitoring during long operations
- Enables incremental backfills that can be paused/resumed

### SQL Specs (`example_sql_specs.json`)

Defines general SQL operations including migrations, function creation, and other DDL.

```json
[
  {
    "name": "create_helper_function",
    "description": "Description of the operation",
    "concurrent": false,
    "query": "CREATE OR REPLACE FUNCTION ..."
  }
]
```

**Fields:**
- `name` (string): Unique identifier for the spec
- `description` (string): Human-readable description
- `concurrent` (boolean): If `true`, runs outside a transaction (required for `CONCURRENTLY`)
- `resultType` (string): Controls query execution mode (optional, default `"exec"`):
  - `"exec"` — uses `db.Exec`, suitable for DDL/DML statements
  - `"query"` — uses `db.Query` and prints returned rows as a formatted table, suitable for SELECT validation queries
- `query` (string): SQL statement to execute

## Example Files

### `example_index_specs.json`
Example index creation specifications for partitioned tables.

### `example_backfill_specs.json`
Example backfill operation for denormalizing timestamp columns.

### `example_sql_specs.json`
Example SQL specs including function creation and index metadata queries.

### `prow_job_runs_backfill.json`
Production backfill specs for denormalizing columns across multiple Prow job-related tables:
- `backfill_prow_job_runs` - Backfill release column
- `backfill_prow_job_run_tests` - Backfill job ID, release, and timestamp
- `backfill_prow_job_run_test_outputs` - Backfill release and timestamp
- `backfill_prow_job_run_prow_pull_requests` - Backfill join table denormalized columns
- `backfill_prow_job_run_annotations` - Backfill annotation denormalized columns

## Usage in Tests

Load specs in your tests using the `loadSpecsFromJSONForTest` helper:

```go
type indexSpec struct {
    Name  string `json:"name"`
    Query string `json:"query"`
}

var specs []indexSpec
loadSpecsFromJSONForTest(t, "example_index_specs.json", &specs)
```

## Usage with CLI

Execute specs from a file using the `--spec-file` flag:

```bash
# Execute all specs in the file
gopar sql --dsn "..." --spec-file config/specs/001_prow_job_runs_backfill.json

# Execute specific specs from the file
gopar sql --dsn "..." --spec-file config/specs/001_prow_job_runs_backfill.json --specs backfill_prow_job_runs

# Dry-run to preview
gopar sql --dsn "..." --spec-file config/specs/001_prow_job_runs_backfill.json --dry-run
```

## Creating Custom Specs

1. Copy one of the example files
2. Modify the SQL queries for your use case
3. Ensure all fields match the `SQLSpec` structure:
   - `name`: unique identifier
   - `description`: human-readable description
   - `concurrent`: boolean for transaction control
   - `query`: SQL statement with `$1` for batch size (if applicable)
4. Update your test to load the new file or use via CLI

## File Naming Convention

- Use `example_` prefix for example/template files
- Use descriptive names that indicate the spec purpose (e.g., `prow_job_runs_backfill.json`)
- Use `.json` extension
