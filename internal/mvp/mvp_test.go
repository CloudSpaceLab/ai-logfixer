package mvp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/demoapp"
	"github.com/CloudSpaceLab/ai-logfixer/internal/mvp"
)

func TestMVPDetectsRepeated503AndAppliesConfigFix(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	configPath := filepath.Join(workDir, "app.json")
	logPath := filepath.Join(workDir, "app.log")

	if err := demoapp.WriteConfig(configPath, demoapp.Config{
		ServiceName: "goravel-demo",
		UpstreamURL: "http://127.0.0.1:1/orders",
	}); err != nil {
		t.Fatalf("write broken demo config: %v", err)
	}

	server := httptest.NewServer(demoapp.NewHandler(configPath, logPath))
	t.Cleanup(server.Close)

	for i := 0; i < 5; i++ {
		response, err := http.Get(server.URL + "/orders")
		if err != nil {
			t.Fatalf("request broken orders endpoint: %v", err)
		}
		if response.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("expected broken orders endpoint to return 503, got %d", response.StatusCode)
		}
		_ = response.Body.Close()
	}

	result, err := mvp.Run(context.Background(), mvp.Options{
		ServiceName:     "goravel-demo",
		BaseURL:         server.URL,
		LogPath:         logPath,
		ConfigPath:      configPath,
		HealthyUpstream: server.URL + "/upstream/orders",
		ErrorThreshold:  3,
		Now:             time.Date(2026, 5, 22, 9, 57, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("run mvp process: %v", err)
	}

	if result.InvestigationRequest.SourceType != contractsv1.SourceTypeAutomatic {
		t.Fatalf("expected automatic investigation, got %q", result.InvestigationRequest.SourceType)
	}
	if result.Diagnosis.Status != contractsv1.DiagnosisStatusComplete {
		t.Fatalf("expected complete diagnosis, got %q", result.Diagnosis.Status)
	}
	if result.RemediationPlan.Status != contractsv1.RemediationStatusApproved {
		t.Fatalf("expected approved remediation plan, got %q", result.RemediationPlan.Status)
	}
	if result.Attempt.Status != contractsv1.RemediationStatusSucceeded {
		t.Fatalf("expected succeeded remediation attempt, got %q", result.Attempt.Status)
	}
	if result.Receipt.Outcome != "succeeded" {
		t.Fatalf("expected succeeded receipt, got %q", result.Receipt.Outcome)
	}
	if result.BackupPath == "" {
		t.Fatal("expected a backup path before patching config")
	}

	fixedConfig, err := demoapp.ReadConfig(configPath)
	if err != nil {
		t.Fatalf("read fixed demo config: %v", err)
	}
	if fixedConfig.UpstreamURL != server.URL+"/upstream/orders" {
		t.Fatalf("expected upstream to be fixed, got %q", fixedConfig.UpstreamURL)
	}

	response, err := http.Get(server.URL + "/orders")
	if err != nil {
		t.Fatalf("request fixed orders endpoint: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected fixed orders endpoint to return 200, got %d", response.StatusCode)
	}
}
