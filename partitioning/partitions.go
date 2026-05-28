package partitioning

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
	log "github.com/sirupsen/logrus"
)

type queryable interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// DB_PARTITIONS provides partition management functionality for PostgreSQL tables
type DB_PARTITIONS struct {
	db queryable
}

func NewPartitions(db *sql.DB) *DB_PARTITIONS {
	return &DB_PARTITIONS{db: db}
}

func (dbp *DB_PARTITIONS) withTx(fn func(*DB_PARTITIONS) error) error {
	sqlDB, ok := dbp.db.(*sql.DB)
	if !ok {
		return fmt.Errorf("cannot begin transaction: already in a transaction")
	}
	tx, err := sqlDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(&DB_PARTITIONS{db: tx}); err != nil {
		return err
	}
	return tx.Commit()
}

func scanRows[T any](rows *sql.Rows, scan func(*sql.Rows) (T, error)) ([]T, error) {
	defer rows.Close()
	var results []T
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, item)
	}
	return results, rows.Err()
}

// PartitionStrategy defines the partitioning strategy type
type PartitionStrategy string

const (
	// PartitionStrategyRange partitions by value ranges (e.g., date ranges)
	PartitionStrategyRange PartitionStrategy = "RANGE"
	// PartitionStrategyList partitions by discrete value lists
	PartitionStrategyList PartitionStrategy = "LIST"
	// PartitionStrategyHash partitions by hash of partition key
	PartitionStrategyHash PartitionStrategy = "HASH"
)

// escapeForLike escapes characters that have special meaning in SQL LIKE patterns.
func escapeForLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// PartitionInfo holds metadata about a partition
type PartitionInfo struct {
	TableName     string
	SchemaName    string
	PartitionDate time.Time
	Age           int
	SizeBytes     int64
	SizePretty    string
	RowEstimate   int64
}

func scanPartitionInfo(rows *sql.Rows) (PartitionInfo, error) {
	var p PartitionInfo
	err := rows.Scan(&p.TableName, &p.SchemaName, &p.PartitionDate, &p.Age, &p.SizeBytes, &p.SizePretty, &p.RowEstimate)
	return p, err
}

// PartitionedTableInfo holds metadata about a partitioned parent table
type PartitionedTableInfo struct {
	TableName         string
	SchemaName        string
	PartitionCount    int
	PartitionStrategy string
}

// PartitionStats holds aggregate statistics about partitions
type PartitionStats struct {
	TotalPartitions int
	TotalSizeBytes  int64
	TotalSizePretty string
	OldestDate      sql.NullTime
	NewestDate      sql.NullTime
	AvgSizeBytes    int64
	AvgSizePretty   string
}

func scanPartitionStats(row *sql.Row) (PartitionStats, error) {
	var s PartitionStats
	err := row.Scan(&s.TotalPartitions, &s.TotalSizeBytes, &s.TotalSizePretty,
		&s.OldestDate, &s.NewestDate, &s.AvgSizeBytes, &s.AvgSizePretty)
	return s, err
}

// RetentionSummary provides a summary of what would be affected by a retention policy
type RetentionSummary struct {
	RetentionDays      int
	CutoffDate         time.Time
	PartitionsToRemove int
	StorageToReclaim   int64
	StoragePretty      string
	OldestPartition    string
	NewestPartition    string
}

// PartitionLevel represents the depth in a partition hierarchy
type PartitionLevel int

const (
	PartitionLevelRoot PartitionLevel = 0 // The parent partitioned table
	PartitionLevel1    PartitionLevel = 1 // First level partitions
	PartitionLevel2    PartitionLevel = 2 // Second level partitions (sub-partitions)
	PartitionLevel3    PartitionLevel = 3 // Third level partitions
)

// PartitionHierarchyInfo holds metadata about a partition in a hierarchy
type PartitionHierarchyInfo struct {
	TableName       string         // Name of this partition
	ParentTable     string         // Name of parent table/partition
	Level           PartitionLevel // Depth in hierarchy (0 = root table)
	IsLeaf          bool           // True if this partition can hold data
	IsPartitioned   bool           // True if this partition is further sub-partitioned
	Strategy        string         // Partitioning strategy at this level (RANGE, LIST, HASH)
	PartitionKey    string         // Column(s) partitioned on
	Children        []string       // Names of child partitions
	PartitionBounds string         // FOR VALUES clause for this partition

	// For time-based partitions
	PartitionDate *time.Time // Date for time-based partitions (if applicable)

	// Size information
	SizeBytes   int64
	SizePretty  string
	RowEstimate int64
}

// PartitionGranularity defines the time granularity for RANGE partitioning
type PartitionGranularity string

const (
	GranularityYearly  PartitionGranularity = "YEARLY"
	GranularityMonthly PartitionGranularity = "MONTHLY"
	GranularityDaily   PartitionGranularity = "DAILY"
	GranularityHourly  PartitionGranularity = "HOURLY"
)

// SubPartitionConfig defines configuration for sub-partitioning
type SubPartitionConfig struct {
	Strategy    PartitionStrategy
	Columns     []string
	Modulus     int                  // For HASH partitioning
	Values      []string             // For LIST partitioning
	Granularity PartitionGranularity // For RANGE partitioning
}

// MultiLevelPartitionConfig supports multi-level partition configuration
type MultiLevelPartitionConfig struct {
	// Root level configuration
	RootStrategy PartitionStrategy
	RootColumns  []string

	// Sub-partition configurations (optional)
	// Index corresponds to partition level (0 = first sub-partition level, etc.)
	SubPartitions []SubPartitionConfig
}

// PartitionConfig defines the configuration for creating a partitioned table
type PartitionConfig struct {
	// Strategy is the partitioning strategy (RANGE, LIST, or HASH)
	Strategy PartitionStrategy

	// Columns are the column(s) to partition by
	// For RANGE and LIST: typically one column (e.g., "date", "created_at")
	// For HASH: can be one or more columns
	Columns []string

	// Modulus is required for HASH partitioning (number of partitions)
	// Not used for RANGE or LIST
	Modulus int
}

// NewRangePartitionConfig creates a partition config for RANGE partitioning
func NewRangePartitionConfig(column string) PartitionConfig {
	return PartitionConfig{
		Strategy: PartitionStrategyRange,
		Columns:  []string{column},
	}
}

// NewListPartitionConfig creates a partition config for LIST partitioning
func NewListPartitionConfig(column string) PartitionConfig {
	return PartitionConfig{
		Strategy: PartitionStrategyList,
		Columns:  []string{column},
	}
}

// NewHashPartitionConfig creates a partition config for HASH partitioning
func NewHashPartitionConfig(modulus int, columns ...string) PartitionConfig {
	return PartitionConfig{
		Strategy: PartitionStrategyHash,
		Columns:  columns,
		Modulus:  modulus,
	}
}

