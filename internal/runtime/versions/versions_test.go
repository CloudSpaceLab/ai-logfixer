package versions_test

import (
	"testing"
	"time"

	"github.com/CloudSpaceLab/ai-logfixer/internal/runtime/versions"
)

func TestDiagnoseDetectsRuntimePackageAndAPIVersionMismatch(t *testing.T) {
	t.Parallel()

	result, err := versions.Diagnose(versions.Options{
		ServiceName: "orders-api",
		Required: []versions.Requirement{
			{Kind: versions.KindRuntime, Name: "node", Constraint: ">=20.0.0"},
			{Kind: versions.KindPackage, Name: "express", Constraint: "^5.0.0"},
			{Kind: versions.KindAPI, Name: "stripe", Constraint: ">=2024.1.0"},
		},
		Observed: []versions.ObservedVersion{
			{Kind: versions.KindRuntime, Name: "node", Version: "18.19.0", Source: "process.version"},
			{Kind: versions.KindPackage, Name: "express", Version: "4.18.3", Source: "package-lock.json"},
			{Kind: versions.KindAPI, Name: "stripe", Version: "2023.10.0", Source: "provider response header"},
		},
		Now: fixedTime(),
	})
	if err != nil {
		t.Fatalf("diagnose versions: %v", err)
	}

	if result.Status != versions.StatusDrift {
		t.Fatalf("expected version drift, got %q", result.Status)
	}
	assertFinding(t, result.Findings, versions.KindRuntime, "node")
	assertFinding(t, result.Findings, versions.KindPackage, "express")
	assertFinding(t, result.Findings, versions.KindAPI, "stripe")
	if result.AutoRepair.Allowed {
		t.Fatal("version diagnostics should not auto-repair without explicit plan")
	}
	if !hasRecommendation(result.Recommendations, "select_compatible_runtime_version") {
		t.Fatalf("expected runtime recommendation, got %+v", result.Recommendations)
	}
	if !hasRecommendation(result.Recommendations, "adjust_package_version_with_lockfile_verification") {
		t.Fatalf("expected package recommendation, got %+v", result.Recommendations)
	}
	if !hasRecommendation(result.Recommendations, "update_api_version_or_client_contract") {
		t.Fatalf("expected API recommendation, got %+v", result.Recommendations)
	}
}

func TestDiagnoseReportsHealthyVersionEvidence(t *testing.T) {
	t.Parallel()

	result, err := versions.Diagnose(versions.Options{
		ServiceName: "orders-api",
		Required: []versions.Requirement{
			{Kind: versions.KindRuntime, Name: "go", Constraint: ">=1.23.0,<1.25.0"},
			{Kind: versions.KindPackage, Name: "express", Constraint: "^5.0.0"},
		},
		Observed: []versions.ObservedVersion{
			{Kind: versions.KindRuntime, Name: "go", Version: "1.24.1"},
			{Kind: versions.KindPackage, Name: "express", Version: "5.1.0"},
		},
		Now: fixedTime(),
	})
	if err != nil {
		t.Fatalf("diagnose healthy versions: %v", err)
	}

	if result.Status != versions.StatusHealthy {
		t.Fatalf("expected healthy status, got %q", result.Status)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("expected no findings, got %+v", result.Findings)
	}
	if !hasRecommendation(result.Recommendations, "no_version_change") {
		t.Fatalf("expected no-change recommendation, got %+v", result.Recommendations)
	}
}

func TestDiagnoseReportsMissingVersionEvidence(t *testing.T) {
	t.Parallel()

	result, err := versions.Diagnose(versions.Options{
		ServiceName: "orders-api",
		Required: []versions.Requirement{
			{Kind: versions.KindPackage, Name: "laravel/framework", Constraint: ">=11.0.0"},
		},
		Now: fixedTime(),
	})
	if err != nil {
		t.Fatalf("diagnose missing evidence: %v", err)
	}

	if len(result.Findings) != 1 || result.Findings[0].Kind != "missing_version_evidence" {
		t.Fatalf("expected missing evidence finding, got %+v", result.Findings)
	}
}

func TestDiagnoseRequiresServiceName(t *testing.T) {
	t.Parallel()

	_, err := versions.Diagnose(versions.Options{
		Required: []versions.Requirement{{Name: "node", Constraint: ">=20.0.0"}},
	})
	if err == nil {
		t.Fatal("expected service name validation error")
	}
}

func assertFinding(t *testing.T, findings []versions.Finding, kind versions.Kind, name string) {
	t.Helper()
	for _, finding := range findings {
		if finding.TargetKind == kind && finding.Name == name {
			return
		}
	}
	t.Fatalf("expected finding for %s %s in %+v", kind, name, findings)
}

func hasRecommendation(recommendations []versions.Recommendation, action string) bool {
	for _, recommendation := range recommendations {
		if recommendation.Action == action {
			return true
		}
	}
	return false
}

func fixedTime() time.Time {
	return time.Date(2026, 6, 8, 13, 0, 0, 0, time.UTC)
}
