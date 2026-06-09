package permissions_test

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

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	permissions "github.com/CloudSpaceLab/ai-logfixer/internal/runtime/permissions"
)

func TestRunDryRunPlansLaravelStoragePermissionRepairWithoutMutating(t *testing.T) {
	t.Parallel()

	appDir := newLaravelApp(t)
	logsDir := filepath.Join(appDir, "storage", "logs")
	chmod(t, logsDir, 0o500)

	result, err := permissions.Run(context.Background(), permissions.Options{
		ServiceName: "orders-api",
		TargetDir:   appDir,
		Framework:   "auto",
		Apply:       false,
		Now:         fixedTime(),
	})
	if err != nil {
		t.Fatalf("run permission resolver dry-run: %v", err)
	}

	if result.Framework != "laravel" {
		t.Fatalf("expected Laravel framework detection, got %q", result.Framework)
	}
	if result.RemediationPlan.Status != contractsv1.RemediationStatusApproved {
		t.Fatalf("expected approved remediation plan, got %q", result.RemediationPlan.Status)
	}
	if result.Attempt.MonitorSummary.Status != "dry_run" {
		t.Fatalf("expected dry-run attempt evidence, got %+v", result.Attempt.MonitorSummary)
	}
	if result.Receipt.Outcome != "dry_run" {
		t.Fatalf("expected dry-run receipt, got %q", result.Receipt.Outcome)
	}
	if len(result.Findings) == 0 {
		t.Fatal("expected at least one permission finding")
	}
	if !strings.Contains(result.Diagnosis.SuspectedRootCause, "storage/logs") {
		t.Fatalf("diagnosis should name the drifted path, got %q", result.Diagnosis.SuspectedRootCause)
	}
	if !strings.Contains(result.RemediationPlan.FixPreview.Before, "0500") {
		t.Fatalf("fix preview should include before mode, got %q", result.RemediationPlan.FixPreview.Before)
	}
	if !strings.Contains(result.RemediationPlan.FixPreview.After, "0775") {
		t.Fatalf("fix preview should include target mode, got %q", result.RemediationPlan.FixPreview.After)
	}
	if got := modePerm(t, logsDir); got != 0o500 {
		t.Fatalf("dry run mutated %s mode: got %04o", logsDir, got)
	}
}

func TestRunAutoDetectsCommonFrameworkPermissionPolicies(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		framework    string
		markers      map[string]string
		expectedPath string
	}{
		{
			name:      "express",
			framework: "express",
			markers: map[string]string{
				"package.json": `{"dependencies":{"express":"^4.18.0"}}` + "\n",
			},
			expectedPath: "uploads",
		},
		{
			name:      "fastapi",
			framework: "fastapi",
			markers: map[string]string{
				"requirements.txt": "fastapi==0.115.0\nuvicorn==0.30.0\n",
			},
			expectedPath: "instance",
		},
		{
			name:      "flask",
			framework: "flask",
			markers: map[string]string{
				"pyproject.toml": "[project]\ndependencies = [\"flask\"]\n",
			},
			expectedPath: "instance",
		},
		{
			name:      "go",
			framework: "go",
			markers: map[string]string{
				"go.mod": "module example.com/orders\n\ngo 1.23\n",
			},
			expectedPath: "data",
		},
		{
			name:      "java",
			framework: "java",
			markers: map[string]string{
				"pom.xml": "<project><modelVersion>4.0.0</modelVersion></project>\n",
			},
			expectedPath: "logs",
		},
		{
			name:      "rails",
			framework: "rails",
			markers: map[string]string{
				"Gemfile":               "source 'https://rubygems.org'\ngem 'rails'\n",
				"config/application.rb": "module Orders\n  class Application < Rails::Application\n  end\nend\n",
			},
			expectedPath: "tmp/cache",
		},
		{
			name:      "ruby",
			framework: "ruby",
			markers: map[string]string{
				"Gemfile": "source 'https://rubygems.org'\ngem 'sinatra'\n",
			},
			expectedPath: "storage",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			appDir := newPolicyApp(t, tc.markers)
			result, err := permissions.Run(context.Background(), permissions.Options{
				ServiceName: "orders-api",
				TargetDir:   appDir,
				Framework:   "auto",
				Apply:       false,
				Now:         fixedTime(),
			})
			if err != nil {
				t.Fatalf("run permission resolver: %v", err)
			}

			if result.Framework != tc.framework {
				t.Fatalf("expected framework %q, got %q", tc.framework, result.Framework)
			}
			if result.Policy.Framework != tc.framework {
				t.Fatalf("expected policy framework %q, got %q", tc.framework, result.Policy.Framework)
			}
			if !policyHasPath(result.Policy, tc.expectedPath) {
				t.Fatalf("expected policy for %s to include %s, got %+v", tc.framework, tc.expectedPath, result.Policy.ExpectedPaths)
			}
			if !operationForPath(result.Operations, tc.expectedPath) {
				t.Fatalf("expected dry-run operation for missing path %s, got %+v", tc.expectedPath, result.Operations)
			}
			if _, err := os.Stat(filepath.Join(appDir, tc.expectedPath)); !os.IsNotExist(err) {
				t.Fatalf("dry-run should not create %s; stat error=%v", tc.expectedPath, err)
			}
		})
	}
}

