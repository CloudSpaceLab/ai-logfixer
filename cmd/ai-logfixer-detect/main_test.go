package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRunDryRunEmitsContractValidIntake(t *testing.T) {
	t.Parallel()

	logPath := writeLog(t, `
2026-05-26T10:00:00Z service=checkout-api method=GET route=/checkout status=503
2026-05-26T10:00:15Z service=checkout-api method=GET route=/checkout status=503
2026-05-26T10:00:30Z service=checkout-api method=GET route=/checkout status=503
`)
	var stdout bytes.Buffer
	err := run(context.Background(), []string{
		"-log", logPath,
		"-service", "checkout-api",
		"-route", "/checkout",
		"-status", "503",
		"-threshold", "3",
		"-now", "2026-05-26T10:01:00Z",
	}, &stdout)
	if err != nil {
		t.Fatalf("expected dry run to succeed: %v", err)
	}

	var got output
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if got.Status != "started" || got.Mode != "dry_run" {
		t.Fatalf("unexpected status/mode: %+v", got)
	}
	if len(got.Signals) != 1 || len(got.Results) != 1 {
		t.Fatalf("expected one signal/result, got signals=%d results=%d", len(got.Signals), len(got.Results))
	}
	result := got.Results[0]
	if result.InvestigationRequest.Service != "checkout-api" {
		t.Fatalf("unexpected service: %s", result.InvestigationRequest.Service)
	}
	if result.InvestigationRequest.ErrorCode != "503" {
		t.Fatalf("unexpected error code: %s", result.InvestigationRequest.ErrorCode)
	}
	if result.Decision.Decision != "start_new" {
		t.Fatalf("unexpected decision: %s", result.Decision.Decision)
	}
	if result.Cluster.ID == "" || result.Branch.ClusterID != result.Cluster.ID {
		t.Fatalf("cluster/branch ids not wired: cluster=%s branch.cluster=%s", result.Cluster.ID, result.Branch.ClusterID)
	}
	if err := result.InvestigationRequest.Validate(); err != nil {
		t.Fatalf("investigation request should validate: %v", err)
	}
	if err := result.Decision.Validate(); err != nil {
		t.Fatalf("investigation decision should validate: %v", err)
	}
}

func TestRunBelowThresholdNoOps(t *testing.T) {
	t.Parallel()

	logPath := writeLog(t, `
2026-05-26T10:00:00Z service=checkout-api method=GET route=/checkout status=503
2026-05-26T10:00:15Z service=checkout-api method=GET route=/checkout status=503
`)
	var stdout bytes.Buffer
	err := run(context.Background(), []string{
		"-log", logPath,
		"-service", "checkout-api",
		"-route", "/checkout",
		"-status", "503",
		"-threshold", "3",
	}, &stdout)
	if err != nil {
		t.Fatalf("expected below-threshold run to succeed: %v", err)
	}

	var got output
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if got.Status != "no_signal" {
		t.Fatalf("expected no_signal, got %+v", got)
	}
	if len(got.Signals) != 0 || len(got.Results) != 0 {
		t.Fatalf("expected no signal/results, got signals=%d results=%d", len(got.Signals), len(got.Results))
	}
}

func TestRunPersistRequiresStoreConfiguration(t *testing.T) {
	t.Parallel()

	logPath := writeLog(t, `
2026-05-26T10:00:00Z service=checkout-api method=GET route=/checkout status=503
2026-05-26T10:00:15Z service=checkout-api method=GET route=/checkout status=503
2026-05-26T10:00:30Z service=checkout-api method=GET route=/checkout status=503
`)
	var stdout bytes.Buffer
	err := run(context.Background(), []string{
		"-log", logPath,
		"-service", "checkout-api",
		"-persist=true",
	}, &stdout)
	if err == nil {
		t.Fatal("expected persist mode to require postgres configuration")
	}
}

func writeLog(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "access.log")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	return path
}
