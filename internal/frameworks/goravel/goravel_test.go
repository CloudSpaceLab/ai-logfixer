package goravel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
)

func TestParseAccessLogGroupsRepeatedRouteFailures(t *testing.T) {
	t.Parallel()

	content := strings.Join([]string{
		"\x1b[32m\x1b[0m" + `[HTTP] 2026-05-24 08:21:13.004 | 500 |      1.353725ms |       127.0.0.1 | GET      "/users"`,
		`[HTTP] 2026-05-24 08:21:13.020 | 500 | 465.558us | 127.0.0.1 | GET "/users"`,
		`[HTTP] 2026-05-24 08:21:13.035 | 500 | 424.13us | 127.0.0.1 | GET "/users"`,
		`[HTTP] 2026-05-24 08:21:13.049 | 500 | 292.711us | 127.0.0.1 | GET "/users"`,
		`[HTTP] 2026-05-24 08:21:13.063 | 500 | 373.521us | 127.0.0.1 | GET "/users"`,
		`[HTTP] 2026-05-24 08:21:15.000 | 200 | 300us | 127.0.0.1 | GET "/"`,
	}, "\n")

	entries := ParseAccessLog(content)
	if len(entries) != 6 {
		t.Fatalf("expected 6 parsed entries, got %d", len(entries))
	}

	groups := RepeatedFailures(entries, FailureThreshold{
		ServiceName: "real-goravel-app",
		MinCount:    3,
		Window:      time.Minute,
	})
	if len(groups) != 1 {
		t.Fatalf("expected one repeated failure group, got %+v", groups)
	}
	group := groups[0]
	if group.Method != "GET" || group.Route != "/users" || group.StatusClass != 500 || group.Count != 5 {
		t.Fatalf("unexpected group: %+v", group)
	}
}

