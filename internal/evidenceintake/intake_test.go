package evidenceintake

import (
	"strings"
	"testing"
	"time"
)

func TestBuildBundleNormalizesIncidentEvidence(t *testing.T) {
	capturedAt := time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)

	bundle, err := BuildBundle(Request{
		AppRoot:    "/srv/checkout",
		Source:     "runtime-v2",
		CapturedAt: capturedAt,
		Process: ProcessMetadata{
			PID:         4217,
			Command:     "php artisan serve",
			Executable:  "/usr/bin/php",
			CWD:         "/srv/checkout",
			User:        "www-data",
			Environment: "staging",
		},
		Logs: []LogSample{
			{
				Source:    "storage/logs/laravel.log",
				Timestamp: capturedAt.Add(-time.Second),
				Lines: []string{
					"level=error route=/orders status=500 password=hunter2 token=abc123",
				},
			},
		},
		Probes: []ProbeResult{
			{
				Name:       "orders endpoint",
				Target:     "http://127.0.0.1/orders",
				Status:     "failed",
				StatusCode: 500,
				Latency:    120 * time.Millisecond,
				Output:     "Authorization: Bearer deadbeef",
			},
		},
		ConfigSnapshots: []ConfigSnapshot{
			{
				Path:    ".env",
				Format:  "dotenv",
				Content: "DB_PASSWORD=secret\nAPP_KEY=base64:abc123",
			},
		},
		PackageManifests: []PackageManifest{
			{
				Path:    "package.json",
				Manager: "npm",
				Packages: []PackageDependency{
					{Name: "next", Version: "16.0.0"},
					{Name: "react", Version: "19.0.0"},
				},
			},
		},
		Permissions: []PermissionState{
			{
				Path:     "storage/logs",
				Owner:    "root",
				Group:    "root",
				Mode:     "0555",
				Readable: true,
				Writable: false,
			},
		},
		Dependencies: []DependencyState{
			{
				Name:   "postgres",
				Kind:   "database",
				Status: "unreachable",
				Detail: "dial tcp password=dbsecret",
			},
		},
		Services: []ServiceState{
			{Name: "nginx", Status: "running", Detail: "active"},
			{Name: "php-fpm", Status: "failed", Detail: "exit status 1"},
		},
	})
	if err != nil {
		t.Fatalf("build bundle: %v", err)
	}
	if err := bundle.Validate(); err != nil {
		t.Fatalf("expected bundle to validate, got %v", err)
	}

	if bundle.SchemaVersion != SchemaVersion {
		t.Fatalf("schema version = %q, want %q", bundle.SchemaVersion, SchemaVersion)
	}
	if bundle.AppRoot != "/srv/checkout" {
		t.Fatalf("app root = %q", bundle.AppRoot)
	}
	if bundle.Summary.TotalItems != len(bundle.Items) {
		t.Fatalf("summary total items = %d, want %d", bundle.Summary.TotalItems, len(bundle.Items))
	}

	wantCounts := map[Kind]int{
		KindProcess:         1,
		KindLog:             1,
		KindProbe:           1,
		KindConfigSnapshot:  1,
		KindPackageManifest: 1,
		KindPermission:      1,
		KindDependency:      1,
		KindService:         2,
	}
	for kind, want := range wantCounts {
		if got := bundle.Summary.CountsByKind[kind]; got != want {
			t.Fatalf("count for %s = %d, want %d", kind, got, want)
		}
	}

	logItem := itemByKind(t, bundle, KindLog)
	if logItem.Source != "/srv/checkout/storage/logs/laravel.log" {
		t.Fatalf("log source = %q", logItem.Source)
	}
	if logItem.RedactionState != RedactionRedacted {
		t.Fatalf("log redaction state = %q", logItem.RedactionState)
	}
	assertNotContains(t, logItem.RawExcerpt, "hunter2", "abc123")
	assertContains(t, logItem.RawExcerpt, "route=/orders")

	configItem := itemByKind(t, bundle, KindConfigSnapshot)
	assertNotContains(t, configItem.RawExcerpt, "secret", "base64:abc123")
	assertContains(t, configItem.RawExcerpt, "DB_PASSWORD=<redacted>")

	assertStringsEqual(t, bundle.Summary.FailingProbes, []string{"orders endpoint"})
	assertStringsEqual(t, bundle.Summary.UnhealthyDependencies, []string{"postgres"})
	assertStringsEqual(t, bundle.Summary.UnhealthyServices, []string{"php-fpm"})
	assertStringsEqual(t, bundle.Summary.UnwritablePaths, []string{"/srv/checkout/storage/logs"})
	if bundle.Summary.RedactedItems < 3 {
		t.Fatalf("redacted items = %d, want at least 3", bundle.Summary.RedactedItems)
	}
}

func TestBuildBundleRejectsMissingRequiredEvidenceFields(t *testing.T) {
	_, err := BuildBundle(Request{
		CapturedAt: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC),
		Logs:       []LogSample{{Lines: []string{"boom"}}},
	})
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	assertContains(t, err.Error(), "app_root is required", "logs[0].source is required")
}

func TestBuildBundleRejectsEmptyEvidenceSet(t *testing.T) {
	_, err := BuildBundle(Request{
		AppRoot:    "/srv/checkout",
		CapturedAt: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	assertContains(t, err.Error(), "at least one evidence source is required")
}

func TestRedactSecretsRetainsOperationalContext(t *testing.T) {
	got := Redact("DB_PASSWORD=swordfish api_key=abc123 Authorization: Bearer deadbeef route=/orders")

	assertNotContains(t, got, "swordfish", "abc123", "deadbeef")
	assertContains(t, got, "DB_PASSWORD=<redacted>", "api_key=<redacted>", "Bearer <redacted>", "route=/orders")
}

func itemByKind(t *testing.T, bundle Bundle, kind Kind) Item {
	t.Helper()

	for _, item := range bundle.Items {
		if item.Kind == kind {
			return item
		}
	}
	t.Fatalf("missing item kind %s", kind)
	return Item{}
}

func assertContains(t *testing.T, value string, want ...string) {
	t.Helper()

	for _, part := range want {
		if !strings.Contains(value, part) {
			t.Fatalf("expected %q to contain %q", value, part)
		}
	}
}

func assertNotContains(t *testing.T, value string, forbidden ...string) {
	t.Helper()

	for _, part := range forbidden {
		if strings.Contains(value, part) {
			t.Fatalf("expected %q not to contain %q", value, part)
		}
	}
}

func assertStringsEqual(t *testing.T, got []string, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
