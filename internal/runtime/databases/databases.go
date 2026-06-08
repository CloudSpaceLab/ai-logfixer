package databases

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
)

type Status string

const (
	StatusHealthy Status = "healthy"
	StatusDrift   Status = "drift_detected"
	StatusBlocked Status = "blocked"
)

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

type Options struct {
	ServiceName       string
	DatabaseURL       string
	AllowedSchemes    []string
	AllowedHosts      []string
	RequiredDatabase  string
	RequiredTables    []TableExpectation
	ObservedTables    []TableState
	ConnectionProbe   ProbeResult
	AllowAutoMutation bool
	Now               time.Time
}

type TableExpectation struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
}

type TableState struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
}

type ProbeResult struct {
	Checked bool   `json:"checked"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
}

type Result struct {
	ServiceName     string           `json:"service_name"`
	Status          Status           `json:"status"`
	DatabaseURL     URLSummary       `json:"database_url"`
	Findings        []Finding        `json:"findings"`
	Recommendations []Recommendation `json:"recommendations"`
	AutoMutation    AutoMutation     `json:"auto_mutation"`
	CheckedAt       time.Time        `json:"checked_at"`
}

type URLSummary struct {
	Present  bool   `json:"present"`
	Scheme   string `json:"scheme,omitempty"`
	Host     string `json:"host,omitempty"`
	Database string `json:"database,omitempty"`
	Redacted string `json:"redacted,omitempty"`
}

type Finding struct {
	Kind     string   `json:"kind"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	Evidence string   `json:"evidence,omitempty"`
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

	result := Result{
		ServiceName: options.ServiceName,
		Status:      StatusHealthy,
		CheckedAt:   options.Now,
		AutoMutation: AutoMutation{
			Allowed: false,
			Reason:  "database credentials, URLs, and schema changes require an explicit approved provider or migration path",
		},
	}
	if options.AllowAutoMutation {
		result.AutoMutation.Reason = "automatic mutation remains disabled until a candidate URL or migration plan is explicitly supplied"
	}

	urlSummary, findings := inspectURL(options)
	result.DatabaseURL = urlSummary
	result.Findings = append(result.Findings, findings...)
	result.Findings = append(result.Findings, inspectProbe(options.ConnectionProbe)...)
	result.Findings = append(result.Findings, inspectSchema(options.RequiredTables, options.ObservedTables)...)
	result.Recommendations = recommendations(result.Findings)
	result.Status = statusForFindings(result.Findings)
	return result, nil
}

func normalizeOptions(options Options) Options {
	options.ServiceName = strings.TrimSpace(options.ServiceName)
	options.DatabaseURL = strings.TrimSpace(options.DatabaseURL)
	options.AllowedSchemes = normalizeList(options.AllowedSchemes)
	options.AllowedHosts = normalizeList(options.AllowedHosts)
	options.RequiredDatabase = strings.Trim(strings.TrimSpace(options.RequiredDatabase), "/")
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}
	return options
}

