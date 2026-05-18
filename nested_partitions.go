package gopar

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
	log "github.com/sirupsen/logrus"
)

// GetPartitionHierarchy returns the complete partition hierarchy for a table
// including intermediate partitions and leaf partitions
func (dbc *DB) GetPartitionHierarchy(tableName string) ([]PartitionHierarchyInfo, error) {
	start := time.Now()

	// Recursive query to get full hierarchy
	query := `
		WITH RECURSIVE partition_tree AS (
			-- Base case: root partitioned table
			SELECT
				c.relname AS table_name,
				NULL::name AS parent_name,
				0 AS level,
				CASE pp.partstrat
					WHEN 'r' THEN 'RANGE'
					WHEN 'l' THEN 'LIST'
					WHEN 'h' THEN 'HASH'
					ELSE 'UNKNOWN'
				END AS strategy,
				pg_get_expr(pp.partexprs, pp.partrelid) AS partition_key,
				NULL::text AS partition_bounds,
				EXISTS(SELECT 1 FROM pg_partitioned_table WHERE partrelid = c.oid) AS is_partitioned,
				c.oid AS partition_oid
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			LEFT JOIN pg_partitioned_table pp ON pp.partrelid = c.oid
			WHERE c.relname = @table_name AND n.nspname = 'public'

			UNION ALL

			-- Recursive case: child partitions
			SELECT
				child.relname AS table_name,
				parent.relname AS parent_name,
				pt.level + 1 AS level,
				CASE pp.partstrat
					WHEN 'r' THEN 'RANGE'
					WHEN 'l' THEN 'LIST'
					WHEN 'h' THEN 'HASH'
					ELSE NULL
				END AS strategy,
				pg_get_expr(pp.partexprs, pp.partrelid) AS partition_key,
				pg_get_expr(child.relpartbound, child.oid) AS partition_bounds,
				EXISTS(SELECT 1 FROM pg_partitioned_table WHERE partrelid = child.oid) AS is_partitioned,
				child.oid AS partition_oid
			FROM partition_tree pt
			JOIN pg_class parent ON parent.relname = pt.table_name
			JOIN pg_inherits i ON i.inhparent = parent.oid
			JOIN pg_class child ON child.oid = i.inhrelid
			LEFT JOIN pg_partitioned_table pp ON pp.partrelid = child.oid
		)
		SELECT
			pt.table_name,
			pt.parent_name,
			pt.level,
			pt.strategy,
			COALESCE(pt.partition_key, '') AS partition_key,
			COALESCE(pt.partition_bounds, '') AS partition_bounds,
			pt.is_partitioned,
			pg_total_relation_size('public.' || pt.table_name) AS size_bytes,
			pg_size_pretty(pg_total_relation_size('public.' || pt.table_name)) AS size_pretty,
			COALESCE(s.n_live_tup, 0) AS row_estimate
		FROM partition_tree pt
		LEFT JOIN pg_stat_user_tables s ON s.relname = pt.table_name AND s.schemaname = 'public'
		ORDER BY pt.level, pt.table_name
	`

	var results []struct {
		TableName       string
		ParentName      sql.NullString
		Level           int
		Strategy        sql.NullString
		PartitionKey    string
		PartitionBounds string
		IsPartitioned   bool
		SizeBytes       int64
		SizePretty      string
		RowEstimate     int64
	}

	result := dbc.DB.Raw(query, sql.Named("table_name", tableName)).Scan(&results)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to query partition hierarchy: %w", result.Error)
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("table %s not found or not partitioned", tableName)
	}

	// Convert to PartitionHierarchyInfo
	var hierarchy []PartitionHierarchyInfo
	for _, r := range results {
		info := PartitionHierarchyInfo{
			TableName:       r.TableName,
			Level:           PartitionLevel(r.Level),
			IsPartitioned:   r.IsPartitioned,
			IsLeaf:          !r.IsPartitioned && r.Level > 0, // Leaf if not partitioned and not root
			PartitionKey:    r.PartitionKey,
			PartitionBounds: r.PartitionBounds,
			SizeBytes:       r.SizeBytes,
			SizePretty:      r.SizePretty,
			RowEstimate:     r.RowEstimate,
		}

		if r.ParentName.Valid {
			info.ParentTable = r.ParentName.String
		}

		if r.Strategy.Valid {
			info.Strategy = r.Strategy.String
		}

		// Try to extract date from partition name if it follows naming convention
		if date := extractDateFromPartitionName(r.TableName); date != nil {
			info.PartitionDate = date
		}

		hierarchy = append(hierarchy, info)
	}

	elapsed := time.Since(start)
	log.WithFields(log.Fields{
		"table":   tableName,
		"count":   len(hierarchy),
		"elapsed": elapsed,
	}).Info("retrieved partition hierarchy")

	return hierarchy, nil
}

