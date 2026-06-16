package readinessresolve_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CloudSpaceLab/ai-logfixer/internal/readinessresolve"
)

func TestLoadCandidateInputValidatesRequiredFields(t *testing.T) {
	t.Parallel()

	inputPath := filepath.Join(t.TempDir(), "candidate.json")
	if err := os.WriteFile(inputPath, []byte(`{"scenario_id":"config-drift-api","operational_lane":"config-drift"}`), 0o644); err != nil {
		t.Fatalf("write candidate input: %v", err)
	}

	_, err := readinessresolve.LoadCandidateInput(inputPath)
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, field := range []string{"service_name", "app_dir", "policy_file", "trace_file", "live_probe_url"} {
		if !strings.Contains(err.Error(), field+" is required") {
			t.Fatalf("expected missing %s validation, got %v", field, err)
		}
	}
}

func TestResolveUnsupportedLaneReturnsStructuredResponse(t *testing.T) {
	t.Parallel()

	response, err := readinessresolve.Resolve(context.Background(), readinessresolve.CandidateInput{
		ScenarioID:          "unknown-api",
		OperationalLane:     "unknown-lane",
		ServiceName:         "unknown-api",
		AppDir:              "/tmp/app",
		PolicyFile:          "/tmp/policy.json",
		TraceFile:           "/tmp/trace.log",
		LiveProbeURL:        "http://127.0.0.1:18083/orders/readiness",
		ExpectedFixedStatus: http.StatusOK,
	})
	if err != nil {
		t.Fatalf("resolve unsupported lane: %v", err)
	}
	if response.Status != readinessresolve.StatusUnsupported {
		t.Fatalf("expected unsupported status, got %+v", response)
	}
	if response.Supported {
		t.Fatalf("unsupported lane should not be marked supported: %+v", response)
	}
	if !strings.Contains(response.Message, "not implemented on main") {
		t.Fatalf("expected implementation status in message, got %q", response.Message)
	}
}

func TestResolvePackageRegressionRollsBackTrustedPackage(t *testing.T) {
	t.Parallel()

	appDir := t.TempDir()
	writeJSONFile(t, filepath.Join(appDir, "package.json"), map[string]any{
		"name": "package-regression-api",
		"dependencies": map[string]any{
			"@acme/tax-client": "file:packages/tax-client-2.0.0",
		},
	})
	writeJSONFile(t, filepath.Join(appDir, "evidence", "package-history.json"), map[string]any{
		"package":         "@acme/tax-client",
		"current":         "file:packages/tax-client-2.0.0",
		"last_known_good": "file:packages/tax-client-1.0.0",
	})
	policyPath := filepath.Join(appDir, "policy.json")
	writeJSONFile(t, policyPath, map[string]any{
		"lane":             "package-regression",
		"allowed_files":    []string{"package.json"},
		"allowed_packages": []string{"@acme/tax-client"},
		"trusted_sources":  []string{"evidence/package-history.json"},
		"verification": map[string]any{
			"method":          "http",
			"expected_status": http.StatusOK,
			"body_contains":   "FIXED",
		},
	})
	tracePath := filepath.Join(appDir, "trace.log")
	if err := os.WriteFile(tracePath, []byte("package regression in @acme/tax-client\n"), 0o644); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/orders/readiness" {
			http.NotFound(writer, request)
			return
		}
		config := readJSONFile(t, filepath.Join(appDir, "package.json"))
		dependencies := config["dependencies"].(map[string]any)
		if dependencies["@acme/tax-client"] != "file:packages/tax-client-1.0.0" {
			http.Error(writer, "package regression", http.StatusInternalServerError)
			return
		}
		_, _ = writer.Write([]byte(`{"status":"FIXED"}`))
	}))
	t.Cleanup(server.Close)

	response, err := readinessresolve.Resolve(context.Background(), readinessresolve.CandidateInput{
		ScenarioID:          "package-regression-api",
		OperationalLane:     "package-regression",
		ServiceName:         "package-regression-api",
		AppDir:              appDir,
		PolicyFile:          policyPath,
		TraceFile:           tracePath,
		LiveProbeURL:        server.URL + "/orders/readiness",
		ExpectedFixedStatus: http.StatusOK,
		FixedBodyContains:   "FIXED",
	})
	if err != nil {
		t.Fatalf("resolve package regression: %v", err)
	}
	if response.Status != readinessresolve.StatusResolved || !response.Supported {
		t.Fatalf("expected resolved package response, got %+v", response)
	}
	config := readJSONFile(t, filepath.Join(appDir, "package.json"))
	dependencies := config["dependencies"].(map[string]any)
	if dependencies["@acme/tax-client"] != "file:packages/tax-client-1.0.0" {
		t.Fatalf("expected package rollback, got %+v", dependencies["@acme/tax-client"])
	}
}

