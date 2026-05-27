package partitioning

import (
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgconn"
	"github.com/lib/pq"
	log "github.com/sirupsen/logrus"
)

// maxPartitionNameLen is the safe maximum for partition table names.
// PostgreSQL identifiers can be up to 63 characters (NAMEDATALEN-1), but
// internally PG creates an array type prefixed with "_". A 63-character
// table name produces a 64-character array type name which exceeds the
// limit, causing SQLSTATE 42710. We cap at 62 to leave room.
const maxPartitionNameLen = 62

// hashSuffixLen is the length of the "_xxxx" hash suffix appended when
// a table name prefix must be shortened (underscore + 4 hex digits).
const hashSuffixLen = 5

func validatePartitionNameLength(name string) error {
	if len(name) > maxPartitionNameLen {
		return fmt.Errorf("partition name %q is %d characters, exceeds safe limit of %d",
			name, len(name), maxPartitionNameLen)
	}
	return nil
}

// dateSuffixLen is the length added by a daily partition date suffix: "_YYYY_MM_DD" (11) or "_pYYYY_MM_DD" (12)
func dateSuffixLen(usePartmanFormat bool) int {
	if usePartmanFormat {
		return 12
	}
	return 11
}

// shortenTablePrefix truncates a table name prefix to fit within maxLen and
// appends a 4-hex-digit FNV hash of the original name for uniqueness.
// If the name already fits it is returned unchanged.
//
// Example:
//
//	shortenTablePrefix("prow_job_run_annotations_new", 20)
//	→ "prow_job_run_an_8f3a"   (15 chars of prefix + "_" + 4 hex = 20)
func shortenTablePrefix(name string, maxLen int) string {
	if len(name) <= maxLen {
		return name
	}

	h := fnv.New32a()
	h.Write([]byte(name))
	hash := fmt.Sprintf("%04x", h.Sum32()&0xFFFF)

	truncLen := maxLen - hashSuffixLen
	if truncLen < 1 {
		truncLen = 1
	}

	// Avoid ending on an underscore before the hash separator
	prefix := name[:truncLen]
	prefix = strings.TrimRight(prefix, "_")

	return prefix + "_" + hash
}

// buildNestedPartitionPrefix computes the intermediate partition prefix for a
// given table and release name, shortening the table name if the resulting
// daily partition names would exceed PostgreSQL's identifier limit.
// Returns the intermediate prefix and the (possibly shortened) table prefix.
func buildNestedPartitionPrefix(tableName, release string, usePartmanFormat bool) string {
	safeName := sanitizePartitionName(release)
	full := tableName + "_" + safeName

	// Check if the longest daily name fits
	maxDaily := len(full) + dateSuffixLen(usePartmanFormat)
	if maxDaily <= maxPartitionNameLen {
		return full
	}

	// Calculate how much space the table prefix can use:
	// total = tablePrefix + "_" + safeName + dateSuffix
	available := maxPartitionNameLen - dateSuffixLen(usePartmanFormat) - 1 - len(safeName)
	shortened := shortenTablePrefix(tableName, available)
	return shortened + "_" + safeName
}

