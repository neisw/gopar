package gopar

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
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
	TableName     string    `gorm:"column:tablename"`
	SchemaName    string    `gorm:"column:schemaname"`
	PartitionDate time.Time `gorm:"column:partition_date"`
	Age           int       `gorm:"column:age_days"`
	SizeBytes     int64     `gorm:"column:size_bytes"`
	SizePretty    string    `gorm:"column:size_pretty"`
	RowEstimate   int64     `gorm:"column:row_estimate"`
}

// PartitionedTableInfo holds metadata about a partitioned parent table
type PartitionedTableInfo struct {
	TableName         string `gorm:"column:tablename"`
	SchemaName        string `gorm:"column:schemaname"`
	PartitionCount    int    `gorm:"column:partition_count"`
	PartitionStrategy string `gorm:"column:partition_strategy"`
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
func (dbc *DB) ListPartitionedTables() ([]PartitionedTableInfo, error) {
	start := time.Now()
	var tables []PartitionedTableInfo

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

	result := dbc.DB.Raw(query).Scan(&tables)
	if result.Error != nil {
		log.WithError(result.Error).Error("failed to list partitioned tables")
		return nil, result.Error
	}

	elapsed := time.Since(start)
	log.WithFields(log.Fields{
		"count":   len(tables),
		"elapsed": elapsed,
	}).Info("listed partitioned tables")

	return tables, nil
}

// ListTablePartitions returns all partitions for a given table
func (dbc *DB) ListTablePartitions(tableName string) ([]PartitionInfo, error) {
	start := time.Now()
	var partitions []PartitionInfo

	// Detect partition format
	usePartmanFormat, err := dbc.detectPartitionFormat(tableName)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Debug("failed to detect partition format, assuming standard")
		usePartmanFormat = false
	}

	// Build SQL pattern based on detected format
	sqlPattern := getPartitionSQLPattern(usePartmanFormat)
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
			AND pg_tables.tablename LIKE @table_pattern ESCAPE '\'
		ORDER BY partition_date ASC
	`, sqlPattern, sqlPattern)

	result := dbc.DB.Raw(query, sql.Named("table_pattern", tablePattern)).Scan(&partitions)
	if result.Error != nil {
		log.WithError(result.Error).WithField("table", tableName).Error("failed to list table partitions")
		return nil, result.Error
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
func (dbc *DB) GetPartitionStats(tableName string) (*PartitionStats, error) {
	start := time.Now()
	var stats PartitionStats

	// Detect partition format
	usePartmanFormat, err := dbc.detectPartitionFormat(tableName)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Debug("failed to detect partition format, assuming standard")
		usePartmanFormat = false
	}

	// Build patterns based on detected format
	sqlPattern := getPartitionSQLPattern(usePartmanFormat)
	tablePattern := getPartitionLikePattern(tableName, usePartmanFormat)

	query := fmt.Sprintf(`
		WITH partition_info AS (
			SELECT
				tablename,
				TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD') AS partition_date,
				pg_total_relation_size('public.'||tablename) AS size_bytes
			FROM pg_tables
			WHERE schemaname = 'public'
				AND tablename LIKE @table_pattern ESCAPE '\'
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

	result := dbc.DB.Raw(query, sql.Named("table_pattern", tablePattern)).Scan(&stats)
	if result.Error != nil {
		log.WithError(result.Error).WithField("table", tableName).Error("failed to get partition statistics")
		return nil, result.Error
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
func (dbc *DB) GetPartitionsForRemoval(tableName string, retentionDays int, attachedOnly bool) ([]PartitionInfo, error) {
	start := time.Now()
	var partitions []PartitionInfo

	cutoffDate := time.Now().AddDate(0, 0, -retentionDays)

	// Detect partition format
	usePartmanFormat, err := dbc.detectPartitionFormat(tableName)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Debug("failed to detect partition format, assuming standard")
		usePartmanFormat = false
	}

	// Build patterns based on detected format
	sqlPattern := getPartitionSQLPattern(usePartmanFormat)
	tablePattern := getPartitionLikePattern(tableName, usePartmanFormat)

	var query string
	if attachedOnly {
		// Only return attached partitions
		query = fmt.Sprintf(`
			WITH attached_partitions AS (
				SELECT c.relname AS tablename
				FROM pg_inherits i
				JOIN pg_class c ON i.inhrelid = c.oid
				JOIN pg_class p ON i.inhparent = p.oid
				WHERE p.relname = @table_name
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
				AND pg_tables.tablename LIKE @table_pattern ESCAPE '\'
				AND pg_tables.tablename IN (SELECT tablename FROM attached_partitions)
				AND TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD') < @cutoff_date
			ORDER BY partition_date ASC
		`, sqlPattern, sqlPattern, sqlPattern)
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
				AND pg_tables.tablename LIKE @table_pattern ESCAPE '\'
				AND TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD') < @cutoff_date
			ORDER BY partition_date ASC
		`, sqlPattern, sqlPattern, sqlPattern)
	}
	result := dbc.DB.Raw(query,
		sql.Named("table_name", tableName),
		sql.Named("table_pattern", tablePattern),
		sql.Named("cutoff_date", cutoffDate)).Scan(&partitions)
	if result.Error != nil {
		log.WithError(result.Error).WithField("table", tableName).Error("failed to get partitions for removal")
		return nil, result.Error
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
func (dbc *DB) GetRetentionSummary(tableName string, retentionDays int, attachedOnly bool) (*RetentionSummary, error) {
	start := time.Now()

	cutoffDate := time.Now().AddDate(0, 0, -retentionDays)

	var summary RetentionSummary
	summary.RetentionDays = retentionDays
	summary.CutoffDate = cutoffDate

	// Detect partition format
	usePartmanFormat, err := dbc.detectPartitionFormat(tableName)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Debug("failed to detect partition format, assuming standard")
		usePartmanFormat = false
	}

	// Build patterns based on detected format
	sqlPattern := getPartitionSQLPattern(usePartmanFormat)
	tablePattern := getPartitionLikePattern(tableName, usePartmanFormat)

	var query string
	if attachedOnly {
		// Only consider attached partitions
		query = fmt.Sprintf(`
			WITH attached_partitions AS (
				SELECT c.relname AS tablename
				FROM pg_inherits i
				JOIN pg_class c ON i.inhrelid = c.oid
				JOIN pg_class p ON i.inhparent = p.oid
				WHERE p.relname = @table_name
			)
			SELECT
				COUNT(*)::INT AS partitions_to_remove,
				COALESCE(SUM(pg_total_relation_size('public.'||tablename)), 0)::BIGINT AS storage_to_reclaim,
				COALESCE(pg_size_pretty(SUM(pg_total_relation_size('public.'||tablename))), '0 bytes') AS storage_pretty,
				MIN(tablename) AS oldest_partition,
				MAX(tablename) AS newest_partition
			FROM pg_tables
			WHERE schemaname = 'public'
				AND tablename LIKE @table_pattern ESCAPE '\'
				AND tablename IN (SELECT tablename FROM attached_partitions)
				AND TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD') < @cutoff_date
		`, sqlPattern)
	} else {
		// Consider all partitions (attached + detached)
		query = fmt.Sprintf(`
			SELECT
				COUNT(*)::INT AS partitions_to_remove,
				COALESCE(SUM(pg_total_relation_size('public.'||tablename)), 0)::BIGINT AS storage_to_reclaim,
				COALESCE(pg_size_pretty(SUM(pg_total_relation_size('public.'||tablename))), '0 bytes') AS storage_pretty,
				MIN(tablename) AS oldest_partition,
				MAX(tablename) AS newest_partition
			FROM pg_tables
			WHERE schemaname = 'public'
				AND tablename LIKE @table_pattern ESCAPE '\'
				AND TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD') < @cutoff_date
		`, sqlPattern)
	}
	result := dbc.DB.Raw(query,
		sql.Named("table_name", tableName),
		sql.Named("table_pattern", tablePattern),
		sql.Named("cutoff_date", cutoffDate)).Scan(&summary)
	if result.Error != nil {
		log.WithError(result.Error).WithField("table", tableName).Error("failed to get retention summary")
		return nil, result.Error
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
func (dbc *DB) ValidateRetentionPolicy(tableName string, retentionDays int) error {
	// Minimum retention is 90 days
	if retentionDays < 90 {
		return fmt.Errorf("retention policy too aggressive: minimum 90 days required, got %d", retentionDays)
	}

	// Get summary for attached partitions only to match stats below
	summary, err := dbc.GetRetentionSummary(tableName, retentionDays, true)
	if err != nil {
		return fmt.Errorf("failed to get retention summary: %w", err)
	}

	// Get stats for attached partitions only (detached partitions are not considered)
	stats, err := dbc.GetAttachedPartitionStats(tableName)
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
func (dbc *DB) DropPartition(partitionName string, dryRun bool) error {
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
	result := dbc.DB.Exec(query)
	if result.Error != nil {
		log.WithError(result.Error).WithFields(log.Fields{
			"partition": partitionName,
			"table":     tableName,
		}).Error("failed to drop partition")
		return result.Error
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
func (dbc *DB) DetachPartition(partitionName string, dryRun bool) error {
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
	result := dbc.DB.Exec(query)
	if result.Error != nil {
		log.WithError(result.Error).WithFields(log.Fields{
			"partition": partitionName,
			"table":     tableName,
		}).Error("failed to detach partition")
		return result.Error
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
func (dbc *DB) DropOldDetachedPartitions(tableName string, retentionDays int, dryRun bool) (int, error) {
	start := time.Now()

	// Get all detached partitions
	detached, err := dbc.ListDetachedPartitions(tableName)
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
			if err := dbc.DropPartition(partition.TableName, true); err != nil {
				return 0, fmt.Errorf("failed to dry-run drop partition %s: %w", partition.TableName, err)
			}
		}
		return len(toRemove), nil
	}

	// Drop all old detached partitions in a transaction
	droppedCount := 0
	var totalSize int64

	err = dbc.DB.Transaction(func(tx *gorm.DB) error {
		txDBC := &DB{DB: tx}
		for _, partition := range toRemove {
			if err := txDBC.DropPartition(partition.TableName, false); err != nil {
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
func (dbc *DB) ListDetachedPartitions(tableName string) ([]PartitionInfo, error) {
	start := time.Now()
	var partitions []PartitionInfo

	// Detect partition format
	usePartmanFormat, err := dbc.detectPartitionFormat(tableName)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Debug("failed to detect partition format, assuming standard")
		usePartmanFormat = false
	}

	// Build patterns based on detected format
	sqlPattern := getPartitionSQLPattern(usePartmanFormat)
	tablePattern := getPartitionLikePattern(tableName, usePartmanFormat)

	query := fmt.Sprintf(`
		WITH attached_partitions AS (
			-- Get all currently attached partitions using pg_inherits
			SELECT c.relname AS tablename
			FROM pg_inherits i
			JOIN pg_class c ON i.inhrelid = c.oid
			JOIN pg_class p ON i.inhparent = p.oid
			WHERE p.relname = @table_name
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
			AND pg_tables.tablename LIKE @table_pattern ESCAPE '\'
			AND pg_tables.tablename NOT IN (SELECT tablename FROM attached_partitions)
		ORDER BY partition_date ASC
	`, sqlPattern, sqlPattern)
	result := dbc.DB.Raw(query,
		sql.Named("table_name", tableName),
		sql.Named("table_pattern", tablePattern)).Scan(&partitions)
	if result.Error != nil {
		log.WithError(result.Error).WithField("table", tableName).Error("failed to list detached partitions")
		return nil, result.Error
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
func (dbc *DB) ListAttachedPartitions(tableName string) ([]PartitionInfo, error) {
	start := time.Now()
	var partitions []PartitionInfo

	// Detect partition format
	usePartmanFormat, err := dbc.detectPartitionFormat(tableName)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Debug("failed to detect partition format, assuming standard")
		usePartmanFormat = false
	}

	// Build pattern based on detected format
	sqlPattern := getPartitionSQLPattern(usePartmanFormat)

	query := fmt.Sprintf(`
		WITH attached_partitions AS (
			-- Get all currently attached partitions using pg_inherits
			SELECT c.relname AS tablename
			FROM pg_inherits i
			JOIN pg_class c ON i.inhrelid = c.oid
			JOIN pg_class p ON i.inhparent = p.oid
			WHERE p.relname = @table_name
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

	result := dbc.DB.Raw(query, sql.Named("table_name", tableName)).Scan(&partitions)
	if result.Error != nil {
		log.WithError(result.Error).WithField("table", tableName).Error("failed to list attached partitions")
		return nil, result.Error
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
func (dbc *DB) GetAttachedPartitionStats(tableName string) (*PartitionStats, error) {
	start := time.Now()
	var stats PartitionStats

	// Detect partition format
	usePartmanFormat, err := dbc.detectPartitionFormat(tableName)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Debug("failed to detect partition format, assuming standard")
		usePartmanFormat = false
	}

	// Build pattern based on detected format
	sqlPattern := getPartitionSQLPattern(usePartmanFormat)

	query := fmt.Sprintf(`
		WITH attached_partitions AS (
			SELECT c.relname AS tablename
			FROM pg_inherits i
			JOIN pg_class c ON i.inhrelid = c.oid
			JOIN pg_class p ON i.inhparent = p.oid
			WHERE p.relname = @table_name
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

	result := dbc.DB.Raw(query, sql.Named("table_name", tableName)).Scan(&stats)
	if result.Error != nil {
		log.WithError(result.Error).WithField("table", tableName).Error("failed to get attached partition statistics")
		return nil, result.Error
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
//
// Workflow:
//  1. Lists all existing partitions (both attached and detached)
//  2. Generates list of missing dates in the specified range
//  3. For each missing date: creates table and attaches it as partition
//  4. Skips dates that already have partitions (attached or detached)
//
// Parameters:
//   - tableName: Name of the partitioned parent table
//   - startDate: Start of date range (inclusive)
//   - endDate: End of date range (inclusive)
//   - usePartmanFormat: If true, uses pg_partman naming format (tablename_pYYYY_MM_DD)
//   - dryRun: If true, logs what would be created without executing
//
// Returns: Count of partitions created (or would be created in dry-run mode)
func (dbc *DB) CreateMissingPartitions(tableName string, startDate, endDate time.Time, usePartmanFormat bool, dryRun bool) (int, error) {
	start := time.Now()

	// Validate date range
	if endDate.Before(startDate) {
		return 0, fmt.Errorf("end date (%s) cannot be before start date (%s)",
			endDate.Format("2006-01-02"), startDate.Format("2006-01-02"))
	}

	// Get list of all existing partitions (attached + detached)
	existingPartitions, err := dbc.ListTablePartitions(tableName)
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
			attached, err := dbc.IsPartitionAttached(partitionName)
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
	err = dbc.DB.Transaction(func(tx *gorm.DB) error {
		txDBC := &DB{DB: tx}

		for _, partitionName := range partitionsToReattach {
			if err := txDBC.AttachPartition(tableName, partitionName, usePartmanFormat, false); err != nil {
				return fmt.Errorf("failed to reattach partition %s: %w", partitionName, err)
			}
		}

		for _, partitionDate := range partitionsToCreate {
			partitionName := buildPartitionName(tableName, partitionDate, usePartmanFormat)

			createTableQuery := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (LIKE %s INCLUDING ALL)", pq.QuoteIdentifier(partitionName), pq.QuoteIdentifier(tableName))
			if result := tx.Exec(createTableQuery); result.Error != nil {
				return fmt.Errorf("failed to create partition table %s: %w", partitionName, result.Error)
			}

			if err := txDBC.AttachPartition(tableName, partitionName, usePartmanFormat, false); err != nil {
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

// gormTypeToPostgresType converts GORM/Go data types to PostgreSQL types

// CreatePartitionedTable creates a new partitioned table based on a GORM model struct
// If the table already exists, it returns without error
//
// Parameters:
//   - model: GORM model struct (must be a pointer, e.g., &models.MyModel{})
//   - tableName: Name for the partitioned table
//   - config: Partition configuration (strategy, columns, etc.)
//   - dryRun: If true, prints SQL without executing
//
// Returns: The SQL statement that was (or would be) executed
//
// Example:
//
//	config := partitions.NewRangePartitionConfig("created_at")
//	sql, err := partitions.CreatePartitionedTable(dbc, &MyModel{}, "my_table", config, true)
//
//nolint:gocyclo

// indexInfo holds information about a database index

// UpdatePartitionedTable updates an existing partitioned table schema based on a GORM model.
// If the table does not exist, it is created using CreatePartitionedTable with the provided config.
// Detects differences between the model and current database schema and generates ALTER statements.
//
// Parameters:
//   - model: GORM model struct (must be a pointer, e.g., &models.MyModel{})
//   - tableName: Name of the partitioned table (created if it does not exist)
//   - config: Partition configuration (used only when creating a new table)
//   - dryRun: If true, prints SQL without executing
//   - dropColumns: If true, columns present in the database but absent from the model will be dropped
//
// Returns: The SQL statements that were (or would be) executed
//
// Example:
//
//	config := db.NewRangePartitionConfig("created_at")
//	sql, err := dbc.UpdatePartitionedTable(&MyModel{}, "my_table", config, true, false)
//
// Note: Cannot modify partition keys or add unique constraints without partition keys
//
//nolint:gocyclo

// getCurrentColumns retrieves the current column schema from the database
// getCurrentIndexes retrieves the current indexes from the database

// getPartitionColumns retrieves the partition key columns for a table
func (dbc *DB) getPartitionColumns(tableName string) ([]string, error) {
	var columns []string

	query := `
		SELECT a.attname
		FROM pg_class c
		JOIN pg_partitioned_table pt ON pt.partrelid = c.oid
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY(pt.partattrs)
		WHERE c.relname = @table_name
			AND c.relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = 'public')
		ORDER BY array_position(pt.partattrs, a.attnum)
	`

	result := dbc.DB.Raw(query, sql.Named("table_name", tableName)).Scan(&columns)
	if result.Error != nil {
		return nil, result.Error
	}

	return columns, nil
}

// makeTableSpecificIndexName creates a table-specific index name from index fields
// This prevents index name collisions when creating multiple tables from the same model
//
// GORM often generates index names like "idx_{original_table_name}_{column_names}"
// (e.g., "idx_prow_job_run_tests_suite_id"). When creating a different table like
// "prow_job_run_tests_copy_1", we need to avoid the redundant name like
// "prow_job_run_tests_copy_1_idx_prow_job_run_tests_suite_id".
//
// Instead, we extract the column names from the index fields and generate:
// "{table_name}_idx_{column_names}"
//
// Examples:
//   - Index fields: [suite_id] -> "prow_job_run_tests_copy_1_idx_suite_id"
//   - Index fields: [created_at] -> "orders_copy_1_idx_created_at"
//   - Index fields: [user_id, org_id] -> "users_backup_idx_user_id_org_id"

// getPrimaryKeyColumns retrieves the columns that are part of the primary key for a table

// normalizeDataType normalizes data type strings for comparison
// Preserves type modifiers (length, precision, scale) while normalizing base type names
// Examples:
//   - "character varying(64)" -> "varchar(64)"
//   - "integer" -> "int"
//   - "numeric(8,2)" -> "numeric(8,2)" (preserved)
func normalizeDataType(dataType string) string {
	dataType = strings.ToLower(strings.TrimSpace(dataType))

	// Map common type variations to standard forms (preserving any modifiers).
	// Uses a slice for deterministic matching order — longer prefixes must come
	// before shorter ones (e.g., "character varying" before "character").
	replacements := []struct{ old, new string }{
		{"character varying", "varchar"},
		{"timestamp without time zone", "timestamp"},
		{"timestamp with time zone", "timestamptz"},
		{"time without time zone", "time"},
		{"time with time zone", "timetz"},
		{"double precision", "float8"},
		{"character", "char"},
		{"integer", "int"},
		{"int4", "int"},
		{"int8", "bigint"},
		{"bigserial", "bigint"},
		{"serial", "int"},
		{"smallint", "int2"},
		{"boolean", "bool"},
		{"real", "float4"},
	}

	// Try to replace the base type name while preserving modifiers
	for _, r := range replacements {
		if suffix, found := strings.CutPrefix(dataType, r.old); found {
			return r.new + suffix
		}
	}

	return dataType
}

// GetDetachedPartitionStats returns statistics about detached partitions for a given table
func (dbc *DB) GetDetachedPartitionStats(tableName string) (*PartitionStats, error) {
	start := time.Now()
	var stats PartitionStats

	// Detect partition format
	usePartmanFormat, err := dbc.detectPartitionFormat(tableName)
	if err != nil {
		log.WithError(err).WithField("table", tableName).Debug("failed to detect partition format, assuming standard")
		usePartmanFormat = false
	}

	// Build patterns based on detected format
	sqlPattern := getPartitionSQLPattern(usePartmanFormat)
	tablePattern := getPartitionLikePattern(tableName, usePartmanFormat)

	query := fmt.Sprintf(`
		WITH attached_partitions AS (
			SELECT c.relname AS tablename
			FROM pg_inherits i
			JOIN pg_class c ON i.inhrelid = c.oid
			JOIN pg_class p ON i.inhparent = p.oid
			WHERE p.relname = @table_name
		),
		detached_info AS (
			SELECT
				tablename,
				TO_DATE(SUBSTRING(tablename FROM '%s'), 'YYYY_MM_DD') AS partition_date,
				pg_total_relation_size('public.'||tablename) AS size_bytes
			FROM pg_tables
			WHERE schemaname = 'public'
				AND tablename LIKE @table_pattern ESCAPE '\'
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

	result := dbc.DB.Raw(query,
		sql.Named("table_name", tableName),
		sql.Named("table_pattern", tablePattern)).Scan(&stats)
	if result.Error != nil {
		log.WithError(result.Error).WithField("table", tableName).Error("failed to get detached partition statistics")
		return nil, result.Error
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
func (dbc *DB) AttachPartition(tableName, partitionName string, usePartmanFormat bool, dryRun bool) error {
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

	result := dbc.DB.Exec(query)
	if result.Error != nil {
		log.WithError(result.Error).WithFields(log.Fields{
			"partition": partitionName,
			"table":     tableName,
		}).Error("failed to attach partition")
		return result.Error
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
func (dbc *DB) IsPartitionAttached(partitionName string) (bool, error) {
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
			WHERE p.relname = @table_name
				AND c.relname = @partition_name
		) AS is_attached
	`

	result := dbc.DB.Raw(query,
		sql.Named("table_name", tableName),
		sql.Named("partition_name", partitionName)).Scan(&isAttached)
	if result.Error != nil {
		log.WithError(result.Error).WithFields(log.Fields{
			"partition": partitionName,
			"table":     tableName,
		}).Error("failed to check partition status")
		return false, result.Error
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
func (dbc *DB) DetachOldPartitions(tableName string, retentionDays int, dryRun bool) (int, error) {
	start := time.Now()

	// Validate retention policy first
	if err := dbc.ValidateRetentionPolicy(tableName, retentionDays); err != nil {
		return 0, fmt.Errorf("retention policy validation failed: %w", err)
	}

	// Get only attached partitions for removal (can only detach what's attached)
	partitions, err := dbc.GetPartitionsForRemoval(tableName, retentionDays, true)
	if err != nil {
		return 0, fmt.Errorf("failed to get partitions for removal: %w", err)
	}

	if len(partitions) == 0 {
		log.WithField("table", tableName).Info("no partitions to detach")
		return 0, nil
	}

	if dryRun {
		for _, partition := range partitions {
			if err := dbc.DetachPartition(partition.TableName, true); err != nil {
				return 0, fmt.Errorf("failed to dry-run detach partition %s: %w", partition.TableName, err)
			}
		}
		return len(partitions), nil
	}

	// Detach all old partitions in a transaction
	detachedCount := 0
	var totalSize int64

	err = dbc.DB.Transaction(func(tx *gorm.DB) error {
		txDBC := &DB{DB: tx}
		for _, partition := range partitions {
			if err := txDBC.DetachPartition(partition.TableName, false); err != nil {
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

// detectPartitionFormat examines existing partitions to determine naming format
// Returns true if pg_partman format (_pYYYY_MM_DD), false for standard format
// Returns error if table not found or has no partitions
func (dbc *DB) detectPartitionFormat(tableName string) (bool, error) {
	var partitionName string
	query := `
		SELECT c.relname
		FROM pg_inherits i
		JOIN pg_class c ON i.inhrelid = c.oid
		JOIN pg_class p ON i.inhparent = p.oid
		WHERE p.relname = @table_name
		LIMIT 1
	`

	result := dbc.DB.Raw(query, sql.Named("table_name", tableName)).Scan(&partitionName)
	if result.Error != nil {
		return false, fmt.Errorf("failed to detect partition format: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		// No partitions found - default to standard format
		return false, nil
	}

	// Check if partition name contains _p followed by year (e.g., _p2026)
	// pg_partman format: tablename_pYYYY_MM_DD
	// standard format: tablename_YYYY_MM_DD
	return strings.Contains(partitionName, "_p20"), nil
}

// getPartitionSQLPattern returns SQL regex pattern for matching partitions
func getPartitionSQLPattern(usePartmanFormat bool) string {
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