// Validate checks if the partition configuration is valid
func (pc PartitionConfig) Validate() error {
	if pc.Strategy == "" {
		return fmt.Errorf("partition strategy must be specified")
	}

	if len(pc.Columns) == 0 {
		return fmt.Errorf("at least one partition column must be specified")
	}

	switch pc.Strategy {
	case PartitionStrategyRange, PartitionStrategyList:
		if len(pc.Columns) != 1 {
			return fmt.Errorf("%s partitioning requires exactly one column, got %d", pc.Strategy, len(pc.Columns))
		}
	case PartitionStrategyHash:
		if pc.Modulus <= 0 {
			return fmt.Errorf("HASH partitioning requires modulus > 0, got %d", pc.Modulus)
		}
	default:
		return fmt.Errorf("unknown partition strategy: %s (valid: RANGE, LIST, HASH)", pc.Strategy)
	}

	return nil
}

// ToSQL generates the PARTITION BY clause for the CREATE TABLE statement
func (pc PartitionConfig) ToSQL() string {
	columnList := strings.Join(pc.Columns, ", ")

	switch pc.Strategy {
	case PartitionStrategyRange:
		return fmt.Sprintf("PARTITION BY RANGE (%s)", columnList)
	case PartitionStrategyList:
		return fmt.Sprintf("PARTITION BY LIST (%s)", columnList)
	case PartitionStrategyHash:
		return fmt.Sprintf("PARTITION BY HASH (%s)", columnList)
	default:
		return ""
	}
}

// ListPartitionedTables returns all partitioned parent tables in the database
func (dbp *DB_PARTITIONS) ListPartitionedTables() ([]PartitionedTableInfo, error) {
	start := time.Now()

	query := `
		SELECT
			c.relname AS tablename,
			n.nspname AS schemaname,
			COUNT(i.inhrelid)::INT AS partition_count,
			CASE pp.partstrat
				WHEN 'r' THEN 'RANGE'
				WHEN 'l' THEN 'LIST'
				WHEN 'h' THEN 'HASH'
				ELSE 'UNKNOWN'
			END AS partition_strategy
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_partitioned_table pp ON pp.partrelid = c.oid
		LEFT JOIN pg_inherits i ON i.inhparent = c.oid
		WHERE n.nspname = 'public'
		GROUP BY c.relname, n.nspname, pp.partstrat
		ORDER BY c.relname
	`

	rows, err := dbp.db.Query(query)
	if err != nil {
		log.WithError(err).Error("failed to list partitioned tables")
		return nil, err
	}
	tables, err := scanRows(rows, func(r *sql.Rows) (PartitionedTableInfo, error) {
		var t PartitionedTableInfo
		err := r.Scan(&t.TableName, &t.SchemaName, &t.PartitionCount, &t.PartitionStrategy)
		return t, err
	})
	if err != nil {
		log.WithError(err).Error("failed to list partitioned tables")
		return nil, err
	}

	elapsed := time.Since(start)
	log.WithFields(log.Fields{
		"count":   len(tables),
		"elapsed": elapsed,
	}).Info("listed partitioned tables")

	return tables, nil
}

// ListTablePartitions returns all partitions for a given table
func (dbp *DB_PARTITIONS) ListTablePartitions(tableName string) ([]PartitionInfo, error) {
	start := time.Now()

	// Detect partition format
	usePartmanFormat, err := dbp.DetectPartitionFormat(tableName)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Debug("failed to detect partition format, assuming standard")
		usePartmanFormat = false
	}

	// Build SQL pattern based on detected format
	sqlPattern := GetPartitionSQLPattern(usePartmanFormat)
	tablePattern := getPartitionLikePattern(tableName, usePartmanFormat)

	query := fmt.Sprintf(`
		SELECT
			tablename,
			'public' as schemaname,
			TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD') AS partition_date,
			(CURRENT_DATE - TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD'))::INT AS age_days,
			pg_total_relation_size('public.'||tablename) AS size_bytes,
			pg_size_pretty(pg_total_relation_size('public.'||tablename)) AS size_pretty,
			COALESCE(n_live_tup, 0) AS row_estimate
		FROM pg_tables
		LEFT JOIN pg_stat_user_tables ON pg_stat_user_tables.relname = pg_tables.tablename
			AND pg_stat_user_tables.schemaname = pg_tables.schemaname
		WHERE pg_tables.schemaname = 'public'
			AND pg_tables.tablename LIKE $1 ESCAPE '\'
		ORDER BY partition_date ASC
	`, sqlPattern, sqlPattern)

	rows, err := dbp.db.Query(query, tablePattern)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Error("failed to list table partitions")
		return nil, err
	}
	partitions, err := scanRows(rows, scanPartitionInfo)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Error("failed to list table partitions")
		return nil, err
	}

	elapsed := time.Since(start)
	log.WithFields(log.Fields{
		"table":   tableName,
		"count":   len(partitions),
		"elapsed": elapsed,
	}).Info("listed table partitions")

	return partitions, nil
}

// GetPartitionStats returns aggregate statistics about partitions for a given table
func (dbp *DB_PARTITIONS) GetPartitionStats(tableName string) (*PartitionStats, error) {
	start := time.Now()

	// Detect partition format
	usePartmanFormat, err := dbp.DetectPartitionFormat(tableName)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Debug("failed to detect partition format, assuming standard")
		usePartmanFormat = false
	}

	// Build patterns based on detected format
	sqlPattern := GetPartitionSQLPattern(usePartmanFormat)
	tablePattern := getPartitionLikePattern(tableName, usePartmanFormat)

	query := fmt.Sprintf(`
		WITH partition_info AS (
			SELECT
				tablename,
				TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD') AS partition_date,
				pg_total_relation_size('public.'||tablename) AS size_bytes
			FROM pg_tables
			WHERE schemaname = 'public'
				AND tablename LIKE $1 ESCAPE '\'
		)
		SELECT
			COUNT(*)::INT AS total_partitions,
			SUM(size_bytes)::BIGINT AS total_size_bytes,
			pg_size_pretty(SUM(size_bytes)) AS total_size_pretty,
			MIN(partition_date) AS oldest_date,
			MAX(partition_date) AS newest_date,
			AVG(size_bytes)::BIGINT AS avg_size_bytes,
			pg_size_pretty(AVG(size_bytes)::BIGINT) AS avg_size_pretty
		FROM partition_info
	`, sqlPattern)

	stats, err := scanPartitionStats(dbp.db.QueryRow(query, tablePattern))
	if err != nil {
		log.WithError(err).WithField("table", tableName).Error("failed to get partition statistics")
		return nil, err
	}

	elapsed := time.Since(start)
	log.WithFields(log.Fields{
		"table":            tableName,
		"total_partitions": stats.TotalPartitions,
		"total_size":       stats.TotalSizePretty,
		"elapsed":          elapsed,
	}).Info("retrieved partition statistics")

	return &stats, nil
}

