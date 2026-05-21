package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateDiagnosisResultAcceptsMinimalCompleteDiagnosis(t *testing.T) {
	result := loadDiagnosisResult(t, "../../../contracts/v1/examples/valid/minimal-diagnosis-result.json")

	if err := result.Validate(); err != nil {
		t.Fatalf("expected valid diagnosis result, got error: %v", err)
	}
}

func TestValidateDiagnosisResultRejectsUnsafeBusinessStates(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		wantError string
	}{
		{
			name:      "high risk patch without approval",
			path:      "../../../contracts/v1/examples/invalid/high-risk-without-approval.json",
			wantError: "high_risk patch plans require approval",
		},
		{
			name:      "unavailable rollback without limitations",
			path:      "../../../contracts/v1/examples/invalid/unavailable-rollback-without-limitations.json",
			wantError: "unavailable rollback plans require limitations",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := loadDiagnosisResult(t, tt.path)
			err := result.Validate()
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("expected error containing %q, got %q", tt.wantError, err.Error())
			}
		})
	}
}

func loadDiagnosisResult(t *testing.T, path string) DiagnosisResult {
	t.Helper()

	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var result DiagnosisResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal diagnosis result: %v", err)
	}

	return result
}
