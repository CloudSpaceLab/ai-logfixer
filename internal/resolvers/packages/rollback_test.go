package packages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRollbackLocalPackageRegressionWritesRollbackDataAndVerifies(t *testing.T) {
	t.Parallel()

	appDir := t.TempDir()
	packagePath := filepath.Join(appDir, "package.json")
	currentSpec := "file:../packages/widget-v2.0.0"
	knownGoodSpec := "file:../packages/widget-v1.2.2"
	writePackageJSON(t, packagePath, map[string]any{
		"name": "checkout-app",
		"dependencies": map[string]any{
			"@acme/widget": currentSpec,
		},
	})

	result, err := Rollback(context.Background(), Options{
		PackageFile:   packagePath,
		PackageName:   "@acme/widget",
		CurrentSpec:   currentSpec,
		KnownGoodSpec: knownGoodSpec,
		VerifyCommand: "grep -q 'file:../packages/widget-v1.2.2' package.json",
		WorkingDir:    appDir,
		Now:           time.Date(2026, 6, 8, 10, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("rollback package regression: %v", err)
	}

	if !result.Applied || result.RolledBack {
		t.Fatalf("expected verified rollback to remain applied, got %+v", result)
	}
	if result.PackageFile != packagePath {
		t.Fatalf("expected package file %q, got %q", packagePath, result.PackageFile)
	}
	if result.PackageName != "@acme/widget" {
		t.Fatalf("expected package name @acme/widget, got %q", result.PackageName)
	}
	if result.DependencySection != "dependencies" {
		t.Fatalf("expected dependency section, got %q", result.DependencySection)
	}
	if result.Before.Spec != currentSpec || result.After.Spec != knownGoodSpec {
		t.Fatalf("unexpected before/after: %+v -> %+v", result.Before, result.After)
	}
	if result.Verification.Status != VerificationSucceeded {
		t.Fatalf("expected successful verification, got %+v", result.Verification)
	}
	if result.Rollback.ManifestPath == "" || result.Rollback.BackupPath == "" {
		t.Fatalf("expected rollback manifest and backup paths, got %+v", result.Rollback)
	}

	spec := readPackageSpec(t, packagePath, "dependencies", "@acme/widget")
	if spec != knownGoodSpec {
		t.Fatalf("expected package spec to be rolled back to known-good local ref, got %q", spec)
	}
	backupSpec := readPackageSpec(t, result.Rollback.BackupPath, "dependencies", "@acme/widget")
	if backupSpec != currentSpec {
		t.Fatalf("expected backup to preserve broken spec %q, got %q", currentSpec, backupSpec)
	}

	var manifest RollbackManifest
	decodeJSONFile(t, result.Rollback.ManifestPath, &manifest)
	if manifest.PackageFile != packagePath {
		t.Fatalf("expected manifest package file %q, got %q", packagePath, manifest.PackageFile)
	}
	if manifest.PackageName != "@acme/widget" ||
		manifest.DependencySection != "dependencies" ||
		manifest.BeforeSpec != currentSpec ||
		manifest.AfterSpec != knownGoodSpec ||
		manifest.BackupPath != result.Rollback.BackupPath {
		t.Fatalf("manifest did not capture rollback evidence: %+v", manifest)
	}
}

func TestRollbackCanVerifyWithHTTPURL(t *testing.T) {
	t.Parallel()

	appDir := t.TempDir()
	packagePath := filepath.Join(appDir, "package.json")
	currentSpec := "file:../packages/widget-v2.0.0"
	knownGoodSpec := "file:../packages/widget-v1.2.2"
	writePackageJSON(t, packagePath, map[string]any{
		"name": "checkout-app",
		"dependencies": map[string]any{
			"@acme/widget": currentSpec,
		},
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spec, err := packageSpec(packagePath, "dependencies", "@acme/widget")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if spec != knownGoodSpec {
			http.Error(w, "package regression still present", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	result, err := Rollback(context.Background(), Options{
		PackageFile:    packagePath,
		PackageName:    "@acme/widget",
		CurrentSpec:    currentSpec,
		KnownGoodSpec:  knownGoodSpec,
		VerifyURL:      server.URL + "/health",
		ExpectedStatus: http.StatusNoContent,
		WorkingDir:     appDir,
		Now:            time.Date(2026, 6, 8, 10, 33, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("rollback package regression with HTTP verification: %v", err)
	}
	if result.Verification.Status != VerificationSucceeded ||
		result.Verification.Kind != "http" ||
		result.Verification.HTTPStatus != http.StatusNoContent {
		t.Fatalf("expected successful HTTP verification, got %+v", result.Verification)
	}
}

func TestRollbackRequiresManifestToMatchExplicitCurrentSpec(t *testing.T) {
	t.Parallel()

	appDir := t.TempDir()
	packagePath := filepath.Join(appDir, "package.json")
	actualSpec := "file:../packages/widget-v2.0.1"
	writePackageJSON(t, packagePath, map[string]any{
		"name": "checkout-app",
		"dependencies": map[string]any{
			"@acme/widget": actualSpec,
		},
	})

	_, err := Rollback(context.Background(), Options{
		PackageFile:   packagePath,
		PackageName:   "@acme/widget",
		CurrentSpec:   "file:../packages/widget-v2.0.0",
		KnownGoodSpec: "file:../packages/widget-v1.2.2",
		VerifyCommand: "true",
		WorkingDir:    appDir,
		Now:           time.Date(2026, 6, 8, 10, 31, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatal("expected explicit current spec mismatch to fail")
	}
	if !strings.Contains(err.Error(), "current spec mismatch") {
		t.Fatalf("expected current spec mismatch error, got %v", err)
	}

	spec := readPackageSpec(t, packagePath, "dependencies", "@acme/widget")
	if spec != actualSpec {
		t.Fatalf("expected package file unchanged after mismatch, got %q", spec)
	}
	if _, statErr := os.Stat(filepath.Join(appDir, ".ai-logfixer-backups")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected no rollback data for rejected policy, got stat err %v", statErr)
	}
}

func TestRollbackRestoresBrokenSpecWhenVerificationFails(t *testing.T) {
	t.Parallel()

	appDir := t.TempDir()
	packagePath := filepath.Join(appDir, "package.json")
	currentSpec := "file:../packages/widget-v2.0.0"
	knownGoodSpec := "file:../packages/widget-v1.2.2"
	writePackageJSON(t, packagePath, map[string]any{
		"name": "checkout-app",
		"dependencies": map[string]any{
			"@acme/widget": currentSpec,
		},
	})

	result, err := Rollback(context.Background(), Options{
		PackageFile:   packagePath,
		PackageName:   "@acme/widget",
		CurrentSpec:   currentSpec,
		KnownGoodSpec: knownGoodSpec,
		VerifyCommand: "exit 9",
		WorkingDir:    appDir,
		Now:           time.Date(2026, 6, 8, 10, 32, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatal("expected verification failure")
	}
	if !strings.Contains(err.Error(), "verify package rollback") {
		t.Fatalf("expected verify error, got %v", err)
	}
	if !result.Applied || !result.RolledBack {
		t.Fatalf("expected failed verification to roll back package file, got %+v", result)
	}
	if result.Verification.Status != VerificationFailed || result.Verification.ExitCode != 9 {
		t.Fatalf("expected failed verification details, got %+v", result.Verification)
	}
	if result.Rollback.ManifestPath == "" || result.Rollback.BackupPath == "" {
		t.Fatalf("expected rollback data even when verification fails, got %+v", result.Rollback)
	}
	spec := readPackageSpec(t, packagePath, "dependencies", "@acme/widget")
	if spec != currentSpec {
		t.Fatalf("expected package file restored after failed verification, got %q", spec)
	}
}

func writePackageJSON(t *testing.T, path string, document map[string]any) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create package dir: %v", err)
	}
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatalf("marshal package json: %v", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatalf("write package json: %v", err)
	}
}

func readPackageSpec(t *testing.T, path string, section string, name string) string {
	t.Helper()

	spec, err := packageSpec(path, section, name)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func packageSpec(path string, section string, name string) (string, error) {
	var document map[string]any
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return "", err
	}
	dependencies, ok := document[section].(map[string]any)
	if !ok {
		return "", errors.New("package file missing " + section + " section")
	}
	spec, ok := dependencies[name].(string)
	if !ok {
		return "", errors.New("package file missing " + name + " in " + section)
	}
	return spec, nil
}

func decodeJSONFile(t *testing.T, path string, target any) {
	t.Helper()

	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}
