package resources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnsureCreatesAllowlistedDirectoryAndRecordsManifest(t *testing.T) {
	t.Parallel()

	appRoot := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	result, err := Ensure(context.Background(), Options{
		AppRoot:          appRoot,
		ResourcePath:     "storage/framework/cache/data",
		Allowlist:        []string{"storage/framework/cache/data"},
		Kind:             KindDirectory,
		Mode:             0o750,
		VerifyURL:        server.URL,
		ExpectedStatus:   http.StatusOK,
		Now:              time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC),
		ManifestBaseName: "resource-test",
	})
	if err != nil {
		t.Fatalf("ensure resource directory: %v", err)
	}

	if !result.Applied || !result.Verified || result.Rollback.RolledBack {
		t.Fatalf("unexpected result state: %+v", result)
	}
	if result.Before.Exists {
		t.Fatalf("expected resource to be missing before creation: %+v", result.Before)
	}
	if !result.After.Exists || result.After.Kind != KindDirectory {
		t.Fatalf("expected directory after creation: %+v", result.After)
	}
	if result.After.Mode != "0750" {
		t.Fatalf("expected recorded mode 0750, got %s", result.After.Mode)
	}
	if result.ManifestPath == "" {
		t.Fatal("expected rollback manifest path")
	}
	if _, err := os.Stat(result.ManifestPath); err != nil {
		t.Fatalf("expected rollback manifest to exist before/after creation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(appRoot, "storage", "framework", "cache", "data")); err != nil {
		t.Fatalf("expected allowlisted directory to exist: %v", err)
	}
	if len(result.CreatedPaths) == 0 || result.CreatedPaths[len(result.CreatedPaths)-1] != "storage/framework/cache/data" {
		t.Fatalf("expected created path evidence to include target, got %+v", result.CreatedPaths)
	}
}

func TestEnsureCreatesAllowlistedPlaceholderFileAndRollsBackOnVerificationFailure(t *testing.T) {
	t.Parallel()

	appRoot := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "still missing", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	result, err := Ensure(context.Background(), Options{
		AppRoot:          appRoot,
		ResourcePath:     "bootstrap/cache/packages.php",
		Allowlist:        []string{"bootstrap/cache/packages.php"},
		Kind:             KindFile,
		Mode:             0o640,
		ContentStrategy:  ContentEmpty,
		VerifyURL:        server.URL,
		ExpectedStatus:   http.StatusOK,
		Now:              time.Date(2026, 6, 8, 9, 1, 0, 0, time.UTC),
		ManifestBaseName: "resource-test",
	})
	if err == nil {
		t.Fatal("expected verification failure")
	}
	if !strings.Contains(err.Error(), "verify resource") {
		t.Fatalf("expected verification error, got %v", err)
	}
	if !result.Applied || result.Verified {
		t.Fatalf("expected applied but unverified result, got %+v", result)
	}
	if !result.Rollback.RolledBack {
		t.Fatalf("expected rollback evidence, got %+v", result.Rollback)
	}
	if result.ManifestPath == "" {
		t.Fatal("expected rollback manifest path")
	}
	if _, err := os.Stat(result.ManifestPath); err != nil {
		t.Fatalf("expected rollback manifest to remain as evidence: %v", err)
	}
	if _, err := os.Stat(filepath.Join(appRoot, "bootstrap", "cache", "packages.php")); !os.IsNotExist(err) {
		t.Fatalf("expected placeholder file to be removed after rollback, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(appRoot, "bootstrap")); !os.IsNotExist(err) {
		t.Fatalf("expected created parent directories to be removed after rollback, err=%v", err)
	}
}

func TestEnsureBlocksUnsafeAndUnallowlistedPaths(t *testing.T) {
	t.Parallel()

	t.Run("path traversal", func(t *testing.T) {
		t.Parallel()

		appRoot := t.TempDir()
		_, err := Ensure(context.Background(), Options{
			AppRoot:      appRoot,
			ResourcePath: "storage/../secrets",
			Allowlist:    []string{"secrets"},
			Kind:         KindDirectory,
		})
		if err == nil || !strings.Contains(err.Error(), "path traversal") {
			t.Fatalf("expected traversal block, got %v", err)
		}
	})

	t.Run("absolute outside root", func(t *testing.T) {
		t.Parallel()

		appRoot := t.TempDir()
		outsideRoot := t.TempDir()
		_, err := Ensure(context.Background(), Options{
			AppRoot:      appRoot,
			ResourcePath: filepath.Join(outsideRoot, "cache"),
			Allowlist:    []string{"cache"},
			Kind:         KindDirectory,
		})
		if err == nil || !strings.Contains(err.Error(), "outside app root") {
			t.Fatalf("expected outside-root block, got %v", err)
		}
	})

	t.Run("unallowlisted", func(t *testing.T) {
		t.Parallel()

		appRoot := t.TempDir()
		_, err := Ensure(context.Background(), Options{
			AppRoot:      appRoot,
			ResourcePath: "storage/logs",
			Allowlist:    []string{"storage/framework/cache"},
			Kind:         KindDirectory,
		})
		if err == nil || !strings.Contains(err.Error(), "not allowlisted") {
			t.Fatalf("expected unallowlisted block, got %v", err)
		}
	})

	t.Run("symlink escape", func(t *testing.T) {
		t.Parallel()

		appRoot := t.TempDir()
		outsideRoot := t.TempDir()
		if err := os.Symlink(outsideRoot, filepath.Join(appRoot, "storage")); err != nil {
			t.Fatalf("create symlink: %v", err)
		}
		_, err := Ensure(context.Background(), Options{
			AppRoot:      appRoot,
			ResourcePath: "storage/cache",
			Allowlist:    []string{"storage/cache"},
			Kind:         KindDirectory,
		})
		if err == nil || !strings.Contains(err.Error(), "symlink escape") {
			t.Fatalf("expected symlink escape block, got %v", err)
		}
	})
}
