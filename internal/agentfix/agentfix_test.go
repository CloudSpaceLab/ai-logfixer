package agentfix

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunStagesExternalAgentPatchAppliesAndRollsBack(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	targetFile := filepath.Join(targetDir, "app", "Service.php")
	mustWrite(t, targetFile, "<?php\nreturn 'broken';\n")

	result, err := Run(context.Background(), Options{
		TargetDir: targetDir,
		Prompt:    "Fix the broken service.",
		Apply:     true,
		Now:       time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC),
		AgentRunner: func(ctx context.Context, agent AgentContext) (CommandOutput, error) {
			path := filepath.Join(agent.StagingDir, "app", "Service.php")
			return CommandOutput{}, os.WriteFile(path, []byte("<?php\nreturn 'fixed';\n"), 0o644)
		},
	})
	if err != nil {
		t.Fatalf("run agentfix: %v", err)
	}
	if !result.Applied {
		t.Fatal("expected patch to be applied")
	}
	if !result.RollbackAvailable || result.ManifestPath == "" {
		t.Fatalf("expected rollback manifest: %+v", result)
	}
	if len(result.Changes) != 1 || result.Changes[0].Type != ChangeModify {
		t.Fatalf("expected one modify change, got %+v", result.Changes)
	}
	raw, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("read target after apply: %v", err)
	}
	if !strings.Contains(string(raw), "fixed") {
		t.Fatalf("expected target file to be fixed, got %s", raw)
	}

	if err := Rollback(result.ManifestPath); err != nil {
		t.Fatalf("rollback agent patch: %v", err)
	}
	raw, err = os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("read target after rollback: %v", err)
	}
	if !strings.Contains(string(raw), "broken") {
		t.Fatalf("expected target file to be restored, got %s", raw)
	}
}

func TestRunValidationFailurePreventsApply(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	targetFile := filepath.Join(targetDir, "app.php")
	mustWrite(t, targetFile, "broken\n")

	result, err := Run(context.Background(), Options{
		TargetDir:          targetDir,
		Prompt:             "Fix the broken file.",
		Apply:              true,
		ValidationCommands: []string{"exit 7"},
		Now:                time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC),
		AgentRunner: func(ctx context.Context, agent AgentContext) (CommandOutput, error) {
			return CommandOutput{}, os.WriteFile(filepath.Join(agent.StagingDir, "app.php"), []byte("fixed\n"), 0o644)
		},
	})
	if err != nil {
		t.Fatalf("run agentfix: %v", err)
	}
	if result.Applied {
		t.Fatal("expected validation failure to prevent apply")
	}
	if result.ValidationPassed {
		t.Fatal("expected validation to fail")
	}
	if len(result.ValidationResults) != 1 || result.ValidationResults[0].ExitCode == 0 {
		t.Fatalf("expected failing validation result, got %+v", result.ValidationResults)
	}
	raw, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("read target after failed validation: %v", err)
	}
	if string(raw) != "broken\n" {
		t.Fatalf("expected target file unchanged, got %s", raw)
	}
}

func TestRunAgentCommandTimeoutKillsChildProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process group timeout behavior is implemented separately on windows")
	}

	targetDir := t.TempDir()
	mustWrite(t, filepath.Join(targetDir, "app.php"), "broken\n")

	scriptPath := filepath.Join(t.TempDir(), "spawn-child.sh")
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	mustWrite(t, scriptPath, "#!/bin/sh\nsleep 30 &\necho $! > \"$1\"\nwait $!\n")
	if err := os.Chmod(scriptPath, 0o755); err != nil {
		t.Fatalf("chmod script: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		output CommandOutput
		err    error
	}, 1)
	go func() {
		output, err := runAgentCommand(ctx, AgentContext{
			TargetDir:  targetDir,
			StagingDir: targetDir,
			Prompt:     "Run a long agent.",
			PromptPath: filepath.Join(targetDir, "prompt.md"),
			Command:    []string{scriptPath, pidPath},
		})
		done <- struct {
			output CommandOutput
			err    error
		}{output: output, err: err}
	}()

	pid := readPID(t, pidPath)
	started := time.Now()
	cancel()
	var runResult struct {
		output CommandOutput
		err    error
	}
	select {
	case runResult = <-done:
	case <-time.After(2 * time.Second):
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("expected canceled agent command to return within 2s")
	}
	elapsed := time.Since(started)
	if runResult.err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("expected timeout to return quickly, took %s", elapsed)
	}

	time.Sleep(100 * time.Millisecond)
	if processExists(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("expected timeout to kill child process %d", pid)
	}
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

func readPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if parseErr != nil {
				t.Fatalf("parse pid %q: %v", raw, parseErr)
			}
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid file was not written: %s", path)
	return 0
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}
