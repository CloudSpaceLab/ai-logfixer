package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/demoapp"
)

func TestRunConfigModeAppliesRuntimeV2Remediation(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	configPath := filepath.Join(workDir, "app.json")
	logPath := filepath.Join(workDir, "app.log")
	if err := demoapp.WriteConfig(configPath, demoapp.Config{
		ServiceName: "goravel-demo",
		UpstreamURL: "http://127.0.0.1:1/orders",
	}); err != nil {
		t.Fatalf("write config: %v", err)
	}
	server := httptest.NewServer(demoapp.NewHandler(configPath, logPath))
	t.Cleanup(server.Close)
	for i := 0; i < 4; i++ {
		response, err := http.Get(server.URL + "/orders")
		if err != nil {
			t.Fatalf("request broken route: %v", err)
		}
		_ = response.Body.Close()
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{
		"-mode", "config",
		"-service", "goravel-demo",
		"-base-url", server.URL,
		"-config", configPath,
		"-log", logPath,
		"-route", "/orders",
		"-status", "503",
		"-config-key", "upstream_url",
		"-replacement-value", server.URL + "/upstream/orders",
		"-threshold", "3",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d stderr=%s", code, stderr.String())
	}
	var result output
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if result.RemediationPlan == nil || result.RemediationPlan.Status != contractsv1.RemediationStatusSucceeded {
		t.Fatalf("expected succeeded remediation plan, got %+v", result.RemediationPlan)
	}
	if !strings.Contains(stderr.String(), "Runtime V2 config completed") {
		t.Fatalf("expected config completion message, got %q", stderr.String())
	}
}

func TestRunConfigModeVerificationFailureEmitsStructuredJSON(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	configPath := filepath.Join(workDir, "app.json")
	logPath := filepath.Join(workDir, "app.log")
	originalUpstream := "http://127.0.0.1:1/orders"
	if err := demoapp.WriteConfig(configPath, demoapp.Config{
		ServiceName: "goravel-demo",
		UpstreamURL: originalUpstream,
	}); err != nil {
		t.Fatalf("write config: %v", err)
	}
	server := httptest.NewServer(demoapp.NewHandler(configPath, logPath))
	t.Cleanup(server.Close)
	for i := 0; i < 4; i++ {
		response, err := http.Get(server.URL + "/orders")
		if err != nil {
			t.Fatalf("request broken route: %v", err)
		}
		_ = response.Body.Close()
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{
		"-mode", "config",
		"-service", "goravel-demo",
		"-base-url", server.URL,
		"-config", configPath,
		"-log", logPath,
		"-route", "/orders",
		"-status", "503",
		"-config-key", "upstream_url",
		"-replacement-value", server.URL + "/upstream/orders",
		"-expected-status", "201",
		"-threshold", "3",
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d stderr=%s", code, stderr.String())
	}
	var result output
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode failure output: %v\n%s", err, stdout.String())
	}
	if result.RemediationPlan == nil || result.RemediationPlan.Status != contractsv1.RemediationStatusFailed {
		t.Fatalf("expected failed remediation plan, got %+v", result.RemediationPlan)
	}
	if result.Attempt == nil || result.Attempt.Status != contractsv1.RemediationStatusFailed {
		t.Fatalf("expected failed remediation attempt, got %+v", result.Attempt)
	}
	if result.Receipt == nil || result.Receipt.Outcome != "failed_rolled_back" {
		t.Fatalf("expected failed rollback receipt, got %+v", result.Receipt)
	}
	if !strings.Contains(stderr.String(), "verify fix") {
		t.Fatalf("expected verification error on stderr, got %q", stderr.String())
	}
	restored, err := demoapp.ReadConfig(configPath)
	if err != nil {
		t.Fatalf("read restored config: %v", err)
	}
	if restored.UpstreamURL != originalUpstream {
		t.Fatalf("expected rollback to restore upstream %q, got %q", originalUpstream, restored.UpstreamURL)
	}
}

func TestRunTruthModeStackTraceOutputsFixBundle(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	stackPath := filepath.Join(workDir, "stack.txt")
	stack := strings.Join([]string{
		"panic: payment failed token=abcd1234",
		"goroutine 7 [running]:",
		"payments/app.(*Service).Charge()",
		"\tC:/srv/payments/app/service.go:55 +0x21",
	}, "\n")
	if err := os.WriteFile(stackPath, []byte(stack), 0o644); err != nil {
		t.Fatalf("write stack trace: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{
		"-mode", "truth",
		"-service", "payments-api",
		"-framework", "go",
		"-environment", "staging",
		"-message", "payment failed token=abcd1234",
		"-stack-trace-file", stackPath,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d stderr=%s", code, stderr.String())
	}
	var result output
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if result.TruthRecovery == nil || result.TruthRecovery.FixBundle.ID == "" {
		t.Fatalf("expected truth recovery fix bundle, got %+v", result.TruthRecovery)
	}
	if strings.Contains(result.TruthRecovery.FixBundle.Prompt, "abcd1234") {
		t.Fatalf("fix bundle leaked token: %s", result.TruthRecovery.FixBundle.Prompt)
	}
	if !strings.Contains(stderr.String(), "Runtime V2 truth completed") {
		t.Fatalf("expected truth completion message, got %q", stderr.String())
	}
}

func TestRunTruthModeProductionRevealOutputsBlockedContracts(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	sourcePath := filepath.Join(workDir, "handler.go")
	if err := os.WriteFile(sourcePath, []byte(`package app

func Handler() {
    defer func() {
        if recover() != nil {
            log.Println("friendly error")
        }
    }()
}
`), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{
		"-mode", "truth",
		"-service", "checkout-api",
		"-framework", "go",
		"-environment", "production",
		"-message", "friendly error",
		"-source-file", sourcePath,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d stderr=%s", code, stderr.String())
	}
	var result output
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if result.TruthRecovery == nil || result.TruthRecovery.RevealPlan.Safe {
		t.Fatalf("expected blocked reveal plan, got %+v", result.TruthRecovery)
	}
	if result.RemediationPlan == nil || result.RemediationPlan.Status != contractsv1.RemediationStatusEscalated {
		t.Fatalf("expected escalated remediation plan, got %+v", result.RemediationPlan)
	}
	if result.Attempt == nil || result.Receipt == nil || result.Receipt.Outcome != "escalated" {
		t.Fatalf("expected escalated attempt and receipt, got attempt=%+v receipt=%+v", result.Attempt, result.Receipt)
	}
}
