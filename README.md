# gopar - Go PostgreSQL Partition Management

A focused Go library for managing PostgreSQL table partitions and migrating data from existing tables to partitioned ones.

## Features

- **Partition Lifecycle Management**: Create, attach, detach, and drop table partitions
- **Data Migration**: Migrate data from non-partitioned tables to partitioned tables  
- **Retention Policies**: Manage partition retention and clean up old data
- **Foreign Key Analysis**: Analyze and migrate foreign keys for partitioned tables
- **Schema Verification**: Verify table schemas and column compatibility
- **Dry-run Mode**: Test operations before applying changes

## What gopar Does NOT Include

Unlike comprehensive database tools, gopar is **focused on partition management and data migration**. It does **NOT** include:

- Schema creation/update from GORM models
- Creating partitioned tables from scratch
- Updating table schemas

You should create your partitioned tables using standard DDL or GORM AutoMigrate before using gopar for data migration and partition management.

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

## Building and Testing

This project includes a comprehensive Makefile for building, testing, and running the CLI:

```bash
# Show all available targets
make help

# Build the CLI
make build

# Run tests
make test

# Run with example specs
make run

# Build for all platforms
make build-all
```

See [MAKEFILE_USAGE.md](MAKEFILE_USAGE.md) for detailed Makefile documentation.

## Requirements