func TestResolvePermissionDriftRepairsAllowlistedPath(t *testing.T) {
	t.Parallel()

	appDir := t.TempDir()
	logDir := filepath.Join(appDir, "storage", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("create log dir: %v", err)
	}
	if err := os.Chmod(logDir, 0o555); err != nil {
		t.Fatalf("chmod log dir: %v", err)
	}
	policyPath := filepath.Join(appDir, "policy.json")
	writeJSONFile(t, policyPath, map[string]any{
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
	tracePath := filepath.Join(appDir, "trace.log")
	if err := os.WriteFile(tracePath, []byte(`127.0.0.1 - - [09/Jun/2026] "GET /orders/readiness HTTP/1.1" 500 -`+"\n"), 0o644); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := os.WriteFile(filepath.Join(logDir, "audit.log"), []byte("ok\n"), 0o644); err != nil {
			http.Error(writer, "permission drift: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = writer.Write([]byte(`{"status":"FIXED"}`))
	}))
	t.Cleanup(server.Close)

	response, err := readinessresolve.Resolve(context.Background(), readinessresolve.CandidateInput{
		ScenarioID:          "permission-drift-local",
		OperationalLane:     "permission-drift",
		ServiceName:         "permission-drift-local",
		AppDir:              appDir,
		PolicyFile:          policyPath,
		TraceFile:           tracePath,
		LiveProbeURL:        server.URL + "/orders/readiness",
		ExpectedFixedStatus: http.StatusOK,
		FixedBodyContains:   "FIXED",
	})
	if err != nil {
		t.Fatalf("resolve permission drift: %v", err)
	}
	if response.Status != readinessresolve.StatusResolved || !response.Supported {
		t.Fatalf("expected resolved permission response, got %+v", response)
	}
	info, err := os.Stat(logDir)
	if err != nil {
		t.Fatalf("stat log dir: %v", err)
	}
	if info.Mode().Perm() != 0o775 {
		t.Fatalf("expected permission repair to set 0775, got %04o", info.Mode().Perm())
	}
	if response.RollbackPath == "" {
		t.Fatalf("expected permission rollback path, got %+v", response)
	}
	if response.RemediationPlan == nil || response.RemediationPlan.RollbackPlan.SnapshotRefs[0] != response.RollbackPath {
		t.Fatalf("expected remediation plan to reference rollback path, got %+v", response.RemediationPlan)
	}
	if response.Attempt == nil || response.Attempt.Status != "succeeded" {
		t.Fatalf("expected succeeded remediation attempt, got %+v", response.Attempt)
	}
	if response.Receipt == nil || !strings.Contains(response.Receipt.BeforeState, "storage/logs") || !strings.Contains(response.Receipt.AfterState, "0775") {
		t.Fatalf("expected permission receipt with before/after path state, got %+v", response.Receipt)
	}
	if len(response.PermissionChanges) != 1 {
		t.Fatalf("expected one permission change, got %+v", response.PermissionChanges)
	}
	change := response.PermissionChanges[0]
	if change.Path != "storage/logs" || change.Action != "repair_dir_permissions" || change.BeforeMode != "0555" || change.AfterMode != "0775" {
		t.Fatalf("expected exact permission repair receipt, got %+v", change)
	}
	rawRollback, err := os.ReadFile(response.RollbackPath)
	if err != nil {
		t.Fatalf("read permission rollback manifest: %v", err)
	}
	if !strings.Contains(string(rawRollback), "storage/logs") || !strings.Contains(string(rawRollback), "0555") {
		t.Fatalf("expected rollback manifest to preserve original mode, got %s", rawRollback)
	}
}

func TestResolvePermissionDriftInfersFrameworkTargetsWhenPolicyOmitsTargets(t *testing.T) {
	t.Parallel()

	appDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(appDir, "artisan"), []byte("#!/usr/bin/env php\n"), 0o755); err != nil {
		t.Fatalf("write artisan marker: %v", err)
	}
	writeJSONFile(t, filepath.Join(appDir, "composer.json"), map[string]any{
		"require": map[string]any{"laravel/framework": "^11.0"},
	})
	logDir := filepath.Join(appDir, "storage", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("create log dir: %v", err)
	}
	if err := os.Chmod(logDir, 0o555); err != nil {
		t.Fatalf("chmod log dir: %v", err)
	}
	policyPath := filepath.Join(appDir, "policy.json")
	writeJSONFile(t, policyPath, map[string]any{
		"lane":           "permission-drift",
		"expected_owner": "app",
		"expected_group": "app",
		"verification": map[string]any{
			"method":          "http",
			"expected_status": http.StatusOK,
			"body_contains":   "FIXED",
		},
	})
	tracePath := filepath.Join(appDir, "trace.log")
	if err := os.WriteFile(tracePath, []byte("permission drift: storage/logs/audit.log: permission denied\n"), 0o644); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := os.WriteFile(filepath.Join(logDir, "audit.log"), []byte("ok\n"), 0o644); err != nil {
			http.Error(writer, "permission drift: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = writer.Write([]byte(`{"status":"FIXED"}`))
	}))
	t.Cleanup(server.Close)

	response, err := readinessresolve.Resolve(context.Background(), readinessresolve.CandidateInput{
		ScenarioID:          "permission-drift-laravel-inferred",
		OperationalLane:     "permission-drift",
		ServiceName:         "permission-drift-laravel-inferred",
		AppDir:              appDir,
		PolicyFile:          policyPath,
		TraceFile:           tracePath,
		LiveProbeURL:        server.URL + "/orders/readiness",
		ExpectedFixedStatus: http.StatusOK,
		FixedBodyContains:   "FIXED",
	})
	if err != nil {
		t.Fatalf("resolve inferred permission drift: %v", err)
	}
	if response.Status != readinessresolve.StatusResolved || !response.Supported {
		t.Fatalf("expected resolved inferred permission response, got %+v", response)
	}
	if got := modePerm(t, logDir); got != 0o775 {
		t.Fatalf("expected inferred Laravel storage/logs repair to set 0775, got %04o", got)
	}
	if response.RollbackPath == "" {
		t.Fatalf("expected inferred repair rollback path, got %+v", response)
	}
	var logChange *readinessresolve.PermissionChange
	for index := range response.PermissionChanges {
		if response.PermissionChanges[index].Path == "storage/logs" {
			logChange = &response.PermissionChanges[index]
			break
		}
	}
	if logChange == nil {
		t.Fatalf("expected inferred storage/logs permission change, got %+v", response.PermissionChanges)
	}
	if logChange.Action != "repair_dir_permissions" || logChange.BeforeMode != "0555" || logChange.AfterMode != "0775" {
		t.Fatalf("expected exact inferred storage/logs repair receipt, got %+v", logChange)
	}
}

