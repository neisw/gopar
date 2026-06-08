package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"

	_ "github.com/lib/pq"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

// SQLSpec defines a SQL statement to execute
type SQLSpec struct {
	Name        string
	Description string
	Query       string
	// Concurrent indicates if the query should run outside a transaction
	// (required for CREATE INDEX CONCURRENTLY)
	Concurrent bool
	// BatchSize indicates the spec should be executed in batches (for backfills).
	// When BatchSize > 0, the query will be executed repeatedly with $1 parameter
	// until no rows are affected. This is used for batch backfill operations.
	BatchSize int64
	// ResultType controls how the query is executed:
	//   "" or "exec" (default) - uses db.Exec, suitable for DDL/DML statements
	//   "query" - uses db.Query and prints returned rows, suitable for SELECT validation queries
	ResultType string
}

var (
	dsn        string
	dryRun     bool
	specsToRun []string
	specFile   string
	// customSpecs allows tests to inject custom SQL specs
	customSpecs []SQLSpec
)

// loadSpecsFromJSON loads specifications from a JSON file
func loadSpecsFromJSON(filename string, v interface{}) error {
	data, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("failed to read spec file %s: %w", filename, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("failed to unmarshal spec file %s: %w", filename, err)
	}
	return nil
}

// NewSQLCommand creates the SQL command for executing SQL specs
func NewSQLCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sql",
		Short: "Execute SQL migration/index specs",
		Long: `Execute predefined SQL specifications for migrations, index creation, and other operations.
Specs can be defined in code and executed via the CLI. Use --dry-run to preview without executing.`,
		RunE: runSQL,
	}

	cmd.Flags().StringVar(&dsn, "dsn", "", "PostgreSQL DSN (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print SQL without executing")
	cmd.Flags().StringSliceVar(&specsToRun, "specs", []string{}, "Specific specs to run (comma-separated). If empty, runs all specs")
	cmd.Flags().StringVar(&specFile, "spec-file", "", "Path to JSON file containing SQL specs to execute")
	cmd.MarkFlagRequired("dsn")

	return cmd
}

func runSQL(cmd *cobra.Command, args []string) error {
	// Use custom specs if provided (for testing), otherwise use defaults or file
	var specs []SQLSpec
	if len(customSpecs) > 0 {
		specs = customSpecs
	} else if specFile != "" {
		// Load specs from JSON file
		if err := loadSpecsFromJSON(specFile, &specs); err != nil {
			return err
		}
		if len(specs) == 0 {
			return fmt.Errorf("no specs found in file %s", specFile)
		}
		log.Infof("Loaded %d specs from %s", len(specs), specFile)
	} else {
		specs = getDefaultSpecs()
	}

	if dryRun {
		log.Info("DRY RUN MODE - SQL will not be executed")
		for _, spec := range specs {
			if shouldRunSpec(spec.Name) {
				fmt.Printf("\n--- Spec: %s ---\n", spec.Name)
				if spec.Description != "" {
					fmt.Printf("Description: %s\n", spec.Description)
				}
				fmt.Printf("Concurrent: %v\n", spec.Concurrent)
				if spec.ResultType != "" {
					fmt.Printf("ResultType: %s\n", spec.ResultType)
				}
				fmt.Printf("Query:\n%s\n", spec.Query)
			}
		}
		return nil
	}

	// Connect to database
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}

	// Execute specs
	for _, spec := range specs {
		if !shouldRunSpec(spec.Name) {
			continue
		}

		if err := executeSpec(db, spec); err != nil {
			return fmt.Errorf("failed to execute spec %s: %w", spec.Name, err)
		}
	}

	log.Info("All SQL specs executed successfully")
	return nil
}

func shouldRunSpec(name string) bool {
	if len(specsToRun) == 0 {
		return true
	}
	for _, s := range specsToRun {
		if s == name {
			return true
		}
	}
	return false
}

func executeSpec(db *sql.DB, spec SQLSpec) error {
	start := time.Now()
	log.Infof("Executing spec: %s", spec.Name)
	if spec.Description != "" {
		log.Infof("  Description: %s", spec.Description)
	}

	if spec.ResultType == "query" {
		return executeQuerySpec(db, spec, start)
	}

	// If BatchSize is set, execute in batches
	if spec.BatchSize > 0 {
		return executeBatchSpec(db, spec, start)
	}

	// Single execution (no batching)
	var err error
	if spec.Concurrent {
		// Run outside transaction for CONCURRENTLY operations
		_, err = db.Exec(spec.Query)
	} else {
		// Run in transaction
		tx, txErr := db.Begin()
		if txErr != nil {
			return fmt.Errorf("failed to begin transaction: %w", txErr)
		}
		defer tx.Rollback()

		_, err = tx.Exec(spec.Query)
		if err == nil {
			err = tx.Commit()
		}
	}

	if err != nil {
		return err
	}

	log.Infof("Spec %s completed in %v", spec.Name, time.Since(start))
	return nil
}

