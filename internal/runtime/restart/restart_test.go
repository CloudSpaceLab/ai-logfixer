package restart

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
)

func TestRunExecutesAllowlistedRestartAndVerifiesHTTPRecovery(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	stalePath := filepath.Join(workDir, "serving-stale-config")
	logPath := filepath.Join(workDir, "orders.log")
	restartMarker := filepath.Join(workDir, "restart-ran")

	mustWriteFile(t, stalePath, "stale\n", 0o644)
	writeRepeatedHTTPFailures(t, logPath, "orders-api", "/orders/readiness", 4)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/orders/readiness" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "text/plain")
		if _, err := os.Stat(stalePath); err == nil {
			http.Error(writer, "service still running with stale config", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("FIXED\n"))
	}))
	t.Cleanup(server.Close)

	restartScript := filepath.Join(workDir, "restart-orders.sh")
	mustWriteFile(t, restartScript, "#!/bin/sh\nset -eu\nrm -f \"$1\"\nprintf restarted > \"$2\"\n", 0o755)

	result, err := Run(context.Background(), Options{
		ServiceName:    "orders-api",
		LogPath:        logPath,
		Route:          "/orders/readiness",
		StatusCode:     http.StatusServiceUnavailable,
		ErrorThreshold: 3,
		ActionName:     "restart-runtime",
		Policy: Policy{
			AllowedActions: []Action{
				{
					Name:        "restart-runtime",
					ServiceName: "orders-api",
					Command: Command{
						Path: restartScript,
						Args: []string{stalePath, restartMarker},
					},
				},
			},
		},
		Verification: Verification{
			HTTP: &HTTPVerification{
				URL:            server.URL + "/orders/readiness",
				ExpectedStatus: http.StatusOK,
				BodyContains:   "FIXED",
			},
		},
		Now: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("run restart resolver: %v", err)
	}

	if result.RemediationPlan.Status != contractsv1.RemediationStatusSucceeded {
		t.Fatalf("expected succeeded remediation plan, got %s", result.RemediationPlan.Status)
	}
	if result.Attempt.Status != contractsv1.RemediationStatusSucceeded {
		t.Fatalf("expected succeeded attempt, got %s", result.Attempt.Status)
	}
	if result.Receipt.Outcome != "succeeded" {
		t.Fatalf("expected succeeded receipt, got %q", result.Receipt.Outcome)
	}
	if _, err := os.Stat(restartMarker); err != nil {
		t.Fatalf("expected allowlisted restart command to run: %v", err)
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("expected restart command to clear stale runtime state, stat err=%v", err)
	}
	if !strings.Contains(result.Receipt.BeforeState, "HTTP 503") {
		t.Fatalf("receipt should record before evidence, got %q", result.Receipt.BeforeState)
	}
	if !strings.Contains(result.Receipt.AfterState, "HTTP 200") || !strings.Contains(result.Receipt.AfterState, "FIXED") {
		t.Fatalf("receipt should record after evidence, got %q", result.Receipt.AfterState)
	}
	if !strings.Contains(result.Receipt.Summary, "cannot be undone") {
		t.Fatalf("receipt should disclose rollback limitation, got %q", result.Receipt.Summary)
	}
	if result.RemediationPlan.RollbackPlan.RollbackType != contractsv1.RollbackUnavailable {
		t.Fatalf("restart rollback should be unavailable, got %s", result.RemediationPlan.RollbackPlan.RollbackType)
	}
	if !containsText(result.RemediationPlan.RollbackPlan.Limitations, "cannot be undone") {
		t.Fatalf("expected rollback limitations to explain restart cannot be undone: %+v", result.RemediationPlan.RollbackPlan.Limitations)
	}
}