// ListLeafPartitions returns only the leaf partitions (those that actually hold data)
// for a partitioned table, regardless of nesting level
func (dbc *DB) ListLeafPartitions(tableName string) ([]PartitionHierarchyInfo, error) {
	start := time.Now()

	query := `
		WITH RECURSIVE partition_tree AS (
			SELECT
				c.relname AS table_name,
				0 AS level,
				c.oid AS partition_oid,
				NULL::name AS parent_name
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relname = @table_name AND n.nspname = 'public'

			UNION ALL

			SELECT
				child.relname AS table_name,
				pt.level + 1 AS level,
				child.oid AS partition_oid,
				parent.relname AS parent_name
			FROM partition_tree pt
			JOIN pg_class parent ON parent.relname = pt.table_name
			JOIN pg_inherits i ON i.inhparent = parent.oid
			JOIN pg_class child ON child.oid = i.inhrelid
		)
		SELECT
			pt.table_name,
			pt.parent_name,
			pt.level,
			pg_total_relation_size('public.' || pt.table_name) AS size_bytes,
			pg_size_pretty(pg_total_relation_size('public.' || pt.table_name)) AS size_pretty,
			COALESCE(s.n_live_tup, 0) AS row_estimate,
			pg_get_expr(c.relpartbound, c.oid) AS partition_bounds
		FROM partition_tree pt
		JOIN pg_class c ON c.relname = pt.table_name
		LEFT JOIN pg_stat_user_tables s ON s.relname = pt.table_name AND s.schemaname = 'public'
		-- Only leaf partitions (not further partitioned)
		WHERE NOT EXISTS (
			SELECT 1 FROM pg_partitioned_table pp WHERE pp.partrelid = c.oid
		)
		AND pt.level > 0  -- Exclude root table
		ORDER BY pt.level, pt.table_name
	`

	var results []struct {
		TableName       string
		ParentName      sql.NullString
		Level           int
		SizeBytes       int64
		SizePretty      string
		RowEstimate     int64
		PartitionBounds sql.NullString
	}

	result := dbc.DB.Raw(query, sql.Named("table_name", tableName)).Scan(&results)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to query leaf partitions: %w", result.Error)
	}

	// Convert to PartitionHierarchyInfo
	var leaves []PartitionHierarchyInfo
	for _, r := range results {
		info := PartitionHierarchyInfo{
			TableName:     r.TableName,
			Level:         PartitionLevel(r.Level),
			IsLeaf:        true,
			IsPartitioned: false,
			SizeBytes:     r.SizeBytes,
			SizePretty:    r.SizePretty,
			RowEstimate:   r.RowEstimate,
		}

		if r.ParentName.Valid {
			info.ParentTable = r.ParentName.String
		}

		if r.PartitionBounds.Valid {
			info.PartitionBounds = r.PartitionBounds.String
		}

		if date := extractDateFromPartitionName(r.TableName); date != nil {
			info.PartitionDate = date
		}

		leaves = append(leaves, info)
	}

	elapsed := time.Since(start)
	log.WithFields(log.Fields{
		"table":   tableName,
		"count":   len(leaves),
		"elapsed": elapsed,
	}).Info("listed leaf partitions")

	return leaves, nil
}

// GetPartitionLevel returns the nesting level of a partition
// Returns 0 for the root table, 1 for first-level partitions, etc.
func (dbc *DB) GetPartitionLevel(partitionName string) (int, error) {
	query := `
		WITH RECURSIVE partition_path AS (
			SELECT
				c.relname AS table_name,
				0 AS level
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relname = @partition_name AND n.nspname = 'public'

			UNION ALL

			SELECT
				parent.relname AS table_name,
				pp.level + 1 AS level
			FROM partition_path pp
			JOIN pg_class child ON child.relname = pp.table_name
			JOIN pg_inherits i ON i.inhrelid = child.oid
			JOIN pg_class parent ON parent.oid = i.inhparent
		)
		SELECT COALESCE(MAX(level), 0) FROM partition_path
	`

	var level int
	result := dbc.DB.Raw(query, sql.Named("partition_name", partitionName)).Scan(&level)
	if result.Error != nil {
		return 0, fmt.Errorf("failed to get partition level: %w", result.Error)
	}

	return level, nil
}

// extractDateFromPartitionName extracts date from partition name
// Supports formats: tablename_YYYY_MM_DD or tablename_suffix_YYYY_MM_DD
func extractDateFromPartitionName(partitionName string) *time.Time {
	// Find last occurrence of pattern _YYYY_MM_DD
	parts := strings.Split(partitionName, "_")
	if len(parts) < 3 {
		return nil
	}

	// Take last 3 parts as potential date
	dateStr := strings.Join(parts[len(parts)-3:], "_")
	t, err := time.Parse("2006_01_02", dateStr)
	if err != nil {
		return nil
	}

	return &t
}

