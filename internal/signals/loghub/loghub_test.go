package loghub

import (
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
)

func TestAnalyzeApacheExcerptEscalatesWithoutPatch(t *testing.T) {
	t.Parallel()

	content := strings.Join([]string{
		`[Fri Dec 16 01:46:23 2005] [error] [client 10.0.0.1] File does not exist: /var/www/html/favicon.ico`,
		`[Fri Dec 16 01:46:24 2005] [error] [client 10.0.0.2] File does not exist: /var/www/html/favicon.ico`,
		`[Fri Dec 16 01:46:25 2005] [error] [client 10.0.0.3] File does not exist: /var/www/html/favicon.ico`,
		`[Fri Dec 16 01:46:26 2005] [notice] workerEnv.init() ok`,
	}, "\n")

	analysis, err := Analyze(content, Options{
		ServiceName: "apache-edge",
		LogPath:     "testdata/loghub/apache_error_excerpt.log",
		Format:      FormatApache,
		Threshold:   3,
		Now:         time.Date(2026, 5, 26, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("analyze Apache excerpt: %v", err)
	}
	if analysis.Signal.Count != 3 {
		t.Fatalf("expected 3 grouped events, got %+v", analysis.Signal)
	}
	if analysis.RemediationPlan.Status != contractsv1.RemediationStatusEscalated || analysis.RemediationPlan.RiskLevel != contractsv1.SafetyBlocked {
		t.Fatalf("expected blocked/escalated remediation, got %+v", analysis.RemediationPlan)
	}
	if analysis.Attempt.Status != contractsv1.RemediationStatusEscalated || analysis.Receipt.Outcome != "escalated" {
		t.Fatalf("expected escalated attempt and receipt, got attempt=%s receipt=%s", analysis.Attempt.Status, analysis.Receipt.Outcome)
	}
}

func TestAnalyzeOpenStackExcerptEscalatesWithoutPatch(t *testing.T) {
	t.Parallel()

	content := strings.Join([]string{
		`2017-05-16 09:00:00.000 1234 ERROR nova.api.openstack HTTP exception thrown: No instances found for any event`,
		`2017-05-16 09:00:01.000 1235 ERROR nova.api.openstack HTTP exception thrown: No instances found for any event`,
		`2017-05-16 09:00:02.000 1236 ERROR nova.api.openstack HTTP exception thrown: No instances found for any event`,
		`2017-05-16 09:00:03.000 1237 INFO nova.api.openstack request completed`,
	}, "\n")

	analysis, err := Analyze(content, Options{
		ServiceName: "openstack-control-plane",
		LogPath:     "testdata/loghub/openstack_abnormal_excerpt.log",
		Format:      FormatOpenStack,
		Threshold:   3,
		Now:         time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("analyze OpenStack excerpt: %v", err)
	}
	if !strings.Contains(analysis.Signal.Signature, "HTTP exception thrown") {
		t.Fatalf("expected HTTP exception signature, got %q", analysis.Signal.Signature)
	}
	if analysis.Diagnosis.SafetyClassification != contractsv1.SafetyBlocked {
		t.Fatalf("expected blocked diagnosis, got %+v", analysis.Diagnosis)
	}
	if analysis.RemediationPlan.Status != contractsv1.RemediationStatusEscalated {
		t.Fatalf("expected escalated remediation plan, got %q", analysis.RemediationPlan.Status)
	}
}

func TestAnalyzeBelowThresholdReturnsError(t *testing.T) {
	t.Parallel()

	content := strings.Join([]string{
		`[Fri Dec 16 01:46:23 2005] [error] [client 10.0.0.1] File does not exist: /var/www/html/favicon.ico`,
		`[Fri Dec 16 01:46:24 2005] [error] [client 10.0.0.2] File does not exist: /var/www/html/favicon.ico`,
	}, "\n")

	_, err := Analyze(content, Options{
		ServiceName: "apache-edge",
		LogPath:     "testdata/loghub/apache_error_excerpt.log",
		Format:      FormatApache,
		Threshold:   3,
		Now:         time.Date(2026, 5, 26, 11, 0, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatal("expected threshold error")
	}
}