func TestRunBlocksWhenRestartActionIsNotAllowlisted(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	stalePath := filepath.Join(workDir, "serving-stale-config")
	logPath := filepath.Join(workDir, "orders.log")
	restartMarker := filepath.Join(workDir, "restart-ran")

	mustWriteFile(t, stalePath, "stale\n", 0o644)
	writeRepeatedHTTPFailures(t, logPath, "orders-api", "/orders/readiness", 4)

	restartScript := filepath.Join(workDir, "restart-orders.sh")
	mustWriteFile(t, restartScript, "#!/bin/sh\nset -eu\nrm -f \"$1\"\nprintf restarted > \"$2\"\n", 0o755)

	result, err := Run(context.Background(), Options{
		ServiceName:    "orders-api",
		LogPath:        logPath,
		Route:          "/orders/readiness",
		StatusCode:     http.StatusServiceUnavailable,
		ErrorThreshold: 3,
		ActionName:     "restart-runtime",
		Policy: Policy{
			AllowedActions: []Action{
				{
					Name:        "restart-runtime",
					ServiceName: "billing-api",
					Command: Command{
						Path: restartScript,
						Args: []string{stalePath, restartMarker},
					},
				},
			},
		},
		Verification: Verification{
			HTTP: &HTTPVerification{
				URL:            "http://127.0.0.1:1/orders/readiness",
				ExpectedStatus: http.StatusOK,
			},
		},
		Now: time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("expected blocked result without error, got %v", err)
	}

	if result.RemediationPlan.Status != contractsv1.RemediationStatusEscalated || result.RemediationPlan.RiskLevel != contractsv1.SafetyBlocked {
		t.Fatalf("expected blocked/escalated plan, got %+v", result.RemediationPlan)
	}
	if result.Attempt.Status != contractsv1.RemediationStatusEscalated {
		t.Fatalf("expected escalated attempt, got %s", result.Attempt.Status)
	}
	if result.Receipt.Outcome != "escalated" {
		t.Fatalf("expected escalated receipt, got %q", result.Receipt.Outcome)
	}
	if _, err := os.Stat(restartMarker); !os.IsNotExist(err) {
		t.Fatalf("blocked resolver should not run restart command, stat err=%v", err)
	}
	if _, err := os.Stat(stalePath); err != nil {
		t.Fatalf("blocked resolver should leave stale runtime state in place: %v", err)
	}
	if !strings.Contains(result.RemediationPlan.UserMessage, "allowlisted") {
		t.Fatalf("blocked plan should explain missing allowlist, got %q", result.RemediationPlan.UserMessage)
	}
}

func TestRunBlocksProcessKillCommandEvenWhenAllowlisted(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	logPath := filepath.Join(workDir, "orders.log")
	writeRepeatedHTTPFailures(t, logPath, "orders-api", "/orders/readiness", 4)

	verifyScript := filepath.Join(workDir, "verify-orders.sh")
	mustWriteFile(t, verifyScript, "#!/bin/sh\nset -eu\nprintf ok\n", 0o755)

	result, err := Run(context.Background(), Options{
		ServiceName:    "orders-api",
		LogPath:        logPath,
		Route:          "/orders/readiness",
		StatusCode:     http.StatusServiceUnavailable,
		ErrorThreshold: 3,
		ActionName:     "restart-runtime",
		Policy: Policy{
			AllowedActions: []Action{
				{
					Name:        "restart-runtime",
					ServiceName: "orders-api",
					Command: Command{
						Path: "kill",
						Args: []string{"-9", "12345"},
					},
				},
			},
		},
		Verification: Verification{
			Command: &CommandVerification{
				Command: Command{Path: verifyScript},
			},
		},
		Now: time.Date(2026, 6, 8, 10, 15, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("expected blocked result without error, got %v", err)
	}

	if result.RemediationPlan.Status != contractsv1.RemediationStatusEscalated {
		t.Fatalf("expected escalated remediation plan, got %s", result.RemediationPlan.Status)
	}
	if !strings.Contains(result.RemediationPlan.UserMessage, "kill is not permitted") {
		t.Fatalf("blocked plan should explain process kill denial, got %q", result.RemediationPlan.UserMessage)
	}
}

func TestRunSupportsCommandVerification(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	stalePath := filepath.Join(workDir, "serving-stale-config")
	logPath := filepath.Join(workDir, "orders.log")

	mustWriteFile(t, stalePath, "stale\n", 0o644)
	writeRepeatedHTTPFailures(t, logPath, "orders-api", "/orders/readiness", 4)

	restartScript := filepath.Join(workDir, "restart-orders.sh")
	mustWriteFile(t, restartScript, "#!/bin/sh\nset -eu\nrm -f \"$1\"\n", 0o755)

	verifyScript := filepath.Join(workDir, "verify-orders.sh")
	mustWriteFile(t, verifyScript, "#!/bin/sh\nset -eu\nif [ -e \"$1\" ]; then exit 1; fi\nprintf 'runtime healthy after restart\\n'\n", 0o755)

	result, err := Run(context.Background(), Options{
		ServiceName:    "orders-api",
		LogPath:        logPath,
		Route:          "/orders/readiness",
		StatusCode:     http.StatusServiceUnavailable,
		ErrorThreshold: 3,
		ActionName:     "restart-runtime",
		Policy: Policy{
			AllowedActions: []Action{
				{
					Name:        "restart-runtime",
					ServiceName: "orders-api",
					Command: Command{
						Path: restartScript,
						Args: []string{stalePath},
					},
				},
			},
		},
		Verification: Verification{
			Command: &CommandVerification{
				Command: Command{
					Path: verifyScript,
					Args: []string{stalePath},
				},
				OutputContains: "runtime healthy",
			},
		},
		Now: time.Date(2026, 6, 8, 10, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("run restart resolver with command verification: %v", err)
	}

	if result.Attempt.Status != contractsv1.RemediationStatusSucceeded {
		t.Fatalf("expected succeeded attempt, got %s", result.Attempt.Status)
	}
	if !strings.Contains(result.Receipt.AfterState, "runtime healthy") {
		t.Fatalf("receipt should include command verification output, got %q", result.Receipt.AfterState)
	}
}

func writeRepeatedHTTPFailures(t *testing.T, path string, service string, route string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		line := time.Date(2026, 6, 8, 9, 0, i, 0, time.UTC).Format(time.RFC3339Nano) +
			" level=error service=" + service + " method=GET route=" + route + " status=503\n"
		appendFile(t, path, line)
	}
}

func mustWriteFile(t *testing.T, path string, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func appendFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()
	if _, err := file.WriteString(content); err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
}

func containsText(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