func TestRunAppliesMinimalLaravelPermissionRepairAndVerifies(t *testing.T) {
	t.Parallel()

	appDir := newLaravelApp(t)
	logsDir := filepath.Join(appDir, "storage", "logs")
	chmod(t, logsDir, 0o500)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := os.WriteFile(filepath.Join(logsDir, "audit.log"), []byte("ok\n"), 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("orders ok"))
	}))
	t.Cleanup(server.Close)

	result, err := permissions.Run(context.Background(), permissions.Options{
		ServiceName:    "orders-api",
		TargetDir:      appDir,
		Framework:      "laravel",
		Apply:          true,
		VerifyURL:      server.URL,
		ExpectedStatus: http.StatusOK,
		Now:            fixedTime(),
	})
	if err != nil {
		t.Fatalf("run permission resolver apply: %v", err)
	}

	if got := modePerm(t, logsDir); got != 0o775 {
		t.Fatalf("expected repaired mode 0775, got %04o", got)
	}
	if result.RollbackPath == "" {
		t.Fatal("expected rollback manifest path")
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
	if !strings.Contains(result.Receipt.BeforeState, "storage/logs") || !strings.Contains(result.Receipt.BeforeState, "0500") {
		t.Fatalf("receipt should include before stat evidence, got %q", result.Receipt.BeforeState)
	}
	if !strings.Contains(result.Receipt.AfterState, "storage/logs") || !strings.Contains(result.Receipt.AfterState, "0775") {
		t.Fatalf("receipt should include after stat evidence, got %q", result.Receipt.AfterState)
	}
}

func TestRunRollsBackWhenHTTPVerificationFails(t *testing.T) {
	t.Parallel()

	appDir := newLaravelApp(t)
	logsDir := filepath.Join(appDir, "storage", "logs")
	chmod(t, logsDir, 0o500)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "still broken", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	_, err := permissions.Run(context.Background(), permissions.Options{
		ServiceName:    "orders-api",
		TargetDir:      appDir,
		Framework:      "laravel",
		Apply:          true,
		VerifyURL:      server.URL,
		ExpectedStatus: http.StatusOK,
		Now:            fixedTime(),
	})
	if err == nil {
		t.Fatal("expected verification failure")
	}
	if got := modePerm(t, logsDir); got != 0o500 {
		t.Fatalf("expected rollback to restore mode 0500, got %04o", got)
	}
}

func TestRunBlocksLaravelPolicyPathEscapingAppRoot(t *testing.T) {
	t.Parallel()

	appDir := newLaravelApp(t)
	logsDir := filepath.Join(appDir, "storage", "logs")
	outside := t.TempDir()
	if err := os.Remove(logsDir); err != nil {
		t.Fatalf("remove logs dir: %v", err)
	}
	if err := os.Symlink(outside, logsDir); err != nil {
		t.Fatalf("create escaping symlink: %v", err)
	}

	result, err := permissions.Run(context.Background(), permissions.Options{
		ServiceName: "orders-api",
		TargetDir:   appDir,
		Framework:   "auto",
		Apply:       true,
		Now:         fixedTime(),
	})
	if err != nil {
		t.Fatalf("unsafe policy path should return blocked contracts, got error: %v", err)
	}
	if result.RemediationPlan.RiskLevel != contractsv1.SafetyBlocked {
		t.Fatalf("expected blocked remediation plan, got %q", result.RemediationPlan.RiskLevel)
	}
	if result.Attempt.Status != contractsv1.RemediationStatusEscalated {
		t.Fatalf("expected escalated attempt, got %q", result.Attempt.Status)
	}
	if !strings.Contains(result.RemediationPlan.UserMessage, "escapes app root") {
		t.Fatalf("blocked plan should explain path escape, got %q", result.RemediationPlan.UserMessage)
	}
}

func TestRunBlocksLaravelPolicyPathThatIsNotDirectory(t *testing.T) {
	t.Parallel()

	appDir := newLaravelApp(t)
	logsDir := filepath.Join(appDir, "storage", "logs")
	if err := os.Remove(logsDir); err != nil {
		t.Fatalf("remove logs dir: %v", err)
	}
	writeFile(t, logsDir, "not a directory\n")

	result, err := permissions.Run(context.Background(), permissions.Options{
		ServiceName: "orders-api",
		TargetDir:   appDir,
		Framework:   "auto",
		Apply:       true,
		Now:         fixedTime(),
	})
	if err != nil {
		t.Fatalf("unsafe file policy path should return blocked contracts, got error: %v", err)
	}
	if result.RemediationPlan.RiskLevel != contractsv1.SafetyBlocked {
		t.Fatalf("expected blocked remediation plan, got %q", result.RemediationPlan.RiskLevel)
	}
	if !strings.Contains(result.RemediationPlan.UserMessage, "is not a directory") {
		t.Fatalf("blocked plan should explain non-directory path, got %q", result.RemediationPlan.UserMessage)
	}
}

func newLaravelApp(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "artisan"), "#!/usr/bin/env php\n")
	writeFile(t, filepath.Join(root, "composer.json"), `{"require":{"laravel/framework":"^11.0"}}`+"\n")
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

func newPolicyApp(t *testing.T, markers map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range markers {
		writeFile(t, filepath.Join(root, path), content)
	}
	return root
}

func policyHasPath(policy permissions.PermissionPolicy, path string) bool {
	for _, expected := range policy.ExpectedPaths {
		if expected.RelativePath == path {
			return true
		}
	}
	return false
}

func operationForPath(operations []permissions.PermissionOperation, path string) bool {
	for _, operation := range operations {
		if operation.RelativePath == path {
			return true
		}
	}
	return false
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s to %04o: %v", path, mode, err)
	}
}

func modePerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

func fixedTime() time.Time {
	return time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC)
}

func TestMain(m *testing.M) {
	code := m.Run()
	if code != 0 {
		fmt.Fprintln(os.Stderr, "permission resolver tests failed")
	}
	os.Exit(code)
}
