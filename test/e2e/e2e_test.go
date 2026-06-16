package e2e

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/openshift-eng/gopar/partitioning"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func getE2EDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("GOPAR_TEST_DSN")
	if dsn == "" {
		t.Skip("GOPAR_TEST_DSN not set, skipping e2e test")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("failed to ping database: %v", err)
	}

	t.Cleanup(func() {
		db.Close()
	})

	return db
}

// dropTable is a cleanup helper that drops a table and all of its partitions.
func dropTable(t *testing.T, db *sql.DB, tableName string) {
	t.Helper()
	_, err := db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", tableName))
	if err != nil {
		t.Logf("warning: failed to drop table %s: %v", tableName, err)
	}
}

// countAttachedPartitions returns the number of child partitions attached to tableName.
func countAttachedPartitions(t *testing.T, db *sql.DB, tableName string) int {
	t.Helper()
	var count int
	err := db.QueryRow(`
		SELECT COUNT(*)
		FROM pg_inherits
		JOIN pg_class parent ON pg_inherits.inhparent = parent.oid
		JOIN pg_class child ON pg_inherits.inhrelid = child.oid
		WHERE parent.relname = $1
	`, tableName).Scan(&count)
	require.NoError(t, err)
	return count
}

// tableExists checks whether a table with the given name exists in public schema.
func tableExists(t *testing.T, db *sql.DB, tableName string) bool {
	t.Helper()
	var exists bool
	err := db.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM pg_tables
			WHERE schemaname = 'public' AND tablename = $1
		)
	`, tableName).Scan(&exists)
	require.NoError(t, err)
	return exists
}

func TestFlatRangePartitions(t *testing.T) {
	db := getE2EDB(t)
	dbp := partitioning.NewPartitions(db)
	tableName := "e2e_flat_range"

	// Create a RANGE-partitioned table
	_, err := db.Exec(fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id BIGINT,
			created_at DATE NOT NULL
		) PARTITION BY RANGE (created_at)
	`, tableName))
	require.NoError(t, err)
	t.Cleanup(func() { dropTable(t, db, tableName) })

	startDate := time.Now().AddDate(0, 0, -5)
	endDate := time.Now().AddDate(0, 0, 1)

	t.Run("create partitions", func(t *testing.T) {
		count, err := dbp.CreateMissingPartitions(tableName, startDate, endDate, true, false)
		require.NoError(t, err)
		assert.Greater(t, count, 0, "should create partitions")

		attached := countAttachedPartitions(t, db, tableName)
		assert.Equal(t, count, attached, "pg_inherits count should match created count")
	})

	t.Run("idempotent creation", func(t *testing.T) {
		count, err := dbp.CreateMissingPartitions(tableName, startDate, endDate, true, false)
		require.NoError(t, err)
		assert.Equal(t, 0, count, "second call should create 0 partitions")
	})

	t.Run("list partitions", func(t *testing.T) {
		partitions, err := dbp.ListTablePartitions(tableName)
		require.NoError(t, err)
		assert.Greater(t, len(partitions), 0, "should list partitions")
	})

	t.Run("partition stats", func(t *testing.T) {
		stats, err := dbp.GetPartitionStats(tableName)
		require.NoError(t, err)
		require.NotNil(t, stats)
		assert.Greater(t, stats.TotalPartitions, 0)
		assert.True(t, stats.OldestDate.Valid, "oldest date should be set")
		assert.True(t, stats.NewestDate.Valid, "newest date should be set")
	})

	t.Run("attached stats", func(t *testing.T) {
		stats, err := dbp.GetAttachedPartitionStats(tableName)
		require.NoError(t, err)
		require.NotNil(t, stats)
		assert.Greater(t, stats.TotalPartitions, 0)
	})

	t.Run("list attached and detached", func(t *testing.T) {
		attached, err := dbp.ListAttachedPartitions(tableName)
		require.NoError(t, err)
		assert.Greater(t, len(attached), 0)

		detached, err := dbp.ListDetachedPartitions(tableName)
		require.NoError(t, err)
		assert.Equal(t, 0, len(detached), "no partitions should be detached yet")
	})

	t.Run("detect partman format", func(t *testing.T) {
		isPartman, err := dbp.DetectPartitionFormat(tableName)
		require.NoError(t, err)
		assert.True(t, isPartman, "should detect partman format (created with usePartmanFormat=true)")
	})

	t.Run("dry run does not modify", func(t *testing.T) {
		countBefore := countAttachedPartitions(t, db, tableName)

		_, err := dbp.DetachOldPartitions(tableName, 90, true)
		require.NoError(t, err)

		countAfter := countAttachedPartitions(t, db, tableName)
		assert.Equal(t, countBefore, countAfter, "dry run should not change partition count")
	})
}