func TestResolvePermissionDriftCreatesMissingAllowlistedPath(t *testing.T) {
	t.Parallel()

	appDir := t.TempDir()
	logDir := filepath.Join(appDir, "storage", "logs")
	policyPath := filepath.Join(appDir, "policy.json")
	writeJSONFile(t, policyPath, map[string]any{
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
	tracePath := filepath.Join(appDir, "trace.log")
	if err := os.WriteFile(tracePath, []byte("permission drift: missing log directory\n"), 0o644); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := os.WriteFile(filepath.Join(logDir, "audit.log"), []byte("ok\n"), 0o644); err != nil {
			http.Error(writer, "permission drift: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = writer.Write([]byte(`{"status":"FIXED"}`))
	}))
	t.Cleanup(server.Close)

	response, err := readinessresolve.Resolve(context.Background(), readinessresolve.CandidateInput{
		ScenarioID:          "permission-drift-missing-local",
		OperationalLane:     "permission-drift",
		ServiceName:         "permission-drift-missing-local",
		AppDir:              appDir,
		PolicyFile:          policyPath,
		TraceFile:           tracePath,
		LiveProbeURL:        server.URL + "/orders/readiness",
		ExpectedFixedStatus: http.StatusOK,
		FixedBodyContains:   "FIXED",
	})
	if err != nil {
		t.Fatalf("resolve missing permission path: %v", err)
	}
	if response.Status != readinessresolve.StatusResolved || !response.Supported {
		t.Fatalf("expected resolved missing permission response, got %+v", response)
	}
	info, err := os.Stat(logDir)
	if err != nil {
		t.Fatalf("missing allowlisted path should be created: %v", err)
	}
	if info.Mode().Perm() != 0o775 {
		t.Fatalf("expected created path mode 0775, got %04o", info.Mode().Perm())
	}
}

func TestResolvePermissionDriftRollsBackLocalRepairWhenVerificationFails(t *testing.T) {
	t.Parallel()

	appDir := t.TempDir()
	logDir := filepath.Join(appDir, "storage", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("create log dir: %v", err)
	}
	if err := os.Chmod(logDir, 0o555); err != nil {
		t.Fatalf("chmod log dir: %v", err)
	}
	policyPath := filepath.Join(appDir, "policy.json")
	writeJSONFile(t, policyPath, map[string]any{
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
	tracePath := filepath.Join(appDir, "trace.log")
	if err := os.WriteFile(tracePath, []byte("permission drift: write storage/logs/audit.log: permission denied\n"), 0o644); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "still broken", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	t.Cleanup(cancel)
	response, err := readinessresolve.Resolve(ctx, readinessresolve.CandidateInput{
		ScenarioID:          "permission-drift-rollback-local",
		OperationalLane:     "permission-drift",
		ServiceName:         "permission-drift-rollback-local",
		AppDir:              appDir,
		PolicyFile:          policyPath,
		TraceFile:           tracePath,
		LiveProbeURL:        server.URL + "/orders/readiness",
		ExpectedFixedStatus: http.StatusOK,
		FixedBodyContains:   "FIXED",
	})
	if err == nil {
		t.Fatal("expected failed verification error")
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("expected rollback result in error, got %v", err)
	}
	info, err := os.Stat(logDir)
	if err != nil {
		t.Fatalf("stat log dir: %v", err)
	}
	if info.Mode().Perm() != 0o555 {
		t.Fatalf("failed verification must roll back original mode 0555, got %04o", info.Mode().Perm())
	}
	if response.Status != readinessresolve.StatusFailed || !response.Supported {
		t.Fatalf("expected structured failed permission response, got %+v", response)
	}
	if response.RollbackPath == "" || len(response.PermissionChanges) != 1 {
		t.Fatalf("expected rollback evidence and permission changes, got %+v", response)
	}
	if response.Attempt == nil || string(response.Attempt.Status) != "rolled_back" {
		t.Fatalf("expected rolled back remediation attempt, got %+v", response.Attempt)
	}
	if response.Receipt == nil || response.Receipt.Outcome != "rolled_back" || !strings.Contains(response.Receipt.AfterState, "0555") {
		t.Fatalf("expected rolled back receipt with restored state, got %+v", response.Receipt)
	}
}

func TestResolvePermissionDriftRestartsAllowlistedDockerServiceAfterStaleVerification(t *testing.T) {
	appDir := t.TempDir()
	stateDir := filepath.Join(appDir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("create state dir: %v", err)
	}
	permissionFixedMarker := filepath.Join(stateDir, "permission-fixed")
	restartNeededMarker := filepath.Join(stateDir, "restart-needed")
	if err := os.WriteFile(restartNeededMarker, []byte("restart required\n"), 0o644); err != nil {
		t.Fatalf("write restart marker: %v", err)
	}
	policyPath := filepath.Join(appDir, "policy.json")
	writeJSONFile(t, policyPath, map[string]any{
		"lane":                    "permission-drift",
		"allowed_paths":           []string{"storage"},
		"expected_mode":           "0775",
		"expected_owner":          "app",
		"expected_group":          "app",
		"allowed_restart_targets": []string{"permission-drift-restart-api"},
		"verification": map[string]any{
			"method":          "http",
			"expected_status": http.StatusOK,
			"body_contains":   "FIXED",
		},
	})
	tracePath := filepath.Join(appDir, "trace.log")
	if err := os.WriteFile(tracePath, []byte("permission drift: storage not writable; process cache still stale after repair\n"), 0o644); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if _, err := os.Stat(permissionFixedMarker); os.IsNotExist(err) {
			http.Error(writer, "permission denied", http.StatusInternalServerError)
			return
		}
		if _, err := os.Stat(restartNeededMarker); err == nil {
			http.Error(writer, "restart required", http.StatusServiceUnavailable)
			return
		}
		_, _ = writer.Write([]byte(`{"status":"FIXED"}`))
	}))
	t.Cleanup(server.Close)

	fakeBin := t.TempDir()
	dockerPath := filepath.Join(fakeBin, "docker")
	script := `#!/bin/sh
set -eu
if [ "$1" != "compose" ]; then
  echo unexpected docker args: "$@" >&2
  exit 1
fi
if [ "$6" = "restart" ]; then
  test "$7" = "permission-drift-restart-api"
  test -f "$AI_LOGFIXER_TEST_PERMISSION_FIXED_MARKER"
  rm -f "$AI_LOGFIXER_TEST_RESTART_MARKER"
  exit 0
fi
script="${13:-}"
case "$script" in
  *"stat -c"*)
    if [ -f "$AI_LOGFIXER_TEST_PERMISSION_FIXED_MARKER" ]; then
      printf 'exists\tdirectory\t775\t10001\t10001\n'
    else
      printf 'exists\tdirectory\t555\t0\t0\n'
    fi
    exit 0
    ;;
  *"chmod '0775' '/app/storage'"*)
    : > "$AI_LOGFIXER_TEST_PERMISSION_FIXED_MARKER"
    exit 0
    ;;
esac
echo unexpected docker exec script: "$script" >&2
exit 1
`
	if err := os.WriteFile(dockerPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AI_LOGFIXER_TEST_PERMISSION_FIXED_MARKER", permissionFixedMarker)
	t.Setenv("AI_LOGFIXER_TEST_RESTART_MARKER", restartNeededMarker)

	response, err := readinessresolve.Resolve(context.Background(), readinessresolve.CandidateInput{
		ScenarioID:          "permission-drift-restart-api",
		OperationalLane:     "permission-drift",
		ServiceName:         "permission-drift-restart-api",
		DockerService:       "permission-drift-restart-api",
		AppDir:              appDir,
		PolicyFile:          policyPath,
		TraceFile:           tracePath,
		ComposeFile:         filepath.Join(appDir, "docker-compose.yml"),
		ComposeProject:      "test-project",
		LiveProbeURL:        server.URL + "/orders/readiness",
		ExpectedFixedStatus: http.StatusOK,
		FixedBodyContains:   "FIXED",
	})
	if err != nil {
		t.Fatalf("resolve restart-sensitive permission drift: %v", err)
	}
	if response.Status != readinessresolve.StatusResolved || !response.Supported {
		t.Fatalf("expected resolved permission response after restart, got %+v", response)
	}
	if response.RestartedService != "permission-drift-restart-api" {
		t.Fatalf("expected restart evidence on response, got %+v", response)
	}
	if _, err := os.Stat(restartNeededMarker); !os.IsNotExist(err) {
		t.Fatalf("expected allowlisted restart to clear stale process marker, stat err=%v", err)
	}
	if response.Receipt == nil || !strings.Contains(response.Receipt.ActionTaken, "restart permission-drift-restart-api") {
		t.Fatalf("expected receipt to include restart evidence, got %+v", response.Receipt)
	}
}

func TestResolvePermissionDriftCreatesMissingDockerReadFileUnderAllowlistedRuntimeDir(t *testing.T) {
	appDir := t.TempDir()
	stateDir := filepath.Join(appDir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("create state dir: %v", err)
	}
	dataDirMarker := filepath.Join(stateDir, "data-dir")
	readFileMarker := filepath.Join(stateDir, "readiness-json")
	writeFileMarker := filepath.Join(stateDir, "startup-lock")
	restartNeededMarker := filepath.Join(stateDir, "restart-needed")
	if err := os.WriteFile(restartNeededMarker, []byte("restart required\n"), 0o644); err != nil {
		t.Fatalf("write restart marker: %v", err)
	}
	policyPath := filepath.Join(appDir, "policy.json")
	writeJSONFile(t, policyPath, map[string]any{
		"lane": "permission-drift",
		"permission_targets": []map[string]any{
			{
				"path":           "data",
				"kind":           "dir",
				"access":         "write",
				"expected_owner": "root",
				"expected_group": "app",
				"expected_mode":  "0775",
			},
			{
				"path":          "data/readiness.json",
				"kind":          "file",
				"access":        "read",
				"expected_mode": "0644",
			},
			{
				"path":           "data/startup.lock",
				"kind":           "file",
				"access":         "write",
				"expected_owner": "root",
				"expected_group": "app",
				"expected_mode":  "0664",
			},
		},
		"expected_owner": "app",
		"expected_group": "app",
		"allowed_restart_targets": []string{
			"permission-drift-go-restart-api",
		},
		"verification": map[string]any{
			"method":          "http",
			"expected_status": http.StatusOK,
			"body_contains":   "FIXED",
		},
	})
	tracePath := filepath.Join(appDir, "trace.log")
	if err := os.WriteFile(tracePath, []byte("permission drift: open data/startup.lock: permission denied\n"), 0o644); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if _, err := os.Stat(readFileMarker); err != nil {
			http.Error(writer, "permission drift: read data/readiness.json: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := os.Stat(writeFileMarker); err != nil {
			http.Error(writer, "permission drift: open data/startup.lock: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := os.Stat(restartNeededMarker); err == nil {
			http.Error(writer, "restart required", http.StatusServiceUnavailable)
			return
		}
		_, _ = writer.Write([]byte(`{"status":"FIXED"}`))
	}))
	t.Cleanup(server.Close)

	fakeBin := t.TempDir()
	dockerPath := filepath.Join(fakeBin, "docker")
	script := `#!/bin/sh
set -eu
if [ "$1" != "compose" ]; then
  echo unexpected docker args: "$@" >&2
  exit 1
fi
if [ "$6" = "restart" ]; then
  test "$7" = "permission-drift-go-restart-api"
  test -f "$AI_LOGFIXER_TEST_DOCKER_READ_FILE"
  test -f "$AI_LOGFIXER_TEST_DOCKER_WRITE_FILE"
  rm -f "$AI_LOGFIXER_TEST_RESTART_MARKER"
  exit 0
fi
script="${13:-}"
case "$script" in
  *"stat -c"*)
    case "$script" in
      *"'/app/data/readiness.json'"*)
        if [ -f "$AI_LOGFIXER_TEST_DOCKER_READ_FILE" ]; then
          printf 'exists\tregular empty file\t644\t0\t10001\n'
        else
          printf 'missing\n'
        fi
        exit 0
        ;;
      *"'/app/data/startup.lock'"*)
        if [ -f "$AI_LOGFIXER_TEST_DOCKER_WRITE_FILE" ]; then
          printf 'exists\tregular empty file\t664\t0\t10001\n'
        else
          printf 'missing\n'
        fi
        exit 0
        ;;
      *"'/app/data'"*)
        if [ -f "$AI_LOGFIXER_TEST_DOCKER_DATA_DIR" ]; then
          printf 'exists\tdirectory\t775\t0\t10001\n'
        else
          printf 'missing\n'
        fi
        exit 0
        ;;
    esac
    ;;
  *"mkdir -p '/app/data'"*)
    : > "$AI_LOGFIXER_TEST_DOCKER_DATA_DIR"
    exit 0
    ;;
  *"'/app/data/readiness.json'"*)
    case "$script" in
      *": > '/app/data/readiness.json'"*)
        test -f "$AI_LOGFIXER_TEST_DOCKER_DATA_DIR"
        : > "$AI_LOGFIXER_TEST_DOCKER_READ_FILE"
        exit 0
        ;;
    esac
    echo missing readable file target was not created >&2
    exit 1
    ;;
  *"'/app/data/startup.lock'"*)
    case "$script" in
      *": > '/app/data/startup.lock'"*)
        test -f "$AI_LOGFIXER_TEST_DOCKER_DATA_DIR"
        : > "$AI_LOGFIXER_TEST_DOCKER_WRITE_FILE"
        exit 0
        ;;
    esac
    echo missing writable file target was not created >&2
    exit 1
    ;;
esac
echo unexpected docker exec script: "$script" >&2
exit 1
`
	if err := os.WriteFile(dockerPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AI_LOGFIXER_TEST_DOCKER_DATA_DIR", dataDirMarker)
	t.Setenv("AI_LOGFIXER_TEST_DOCKER_READ_FILE", readFileMarker)
	t.Setenv("AI_LOGFIXER_TEST_DOCKER_WRITE_FILE", writeFileMarker)
	t.Setenv("AI_LOGFIXER_TEST_RESTART_MARKER", restartNeededMarker)

	response, err := readinessresolve.Resolve(context.Background(), readinessresolve.CandidateInput{
		ScenarioID:          "permission-drift-go-restart-api",
		OperationalLane:     "permission-drift",
		ServiceName:         "permission-drift-go-restart-api",
		DockerService:       "permission-drift-go-restart-api",
		AppDir:              appDir,
		PolicyFile:          policyPath,
		TraceFile:           tracePath,
		ComposeFile:         filepath.Join(appDir, "docker-compose.yml"),
		ComposeProject:      "test-project",
		LiveProbeURL:        server.URL + "/orders/readiness",
		ExpectedFixedStatus: http.StatusOK,
		FixedBodyContains:   "FIXED",
	})
	if err != nil {
		t.Fatalf("resolve missing Docker permission targets: %v", err)
	}
	if response.Status != readinessresolve.StatusResolved || !response.Supported {
		t.Fatalf("expected resolved permission response, got %+v", response)
	}
	if response.RestartedService != "permission-drift-go-restart-api" {
		t.Fatalf("expected allowlisted restart evidence, got %+v", response)
	}
	var readChange *readinessresolve.PermissionChange
	for i := range response.PermissionChanges {
		if response.PermissionChanges[i].Path == "data/readiness.json" {
			readChange = &response.PermissionChanges[i]
			break
		}
	}
	if readChange == nil || readChange.Action != "create_readable_file" || readChange.BeforeExists || !readChange.AfterExists {
		t.Fatalf("expected explicit missing read-file creation receipt, got %+v", response.PermissionChanges)
	}
}

func TestResolvePermissionDriftRepairsAllowlistedFileTarget(t *testing.T) {
	t.Parallel()

	appDir := t.TempDir()
	configDir := filepath.Join(appDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	configFile := filepath.Join(configDir, "readiness.json")
	if err := os.WriteFile(configFile, []byte(`{"status":"FIXED"}`+"\n"), 0o400); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	policyPath := filepath.Join(appDir, "policy.json")
	writeJSONFile(t, policyPath, map[string]any{
		"lane": "permission-drift",
		"permission_targets": []map[string]any{
			{
				"path":          "config/readiness.json",
				"kind":          "file",
				"access":        "read",
				"expected_mode": "0644",
			},
		},
		"expected_owner": "app",
		"expected_group": "app",
		"verification": map[string]any{
			"method":          "http",
			"expected_status": http.StatusOK,
			"body_contains":   "FIXED",
		},
	})
	tracePath := filepath.Join(appDir, "trace.log")
	if err := os.WriteFile(tracePath, []byte("permission drift: open config/readiness.json: permission denied\n"), 0o644); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, err := os.ReadFile(configFile)
		if err != nil {
			http.Error(writer, "permission drift: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = writer.Write(raw)
	}))
	t.Cleanup(server.Close)

	response, err := readinessresolve.Resolve(context.Background(), readinessresolve.CandidateInput{
		ScenarioID:          "permission-drift-file-local",
		OperationalLane:     "permission-drift",
		ServiceName:         "permission-drift-file-local",
		AppDir:              appDir,
		PolicyFile:          policyPath,
		TraceFile:           tracePath,
		LiveProbeURL:        server.URL + "/orders/readiness",
		ExpectedFixedStatus: http.StatusOK,
		FixedBodyContains:   "FIXED",
	})
	if err != nil {
		t.Fatalf("resolve permission file drift: %v", err)
	}
	if response.Status != readinessresolve.StatusResolved || !response.Supported {
		t.Fatalf("expected resolved permission response, got %+v", response)
	}
	info, err := os.Stat(configFile)
	if err != nil {
		t.Fatalf("stat config file: %v", err)
	}
	if info.IsDir() {
		t.Fatalf("file permission repair must not convert %s into a directory", configFile)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("expected file mode 0644, got %04o", info.Mode().Perm())
	}
}

func TestResolvePermissionDriftRepairsFileParentSearchPermission(t *testing.T) {
	t.Parallel()

	appDir := t.TempDir()
	configDir := filepath.Join(appDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	configFile := filepath.Join(configDir, "readiness.json")
	if err := os.WriteFile(configFile, []byte(`{"status":"FIXED"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	if err := os.Chmod(configDir, 0o666); err != nil {
		t.Fatalf("remove parent search permission: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(configDir, 0o755)
	})

	policyPath := filepath.Join(appDir, "policy.json")
	writeJSONFile(t, policyPath, map[string]any{
		"lane": "permission-drift",
		"permission_targets": []map[string]any{
			{
				"path":          "config/readiness.json",
				"kind":          "file",
				"access":        "read",
				"expected_mode": "0644",
			},
		},
		"expected_owner": "app",
		"expected_group": "app",
		"verification": map[string]any{
			"method":          "http",
			"expected_status": http.StatusOK,
			"body_contains":   "FIXED",
		},
	})
	tracePath := filepath.Join(appDir, "trace.log")
	if err := os.WriteFile(tracePath, []byte("permission drift: open config/readiness.json: permission denied\n"), 0o644); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, err := os.ReadFile(configFile)
		if err != nil {
			http.Error(writer, "permission drift: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = writer.Write(raw)
	}))
	t.Cleanup(server.Close)

	response, err := readinessresolve.Resolve(context.Background(), readinessresolve.CandidateInput{
		ScenarioID:          "permission-drift-file-parent-local",
		OperationalLane:     "permission-drift",
		ServiceName:         "permission-drift-file-parent-local",
		AppDir:              appDir,
		PolicyFile:          policyPath,
		TraceFile:           tracePath,
		LiveProbeURL:        server.URL + "/orders/readiness",
		ExpectedFixedStatus: http.StatusOK,
		FixedBodyContains:   "FIXED",
	})
	if err != nil {
		t.Fatalf("resolve parent search permission drift: %v", err)
	}
	if response.Status != readinessresolve.StatusResolved || !response.Supported {
		t.Fatalf("expected resolved permission response, got %+v", response)
	}

	info, err := os.Stat(configDir)
	if err != nil {
		t.Fatalf("stat config dir: %v", err)
	}
	if info.Mode().Perm() != 0o711 {
		t.Fatalf("expected parent search repair to set 0711, got %04o", info.Mode().Perm())
	}
	if info.Mode().Perm()&0o002 != 0 {
		t.Fatalf("parent search repair must not leave world-writable permissions, got %04o", info.Mode().Perm())
	}
	var parentChange *readinessresolve.PermissionChange
	for i := range response.PermissionChanges {
		if response.PermissionChanges[i].Path == "config" {
			parentChange = &response.PermissionChanges[i]
			break
		}
	}
	if parentChange == nil {
		t.Fatalf("expected parent search permission change in receipt, got %+v", response.PermissionChanges)
	}
	if parentChange.Action != "repair_parent_search_permission" || parentChange.BeforeMode != "0666" || parentChange.AfterMode != "0711" {
		t.Fatalf("expected exact parent search repair receipt, got %+v", parentChange)
	}
}

func TestResolvePermissionDriftCreatesMissingWritableFileTarget(t *testing.T) {
	t.Parallel()

	appDir := t.TempDir()
	dataDir := filepath.Join(appDir, "data")
	policyPath := filepath.Join(appDir, "policy.json")
	writeJSONFile(t, policyPath, map[string]any{
		"lane": "permission-drift",
		"permission_targets": []map[string]any{
			{
				"path":          "data",
				"kind":          "dir",
				"access":        "write",
				"expected_mode": "0775",
			},
			{
				"path":          "data/app.sqlite",
				"kind":          "file",
				"access":        "write",
				"expected_mode": "0664",
			},
		},
		"expected_owner": "app",
		"expected_group": "app",
		"verification": map[string]any{
			"method":          "http",
			"expected_status": http.StatusOK,
			"body_contains":   "FIXED",
		},
	})
	tracePath := filepath.Join(appDir, "trace.log")
	if err := os.WriteFile(tracePath, []byte("permission drift: open data/app.sqlite: no such file or directory\n"), 0o644); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	dbPath := filepath.Join(dataDir, "app.sqlite")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		file, err := os.OpenFile(dbPath, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			http.Error(writer, "permission drift: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer file.Close()
		if _, err := file.WriteString("readiness audit\n"); err != nil {
			http.Error(writer, "permission drift: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = writer.Write([]byte(`{"status":"FIXED"}`))
	}))
	t.Cleanup(server.Close)

	response, err := readinessresolve.Resolve(context.Background(), readinessresolve.CandidateInput{
		ScenarioID:          "permission-drift-missing-file-local",
		OperationalLane:     "permission-drift",
		ServiceName:         "permission-drift-missing-file-local",
		AppDir:              appDir,
		PolicyFile:          policyPath,
		TraceFile:           tracePath,
		LiveProbeURL:        server.URL + "/orders/readiness",
		ExpectedFixedStatus: http.StatusOK,
		FixedBodyContains:   "FIXED",
	})
	if err != nil {
		t.Fatalf("resolve missing writable file target: %v", err)
	}
	if response.Status != readinessresolve.StatusResolved || !response.Supported {
		t.Fatalf("expected resolved permission response, got %+v", response)
	}
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("writable file target should be created: %v", err)
	}
	if info.IsDir() {
		t.Fatalf("writable file target must be a file, got directory")
	}
	if info.Mode().Perm() != 0o664 {
		t.Fatalf("expected writable file mode 0664, got %04o", info.Mode().Perm())
	}
}

func TestResolvePermissionDriftRejectsWorldWritablePolicy(t *testing.T) {
	t.Parallel()

	appDir := t.TempDir()
	logDir := filepath.Join(appDir, "storage", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("create log dir: %v", err)
	}
	policyPath := filepath.Join(appDir, "policy.json")
	writeJSONFile(t, policyPath, map[string]any{
		"lane":          "permission-drift",
		"allowed_paths": []string{"storage/logs"},
		"expected_mode": "0777",
		"verification": map[string]any{
			"method":          "http",
			"expected_status": http.StatusOK,
			"body_contains":   "FIXED",
		},
	})
	tracePath := filepath.Join(appDir, "trace.log")
	if err := os.WriteFile(tracePath, []byte("permission denied\n"), 0o644); err != nil {
		t.Fatalf("write trace: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"status":"FIXED"}`))
	}))
	t.Cleanup(server.Close)

	_, err := readinessresolve.Resolve(context.Background(), readinessresolve.CandidateInput{
		ScenarioID:          "permission-drift-unsafe",
		OperationalLane:     "permission-drift",
		ServiceName:         "permission-drift-unsafe",
		AppDir:              appDir,
		PolicyFile:          policyPath,
		TraceFile:           tracePath,
		LiveProbeURL:        server.URL + "/orders/readiness",
		ExpectedFixedStatus: http.StatusOK,
		FixedBodyContains:   "FIXED",
	})
	if err == nil {
		t.Fatal("expected unsafe permission policy to fail")
	}
	if !strings.Contains(err.Error(), "0777") {
		t.Fatalf("expected unsafe mode in error, got %v", err)
	}
}