// partitionExists checks if a partition table exists
func (dbc *DB) partitionExists(partitionName string) (bool, error) {
	var exists bool
	query := `
		SELECT EXISTS (
			SELECT 1 FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relname = @partition_name AND n.nspname = 'public'
		)
	`

	result := dbc.DB.Raw(query, sql.Named("partition_name", partitionName)).Scan(&exists)
	return exists, result.Error
}

// CreateMissingPartitionsListToRange creates LIST → RANGE nested partitions
// For each release value, creates an intermediate partition that is RANGE-partitioned by date
func (dbc *DB) CreateMissingPartitionsListToRange(
	tableName string,
	releases []string, // List of release names (e.g., ["v1.0", "v2.0", "v3.0"])
	startDate, endDate time.Time,
	dateColumn string,
	dryRun bool,
) (int, error) {
	createdCount := 0

	l := log.WithFields(log.Fields{
		"table":      tableName,
		"releases":   releases,
		"start_date": startDate.Format("2006-01-02"),
		"end_date":   endDate.Format("2006-01-02"),
		"dry_run":    dryRun,
	})

	l.Info("creating LIST → RANGE nested partitions")

	// For each release, create intermediate partition and its daily sub-partitions
	for _, release := range releases {
		// Sanitize release name for table naming (replace dots, spaces, etc.)
		safeName := sanitizePartitionName(release)
		intermediatePartition := fmt.Sprintf("%s_%s", tableName, safeName)

		// Check if intermediate partition already exists
		exists, err := dbc.partitionExists(intermediatePartition)
		if err != nil {
			return createdCount, fmt.Errorf("failed to check if %s exists: %w", intermediatePartition, err)
		}

		if !exists {
			// Create intermediate partition (LIST member that is RANGE-partitioned)
			if dryRun {
				l.WithFields(log.Fields{
					"partition": intermediatePartition,
					"release":   release,
				}).Info("[DRY RUN] would create intermediate LIST partition with RANGE sub-partitioning")
			} else {
				err := dbc.createListMemberWithRangeSubPartitions(
					tableName,
					intermediatePartition,
					release,
					dateColumn,
				)
				if err != nil {
					return createdCount, fmt.Errorf("failed to create intermediate partition %s: %w", intermediatePartition, err)
				}
				l.WithField("partition", intermediatePartition).Info("created intermediate partition")
			}
			createdCount++
		}

		// Create daily partitions under this release
		if !dryRun {
			dailyCount, err := dbc.createDailyPartitionsUnder(
				intermediatePartition,
				startDate,
				endDate,
				dryRun,
			)
			if err != nil {
				return createdCount, fmt.Errorf("failed to create daily partitions under %s: %w", intermediatePartition, err)
			}
			createdCount += dailyCount
			l.WithFields(log.Fields{
				"intermediate": intermediatePartition,
				"daily_count":  dailyCount,
			}).Info("created daily partitions")
		}
	}

	l.WithField("total_created", createdCount).Info("completed LIST → RANGE partition creation")
	return createdCount, nil
}

// createListMemberWithRangeSubPartitions creates a LIST partition member that is itself RANGE-partitioned
func (dbc *DB) createListMemberWithRangeSubPartitions(
	parentTable string,
	partitionName string,
	listValue string,
	rangeColumn string,
) error {
	// SQL example:
	// CREATE TABLE events_v1_0 PARTITION OF events
	//   FOR VALUES IN ('v1.0')
	//   PARTITION BY RANGE (event_date);

	sql := fmt.Sprintf(
		"CREATE TABLE %s PARTITION OF %s FOR VALUES IN (%s) PARTITION BY RANGE (%s)",
		pq.QuoteIdentifier(partitionName),
		pq.QuoteIdentifier(parentTable),
		pq.QuoteLiteral(listValue),
		pq.QuoteIdentifier(rangeColumn),
	)

	log.WithFields(log.Fields{
		"sql":       sql,
		"partition": partitionName,
		"value":     listValue,
	}).Debug("creating LIST member with RANGE sub-partitioning")

	result := dbc.DB.Exec(sql)
	if result.Error != nil {
		return fmt.Errorf("failed to execute CREATE TABLE: %w", result.Error)
	}

	return nil
}