// GetPartitionsForRemoval identifies partitions older than the retention period for a given table
// This is a read-only operation (dry-run) that shows what would be removed (deleted or detached)
// If attachedOnly is true, only returns attached partitions (useful for detach operations)
// If attachedOnly is false, returns all partitions (useful for drop operations on both attached and detached)
func (dbp *DB_PARTITIONS) GetPartitionsForRemoval(tableName string, retentionDays int, attachedOnly bool) ([]PartitionInfo, error) {
	start := time.Now()

	cutoffDate := time.Now().AddDate(0, 0, -retentionDays)

	// Detect partition format
	usePartmanFormat, err := dbp.DetectPartitionFormat(tableName)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Debug("failed to detect partition format, assuming standard")
		usePartmanFormat = false
	}

	// Build patterns based on detected format
	sqlPattern := GetPartitionSQLPattern(usePartmanFormat)
	tablePattern := getPartitionLikePattern(tableName, usePartmanFormat)

	var query string
	var args []any
	if attachedOnly {
		// Only return attached partitions
		query = fmt.Sprintf(`
			WITH attached_partitions AS (
				SELECT c.relname AS tablename
				FROM pg_inherits i
				JOIN pg_class c ON i.inhrelid = c.oid
				JOIN pg_class p ON i.inhparent = p.oid
				WHERE p.relname = $1
			)
			SELECT
				tablename,
				'public' as schemaname,
				TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD') AS partition_date,
				(CURRENT_DATE - TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD'))::INT AS age_days,
				pg_total_relation_size('public.'||tablename) AS size_bytes,
				pg_size_pretty(pg_total_relation_size('public.'||tablename)) AS size_pretty,
				COALESCE(n_live_tup, 0) AS row_estimate
			FROM pg_tables
			LEFT JOIN pg_stat_user_tables ON pg_stat_user_tables.relname = pg_tables.tablename
				AND pg_stat_user_tables.schemaname = pg_tables.schemaname
			WHERE pg_tables.schemaname = 'public'
				AND pg_tables.tablename LIKE $2 ESCAPE '\'
				AND pg_tables.tablename IN (SELECT tablename FROM attached_partitions)
				AND TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD') < $3
			ORDER BY partition_date ASC
		`, sqlPattern, sqlPattern, sqlPattern)
		args = []any{tableName, tablePattern, cutoffDate}
	} else {
		// Return all partitions (attached + detached)
		query = fmt.Sprintf(`
			SELECT
				tablename,
				'public' as schemaname,
				TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD') AS partition_date,
				(CURRENT_DATE - TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD'))::INT AS age_days,
				pg_total_relation_size('public.'||tablename) AS size_bytes,
				pg_size_pretty(pg_total_relation_size('public.'||tablename)) AS size_pretty,
				COALESCE(n_live_tup, 0) AS row_estimate
			FROM pg_tables
			LEFT JOIN pg_stat_user_tables ON pg_stat_user_tables.relname = pg_tables.tablename
				AND pg_stat_user_tables.schemaname = pg_tables.schemaname
			WHERE pg_tables.schemaname = 'public'
				AND pg_tables.tablename LIKE $1 ESCAPE '\'
				AND TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD') < $2
			ORDER BY partition_date ASC
		`, sqlPattern, sqlPattern, sqlPattern)
		args = []any{tablePattern, cutoffDate}
	}

	rows, err := dbp.db.Query(query, args...)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Error("failed to get partitions for removal")
		return nil, err
	}
	partitions, err := scanRows(rows, scanPartitionInfo)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Error("failed to get partitions for removal")
		return nil, err
	}

	elapsed := time.Since(start)
	log.WithFields(log.Fields{
		"table":          tableName,
		"retention_days": retentionDays,
		"cutoff_date":    cutoffDate.Format("2006-01-02"),
		"attached_only":  attachedOnly,
		"count":          len(partitions),
		"elapsed":        elapsed,
	}).Info("identified partitions for removal")

	return partitions, nil
}

// GetRetentionSummary provides a summary of what would be affected by a retention policy for a given table
// If attachedOnly is true, only considers attached partitions (useful for detach operations)
// If attachedOnly is false, considers all partitions (useful for drop operations on both attached and detached)
func (dbp *DB_PARTITIONS) GetRetentionSummary(tableName string, retentionDays int, attachedOnly bool) (*RetentionSummary, error) {
	start := time.Now()

	cutoffDate := time.Now().AddDate(0, 0, -retentionDays)

	var summary RetentionSummary
	summary.RetentionDays = retentionDays
	summary.CutoffDate = cutoffDate

	// Detect partition format
	usePartmanFormat, err := dbp.DetectPartitionFormat(tableName)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Debug("failed to detect partition format, assuming standard")
		usePartmanFormat = false
	}

	// Build patterns based on detected format
	sqlPattern := GetPartitionSQLPattern(usePartmanFormat)
	tablePattern := getPartitionLikePattern(tableName, usePartmanFormat)

	var query string
	var args []any
	if attachedOnly {
		// Only consider attached partitions
		query = fmt.Sprintf(`
			WITH attached_partitions AS (
				SELECT c.relname AS tablename
				FROM pg_inherits i
				JOIN pg_class c ON i.inhrelid = c.oid
				JOIN pg_class p ON i.inhparent = p.oid
				WHERE p.relname = $1
			)
			SELECT
				COUNT(*)::INT AS partitions_to_remove,
				COALESCE(SUM(pg_total_relation_size('public.'||tablename)), 0)::BIGINT AS storage_to_reclaim,
				COALESCE(pg_size_pretty(SUM(pg_total_relation_size('public.'||tablename))), '0 bytes') AS storage_pretty,
				COALESCE(MIN(tablename), '') AS oldest_partition,
				COALESCE(MAX(tablename), '') AS newest_partition
			FROM pg_tables
			WHERE schemaname = 'public'
				AND tablename LIKE $2 ESCAPE '\'
				AND tablename IN (SELECT tablename FROM attached_partitions)
				AND TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD') < $3
		`, sqlPattern)
		args = []any{tableName, tablePattern, cutoffDate}
	} else {
		// Consider all partitions (attached + detached)
		query = fmt.Sprintf(`
			SELECT
				COUNT(*)::INT AS partitions_to_remove,
				COALESCE(SUM(pg_total_relation_size('public.'||tablename)), 0)::BIGINT AS storage_to_reclaim,
				COALESCE(pg_size_pretty(SUM(pg_total_relation_size('public.'||tablename))), '0 bytes') AS storage_pretty,
				COALESCE(MIN(tablename), '') AS oldest_partition,
				COALESCE(MAX(tablename), '') AS newest_partition
			FROM pg_tables
			WHERE schemaname = 'public'
				AND tablename LIKE $1 ESCAPE '\'
				AND TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD') < $2
		`, sqlPattern)
		args = []any{tablePattern, cutoffDate}
	}

	err = dbp.db.QueryRow(query, args...).Scan(
		&summary.PartitionsToRemove,
		&summary.StorageToReclaim,
		&summary.StoragePretty,
		&summary.OldestPartition,
		&summary.NewestPartition,
	)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Error("failed to get retention summary")
		return nil, err
	}

	elapsed := time.Since(start)
	log.WithFields(log.Fields{
		"table":                tableName,
		"retention_days":       retentionDays,
		"attached_only":        attachedOnly,
		"partitions_to_remove": summary.PartitionsToRemove,
		"storage_to_reclaim":   summary.StoragePretty,
		"elapsed":              elapsed,
	}).Info("calculated retention summary")

	return &summary, nil
}