func TestResolvePermissionDriftBlocksSymlinkEscapeBeforeRepair(t *testing.T) {
	t.Parallel()

	appDir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(appDir, "storage")); err != nil {
		t.Fatalf("create escaping storage symlink: %v", err)
	}
	policyPath := filepath.Join(appDir, "policy.json")
	writeJSONFile(t, policyPath, map[string]any{
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
	tracePath := filepath.Join(appDir, "trace.log")
	if err := os.WriteFile(tracePath, []byte("permission drift: write storage/logs/audit.log: permission denied\n"), 0o644); err != nil {
		t.Fatalf("write trace: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"status":"FIXED"}`))
	}))
	t.Cleanup(server.Close)

	_, err := readinessresolve.Resolve(context.Background(), readinessresolve.CandidateInput{
		ScenarioID:          "permission-drift-symlink-escape",
		OperationalLane:     "permission-drift",
		ServiceName:         "permission-drift-symlink-escape",
		AppDir:              appDir,
		PolicyFile:          policyPath,
		TraceFile:           tracePath,
		LiveProbeURL:        server.URL + "/orders/readiness",
		ExpectedFixedStatus: http.StatusOK,
		FixedBodyContains:   "FIXED",
	})
	if err == nil {
		t.Fatal("expected symlink escape to block permission remediation")
	}
	if !strings.Contains(err.Error(), "escapes app_dir") {
		t.Fatalf("expected symlink escape error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "logs")); !os.IsNotExist(statErr) {
		t.Fatalf("permission repair must not create escaped outside path, stat error=%v", statErr)
	}
}

func TestResolveRestartReloadRunsAllowlistedDockerRestart(t *testing.T) {
	appDir := t.TempDir()
	marker := filepath.Join(appDir, "runtime", "restart-required")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatalf("create runtime dir: %v", err)
	}
	if err := os.WriteFile(marker, []byte("restart required\n"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	policyPath := filepath.Join(appDir, "policy.json")
	writeJSONFile(t, policyPath, map[string]any{
		"lane":                    "restart-reload",
		"allowed_restart_targets": []string{"restart-reload-api"},
		"verification": map[string]any{
			"method":          "http",
			"expected_status": http.StatusOK,
			"body_contains":   "FIXED",
		},
	})
	tracePath := filepath.Join(appDir, "trace.log")
	if err := os.WriteFile(tracePath, []byte("starting with stale runtime state; a service restart should clear this\n"), 0o644); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if _, err := os.Stat(marker); err == nil {
			http.Error(writer, "restart required", http.StatusServiceUnavailable)
			return
		}
		_, _ = writer.Write([]byte(`{"status":"FIXED"}`))
	}))
	t.Cleanup(server.Close)

	fakeBin := t.TempDir()
	dockerPath := filepath.Join(fakeBin, "docker")
	script := "#!/bin/sh\nset -eu\nif [ \"$1\" = compose ] && [ \"$6\" = restart ]; then rm -f \"$AI_LOGFIXER_TEST_RESTART_MARKER\"; exit 0; fi\necho unexpected docker args: \"$@\" >&2\nexit 1\n"
	if err := os.WriteFile(dockerPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AI_LOGFIXER_TEST_RESTART_MARKER", marker)

	response, err := readinessresolve.Resolve(context.Background(), readinessresolve.CandidateInput{
		ScenarioID:          "restart-reload-api",
		OperationalLane:     "restart-reload",
		ServiceName:         "restart-reload-api",
		DockerService:       "restart-reload-api",
		AppDir:              appDir,
		PolicyFile:          policyPath,
		TraceFile:           tracePath,
		ComposeFile:         filepath.Join(appDir, "docker-compose.yml"),
		ComposeProject:      "test-project",
		LiveProbeURL:        server.URL + "/orders/readiness",
		ExpectedFixedStatus: http.StatusOK,
		FixedBodyContains:   "FIXED",
	})
	if err != nil {
		t.Fatalf("resolve restart reload: %v", err)
	}
	if response.Status != readinessresolve.StatusResolved || !response.Supported {
		t.Fatalf("expected resolved restart response, got %+v", response)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("expected fake docker restart to clear marker, stat err=%v", err)
	}
}