// GetPartitionHierarchy returns the complete partition hierarchy for a table
// including intermediate partitions and leaf partitions
func (dbp *DB_PARTITIONS) GetPartitionHierarchy(tableName string) ([]PartitionHierarchyInfo, error) {
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

	result := dbp.DB.Raw(query, sql.Named("table_name", tableName)).Scan(&results)
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
func (dbp *DB_PARTITIONS) ListLeafPartitions(tableName string) ([]PartitionHierarchyInfo, error) {
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

	result := dbp.DB.Raw(query, sql.Named("table_name", tableName)).Scan(&results)
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
func (dbp *DB_PARTITIONS) GetPartitionLevel(partitionName string) (int, error) {
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
	result := dbp.DB.Raw(query, sql.Named("partition_name", partitionName)).Scan(&level)
	if result.Error != nil {
		return 0, fmt.Errorf("failed to get partition level: %w", result.Error)
	}

	return level, nil
}

// extractDateFromPartitionName extracts date from partition name
// Supports formats: tablename_YYYY_MM_DD, tablename_suffix_YYYY_MM_DD,
// tablename_pYYYY_MM_DD (pg_partman), tablename_suffix_pYYYY_MM_DD
func extractDateFromPartitionName(partitionName string) *time.Time {
	// Find last occurrence of pattern _YYYY_MM_DD or _pYYYY_MM_DD
	parts := strings.Split(partitionName, "_")
	if len(parts) < 3 {
		return nil
	}

	// Take last 3 parts as potential date
	dateStr := strings.Join(parts[len(parts)-3:], "_")

	// Check for pg_partman format (pYYYY_MM_DD)
	if len(dateStr) > 1 && dateStr[0] == 'p' && dateStr[1] >= '0' && dateStr[1] <= '9' {
		// Strip the 'p' prefix
		dateStr = dateStr[1:]
	}

	t, err := time.Parse("2006_01_02", dateStr)
	if err != nil {
		return nil
	}

	return &t
}

// partitionExists checks if a partition table exists
func (dbp *DB_PARTITIONS) partitionExists(partitionName string) (bool, error) {
	var exists bool
	query := `
		SELECT EXISTS (
			SELECT 1 FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relname = @partition_name AND n.nspname = 'public'
		)
	`

	result := dbp.DB.Raw(query, sql.Named("partition_name", partitionName)).Scan(&exists)
	return exists, result.Error
}

// CreateMissingPartitionsListToRange creates LIST → RANGE nested partitions
// For each release value, creates an intermediate partition that is RANGE-partitioned by date
func (dbp *DB_PARTITIONS) CreateMissingPartitionsListToRange(
	tableName string,
	releases []string, // List of release names (e.g., ["v1.0", "v2.0", "v3.0"])
	startDate, endDate time.Time,
	dateColumn string,
	usePartmanFormat bool,
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
		intermediatePartition := buildNestedPartitionPrefix(tableName, release, usePartmanFormat)

		if intermediatePartition != tableName+"_"+sanitizePartitionName(release) {
			l.WithFields(log.Fields{
				"original":  tableName + "_" + sanitizePartitionName(release),
				"shortened": intermediatePartition,
			}).Info("shortened partition prefix to fit PostgreSQL identifier limit")
		}

		// Check if intermediate partition already exists
		exists, err := dbp.partitionExists(intermediatePartition)
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
				createdCount++
			} else {
				err := dbp.createListMemberWithRangeSubPartitions(
					tableName,
					intermediatePartition,
					release,
					dateColumn,
				)
				if err != nil {
					// SQLSTATE 42P17 = partition overlap. This happens when a
					// partition for this list value already exists under a
					// different name — e.g. after a table rename where a
					// hash-shortened name no longer matches the computed name.
					var pgErr *pgconn.PgError
					if errors.As(err, &pgErr) && pgErr.Code == "42P17" {
						existingName, findErr := dbp.findPartitionForListValue(tableName, release)
						if findErr == nil && existingName != "" {
							l.WithFields(log.Fields{
								"computed": intermediatePartition,
								"existing": existingName,
								"release":  release,
							}).Warn("partition for list value already exists under a different name, using existing partition")
							intermediatePartition = existingName
						} else {
							return createdCount, fmt.Errorf("failed to create intermediate partition %s: %w", intermediatePartition, err)
						}
					} else {
						return createdCount, fmt.Errorf("failed to create intermediate partition %s: %w", intermediatePartition, err)
					}
				} else {
					l.WithField("partition", intermediatePartition).Info("created intermediate partition")
					createdCount++
				}
			}
		}

		// Create daily partitions under this release
		if !dryRun {
			dailyCount, err := dbp.createDailyPartitionsUnder(
				intermediatePartition,
				startDate,
				endDate,
				usePartmanFormat,
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

// findPartitionForListValue finds an existing partition of parentTable whose
// list bounds match the given value. Returns the partition name or "" if none found.
func (dbp *DB_PARTITIONS) findPartitionForListValue(parentTable, listValue string) (string, error) {
	query := `
		SELECT child.relname
		FROM pg_class parent
		JOIN pg_namespace n ON n.oid = parent.relnamespace
		JOIN pg_inherits i ON i.inhparent = parent.oid
		JOIN pg_class child ON child.oid = i.inhrelid
		WHERE parent.relname = @parent_table
		  AND n.nspname = 'public'
		  AND pg_get_expr(child.relpartbound, child.oid) = 'FOR VALUES IN (' || quote_literal(@list_value) || ')'
		LIMIT 1
	`
	var partitionName string
	result := dbp.DB.Raw(query,
		sql.Named("parent_table", parentTable),
		sql.Named("list_value", listValue),
	).Scan(&partitionName)
	if result.Error != nil {
		return "", result.Error
	}
	return partitionName, nil
}

// createListMemberWithRangeSubPartitions creates a LIST partition member that is itself RANGE-partitioned
func (dbp *DB_PARTITIONS) createListMemberWithRangeSubPartitions(
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
		"CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES IN (%s) PARTITION BY RANGE (%s)",
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

	result := dbp.DB.Exec(sql)
	if result.Error != nil {
		return fmt.Errorf("failed to execute CREATE TABLE: %w", result.Error)
	}

	return nil
}

// createDailyPartitionsUnder creates daily RANGE partitions under an intermediate partition
func (dbp *DB_PARTITIONS) createDailyPartitionsUnder(
	intermediatePartition string,
	startDate, endDate time.Time,
	usePartmanFormat bool,
	dryRun bool,
) (int, error) {
	createdCount := 0

	// Normalize dates to midnight UTC
	currentDate := startDate.UTC().Truncate(24 * time.Hour)
	endDateNormalized := endDate.UTC().Truncate(24 * time.Hour)

	for !currentDate.After(endDateNormalized) {
		nextDate := currentDate.AddDate(0, 0, 1)

		// Partition name: events_v1_0_2024_01_01 or events_v1_0_p2024_01_01
		var dailyPartition string
		if usePartmanFormat {
			dailyPartition = fmt.Sprintf("%s_p%s", intermediatePartition, currentDate.Format("2006_01_02"))
		} else {
			dailyPartition = fmt.Sprintf("%s_%s", intermediatePartition, currentDate.Format("2006_01_02"))
		}

		if err := validatePartitionNameLength(dailyPartition); err != nil {
			return createdCount, err
		}

		// Check if daily partition already exists
		exists, err := dbp.partitionExists(dailyPartition)
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
					"CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)",
					pq.QuoteIdentifier(dailyPartition),
					pq.QuoteIdentifier(intermediatePartition),
					pq.QuoteLiteral(currentDate.Format("2006-01-02")),
					pq.QuoteLiteral(nextDate.Format("2006-01-02")),
				)

				result := dbp.DB.Exec(sql)
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
func (dbp *DB_PARTITIONS) GetDailyPartitionsForRelease(
	tableName string,
	release string,
	usePartmanFormat bool,
) ([]PartitionHierarchyInfo, error) {
	intermediatePartition := buildNestedPartitionPrefix(tableName, release, usePartmanFormat)

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

	result := dbp.DB.Raw(query, sql.Named("intermediate_partition", intermediatePartition)).Scan(&partitions)

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

var partitionBoundsFromRe = regexp.MustCompile(`FROM \('(\d{4}-\d{2}-\d{2})`)

// extractDateFromPartitionBounds parses the FROM date from a RANGE partition
// bounds expression like "FOR VALUES FROM ('2026-04-29') TO ('2026-04-30')".
func extractDateFromPartitionBounds(bounds string) *time.Time {
	m := partitionBoundsFromRe.FindStringSubmatch(bounds)
	if len(m) < 2 {
		return nil
	}
	t, err := time.Parse("2006-01-02", m[1])
	if err != nil {
		return nil
	}
	return &t
}

// RenamePartitionsToMatchConfig renames all partitions of tableName so that
// their names match what buildNestedPartitionPrefix would produce for the
// current table name and configuration. This is useful after a table swap
// where partitions created under the old name (possibly hash-shortened)
// no longer match the expected naming for the renamed table.
func (dbp *DB_PARTITIONS) RenamePartitionsToMatchConfig(
	tableName string,
	releases []string,
	usePartmanFormat bool,
	dryRun bool,
) (int, error) {
	renamedCount := 0

	l := log.WithFields(log.Fields{
		"table":   tableName,
		"dry_run": dryRun,
	})

	l.Info("reconciling partition names with current table configuration")

	for _, release := range releases {
		expectedIntermediate := buildNestedPartitionPrefix(tableName, release, usePartmanFormat)

		actualIntermediate, err := dbp.findPartitionForListValue(tableName, release)
		if err != nil {
			return renamedCount, fmt.Errorf("failed to find partition for release %s: %w", release, err)
		}
		if actualIntermediate == "" {
			l.WithField("release", release).Debug("no partition exists for release, skipping")
			continue
		}

		// Rename the intermediate partition if needed
		if actualIntermediate != expectedIntermediate {
			if err := validatePartitionNameLength(expectedIntermediate); err != nil {
				return renamedCount, fmt.Errorf("cannot rename %s: target name too long: %w", actualIntermediate, err)
			}

			exists, err := dbp.partitionExists(expectedIntermediate)
			if err != nil {
				return renamedCount, fmt.Errorf("failed to check if %s exists: %w", expectedIntermediate, err)
			}
			if exists {
				return renamedCount, fmt.Errorf("cannot rename %s to %s: target already exists", actualIntermediate, expectedIntermediate)
			}

			if dryRun {
				l.WithFields(log.Fields{
					"from":    actualIntermediate,
					"to":      expectedIntermediate,
					"release": release,
				}).Info("[DRY RUN] would rename intermediate partition")
			} else {
				renameSQL := fmt.Sprintf("ALTER TABLE %s RENAME TO %s",
					pq.QuoteIdentifier(actualIntermediate),
					pq.QuoteIdentifier(expectedIntermediate),
				)
				if result := dbp.DB.Exec(renameSQL); result.Error != nil {
					return renamedCount, fmt.Errorf("failed to rename %s to %s: %w", actualIntermediate, expectedIntermediate, result.Error)
				}
				l.WithFields(log.Fields{
					"from":    actualIntermediate,
					"to":      expectedIntermediate,
					"release": release,
				}).Info("renamed intermediate partition")
			}
			renamedCount++
		}

		// Find daily child partitions under the intermediate (use the name
		// that currently exists in the catalog — the renamed one if we just
		// renamed it, otherwise the original).
		currentIntermediate := expectedIntermediate
		if dryRun && actualIntermediate != expectedIntermediate {
			currentIntermediate = actualIntermediate
		}

		children, err := dbp.getChildPartitions(currentIntermediate)
		if err != nil {
			return renamedCount, fmt.Errorf("failed to list children of %s: %w", currentIntermediate, err)
		}

		for _, child := range children {
			date := extractDateFromPartitionBounds(child.bounds)
			if date == nil {
				date = extractDateFromPartitionName(child.name)
			}
			if date == nil {
				l.WithField("partition", child.name).Warn("could not extract date from partition, skipping")
				continue
			}

			var expectedDaily string
			if usePartmanFormat {
				expectedDaily = fmt.Sprintf("%s_p%s", expectedIntermediate, date.Format("2006_01_02"))
			} else {
				expectedDaily = fmt.Sprintf("%s_%s", expectedIntermediate, date.Format("2006_01_02"))
			}

			if child.name == expectedDaily {
				continue
			}

			if err := validatePartitionNameLength(expectedDaily); err != nil {
				return renamedCount, fmt.Errorf("cannot rename %s: target name too long: %w", child.name, err)
			}

			if dryRun {
				l.WithFields(log.Fields{
					"from": child.name,
					"to":   expectedDaily,
				}).Info("[DRY RUN] would rename daily partition")
			} else {
				renameSQL := fmt.Sprintf("ALTER TABLE %s RENAME TO %s",
					pq.QuoteIdentifier(child.name),
					pq.QuoteIdentifier(expectedDaily),
				)
				if result := dbp.DB.Exec(renameSQL); result.Error != nil {
					return renamedCount, fmt.Errorf("failed to rename %s to %s: %w", child.name, expectedDaily, result.Error)
				}
				l.WithFields(log.Fields{
					"from": child.name,
					"to":   expectedDaily,
				}).Debug("renamed daily partition")
			}
			renamedCount++
		}
	}

	l.WithField("total_renamed", renamedCount).Info("partition name reconciliation complete")
	return renamedCount, nil
}

type childPartition struct {
	name   string
	bounds string
}

// getChildPartitions returns the immediate child partitions of a table.
func (dbp *DB_PARTITIONS) getChildPartitions(parentName string) ([]childPartition, error) {
	query := `
		SELECT child.relname AS name,
		       pg_get_expr(child.relpartbound, child.oid) AS bounds
		FROM pg_class parent
		JOIN pg_namespace n ON n.oid = parent.relnamespace
		JOIN pg_inherits i ON i.inhparent = parent.oid
		JOIN pg_class child ON child.oid = i.inhrelid
		WHERE parent.relname = @parent_name AND n.nspname = 'public'
		ORDER BY child.relname
	`

	var rows []struct {
		Name   string
		Bounds string
	}
	result := dbp.DB.Raw(query, sql.Named("parent_name", parentName)).Scan(&rows)
	if result.Error != nil {
		return nil, result.Error
	}

	children := make([]childPartition, len(rows))
	for i, r := range rows {
		children[i] = childPartition{name: r.Name, bounds: r.Bounds}
	}
	return children, nil
}
