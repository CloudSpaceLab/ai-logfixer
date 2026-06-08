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
		ScenarioID:          "permission-drift-api",
		OperationalLane:     "permission-drift",
		ServiceName:         "permission-drift-api",
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
