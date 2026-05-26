package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
)

func TestRunDryRunOutputsGoravelContracts(t *testing.T) {
	t.Parallel()

	targetDir, logPath, _ := newGoravelCLIFileFixture(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run([]string{
		"-target", targetDir,
		"-access-log", logPath,
		"-service", "real-goravel-app",
		"-threshold", "3",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", code, stderr.String())
	}

	var output runOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode stdout: %v\n%s", err, stdout.String())
	}
	if output.Failure.Route != "/users" || output.Failure.StatusClass != 500 {
		t.Fatalf("unexpected failure output: %+v", output.Failure)
	}
	if output.Route.ControllerType != "UserController" || output.Route.HandlerMethod != "Index" {
		t.Fatalf("unexpected route output: %+v", output.Route)
	}
	if output.RemediationPlan.Status != contractsv1.RemediationStatusAwaitingApproval {
		t.Fatalf("expected awaiting approval plan, got %q", output.RemediationPlan.Status)
	}
	if output.Attempt != nil || output.Receipt != nil || output.SourceFile != nil {
		t.Fatalf("dry run should not include execution output: %+v", output)
	}
	if !strings.Contains(stderr.String(), "Goravel analysis dry run completed") {
		t.Fatalf("expected dry-run status on stderr, got %q", stderr.String())
	}
}

func TestRunApplyPatchesAndEmitsReceipt(t *testing.T) {
	t.Parallel()

	targetDir, logPath, controllerPath := newGoravelCLIFileFixture(t)
	backupDir := filepath.Join(targetDir, ".ai-logfixer-backups", "cli-test")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run([]string{
		"-target", targetDir,
		"-access-log", logPath,
		"-service", "real-goravel-app",
		"-threshold", "3",
		"-apply=true",
		"-approve-source-patch=true",
		"-backup-dir", backupDir,
		"-restart-command", "cd .",
		"-verify-command", "cd .",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d: %s\nstdout=%s", code, stderr.String(), stdout.String())
	}

	var output runOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode stdout: %v\n%s", err, stdout.String())
	}
	if output.Attempt == nil || output.Attempt.Status != contractsv1.RemediationStatusSucceeded {
		t.Fatalf("expected succeeded attempt, got %+v", output.Attempt)
	}
	if output.Receipt == nil || output.Receipt.Outcome != "succeeded" {
		t.Fatalf("expected succeeded receipt, got %+v", output.Receipt)
	}
	if output.SourceFile == nil || !output.SourceFile.Applied || !output.SourceFile.Restarted || !output.SourceFile.Verified {
		t.Fatalf("unexpected source file result: %+v", output.SourceFile)
	}

	raw, err := os.ReadFile(controllerPath)
	if err != nil {
		t.Fatalf("read patched controller: %v", err)
	}
	if strings.Contains(string(raw), "panic(") {
		t.Fatalf("expected panic line to be removed, got %s", raw)
	}
	if !strings.Contains(stderr.String(), "Goravel source patch completed") {
		t.Fatalf("expected apply status on stderr, got %q", stderr.String())
	}
}

func TestRunApplyRequiresApprovalAndVerification(t *testing.T) {
	t.Parallel()

	targetDir, logPath, controllerPath := newGoravelCLIFileFixture(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run([]string{
		"-target", targetDir,
		"-access-log", logPath,
		"-service", "real-goravel-app",
		"-apply=true",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit code without approval")
	}
	if !strings.Contains(stderr.String(), "-approve-source-patch is required with -apply") {
		t.Fatalf("expected approval error, got %q", stderr.String())
	}

	raw, err := os.ReadFile(controllerPath)
	if err != nil {
		t.Fatalf("read controller: %v", err)
	}
	if !strings.Contains(string(raw), "panic(") {
		t.Fatalf("source should not be patched without approval, got %s", raw)
	}
}

func TestRunShellCommandReturnsWhenChildOutlivesShell(t *testing.T) {
	t.Parallel()

	started := time.Now()
	if err := runShellCommand(context.Background(), backgroundSleepCommand(), t.TempDir(), 2*time.Second); err != nil {
		t.Fatalf("run background command: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("expected command to return after shell exits, took %s", elapsed)
	}
}

func backgroundSleepCommand() string {
	if runtime.GOOS == "windows" {
		return `powershell -NoProfile -Command "Start-Process powershell -ArgumentList '-NoProfile -Command Start-Sleep -Seconds 3' -WindowStyle Hidden"`
	}
	return "sleep 3 &"
}

func newGoravelCLIFileFixture(t *testing.T) (string, string, string) {
	t.Helper()

	targetDir := t.TempDir()
	logPath := filepath.Join(targetDir, "storage", "logs", "goravel.log")
	writeFixtureFile(t, filepath.Join(targetDir, "routes", "web.go"), `package routes

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
	writeFixtureFile(t, controllerPath, `package controllers

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
	writeFixtureFile(t, logPath, strings.Join([]string{
		`[HTTP] 2026-05-24 08:21:13.004 | 500 | 1.353725ms | 127.0.0.1 | GET "/users"`,
		`[HTTP] 2026-05-24 08:21:13.020 | 500 | 465.558us | 127.0.0.1 | GET "/users"`,
		`[HTTP] 2026-05-24 08:21:13.035 | 500 | 424.13us | 127.0.0.1 | GET "/users"`,
		`[HTTP] 2026-05-24 08:21:13.049 | 500 | 292.711us | 127.0.0.1 | GET "/users"`,
	}, "\n"))

	return targetDir, logPath, controllerPath
}

func writeFixtureFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
