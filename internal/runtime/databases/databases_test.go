package databases_test

import (
	"strings"
	"testing"
	"time"

	"github.com/CloudSpaceLab/ai-logfixer/internal/runtime/databases"
)

func TestDiagnoseDetectsDatabaseURLAndSchemaDrift(t *testing.T) {
	t.Parallel()

	result, err := databases.Diagnose(databases.Options{
		ServiceName:      "orders-api",
		DatabaseURL:      "postgres://orders:secret@127.0.0.1:5432/wrongdb?sslmode=disable",
		AllowedSchemes:   []string{"postgres"},
		AllowedHosts:     []string{"db.internal"},
		RequiredDatabase: "orders",
		RequiredTables: []databases.TableExpectation{
			{Name: "orders", Columns: []string{"id", "total_cents", "customer_id"}},
			{Name: "customers", Columns: []string{"id"}},
		},
		ObservedTables: []databases.TableState{
			{Name: "orders", Columns: []string{"id", "total_cents"}},
		},
		ConnectionProbe: databases.ProbeResult{Checked: true, OK: false, Error: "password authentication failed"},
		Now:             fixedTime(),
	})
	if err != nil {
		t.Fatalf("diagnose database drift: %v", err)
	}

	if result.Status != databases.StatusDrift {
		t.Fatalf("expected drift status, got %q", result.Status)
	}
	assertFinding(t, result.Findings, "database_host_drift")
	assertFinding(t, result.Findings, "database_name_drift")
	assertFinding(t, result.Findings, "database_connection_failed")
	assertFinding(t, result.Findings, "missing_database_column")
	assertFinding(t, result.Findings, "missing_database_table")
	if strings.Contains(result.DatabaseURL.Redacted, "secret") {
		t.Fatalf("database URL should redact credentials, got %q", result.DatabaseURL.Redacted)
	}
	if result.AutoMutation.Allowed {
		t.Fatal("database diagnostics should not auto-mutate credentials or schema")
	}
}

func TestDiagnoseReportsHealthyWhenEvidenceMatches(t *testing.T) {
	t.Parallel()

	result, err := databases.Diagnose(databases.Options{
		ServiceName:      "orders-api",
		DatabaseURL:      "postgres://orders:secret@db.internal:5432/orders?sslmode=require",
		AllowedSchemes:   []string{"postgres"},
		AllowedHosts:     []string{"db.internal"},
		RequiredDatabase: "orders",
		RequiredTables: []databases.TableExpectation{
			{Name: "orders", Columns: []string{"id", "total_cents"}},
		},
		ObservedTables: []databases.TableState{
			{Name: "orders", Columns: []string{"id", "total_cents", "created_at"}},
		},
		ConnectionProbe: databases.ProbeResult{Checked: true, OK: true},
		Now:             fixedTime(),
	})
	if err != nil {
		t.Fatalf("diagnose healthy database: %v", err)
	}

	if result.Status != databases.StatusHealthy {
		t.Fatalf("expected healthy status, got %q", result.Status)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("expected no findings, got %+v", result.Findings)
	}
	if len(result.Recommendations) != 1 || result.Recommendations[0].Action != "no_database_change" {
		t.Fatalf("expected no-change recommendation, got %+v", result.Recommendations)
	}
}

func TestDiagnoseDetectsMissingAndMalformedDatabaseURL(t *testing.T) {
	t.Parallel()

	missing, err := databases.Diagnose(databases.Options{
		ServiceName: "orders-api",
		Now:         fixedTime(),
	})
	if err != nil {
		t.Fatalf("diagnose missing URL: %v", err)
	}
	assertFinding(t, missing.Findings, "missing_database_url")

	malformed, err := databases.Diagnose(databases.Options{
		ServiceName: "orders-api",
		DatabaseURL: "://bad-url",
		Now:         fixedTime(),
	})
	if err != nil {
		t.Fatalf("diagnose malformed URL: %v", err)
	}
	assertFinding(t, malformed.Findings, "malformed_database_url")
}

func TestDiagnoseRequiresServiceName(t *testing.T) {
	t.Parallel()

	_, err := databases.Diagnose(databases.Options{DatabaseURL: "postgres://db/orders"})
	if err == nil {
		t.Fatal("expected service name validation error")
	}
}

func assertFinding(t *testing.T, findings []databases.Finding, kind string) {
	t.Helper()
	for _, finding := range findings {
		if finding.Kind == kind {
			return
		}
	}
	t.Fatalf("expected finding %q in %+v", kind, findings)
}

func fixedTime() time.Time {
	return time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
}