// ValidateRetentionPolicy checks if a retention policy would be safe to apply for a given table
// Returns an error if the policy would delete critical data or too much data
// Only considers attached partitions when validating thresholds
func (dbp *DB_PARTITIONS) ValidateRetentionPolicy(tableName string, retentionDays int) error {
	// Minimum retention is 90 days
	if retentionDays < 90 {
		return fmt.Errorf("retention policy too aggressive: minimum 90 days required, got %d", retentionDays)
	}

	// Get summary for attached partitions only to match stats below
	summary, err := dbp.GetRetentionSummary(tableName, retentionDays, true)
	if err != nil {
		return fmt.Errorf("failed to get retention summary: %w", err)
	}

	// Get stats for attached partitions only (detached partitions are not considered)
	stats, err := dbp.GetAttachedPartitionStats(tableName)
	if err != nil {
		return fmt.Errorf("failed to get attached partition stats: %w", err)
	}

	// Check if we'd delete more than 75% of attached partitions
	if stats.TotalPartitions > 0 {
		deletePercentage := float64(summary.PartitionsToRemove) / float64(stats.TotalPartitions) * 100
		if deletePercentage > 75 {
			return fmt.Errorf("retention policy would delete %.1f%% of attached partitions (%d of %d) - exceeds 75%% safety threshold",
				deletePercentage, summary.PartitionsToRemove, stats.TotalPartitions)
		}
	}

	// Check if we'd delete more than 80% of storage from attached partitions
	if stats.TotalSizeBytes > 0 {
		deletePercentage := float64(summary.StorageToReclaim) / float64(stats.TotalSizeBytes) * 100
		if deletePercentage > 80 {
			return fmt.Errorf("retention policy would delete %.1f%% of attached storage (%s of %s) - exceeds 80%% safety threshold",
				deletePercentage, summary.StoragePretty, stats.TotalSizePretty)
		}
	}

	log.WithFields(log.Fields{
		"table":                tableName,
		"retention_days":       retentionDays,
		"partitions_to_remove": summary.PartitionsToRemove,
		"attached_partitions":  stats.TotalPartitions,
		"attached_storage":     stats.TotalSizePretty,
		"storage_to_reclaim":   summary.StoragePretty,
	}).Info("retention policy validated")

	return nil
}

// DropPartition drops a single partition (DESTRUCTIVE - requires write access)
// This is a wrapper around DROP TABLE for safety and logging
func (dbp *DB_PARTITIONS) DropPartition(partitionName string, dryRun bool) error {
	start := time.Now()

	// Extract table name from partition name
	tableName, err := extractTableNameFromPartition(partitionName)
	if err != nil {
		return fmt.Errorf("invalid partition name: %w", err)
	}

	// Validate partition name format for safety
	if !isValidPartitionName(tableName, partitionName) {
		return fmt.Errorf("invalid partition name: %s - must match %s_YYYY_MM_DD or %s_pYYYY_MM_DD", partitionName, tableName, tableName)
	}

	if dryRun {
		log.WithFields(log.Fields{
			"partition": partitionName,
			"table":     tableName,
		}).Info("[DRY RUN] would drop partition")
		return nil
	}

	query := "DROP TABLE IF EXISTS " + pq.QuoteIdentifier(partitionName)
	if _, err := dbp.db.Exec(query); err != nil {
		log.WithError(err).WithFields(log.Fields{
			"partition": partitionName,
			"table":     tableName,
		}).Error("failed to drop partition")
		return err
	}

	elapsed := time.Since(start)
	log.WithFields(log.Fields{
		"partition": partitionName,
		"table":     tableName,
		"elapsed":   elapsed,
	}).Info("dropped partition")

	return nil
}

// DetachPartition detaches a partition from the parent table (safer alternative to DROP)
// The detached table can be archived or dropped later
func (dbp *DB_PARTITIONS) DetachPartition(partitionName string, dryRun bool) error {
	start := time.Now()

	// Extract table name from partition name
	tableName, err := extractTableNameFromPartition(partitionName)
	if err != nil {
		return fmt.Errorf("invalid partition name: %w", err)
	}

	// Validate partition name format for safety
	if !isValidPartitionName(tableName, partitionName) {
		return fmt.Errorf("invalid partition name: %s - must match %s_YYYY_MM_DD or %s_pYYYY_MM_DD", partitionName, tableName, tableName)
	}

	if dryRun {
		log.WithFields(log.Fields{
			"partition": partitionName,
			"table":     tableName,
		}).Info("[DRY RUN] would detach partition")
		return nil
	}

	query := fmt.Sprintf("ALTER TABLE %s DETACH PARTITION %s", pq.QuoteIdentifier(tableName), pq.QuoteIdentifier(partitionName))
	if _, err := dbp.db.Exec(query); err != nil {
		log.WithError(err).WithFields(log.Fields{
			"partition": partitionName,
			"table":     tableName,
		}).Error("failed to detach partition")
		return err
	}

	elapsed := time.Since(start)
	log.WithFields(log.Fields{
		"partition": partitionName,
		"table":     tableName,
		"elapsed":   elapsed,
	}).Info("detached partition")

	return nil
}

