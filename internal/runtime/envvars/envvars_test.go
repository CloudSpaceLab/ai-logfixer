package envvars_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	envvars "github.com/CloudSpaceLab/ai-logfixer/internal/runtime/envvars"
)

func TestRunBlocksMissingSecretEnvironmentVariable(t *testing.T) {
	t.Parallel()

	result, err := envvars.Run(context.Background(), envvars.Options{
		ServiceName: "orders-api",
		Policy: envvars.Policy{
			Variables: []envvars.VariableRequirement{
				{Name: "STRIPE_SECRET_KEY", Required: true, Secret: true},
			},
		},
		LookupEnv: emptyEnv,
		Now:       fixedTime(),
	})
	if err != nil {
		t.Fatalf("run env diagnostics: %v", err)
	}

	if result.RemediationPlan.RiskLevel != contractsv1.SafetyBlocked {
		t.Fatalf("expected blocked plan for missing secret, got %q", result.RemediationPlan.RiskLevel)
	}
	if result.Attempt.Status != contractsv1.RemediationStatusEscalated {
		t.Fatalf("expected escalated attempt, got %q", result.Attempt.Status)
	}
	if !strings.Contains(result.Diagnosis.SuspectedRootCause, "STRIPE_SECRET_KEY") {
		t.Fatalf("diagnosis should name missing var, got %q", result.Diagnosis.SuspectedRootCause)
	}
	if strings.Contains(result.Receipt.AfterState, "secret") && strings.Contains(result.Receipt.AfterState, "value") {
		t.Fatalf("receipt must not invent or expose secret values: %q", result.Receipt.AfterState)
	}
}

func TestRunDryRunPlansExplicitNonSecretDefaultWithoutWriting(t *testing.T) {
	t.Parallel()

	envPath := filepath.Join(t.TempDir(), ".env")
	result, err := envvars.Run(context.Background(), envvars.Options{
		ServiceName: "orders-api",
		EnvFilePath: envPath,
		Policy: envvars.Policy{
			Variables: []envvars.VariableRequirement{
				{Name: "CACHE_DRIVER", Required: true, Secret: false, DefaultValue: "file", AllowDefaultWrite: true},
			},
		},
		LookupEnv: emptyEnv,
		Apply:     false,
		Now:       fixedTime(),
	})
	if err != nil {
		t.Fatalf("run env diagnostics dry-run: %v", err)
	}

	if result.RemediationPlan.Status != contractsv1.RemediationStatusApproved {
		t.Fatalf("expected approved dry-run plan, got %q", result.RemediationPlan.Status)
	}
	if result.Receipt.Outcome != "dry_run" {
		t.Fatalf("expected dry-run receipt, got %q", result.Receipt.Outcome)
	}
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatalf("dry run should not create env file, stat err=%v", err)
	}
}

func TestRunAppliesExplicitNonSecretDefaultAndWritesRollback(t *testing.T) {
	t.Parallel()

	envPath := filepath.Join(t.TempDir(), ".env")
	result, err := envvars.Run(context.Background(), envvars.Options{
		ServiceName: "orders-api",
		EnvFilePath: envPath,
		Policy: envvars.Policy{
			Variables: []envvars.VariableRequirement{
				{Name: "CACHE_DRIVER", Required: true, Secret: false, DefaultValue: "file", AllowDefaultWrite: true},
			},
		},
		LookupEnv: emptyEnv,
		Apply:     true,
		Now:       fixedTime(),
	})
	if err != nil {
		t.Fatalf("run env diagnostics apply: %v", err)
	}

	raw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	if string(raw) != "CACHE_DRIVER=file\n" {
		t.Fatalf("unexpected env file contents: %q", string(raw))
	}
	if result.RollbackPath == "" {
		t.Fatal("expected rollback path")
	}
	if _, err := os.Stat(result.RollbackPath); err != nil {
		t.Fatalf("rollback manifest should exist: %v", err)
	}
	if result.Attempt.Status != contractsv1.RemediationStatusSucceeded {
		t.Fatalf("expected succeeded attempt, got %q", result.Attempt.Status)
	}
	if result.Receipt.Outcome != "succeeded" {
		t.Fatalf("expected succeeded receipt, got %q", result.Receipt.Outcome)
	}
}

func emptyEnv(string) (string, bool) {
	return "", false
}

func fixedTime() time.Time {
	return time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
}
