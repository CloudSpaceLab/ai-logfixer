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

	"github.com/CloudSpaceLab/ai-logfixer/internal/readinessresolve"
)

func TestRunReadsCandidateInputFile(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	inputPath := filepath.Join(workDir, "candidate.json")
	payload := map[string]any{
		"scenario_id":           "unknown-api",
		"operational_lane":      "unknown-lane",
		"runtime":               "go",
		"app_carrier":           "net-http",
		"service_name":          "unknown-api",
		"docker_service":        "unknown-api",
		"app_dir":               filepath.Join(workDir, "app"),
		"policy_file":           filepath.Join(workDir, "policy.json"),
		"trace_file":            filepath.Join(workDir, "trace.log"),
		"live_probe_url":        "http://127.0.0.1:18084/orders/readiness",
		"expected_fixed_status": http.StatusOK,
		"fixed_body_contains":   "FIXED",
		"safe_action":           "structured unsupported response",
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal candidate input: %v", err)
	}
	if err := os.WriteFile(inputPath, raw, 0o644); err != nil {
		t.Fatalf("write candidate input: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"-input", inputPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d stderr=%s", code, stderr.String())
	}

	var response readinessresolve.Response
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decode stdout: %v\n%s", err, stdout.String())
	}
	if response.ScenarioID != "unknown-api" || response.Status != readinessresolve.StatusUnsupported {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestRunEmitsPermissionRollbackEvidenceWhenVerificationFails(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	appDir := filepath.Join(workDir, "app")
	logDir := filepath.Join(appDir, "storage", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("create log dir: %v", err)
	}
	if err := os.Chmod(logDir, 0o555); err != nil {
		t.Fatalf("chmod log dir: %v", err)
	}
	policyPath := filepath.Join(workDir, "policy.json")
	writeJSON(t, policyPath, map[string]any{
		"lane":           "permission-drift",
		"allowed_paths":  []string{"storage/logs"},
		"expected_mode":  "0775",
		"expected_owner": "app",
		"expected_group": "app",
		"verification": map[string]any{
			"method":          "http",
			"expected_status": http.StatusOK,
			"body_contains":   "FIXED",
		},
	})
	tracePath := filepath.Join(workDir, "trace.log")
	if err := os.WriteFile(tracePath, []byte("permission drift: write storage/logs/audit.log: permission denied\n"), 0o644); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "still broken", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	inputPath := filepath.Join(workDir, "candidate.json")
	writeJSON(t, inputPath, map[string]any{
		"scenario_id":           "permission-drift-cli-rollback",
		"operational_lane":      "permission-drift",
		"runtime":               "go",
		"app_carrier":           "net-http",
		"service_name":          "permission-drift-cli-rollback",
		"app_dir":               appDir,
		"policy_file":           policyPath,
		"trace_file":            tracePath,
		"live_probe_url":        server.URL + "/orders/readiness",
		"expected_fixed_status": http.StatusOK,
		"fixed_body_contains":   "FIXED",
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"-input", inputPath}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}

	var response readinessresolve.Response
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decode stdout: %v\n%s", err, stdout.String())
	}
	if response.Status != readinessresolve.StatusFailed || response.RollbackPath == "" || len(response.PermissionChanges) != 1 {
		t.Fatalf("expected failed response with rollback evidence, got %+v", response)
	}
	if response.Attempt == nil || string(response.Attempt.Status) != "rolled_back" {
		t.Fatalf("expected rolled back attempt, got %+v", response.Attempt)
	}
	if !strings.Contains(stderr.String(), "rolled back") {
		t.Fatalf("expected rollback message on stderr, got %q", stderr.String())
	}
	info, err := os.Stat(logDir)
	if err != nil {
		t.Fatalf("stat log dir: %v", err)
	}
	if info.Mode().Perm() != 0o555 {
		t.Fatalf("failed verification must restore original mode 0555, got %04o", info.Mode().Perm())
	}
}

func writeJSON(t *testing.T, path string, value map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
