package rollbackmanifest_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CloudSpaceLab/ai-logfixer/internal/remediation/rollbackmanifest"
)

func TestExecuteRestoresFileRemovesCreatedPathAndChmods(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "config.json"), `{"broken":true}`+"\n", 0o644)
	writeFile(t, filepath.Join(root, ".ai-logfixer", "backups", "config.json"), `{"broken":false}`+"\n", 0o600)
	if err := os.MkdirAll(filepath.Join(root, "tmp", "created"), 0o755); err != nil {
		t.Fatalf("create temporary path: %v", err)
	}
	writeFile(t, filepath.Join(root, "storage", "logs", "app.log"), "log\n", 0o600)

	manifest := rollbackmanifest.New(root, fixedTime(), []rollbackmanifest.Entry{
		{Action: rollbackmanifest.ActionRestoreFile, Path: "config.json", BackupPath: ".ai-logfixer/backups/config.json", Mode: "0644"},
		{Action: rollbackmanifest.ActionRemoveCreatedPath, Path: "tmp/created"},
		{Action: rollbackmanifest.ActionChmod, Path: "storage/logs/app.log", Mode: "0775"},
	})
	manifestPath := filepath.Join(root, ".ai-logfixer", "rollback.json")
	if err := rollbackmanifest.Write(manifestPath, manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	result, err := rollbackmanifest.Execute(manifestPath)
	if err != nil {
		t.Fatalf("execute rollback manifest: %v", err)
	}

	if len(result.Executed) != 3 {
		t.Fatalf("expected three executed steps, got %+v", result.Executed)
	}
	assertFileContent(t, filepath.Join(root, "config.json"), `{"broken":false}`+"\n")
	if _, err := os.Stat(filepath.Join(root, "tmp", "created")); !os.IsNotExist(err) {
		t.Fatalf("expected created path to be removed, stat err=%v", err)
	}
	if got := modePerm(t, filepath.Join(root, "storage", "logs", "app.log")); got != 0o775 {
		t.Fatalf("expected chmod rollback to set 0775, got %04o", got)
	}
}

func TestExecuteBlocksPathEscapes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manifest := rollbackmanifest.New(root, fixedTime(), []rollbackmanifest.Entry{
		{Action: rollbackmanifest.ActionRemoveCreatedPath, Path: "../outside"},
	})
	manifestPath := filepath.Join(root, "rollback.json")
	if err := rollbackmanifest.Write(manifestPath, manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	_, err := rollbackmanifest.Execute(manifestPath)
	if err == nil {
		t.Fatal("expected path escape to be blocked")
	}
}

func TestExecuteRejectsUnknownAction(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	manifest := rollbackmanifest.New(root, fixedTime(), []rollbackmanifest.Entry{
		{Action: rollbackmanifest.Action("shell"), Path: "config.json"},
	})
	manifestPath := filepath.Join(root, "rollback.json")
	if err := rollbackmanifest.Write(manifestPath, manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	_, err := rollbackmanifest.Execute(manifestPath)
	if err == nil {
		t.Fatal("expected unknown action to be rejected")
	}
}

func writeFile(t *testing.T, path string, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertFileContent(t *testing.T, path string, expected string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(raw) != expected {
		t.Fatalf("expected %s content %q, got %q", path, expected, string(raw))
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
	return time.Date(2026, 6, 8, 13, 30, 0, 0, time.UTC)
}