func TestResolveConfigDriftAppliesTrustedPolicyValue(t *testing.T) {
	t.Parallel()

	appDir := t.TempDir()
	writeJSONFile(t, filepath.Join(appDir, "config", "runtime.json"), map[string]any{
		"api_base_url": "http://127.0.0.1:1",
	})

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/health":
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte("ok\n"))
		case "/orders/readiness":
			config := readJSONFile(t, filepath.Join(appDir, "config", "runtime.json"))
			baseURL, _ := config["api_base_url"].(string)
			response, err := http.Get(strings.TrimRight(baseURL, "/") + "/health")
			if err != nil {
				http.Error(writer, "bad config", http.StatusBadGateway)
				return
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				http.Error(writer, "bad upstream", http.StatusBadGateway)
				return
			}
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{"status":"FIXED"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)

	writeJSONFile(t, filepath.Join(appDir, "config", "last-known-good.json"), map[string]any{
		"api_base_url": server.URL,
		"source":       "trusted-server-inventory",
	})
	policyPath := filepath.Join(appDir, "policy.json")
	writeJSONFile(t, policyPath, map[string]any{
		"lane": "config-drift",
		"allowed_files": []string{
			"config/runtime.json",
		},
		"allowed_keys": []string{
			"api_base_url",
		},
		"trusted_sources": []string{
			"config/last-known-good.json",
		},
		"verification": map[string]any{
			"method":          "http",
			"url":             server.URL + "/orders/readiness",
			"expected_status": http.StatusOK,
			"body_contains":   "FIXED",
		},
	})
	tracePath := filepath.Join(appDir, "trace.log")
	trace := `127.0.0.1 - - [01/Jun/2026:12:00:00 +0000] "GET /orders/readiness HTTP/1.1" 502 -` + "\n"
	if err := os.WriteFile(tracePath, []byte(trace), 0o644); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	response, err := readinessresolve.Resolve(context.Background(), readinessresolve.CandidateInput{
		ScenarioID:          "config-drift-api",
		OperationalLane:     "config-drift",
		Runtime:             "python",
		AppCarrier:          "python-http",
		ServiceName:         "config-drift-api",
		AppDir:              appDir,
		PolicyFile:          policyPath,
		TraceFile:           tracePath,
		LiveProbeURL:        server.URL + "/orders/readiness",
		ExpectedFixedStatus: http.StatusOK,
		FixedBodyContains:   "FIXED",
		SafeAction:          "patch allowlisted config key from trusted last-known-good snapshot",
	})
	if err != nil {
		t.Fatalf("resolve config drift: %v", err)
	}
	if response.Status != readinessresolve.StatusResolved || !response.Supported {
		t.Fatalf("expected resolved supported response, got %+v", response)
	}
	if response.RemediationPlan == nil || response.RemediationPlan.Status != "succeeded" {
		t.Fatalf("expected succeeded remediation plan, got %+v", response.RemediationPlan)
	}

	config := readJSONFile(t, filepath.Join(appDir, "config", "runtime.json"))
	if config["api_base_url"] != server.URL {
		t.Fatalf("expected trusted config value to be applied, got %+v", config["api_base_url"])
	}
}

func writeJSONFile(t *testing.T, path string, value map[string]any) {
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

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return value
}

func modePerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}
