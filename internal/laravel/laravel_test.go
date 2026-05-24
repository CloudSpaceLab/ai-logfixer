package laravel

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CloudSpaceLab/ai-logfixer/internal/agentfix"
	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
)

func TestRunDetectsLaravel200ErrorPageAndCreatesInferredMissingClassStub(t *testing.T) {
	t.Parallel()

	targetDir := newLaravelFixture(t)
	stubPath := filepath.Join(targetDir, "app", "Services", "AuditTrailFormatter.php")

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		if _, err := os.Stat(stubPath); err == nil {
			_, _ = writer.Write([]byte(`<html><title>Transaction/Risk Details</title><body>Transaction 3478538</body></html>`))
			return
		}
		_, _ = writer.Write([]byte(`<html><head><title>Sorry</title></head><body><div>Sorry.</div><button>Go Back</button><p style="display:none">Server unable to attend to this request at this time</p></body></html>`))
	}))
	t.Cleanup(server.Close)

	result, err := Run(context.Background(), Options{
		ServiceName: "fraudv",
		TargetDir:   targetDir,
		URL:         server.URL + "/transactions/3478538",
		Apply:       true,
		Now:         time.Date(2026, 5, 24, 8, 45, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("run Laravel fixer: %v", err)
	}

	if result.Diagnosis.Status != contractsv1.DiagnosisStatusComplete {
		t.Fatalf("expected complete diagnosis, got %q", result.Diagnosis.Status)
	}
	if result.Attempt.Status != contractsv1.RemediationStatusSucceeded {
		t.Fatalf("expected succeeded attempt, got %q", result.Attempt.Status)
	}
	if result.HTTPProbe.LaravelErrorPage {
		t.Fatalf("expected final probe to no longer match Laravel error page: %+v", result.HTTPProbe)
	}
	if result.HTTPProbe.StatusCode != http.StatusOK {
		t.Fatalf("expected final status 200, got %d", result.HTTPProbe.StatusCode)
	}
	if _, err := os.Stat(stubPath); err != nil {
		t.Fatalf("expected inferred missing-class stub to be created: %v", err)
	}
	raw, err := os.ReadFile(stubPath)
	if err != nil {
		t.Fatalf("read created class stub: %v", err)
	}
	source := string(raw)
	for _, expected := range []string{
		"class AuditTrailFormatter",
		"public static function renderBadge(...$args)",
		"public static function recordEvent(...$args)",
		"public static function __callStatic($name, $args)",
	} {
		if !strings.Contains(source, expected) {
			t.Fatalf("created file does not contain %q:\n%s", expected, source)
		}
	}
	if result.BackupPath == "" {
		t.Fatal("expected rollback marker path")
	}
	if len(result.MissingClasses) == 0 || len(result.MissingClasses[0].InferredMethods) < 2 {
		t.Fatalf("expected inferred methods in result: %+v", result.MissingClasses)
	}
}

func TestHTTPStatusOnlyWouldMissLaravelFriendlyErrorPage(t *testing.T) {
	t.Parallel()

	targetDir := newLaravelFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`<html><head><title>Sorry</title></head><body>Sorry.<button>Go Back</button></body></html>`))
	}))
	t.Cleanup(server.Close)

	_, err := Run(context.Background(), Options{
		ServiceName:    "fraudv",
		TargetDir:      targetDir,
		URL:            server.URL + "/transactions/3478538",
		Apply:          false,
		HTTPStatusOnly: true,
		Now:            time.Date(2026, 5, 24, 8, 45, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatal("expected status-only mode to miss the Laravel friendly 200 error page")
	}
	if !strings.Contains(err.Error(), "HTTP status is 200") {
		t.Fatalf("expected status-only explanation, got %v", err)
	}
}

func TestRunUsesLaravelLogMissingClassEvidence(t *testing.T) {
	t.Parallel()

	targetDir := newLaravelFixture(t)
	logPath := filepath.Join(targetDir, "storage", "logs", "laravel-2026-05-24.log")
	logLine := `[2026-05-24 08:13:41] production.ERROR: fraudsniperapp22--Class "App\Services\AuditTrailFormatter" not found (View: /var/www/fraudv/resources/views/pages/transactions/show.blade.php)`
	if err := os.WriteFile(logPath, []byte(logLine+"\n"), 0o644); err != nil {
		t.Fatalf("write Laravel log: %v", err)
	}

	result, err := Run(context.Background(), Options{
		ServiceName: "fraudv",
		TargetDir:   targetDir,
		LogPath:     logPath,
		Apply:       false,
		Now:         time.Date(2026, 5, 24, 8, 45, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("run Laravel fixer dry-run: %v", err)
	}

	foundLogEvidence := false
	for _, item := range result.MissingClasses {
		if item.Class == `App\Services\AuditTrailFormatter` && item.FromLog {
			foundLogEvidence = true
		}
	}
	if !foundLogEvidence {
		t.Fatalf("expected missing class to be linked to log evidence: %+v", result.MissingClasses)
	}
	if result.Receipt.Outcome != "dry_run" {
		t.Fatalf("expected dry-run receipt, got %q", result.Receipt.Outcome)
	}
}

func TestRunDiagnosesUnsupportedLaravelSQLIssueWithoutPatching(t *testing.T) {
	t.Parallel()

	targetDir := newLaravelBaseFixture(t)
	logPath := filepath.Join(targetDir, "storage", "logs", "laravel-2026-05-24.log")
	logLine := `[2026-05-24 08:13:41] production.ERROR: SQLSTATE[42S22]: Column not found: 1054 Unknown column 'risk_narration' in 'field list' (Connection: mysql, SQL: select risk_narration from transactions)`
	if err := os.WriteFile(logPath, []byte(logLine+"\n"), 0o644); err != nil {
		t.Fatalf("write Laravel log: %v", err)
	}

	result, err := Run(context.Background(), Options{
		ServiceName: "fraudv",
		TargetDir:   targetDir,
		LogPath:     logPath,
		Apply:       true,
		Now:         time.Date(2026, 5, 24, 8, 45, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("run Laravel fixer: %v", err)
	}

	if result.Attempt.Status != contractsv1.RemediationStatusEscalated {
		t.Fatalf("expected escalated attempt, got %q", result.Attempt.Status)
	}
	if result.RemediationPlan.RiskLevel != contractsv1.SafetyBlocked {
		t.Fatalf("expected blocked remediation plan, got %q", result.RemediationPlan.RiskLevel)
	}
	if result.Receipt.Outcome != "escalated" {
		t.Fatalf("expected escalated receipt, got %q", result.Receipt.Outcome)
	}
	if len(result.Issues) == 0 || result.Issues[0].Kind != "missing_column" {
		t.Fatalf("expected missing_column issue, got %+v", result.Issues)
	}
	if result.CreatedPath != "" || result.BackupPath != "" {
		t.Fatalf("expected no generated files for unsupported issue, got created=%q backup=%q", result.CreatedPath, result.BackupPath)
	}
}

func TestRunClassifiesLaravelErrorPageWithoutLogEvidence(t *testing.T) {
	t.Parallel()

	targetDir := newLaravelBaseFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`<html><head><title>Sorry</title></head><body><div>Sorry.</div><button>Go Back</button><p>Server unable to attend to this request at this time</p></body></html>`))
	}))
	t.Cleanup(server.Close)

	result, err := Run(context.Background(), Options{
		ServiceName: "fraudv",
		TargetDir:   targetDir,
		URL:         server.URL + "/transactions/3478538",
		Apply:       true,
		Now:         time.Date(2026, 5, 24, 8, 45, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("run Laravel fixer: %v", err)
	}

	if result.HTTPProbe.StatusCode != http.StatusOK || !result.HTTPProbe.LaravelErrorPage {
		t.Fatalf("expected Laravel 200 error page probe, got %+v", result.HTTPProbe)
	}
	if len(result.Issues) == 0 || result.Issues[0].Kind != "unknown_laravel_error_page" {
		t.Fatalf("expected unknown_laravel_error_page issue, got %+v", result.Issues)
	}
	if result.Attempt.Status != contractsv1.RemediationStatusEscalated {
		t.Fatalf("expected escalated attempt, got %q", result.Attempt.Status)
	}
}

func TestRunDelegatesUnsupportedIssueToExternalAgentAndAppliesValidatedPatch(t *testing.T) {
	t.Parallel()

	targetDir := newLaravelBaseFixture(t)
	fixPath := filepath.Join(targetDir, "config", "ai-logfixer-fixed.php")
	logPath := filepath.Join(targetDir, "storage", "logs", "laravel-2026-05-24.log")
	logLine := `[2026-05-24 08:13:41] production.ERROR: SQLSTATE[42S22]: Column not found: 1054 Unknown column 'risk_narration' in 'field list'`
	if err := os.WriteFile(logPath, []byte(logLine+"\n"), 0o644); err != nil {
		t.Fatalf("write Laravel log: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		if _, err := os.Stat(fixPath); err == nil {
			_, _ = writer.Write([]byte(`<html><title>Recovered</title><body>ok</body></html>`))
			return
		}
		_, _ = writer.Write([]byte(`<html><head><title>Sorry</title></head><body><div>Sorry.</div><button>Go Back</button></body></html>`))
	}))
	t.Cleanup(server.Close)

	result, err := Run(context.Background(), Options{
		ServiceName:   "fraudv",
		TargetDir:     targetDir,
		LogPath:       logPath,
		URL:           server.URL + "/transactions/3478538",
		Apply:         true,
		ExternalAgent: true,
		Now:           time.Date(2026, 5, 24, 8, 45, 0, 0, time.UTC),
		ExternalAgentRunner: func(ctx context.Context, agent agentfix.AgentContext) (agentfix.CommandOutput, error) {
			path := filepath.Join(agent.StagingDir, "config", "ai-logfixer-fixed.php")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return agentfix.CommandOutput{}, err
			}
			return agentfix.CommandOutput{Stdout: "created focused Laravel fix"}, os.WriteFile(path, []byte("<?php\nreturn true;\n"), 0o644)
		},
	})
	if err != nil {
		t.Fatalf("run Laravel fixer with external agent: %v", err)
	}

	if result.Attempt.Status != contractsv1.RemediationStatusSucceeded {
		t.Fatalf("expected succeeded external-agent attempt, got %q", result.Attempt.Status)
	}
	if result.ExternalAgent == nil || !result.ExternalAgent.Applied || result.ExternalAgent.ManifestPath == "" {
		t.Fatalf("expected applied external-agent result with manifest, got %+v", result.ExternalAgent)
	}
	if result.HTTPProbe.LaravelErrorPage {
		t.Fatalf("expected final probe to be healthy, got %+v", result.HTTPProbe)
	}
	if _, err := os.Stat(fixPath); err != nil {
		t.Fatalf("expected external-agent patch file to exist: %v", err)
	}
	if err := agentfix.Rollback(result.ExternalAgent.ManifestPath); err != nil {
		t.Fatalf("rollback external-agent patch: %v", err)
	}
	if _, err := os.Stat(fixPath); !os.IsNotExist(err) {
		t.Fatalf("expected rollback to remove created patch file, got err=%v", err)
	}
}