// DropOldDetachedPartitions drops detached partitions older than retentionDays (DESTRUCTIVE)
// This removes detached partitions that are no longer needed
// Use this after archiving detached partitions or when you're sure the data is no longer needed
func (dbp *DB_PARTITIONS) DropOldDetachedPartitions(tableName string, retentionDays int, dryRun bool) (int, error) {
	start := time.Now()

	// Get all detached partitions
	detached, err := dbp.ListDetachedPartitions(tableName)
	if err != nil {
		return 0, fmt.Errorf("failed to list detached partitions: %w", err)
	}

	if len(detached) == 0 {
		log.WithField("table", tableName).Info("no detached partitions found")
		return 0, nil
	}

	// Filter by retention period
	cutoffDate := time.Now().AddDate(0, 0, -retentionDays)
	var toRemove []PartitionInfo

	for _, partition := range detached {
		if partition.PartitionDate.Before(cutoffDate) {
			toRemove = append(toRemove, partition)
		}
	}

	if len(toRemove) == 0 {
		log.WithFields(log.Fields{
			"table":          tableName,
			"retention_days": retentionDays,
			"cutoff_date":    cutoffDate.Format("2006-01-02"),
		}).Info("no detached partitions older than retention period")
		return 0, nil
	}

	if dryRun {
		for _, partition := range toRemove {
			if err := dbp.DropPartition(partition.TableName, true); err != nil {
				return 0, fmt.Errorf("failed to dry-run drop partition %s: %w", partition.TableName, err)
			}
		}
		return len(toRemove), nil
	}

	// Drop all old detached partitions in a transaction
	droppedCount := 0
	var totalSize int64

	err = dbp.withTx(func(txp *DB_PARTITIONS) error {
		for _, partition := range toRemove {
			if err := txp.DropPartition(partition.TableName, false); err != nil {
				return fmt.Errorf("failed to drop partition %s: %w", partition.TableName, err)
			}
			droppedCount++
			totalSize += partition.SizeBytes
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	elapsed := time.Since(start)
	log.WithFields(log.Fields{
		"table":             tableName,
		"retention_days":    retentionDays,
		"total_dropped":     droppedCount,
		"storage_reclaimed": fmt.Sprintf("%d bytes", totalSize),
		"elapsed":           elapsed,
	}).Info("completed dropping old detached partitions")

	return droppedCount, nil
}

// ListDetachedPartitions returns partitions that have been detached from the parent table
// Detached partitions are standalone tables that match the naming pattern but are no longer
// part of the partitioned table hierarchy
func (dbp *DB_PARTITIONS) ListDetachedPartitions(tableName string) ([]PartitionInfo, error) {
	start := time.Now()

	// Detect partition format
	usePartmanFormat, err := dbp.DetectPartitionFormat(tableName)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Debug("failed to detect partition format, assuming standard")
		usePartmanFormat = false
	}

	// Build patterns based on detected format
	sqlPattern := GetPartitionSQLPattern(usePartmanFormat)
	tablePattern := getPartitionLikePattern(tableName, usePartmanFormat)

	query := fmt.Sprintf(`
		WITH attached_partitions AS (
			-- Get all currently attached partitions using pg_inherits
			SELECT c.relname AS tablename
			FROM pg_inherits i
			JOIN pg_class c ON i.inhrelid = c.oid
			JOIN pg_class p ON i.inhparent = p.oid
			WHERE p.relname = $1
		)
		SELECT
			tablename,
			'public' as schemaname,
			TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD') AS partition_date,
			(CURRENT_DATE - TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD'))::INT AS age_days,
			pg_total_relation_size('public.'||tablename) AS size_bytes,
			pg_size_pretty(pg_total_relation_size('public.'||tablename)) AS size_pretty,
			COALESCE(n_live_tup, 0) AS row_estimate
		FROM pg_tables
		LEFT JOIN pg_stat_user_tables ON pg_stat_user_tables.relname = pg_tables.tablename
			AND pg_stat_user_tables.schemaname = pg_tables.schemaname
		WHERE pg_tables.schemaname = 'public'
			AND pg_tables.tablename LIKE $2 ESCAPE '\'
			AND pg_tables.tablename NOT IN (SELECT tablename FROM attached_partitions)
		ORDER BY partition_date ASC
	`, sqlPattern, sqlPattern)

	rows, err := dbp.db.Query(query, tableName, tablePattern)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Error("failed to list detached partitions")
		return nil, err
	}
	partitions, err := scanRows(rows, scanPartitionInfo)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Error("failed to list detached partitions")
		return nil, err
	}

	elapsed := time.Since(start)
	log.WithFields(log.Fields{
		"table":   tableName,
		"count":   len(partitions),
		"elapsed": elapsed,
	}).Info("listed detached partitions")

	return partitions, nil
}

// ListAttachedPartitions returns partitions that are currently attached to the parent table
// These are partitions that are part of the active partitioned table hierarchy
func (dbp *DB_PARTITIONS) ListAttachedPartitions(tableName string) ([]PartitionInfo, error) {
	start := time.Now()

	// Detect partition format
	usePartmanFormat, err := dbp.DetectPartitionFormat(tableName)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Debug("failed to detect partition format, assuming standard")
		usePartmanFormat = false
	}

	// Build pattern based on detected format
	sqlPattern := GetPartitionSQLPattern(usePartmanFormat)

	query := fmt.Sprintf(`
		WITH attached_partitions AS (
			-- Get all currently attached partitions using pg_inherits
			SELECT c.relname AS tablename
			FROM pg_inherits i
			JOIN pg_class c ON i.inhrelid = c.oid
			JOIN pg_class p ON i.inhparent = p.oid
			WHERE p.relname = $1
		)
		SELECT
			tablename,
			'public' as schemaname,
			TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD') AS partition_date,
			(CURRENT_DATE - TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD'))::INT AS age_days,
			pg_total_relation_size('public.'||tablename) AS size_bytes,
			pg_size_pretty(pg_total_relation_size('public.'||tablename)) AS size_pretty,
			COALESCE(n_live_tup, 0) AS row_estimate
		FROM pg_tables
		LEFT JOIN pg_stat_user_tables ON pg_stat_user_tables.relname = pg_tables.tablename
			AND pg_stat_user_tables.schemaname = pg_tables.schemaname
		WHERE pg_tables.schemaname = 'public'
			AND pg_tables.tablename IN (SELECT tablename FROM attached_partitions)
		ORDER BY partition_date ASC
	`, sqlPattern, sqlPattern)

	rows, err := dbp.db.Query(query, tableName)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Error("failed to list attached partitions")
		return nil, err
	}
	partitions, err := scanRows(rows, scanPartitionInfo)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Error("failed to list attached partitions")
		return nil, err
	}

	elapsed := time.Since(start)
	log.WithFields(log.Fields{
		"table":   tableName,
		"count":   len(partitions),
		"elapsed": elapsed,
	}).Info("listed attached partitions")

	return partitions, nil
}

