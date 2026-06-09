package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	packagerollback "github.com/CloudSpaceLab/ai-logfixer/internal/resolvers/packages"
)

func TestRunAppliesPackageRollbackAndWritesJSONResult(t *testing.T) {
	t.Parallel()

	appDir := t.TempDir()
	packagePath := filepath.Join(appDir, "package.json")
	currentSpec := "file:../packages/widget-v2.0.0"
	knownGoodSpec := "file:../packages/widget-v1.2.2"
	writeTestPackageJSON(t, packagePath, `{
  "name": "checkout-app",
  "dependencies": {
    "@acme/widget": "`+currentSpec+`"
  }
}
`)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{
		"-package-file", packagePath,
		"-package", "@acme/widget",
		"-current", currentSpec,
		"-known-good", knownGoodSpec,
		"-verify-command", "grep -q 'file:../packages/widget-v1.2.2' package.json",
		"-workdir", appDir,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d stderr=%s", code, stderr.String())
	}

	var result packagerollback.Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode command output: %v\n%s", err, stdout.String())
	}
	if !result.Applied || result.RolledBack {
		t.Fatalf("expected applied rollback result, got %+v", result)
	}
	if result.Verification.Status != packagerollback.VerificationSucceeded {
		t.Fatalf("expected successful verification, got %+v", result.Verification)
	}
	if result.Rollback.ManifestPath == "" {
		t.Fatalf("expected rollback manifest path, got %+v", result.Rollback)
	}
	if !strings.Contains(stderr.String(), "Package rollback completed") {
		t.Fatalf("expected completion message, got %q", stderr.String())
	}
}

func writeTestPackageJSON(t *testing.T, path string, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create package dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write package json: %v", err)
	}
}
