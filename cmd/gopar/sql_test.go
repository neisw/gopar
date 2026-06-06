package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// getTestDBClient returns a database client for testing.
// It reads the DSN from the GOPAR_TEST_DSN environment variable.
// If the variable is not set, the test is skipped.
func getTestDBClient(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("GOPAR_TEST_DSN")
	if dsn == "" {
		t.Skip("skipping: set GOPAR_TEST_DSN environment variable to run")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("Failed to connect to database: %v", err)
	}

	if err := db.Ping(); err != nil {
		t.Fatalf("Failed to ping database: %v", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Logf("failed to close database connection: %v", err)
		}
	})

	return db
}

// loadSpecsFromJSONForTest loads specifications from a JSON file for testing
// This is a test helper that wraps the loadSpecsFromJSON function
func loadSpecsFromJSONForTest(t *testing.T, filename string, v interface{}) {
	t.Helper()
	specPath := filepath.Join("config", "specs", filename)
	if err := loadSpecsFromJSON(specPath, v); err != nil {
		t.Fatalf("Failed to load spec file: %v", err)
	}
}

// Test_CreatePartitionIndexes creates indexes on partitioned tables using CREATE INDEX CONCURRENTLY
// to avoid locking production tables. Each statement runs outside a transaction as required by CONCURRENTLY.
//
// To run: set GOPAR_TEST_DSN environment variable, then:
//
//	GOPAR_TEST_DSN="host=localhost user=postgres..." go test -run Test_CreatePartitionIndexes -v -timeout 0
func Test_CreatePartitionIndexes(t *testing.T) {
	sqlDB := getTestDBClient(t)

	// Define index specs
	type indexSpec struct {
		Name  string `json:"name"`
		Query string `json:"query"`
	}

	var specs []indexSpec
	loadSpecsFromJSONForTest(t, "example_index_specs.json", &specs)

	for _, spec := range specs {
		t.Run(spec.Name, func(t *testing.T) {
			start := time.Now()
			t.Logf("Creating index %s...", spec.Name)
			_, err := sqlDB.Exec(spec.Query)
			if err != nil {
				t.Fatalf("Failed to create index %s: %v", spec.Name, err)
			}
			t.Logf("Index %s created in %v", spec.Name, time.Since(start))
		})
	}
}

// Test_BackfillDenormalizedColumns demonstrates backfilling denormalized columns
// in batches to avoid long-running locks. The batch size is configurable.
//
// To run: set GOPAR_TEST_DSN environment variable, then:
//
//	GOPAR_TEST_DSN="host=localhost user=postgres..." go test -run Test_BackfillDenormalizedColumns -v -timeout 0
func Test_BackfillDenormalizedColumns(t *testing.T) {
	const backfillBatchSize = 10_000
	sqlDB := getTestDBClient(t)

	// Define backfill specs
	type backfillSpec struct {
		TableName   string `json:"tableName"`
		Description string `json:"description"`
		UpdateQuery string `json:"updateQuery"`
	}

	var specs []backfillSpec
	loadSpecsFromJSONForTest(t, "example_backfill_specs.json", &specs)

	for _, spec := range specs {
		t.Run(spec.TableName, func(t *testing.T) {
			start := time.Now()
			totalUpdated := 0

			t.Logf("Starting backfill for %s: %s", spec.TableName, spec.Description)

			for {
				result, err := sqlDB.Exec(spec.UpdateQuery, backfillBatchSize)
				if err != nil {
					t.Fatalf("Failed to backfill %s: %v", spec.TableName, err)
				}

				rowsAffected, err := result.RowsAffected()
				if err != nil {
					t.Fatalf("Failed to get rows affected: %v", err)
				}

				totalUpdated += int(rowsAffected)
				t.Logf("  Batch updated %d rows (total: %d)", rowsAffected, totalUpdated)

				if rowsAffected < int64(backfillBatchSize) {
					break
				}
			}

			t.Logf("Backfill for %s completed in %v (total rows: %d)",
				spec.TableName, time.Since(start), totalUpdated)
		})
	}
}

// Test_ExecuteSQLSpecs demonstrates how to execute a list of SQL specs
// similar to the CLI command
//
// To run: set GOPAR_TEST_DSN environment variable, then:
//
//	GOPAR_TEST_DSN="host=localhost user=postgres..." go test -run Test_ExecuteSQLSpecs -v -timeout 0
func Test_ExecuteSQLSpecs(t *testing.T) {
	sqlDB := getTestDBClient(t)

	// Load SQL specs from JSON
	var specs []SQLSpec
	loadSpecsFromJSONForTest(t, "example_sql_specs.json", &specs)

	for _, spec := range specs {
		t.Run(spec.Name, func(t *testing.T) {
			err := executeSpec(sqlDB, spec)
			if err != nil {
				t.Fatalf("Failed to execute spec %s: %v", spec.Name, err)
			}
		})
	}
}
