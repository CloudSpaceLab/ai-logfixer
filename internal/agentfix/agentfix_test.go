package agentfix

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func mustWrite(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