// executeQuerySpec runs a SELECT query and prints the result rows as a table
func executeQuerySpec(db *sql.DB, spec SQLSpec, start time.Time) error {
	rows, err := db.Query(spec.Query)
	if err != nil {
		return fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("failed to get columns: %w", err)
	}

	// Collect all rows into string slices
	var results [][]string
	for rows.Next() {
		values := make([]interface{}, len(columns))
		ptrs := make([]interface{}, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}

		if err := rows.Scan(ptrs...); err != nil {
			return fmt.Errorf("failed to scan row: %w", err)
		}

		row := make([]string, len(columns))
		for i, v := range values {
			if v == nil {
				row[i] = "NULL"
			} else {
				row[i] = fmt.Sprintf("%v", v)
			}
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating rows: %w", err)
	}

	if len(results) == 0 {
		log.Infof("Spec %s: query returned 0 rows (no mismatches) in %v", spec.Name, time.Since(start))
		return nil
	}

	// Compute column widths
	widths := make([]int, len(columns))
	for i, col := range columns {
		widths[i] = len(col)
	}
	for _, row := range results {
		for i, val := range row {
			if len(val) > widths[i] {
				widths[i] = len(val)
			}
		}
	}

	// Print header
	fmt.Printf("\n--- %s: %d rows returned ---\n", spec.Name, len(results))
	for i, col := range columns {
		if i > 0 {
			fmt.Print(" | ")
		}
		fmt.Printf("%-*s", widths[i], col)
	}
	fmt.Println()
	for i, w := range widths {
		if i > 0 {
			fmt.Print("-+-")
		}
		for j := 0; j < w; j++ {
			fmt.Print("-")
		}
	}
	fmt.Println()

	// Print rows
	for _, row := range results {
		for i, val := range row {
			if i > 0 {
				fmt.Print(" | ")
			}
			fmt.Printf("%-*s", widths[i], val)
		}
		fmt.Println()
	}
	fmt.Println()

	log.Infof("Spec %s completed in %v (%d rows)", spec.Name, time.Since(start), len(results))
	return nil
}

// executeBatchSpec executes a spec in batches until no more rows are affected
func executeBatchSpec(db *sql.DB, spec SQLSpec, start time.Time) error {
	totalUpdated := int64(0)
	batchNum := 0

	log.Infof("Executing spec %s in batches (batch size: %d)", spec.Name, spec.BatchSize)

	for {
		batchNum++
		batchStart := time.Now()

		var result sql.Result
		var err error

		if spec.Concurrent {
			// Run outside transaction for CONCURRENTLY operations
			result, err = db.Exec(spec.Query, spec.BatchSize)
		} else {
			// Run in transaction
			tx, txErr := db.Begin()
			if txErr != nil {
				return fmt.Errorf("batch %d failed to begin transaction: %w", batchNum, txErr)
			}

			result, err = tx.Exec(spec.Query, spec.BatchSize)
			if err == nil {
				err = tx.Commit()
			}
			if err != nil {
				tx.Rollback()
			}
		}

		if err != nil {
			return fmt.Errorf("batch %d failed for %s: %w", batchNum, spec.Name, err)
		}

		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("batch %d failed to get rows affected: %w", batchNum, err)
		}

		totalUpdated += rowsAffected
		log.Infof("%s batch %d: %d rows updated (%d total) in %v",
			spec.Name, batchNum, rowsAffected, totalUpdated, time.Since(batchStart))

		// If no rows were affected, we're done
		if rowsAffected == 0 {
			break
		}
	}

	log.Infof("%s: complete — %d total rows updated across %d batches in %v",
		spec.Name, totalUpdated, batchNum, time.Since(start))
	return nil
}

// getDefaultSpecs returns the default SQL specifications
// Modify this function to add your own SQL specs
func getDefaultSpecs() []SQLSpec {
	return []SQLSpec{
		{
			Name:        "example_index_concurrent",
			Description: "Example of creating an index concurrently",
			Concurrent:  true,
			Query: `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_example
				ON your_table (column_name)`,
		},
		{
			Name:        "example_composite_index",
			Description: "Example of creating a composite index",
			Concurrent:  true,
			Query: `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_example_composite
				ON your_table (column1, column2)`,
		},
	}
}