func TestAnalyzeMapsGoravelRouteToControllerAndBuildsSourcePatchPlan(t *testing.T) {
	t.Parallel()

	targetDir, logPath, controllerPath := newGoravelPanicFixture(t)

	analysis, err := Analyze(Options{
		ServiceName:   "real-goravel-app",
		TargetDir:     targetDir,
		AccessLogPath: logPath,
		Threshold:     3,
		Window:        time.Minute,
		Now:           time.Date(2026, 5, 25, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("analyze Goravel fixture: %v", err)
	}

	if analysis.Route.ControllerType != "UserController" || analysis.Route.HandlerMethod != "Index" {
		t.Fatalf("expected UserController.Index mapping, got %+v", analysis.Route)
	}
	if filepath.Clean(analysis.Route.HandlerFile) != filepath.Clean(controllerPath) {
		t.Fatalf("expected handler path %q, got %q", controllerPath, analysis.Route.HandlerFile)
	}
	if !strings.Contains(analysis.HandlerExcerpt, "panic(") {
		t.Fatalf("expected panic source evidence, got %s", analysis.HandlerExcerpt)
	}
	if analysis.Diagnosis.Status != contractsv1.DiagnosisStatusComplete {
		t.Fatalf("expected complete diagnosis, got %q", analysis.Diagnosis.Status)
	}
	if analysis.Diagnosis.PatchPlan == nil || analysis.Diagnosis.PatchPlan.TargetType != contractsv1.PatchTargetFile {
		t.Fatalf("expected file patch plan, got %+v", analysis.Diagnosis.PatchPlan)
	}
	if !analysis.Diagnosis.PatchPlan.RequiresApproval {
		t.Fatal("expected source patch plan to require approval")
	}
	if analysis.RemediationPlan.Status != contractsv1.RemediationStatusAwaitingApproval {
		t.Fatalf("expected awaiting approval plan, got %q", analysis.RemediationPlan.Status)
	}
	if analysis.RemediationPlan.RollbackPlan.RollbackType != contractsv1.RollbackSnapshot {
		t.Fatalf("expected snapshot rollback, got %+v", analysis.RemediationPlan.RollbackPlan)
	}
}

func TestExecutePanicPatchBacksUpPatchesRestartsAndVerifies(t *testing.T) {
	t.Parallel()

	targetDir, logPath, controllerPath := newGoravelPanicFixture(t)
	analysis, err := Analyze(Options{
		ServiceName:   "real-goravel-app",
		TargetDir:     targetDir,
		AccessLogPath: logPath,
		Threshold:     3,
		Window:        time.Minute,
		Now:           time.Date(2026, 5, 25, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("analyze Goravel fixture: %v", err)
	}

	restarted := false
	result, err := ExecutePanicPatch(context.Background(), analysis, ExecutionOptions{
		BackupDir: filepath.Join(targetDir, ".ai-logfixer-backups", "goravel-source-test"),
		Now:       time.Date(2026, 5, 25, 9, 1, 0, 0, time.UTC),
		Restart: func(ctx context.Context) error {
			restarted = true
			return nil
		},
		Verify: func(ctx context.Context) error {
			raw, err := os.ReadFile(controllerPath)
			if err != nil {
				return err
			}
			if strings.Contains(string(raw), "panic(") {
				return errors.New("panic still present")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("execute panic patch: %v", err)
	}

	if !restarted {
		t.Fatal("expected restart callback to run")
	}
	if result.Attempt.Status != contractsv1.RemediationStatusSucceeded {
		t.Fatalf("expected succeeded attempt, got %q", result.Attempt.Status)
	}
	if result.Receipt.Outcome != "succeeded" {
		t.Fatalf("expected succeeded receipt, got %q", result.Receipt.Outcome)
	}
	if !result.SourceFile.Applied || !result.SourceFile.Restarted || !result.SourceFile.Verified {
		t.Fatalf("unexpected source execution result: %+v", result.SourceFile)
	}
	if _, err := os.Stat(result.SourceFile.BackupPath); err != nil {
		t.Fatalf("expected source backup to exist: %v", err)
	}
	raw, err := os.ReadFile(controllerPath)
	if err != nil {
		t.Fatalf("read patched controller: %v", err)
	}
	if strings.Contains(string(raw), "panic(") {
		t.Fatalf("expected panic to be removed, got %s", raw)
	}
}

func TestExecutePanicPatchRollsBackWhenVerificationFails(t *testing.T) {
	t.Parallel()

	targetDir, logPath, controllerPath := newGoravelPanicFixture(t)
	analysis, err := Analyze(Options{
		ServiceName:   "real-goravel-app",
		TargetDir:     targetDir,
		AccessLogPath: logPath,
		Threshold:     3,
		Window:        time.Minute,
		Now:           time.Date(2026, 5, 25, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("analyze Goravel fixture: %v", err)
	}

	result, err := ExecutePanicPatch(context.Background(), analysis, ExecutionOptions{
		BackupDir: filepath.Join(targetDir, ".ai-logfixer-backups", "goravel-source-test"),
		Now:       time.Date(2026, 5, 25, 9, 1, 0, 0, time.UTC),
		Verify: func(ctx context.Context) error {
			return errors.New("route still returns 500")
		},
	})
	if err == nil {
		t.Fatal("expected verification error")
	}
	if result.Attempt.Status != contractsv1.RemediationStatusRolledBack {
		t.Fatalf("expected rolled back attempt, got %q", result.Attempt.Status)
	}
	if result.Receipt.Outcome != "rolled_back" {
		t.Fatalf("expected rolled back receipt, got %q", result.Receipt.Outcome)
	}
	if !result.SourceFile.RolledBack {
		t.Fatalf("expected source execution to roll back, got %+v", result.SourceFile)
	}

	raw, err := os.ReadFile(controllerPath)
	if err != nil {
		t.Fatalf("read rolled back controller: %v", err)
	}
	if !strings.Contains(string(raw), "panic(") {
		t.Fatalf("expected panic to be restored after rollback, got %s", raw)
	}
}

func TestAnalyzeMapsNonUserRouteDynamically(t *testing.T) {
	t.Parallel()

	targetDir, logPath, controllerPath := newGoravelCustomFixture(t, "/checkout", "CheckoutController", "Show", `    panic("checkout failed before response")

    return ctx.Response().Success().Json(http.Json{
        "ok": true,
    })`, "")

	analysis, err := Analyze(Options{
		ServiceName:   "checkout-api",
		TargetDir:     targetDir,
		AccessLogPath: logPath,
		Threshold:     3,
		Window:        time.Minute,
		Now:           time.Date(2026, 5, 25, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("analyze non-user route: %v", err)
	}
	if analysis.Route.Path != "/checkout" || analysis.Route.ControllerType != "CheckoutController" || analysis.Route.HandlerMethod != "Show" {
		t.Fatalf("unexpected dynamic route mapping: %+v", analysis.Route)
	}
	if filepath.Clean(analysis.Route.HandlerFile) != filepath.Clean(controllerPath) {
		t.Fatalf("expected handler path %q, got %q", controllerPath, analysis.Route.HandlerFile)
	}
	if analysis.RemediationPlan.Status != contractsv1.RemediationStatusAwaitingApproval {
		t.Fatalf("expected source patch awaiting approval, got %q", analysis.RemediationPlan.Status)
	}
}

func TestExecutePanicPatchOnlyRemovesPanicInMappedHandler(t *testing.T) {
	t.Parallel()

	targetDir, logPath, controllerPath := newGoravelCustomFixture(t, "/checkout", "CheckoutController", "Show", `    panic("mapped checkout handler failed")

    return ctx.Response().Success().Json(http.Json{
        "ok": true,
    })`, `func (r *CheckoutController) Other(ctx http.Context) http.Response {
    panic("unrelated handler panic must stay")
}
`)

	analysis, err := Analyze(Options{
		ServiceName:   "checkout-api",
		TargetDir:     targetDir,
		AccessLogPath: logPath,
		Threshold:     3,
		Window:        time.Minute,
		Now:           time.Date(2026, 5, 25, 10, 5, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("analyze checkout fixture: %v", err)
	}

	result, err := ExecutePanicPatch(context.Background(), analysis, ExecutionOptions{
		BackupDir: filepath.Join(targetDir, ".ai-logfixer-backups", "handler-scope-test"),
		Now:       time.Date(2026, 5, 25, 10, 6, 0, 0, time.UTC),
		Verify: func(ctx context.Context) error {
			raw, err := os.ReadFile(controllerPath)
			if err != nil {
				return err
			}
			content := string(raw)
			if strings.Contains(content, `panic("mapped checkout handler failed")`) {
				return errors.New("mapped handler panic still present")
			}
			if !strings.Contains(content, `panic("unrelated handler panic must stay")`) {
				return errors.New("unrelated handler panic was removed")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("execute scoped panic patch: %v", err)
	}
	if result.Attempt.Status != contractsv1.RemediationStatusSucceeded {
		t.Fatalf("expected succeeded attempt, got %q", result.Attempt.Status)
	}
}

func TestAnalyzeBlocksMultiplePanicLinesInMappedHandler(t *testing.T) {
	t.Parallel()

	targetDir, logPath, _ := newGoravelCustomFixture(t, "/checkout", "CheckoutController", "Show", `    panic("first panic")
    panic("second panic")
`, "")

	analysis, err := Analyze(Options{
		ServiceName:   "checkout-api",
		TargetDir:     targetDir,
		AccessLogPath: logPath,
		Threshold:     3,
		Window:        time.Minute,
		Now:           time.Date(2026, 5, 25, 10, 10, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("analyze multiple panic fixture: %v", err)
	}
	if analysis.RemediationPlan.Status != contractsv1.RemediationStatusEscalated || analysis.RemediationPlan.RiskLevel != contractsv1.SafetyBlocked {
		t.Fatalf("expected blocked/escalated plan, got %+v", analysis.RemediationPlan)
	}
	if analysis.Diagnosis.PatchPlan == nil || analysis.Diagnosis.PatchPlan.RiskLevel != contractsv1.SafetyBlocked {
		t.Fatalf("expected blocked diagnosis patch plan, got %+v", analysis.Diagnosis.PatchPlan)
	}
	if _, err := ExecutePanicPatch(context.Background(), analysis, ExecutionOptions{}); err == nil {
		t.Fatal("expected blocked patch execution error")
	}
}

func TestAnalyzeBlocksHandlerWithoutAllowlistedPatch(t *testing.T) {
	t.Parallel()

	targetDir, logPath, _ := newGoravelCustomFixture(t, "/checkout", "CheckoutController", "Show", `    return ctx.Response().Success().Json(http.Json{
        "ok": true,
    })`, "")

	analysis, err := Analyze(Options{
		ServiceName:   "checkout-api",
		TargetDir:     targetDir,
		AccessLogPath: logPath,
		Threshold:     3,
		Window:        time.Minute,
		Now:           time.Date(2026, 5, 25, 10, 15, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("analyze no-panic fixture: %v", err)
	}
	if analysis.PatchSafety.Safe {
		t.Fatal("handler without panic should not be considered safe to auto-patch")
	}
	if analysis.RemediationPlan.Status != contractsv1.RemediationStatusEscalated || analysis.RemediationPlan.RiskLevel != contractsv1.SafetyBlocked {
		t.Fatalf("expected blocked/escalated plan, got %+v", analysis.RemediationPlan)
	}
}

func newGoravelPanicFixture(t *testing.T) (string, string, string) {
	t.Helper()

	targetDir := t.TempDir()
	logPath := filepath.Join(targetDir, "storage", "logs", "goravel.log")
	mustWrite(t, filepath.Join(targetDir, "routes", "web.go"), `package routes

import (
    "github.com/goravel/framework/facades"
    "goravel/app/http/controllers"
)

func Web() {
    userController := controllers.NewUserController()
    facades.Route().Get("/users", userController.Index)
}
`)
	controllerPath := filepath.Join(targetDir, "app", "http", "controllers", "user_controller.go")
	mustWrite(t, controllerPath, `package controllers

import "github.com/goravel/framework/contracts/http"

type UserController struct {}

func NewUserController() *UserController {
    return &UserController{}
}

func (r *UserController) Index(ctx http.Context) http.Response {
    panic("random framework test fault: user controller crashed before response")

    return ctx.Response().Success().Json(http.Json{
        "Hello": "Goravel",
    })
}
`)
	mustWrite(t, logPath, strings.Join([]string{
		`[HTTP] 2026-05-24 08:21:13.004 | 500 | 1.353725ms | 127.0.0.1 | GET "/users"`,
		`[HTTP] 2026-05-24 08:21:13.020 | 500 | 465.558us | 127.0.0.1 | GET "/users"`,
		`[HTTP] 2026-05-24 08:21:13.035 | 500 | 424.13us | 127.0.0.1 | GET "/users"`,
		`[HTTP] 2026-05-24 08:21:13.049 | 500 | 292.711us | 127.0.0.1 | GET "/users"`,
	}, "\n"))

	return targetDir, logPath, controllerPath
}

func newGoravelCustomFixture(t *testing.T, routePath string, controllerType string, handlerMethod string, handlerBody string, extraMethods string) (string, string, string) {
	t.Helper()

	targetDir := t.TempDir()
	logPath := filepath.Join(targetDir, "storage", "logs", "goravel.log")
	controllerVar := strings.ToLower(controllerType[:1]) + controllerType[1:]
	mustWrite(t, filepath.Join(targetDir, "routes", "web.go"), `package routes

import (
    "github.com/goravel/framework/facades"
    "goravel/app/http/controllers"
)

func Web() {
    `+controllerVar+` := controllers.New`+controllerType+`()
    facades.Route().Get("`+routePath+`", `+controllerVar+`.`+handlerMethod+`)
}
`)
	controllerPath := filepath.Join(targetDir, "app", "http", "controllers", snakeCase(controllerType)+".go")
	mustWrite(t, controllerPath, `package controllers

import "github.com/goravel/framework/contracts/http"

type `+controllerType+` struct {}

func New`+controllerType+`() *`+controllerType+` {
    return &`+controllerType+`{}
}

func (r *`+controllerType+`) `+handlerMethod+`(ctx http.Context) http.Response {
`+handlerBody+`
}

`+extraMethods+`
`)
	mustWrite(t, logPath, strings.Join([]string{
		`[HTTP] 2026-05-24 08:21:13.004 | 500 | 1.353725ms | 127.0.0.1 | GET "` + routePath + `"`,
		`[HTTP] 2026-05-24 08:21:13.020 | 500 | 465.558us | 127.0.0.1 | GET "` + routePath + `"`,
		`[HTTP] 2026-05-24 08:21:13.035 | 500 | 424.13us | 127.0.0.1 | GET "` + routePath + `"`,
		`[HTTP] 2026-05-24 08:21:13.049 | 500 | 292.711us | 127.0.0.1 | GET "` + routePath + `"`,
	}, "\n"))

	return targetDir, logPath, controllerPath
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
