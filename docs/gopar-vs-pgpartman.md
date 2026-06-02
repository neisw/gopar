# gopar vs pg_partman: Comparison Analysis

## Deployment Model

| | pg_partman | gopar |
|---|---|---|
| **Install** | PostgreSQL extension (`CREATE EXTENSION pg_partman`) — requires superuser or CREATE privilege, server filesystem access | Go library — imported as a module, no server-side installation |
| **Runtime** | In-database: BGW process or cron calling `run_maintenance()` | Application-side: called from Go code or CLI, connects via standard `database/sql` |
| **Dependencies** | Extension files on PG server; optionally `pg_partman_bgw` in `shared_preload_libraries` (requires restart) | Only `lib/pq` driver — works with any PostgreSQL connection |

**Implication:** pg_partman is unavailable in environments where you can't install extensions (locked-down managed services, strict DBA policies). gopar works anywhere you have a PostgreSQL connection string.

## Partition Creation

| | pg_partman | gopar |
|---|---|---|
| **RANGE (time)** | Automated via `create_parent()` + `run_maintenance()`. Interval-based: daily, weekly, monthly, hourly, etc. | `CreateMissingPartitions()` — explicit start/end date range, daily granularity |
| **RANGE (serial/ID)** | Supported — monitors max value, pre-creates ahead | Not supported |
| **LIST** | **Not automated** — LIST partitions must be created manually | Automated via `CreateMissingPartitionsListToRange()` — creates LIST partitions for each release value |
| **Pre-creation** | `premake` setting auto-creates N future partitions on each maintenance run | Explicit — caller specifies the date range (e.g., today + 2 days) |
| **Default partition** | Auto-creates `_default` to catch unrouted rows | Not managed — caller must handle if needed |
| **Idempotency** | Checks existing partitions, skips creation | Same — checks `pg_inherits`, returns 0 for already-existing partitions |
| **Template tables** | Clones indexes/constraints/grants from a template table to new partitions | Not managed — indexes must be created separately (via SQL specs in plan system) |

**Key difference:** pg_partman's `premake` model is fire-and-forget — maintenance runs on a schedule and keeps partitions ahead of the data. gopar requires explicit calls with date ranges, giving the caller full control but requiring orchestration.

## Nested / Sub-Partitioning

| | pg_partman | gopar |
|---|---|---|
| **LIST -> RANGE** | Partially supported: LIST level must be created manually; RANGE sub-level can be automated via `create_sub_parent()`. Each LIST child needs its own `part_config` entry. | **Fully automated**: `CreateMissingPartitionsListToRange()` creates both LIST intermediates and RANGE leaves in one call. |
| **Configuration** | Separate `part_config_sub` table per sub-level, per parent | Single function call with releases list + date range |
| **Adding a new release** | Manual: create the LIST partition, then call `create_sub_parent()` to register it | Automatic: include the new release in the releases list — gopar creates the intermediate and all daily children |

**Key difference:** This is gopar's primary advantage. pg_partman was designed for RANGE automation and treats LIST as a manual concern. gopar treats LIST -> RANGE nesting as a first-class operation.

## Naming Conventions

| | pg_partman | gopar |
|---|---|---|
| **Format** | Fixed: `{parent}_p{YYYY_MM_DD}` (the `_p` prefix is non-negotiable) | Supports both: standard (`_YYYY_MM_DD`) and partman-compatible (`_pYYYY_MM_DD`) |
| **Detection** | N/A — always uses its own format | `DetectPartitionFormat()` auto-detects which format existing partitions use |
| **Renaming** | N/A | `RenamePartitionsToMatchConfig()` renames partitions between formats (useful after table swaps) |
| **Name overflow** | No special handling — PostgreSQL's 63-char limit can be hit with long table names | Auto-shortens names and appends FNV hash when nested names would exceed 62 chars |

## Retention & Cleanup

| | pg_partman | gopar |
|---|---|---|
| **Configuration** | `retention` interval in `part_config` (e.g., `'90 days'`) | Explicit `retentionDays` int parameter per call |
| **Detach** | Automatic during `run_maintenance()` if `retention` is set | Explicit: `DetachOldPartitions(tableName, retentionDays, dryRun)` |
| **Drop** | Controlled by `retention_keep_table`: true = detach only, false = detach + drop | Two-phase by design: detach first (`DetachOldPartitions`), drop separately (`DropOldDetachedPartitions`) |
| **Archive** | `retention_schema` — can move detached tables to another schema | Not built-in |
| **Safety guards** | None — will detach/drop whatever the retention says | 90-day minimum retention, 75% partition count threshold, 80% storage threshold |
| **Dry-run** | No native dry-run for retention | All operations support `dryRun` flag |
| **Preview** | `check_default()` for data in default partition | `GetRetentionSummary()` shows what would be affected before execution |

**Key difference:** gopar enforces safety thresholds that pg_partman doesn't — you can't accidentally detach 90% of your partitions. The two-phase detach -> drop model also provides a recovery window.

## Maintenance Model

| | pg_partman | gopar |
|---|---|---|
| **Scheduling** | BGW (server-side timer) or external cron | Application-driven — CI/CD pipeline, cron job calling CLI, or in-process |
| **Scope** | `run_maintenance()` handles ALL managed tables in one call | Per-table operations — caller orchestrates across tables |
| **Atomicity** | Single function handles create + retention + default-partition data movement | Each operation (create, detach, drop) is a separate call, each wrapped in its own transaction |
| **Plan system** | N/A — configuration is in `part_config` table | JSON plan files define multi-step pipelines: discover releases -> create tables -> create partitions -> migrate data -> swap -> rename |

## Monitoring & Observability

| | pg_partman | gopar |
|---|---|---|
| **Config metadata** | `part_config` and `part_config_sub` tables — queryable from SQL | No persistent metadata — state is derived from PostgreSQL catalogs on each call |
| **Job monitoring** | Optional `pg_jobmon` integration for alerting on maintenance failures | Structured logging via `logrus` — caller integrates with their observability stack |
| **Diagnostic functions** | `check_default()`, `show_partitions()`, `show_partition_info()` | `GetPartitionStats()`, `GetRetentionSummary()`, `GetPartitionHierarchy()`, `ListLeafPartitions()` |

## Migration Support

| | pg_partman | gopar |
|---|---|---|
| **Data migration** | `partition_data_time()` / `partition_data_id()` — batch moves rows from old table into partitioned children | Not built-in — handled via SQL specs in plan steps (INSERT...SELECT) |
| **Table swap** | Not built-in | `SwapTable()` — atomic rename dance (target -> _old, source -> target, _old -> source) |
| **Adopt existing** | `create_parent()` can adopt existing partitions if they follow pg_partman naming | `DetectPartitionFormat()` + `RenamePartitionsToMatchConfig()` to normalize names |
| **Undo** | `undo_partition_time()` — moves data back to a single table | Not built-in |

## When to Use Which

**pg_partman is better when:**
- You have a straightforward RANGE-by-time table and want zero application code
- Your cloud provider supports the extension (RDS, Cloud SQL, Azure Flexible)
- You want the BGW to handle maintenance without any external scheduler
- You need hourly/weekly/monthly granularity (not just daily)
- You want template-table-based index propagation

**gopar is better when:**
- You need automated LIST -> RANGE nesting (the primary use case — e.g., partitioning by release then by date)
- You can't install PostgreSQL extensions in your environment
- You want safety guardrails (minimum retention, safety thresholds, dry-run)
- You need a table-swap migration workflow
- You want partition naming flexibility (standard vs partman format, auto-detection, rename)
- You need the plan/pipeline system for orchestrating multi-step partition management across several tables
