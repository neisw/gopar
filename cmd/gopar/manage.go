package main

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"
	"github.com/openshift-eng/gopar/partitioning"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

type ManageConfig struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Steps       []StepConfig `json:"steps"`
}

type DateSpec struct {
	Absolute string `json:"absolute,omitempty"`
	Relative string `json:"relative,omitempty"`
}

type StepConfig struct {
	Name string `json:"name"`
	Type string `json:"type"`

	// sql_specs
	SpecFile string   `json:"spec_file,omitempty"`
	Specs    []string `json:"specs,omitempty"`

	// query_releases
	Query   string `json:"query,omitempty"`
	StoreAs string `json:"store_as,omitempty"`

	// partition operations
	Table            string    `json:"table,omitempty"`
	ReleasesFrom     string    `json:"releases_from,omitempty"`
	Releases         []string  `json:"releases,omitempty"`
	DateColumn       string    `json:"date_column,omitempty"`
	StartDate        *DateSpec `json:"start_date,omitempty"`
	EndDate          *DateSpec `json:"end_date,omitempty"`
	UsePartmanFormat bool      `json:"use_partman_format,omitempty"`
	RetentionDays    int       `json:"retention_days,omitempty"`
}

type pipelineContext struct {
	variables map[string][]string
}

var manageConfigFile string
var startStep string

func NewManageCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "manage",
		Short: "Execute a partition management pipeline from a config file",
		Long: `Execute a sequence of SQL and partition management steps defined in a JSON config file.
Steps run sequentially in order. Supports SQL spec execution, release discovery from
the database, and all partition management operations (create, rename, detach, drop).

Use --dry-run to preview all operations without executing.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runManage()
		},
	}

	cmd.Flags().StringVar(&manageConfigFile, "config", "", "Path to manage pipeline config JSON file (required)")
	cmd.MarkFlagRequired("config")
	cmd.Flags().StringVar(&dsn, "dsn", "", "PostgreSQL DSN (required)")
	cmd.MarkFlagRequired("dsn")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview operations without executing")
	cmd.Flags().StringVar(&startStep, "start-step", "", "Resume pipeline from this step name (skip earlier steps)")

	return cmd
}

func runManage() error {
	var config ManageConfig
	if err := loadSpecsFromJSON(manageConfigFile, &config); err != nil {
		return fmt.Errorf("failed to load manage config: %w", err)
	}

	if err := validateManageConfig(config); err != nil {
		return fmt.Errorf("invalid manage config: %w", err)
	}

	log.WithFields(log.Fields{
		"name":    config.Name,
		"steps":   len(config.Steps),
		"dry_run": dryRun,
	}).Info("starting manage pipeline")

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}

	return executePipeline(db, config, startStep, dryRun)
}

func executePipeline(db *sql.DB, config ManageConfig, startStep string, dryRun bool) error {
	ctx := &pipelineContext{
		variables: make(map[string][]string),
	}
	now := time.Now().UTC()

	skipping := startStep != ""
	if skipping {
		found := false
		for _, step := range config.Steps {
			if step.Name == startStep {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("start-step %q not found in config (available: %s)", startStep, stepNames(config.Steps))
		}
	}

	for i, step := range config.Steps {
		l := log.WithFields(log.Fields{
			"step": fmt.Sprintf("%d/%d", i+1, len(config.Steps)),
			"name": step.Name,
			"type": step.Type,
		})

		if skipping {
			if step.Name == startStep {
				skipping = false
			} else {
				l.Info("skipping step (before start-step)")
				continue
			}
		}

		l.Info("executing step")

		var err error
		switch step.Type {
		case "sql_specs":
			err = executeSQLSpecsStep(db, step, dryRun)
		case "query_releases":
			err = executeQueryReleasesStep(db, step, ctx, dryRun)
		case "create_partitions_list_to_range":
			err = executeCreatePartitionsListToRangeStep(db, step, ctx, now, dryRun)
		case "create_partitions_range":
			err = executeCreatePartitionsRangeStep(db, step, ctx, now, dryRun)
		case "rename_partitions":
			err = executeRenamePartitionsStep(db, step, ctx, dryRun)
		case "detach_old_partitions":
			err = executeDetachOldPartitionsStep(db, step, dryRun)
		case "drop_old_detached":
			err = executeDropOldDetachedStep(db, step, dryRun)
		default:
			err = fmt.Errorf("unknown step type: %s", step.Type)
		}

		if err != nil {
			return fmt.Errorf("step %d (%s) failed: %w", i+1, step.Name, err)
		}

		l.Info("step completed")
	}

	log.Info("manage pipeline completed successfully")
	return nil
}

func executeSQLSpecsStep(db *sql.DB, step StepConfig, dryRun bool) error {
	var specs []SQLSpec
	if err := loadSpecsFromJSON(step.SpecFile, &specs); err != nil {
		return err
	}

	if len(specs) == 0 {
		return fmt.Errorf("no specs found in file %s", step.SpecFile)
	}

	log.Infof("loaded %d specs from %s", len(specs), step.SpecFile)

	for _, spec := range specs {
		if len(step.Specs) > 0 && !containsString(step.Specs, spec.Name) {
			continue
		}

		if dryRun {
			fmt.Printf("\n--- [DRY RUN] Spec: %s ---\n", spec.Name)
			if spec.Description != "" {
				fmt.Printf("Description: %s\n", spec.Description)
			}
			fmt.Printf("Concurrent: %v\n", spec.Concurrent)
			if spec.ResultType != "" {
				fmt.Printf("ResultType: %s\n", spec.ResultType)
			}
			fmt.Printf("Query:\n%s\n", spec.Query)
			continue
		}

		if err := executeSpec(db, spec); err != nil {
			return fmt.Errorf("spec %s failed: %w", spec.Name, err)
		}
	}

	return nil
}

func executeQueryReleasesStep(db *sql.DB, step StepConfig, ctx *pipelineContext, dryRun bool) error {
	l := log.WithFields(log.Fields{
		"store_as": step.StoreAs,
	})

	if dryRun {
		l.WithField("query", step.Query).Info("[DRY RUN] would query releases")
		ctx.variables[step.StoreAs] = []string{"(dry-run: no query executed)"}
		return nil
	}

	rows, err := db.Query(step.Query)
	if err != nil {
		return fmt.Errorf("release query failed: %w", err)
	}
	defer rows.Close()

	var releases []string
	for rows.Next() {
		var release string
		if err := rows.Scan(&release); err != nil {
			return fmt.Errorf("failed to scan release row: %w", err)
		}
		releases = append(releases, release)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating release rows: %w", err)
	}

	ctx.variables[step.StoreAs] = releases
	l.WithField("count", len(releases)).Info("discovered releases")
	for _, r := range releases {
		l.WithField("release", r).Debug("found release")
	}

	return nil
}

func executeCreatePartitionsListToRangeStep(db *sql.DB, step StepConfig, ctx *pipelineContext, now time.Time, dryRun bool) error {
	releases, err := resolveReleases(step, ctx)
	if err != nil {
		return err
	}

	startDate, err := resolveDateSpec(step.StartDate, now)
	if err != nil {
		return fmt.Errorf("invalid start_date: %w", err)
	}

	endDate, err := resolveDateSpec(step.EndDate, now)
	if err != nil {
		return fmt.Errorf("invalid end_date: %w", err)
	}

	dbp := partitioning.NewPartitions(db)
	created, err := dbp.CreateMissingPartitionsListToRange(
		step.Table,
		releases,
		startDate,
		endDate,
		step.DateColumn,
		step.UsePartmanFormat,
		dryRun,
	)
	if err != nil {
		return err
	}

	log.WithFields(log.Fields{
		"table":   step.Table,
		"created": created,
	}).Info("LIST → RANGE partitions created")

	return nil
}

func executeCreatePartitionsRangeStep(db *sql.DB, step StepConfig, ctx *pipelineContext, now time.Time, dryRun bool) error {
	startDate, err := resolveDateSpec(step.StartDate, now)
	if err != nil {
		return fmt.Errorf("invalid start_date: %w", err)
	}

	endDate, err := resolveDateSpec(step.EndDate, now)
	if err != nil {
		return fmt.Errorf("invalid end_date: %w", err)
	}

	dbp := partitioning.NewPartitions(db)
	created, err := dbp.CreateMissingPartitions(
		step.Table,
		startDate,
		endDate,
		step.UsePartmanFormat,
		dryRun,
	)
	if err != nil {
		return err
	}

	log.WithFields(log.Fields{
		"table":   step.Table,
		"created": created,
	}).Info("RANGE partitions created")

	return nil
}

func executeRenamePartitionsStep(db *sql.DB, step StepConfig, ctx *pipelineContext, dryRun bool) error {
	var releases []string
	if step.ReleasesFrom != "" || len(step.Releases) > 0 {
		var err error
		releases, err = resolveReleases(step, ctx)
		if err != nil {
			return err
		}
	}

	dbp := partitioning.NewPartitions(db)
	renamed, err := dbp.RenamePartitionsToMatchConfig(
		step.Table,
		releases,
		step.UsePartmanFormat,
		dryRun,
	)
	if err != nil {
		return err
	}

	log.WithFields(log.Fields{
		"table":   step.Table,
		"renamed": renamed,
	}).Info("partitions renamed")

	return nil
}

func executeDetachOldPartitionsStep(db *sql.DB, step StepConfig, dryRun bool) error {
	dbp := partitioning.NewPartitions(db)
	detached, err := dbp.DetachOldPartitions(step.Table, step.RetentionDays, dryRun)
	if err != nil {
		return err
	}

	log.WithFields(log.Fields{
		"table":    step.Table,
		"detached": detached,
	}).Info("old partitions detached")

	return nil
}

func executeDropOldDetachedStep(db *sql.DB, step StepConfig, dryRun bool) error {
	dbp := partitioning.NewPartitions(db)
	dropped, err := dbp.DropOldDetachedPartitions(step.Table, step.RetentionDays, dryRun)
	if err != nil {
		return err
	}

	log.WithFields(log.Fields{
		"table":   step.Table,
		"dropped": dropped,
	}).Info("old detached partitions dropped")

	return nil
}

func resolveReleases(step StepConfig, ctx *pipelineContext) ([]string, error) {
	if step.ReleasesFrom != "" {
		releases, ok := ctx.variables[step.ReleasesFrom]
		if !ok {
			return nil, fmt.Errorf("variable %q not found (set by a prior query_releases step with store_as)", step.ReleasesFrom)
		}
		if len(releases) == 0 {
			log.WithField("variable", step.ReleasesFrom).Warn("release list is empty, no partitions will be created")
		}
		return releases, nil
	}
	if len(step.Releases) > 0 {
		return step.Releases, nil
	}
	return nil, fmt.Errorf("step %q requires either releases_from or releases", step.Name)
}

func resolveDateSpec(spec *DateSpec, now time.Time) (time.Time, error) {
	if spec == nil {
		return time.Time{}, fmt.Errorf("date spec is required")
	}
	if spec.Absolute != "" {
		return time.Parse("2006-01-02", spec.Absolute)
	}
	if spec.Relative != "" {
		return parseRelativeDate(spec.Relative, now)
	}
	return time.Time{}, fmt.Errorf("date spec must have either absolute or relative value")
}

func parseRelativeDate(rel string, now time.Time) (time.Time, error) {
	if len(rel) < 2 {
		return time.Time{}, fmt.Errorf("invalid relative date: %q", rel)
	}

	unit := rel[len(rel)-1]
	numStr := rel[:len(rel)-1]
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid relative date %q: %w", rel, err)
	}

	switch unit {
	case 'd':
		return now.AddDate(0, 0, n), nil
	case 'w':
		return now.AddDate(0, 0, n*7), nil
	case 'm':
		return now.AddDate(0, n, 0), nil
	default:
		return time.Time{}, fmt.Errorf("unknown relative date unit %q in %q (use d/w/m)", string(unit), rel)
	}
}

func validateManageConfig(config ManageConfig) error {
	if len(config.Steps) == 0 {
		return fmt.Errorf("config has no steps")
	}

	storeAsNames := make(map[string]bool)

	for i, step := range config.Steps {
		if step.Name == "" {
			return fmt.Errorf("step %d: name is required", i+1)
		}

		switch step.Type {
		case "sql_specs":
			if step.SpecFile == "" {
				return fmt.Errorf("step %d (%s): spec_file is required for sql_specs", i+1, step.Name)
			}

		case "query_releases":
			if step.Query == "" {
				return fmt.Errorf("step %d (%s): query is required for query_releases", i+1, step.Name)
			}
			if step.StoreAs == "" {
				return fmt.Errorf("step %d (%s): store_as is required for query_releases", i+1, step.Name)
			}
			storeAsNames[step.StoreAs] = true

		case "create_partitions_list_to_range":
			if step.Table == "" {
				return fmt.Errorf("step %d (%s): table is required", i+1, step.Name)
			}
			if step.DateColumn == "" {
				return fmt.Errorf("step %d (%s): date_column is required for create_partitions_list_to_range", i+1, step.Name)
			}
			if err := validateReleasesConfig(step, i, storeAsNames); err != nil {
				return err
			}
			if err := validateDateConfig(step, i); err != nil {
				return err
			}

		case "create_partitions_range":
			if step.Table == "" {
				return fmt.Errorf("step %d (%s): table is required", i+1, step.Name)
			}
			if err := validateDateConfig(step, i); err != nil {
				return err
			}

		case "rename_partitions":
			if step.Table == "" {
				return fmt.Errorf("step %d (%s): table is required", i+1, step.Name)
			}
			if step.ReleasesFrom != "" && !storeAsNames[step.ReleasesFrom] {
				return fmt.Errorf("step %d (%s): releases_from %q references a variable not set by any prior query_releases step", i+1, step.Name, step.ReleasesFrom)
			}

		case "detach_old_partitions", "drop_old_detached":
			if step.Table == "" {
				return fmt.Errorf("step %d (%s): table is required", i+1, step.Name)
			}
			if step.RetentionDays <= 0 {
				return fmt.Errorf("step %d (%s): retention_days must be positive", i+1, step.Name)
			}

		default:
			return fmt.Errorf("step %d (%s): unknown type %q", i+1, step.Name, step.Type)
		}
	}

	return nil
}

func validateReleasesConfig(step StepConfig, idx int, storeAsNames map[string]bool) error {
	if step.ReleasesFrom == "" && len(step.Releases) == 0 {
		return fmt.Errorf("step %d (%s): either releases_from or releases is required", idx+1, step.Name)
	}
	if step.ReleasesFrom != "" && !storeAsNames[step.ReleasesFrom] {
		return fmt.Errorf("step %d (%s): releases_from %q references a variable not set by any prior query_releases step", idx+1, step.Name, step.ReleasesFrom)
	}
	return nil
}

func validateDateConfig(step StepConfig, idx int) error {
	if step.StartDate == nil {
		return fmt.Errorf("step %d (%s): start_date is required", idx+1, step.Name)
	}
	if step.EndDate == nil {
		return fmt.Errorf("step %d (%s): end_date is required", idx+1, step.Name)
	}
	if step.StartDate.Absolute == "" && step.StartDate.Relative == "" {
		return fmt.Errorf("step %d (%s): start_date must have either absolute or relative value", idx+1, step.Name)
	}
	if step.EndDate.Absolute == "" && step.EndDate.Relative == "" {
		return fmt.Errorf("step %d (%s): end_date must have either absolute or relative value", idx+1, step.Name)
	}
	return nil
}

func stepNames(steps []StepConfig) string {
	names := make([]string, len(steps))
	for i, s := range steps {
		names[i] = s.Name
	}
	return strings.Join(names, ", ")
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
