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
	if strings.Contains(stdout.String(), "abcd1234") {
		t.Fatalf("truth recovery public output leaked token:\n%s", stdout.String())
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

func TestRunPermissionsModeAppliesLaravelPermissionRepair(t *testing.T) {
	t.Parallel()

	appDir := newPermissionLaravelApp(t)
	logsDir := filepath.Join(appDir, "storage", "logs")
	if err := os.Chmod(logsDir, 0o500); err != nil {
		t.Fatalf("break logs dir mode: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := os.WriteFile(filepath.Join(logsDir, "orders.log"), []byte("ok\n"), 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("orders ok"))
	}))
	t.Cleanup(server.Close)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{
		"-mode", "permissions",
		"-service", "orders-api",
		"-target", appDir,
		"-framework", "auto",
		"-verify-url", server.URL,
		"-expected-status", "200",
		"-apply",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d stderr=%s", code, stderr.String())
	}

	var result output
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if result.RemediationPlan == nil || result.RemediationPlan.Status != contractsv1.RemediationStatusApproved {
		t.Fatalf("expected approved permissions remediation plan, got %+v", result.RemediationPlan)
	}
	if result.Attempt == nil || result.Attempt.Status != contractsv1.RemediationStatusSucceeded {
		t.Fatalf("expected succeeded permissions attempt, got %+v", result.Attempt)
	}
	if result.Receipt == nil || result.Receipt.Outcome != "succeeded" {
		t.Fatalf("expected succeeded permissions receipt, got %+v", result.Receipt)
	}
	if !strings.Contains(stderr.String(), "Runtime V2 permissions completed") {
		t.Fatalf("expected permissions completion message, got %q", stderr.String())
	}
	info, err := os.Stat(logsDir)
	if err != nil {
		t.Fatalf("stat repaired logs dir: %v", err)
	}
	if info.Mode().Perm() != 0o775 {
		t.Fatalf("expected logs dir mode 0775, got %04o", info.Mode().Perm())
	}
}

func TestRunEnvvarsModeAppliesNonSecretDefaultFromInputFile(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	envPath := filepath.Join(workDir, ".env")
	result, stderr := runInputMode(t, "envvars", map[string]any{
		"service_name":  "orders-api",
		"env_file_path": envPath,
		"apply":         true,
		"environment":   map[string]string{},
		"policy": map[string]any{
			"variables": []map[string]any{
				{"name": "CACHE_DRIVER", "required": true, "secret": false, "default_value": "file", "allow_default_write": true},
			},
		},
	})

	if _, ok := result["envvars"].(map[string]any); !ok {
		t.Fatalf("expected envvars result, got %+v stderr=%s", result, stderr)
	}
	raw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	if string(raw) != "CACHE_DRIVER=file\n" {
		t.Fatalf("unexpected env contents: %q", string(raw))
	}
	if !strings.Contains(stderr, "Runtime V2 envvars completed") {
		t.Fatalf("expected envvars completion message, got %q", stderr)
	}
}

func TestRunDatabaseModeDiagnosesDriftFromInputFile(t *testing.T) {
	t.Parallel()

	result, _ := runInputMode(t, "database", map[string]any{
		"service_name":      "orders-api",
		"database_url":      "postgres://orders:secret@127.0.0.1:5432/wrongdb?sslmode=disable",
		"allowed_schemes":   []string{"postgres"},
		"allowed_hosts":     []string{"db.internal"},
		"required_database": "orders",
		"required_tables": []map[string]any{
			{"name": "orders", "columns": []string{"id", "total_cents", "customer_id"}},
		},
		"observed_tables": []map[string]any{
			{"name": "orders", "columns": []string{"id", "total_cents"}},
		},
		"connection_probe": map[string]any{"checked": true, "ok": false, "error": "password authentication failed"},
	})

	database, ok := result["database"].(map[string]any)
	if !ok {
		t.Fatalf("expected database result, got %+v", result)
	}
	if database["status"] != "drift_detected" {
		t.Fatalf("expected drift status, got %+v", database)
	}
	if strings.Contains(mustJSON(t, database), "secret") {
		t.Fatalf("database output leaked credential: %+v", database)
	}
}

func TestRunResourcesModeCreatesAllowlistedRuntimeDirectory(t *testing.T) {
	t.Parallel()

	appRoot := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	result, _ := runInputMode(t, "resources", map[string]any{
		"app_root":           appRoot,
		"resource_path":      "storage/framework/cache/data",
		"allowlist":          []string{"storage/framework/cache/data"},
		"kind":               "dir",
		"mode":               488,
		"verify_url":         server.URL,
		"expected_status":    200,
		"manifest_base_name": "resource-cli-test",
	})

	resource, ok := result["resource"].(map[string]any)
	if !ok {
		t.Fatalf("expected resource result, got %+v", result)
	}
	if resource["applied"] != true || resource["verified"] != true {
		t.Fatalf("expected applied and verified resource result, got %+v", resource)
	}
	if _, err := os.Stat(filepath.Join(appRoot, "storage", "framework", "cache", "data")); err != nil {
		t.Fatalf("expected runtime resource directory: %v", err)
	}
}