func TestFlatRangeDetachDrop(t *testing.T) {
	db := getE2EDB(t)
	dbp := partitioning.NewPartitions(db)
	tableName := "e2e_flat_detach"

	_, err := db.Exec(fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id BIGINT,
			created_at DATE NOT NULL
		) PARTITION BY RANGE (created_at)
	`, tableName))
	require.NoError(t, err)
	t.Cleanup(func() { dropTable(t, db, tableName) })

	// Create old partitions (>90 days to exceed minimum retention policy)
	oldStart := time.Now().AddDate(0, 0, -100)
	oldEnd := time.Now().AddDate(0, 0, -95)
	recentStart := time.Now().AddDate(0, 0, -2)
	recentEnd := time.Now().AddDate(0, 0, 1)

	count, err := dbp.CreateMissingPartitions(tableName, oldStart, oldEnd, true, false)
	require.NoError(t, err)
	require.Greater(t, count, 0)

	count, err = dbp.CreateMissingPartitions(tableName, recentStart, recentEnd, true, false)
	require.NoError(t, err)
	require.Greater(t, count, 0)

	totalBefore := countAttachedPartitions(t, db, tableName)

	t.Run("detach old partitions", func(t *testing.T) {
		detached, err := dbp.DetachOldPartitions(tableName, 90, false)
		require.NoError(t, err)
		assert.Greater(t, detached, 0, "should detach old partitions")

		totalAfter := countAttachedPartitions(t, db, tableName)
		assert.Less(t, totalAfter, totalBefore, "attached count should decrease")
	})

	t.Run("list detached", func(t *testing.T) {
		detached, err := dbp.ListDetachedPartitions(tableName)
		require.NoError(t, err)
		assert.Greater(t, len(detached), 0, "should find detached partitions")
	})

	t.Run("detached stats", func(t *testing.T) {
		stats, err := dbp.GetDetachedPartitionStats(tableName)
		require.NoError(t, err)
		require.NotNil(t, stats)
		assert.Greater(t, stats.TotalPartitions, 0)
	})

	t.Run("drop old detached", func(t *testing.T) {
		dropped, err := dbp.DropOldDetachedPartitions(tableName, 90, false)
		require.NoError(t, err)
		assert.Greater(t, dropped, 0, "should drop detached partitions")

		detached, err := dbp.ListDetachedPartitions(tableName)
		require.NoError(t, err)
		assert.Equal(t, 0, len(detached), "no detached partitions should remain")
	})
}

func TestNestedListRangePartitions(t *testing.T) {
	db := getE2EDB(t)
	dbp := partitioning.NewPartitions(db)
	tableName := "e2e_nested"

	_, err := db.Exec(fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id BIGINT,
			release TEXT NOT NULL,
			created_at DATE NOT NULL
		) PARTITION BY LIST (release)
	`, tableName))
	require.NoError(t, err)
	t.Cleanup(func() {
		// Drop all related tables (intermediates + leaves may be detached)
		dropTable(t, db, tableName)
		// Clean up any detached leaf tables that survived CASCADE
		rows, err := db.Query(`
			SELECT tablename FROM pg_tables
			WHERE schemaname = 'public' AND tablename LIKE 'e2e_nested_%'
		`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var name string
				if rows.Scan(&name) == nil {
					db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", name))
				}
			}
		}
	})

	releases := []string{"4.17", "4.18"}
	startDate := time.Now().AddDate(0, 0, -5)
	endDate := time.Now().AddDate(0, 0, 1)

	t.Run("create nested partitions", func(t *testing.T) {
		count, err := dbp.CreateMissingPartitionsListToRange(
			tableName, releases, startDate, endDate, "created_at", true, false,
		)
		require.NoError(t, err)
		assert.Greater(t, count, 0, "should create nested partitions")
	})

	t.Run("idempotent creation", func(t *testing.T) {
		count, err := dbp.CreateMissingPartitionsListToRange(
			tableName, releases, startDate, endDate, "created_at", true, false,
		)
		require.NoError(t, err)
		assert.Equal(t, 0, count, "second call should create 0 partitions")
	})

	t.Run("partition hierarchy", func(t *testing.T) {
		hierarchy, err := dbp.GetPartitionHierarchy(tableName)
		require.NoError(t, err)
		assert.Greater(t, len(hierarchy), 0, "should return hierarchy entries")

		// Should have intermediate partitions (level 1) and leaf partitions (level 2)
		var hasLevel1, hasLevel2 bool
		for _, h := range hierarchy {
			if h.Level == 1 {
				hasLevel1 = true
			}
			if h.Level == 2 {
				hasLevel2 = true
			}
		}
		assert.True(t, hasLevel1, "should have level 1 (LIST) partitions")
		assert.True(t, hasLevel2, "should have level 2 (RANGE) leaf partitions")
	})

	t.Run("list leaf partitions", func(t *testing.T) {
		leaves, err := dbp.ListLeafPartitions(tableName)
		require.NoError(t, err)
		assert.Greater(t, len(leaves), 0, "should return leaf partitions")

		for _, leaf := range leaves {
			assert.True(t, leaf.IsLeaf, "all returned partitions should be leaves")
		}
	})

	t.Run("nested partition stats", func(t *testing.T) {
		stats, err := dbp.GetPartitionStats(tableName)
		require.NoError(t, err)
		require.NotNil(t, stats)
		assert.Greater(t, stats.TotalPartitions, 0)
	})

	t.Run("nested attached stats", func(t *testing.T) {
		stats, err := dbp.GetAttachedPartitionStats(tableName)
		require.NoError(t, err)
		require.NotNil(t, stats)
		assert.Greater(t, stats.TotalPartitions, 0)
	})

	t.Run("nested list attached", func(t *testing.T) {
		attached, err := dbp.ListAttachedPartitions(tableName)
		require.NoError(t, err)
		assert.Greater(t, len(attached), 0)
	})

	t.Run("nested list detached initially empty", func(t *testing.T) {
		detached, err := dbp.ListDetachedPartitions(tableName)
		require.NoError(t, err)
		assert.Equal(t, 0, len(detached))
	})

	t.Run("get partition level", func(t *testing.T) {
		leaves, err := dbp.ListLeafPartitions(tableName)
		require.NoError(t, err)
		require.Greater(t, len(leaves), 0)

		level, err := dbp.GetPartitionLevel(leaves[0].TableName)
		require.NoError(t, err)
		assert.Equal(t, 2, level, "leaf partition should be at level 2")
	})
}

