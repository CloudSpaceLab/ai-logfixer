package tokens_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CloudSpaceLab/ai-logfixer/internal/runtime/tokens"
)

func TestDiagnoseDetectsMissingExpiredInvalidToken(t *testing.T) {
	t.Parallel()

	result, err := tokens.Diagnose(tokens.Options{
		ServiceName:  "orders-api",
		Provider:     "stripe",
		TokenName:    "STRIPE_API_KEY",
		TokenPresent: false,
		TokenValue:   "sk_test_should_not_leak",
		ExpiresAt:    fixedTime().Add(-time.Hour),
		Probe: tokens.Probe{
			Checked:    true,
			StatusCode: http.StatusUnauthorized,
			Body:       "invalid bearer sk_test_should_not_leak",
		},
		Now: fixedTime(),
	})
	if err != nil {
		t.Fatalf("diagnose token: %v", err)
	}

	if result.Status != tokens.StatusDrift {
		t.Fatalf("expected token drift, got %q", result.Status)
	}
	assertFinding(t, result.Findings, "missing_token")
	assertFinding(t, result.Findings, "expired_token")
	assertFinding(t, result.Findings, "invalid_token")
	if result.TokenEvidence.Fingerprint == "" {
		t.Fatal("expected token fingerprint")
	}
	if strings.Contains(result.TokenEvidence.Fingerprint, "sk_test") {
		t.Fatalf("fingerprint leaked token: %s", result.TokenEvidence.Fingerprint)
	}
	for _, finding := range result.Findings {
		if strings.Contains(finding.Evidence, "sk_test_should_not_leak") {
			t.Fatalf("finding leaked token value: %+v", finding)
		}
	}
	if result.AutoMutation.Allowed {
		t.Fatal("token diagnostics must not auto-mutate secrets")
	}
}

func TestDiagnoseDetectsScopeDrift(t *testing.T) {
	t.Parallel()

	result, err := tokens.Diagnose(tokens.Options{
		ServiceName:    "orders-api",
		Provider:       "github",
		TokenName:      "GITHUB_TOKEN",
		TokenPresent:   true,
		RequiredScopes: []string{"repo:read", "issues:write"},
		ObservedScopes: []string{"repo:read"},
		Probe:          tokens.Probe{Checked: true, StatusCode: http.StatusForbidden, Body: "missing scope"},
		Now:            fixedTime(),
	})
	if err != nil {
		t.Fatalf("diagnose token scope: %v", err)
	}

	assertFinding(t, result.Findings, "insufficient_token_scope")
	assertFinding(t, result.Findings, "missing_token_scope")
	if !hasRecommendation(result.Recommendations, "update_token_scope") {
		t.Fatalf("expected update_token_scope recommendation, got %+v", result.Recommendations)
	}
}

func TestDiagnoseReportsHealthyTokenEvidence(t *testing.T) {
	t.Parallel()

	result, err := tokens.Diagnose(tokens.Options{
		ServiceName:    "orders-api",
		Provider:       "github",
		TokenName:      "GITHUB_TOKEN",
		TokenPresent:   true,
		TokenValue:     "ghp_example",
		RequiredScopes: []string{"issues:write"},
		ObservedScopes: []string{"issues:write", "repo:read"},
		ExpiresAt:      fixedTime().Add(time.Hour),
		Probe:          tokens.Probe{Checked: true, StatusCode: http.StatusOK},
		Now:            fixedTime(),
	})
	if err != nil {
		t.Fatalf("diagnose healthy token: %v", err)
	}

	if result.Status != tokens.StatusHealthy {
		t.Fatalf("expected healthy token status, got %q", result.Status)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("expected no findings, got %+v", result.Findings)
	}
	if !hasRecommendation(result.Recommendations, "no_token_change") {
		t.Fatalf("expected no_token_change recommendation, got %+v", result.Recommendations)
	}
}

func TestDiagnoseRequiresServiceAndTokenName(t *testing.T) {
	t.Parallel()

	if _, err := tokens.Diagnose(tokens.Options{TokenName: "API_KEY"}); err == nil {
		t.Fatal("expected service name validation error")
	}
	if _, err := tokens.Diagnose(tokens.Options{ServiceName: "orders-api"}); err == nil {
		t.Fatal("expected token name validation error")
	}
}

func assertFinding(t *testing.T, findings []tokens.Finding, kind string) {
	t.Helper()
	for _, finding := range findings {
		if finding.Kind == kind {
			return
		}
	}
	t.Fatalf("expected finding %q in %+v", kind, findings)
}

func hasRecommendation(recommendations []tokens.Recommendation, action string) bool {
	for _, recommendation := range recommendations {
		if recommendation.Action == action {
			return true
		}
	}
	return false
}

func fixedTime() time.Time {
	return time.Date(2026, 6, 8, 12, 30, 0, 0, time.UTC)
}