func inspectURL(options Options) (URLSummary, []Finding) {
	if options.DatabaseURL == "" {
		return URLSummary{}, []Finding{{
			Kind:     "missing_database_url",
			Severity: SeverityCritical,
			Message:  "database URL is missing",
		}}
	}

	parsed, err := url.Parse(options.DatabaseURL)
	if err != nil || parsed.Scheme == "" {
		return URLSummary{Present: true, Redacted: redactURL(options.DatabaseURL)}, []Finding{{
			Kind:     "malformed_database_url",
			Severity: SeverityCritical,
			Message:  "database URL cannot be parsed",
			Evidence: errString(err),
		}}
	}

	summary := URLSummary{
		Present:  true,
		Scheme:   strings.ToLower(parsed.Scheme),
		Host:     strings.ToLower(parsed.Hostname()),
		Database: strings.Trim(path.Clean(parsed.Path), "/"),
		Redacted: redactParsedURL(parsed),
	}
	var findings []Finding
	if len(options.AllowedSchemes) > 0 && !slices.Contains(options.AllowedSchemes, summary.Scheme) {
		findings = append(findings, Finding{
			Kind:     "database_scheme_drift",
			Severity: SeverityCritical,
			Message:  fmt.Sprintf("database scheme %q is not allowlisted", summary.Scheme),
		})
	}
	if len(options.AllowedHosts) > 0 && !hostAllowed(summary.Host, options.AllowedHosts) {
		findings = append(findings, Finding{
			Kind:     "database_host_drift",
			Severity: SeverityCritical,
			Message:  fmt.Sprintf("database host %q is not allowlisted", summary.Host),
		})
	}
	if options.RequiredDatabase != "" && summary.Database != options.RequiredDatabase {
		findings = append(findings, Finding{
			Kind:     "database_name_drift",
			Severity: SeverityCritical,
			Message:  fmt.Sprintf("database name %q does not match required database %q", summary.Database, options.RequiredDatabase),
		})
	}
	if summary.Database == "" && summary.Scheme != "sqlite" && summary.Scheme != "sqlite3" {
		findings = append(findings, Finding{
			Kind:     "missing_database_name",
			Severity: SeverityWarning,
			Message:  "database URL does not name a database/schema",
		})
	}
	return summary, findings
}

func inspectProbe(probe ProbeResult) []Finding {
	if !probe.Checked || probe.OK {
		return nil
	}
	return []Finding{{
		Kind:     "database_connection_failed",
		Severity: SeverityCritical,
		Message:  "database connection probe failed",
		Evidence: probe.Error,
	}}
}

func inspectSchema(required []TableExpectation, observed []TableState) []Finding {
	observedByName := map[string]TableState{}
	for _, table := range observed {
		observedByName[strings.ToLower(strings.TrimSpace(table.Name))] = normalizeTableState(table)
	}

	var findings []Finding
	for _, expectation := range required {
		name := strings.ToLower(strings.TrimSpace(expectation.Name))
		if name == "" {
			continue
		}
		table, ok := observedByName[name]
		if !ok {
			findings = append(findings, Finding{
				Kind:     "missing_database_table",
				Severity: SeverityCritical,
				Message:  fmt.Sprintf("required table %q is missing", expectation.Name),
			})
			continue
		}
		for _, column := range normalizeList(expectation.Columns) {
			if !slices.Contains(table.Columns, column) {
				findings = append(findings, Finding{
					Kind:     "missing_database_column",
					Severity: SeverityCritical,
					Message:  fmt.Sprintf("required column %q is missing from table %q", column, expectation.Name),
				})
			}
		}
	}
	return findings
}

func recommendations(findings []Finding) []Recommendation {
	if len(findings) == 0 {
		return []Recommendation{{Action: "no_database_change", Reason: "database URL, probe, and schema evidence matched expectations", Safe: true}}
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
	case "missing_database_table", "missing_database_column":
		return "run_or_prepare_database_migration"
	case "database_connection_failed":
		return "inspect_database_service_and_credentials"
	default:
		return "review_database_configuration"
	}
}

func statusForFindings(findings []Finding) Status {
	if len(findings) == 0 {
		return StatusHealthy
	}
	for _, finding := range findings {
		if finding.Severity == SeverityCritical {
			return StatusDrift
		}
	}
	return StatusBlocked
}

func normalizeList(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			normalized = append(normalized, value)
		}
	}
	return normalized
}

func normalizeTableState(table TableState) TableState {
	return TableState{
		Name:    strings.ToLower(strings.TrimSpace(table.Name)),
		Columns: normalizeList(table.Columns),
	}
}

func hostAllowed(host string, allowed []string) bool {
	if slices.Contains(allowed, host) {
		return true
	}
	for _, candidate := range allowed {
		if ip := net.ParseIP(candidate); ip != nil && ip.String() == host {
			return true
		}
	}
	return false
}

func redactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "<redacted-database-url>"
	}
	return redactParsedURL(parsed)
}

func redactParsedURL(parsed *url.URL) string {
	clone := *parsed
	if clone.User != nil {
		username := clone.User.Username()
		if username == "" {
			username = "user"
		}
		clone.User = url.UserPassword(username, "redacted")
	}
	return clone.String()
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
