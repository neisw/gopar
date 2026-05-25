package main

import (
	"testing"
)

// Test_LoadIndexSpecs verifies that index specs can be loaded from JSON
func Test_LoadIndexSpecs(t *testing.T) {
	type indexSpec struct {
		Name  string `json:"name"`
		Query string `json:"query"`
	}

	var specs []indexSpec
	loadSpecsFromJSONForTest(t, "example_index_specs.json", &specs)

	if len(specs) == 0 {
		t.Fatal("Expected at least one index spec")
	}

	for _, spec := range specs {
		if spec.Name == "" {
			t.Error("Index spec missing name")
		}
		if spec.Query == "" {
			t.Error("Index spec missing query")
		}
		t.Logf("Loaded index spec: %s", spec.Name)
	}
}

// Test_LoadBackfillSpecs verifies that backfill specs can be loaded from JSON
func Test_LoadBackfillSpecs(t *testing.T) {
	type backfillSpec struct {
		TableName   string `json:"tableName"`
		Description string `json:"description"`
		UpdateQuery string `json:"updateQuery"`
	}

	var specs []backfillSpec
	loadSpecsFromJSONForTest(t, "example_backfill_specs.json", &specs)

	if len(specs) == 0 {
		t.Fatal("Expected at least one backfill spec")
	}

	for _, spec := range specs {
		if spec.TableName == "" {
			t.Error("Backfill spec missing tableName")
		}
		if spec.UpdateQuery == "" {
			t.Error("Backfill spec missing updateQuery")
		}
		t.Logf("Loaded backfill spec: %s - %s", spec.TableName, spec.Description)
	}
}

// Test_LoadSQLSpecs verifies that SQL specs can be loaded from JSON
func Test_LoadSQLSpecs(t *testing.T) {
	var specs []SQLSpec
	loadSpecsFromJSONForTest(t, "example_sql_specs.json", &specs)

	if len(specs) == 0 {
		t.Fatal("Expected at least one SQL spec")
	}

	for _, spec := range specs {
		if spec.Name == "" {
			t.Error("SQL spec missing name")
		}
		if spec.Query == "" {
			t.Error("SQL spec missing query")
		}
		t.Logf("Loaded SQL spec: %s - %s (concurrent: %v)", spec.Name, spec.Description, spec.Concurrent)
	}
}
