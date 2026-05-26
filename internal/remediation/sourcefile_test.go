package remediation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestApplySourceEditBacksUpPatchesRestartsAndVerifies(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	sourcePath := filepath.Join(targetDir, "app", "http", "controllers", "user_controller.go")
	mustWrite(t, sourcePath, "func Index() {\n    panic(\"boom\")\n    ok()\n}\n")

	restarted := false
	verified := false
	result, err := ApplySourceEdit(context.Background(), SourceFileOptions{
		Edit: SourceEdit{
			Path:   sourcePath,
			Before: "    panic(\"boom\")\n",
			After:  "",
		},
		BackupDir: filepath.Join(targetDir, ".ai-logfixer-backups", "source-test"),
		Now:       time.Date(2026, 5, 25, 10, 0, 0, 0, time.UTC),
		Restart: func(ctx context.Context) error {
			restarted = true
			return nil
		},
		Verify: func(ctx context.Context) error {
			verified = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("apply source edit: %v", err)
	}
	if !result.Applied || !result.Restarted || !result.Verified || result.BackupPath == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !restarted || !verified {
		t.Fatalf("expected restart and verify callbacks, restarted=%t verified=%t", restarted, verified)
	}

	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read patched source: %v", err)
	}
	if strings.Contains(string(raw), "panic(") {
		t.Fatalf("expected panic to be removed, got %s", raw)
	}
	if _, err := os.Stat(result.BackupPath); err != nil {
		t.Fatalf("expected backup path to exist: %v", err)
	}
}

func TestApplySourceEditRollsBackWhenVerificationFails(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	sourcePath := filepath.Join(targetDir, "app.go")
	original := "func Index() {\n    panic(\"boom\")\n    ok()\n}\n"
	mustWrite(t, sourcePath, original)

	result, err := ApplySourceEdit(context.Background(), SourceFileOptions{
		Edit: SourceEdit{
			Path:   sourcePath,
			Before: "    panic(\"boom\")\n",
			After:  "",
		},
		BackupDir: filepath.Join(targetDir, ".ai-logfixer-backups", "source-test"),
		Verify: func(ctx context.Context) error {
			return errors.New("route still failing")
		},
	})
	if err == nil {
		t.Fatal("expected verification error")
	}
	if !result.Applied || !result.RolledBack {
		t.Fatalf("expected applied patch to roll back, got %+v", result)
	}

	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read rolled back source: %v", err)
	}
	if string(raw) != original {
		t.Fatalf("expected original source after rollback, got %s", raw)
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