func TestNestedDetachDrop(t *testing.T) {
	db := getE2EDB(t)
	dbp := partitioning.NewPartitions(db)
	tableName := "e2e_nested_dd"

	_, err := db.Exec(fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id BIGINT,
			release TEXT NOT NULL,
			created_at DATE NOT NULL
		) PARTITION BY LIST (release)
	`, tableName))
	require.NoError(t, err)
	t.Cleanup(func() {
		dropTable(t, db, tableName)
		rows, err := db.Query(`
			SELECT tablename FROM pg_tables
			WHERE schemaname = 'public' AND tablename LIKE 'e2e_nested_dd_%'
		`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var name string
				if rows.Scan(&name) == nil {
					db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", name))
				}
			}
		}
	})

	releases := []string{"4.17"}

	// Create old partitions (>90 days to exceed minimum retention policy)
	oldStart := time.Now().AddDate(0, 0, -100)
	oldEnd := time.Now().AddDate(0, 0, -95)
	count, err := dbp.CreateMissingPartitionsListToRange(
		tableName, releases, oldStart, oldEnd, "created_at", true, false,
	)
	require.NoError(t, err)
	require.Greater(t, count, 0)

	// Create recent partitions
	recentStart := time.Now().AddDate(0, 0, -2)
	recentEnd := time.Now().AddDate(0, 0, 1)
	count, err = dbp.CreateMissingPartitionsListToRange(
		tableName, releases, recentStart, recentEnd, "created_at", true, false,
	)
	require.NoError(t, err)
	require.Greater(t, count, 0)

	t.Run("detach old nested partitions", func(t *testing.T) {
		detached, err := dbp.DetachOldPartitions(tableName, 90, false)
		require.NoError(t, err)
		assert.Greater(t, detached, 0, "should detach old nested leaf partitions")
	})

	t.Run("nested list detached after detach", func(t *testing.T) {
		detached, err := dbp.ListDetachedPartitions(tableName)
		require.NoError(t, err)
		assert.Greater(t, len(detached), 0, "should find detached leaf partitions")
	})

	t.Run("nested detached stats", func(t *testing.T) {
		stats, err := dbp.GetDetachedPartitionStats(tableName)
		require.NoError(t, err)
		require.NotNil(t, stats)
		assert.Greater(t, stats.TotalPartitions, 0)
	})

	t.Run("drop old nested detached", func(t *testing.T) {
		dropped, err := dbp.DropOldDetachedPartitions(tableName, 90, false)
		require.NoError(t, err)
		assert.Greater(t, dropped, 0, "should drop detached leaf partitions")
	})

	t.Run("recent partitions still attached", func(t *testing.T) {
		attached, err := dbp.ListAttachedPartitions(tableName)
		require.NoError(t, err)
		assert.Greater(t, len(attached), 0, "recent partitions should remain")
	})
}

func TestRenamePartitions(t *testing.T) {
	db := getE2EDB(t)
	dbp := partitioning.NewPartitions(db)
	tableName := "e2e_rename"

	_, err := db.Exec(fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id BIGINT,
			created_at DATE NOT NULL
		) PARTITION BY RANGE (created_at)
	`, tableName))
	require.NoError(t, err)
	t.Cleanup(func() {
		dropTable(t, db, tableName)
		// Clean up any renamed partitions that survived CASCADE
		rows, err := db.Query(`
			SELECT tablename FROM pg_tables
			WHERE schemaname = 'public' AND tablename LIKE 'e2e_rename_%'
		`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var name string
				if rows.Scan(&name) == nil {
					db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", name))
				}
			}
		}
	})

	// Create partitions in standard format (no _p prefix)
	startDate := time.Now().AddDate(0, 0, -3)
	endDate := time.Now()
	count, err := dbp.CreateMissingPartitions(tableName, startDate, endDate, false, false)
	require.NoError(t, err)
	require.Greater(t, count, 0)

	t.Run("detect standard format", func(t *testing.T) {
		isPartman, err := dbp.DetectPartitionFormat(tableName)
		require.NoError(t, err)
		assert.False(t, isPartman, "should detect standard format (no _p prefix)")
	})

	t.Run("rename to partman format", func(t *testing.T) {
		renamed, err := dbp.RenamePartitionsToMatchConfig(tableName, nil, true, false)
		require.NoError(t, err)
		assert.Greater(t, renamed, 0, "should rename partitions")

		isPartman, err := dbp.DetectPartitionFormat(tableName)
		require.NoError(t, err)
		assert.True(t, isPartman, "should now detect partman format")
	})
}