// GetAttachedPartitionStats returns statistics about attached partitions for a given table
func (dbp *DB_PARTITIONS) GetAttachedPartitionStats(tableName string) (*PartitionStats, error) {
	start := time.Now()

	// Detect partition format
	usePartmanFormat, err := dbp.DetectPartitionFormat(tableName)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Debug("failed to detect partition format, assuming standard")
		usePartmanFormat = false
	}

	// Build pattern based on detected format
	sqlPattern := GetPartitionSQLPattern(usePartmanFormat)

	query := fmt.Sprintf(`
		WITH attached_partitions AS (
			SELECT c.relname AS tablename
			FROM pg_inherits i
			JOIN pg_class c ON i.inhrelid = c.oid
			JOIN pg_class p ON i.inhparent = p.oid
			WHERE p.relname = $1
		),
		attached_info AS (
			SELECT
				tablename,
				TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD') AS partition_date,
				pg_total_relation_size('public.'||tablename) AS size_bytes
			FROM pg_tables
			WHERE schemaname = 'public'
				AND tablename IN (SELECT tablename FROM attached_partitions)
		)
		SELECT
			COALESCE(COUNT(*), 0)::INT AS total_partitions,
			COALESCE(SUM(size_bytes), 0)::BIGINT AS total_size_bytes,
			pg_size_pretty(COALESCE(SUM(size_bytes), 0)) AS total_size_pretty,
			MIN(partition_date) AS oldest_date,
			MAX(partition_date) AS newest_date,
			COALESCE(AVG(size_bytes), 0)::BIGINT AS avg_size_bytes,
			pg_size_pretty(COALESCE(AVG(size_bytes), 0)::BIGINT) AS avg_size_pretty
		FROM attached_info
	`, sqlPattern)

	stats, err := scanPartitionStats(dbp.db.QueryRow(query, tableName))
	if err != nil {
		log.WithError(err).WithField("table", tableName).Error("failed to get attached partition statistics")
		return nil, err
	}

	elapsed := time.Since(start)
	log.WithFields(log.Fields{
		"table":            tableName,
		"total_partitions": stats.TotalPartitions,
		"total_size":       stats.TotalSizePretty,
		"elapsed":          elapsed,
	}).Info("retrieved attached partition statistics")

	return &stats, nil
}

// CreateMissingPartitions creates partitions for a date range if they don't already exist
// Supports both standard format (tablename_YYYY_MM_DD) and pg_partman format (tablename_pYYYY_MM_DD)
// Each partition covers a 24-hour period from midnight to midnight
func (dbp *DB_PARTITIONS) CreateMissingPartitions(tableName string, startDate, endDate time.Time, usePartmanFormat bool, dryRun bool) (int, error) {
	start := time.Now()

	// Validate date range
	if endDate.Before(startDate) {
		return 0, fmt.Errorf("end date (%s) cannot be before start date (%s)",
			endDate.Format("2006-01-02"), startDate.Format("2006-01-02"))
	}

	// Validate that partition names will fit within PostgreSQL's identifier limit
	maxName := len(tableName) + dateSuffixLen(usePartmanFormat)
	if maxName > maxPartitionNameLen {
		return 0, fmt.Errorf(
			"partition names for table %q would be %d characters (limit %d); shorten the table name",
			tableName, maxName, maxPartitionNameLen)
	}

	// Get list of all existing partitions (attached + detached)
	existingPartitions, err := dbp.ListTablePartitions(tableName)
	if err != nil {
		return 0, fmt.Errorf("failed to list existing partitions: %w", err)
	}

	// Create a map of existing partition dates for quick lookup
	existingDates := make(map[string]bool)
	for _, p := range existingPartitions {
		dateStr := p.PartitionDate.Format("2006_01_02")
		existingDates[dateStr] = true
	}

	// Generate list of partitions to create and detached partitions to reattach
	var partitionsToCreate []time.Time
	var partitionsToReattach []string
	currentDate := startDate
	for !currentDate.After(endDate) {
		dateStr := currentDate.Format("2006_01_02")
		if !existingDates[dateStr] {
			partitionsToCreate = append(partitionsToCreate, currentDate)
		} else {
			// Partition exists — verify it is attached
			partitionName := buildPartitionName(tableName, currentDate, usePartmanFormat)
			attached, err := dbp.IsPartitionAttached(partitionName)
			if err != nil {
				return 0, fmt.Errorf("failed to check if partition %s is attached: %w", partitionName, err)
			}
			if !attached {
				partitionsToReattach = append(partitionsToReattach, partitionName)
			}
		}
		currentDate = currentDate.AddDate(0, 0, 1) // Move to next day
	}

	if len(partitionsToCreate) == 0 && len(partitionsToReattach) == 0 {
		log.WithFields(log.Fields{
			"table":      tableName,
			"start_date": startDate.Format("2006-01-02"),
			"end_date":   endDate.Format("2006-01-02"),
		}).Info("no missing partitions to create")
		return 0, nil
	}

	if dryRun {
		for _, partitionDate := range partitionsToCreate {
			partitionName := buildPartitionName(tableName, partitionDate, usePartmanFormat)
			log.WithFields(log.Fields{
				"partition": partitionName,
				"table":     tableName,
			}).Info("[DRY RUN] would create partition")
		}
		for _, partitionName := range partitionsToReattach {
			log.WithFields(log.Fields{
				"partition": partitionName,
				"table":     tableName,
			}).Info("[DRY RUN] would reattach partition")
		}
		return len(partitionsToCreate), nil
	}

	createdCount := 0
	err = dbp.withTx(func(txp *DB_PARTITIONS) error {
		for _, partitionName := range partitionsToReattach {
			if err := txp.AttachPartition(tableName, partitionName, usePartmanFormat, false); err != nil {
				return fmt.Errorf("failed to reattach partition %s: %w", partitionName, err)
			}
		}

		for _, partitionDate := range partitionsToCreate {
			partitionName := buildPartitionName(tableName, partitionDate, usePartmanFormat)

			createTableQuery := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (LIKE %s INCLUDING ALL)", pq.QuoteIdentifier(partitionName), pq.QuoteIdentifier(tableName))
			if _, err := txp.db.Exec(createTableQuery); err != nil {
				return fmt.Errorf("failed to create partition table %s: %w", partitionName, err)
			}

			if err := txp.AttachPartition(tableName, partitionName, usePartmanFormat, false); err != nil {
				return fmt.Errorf("failed to attach partition %s: %w", partitionName, err)
			}

			createdCount++
		}

		return nil
	})
	if err != nil {
		return 0, err
	}

	elapsed := time.Since(start)
	log.WithFields(log.Fields{
		"table":      tableName,
		"start_date": startDate.Format("2006-01-02"),
		"end_date":   endDate.Format("2006-01-02"),
		"created":    createdCount,
		"reattached": len(partitionsToReattach),
		"dry_run":    dryRun,
		"elapsed":    elapsed,
	}).Info("completed creating missing partitions")

	return createdCount, nil
}

// CreatePartitionedTable creates a new partitioned table based on a model struct
// If the table already exists, it returns without error
//
//nolint:gocyclo

// UpdatePartitionedTable updates an existing partitioned table schema based on a model.
//
//nolint:gocyclo