func TestRunRestartModeRunsAllowlistedActionFromInputFile(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	stalePath := filepath.Join(workDir, "serving-stale-config")
	logPath := filepath.Join(workDir, "orders.log")
	restartScript := filepath.Join(workDir, "restart-orders.sh")
	verifyScript := filepath.Join(workDir, "verify-orders.sh")
	if err := os.WriteFile(stalePath, []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("write stale marker: %v", err)
	}
	writeRepeatedHTTPFailures(t, logPath, "orders-api", "/orders/readiness", 4)
	writeExecutable(t, restartScript, "#!/bin/sh\nset -eu\nrm -f \"$1\"\n")
	writeExecutable(t, verifyScript, "#!/bin/sh\nset -eu\nif [ -e \"$1\" ]; then exit 1; fi\nprintf 'runtime healthy after restart\\n'\n")

	result, _ := runInputMode(t, "restart", map[string]any{
		"service_name":    "orders-api",
		"log_path":        logPath,
		"route":           "/orders/readiness",
		"status_code":     503,
		"error_threshold": 3,
		"action_name":     "restart-runtime",
		"policy": map[string]any{
			"allowed_actions": []map[string]any{
				{
					"name":         "restart-runtime",
					"service_name": "orders-api",
					"command":      map[string]any{"path": restartScript, "args": []string{stalePath}},
				},
			},
		},
		"verification": map[string]any{
			"command": map[string]any{
				"command":         map[string]any{"path": verifyScript, "args": []string{stalePath}},
				"output_contains": "runtime healthy",
			},
		},
	})

	restart, ok := result["restart"].(map[string]any)
	if !ok {
		t.Fatalf("expected restart result, got %+v", result)
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("expected restart action to remove stale marker, stat err=%v", err)
	}
	if restart["receipt"] == nil {
		t.Fatalf("expected restart receipt, got %+v", restart)
	}
}

func TestRunTokensModeRedactsTokenEvidenceFromInputFile(t *testing.T) {
	t.Parallel()

	secret := "sk_test_should_not_leak"
	result, _ := runInputMode(t, "tokens", map[string]any{
		"service_name":  "orders-api",
		"provider":      "stripe",
		"token_name":    "STRIPE_API_KEY",
		"token_present": false,
		"token_value":   secret,
		"probe":         map[string]any{"checked": true, "status_code": 401, "body": "invalid bearer " + secret},
	})

	token, ok := result["token"].(map[string]any)
	if !ok {
		t.Fatalf("expected token result, got %+v", result)
	}
	if token["status"] != "token_drift_detected" {
		t.Fatalf("expected token drift, got %+v", token)
	}
	if strings.Contains(mustJSON(t, result), secret) {
		t.Fatalf("token diagnostics leaked secret: %+v", result)
	}
}

func TestRunVersionsModeDiagnosesMismatchFromInputFile(t *testing.T) {
	t.Parallel()

	result, _ := runInputMode(t, "versions", map[string]any{
		"service_name": "orders-api",
		"required": []map[string]any{
			{"kind": "runtime", "name": "node", "constraint": ">=20.0.0"},
			{"kind": "package", "name": "express", "constraint": "^5.0.0"},
		},
		"observed": []map[string]any{
			{"kind": "runtime", "name": "node", "version": "18.19.0", "source": "process.version"},
			{"kind": "package", "name": "express", "version": "4.18.3", "source": "package-lock.json"},
		},
	})

	versions, ok := result["versions"].(map[string]any)
	if !ok {
		t.Fatalf("expected versions result, got %+v", result)
	}
	if versions["status"] != "version_mismatch_detected" {
		t.Fatalf("expected version mismatch, got %+v", versions)
	}
}

func newPermissionLaravelApp(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "artisan"), []byte("#!/usr/bin/env php\n"), 0o755); err != nil {
		t.Fatalf("write artisan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "composer.json"), []byte(`{"require":{"laravel/framework":"^11.0"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write composer.json: %v", err)
	}
	for _, dir := range []string{
		"storage",
		"storage/logs",
		"storage/framework/cache",
		"storage/framework/sessions",
		"storage/framework/views",
		"bootstrap/cache",
	} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o775); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	return root
}

func runInputMode(t *testing.T, mode string, input map[string]any) (map[string]any, string) {
	t.Helper()
	inputPath := filepath.Join(t.TempDir(), mode+"-input.json")
	raw, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	if err := os.WriteFile(inputPath, append(raw, '\n'), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"-mode", mode, "-input", inputPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected %s mode exit 0, got %d stderr=%s stdout=%s", mode, code, stderr.String(), stdout.String())
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode %s output: %v\n%s", mode, err, stdout.String())
	}
	return result, stderr.String()
}

func writeRepeatedHTTPFailures(t *testing.T, path string, service string, route string, count int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create log parent: %v", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer file.Close()
	for i := 0; i < count; i++ {
		if _, err := file.WriteString("2026-06-08T09:00:00Z level=error service=" + service + " method=GET route=" + route + " status=503\n"); err != nil {
			t.Fatalf("write log: %v", err)
		}
	}
}

func writeExecutable(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return string(raw)
}
