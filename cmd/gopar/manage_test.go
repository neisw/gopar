package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func Test_ParseRelativeDate(t *testing.T) {
	now := time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		input    string
		expected time.Time
		wantErr  bool
	}{
		{"+30d", time.Date(2025, 7, 15, 0, 0, 0, 0, time.UTC), false},
		{"-30d", time.Date(2025, 5, 16, 0, 0, 0, 0, time.UTC), false},
		{"+2w", time.Date(2025, 6, 29, 0, 0, 0, 0, time.UTC), false},
		{"-1w", time.Date(2025, 6, 8, 0, 0, 0, 0, time.UTC), false},
		{"+3m", time.Date(2025, 9, 15, 0, 0, 0, 0, time.UTC), false},
		{"-1m", time.Date(2025, 5, 15, 0, 0, 0, 0, time.UTC), false},
		{"+0d", time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC), false},
		{"", time.Time{}, true},
		{"d", time.Time{}, true},
		{"+5x", time.Time{}, true},
		{"abc", time.Time{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := parseRelativeDate(tt.input, now)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for input %q, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for input %q: %v", tt.input, err)
			}
			if !result.Equal(tt.expected) {
				t.Errorf("parseRelativeDate(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func Test_ResolveDateSpec(t *testing.T) {
	now := time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC)

	t.Run("absolute date", func(t *testing.T) {
		spec := &DateSpec{Absolute: "2025-01-15"}
		result, err := resolveDateSpec(spec, now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
		if !result.Equal(expected) {
			t.Errorf("got %v, want %v", result, expected)
		}
	})

	t.Run("relative date", func(t *testing.T) {
		spec := &DateSpec{Relative: "+7d"}
		result, err := resolveDateSpec(spec, now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := time.Date(2025, 6, 22, 0, 0, 0, 0, time.UTC)
		if !result.Equal(expected) {
			t.Errorf("got %v, want %v", result, expected)
		}
	})

	t.Run("nil spec", func(t *testing.T) {
		_, err := resolveDateSpec(nil, now)
		if err == nil {
			t.Error("expected error for nil spec")
		}
	})

	t.Run("empty spec", func(t *testing.T) {
		spec := &DateSpec{}
		_, err := resolveDateSpec(spec, now)
		if err == nil {
			t.Error("expected error for empty spec")
		}
	})
}

func Test_ManageConfigParsing(t *testing.T) {
	input := `{
		"name": "test_pipeline",
		"description": "Test pipeline",
		"steps": [
			{
				"name": "discover",
				"type": "query_releases",
				"query": "SELECT DISTINCT release FROM t",
				"store_as": "releases"
			},
			{
				"name": "create_parts",
				"type": "create_partitions_list_to_range",
				"table": "my_table",
				"releases_from": "releases",
				"date_column": "created_at",
				"start_date": {"relative": "-30d"},
				"end_date": {"relative": "+90d"},
				"use_partman_format": true
			},
			{
				"name": "cleanup",
				"type": "detach_old_partitions",
				"table": "my_table",
				"retention_days": 180
			}
		]
	}`

	var config ManageConfig
	if err := json.Unmarshal([]byte(input), &config); err != nil {
		t.Fatalf("failed to parse config: %v", err)
	}

	if config.Name != "test_pipeline" {
		t.Errorf("name = %q, want %q", config.Name, "test_pipeline")
	}
	if len(config.Steps) != 3 {
		t.Fatalf("steps count = %d, want 3", len(config.Steps))
	}

	// Verify query_releases step
	step0 := config.Steps[0]
	if step0.Type != "query_releases" {
		t.Errorf("step 0 type = %q, want %q", step0.Type, "query_releases")
	}
	if step0.StoreAs != "releases" {
		t.Errorf("step 0 store_as = %q, want %q", step0.StoreAs, "releases")
	}

	// Verify partition step with date specs
	step1 := config.Steps[1]
	if step1.ReleasesFrom != "releases" {
		t.Errorf("step 1 releases_from = %q, want %q", step1.ReleasesFrom, "releases")
	}
	if step1.StartDate == nil || step1.StartDate.Relative != "-30d" {
		t.Errorf("step 1 start_date = %+v, want relative -30d", step1.StartDate)
	}
	if !step1.UsePartmanFormat {
		t.Error("step 1 use_partman_format should be true")
	}

	// Verify retention step
	step2 := config.Steps[2]
	if step2.RetentionDays != 180 {
		t.Errorf("step 2 retention_days = %d, want 180", step2.RetentionDays)
	}
}

func Test_ValidateManageConfig(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		config := ManageConfig{
			Name: "test",
			Steps: []StepConfig{
				{Name: "query", Type: "query_releases", Query: "SELECT 1", StoreAs: "rel"},
				{
					Name: "create", Type: "create_partitions_list_to_range",
					Table: "t", ReleasesFrom: "rel", DateColumn: "d",
					StartDate: &DateSpec{Relative: "-1d"}, EndDate: &DateSpec{Relative: "+1d"},
				},
			},
		}
		if err := validateManageConfig(config); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("empty steps", func(t *testing.T) {
		config := ManageConfig{Name: "test", Steps: []StepConfig{}}
		if err := validateManageConfig(config); err == nil {
			t.Error("expected error for empty steps")
		}
	})

	t.Run("unknown step type", func(t *testing.T) {
		config := ManageConfig{
			Name:  "test",
			Steps: []StepConfig{{Name: "bad", Type: "nonexistent"}},
		}
		if err := validateManageConfig(config); err == nil {
			t.Error("expected error for unknown step type")
		}
	})

	t.Run("missing releases_from reference", func(t *testing.T) {
		config := ManageConfig{
			Name: "test",
			Steps: []StepConfig{
				{
					Name: "create", Type: "create_partitions_list_to_range",
					Table: "t", ReleasesFrom: "nonexistent", DateColumn: "d",
					StartDate: &DateSpec{Relative: "-1d"}, EndDate: &DateSpec{Relative: "+1d"},
				},
			},
		}
		if err := validateManageConfig(config); err == nil {
			t.Error("expected error for missing releases_from reference")
		}
	})

	t.Run("missing retention_days", func(t *testing.T) {
		config := ManageConfig{
			Name:  "test",
			Steps: []StepConfig{{Name: "detach", Type: "detach_old_partitions", Table: "t"}},
		}
		if err := validateManageConfig(config); err == nil {
			t.Error("expected error for missing retention_days")
		}
	})

	t.Run("valid rename_partitions without releases (flat RANGE)", func(t *testing.T) {
		config := ManageConfig{
			Name: "test",
			Steps: []StepConfig{
				{Name: "rename", Type: "rename_partitions", Table: "t", UsePartmanFormat: true},
			},
		}
		if err := validateManageConfig(config); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("valid rename_partitions with releases (LIST→RANGE)", func(t *testing.T) {
		config := ManageConfig{
			Name: "test",
			Steps: []StepConfig{
				{Name: "query", Type: "query_releases", Query: "SELECT 1", StoreAs: "rel"},
				{Name: "rename", Type: "rename_partitions", Table: "t", ReleasesFrom: "rel"},
			},
		}
		if err := validateManageConfig(config); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("rename_partitions with bad releases_from reference", func(t *testing.T) {
		config := ManageConfig{
			Name: "test",
			Steps: []StepConfig{
				{Name: "rename", Type: "rename_partitions", Table: "t", ReleasesFrom: "nonexistent"},
			},
		}
		if err := validateManageConfig(config); err == nil {
			t.Error("expected error for bad releases_from reference")
		}
	})
}

func Test_ResolveReleases(t *testing.T) {
	ctx := &pipelineContext{
		variables: map[string][]string{
			"active": {"v4.0", "v4.1", "v4.2"},
		},
	}

	t.Run("from context variable", func(t *testing.T) {
		step := StepConfig{Name: "test", ReleasesFrom: "active"}
		releases, err := resolveReleases(step, ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(releases) != 3 || releases[0] != "v4.0" {
			t.Errorf("got %v, want [v4.0 v4.1 v4.2]", releases)
		}
	})

	t.Run("from static list", func(t *testing.T) {
		step := StepConfig{Name: "test", Releases: []string{"v5.0"}}
		releases, err := resolveReleases(step, ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(releases) != 1 || releases[0] != "v5.0" {
			t.Errorf("got %v, want [v5.0]", releases)
		}
	})

	t.Run("releases_from takes precedence", func(t *testing.T) {
		step := StepConfig{Name: "test", ReleasesFrom: "active", Releases: []string{"v5.0"}}
		releases, err := resolveReleases(step, ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(releases) != 3 {
			t.Errorf("releases_from should take precedence, got %v", releases)
		}
	})

	t.Run("missing variable", func(t *testing.T) {
		step := StepConfig{Name: "test", ReleasesFrom: "nonexistent"}
		_, err := resolveReleases(step, ctx)
		if err == nil {
			t.Error("expected error for missing variable")
		}
	})

	t.Run("neither set", func(t *testing.T) {
		step := StepConfig{Name: "test"}
		_, err := resolveReleases(step, ctx)
		if err == nil {
			t.Error("expected error when neither releases_from nor releases is set")
		}
	})
}

func Test_StepNames(t *testing.T) {
	steps := []StepConfig{
		{Name: "alpha", Type: "sql_specs"},
		{Name: "beta", Type: "query_releases"},
		{Name: "gamma", Type: "rename_partitions"},
	}
	got := stepNames(steps)
	expected := "alpha, beta, gamma"
	if got != expected {
		t.Errorf("stepNames() = %q, want %q", got, expected)
	}
}

func Test_StartStepValidation(t *testing.T) {
	config := ManageConfig{
		Name: "test",
		Steps: []StepConfig{
			{Name: "step_a", Type: "sql_specs", SpecFile: "a.json"},
			{Name: "step_b", Type: "sql_specs", SpecFile: "b.json"},
			{Name: "step_c", Type: "sql_specs", SpecFile: "c.json"},
		},
	}

	t.Run("nonexistent start step", func(t *testing.T) {
		err := executePipeline(nil, config, "nonexistent", true)
		if err == nil {
			t.Error("expected error for nonexistent start step")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("error should mention 'not found', got: %v", err)
		}
	})
}

func Test_ContainsString(t *testing.T) {
	tests := []struct {
		slice    []string
		s        string
		expected bool
	}{
		{[]string{"a", "b", "c"}, "b", true},
		{[]string{"a", "b", "c"}, "d", false},
		{[]string{}, "a", false},
		{nil, "a", false},
	}

	for _, tt := range tests {
		result := containsString(tt.slice, tt.s)
		if result != tt.expected {
			t.Errorf("containsString(%v, %q) = %v, want %v", tt.slice, tt.s, result, tt.expected)
		}
	}
}
