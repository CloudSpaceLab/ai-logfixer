package tokens

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

type Status string

const (
	StatusHealthy Status = "healthy"
	StatusDrift   Status = "token_drift_detected"
	StatusBlocked Status = "blocked"
)

type Options struct {
	ServiceName       string
	Provider          string
	TokenName         string
	TokenPresent      bool
	TokenValue        string
	Probe             Probe
	RequiredScopes    []string
	ObservedScopes    []string
	ExpiresAt         time.Time
	Now               time.Time
	AllowAutoMutation bool
}

type Probe struct {
	Checked    bool   `json:"checked"`
	StatusCode int    `json:"status_code,omitempty"`
	Error      string `json:"error,omitempty"`
	Body       string `json:"body,omitempty"`
}

type Result struct {
	ServiceName     string           `json:"service_name"`
	Provider        string           `json:"provider"`
	TokenName       string           `json:"token_name"`
	Status          Status           `json:"status"`
	TokenEvidence   TokenEvidence    `json:"token_evidence"`
	Findings        []Finding        `json:"findings"`
	Recommendations []Recommendation `json:"recommendations"`
	AutoMutation    AutoMutation     `json:"auto_mutation"`
	CheckedAt       time.Time        `json:"checked_at"`
}

type TokenEvidence struct {
	Present     bool      `json:"present"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
}

type Finding struct {
	Kind     string `json:"kind"`
	Message  string `json:"message"`
	Evidence string `json:"evidence,omitempty"`
}

type Recommendation struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
	Safe   bool   `json:"safe"`
}

type AutoMutation struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
}

func Diagnose(options Options) (Result, error) {
	options = normalizeOptions(options)
	if options.ServiceName == "" {
		return Result{}, errors.New("service name is required")
	}
	if options.TokenName == "" {
		return Result{}, errors.New("token name is required")
	}

	result := Result{
		ServiceName: options.ServiceName,
		Provider:    options.Provider,
		TokenName:   options.TokenName,
		CheckedAt:   options.Now,
		TokenEvidence: TokenEvidence{
			Present:     options.TokenPresent,
			Fingerprint: tokenFingerprint(options.TokenValue),
			ExpiresAt:   options.ExpiresAt,
		},
		AutoMutation: AutoMutation{
			Allowed: false,
			Reason:  "tokens and API keys must be rotated through an approved secret provider",
		},
	}
	if options.AllowAutoMutation {
		result.AutoMutation.Reason = "automatic token mutation remains disabled because AI LogFixer was not given an approved replacement secret"
	}

	result.Findings = append(result.Findings, inspectPresence(options)...)
	result.Findings = append(result.Findings, inspectExpiry(options)...)
	result.Findings = append(result.Findings, inspectProbe(options.Probe)...)
	result.Findings = append(result.Findings, inspectScopes(options.RequiredScopes, options.ObservedScopes)...)
	result.Status = statusForFindings(result.Findings)
	result.Recommendations = recommendations(result.Findings)
	return result, nil
}

func normalizeOptions(options Options) Options {
	options.ServiceName = strings.TrimSpace(options.ServiceName)
	options.Provider = strings.TrimSpace(options.Provider)
	if options.Provider == "" {
		options.Provider = "unknown-provider"
	}
	options.TokenName = strings.TrimSpace(options.TokenName)
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}
	options.RequiredScopes = normalizeScopes(options.RequiredScopes)
	options.ObservedScopes = normalizeScopes(options.ObservedScopes)
	return options
}

func inspectPresence(options Options) []Finding {
	if options.TokenPresent {
		return nil
	}
	return []Finding{{
		Kind:    "missing_token",
		Message: fmt.Sprintf("%s is missing for %s", options.TokenName, options.Provider),
	}}
}

func inspectExpiry(options Options) []Finding {
	if options.ExpiresAt.IsZero() || options.ExpiresAt.After(options.Now) {
		return nil
	}
	return []Finding{{
		Kind:     "expired_token",
		Message:  fmt.Sprintf("%s expired at %s", options.TokenName, options.ExpiresAt.UTC().Format(time.RFC3339)),
		Evidence: "expiration metadata",
	}}
}

func inspectProbe(probe Probe) []Finding {
	if !probe.Checked {
		return nil
	}
	if probe.StatusCode == http.StatusUnauthorized {
		return []Finding{{Kind: "invalid_token", Message: "provider rejected the token with HTTP 401", Evidence: redactProbeEvidence(probe)}}
	}
	if probe.StatusCode == http.StatusForbidden {
		return []Finding{{Kind: "insufficient_token_scope", Message: "provider rejected the token with HTTP 403", Evidence: redactProbeEvidence(probe)}}
	}
	if probe.StatusCode >= 400 {
		return []Finding{{Kind: "token_probe_failed", Message: fmt.Sprintf("provider token probe returned HTTP %d", probe.StatusCode), Evidence: redactProbeEvidence(probe)}}
	}
	if probe.Error != "" {
		return []Finding{{Kind: "token_probe_failed", Message: "provider token probe failed", Evidence: redactProbeEvidence(probe)}}
	}
	return nil
}

func inspectScopes(required []string, observed []string) []Finding {
	if len(required) == 0 {
		return nil
	}
	var findings []Finding
	for _, scope := range required {
		if !slices.Contains(observed, scope) {
			findings = append(findings, Finding{
				Kind:     "missing_token_scope",
				Message:  fmt.Sprintf("token is missing required scope %q", scope),
				Evidence: "scope metadata",
			})
		}
	}
	return findings
}

func recommendations(findings []Finding) []Recommendation {
	if len(findings) == 0 {
		return []Recommendation{{Action: "no_token_change", Reason: "token evidence and provider probe were healthy", Safe: true}}
	}
	seen := map[string]bool{}
	var recommendations []Recommendation
	for _, finding := range findings {
		action := actionForFinding(finding.Kind)
		if seen[action] {
			continue
		}
		seen[action] = true
		recommendations = append(recommendations, Recommendation{
			Action: action,
			Reason: finding.Message,
			Safe:   false,
		})
	}
	return recommendations
}

func actionForFinding(kind string) string {
	switch kind {
	case "missing_token", "invalid_token", "expired_token":
		return "rotate_or_restore_secret"
	case "insufficient_token_scope", "missing_token_scope":
		return "update_token_scope"
	default:
		return "review_provider_token_health"
	}
}

func statusForFindings(findings []Finding) Status {
	if len(findings) == 0 {
		return StatusHealthy
	}
	return StatusDrift
}

func normalizeScopes(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			normalized = append(normalized, value)
		}
	}
	slices.Sort(normalized)
	return slices.Compact(normalized)
}

func tokenFingerprint(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:8])
}

func redactProbeEvidence(probe Probe) string {
	parts := []string{}
	if probe.StatusCode > 0 {
		parts = append(parts, fmt.Sprintf("status=%d", probe.StatusCode))
	}
	if probe.Error != "" {
		parts = append(parts, "error="+redactSecretish(probe.Error))
	}
	if probe.Body != "" {
		parts = append(parts, "body="+redactSecretish(probe.Body))
	}
	return strings.Join(parts, " ")
}

func redactSecretish(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	fields := strings.Fields(value)
	for index, field := range fields {
		trimmed := strings.Trim(field, `"'`)
		lower := strings.ToLower(trimmed)
		previous := ""
		if index > 0 {
			previous = strings.ToLower(strings.Trim(fields[index-1], `"'`))
		}
		if previous == "bearer" ||
			strings.Contains(lower, "sk_") ||
			strings.Contains(lower, "ghp_") ||
			strings.Contains(lower, "token=") ||
			strings.Contains(lower, "api_key=") ||
			strings.Contains(lower, "apikey=") ||
			strings.Contains(lower, "secret=") ||
			strings.Contains(lower, "password=") {
			fields[index] = "<redacted>"
		}
	}
	redacted := strings.Join(fields, " ")
	if len(redacted) > 240 {
		redacted = redacted[:240] + "..."
	}
	return redacted
}