func newLaravelBaseFixture(t *testing.T) string {
	t.Helper()

	targetDir := t.TempDir()
	mustWrite(t, filepath.Join(targetDir, "artisan"), "#!/usr/bin/env php\n")
	mustWrite(t, filepath.Join(targetDir, "composer.json"), `{
  "autoload": {
    "psr-4": {
      "App\\": "app/"
    }
  }
}
`)
	if err := os.MkdirAll(filepath.Join(targetDir, "storage", "logs"), 0o755); err != nil {
		t.Fatalf("create logs dir: %v", err)
	}
	return targetDir
}

func newLaravelFixture(t *testing.T) string {
	t.Helper()

	targetDir := newLaravelBaseFixture(t)
	mustWrite(t, filepath.Join(targetDir, "resources", "views", "pages", "transactions", "show.blade.php"), `<?php echo \App\Services\AuditTrailFormatter::renderBadge($comment ?? ''); ?>`)
	mustWrite(t, filepath.Join(targetDir, "app", "Http", "Controllers", "TransactionController.php"), `<?php
namespace App\Http\Controllers;

class TransactionController
{
    public function storeComment(): void
    {
        \App\Services\AuditTrailFormatter::recordEvent('', 1, 'case', 1, 'fraud_case', 1);
    }
}
`)
	return targetDir
}

func mustWrite(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func Example_isLaravelProductionErrorPage() {
	ok, signal := isLaravelProductionErrorPage(`<title>Sorry</title><div>Sorry.</div><button>Go Back</button>`)
	fmt.Println(ok, signal)
	// Output:
	// true <title>sorry</title>
}