// createDailyPartitionsUnder creates daily RANGE partitions under an intermediate partition
func (dbc *DB) createDailyPartitionsUnder(
	intermediatePartition string,
	startDate, endDate time.Time,
	dryRun bool,
) (int, error) {
	createdCount := 0

	// Normalize dates to midnight UTC
	currentDate := startDate.UTC().Truncate(24 * time.Hour)
	endDateNormalized := endDate.UTC().Truncate(24 * time.Hour)

	for !currentDate.After(endDateNormalized) {
		nextDate := currentDate.AddDate(0, 0, 1)

		// Partition name: events_v1_0_2024_01_01
		dailyPartition := fmt.Sprintf("%s_%s",
			intermediatePartition,
			currentDate.Format("2006_01_02"))

		// Check if daily partition already exists
		exists, err := dbc.partitionExists(dailyPartition)
		if err != nil {
			return createdCount, err
		}

		if !exists {
			if dryRun {
				log.WithField("partition", dailyPartition).Info("[DRY RUN] would create daily partition")
			} else {
				// Create daily partition
				// CREATE TABLE events_v1_0_2024_01_01 PARTITION OF events_v1_0
				//   FOR VALUES FROM ('2024-01-01') TO ('2024-01-02');

				sql := fmt.Sprintf(
					"CREATE TABLE %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)",
					pq.QuoteIdentifier(dailyPartition),
					pq.QuoteIdentifier(intermediatePartition),
					pq.QuoteLiteral(currentDate.Format("2006-01-02")),
					pq.QuoteLiteral(nextDate.Format("2006-01-02")),
				)

				result := dbc.DB.Exec(sql)
				if result.Error != nil {
					return createdCount, fmt.Errorf("failed to create daily partition %s: %w", dailyPartition, result.Error)
				}

				log.WithField("partition", dailyPartition).Debug("created daily partition")
			}
			createdCount++
		}

		currentDate = nextDate
	}

	return createdCount, nil
}

// sanitizePartitionName converts a release name into a valid partition name component
// Examples:
//
//	"v1.0" → "v1_0"
//	"2024.1" → "2024_1"
//	"Release 2.0" → "release_2_0"
func sanitizePartitionName(name string) string {
	// Replace common separators with underscore
	result := strings.ReplaceAll(name, ".", "_")
	result = strings.ReplaceAll(result, " ", "_")
	result = strings.ReplaceAll(result, "-", "_")
	result = strings.ToLower(result)

	// Remove any remaining non-alphanumeric characters except underscore
	var sanitized strings.Builder
	for _, r := range result {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			sanitized.WriteRune(r)
		}
	}

	return sanitized.String()
}

// GetDailyPartitionsForRelease returns all daily partitions for a specific release
func (dbc *DB) GetDailyPartitionsForRelease(
	tableName string,
	release string,
) ([]PartitionHierarchyInfo, error) {
	safeName := sanitizePartitionName(release)
	intermediatePartition := fmt.Sprintf("%s_%s", tableName, safeName)

	// Get all children of the intermediate partition
	query := `
		SELECT
			child.relname AS table_name,
			parent.relname AS parent_name,
			2 AS level,
			pg_get_expr(child.relpartbound, child.oid) AS partition_bounds,
			pg_total_relation_size('public.' || child.relname) AS size_bytes,
			pg_size_pretty(pg_total_relation_size('public.' || child.relname)) AS size_pretty,
			COALESCE(s.n_live_tup, 0) AS row_estimate
		FROM pg_class parent
		JOIN pg_inherits i ON i.inhparent = parent.oid
		JOIN pg_class child ON child.oid = i.inhrelid
		LEFT JOIN pg_stat_user_tables s ON s.relname = child.relname AND s.schemaname = 'public'
		WHERE parent.relname = @intermediate_partition
		ORDER BY child.relname
	`

	var partitions []struct {
		TableName       string
		ParentName      string
		Level           int
		PartitionBounds string
		SizeBytes       int64
		SizePretty      string
		RowEstimate     int64
	}

	result := dbc.DB.Raw(query, sql.Named("intermediate_partition", intermediatePartition)).Scan(&partitions)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get daily partitions for release %s: %w", release, result.Error)
	}

	// Convert to PartitionHierarchyInfo
	var results []PartitionHierarchyInfo
	for _, p := range partitions {
		// Extract date from partition name (events_v1_0_2024_01_01)
		datePart := extractDateFromPartitionName(p.TableName)

		info := PartitionHierarchyInfo{
			TableName:       p.TableName,
			ParentTable:     p.ParentName,
			Level:           PartitionLevel(p.Level),
			IsLeaf:          true,
			IsPartitioned:   false,
			PartitionBounds: p.PartitionBounds,
			SizeBytes:       p.SizeBytes,
			SizePretty:      p.SizePretty,
			RowEstimate:     p.RowEstimate,
		}

		if datePart != nil {
			info.PartitionDate = datePart
		}

		results = append(results, info)
	}

	return results, nil
}
