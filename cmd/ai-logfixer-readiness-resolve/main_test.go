package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/CloudSpaceLab/ai-logfixer/internal/readinessresolve"
)

func TestRunReadsCandidateInputFile(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	inputPath := filepath.Join(workDir, "candidate.json")
	payload := map[string]any{
		"scenario_id":           "restart-reload-api",
		"operational_lane":      "restart-reload",
		"runtime":               "go",
		"app_carrier":           "net-http",
		"service_name":          "restart-reload-api",
		"docker_service":        "restart-reload-api",
		"app_dir":               filepath.Join(workDir, "app"),
		"policy_file":           filepath.Join(workDir, "policy.json"),
		"trace_file":            filepath.Join(workDir, "trace.log"),
		"live_probe_url":        "http://127.0.0.1:18084/orders/readiness",
		"expected_fixed_status": http.StatusOK,
		"fixed_body_contains":   "FIXED",
		"safe_action":           "restart the smallest allowlisted service target",
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
	if response.ScenarioID != "restart-reload-api" || response.Status != readinessresolve.StatusUnsupported {
		t.Fatalf("unexpected response: %+v", response)
	}
}