- Go 1.20 or later
- PostgreSQL 11+ (for native partitioning support)
- [GORM](https://gorm.io/) v1.20+

## Usage

### Basic Setup

```go
import (
    "gorm.io/driver/postgres"
    "gorm.io/gorm"
    "github.com/neisw/gopar"
)

// Connect to PostgreSQL using GORM
dsn := "host=localhost user=postgres password=postgres dbname=mydb port=5432 sslmode=disable"
gormDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
if err != nil {
    panic(err)
}

// Wrap with gopar
db := gopar.New(gormDB)
```

### Creating Partitions

```go
// Assume you already have a partitioned table created via DDL:
// CREATE TABLE events (...) PARTITION BY RANGE (created_at);

// Create daily partitions for a date range
startDate := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
endDate := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

created, err := db.CreateMissingPartitions("events", startDate, endDate, false)
if err != nil {
    log.Fatalf("Failed to create partitions: %v", err)
}
log.Printf("Created %d partitions", created)
```

### Migrating to Partitioned Tables

```go
// Step 0: Create the partitioned table first using DDL or GORM
// CREATE TABLE events_partitioned (...) PARTITION BY RANGE (created_at);

// Step 1: Initial migration (migrate data up to a specific date)
migrateUpTo := time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC)
err := db.MigrateToPartitionedTable("events", "created_at", migrateUpTo, false)
if err != nil {
    log.Fatalf("Failed to migrate: %v", err)
}

// Step 2: Update migration (incrementally catch up)
migrateUpTo = time.Date(2024, 12, 1, 0, 0, 0, 0, time.UTC)
err = db.UpdatePartitionedTableMigration("events", "created_at", migrateUpTo, false)
if err != nil {
    log.Fatalf("Failed to update migration: %v", err)
}

// Step 3: Finalize migration (swap tables)
finalMigrateUpTo := time.Now()
err = db.FinalizePartitionedTableMigration("events", "created_at", finalMigrateUpTo, true, false)
if err != nil {
    log.Fatalf("Failed to finalize migration: %v", err)
}
```

### Managing Partition Retention

```go
// List partitions older than 90 days
retentionDays := 90
partitions, err := db.GetPartitionsForRemoval("events", retentionDays, true)
if err != nil {
    log.Fatalf("Failed to get partitions: %v", err)
}

// Detach old partitions (safer than dropping)
count, err := db.DetachOldPartitions("events", retentionDays, false)
if err != nil {
    log.Fatalf("Failed to detach partitions: %v", err)
}
log.Printf("Detached %d partitions", count)

// Drop detached partitions
count, err = db.DropOldDetachedPartitions("events", retentionDays, false)
if err != nil {
    log.Fatalf("Failed to drop partitions: %v", err)
}
log.Printf("Dropped %d detached partitions", count)
```

### Foreign Key Analysis

```go
// Analyze the impact of partitioning a table on foreign keys
err := db.AnalyzePartitioningImpact("events")
if err != nil {
    log.Fatalf("Failed to analyze: %v", err)
}
```

## Key Concepts

### Daily Partitions

The library assumes daily partitions with the naming convention: `tablename_YYYY_MM_DD`

For example:
- `events_2024_01_01`
- `events_2024_01_02`
- etc.

### Nested (Multi-level) Partitioning

gopar supports nested partitioning where partitions are themselves partitioned. Common patterns:

**LIST → RANGE (Release/Daily Pattern)**

Partition by release version, then by date within each release:

```go
// Create nested partitions: LIST by release → RANGE by date
releases := []string{"v1.0", "v2.0", "v3.0"}
startDate := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
endDate := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

count, err := db.CreateMissingPartitionsListToRange(
    "events",
    releases,
    startDate,
    endDate,
    "event_date",
    false,
)
// Creates: 3 intermediate partitions + 1,095 daily leaf partitions
// Structure:
// events (LIST by release_name)
// ├── events_v1_0 (RANGE by event_date)
// │   ├── events_v1_0_2024_01_01
// │   ├── events_v1_0_2024_01_02
// │   └── ... (365 partitions)
// ├── events_v2_0 (RANGE by event_date)
// │   └── ... (365 partitions)
// └── events_v3_0 (RANGE by event_date)
//     └── ... (365 partitions)
```

**Querying Nested Partitions**

```go
// Get full partition hierarchy
hierarchy, err := db.GetPartitionHierarchy("events")

// Get only leaf partitions (those that hold data)
leaves, err := db.ListLeafPartitions("events")

// Get all daily partitions for a specific release
v1Partitions, err := db.GetDailyPartitionsForRelease("events", "v1.0")
```

**Nested Partition Retention**

```go
// Option 1: Drop entire release (intermediate + all children)
err := db.DetachPartition("events_v1_0", false)
err = db.DropPartition("events_v1_0", false)

// Option 2: Different retention per release
retentionByRelease := map[string]int{
    "v1.0": 30,  // Keep 30 days
    "v2.0": 90,  // Keep 90 days
    "v3.0": 0,   // Keep forever
}

for release, days := range retentionByRelease {
    if days == 0 {
        continue
    }
    
    partitions, _ := db.GetDailyPartitionsForRelease("events", release)
    cutoff := time.Now().AddDate(0, 0, -days)
    
    for _, p := range partitions {
        if p.PartitionDate != nil && p.PartitionDate.Before(cutoff) {
            db.DetachPartition(p.TableName, false)
        }
    }
}
```

**Important Notes for Nested Partitioning:**

1. **Primary Key Requirements**: Must include all partition keys
   ```sql
   -- For LIST → RANGE partitioning
   PRIMARY KEY (id, release_name, event_date)
   ```

2. **Partition Pruning**: Queries benefit from multi-level pruning
   ```sql
   -- Single partition scan
   WHERE release_name = 'v1.0' AND event_date = '2024-01-15'
   -- Only scans: events_v1_0_2024_01_15
   ```

3. **Supported Combinations**: LIST→RANGE, RANGE→LIST, RANGE→RANGE, LIST→LIST, HASH combinations

### Dry-run Mode

Most operations support a `dryRun` parameter. When set to `true`, the operation will:
- Validate the operation
- Log what would be executed
- Return without making any changes

### Foreign Keys on Partitioned Tables

PostgreSQL has restrictions on foreign keys with partitioned tables:

- **Outbound FKs** (partitioned table → non-partitioned table): Allowed ✅
- **Outbound FKs** (partitioned table → partitioned table): Not supported ❌
- **Inbound FKs** (non-partitioned table → partitioned table): Requires expanding FK to include partition key

gopar provides utilities to analyze and handle these cases during migrations.

## API Documentation

### Main Types

- `DB`: Main database wrapper providing all partition management methods
- `PartitionInfo`: Metadata about a partition
- `PartitionStats`: Aggregate statistics about partitions
- `RetentionSummary`: Summary of retention policy impact
- `PartitionHierarchyInfo`: Metadata about a partition in a nested hierarchy
- `PartitionLevel`: Depth in partition hierarchy (0=root, 1=first level, etc.)
- `MultiLevelPartitionConfig`: Configuration for multi-level partitioning

### Key Methods

#### Partition Management
- `CreateMissingPartitions()`: Create partitions for a date range
- `ListPartitionedTables()`: List all partitioned tables
- `ListTablePartitions()`: List partitions for a table
- `ListAttachedPartitions()`: List currently attached partitions
- `ListDetachedPartitions()`: List detached partitions
- `AttachPartition()`: Attach a detached partition
- `DetachPartition()`: Detach a partition (safer than dropping)
- `DropPartition()`: Drop a partition permanently

#### Nested Partition Management
- `CreateMissingPartitionsListToRange()`: Create LIST→RANGE nested partitions
- `GetPartitionHierarchy()`: Get complete partition hierarchy with all levels
- `ListLeafPartitions()`: Get only leaf partitions (those holding data)
- `GetPartitionLevel()`: Get the nesting level of a specific partition
- `GetDailyPartitionsForRelease()`: Get daily partitions for a specific release
- `IsPartitionAttached()`: Check if partition is attached to parent

#### Migration
- `MigrateToPartitionedTable()`: Initial migration to partitioned table (table must exist)
- `UpdatePartitionedTableMigration()`: Incremental migration update
- `FinalizePartitionedTableMigration()`: Finalize migration and swap tables
- `MigrateTableData()`: Migrate all data between tables
- `MigrateTableDataRange()`: Migrate data for a specific date range

#### Foreign Keys
- `AnalyzePartitioningImpact()`: Analyze FK impact of partitioning
- `GetFKRelationships()`: Get all FK relationships for a table
- `MoveForeignKeys()`: Move FKs when swapping tables

#### Retention Management
- `GetPartitionsForRemoval()`: List partitions older than retention period
- `GetRetentionSummary()`: Summary of retention policy impact
- `ValidateRetentionPolicy()`: Validate retention policy safety
- `DetachOldPartitions()`: Detach old partitions
- `DropOldDetachedPartitions()`: Drop detached partitions

#### Utilities
- `GetTableColumns()`: Get column information
- `VerifyTablesHaveSameColumns()`: Verify schema compatibility
- `GetTableRowCount()`: Get row count for a table
- `RenameTables()`: Atomically rename multiple tables
- `SyncIdentityColumn()`: Sync IDENTITY sequence after migration
- `GetPartitionStrategy()`: Get partition strategy for a table
- `VerifyPartitionCoverage()`: Verify partitions exist for date range

## Comparison with gorp

**gopar** is a focused subset of [gorp](https://github.com/neisw/gorp):

| Feature | gopar | gorp |
|---------|-------|------|
| Create partitioned tables from GORM models | ❌ | ✅ |
| Update table schemas | ❌ | ✅ |
| Partition lifecycle management | ✅ | ✅ |
| Data migration | ✅ | ✅ |
| Foreign key analysis | ✅ | ✅ |
| Retention policies | ✅ | ✅ |

**Use gopar when**: You want a lightweight library focused only on partition management and data migration, and you're comfortable creating table schemas yourself.

**Use gorp when**: You want comprehensive database utilities including schema creation/updates from GORM models.

## Testing

Run tests with:

```bash
go test -v
```

**Note**: Integration tests require a PostgreSQL database connection.

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

Apache License 2.0

## Related Projects

- [gorp](https://github.com/neisw/gorp) - Comprehensive PostgreSQL partition management with schema creation
- [GORM](https://gorm.io/) - The ORM library used by gopar
- [lib/pq](https://github.com/lib/pq) - PostgreSQL driver for Go