func TestNestedRenamePartitions(t *testing.T) {
	db := getE2EDB(t)
	dbp := partitioning.NewPartitions(db)
	tableName := "e2e_nested_rename"

	_, err := db.Exec(fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id BIGINT,
			release TEXT NOT NULL,
			created_at DATE NOT NULL
		) PARTITION BY LIST (release)
	`, tableName))
	require.NoError(t, err)
	t.Cleanup(func() {
		dropTable(t, db, tableName)
		rows, err := db.Query(`
			SELECT tablename FROM pg_tables
			WHERE schemaname = 'public' AND tablename LIKE 'e2e_nested_rename_%'
		`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var name string
				if rows.Scan(&name) == nil {
					db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", name))
				}
			}
		}
	})

	releases := []string{"4.17"}
	startDate := time.Now().AddDate(0, 0, -3)
	endDate := time.Now()

	// Create in standard format (no _p prefix on daily partitions)
	count, err := dbp.CreateMissingPartitionsListToRange(
		tableName, releases, startDate, endDate, "created_at", false, false,
	)
	require.NoError(t, err)
	require.Greater(t, count, 0)

	t.Run("rename nested to partman format", func(t *testing.T) {
		renamed, err := dbp.RenamePartitionsToMatchConfig(tableName, releases, true, false)
		require.NoError(t, err)
		assert.Greater(t, renamed, 0, "should rename nested partitions")
	})
}

func TestDryRunModes(t *testing.T) {
	db := getE2EDB(t)
	dbp := partitioning.NewPartitions(db)
	tableName := "e2e_dryrun"

	_, err := db.Exec(fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id BIGINT,
			created_at DATE NOT NULL
		) PARTITION BY RANGE (created_at)
	`, tableName))
	require.NoError(t, err)
	t.Cleanup(func() { dropTable(t, db, tableName) })

	// Create old partitions (>90 days to exceed minimum retention)
	oldStart := time.Now().AddDate(0, 0, -100)
	oldEnd := time.Now().AddDate(0, 0, -95)
	count, err := dbp.CreateMissingPartitions(tableName, oldStart, oldEnd, true, false)
	require.NoError(t, err)
	require.Greater(t, count, 0)

	// Create enough recent partitions so old ones are <75% of total (safety threshold)
	recentStart := time.Now().AddDate(0, 0, -30)
	recentEnd := time.Now()
	count, err = dbp.CreateMissingPartitions(tableName, recentStart, recentEnd, true, false)
	require.NoError(t, err)
	require.Greater(t, count, 0)

	countBefore := countAttachedPartitions(t, db, tableName)

	t.Run("dry run create", func(t *testing.T) {
		newStart := time.Now().AddDate(0, 0, 1)
		newEnd := time.Now().AddDate(0, 0, 5)
		count, err := dbp.CreateMissingPartitions(tableName, newStart, newEnd, true, true)
		require.NoError(t, err)
		assert.Greater(t, count, 0, "dry run should report partitions to create")

		countAfter := countAttachedPartitions(t, db, tableName)
		assert.Equal(t, countBefore, countAfter, "dry run should not create partitions")
	})

	t.Run("dry run detach", func(t *testing.T) {
		detached, err := dbp.DetachOldPartitions(tableName, 90, true)
		require.NoError(t, err)
		assert.Greater(t, detached, 0, "dry run should report partitions to detach")

		countAfter := countAttachedPartitions(t, db, tableName)
		assert.Equal(t, countBefore, countAfter, "dry run should not detach partitions")
	})

	t.Run("dry run rename", func(t *testing.T) {
		// Create in standard format, dry run rename to partman
		rTable := "e2e_dryrun_rn"
		_, err := db.Exec(fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				id BIGINT,
				created_at DATE NOT NULL
			) PARTITION BY RANGE (created_at)
		`, rTable))
		require.NoError(t, err)
		t.Cleanup(func() { dropTable(t, db, rTable) })

		start := time.Now().AddDate(0, 0, -2)
		end := time.Now()
		_, err = dbp.CreateMissingPartitions(rTable, start, end, false, false)
		require.NoError(t, err)

		renamed, err := dbp.RenamePartitionsToMatchConfig(rTable, nil, true, true)
		require.NoError(t, err)
		assert.Greater(t, renamed, 0, "dry run should report renames")

		isPartman, err := dbp.DetectPartitionFormat(rTable)
		require.NoError(t, err)
		assert.False(t, isPartman, "dry run should not actually rename")
	})
}

func TestRetentionSummary(t *testing.T) {
	db := getE2EDB(t)
	dbp := partitioning.NewPartitions(db)
	tableName := "e2e_retention"

	_, err := db.Exec(fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id BIGINT,
			created_at DATE NOT NULL
		) PARTITION BY RANGE (created_at)
	`, tableName))
	require.NoError(t, err)
	t.Cleanup(func() { dropTable(t, db, tableName) })

	// Create partitions spanning 15 days
	startDate := time.Now().AddDate(0, 0, -15)
	endDate := time.Now()
	_, err = dbp.CreateMissingPartitions(tableName, startDate, endDate, true, false)
	require.NoError(t, err)

	t.Run("retention summary with removable partitions", func(t *testing.T) {
		summary, err := dbp.GetRetentionSummary(tableName, 5, true)
		require.NoError(t, err)
		require.NotNil(t, summary)
		assert.Greater(t, summary.PartitionsToRemove, 0, "should identify partitions to remove")
		assert.Equal(t, 5, summary.RetentionDays)
	})

	t.Run("retention summary with generous retention", func(t *testing.T) {
		summary, err := dbp.GetRetentionSummary(tableName, 30, true)
		require.NoError(t, err)
		require.NotNil(t, summary)
		assert.Equal(t, 0, summary.PartitionsToRemove, "should not identify any partitions to remove")
	})

	t.Run("validate retention policy", func(t *testing.T) {
		err := dbp.ValidateRetentionPolicy(tableName, 90)
		require.NoError(t, err)
	})
}

func TestPartitionColumns(t *testing.T) {
	db := getE2EDB(t)
	dbp := partitioning.NewPartitions(db)
	tableName := "e2e_columns"

	_, err := db.Exec(fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id BIGINT,
			created_at DATE NOT NULL
		) PARTITION BY RANGE (created_at)
	`, tableName))
	require.NoError(t, err)
	t.Cleanup(func() { dropTable(t, db, tableName) })

	cols, err := dbp.GetPartitionColumns(tableName)
	require.NoError(t, err)
	assert.Contains(t, cols, "created_at", "should identify partition column")
}

func TestListPartitionedTables(t *testing.T) {
	db := getE2EDB(t)
	dbp := partitioning.NewPartitions(db)
	tableName := "e2e_list_tables"

	_, err := db.Exec(fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id BIGINT,
			created_at DATE NOT NULL
		) PARTITION BY RANGE (created_at)
	`, tableName))
	require.NoError(t, err)
	t.Cleanup(func() { dropTable(t, db, tableName) })

	tables, err := dbp.ListPartitionedTables()
	require.NoError(t, err)

	var found bool
	for _, tbl := range tables {
		if tbl.TableName == tableName {
			found = true
			assert.Equal(t, "RANGE", tbl.PartitionStrategy)
			break
		}
	}
	assert.True(t, found, "should find the test table in partitioned tables list")
}