// getPartitionColumns retrieves the partition key columns for a table
func (dbp *DB_PARTITIONS) GetPartitionColumns(tableName string) ([]string, error) {
	query := `
		SELECT a.attname
		FROM pg_class c
		JOIN pg_partitioned_table pt ON pt.partrelid = c.oid
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY(pt.partattrs)
		WHERE c.relname = $1
			AND c.relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = 'public')
		ORDER BY array_position(pt.partattrs, a.attnum)
	`

	rows, err := dbp.db.Query(query, tableName)
	if err != nil {
		return nil, err
	}
	columns, err := scanRows(rows, func(r *sql.Rows) (string, error) {
		var col string
		return col, r.Scan(&col)
	})
	if err != nil {
		return nil, err
	}

	return columns, nil
}

// GetDetachedPartitionStats returns statistics about detached partitions for a given table
func (dbp *DB_PARTITIONS) GetDetachedPartitionStats(tableName string) (*PartitionStats, error) {
	start := time.Now()

	// Detect partition format
	usePartmanFormat, err := dbp.DetectPartitionFormat(tableName)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Debug("failed to detect partition format, assuming standard")
		usePartmanFormat = false
	}

	// Build patterns based on detected format
	sqlPattern := GetPartitionSQLPattern(usePartmanFormat)
	tablePattern := getPartitionLikePattern(tableName, usePartmanFormat)

	query := fmt.Sprintf(`
		WITH attached_partitions AS (
			SELECT c.relname AS tablename
			FROM pg_inherits i
			JOIN pg_class c ON i.inhrelid = c.oid
			JOIN pg_class p ON i.inhparent = p.oid
			WHERE p.relname = $1
		),
		detached_info AS (
			SELECT
				tablename,
				TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD') AS partition_date,
				pg_total_relation_size('public.'||tablename) AS size_bytes
			FROM pg_tables
			WHERE schemaname = 'public'
				AND tablename LIKE $2 ESCAPE '\'
				AND tablename NOT IN (SELECT tablename FROM attached_partitions)
		)
		SELECT
			COUNT(*)::INT AS total_partitions,
			COALESCE(SUM(size_bytes), 0)::BIGINT AS total_size_bytes,
			COALESCE(pg_size_pretty(SUM(size_bytes)), '0 bytes') AS total_size_pretty,
			MIN(partition_date) AS oldest_date,
			MAX(partition_date) AS newest_date,
			COALESCE(AVG(size_bytes), 0)::BIGINT AS avg_size_bytes,
			COALESCE(pg_size_pretty(AVG(size_bytes)::BIGINT), '0 bytes') AS avg_size_pretty
		FROM detached_info
	`, sqlPattern)

	stats, err := scanPartitionStats(dbp.db.QueryRow(query, tableName, tablePattern))
	if err != nil {
		log.WithError(err).WithField("table", tableName).Error("failed to get detached partition statistics")
		return nil, err
	}

	elapsed := time.Since(start)
	log.WithFields(log.Fields{
		"table":            tableName,
		"total_partitions": stats.TotalPartitions,
		"total_size":       stats.TotalSizePretty,
		"elapsed":          elapsed,
	}).Info("retrieved detached partition statistics")

	return &stats, nil
}

// AttachPartition attaches a partition to the parent table with the appropriate date range
// Supports both standard format (tableName_YYYY_MM_DD) and pg_partman format (tableName_pYYYY_MM_DD)
func (dbp *DB_PARTITIONS) AttachPartition(tableName, partitionName string, usePartmanFormat bool, dryRun bool) error {
	start := time.Now()

	// Validate partition name format for safety
	if !isValidPartitionName(tableName, partitionName) {
		return fmt.Errorf("invalid partition name: %s - must match %s_YYYY_MM_DD or %s_pYYYY_MM_DD", partitionName, tableName, tableName)
	}

	// Extract date from partition name
	prefix := tableName + "_"
	dateStr := partitionName[len(prefix):]

	// Handle pg_partman format (_pYYYY_MM_DD)
	if usePartmanFormat && len(dateStr) > 0 && dateStr[0] == 'p' {
		dateStr = dateStr[1:] // Strip the 'p' prefix
	}

	partitionDate, err := time.Parse("2006_01_02", dateStr)
	if err != nil {
		return fmt.Errorf("invalid partition date format: %w", err)
	}

	// Calculate date range for the partition
	rangeStart := partitionDate.Format("2006-01-02")
	rangeEnd := partitionDate.AddDate(0, 0, 1).Format("2006-01-02")

	if dryRun {
		log.WithFields(log.Fields{
			"partition":   partitionName,
			"table":       tableName,
			"range_start": rangeStart,
			"range_end":   rangeEnd,
		}).Info("[DRY RUN] would attach partition")
		return nil
	}

	// Attach the partition with FOR VALUES clause
	query := fmt.Sprintf(
		"ALTER TABLE %s ATTACH PARTITION %s FOR VALUES FROM ('%s') TO ('%s')",
		pq.QuoteIdentifier(tableName),
		pq.QuoteIdentifier(partitionName),
		rangeStart,
		rangeEnd,
	)

	if _, err := dbp.db.Exec(query); err != nil {
		log.WithError(err).WithFields(log.Fields{
			"partition": partitionName,
			"table":     tableName,
		}).Error("failed to attach partition")
		return err
	}

	elapsed := time.Since(start)
	log.WithFields(log.Fields{
		"partition": partitionName,
		"table":     tableName,
		"elapsed":   elapsed,
	}).Info("attached partition")

	return nil
}

// IsPartitionAttached checks if a partition is currently attached to the parent table
func (dbp *DB_PARTITIONS) IsPartitionAttached(partitionName string) (bool, error) {
	start := time.Now()

	// Extract table name from partition name
	tableName, err := extractTableNameFromPartition(partitionName)
	if err != nil {
		return false, fmt.Errorf("invalid partition name: %w", err)
	}

	// Validate partition name format for safety
	if !isValidPartitionName(tableName, partitionName) {
		return false, fmt.Errorf("invalid partition name: %s", partitionName)
	}

	var isAttached bool
	query := `
		SELECT EXISTS(
			SELECT 1
			FROM pg_inherits i
			JOIN pg_class c ON i.inhrelid = c.oid
			JOIN pg_class p ON i.inhparent = p.oid
			WHERE p.relname = $1
				AND c.relname = $2
		) AS is_attached
	`

	err = dbp.db.QueryRow(query, tableName, partitionName).Scan(&isAttached)
	if err != nil {
		log.WithError(err).WithFields(log.Fields{
			"partition": partitionName,
			"table":     tableName,
		}).Error("failed to check partition status")
		return false, err
	}

	elapsed := time.Since(start)
	log.WithFields(log.Fields{
		"partition":   partitionName,
		"table":       tableName,
		"is_attached": isAttached,
		"elapsed":     elapsed,
	}).Debug("checked partition attachment status")

	return isAttached, nil
}

