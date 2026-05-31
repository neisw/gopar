package e2e

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/neisw/gopar/partitioning"
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