// TestBoundsRegexDiagnostic verifies the SQL regex used in getAttachedLeafPartitions
// actually matches what pg_get_expr returns for partition bounds.
func TestBoundsRegexDiagnostic(t *testing.T) {
	db := getE2EDB(t)
	dbp := partitioning.NewPartitions(db)
	tableName := "e2e_regex_diag"

	_, err := db.Exec(fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id BIGINT,
			release TEXT NOT NULL,
			created_at DATE NOT NULL
		) PARTITION BY LIST (release)
	`, tableName))
	require.NoError(t, err)
	t.Cleanup(func() {
		dropTable(t, db, tableName)
		rows, err := db.Query(`
			SELECT tablename FROM pg_tables
			WHERE schemaname = 'public' AND tablename LIKE 'e2e_regex_diag_%'
		`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var name string
				if rows.Scan(&name) == nil {
					db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", name))
				}
			}
		}
	})

	releases := []string{"4.17"}
	startDate := time.Now().AddDate(0, 0, -3)
	endDate := time.Now()

	count, err := dbp.CreateMissingPartitionsListToRange(
		tableName, releases, startDate, endDate, "created_at", true, false,
	)
	require.NoError(t, err)
	require.Greater(t, count, 0)

	t.Run("pg_get_expr returns expected format", func(t *testing.T) {
		// Query the actual partition bounds to verify the format
		rows, err := db.Query(`
			WITH RECURSIVE partition_tree AS (
				SELECT c.oid, c.relname AS table_name, 0 AS level
				FROM pg_class c
				JOIN pg_namespace n ON n.oid = c.relnamespace
				WHERE c.relname = $1 AND n.nspname = 'public'
				UNION ALL
				SELECT child.oid, child.relname, pt.level + 1
				FROM partition_tree pt
				JOIN pg_class parent ON parent.relname = pt.table_name
				JOIN pg_inherits i ON i.inhparent = parent.oid
				JOIN pg_class child ON child.oid = i.inhrelid
			)
			SELECT
				pt.table_name,
				pg_get_expr(c.relpartbound, c.oid) AS bound_raw,
				pt.level
			FROM partition_tree pt
			JOIN pg_class c ON c.relname = pt.table_name
			WHERE pt.level > 0
			ORDER BY pt.level, pt.table_name
		`, tableName)
		require.NoError(t, err)
		defer rows.Close()

		for rows.Next() {
			var name, bounds string
			var level int
			require.NoError(t, rows.Scan(&name, &bounds, &level))
			t.Logf("level=%d name=%s bounds=%q", level, name, bounds)

			if level == 2 {
				// Leaf RANGE partition — bounds should contain FROM ('YYYY-MM-DD')
				assert.Contains(t, bounds, "FOR VALUES FROM", "leaf partition should have RANGE bounds")
				assert.Contains(t, bounds, "TO", "leaf partition should have TO clause")
			}
		}
		require.NoError(t, rows.Err())
	})

	t.Run("regex extracts date from actual bounds", func(t *testing.T) {
		// Use the exact same regex as getAttachedLeafPartitions
		rows, err := db.Query(`
			WITH RECURSIVE partition_tree AS (
				SELECT c.oid, c.relname AS table_name, 0 AS level
				FROM pg_class c
				JOIN pg_namespace n ON n.oid = c.relnamespace
				WHERE c.relname = $1 AND n.nspname = 'public'
				UNION ALL
				SELECT child.oid, child.relname, pt.level + 1
				FROM partition_tree pt
				JOIN pg_class parent ON parent.relname = pt.table_name
				JOIN pg_inherits i ON i.inhparent = parent.oid
				JOIN pg_class child ON child.oid = i.inhrelid
			)
			SELECT
				pt.table_name,
				pg_get_expr(c.relpartbound, c.oid) AS bound_raw,
				substring(pg_get_expr(c.relpartbound, c.oid) FROM 'FROM \(''(\d{4}-\d{2}-\d{2})''') AS extracted_date
			FROM partition_tree pt
			JOIN pg_class c ON c.relname = pt.table_name
			WHERE NOT EXISTS (SELECT 1 FROM pg_partitioned_table pp WHERE pp.partrelid = c.oid)
			AND pt.level > 0
			ORDER BY pt.table_name
		`, tableName)
		require.NoError(t, err)
		defer rows.Close()

		leafCount := 0
		for rows.Next() {
			var name, bounds string
			var extractedDate sql.NullString
			require.NoError(t, rows.Scan(&name, &bounds, &extractedDate))
			t.Logf("name=%s bounds=%q extracted_date=%v (valid=%v)", name, bounds, extractedDate.String, extractedDate.Valid)

			assert.True(t, extractedDate.Valid, "regex should extract date from bounds for partition %s (bounds: %s)", name, bounds)
			if extractedDate.Valid {
				_, err := time.Parse("2006-01-02", extractedDate.String)
				assert.NoError(t, err, "extracted date should parse as YYYY-MM-DD for partition %s", name)
			}
			leafCount++
		}
		require.NoError(t, rows.Err())
		assert.Greater(t, leafCount, 0, "should have leaf partitions to test")
	})

	t.Run("getAttachedLeafPartitions returns correct dates", func(t *testing.T) {
		attached, err := dbp.ListAttachedPartitions(tableName)
		require.NoError(t, err)
		require.Greater(t, len(attached), 0, "should have attached partitions")

		for _, p := range attached {
			assert.False(t, p.PartitionDate.IsZero(), "partition %s should have non-zero date", p.TableName)
			// Verify the date is within the expected range
			assert.True(t, !p.PartitionDate.Before(startDate.AddDate(0, 0, -1)),
				"partition %s date %s should not be before start date %s",
				p.TableName, p.PartitionDate.Format("2006-01-02"), startDate.Format("2006-01-02"))
			assert.True(t, !p.PartitionDate.After(endDate.AddDate(0, 0, 1)),
				"partition %s date %s should not be after end date %s",
				p.TableName, p.PartitionDate.Format("2006-01-02"), endDate.Format("2006-01-02"))
			t.Logf("partition=%s date=%s age=%d", p.TableName, p.PartitionDate.Format("2006-01-02"), p.Age)
		}
	})
}

// TestNestedRetentionPrecision verifies that DetachOldPartitions on a nested table
// only detaches partitions older than the retention period and never touches recent ones.
func TestNestedRetentionPrecision(t *testing.T) {
	db := getE2EDB(t)
	dbp := partitioning.NewPartitions(db)
	tableName := "e2e_retention_prec"

	_, err := db.Exec(fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id BIGINT,
			release TEXT NOT NULL,
			created_at DATE NOT NULL
		) PARTITION BY LIST (release)
	`, tableName))
	require.NoError(t, err)
	t.Cleanup(func() {
		dropTable(t, db, tableName)
		rows, err := db.Query(`
			SELECT tablename FROM pg_tables
			WHERE schemaname = 'public' AND tablename LIKE 'e2e_retention_prec_%'
		`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var name string
				if rows.Scan(&name) == nil {
					db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", name))
				}
			}
		}
	})

	releases := []string{"4.17", "4.18"}

	// Create partitions at 3 distinct age groups:
	// 1. Old: -100 days (should be detached with 90-day retention)
	// 2. Medium: -50 days (should NOT be detached with 90-day retention)
	// 3. Recent: -5 days (should NOT be detached)
	oldDate := time.Now().AddDate(0, 0, -100)
	mediumDate := time.Now().AddDate(0, 0, -50)
	recentDate := time.Now().AddDate(0, 0, -5)

	for _, label := range []struct {
		start time.Time
		end   time.Time
		name  string
	}{
		{oldDate, oldDate.AddDate(0, 0, 2), "old"},
		{mediumDate, mediumDate.AddDate(0, 0, 2), "medium"},
		{recentDate, recentDate.AddDate(0, 0, 2), "recent"},
	} {
		count, err := dbp.CreateMissingPartitionsListToRange(
			tableName, releases, label.start, label.end, "created_at", true, false,
		)
		require.NoError(t, err, "creating %s partitions", label.name)
		require.Greater(t, count, 0, "should create %s partitions", label.name)
	}

	// Record partition state before detach
	attachedBefore, err := dbp.ListAttachedPartitions(tableName)
	require.NoError(t, err)
	totalBefore := len(attachedBefore)
	t.Logf("total attached before detach: %d", totalBefore)

	// Log all partitions and their dates
	for _, p := range attachedBefore {
		t.Logf("  before: %s date=%s age=%d", p.TableName, p.PartitionDate.Format("2006-01-02"), p.Age)
	}

	// Count how many are actually old (>90 days)
	cutoff := time.Now().AddDate(0, 0, -90)
	expectedOld := 0
	for _, p := range attachedBefore {
		if p.PartitionDate.Before(cutoff) {
			expectedOld++
		}
	}
	t.Logf("partitions older than 90 days: %d", expectedOld)
	require.Greater(t, expectedOld, 0, "should have some old partitions")

	// Detach with 90-day retention
	detached, err := dbp.DetachOldPartitions(tableName, 90, false)
	require.NoError(t, err)
	assert.Equal(t, expectedOld, detached, "should only detach partitions older than 90 days")

	// Verify remaining attached partitions
	attachedAfter, err := dbp.ListAttachedPartitions(tableName)
	require.NoError(t, err)

	for _, p := range attachedAfter {
		t.Logf("  after: %s date=%s age=%d", p.TableName, p.PartitionDate.Format("2006-01-02"), p.Age)
		assert.True(t, !p.PartitionDate.Before(cutoff),
			"partition %s (date=%s) should NOT have been detached — it is within the 90-day retention window",
			p.TableName, p.PartitionDate.Format("2006-01-02"))
	}

	expectedRemaining := totalBefore - expectedOld
	assert.Equal(t, expectedRemaining, len(attachedAfter),
		"remaining attached partitions should equal total minus detached")

	// Specifically verify medium (50-day) and recent (5-day) partitions survive
	mediumCutoff := mediumDate.AddDate(0, 0, -1)
	mediumEnd := mediumDate.AddDate(0, 0, 3)
	var mediumSurvivors int
	for _, p := range attachedAfter {
		if p.PartitionDate.After(mediumCutoff) && p.PartitionDate.Before(mediumEnd) {
			mediumSurvivors++
		}
	}
	assert.Greater(t, mediumSurvivors, 0, "50-day-old partitions should NOT be detached")

	recentCutoff := recentDate.AddDate(0, 0, -1)
	recentEnd := recentDate.AddDate(0, 0, 3)
	var recentSurvivors int
	for _, p := range attachedAfter {
		if p.PartitionDate.After(recentCutoff) && p.PartitionDate.Before(recentEnd) {
			recentSurvivors++
		}
	}
	assert.Greater(t, recentSurvivors, 0, "5-day-old partitions should NOT be detached")
}

