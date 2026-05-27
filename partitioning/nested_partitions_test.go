package partitioning

import (
	"strings"
	"testing"
)

func Test_validatePartitionNameLength(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "under limit",
			input:   "events_2024_01_01",
			wantErr: false,
		},
		{
			name:    "at limit",
			input:   strings.Repeat("a", 62),
			wantErr: false,
		},
		{
			name:    "over limit by one",
			input:   strings.Repeat("a", 63),
			wantErr: true,
		},
		{
			name:    "real world overflow",
			input:   "prow_job_run_annotations_new_aro_classic_production_p2026_04_29",
			wantErr: true, // 63 chars, exceeds safe limit of 62
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePartitionNameLength(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("validatePartitionNameLength(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func Test_dateSuffixLen(t *testing.T) {
	if dateSuffixLen(false) != 11 {
		t.Errorf("standard format suffix should be 11, got %d", dateSuffixLen(false))
	}
	if dateSuffixLen(true) != 12 {
		t.Errorf("partman format suffix should be 12, got %d", dateSuffixLen(true))
	}
}

func Test_shortenTablePrefix(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "already fits",
			input:  "events",
			maxLen: 20,
			want:   "events",
		},
		{
			name:   "exactly at limit",
			input:  "events",
			maxLen: 6,
			want:   "events",
		},
		{
			name:   "needs shortening",
			input:  "prow_job_run_annotations_new",
			maxLen: 20,
		},
		{
			name:   "very short limit",
			input:  "prow_job_run_annotations_new",
			maxLen: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shortenTablePrefix(tt.input, tt.maxLen)

			if len(got) > tt.maxLen {
				t.Errorf("result %q is %d chars, exceeds maxLen %d", got, len(got), tt.maxLen)
			}

			if tt.want != "" && got != tt.want {
				t.Errorf("expected %q, got %q", tt.want, got)
			}

			// Verify deterministic
			got2 := shortenTablePrefix(tt.input, tt.maxLen)
			if got != got2 {
				t.Errorf("not deterministic: got %q then %q", got, got2)
			}
		})
	}
}

func Test_shortenTablePrefix_deterministic(t *testing.T) {
	a := shortenTablePrefix("prow_job_run_annotations_new", 20)
	b := shortenTablePrefix("prow_job_run_annotations_new", 20)
	if a != b {
		t.Errorf("same input produced different outputs: %q vs %q", a, b)
	}
}

func Test_shortenTablePrefix_unique(t *testing.T) {
	a := shortenTablePrefix("prow_job_run_annotations_new", 20)
	b := shortenTablePrefix("prow_job_run_annotations_old", 20)
	if a == b {
		t.Errorf("different inputs produced same output: %q", a)
	}
}

func Test_buildNestedPartitionPrefix(t *testing.T) {
	tests := []struct {
		name             string
		tableName        string
		release          string
		usePartmanFormat bool
		wantShortened    bool
	}{
		{
			name:      "short names unchanged",
			tableName: "events",
			release:   "v4.18",
		},
		{
			name:      "standard format at limit unchanged",
			tableName: "prow_job_run_annotations_new",
			release:   "aro_classic_production",
			// 28 + 1 + 22 + 11 = 62, fits
		},
		{
			name:             "partman format triggers shortening",
			tableName:        "prow_job_run_annotations_new",
			release:          "aro_classic_production",
			usePartmanFormat: true,
			wantShortened:    true,
			// 28 + 1 + 22 + 12 = 63, over limit
		},
		{
			name:          "very long release triggers shortening",
			tableName:     "prow_job_run_annotations_new",
			release:       "aro_classic_production_extended",
			wantShortened: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildNestedPartitionPrefix(tt.tableName, tt.release, tt.usePartmanFormat)
			safeName := sanitizePartitionName(tt.release)
			full := tt.tableName + "_" + safeName

			maxDaily := len(result) + dateSuffixLen(tt.usePartmanFormat)
			if maxDaily > maxPartitionNameLen {
				t.Errorf("result %q (%d chars) would produce daily names of %d chars, exceeds limit %d",
					result, len(result), maxDaily, maxPartitionNameLen)
			}

			wasShortened := result != full
			if wasShortened != tt.wantShortened {
				t.Errorf("shortened=%v want=%v, full=%q result=%q", wasShortened, tt.wantShortened, full, result)
			}

			// Verify deterministic
			result2 := buildNestedPartitionPrefix(tt.tableName, tt.release, tt.usePartmanFormat)
			if result != result2 {
				t.Errorf("not deterministic: %q vs %q", result, result2)
			}
		})
	}
}

func Test_extractDateFromPartitionBounds(t *testing.T) {
	tests := []struct {
		name   string
		bounds string
		want   string // expected date string or "" if nil
	}{
		{
			name:   "standard range bounds",
			bounds: "FOR VALUES FROM ('2026-04-29') TO ('2026-04-30')",
			want:   "2026-04-29",
		},
		{
			name:   "different date",
			bounds: "FOR VALUES FROM ('2024-01-01') TO ('2024-01-02')",
			want:   "2024-01-01",
		},
		{
			name:   "list bounds (no FROM)",
			bounds: "FOR VALUES IN ('aro_classic_production')",
			want:   "",
		},
		{
			name:   "empty string",
			bounds: "",
			want:   "",
		},
		{
			name:   "malformed bounds",
			bounds: "FOR VALUES FROM ('not-a-date') TO ('also-not')",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractDateFromPartitionBounds(tt.bounds)
			if tt.want == "" {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
			} else {
				if got == nil {
					t.Fatalf("expected %s, got nil", tt.want)
				}
				if got.Format("2006-01-02") != tt.want {
					t.Errorf("expected %s, got %s", tt.want, got.Format("2006-01-02"))
				}
			}
		})
	}
}

func Test_nestedPartitionNameOverflow(t *testing.T) {
	tests := []struct {
		name             string
		tableName        string
		release          string
		usePartmanFormat bool
		wantErr          bool
	}{
		{
			name:      "short names fit",
			tableName: "events",
			release:   "v4.18",
			wantErr:   false,
		},
		{
			name:      "standard format at limit",
			tableName: "prow_job_run_annotations_new",
			release:   "aro_classic_production",
			wantErr:   false, // 28 + 1 + 22 + 11 = 62, exactly at limit
		},
		{
			name:             "partman format overflows",
			tableName:        "prow_job_run_annotations_new",
			release:          "aro_classic_production",
			usePartmanFormat: true,
			wantErr:          true, // 28 + 1 + 22 + 12 = 63, over limit of 62
		},
		{
			name:      "exactly at limit",
			tableName: strings.Repeat("a", 25),
			release:   strings.Repeat("b", 25),
			wantErr:   false, // 25 + 1 + 25 + 11 = 62
		},
		{
			name:      "one over limit",
			tableName: strings.Repeat("a", 26),
			release:   strings.Repeat("b", 25),
			wantErr:   true, // 26 + 1 + 25 + 11 = 63
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			safeName := sanitizePartitionName(tt.release)
			intermediate := tt.tableName + "_" + safeName
			maxDaily := len(intermediate) + dateSuffixLen(tt.usePartmanFormat)
			overflows := maxDaily > maxPartitionNameLen

			if overflows != tt.wantErr {
				t.Errorf("table=%q release=%q intermediate=%q (len %d) daily would be %d chars, overflows=%v want=%v",
					tt.tableName, tt.release, intermediate, len(intermediate), maxDaily, overflows, tt.wantErr)
			}
		})
	}
}
