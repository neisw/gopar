# gopar - Go PostgreSQL Partition Management

A Go library and CLI for managing PostgreSQL table partitions, with first-class support for nested LIST -> RANGE partitioning.

## Features

- **Partition Lifecycle Management**: Create, attach, detach, and drop table partitions
- **Nested Partitioning**: Automated LIST -> RANGE partition creation (e.g., partition by release, then by date)
- **Retention Policies**: Two-phase detach/drop with safety thresholds (90-day minimum, 75% partition count)
- **Partition Format Detection**: Auto-detect and convert between standard (`_YYYY_MM_DD`) and pg-partman (`_pYYYY_MM_DD`) naming
- **Pipeline System**: JSON-driven multi-step plans for orchestrating partition operations across tables
- **SQL Spec Execution**: Run migration/index SQL from JSON spec files with batch and concurrent modes
- **Dry-run Mode**: Preview all operations before applying changes

## Installation

### As a Library

```bash
go get github.com/neisw/gopar
```

### CLI Tool

```bash
# Install from source
make install

# Or build locally
make build
# Binary will be in ./build/gopar
```

## Requirements

- Go 1.24+
- PostgreSQL 11+ (for native partitioning support)
- [lib/pq](https://github.com/lib/pq) PostgreSQL driver (included as a dependency)

## Usage

### Basic Setup

```go
import (
    "database/sql"
    _ "github.com/lib/pq"
    "github.com/neisw/gopar/partitioning"
)

// Connect to PostgreSQL
dsn := "host=localhost user=postgres password=postgres dbname=mydb port=5432 sslmode=disable"
db, err := sql.Open("postgres", dsn)
if err != nil {
    panic(err)
}

// Create partition manager
dbp := partitioning.NewPartitions(db)
```

### Creating Flat RANGE Partitions

```go
// Assume you already have a partitioned table:
// CREATE TABLE events (...) PARTITION BY RANGE (created_at);

startDate := time.Now()
endDate := time.Now().AddDate(0, 0, 3)

// usePartmanFormat: false for _YYYY_MM_DD, true for _pYYYY_MM_DD
created, err := dbp.CreateMissingPartitions("events", startDate, endDate, false, false)
log.Printf("Created %d partitions", created)
```

### Creating Nested LIST -> RANGE Partitions

```go
// Table partitioned by LIST on release, with RANGE sub-partitions by date:
// CREATE TABLE test_results (...) PARTITION BY LIST (release);

releases := []string{"4.17", "4.18", "4.19"}
startDate := time.Now().AddDate(0, 0, -100)
endDate := time.Now().AddDate(0, 0, 2)

count, err := dbp.CreateMissingPartitionsListToRange(
    "test_results",
    releases,
    startDate,
    endDate,
    "date",       // date column name
    true,         // use partman format
    false,        // dry run
)
// Creates LIST intermediates + daily RANGE leaves:
// test_results (LIST by release)
// +-- test_results_4_17 (RANGE by date)
// |   +-- test_results_4_17_p2026_02_20
// |   +-- test_results_4_17_p2026_02_21
// |   +-- ...
// +-- test_results_4_18 (RANGE by date)
// |   +-- ...
// +-- test_results_4_19 (RANGE by date)
//     +-- ...
```

### Querying Partition Hierarchy

```go
// Get full partition hierarchy
hierarchy, err := dbp.GetPartitionHierarchy("test_results")

// Get only leaf partitions (those that hold data)
leaves, err := dbp.ListLeafPartitions("test_results")

// Get daily partitions for a specific release
v17Parts, err := dbp.GetDailyPartitionsForRelease("test_results", "4.17")
```

### Managing Partition Retention

```go
// Preview what would be affected
summary, err := dbp.GetRetentionSummary("events", 90, true)

// List partitions older than 90 days
candidates, err := dbp.GetPartitionsForRemoval("events", 90, true)

// Phase 1: Detach old partitions (keeps them as standalone tables)
detached, err := dbp.DetachOldPartitions("events", 90, false)
log.Printf("Detached %d partitions", detached)

// Phase 2: Drop previously detached partitions older than 100 days
dropped, err := dbp.DropOldDetachedPartitions("events", 100, false)
log.Printf("Dropped %d detached partitions", dropped)
```

### Partition Format Detection and Renaming

```go
// Detect whether existing partitions use partman naming (_pYYYY_MM_DD)
isPartman, err := dbp.DetectPartitionFormat("events")

// Rename partitions to match a target format (e.g., after a table swap)
renamed, err := dbp.RenamePartitionsToMatchConfig(
    "test_results",
    releases,
    true,  // target: partman format
    false, // dry run
)
```

### Dry-run Mode

Most operations support a `dryRun` parameter. When set to `true`, the operation will log what would be executed and return without making changes.

## CLI Usage

The `gopar` CLI provides two main commands:

### `gopar sql` - Execute SQL Specs

Run SQL migration/index specs from JSON files:

```bash
gopar sql \
    --dsn "host=localhost user=postgres dbname=mydb" \
    --spec-file config/specs/prowjobruntests/001_create_indexes.json \
    --dry-run

# Run specific specs from a file
gopar sql \
    --dsn "..." \
    --spec-file config/specs/example.json \
    --specs "create_index_a,create_index_b"
```

SQL spec files are JSON arrays of operations:

```json
[
  {
    "name": "create_index_on_date",
    "description": "Add index on date column",
    "concurrent": true,
    "query": "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_events_date ON events (date)"
  }
]
```

- `concurrent: true` runs the query outside a transaction (required for `CREATE INDEX CONCURRENTLY`)
- `concurrent: false` wraps the query in a transaction
- `batch_size` enables repeated execution for batch backfills

### `gopar manage` - Execute Partition Pipelines

Run multi-step partition management plans from JSON config files:

```bash
gopar manage \
    --dsn "host=localhost user=postgres dbname=mydb" \
    --config config/plans/prowjobruntests.json \
    --dry-run

# Resume from a specific step
gopar manage \
    --dsn "..." \
    --config config/plans/prowjobruntests_finalize.json \
    --start-step swap_tables
```

Plan files define a pipeline of steps:

```json
{
  "name": "migration_pipeline",
  "description": "Migrate tables to LIST->RANGE partitioning",
  "steps": [
    {
      "name": "discover_releases",
      "type": "query_releases",
      "query": "SELECT DISTINCT release FROM releases ORDER BY release",
      "store_as": "releases"
    },
    {
      "name": "create_partitions",
      "type": "create_partitions_list_to_range",
      "table": "test_results_new",
      "releases_from": "releases",
      "date_column": "date",
      "start_date": {"relative": "-100d"},
      "end_date": {"relative": "+2d"},
      "use_partman_format": true
    },
    {
      "name": "run_migration_sql",
      "type": "sql_specs",
      "spec_file": "config/specs/004_migrate_data.json"
    },
    {
      "name": "rename_partitions",
      "type": "rename_partitions",
      "table": "test_results",
      "releases_from": "releases",
      "use_partman_format": true
    }
  ]
}
```

**Available step types:**

| Type | Description |
|------|-------------|
| `query_releases` | Run a SQL query and store results as a variable |
| `sql_specs` | Execute a SQL spec file |
| `create_partitions_list_to_range` | Create nested LIST -> RANGE partitions |
| `create_partitions_range` | Create flat RANGE partitions |
| `rename_partitions` | Rename partitions to match naming config |
| `detach_old_partitions` | Detach partitions older than retention period |
| `drop_old_detached` | Drop previously detached partitions |

### Global Flags

```
--log-level string   Log level: trace, debug, info, warn, error (default "info")
--dsn string         PostgreSQL connection string
--dry-run            Preview operations without executing
```

## Key Concepts

### Partition Naming

gopar supports two naming conventions and auto-detects which is in use:

| Format | Example | When to use |
|--------|---------|-------------|
| Standard | `events_2024_01_15` | Default for new tables |
| pg-partman | `events_p2024_01_15` | Compatibility with pg_partman-managed tables |

Names that would exceed PostgreSQL's 63-character identifier limit are automatically shortened with an FNV hash suffix.

### Safety Thresholds

Retention operations enforce safety guards:
- **90-day minimum retention** - cannot detach partitions younger than 90 days
- **75% partition count threshold** - refuses to detach more than 75% of partitions
- **Two-phase cleanup** - detach first, drop separately, providing a recovery window

### Nested Partitioning

For LIST -> RANGE tables, the partition hierarchy has three levels:

```
parent_table (LIST by release)
+-- parent_table_4_17 (RANGE by date)    <- intermediate
|   +-- parent_table_4_17_p2026_01_01    <- leaf (holds data)
|   +-- parent_table_4_17_p2026_01_02
+-- parent_table_4_18 (RANGE by date)
    +-- parent_table_4_18_p2026_01_01
```

**Primary key requirement**: Must include all partition key columns.

```sql
PRIMARY KEY (id, release, date)
```

## API Reference

### Types

- `DB_PARTITIONS` - Main partition manager, created via `partitioning.NewPartitions(db)`
- `PartitionInfo` - Metadata about a single partition (name, date, age, size, row estimate)
- `PartitionedTableInfo` - Metadata about a partitioned parent table
- `PartitionStats` - Aggregate statistics (total partitions, size, date range)
- `RetentionSummary` - Preview of retention policy impact
- `PartitionHierarchyInfo` - Partition metadata within a nested hierarchy

### Methods on DB_PARTITIONS

#### Partition Discovery
- `ListPartitionedTables()` - List all partitioned tables in the database
- `ListTablePartitions(tableName)` - List all partitions for a table
- `ListAttachedPartitions(tableName)` - List currently attached partitions
- `ListDetachedPartitions(tableName)` - List detached (standalone) partitions
- `IsPartitionAttached(partitionName)` - Check if a partition is attached to its parent
- `GetPartitionColumns(tableName)` - Get partition key columns

#### Partition Creation
- `CreateMissingPartitions(tableName, startDate, endDate, usePartmanFormat, dryRun)` - Create daily RANGE partitions
- `CreateMissingPartitionsListToRange(tableName, releases, startDate, endDate, dateColumn, usePartmanFormat, dryRun)` - Create nested LIST -> RANGE partitions
- `AttachPartition(tableName, partitionName, usePartmanFormat, dryRun)` - Attach a detached partition

#### Partition Removal
- `DetachPartition(partitionName, dryRun)` - Detach a single partition
- `DetachOldPartitions(tableName, retentionDays, dryRun)` - Detach all partitions older than retention period
- `DropPartition(partitionName, dryRun)` - Drop a single partition
- `DropOldDetachedPartitions(tableName, retentionDays, dryRun)` - Drop detached partitions older than retention period

#### Statistics & Retention
- `GetPartitionStats(tableName)` - Aggregate stats for all partitions
- `GetAttachedPartitionStats(tableName)` - Stats for attached partitions only
- `GetDetachedPartitionStats(tableName)` - Stats for detached partitions only
- `GetPartitionsForRemoval(tableName, retentionDays, attachedOnly)` - List partitions eligible for removal
- `GetRetentionSummary(tableName, retentionDays, attachedOnly)` - Preview retention policy impact
- `ValidateRetentionPolicy(tableName, retentionDays)` - Validate safety thresholds

#### Nested Partition Operations
- `GetPartitionHierarchy(tableName)` - Get complete partition tree
- `ListLeafPartitions(tableName)` - Get only leaf partitions (those holding data)
- `GetPartitionLevel(partitionName)` - Get nesting depth of a partition
- `GetDailyPartitionsForRelease(tableName, release)` - Get daily partitions for one release

#### Partition Naming
- `DetectPartitionFormat(tableName)` - Returns true if partitions use pg-partman naming
- `RenamePartitionsToMatchConfig(tableName, releases, usePartmanFormat, dryRun)` - Rename partitions to target format

## Building and Testing

```bash
make help          # Show all targets
make build         # Build CLI binary
make test          # Run tests
make test-unit     # Run unit tests only (no database)
make e2e           # Run e2e tests (starts PostgreSQL container)
make run           # Build and run with example spec (dry-run)
make build-all     # Build for Linux, macOS, Windows
```

See [MAKEFILE_USAGE.md](MAKEFILE_USAGE.md) for detailed Makefile documentation.

## Testing

```bash
# Unit tests (no database required)
make test-unit

# E2E tests with containerized PostgreSQL
make e2e

# Integration tests against an external database
GOPAR_DSN="host=localhost user=postgres dbname=mydb" \
    go test -v ./test/integration/...
```

## License

Apache License 2.0

## Related Projects

- [pg_partman](https://github.com/pgpartman/pg_partman) - PostgreSQL extension for automated partition management (see [comparison](docs/gopar-vs-pgpartman.md))
- [lib/pq](https://github.com/lib/pq) - PostgreSQL driver for Go