// TestTimestampPartitionBounds verifies that the date extraction regex works
// when the partition column is TIMESTAMP WITH TIME ZONE (not DATE).
// pg_get_expr returns 'FROM (”2026-04-11 00:00:00+00”)' for timestamps,
// vs 'FROM (”2026-04-11”)' for dates. The regex must handle both.
func TestTimestampPartitionBounds(t *testing.T) {
	db := getE2EDB(t)
	dbp := partitioning.NewPartitions(db)

	t.Run("flat range with timestamp column", func(t *testing.T) {
		tableName := "e2e_ts_flat"
		_, err := db.Exec(fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				id BIGINT,
				created_at TIMESTAMP WITH TIME ZONE NOT NULL
			) PARTITION BY RANGE (created_at)
		`, tableName))
		require.NoError(t, err)
		t.Cleanup(func() { dropTable(t, db, tableName) })

		startDate := time.Now().AddDate(0, 0, -5)
		endDate := time.Now().AddDate(0, 0, 1)

		count, err := dbp.CreateMissingPartitions(tableName, startDate, endDate, true, false)
		require.NoError(t, err)
		require.Greater(t, count, 0)

		partitions, err := dbp.ListTablePartitions(tableName)
		require.NoError(t, err)
		assert.Equal(t, count, len(partitions), "should list all created partitions")

		for _, p := range partitions {
			assert.False(t, p.PartitionDate.IsZero(),
				"partition %s should have non-zero date (bounds extraction must work with timestamp format)", p.TableName)
			t.Logf("partition=%s date=%s age=%d", p.TableName, p.PartitionDate.Format("2006-01-02"), p.Age)
		}
	})

	t.Run("nested list-range with timestamp column", func(t *testing.T) {
		tableName := "e2e_ts_nested"
		_, err := db.Exec(fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				id BIGINT,
				release TEXT NOT NULL,
				created_at TIMESTAMP WITH TIME ZONE NOT NULL
			) PARTITION BY LIST (release)
		`, tableName))
		require.NoError(t, err)
		t.Cleanup(func() {
			dropTable(t, db, tableName)
			rows, err := db.Query(`
				SELECT tablename FROM pg_tables
				WHERE schemaname = 'public' AND tablename LIKE 'e2e_ts_nested_%'
			`)
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var name string
					if rows.Scan(&name) == nil {
						db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", name))
					}
				}
			}
		})

		releases := []string{"4.17", "4.18"}
		startDate := time.Now().AddDate(0, 0, -5)
		endDate := time.Now().AddDate(0, 0, 1)

		count, err := dbp.CreateMissingPartitionsListToRange(
			tableName, releases, startDate, endDate, "created_at", true, false,
		)
		require.NoError(t, err)
		require.Greater(t, count, 0)

		// Verify pg_get_expr returns timestamp format and regex still extracts dates
		var boundsExample string
		err = db.QueryRow(`
			WITH RECURSIVE partition_tree AS (
				SELECT c.oid, c.relname AS table_name, 0 AS level
				FROM pg_class c
				JOIN pg_namespace n ON n.oid = c.relnamespace
				WHERE c.relname = $1 AND n.nspname = 'public'
				UNION ALL
				SELECT child.oid, child.relname, pt.level + 1
				FROM partition_tree pt
				JOIN pg_class parent ON parent.relname = pt.table_name
				JOIN pg_inherits i ON i.inhparent = parent.oid
				JOIN pg_class child ON child.oid = i.inhrelid
			)
			SELECT pg_get_expr(c.relpartbound, c.oid)
			FROM partition_tree pt
			JOIN pg_class c ON c.relname = pt.table_name
			WHERE NOT EXISTS (SELECT 1 FROM pg_partitioned_table pp WHERE pp.partrelid = c.oid)
			AND pt.level > 0
			LIMIT 1
		`, tableName).Scan(&boundsExample)
		require.NoError(t, err)
		t.Logf("actual pg_get_expr output for timestamp column: %q", boundsExample)

		// ListAttachedPartitions uses getAttachedLeafPartitions which uses the regex
		attached, err := dbp.ListAttachedPartitions(tableName)
		require.NoError(t, err)
		require.Greater(t, len(attached), 0, "should find attached leaf partitions")

		for _, p := range attached {
			assert.False(t, p.PartitionDate.IsZero(),
				"partition %s should have non-zero date extracted from timestamp bounds", p.TableName)
			t.Logf("partition=%s date=%s age=%d", p.TableName, p.PartitionDate.Format("2006-01-02"), p.Age)
		}
	})

	t.Run("nested timestamp retention does not detach recent", func(t *testing.T) {
		tableName := "e2e_ts_retention"
		_, err := db.Exec(fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				id BIGINT,
				release TEXT NOT NULL,
				created_at TIMESTAMP WITH TIME ZONE NOT NULL
			) PARTITION BY LIST (release)
		`, tableName))
		require.NoError(t, err)
		t.Cleanup(func() {
			dropTable(t, db, tableName)
			rows, err := db.Query(`
				SELECT tablename FROM pg_tables
				WHERE schemaname = 'public' AND tablename LIKE 'e2e_ts_retention_%'
			`)
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var name string
					if rows.Scan(&name) == nil {
						db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", name))
					}
				}
			}
		})

		releases := []string{"4.18"}

		// Old partitions (>90 days)
		oldStart := time.Now().AddDate(0, 0, -100)
		oldEnd := oldStart.AddDate(0, 0, 2)
		count, err := dbp.CreateMissingPartitionsListToRange(
			tableName, releases, oldStart, oldEnd, "created_at", true, false,
		)
		require.NoError(t, err)
		require.Greater(t, count, 0)

		// Recent partitions (50 days) — these must NOT be detached
		recentStart := time.Now().AddDate(0, 0, -50)
		recentEnd := recentStart.AddDate(0, 0, 2)
		count, err = dbp.CreateMissingPartitionsListToRange(
			tableName, releases, recentStart, recentEnd, "created_at", true, false,
		)
		require.NoError(t, err)
		require.Greater(t, count, 0)

		// Very recent partitions
		veryRecentStart := time.Now().AddDate(0, 0, -3)
		veryRecentEnd := time.Now().AddDate(0, 0, 1)
		count, err = dbp.CreateMissingPartitionsListToRange(
			tableName, releases, veryRecentStart, veryRecentEnd, "created_at", true, false,
		)
		require.NoError(t, err)
		require.Greater(t, count, 0)

		attachedBefore, err := dbp.ListAttachedPartitions(tableName)
		require.NoError(t, err)
		t.Logf("attached before detach: %d", len(attachedBefore))
		for _, p := range attachedBefore {
			t.Logf("  %s date=%s age=%d", p.TableName, p.PartitionDate.Format("2006-01-02"), p.Age)
		}

		// Count expected old partitions
		cutoff := time.Now().AddDate(0, 0, -90)
		expectedOld := 0
		for _, p := range attachedBefore {
			if p.PartitionDate.Before(cutoff) {
				expectedOld++
			}
		}
		require.Greater(t, expectedOld, 0, "should have old partitions")

		// Detach with 90-day retention
		detached, err := dbp.DetachOldPartitions(tableName, 90, false)
		require.NoError(t, err)
		assert.Equal(t, expectedOld, detached,
			"should only detach old partitions, not recent ones (timestamp bounds)")

		attachedAfter, err := dbp.ListAttachedPartitions(tableName)
		require.NoError(t, err)
		t.Logf("attached after detach: %d", len(attachedAfter))

		for _, p := range attachedAfter {
			assert.False(t, p.PartitionDate.Before(cutoff),
				"partition %s (date=%s) should NOT have been detached — within 90-day window",
				p.TableName, p.PartitionDate.Format("2006-01-02"))
		}

		expectedRemaining := len(attachedBefore) - expectedOld
		assert.Equal(t, expectedRemaining, len(attachedAfter),
			"only old partitions should have been removed")
	})
}

// TestFlatTimestampLifecycle mimics the existing test_analysis_by_job_by_dates table:
// a flat RANGE-partitioned table with a TIMESTAMP WITH TIME ZONE column.
// It exercises the full partition lifecycle: create, detach old, drop old detached.
func TestFlatTimestampLifecycle(t *testing.T) {
	db := getE2EDB(t)
	dbp := partitioning.NewPartitions(db)
	tableName := "e2e_ts_lifecycle"

	_, err := db.Exec(fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			date TIMESTAMP WITH TIME ZONE NOT NULL,
			test_id BIGINT,
			release TEXT NOT NULL,
			job_name TEXT,
			test_name TEXT,
			runs BIGINT,
			passes BIGINT,
			flakes BIGINT,
			failures BIGINT
		) PARTITION BY RANGE (date)
	`, tableName))
	require.NoError(t, err)
	t.Cleanup(func() {
		dropTable(t, db, tableName)
		rows, err := db.Query(`
			SELECT tablename FROM pg_tables
			WHERE schemaname = 'public' AND tablename LIKE 'e2e_ts_lifecycle_%'
		`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var name string
				if rows.Scan(&name) == nil {
					db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", name))
				}
			}
		}
	})

	// Seed partitions at three age tiers:
	// - very old (115 days): will be detached AND dropped
	// - old (105 days): will be detached but NOT dropped
	// - recent (today): should survive untouched
	veryOldStart := time.Now().AddDate(0, 0, -115)
	veryOldEnd := veryOldStart.AddDate(0, 0, 2)
	oldStart := time.Now().AddDate(0, 0, -105)
	oldEnd := oldStart.AddDate(0, 0, 2)
	recentStart := time.Now().AddDate(0, 0, -3)
	recentEnd := recentStart.AddDate(0, 0, 2)

	for _, tier := range []struct {
		start, end time.Time
		label      string
	}{
		{veryOldStart, veryOldEnd, "very old (115d)"},
		{oldStart, oldEnd, "old (105d)"},
		{recentStart, recentEnd, "recent"},
	} {
		count, err := dbp.CreateMissingPartitions(tableName, tier.start, tier.end, true, false)
		require.NoError(t, err, "creating %s partitions", tier.label)
		require.Greater(t, count, 0, "should create %s partitions", tier.label)
		t.Logf("created %d %s partitions", count, tier.label)
	}

	t.Run("add partitions for today+2", func(t *testing.T) {
		start := time.Now()
		end := time.Now().AddDate(0, 0, 3)
		count, err := dbp.CreateMissingPartitions(tableName, start, end, true, false)
		require.NoError(t, err)
		assert.Greater(t, count, 0, "should create new partitions for today through today+2")
		t.Logf("created %d new partitions (today + 2 days)", count)

		// Verify via ListAttachedPartitions that dates are extracted from timestamp bounds
		attached, err := dbp.ListAttachedPartitions(tableName)
		require.NoError(t, err)
		for _, p := range attached {
			assert.False(t, p.PartitionDate.IsZero(),
				"partition %s should have a non-zero date (timestamp bounds regex must work)", p.TableName)
			assert.Greater(t, p.Age, -4,
				"partition %s age (%d) should be reasonable", p.TableName, p.Age)
			t.Logf("  %s date=%s age=%d", p.TableName, p.PartitionDate.Format("2006-01-02"), p.Age)
		}
	})

	t.Run("detach partitions older than 100 days", func(t *testing.T) {
		attachedBefore, err := dbp.ListAttachedPartitions(tableName)
		require.NoError(t, err)

		cutoff := time.Now().AddDate(0, 0, -100)
		expectedDetach := 0
		for _, p := range attachedBefore {
			if p.PartitionDate.Before(cutoff) {
				expectedDetach++
			}
		}
		require.Greater(t, expectedDetach, 0, "should have partitions older than 100 days")

		detached, err := dbp.DetachOldPartitions(tableName, 100, false)
		require.NoError(t, err)
		assert.Equal(t, expectedDetach, detached,
			"should detach exactly the partitions older than 100 days")
		t.Logf("detached %d partitions (expected %d)", detached, expectedDetach)

		// Verify remaining attached partitions are all within the retention window
		attachedAfter, err := dbp.ListAttachedPartitions(tableName)
		require.NoError(t, err)
		for _, p := range attachedAfter {
			assert.False(t, p.PartitionDate.Before(cutoff),
				"partition %s (date=%s, age=%d) should not be attached — it predates the 100-day cutoff",
				p.TableName, p.PartitionDate.Format("2006-01-02"), p.Age)
		}

		// Verify detached partitions exist
		detachedList, err := dbp.ListDetachedPartitions(tableName)
		require.NoError(t, err)
		assert.Equal(t, expectedDetach, len(detachedList),
			"all detached partitions should still exist as standalone tables")
	})

	t.Run("drop detached partitions older than 110 days", func(t *testing.T) {
		detachedBefore, err := dbp.ListDetachedPartitions(tableName)
		require.NoError(t, err)
		require.Greater(t, len(detachedBefore), 0, "should have detached partitions")

		// Count how many detached partitions are older than 110 days (the very old tier)
		dropCutoff := time.Now().AddDate(0, 0, -110)
		expectedDrop := 0
		expectedSurvive := 0
		for _, p := range detachedBefore {
			if p.PartitionDate.Before(dropCutoff) {
				expectedDrop++
				t.Logf("  will drop: %s date=%s age=%d", p.TableName, p.PartitionDate.Format("2006-01-02"), p.Age)
			} else {
				expectedSurvive++
				t.Logf("  will keep: %s date=%s age=%d", p.TableName, p.PartitionDate.Format("2006-01-02"), p.Age)
			}
		}
		require.Greater(t, expectedDrop, 0, "should have detached partitions older than 110 days")
		require.Greater(t, expectedSurvive, 0, "should have detached partitions newer than 110 days (the 105-day tier)")

		dropped, err := dbp.DropOldDetachedPartitions(tableName, 110, false)
		require.NoError(t, err)
		assert.Equal(t, expectedDrop, dropped,
			"should drop only detached partitions older than 110 days")
		t.Logf("dropped %d detached partitions (expected %d)", dropped, expectedDrop)

		// Verify the 105-day-old detached partitions still exist
		detachedAfter, err := dbp.ListDetachedPartitions(tableName)
		require.NoError(t, err)
		assert.Equal(t, expectedSurvive, len(detachedAfter),
			"detached partitions between 100-110 days should survive the drop")

		// Verify the dropped tables no longer exist
		for _, p := range detachedBefore {
			if p.PartitionDate.Before(dropCutoff) {
				assert.False(t, tableExists(t, db, p.TableName),
					"dropped partition %s should no longer exist", p.TableName)
			}
		}
	})

	t.Run("recent partitions still attached", func(t *testing.T) {
		attached, err := dbp.ListAttachedPartitions(tableName)
		require.NoError(t, err)
		assert.Greater(t, len(attached), 0, "recent partitions should remain attached")

		// All remaining attached partitions should be within the 100-day window
		cutoff := time.Now().AddDate(0, 0, -100)
		for _, p := range attached {
			assert.False(t, p.PartitionDate.Before(cutoff),
				"attached partition %s (date=%s) should be within retention window",
				p.TableName, p.PartitionDate.Format("2006-01-02"))
		}

		// Should have partitions at today+2
		tomorrow := time.Now().AddDate(0, 0, 1)
		var hasFuture bool
		for _, p := range attached {
			if !p.PartitionDate.Before(tomorrow) {
				hasFuture = true
				break
			}
		}
		assert.True(t, hasFuture, "should still have future partitions (today+2)")
	})
}
