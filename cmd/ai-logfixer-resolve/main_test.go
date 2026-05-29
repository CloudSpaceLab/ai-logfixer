package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRunOutputsEscalationWithoutAgent(t *testing.T) {
	t.Parallel()

	targetDir := t.TempDir()
	writeCLIFile(t, filepath.Join(targetDir, "package.json"), `{"dependencies":{"express":"latest"}}`)
	sourcePath := filepath.Join(targetDir, "src", "orders.js")
	writeCLIFile(t, sourcePath, `exports.show = () => "BROKEN";`)
	tracePath := filepath.Join(targetDir, "trace.txt")
	writeCLIFile(t, tracePath, `Error: database unavailable
    at exports.show (`+sourcePath+`:1:21)`)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{
		"-target", targetDir,
		"-service", "node-cli",
		"-message", "database unavailable",
		"-stack-trace-file", tracePath,
		"-apply=true",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d stderr=%s", code, stderr.String())
	}

	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	profile := result["profile"].(map[string]any)
	if profile["language"] != "node" || profile["framework"] != "express" {
		t.Fatalf("unexpected profile: %+v", profile)
	}
	receipt := result["receipt"].(map[string]any)
	if receipt["outcome"] != "escalated" {
		t.Fatalf("expected escalated receipt, got %+v", receipt)
	}
}

func writeCLIFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