// DetachOldPartitions detaches all partitions older than the retention period for a given table
// This is safer than dropping as partitions can be reattached if needed
func (dbp *DB_PARTITIONS) DetachOldPartitions(tableName string, retentionDays int, dryRun bool) (int, error) {
	start := time.Now()

	// Validate retention policy first
	if err := dbp.ValidateRetentionPolicy(tableName, retentionDays); err != nil {
		return 0, fmt.Errorf("retention policy validation failed: %w", err)
	}

	// Get only attached partitions for removal (can only detach what's attached)
	partitions, err := dbp.GetPartitionsForRemoval(tableName, retentionDays, true)
	if err != nil {
		return 0, fmt.Errorf("failed to get partitions for removal: %w", err)
	}

	if len(partitions) == 0 {
		log.WithField("table", tableName).Info("no partitions to detach")
		return 0, nil
	}

	if dryRun {
		for _, partition := range partitions {
			if err := dbp.DetachPartition(partition.TableName, true); err != nil {
				return 0, fmt.Errorf("failed to dry-run detach partition %s: %w", partition.TableName, err)
			}
		}
		return len(partitions), nil
	}

	// Detach all old partitions in a transaction
	detachedCount := 0
	var totalSize int64

	err = dbp.withTx(func(txp *DB_PARTITIONS) error {
		for _, partition := range partitions {
			if err := txp.DetachPartition(partition.TableName, false); err != nil {
				return fmt.Errorf("failed to detach partition %s: %w", partition.TableName, err)
			}
			detachedCount++
			totalSize += partition.SizeBytes
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	elapsed := time.Since(start)
	log.WithFields(log.Fields{
		"table":            tableName,
		"retention_days":   retentionDays,
		"total_detached":   detachedCount,
		"storage_affected": fmt.Sprintf("%d bytes", totalSize),
		"elapsed":          elapsed,
	}).Info("completed detaching old partitions")

	return detachedCount, nil
}

// buildPartitionName generates a partition name based on format preference
func buildPartitionName(tableName string, date time.Time, usePartmanFormat bool) string {
	dateStr := date.Format("2006_01_02")
	if usePartmanFormat {
		return fmt.Sprintf("%s_p%s", tableName, dateStr)
	}
	return fmt.Sprintf("%s_%s", tableName, dateStr)
}

// DetectPartitionFormat examines existing partitions to determine naming format
// Returns true if pg_partman format (_pYYYY_MM_DD), false for standard format
// Returns error if table not found or has no partitions
func (dbp *DB_PARTITIONS) DetectPartitionFormat(tableName string) (bool, error) {
	var partitionName string
	query := `
		SELECT c.relname
		FROM pg_inherits i
		JOIN pg_class c ON i.inhrelid = c.oid
		JOIN pg_class p ON i.inhparent = p.oid
		WHERE p.relname = $1
		LIMIT 1
	`

	err := dbp.db.QueryRow(query, tableName).Scan(&partitionName)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to detect partition format: %w", err)
	}

	return strings.Contains(partitionName, "_p20"), nil
}

// GetPartitionSQLPattern returns SQL regex pattern for matching partitions
func GetPartitionSQLPattern(usePartmanFormat bool) string {
	if usePartmanFormat {
		return "_p(\\d{4}_\\d{2}_\\d{2})$"
	}
	return "_(\\d{4}_\\d{2}_\\d{2})$"
}

// getPartitionLikePattern returns LIKE pattern for partition matching
func getPartitionLikePattern(tableName string, usePartmanFormat bool) string {
	if usePartmanFormat {
		return escapeForLike(tableName) + "\\_p20%"
	}
	return escapeForLike(tableName) + "\\_20%"
}

// extractTableNameFromPartition extracts the table name from a partition name
// Supports both standard format {tablename}_YYYY_MM_DD and pg_partman format {tablename}_pYYYY_MM_DD
func extractTableNameFromPartition(partitionName string) (string, error) {
	// Minimum length check (shortest valid: x_2000_01_01 = 12 chars)
	if len(partitionName) < 12 {
		return "", fmt.Errorf("partition name too short: %s", partitionName)
	}

	// Check for pg_partman format (_pYYYY_MM_DD) - 12 char suffix
	if len(partitionName) >= 13 && partitionName[len(partitionName)-12] == '_' && partitionName[len(partitionName)-11] == 'p' {
		dateStr := partitionName[len(partitionName)-10:]
		if _, err := time.Parse("2006_01_02", dateStr); err == nil {
			// Valid pg_partman format
			return partitionName[:len(partitionName)-12], nil
		}
	}

	// Check for standard format (_YYYY_MM_DD) - 11 char suffix
	if len(partitionName) >= 11 {
		dateStr := partitionName[len(partitionName)-10:]
		if _, err := time.Parse("2006_01_02", dateStr); err == nil {
			// Valid standard format
			return partitionName[:len(partitionName)-11], nil
		}
	}

	return "", fmt.Errorf("invalid partition name format: %s (expected tablename_YYYY_MM_DD or tablename_pYYYY_MM_DD)", partitionName)
}

// isValidPartitionName validates that a partition name matches the expected format for a given table
// Supports both standard format (tablename_YYYY_MM_DD) and pg_partman format (tablename_pYYYY_MM_DD)
// This is a safety check to prevent SQL injection and accidental drops
func isValidPartitionName(tableName, partitionName string) bool {
	expectedPrefix := tableName + "_"

	if !strings.HasPrefix(partitionName, expectedPrefix) {
		return false
	}

	// Check pg_partman format: tablename_pYYYY_MM_DD (length = prefix + 1 + 10)
	expectedLenPartman := len(expectedPrefix) + 11
	if len(partitionName) == expectedLenPartman && partitionName[len(expectedPrefix)] == 'p' {
		// Must start with p20xx (year 2000-2099)
		if len(partitionName) >= len(expectedPrefix)+3 && partitionName[len(expectedPrefix)+1:len(expectedPrefix)+3] == "20" {
			dateStr := partitionName[len(expectedPrefix)+1:] // YYYY_MM_DD format
			_, err := time.Parse("2006_01_02", dateStr)
			return err == nil
		}
	}

	// Check standard format: tablename_YYYY_MM_DD (length = prefix + 10)
	expectedLenStandard := len(expectedPrefix) + 10
	if len(partitionName) == expectedLenStandard {
		// Must start with 20xx (year 2000-2099)
		if len(partitionName) >= len(expectedPrefix)+2 && partitionName[len(expectedPrefix):len(expectedPrefix)+2] == "20" {
			dateStr := partitionName[len(expectedPrefix):] // YYYY_MM_DD format
			_, err := time.Parse("2006_01_02", dateStr)
			return err == nil
		}
	}

	return false
}
