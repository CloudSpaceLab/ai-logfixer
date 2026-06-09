package envvars

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/engine"
)

type LookupFunc func(string) (string, bool)

type Options struct {
	ServiceName string
	EnvFilePath string
	Policy      Policy
	LookupEnv   LookupFunc
	Apply       bool
	Now         time.Time
}

type Policy struct {
	Variables []VariableRequirement `json:"variables"`
}

type VariableRequirement struct {
	Name              string `json:"name"`
	Required          bool   `json:"required"`
	Secret            bool   `json:"secret"`
	DefaultValue      string `json:"default_value,omitempty"`
	AllowDefaultWrite bool   `json:"allow_default_write"`
}

type Finding struct {
	Name       string `json:"name"`
	Problem    string `json:"problem"`
	Secret     bool   `json:"secret"`
	CanDefault bool   `json:"can_default"`
}

type Operation struct {
	Action string `json:"action"`
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
	Path   string `json:"path,omitempty"`
}

type Result struct {
	InvestigationRequest contractsv1.InvestigationRequest `json:"investigation_request"`
	Diagnosis            contractsv1.DiagnosisResult      `json:"diagnosis"`
	RemediationPlan      contractsv1.RemediationPlan      `json:"remediation_plan"`
	Attempt              contractsv1.RemediationAttempt   `json:"attempt"`
	Receipt              contractsv1.Receipt              `json:"receipt"`
	Findings             []Finding                        `json:"findings"`
	Operations           []Operation                      `json:"operations"`
	RollbackPath         string                           `json:"rollback_path,omitempty"`
}

type rollbackManifest struct {
	CreatedAt time.Time `json:"created_at"`
	EnvPath   string    `json:"env_path"`
	Existed   bool      `json:"existed"`
	Content   string    `json:"content"`
}

func Run(_ context.Context, options Options) (Result, error) {
	options = normalizeOptions(options)
	findings, operations := evaluatePolicy(options)
	if len(findings) == 0 {
		return buildSuccessResult(options, findings, operations, "", "succeeded"), nil
	}
	if hasBlockedFinding(findings) {
		return buildBlockedResult(options, findings), nil
	}

	result := buildSuccessResult(options, findings, operations, "", "dry_run")
	if !options.Apply {
		return result, validateResult(result)
	}
	if strings.TrimSpace(options.EnvFilePath) == "" {
		return buildBlockedResult(options, []Finding{{Problem: "env file path is required for default writes"}}), nil
	}

	rollbackPath, err := writeRollback(options)
	if err != nil {
		return Result{}, fmt.Errorf("write env rollback manifest: %w", err)
	}
	if err := applyOperations(options, operations); err != nil {
		_ = restoreRollback(rollbackPath)
		return Result{}, fmt.Errorf("apply env defaults: %w", err)
	}
	result = buildSuccessResult(options, findings, operations, rollbackPath, "succeeded")
	return result, validateResult(result)
}

func normalizeOptions(options Options) Options {
	if options.ServiceName == "" {
		options.ServiceName = "unknown-service"
	}
	if options.LookupEnv == nil {
		options.LookupEnv = os.LookupEnv
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}
	return options
}

func evaluatePolicy(options Options) ([]Finding, []Operation) {
	var findings []Finding
	var operations []Operation
	for _, variable := range options.Policy.Variables {
		if strings.TrimSpace(variable.Name) == "" || !variable.Required {
			continue
		}
		if _, ok := options.LookupEnv(variable.Name); ok {
			continue
		}
		canDefault := !variable.Secret && variable.AllowDefaultWrite && variable.DefaultValue != ""
		findings = append(findings, Finding{
			Name:       variable.Name,
			Problem:    "missing_required_env_var",
			Secret:     variable.Secret,
			CanDefault: canDefault,
		})
		if canDefault {
			operations = append(operations, Operation{
				Action: "write_default",
				Name:   variable.Name,
				Value:  variable.DefaultValue,
				Path:   options.EnvFilePath,
			})
		}
	}
	return findings, operations
}

func hasBlockedFinding(findings []Finding) bool {
	for _, finding := range findings {
		if finding.Secret || !finding.CanDefault {
			return true
		}
	}
	return false
}

func writeRollback(options Options) (string, error) {
	manifest := rollbackManifest{CreatedAt: options.Now, EnvPath: options.EnvFilePath}
	raw, err := os.ReadFile(filepath.Clean(options.EnvFilePath))
	if err == nil {
		manifest.Existed = true
		manifest.Content = string(raw)
	} else if !os.IsNotExist(err) {
		return "", err
	}

	rollbackDir := filepath.Join(filepath.Dir(options.EnvFilePath), ".ai-logfixer", "env-rollbacks")
	if err := os.MkdirAll(rollbackDir, 0o755); err != nil {
		return "", err
	}
	rollbackPath := filepath.Join(rollbackDir, "env-"+options.Now.Format("20060102T150405Z")+".json")
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	encoded = append(encoded, '\n')
	return rollbackPath, os.WriteFile(rollbackPath, encoded, 0o600)
}

func applyOperations(options Options, operations []Operation) error {
	if err := os.MkdirAll(filepath.Dir(options.EnvFilePath), 0o755); err != nil {
		return err
	}
	existing := map[string]string{}
	if raw, err := os.ReadFile(filepath.Clean(options.EnvFilePath)); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if ok {
				existing[key] = value
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, operation := range operations {
		if operation.Action != "write_default" {
			return fmt.Errorf("unsupported env operation %q", operation.Action)
		}
		existing[operation.Name] = operation.Value
	}
	var builder strings.Builder
	for _, operation := range operations {
		builder.WriteString(operation.Name)
		builder.WriteString("=")
		builder.WriteString(existing[operation.Name])
		builder.WriteString("\n")
	}
	return os.WriteFile(filepath.Clean(options.EnvFilePath), []byte(builder.String()), 0o600)
}

func restoreRollback(path string) error {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return err
	}
	var manifest rollbackManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	if !manifest.Existed {
		if err := os.Remove(manifest.EnvPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return os.WriteFile(manifest.EnvPath, []byte(manifest.Content), 0o600)
}

func buildBlockedResult(options Options, findings []Finding) Result {
	factory := engine.NewContractIDFactory()
	reason := findingsSummary(findings)
	signal := engine.IncidentSignal{
		Service: options.ServiceName,
		Source:  options.EnvFilePath,
		Kind:    "env_var_drift",
		Code:    "missing_env_var",
		Count:   len(findings),
		Start:   options.Now.Add(-time.Minute),
		End:     options.Now,
		Tags:    []string{"env", "blocked"},
	}
	investigation := buildInvestigation(options, factory, signal, "Environment variable remediation blocked")
	diagnosis := contractsv1.DiagnosisResult{
		ID:                   factory.ID("diag_env_blocked", options.ServiceName, reason),
		ContractVersion:      contractsv1.ContractVersion,
		SchemaURL:            contractsv1.DiagnosisSchemaURL,
		Status:               contractsv1.DiagnosisStatusBlockedBySafety,
		Summary:              "Missing environment variables were detected, but automatic remediation is blocked.",
		Confidence:           0.86,
		SuspectedRootCause:   reason,
		AffectedServices:     []string{options.ServiceName},
		EvidenceItems:        []contractsv1.EvidenceItem{envEvidence(options, factory, reason)},
		Recommendations:      []contractsv1.RunbookRecommendation{{ID: factory.ID("rec_env_secret", reason), Title: "Provide missing environment value", Reason: reason, Confidence: 0.86, Steps: []string{"Review the missing environment variable.", "Set it through the approved secret/config provider.", "Restart or reload the service if required."}, RequiredPermissions: []string{"secrets:write", "config:review"}, EstimatedRisk: contractsv1.SafetyBlocked, RequiresApproval: false}},
		SafetyClassification: contractsv1.SafetyBlocked,
		DisplayStatus:        "Environment remediation blocked",
		UserMessage:          reason,
		NextActions:          []contractsv1.NextAction{{ID: "next_review_env", Label: "Review env", ActionType: "manual_review", Description: "Provide the missing environment variable through an approved provider.", Enabled: true}},
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: factory.ID("tl_env_blocked", reason), Type: "diagnosis.blocked", Message: reason, Severity: "warning", Timestamp: options.Now}},
		ExternalRefs:         []contractsv1.ExternalRef{},
		KnowledgeRefs:        []contractsv1.KnowledgeRef{},
		CreatedAt:            options.Now,
	}
	builder := engine.BlockedPlanBuilder{IDFactory: factory, Now: options.Now, Actor: "ai-logfixer-env-diagnostics"}
	plan := builder.RemediationPlan(diagnosis.ID, signal, reason)
	attempt := builder.EscalatedAttempt(plan.ID, signal, reason)
	receipt := builder.EscalatedReceipt(diagnosis.ID, plan.ID, attempt.ID, signal, reason)
	return Result{InvestigationRequest: investigation, Diagnosis: diagnosis, RemediationPlan: plan, Attempt: attempt, Receipt: receipt, Findings: findings}
}

func buildSuccessResult(options Options, findings []Finding, operations []Operation, rollbackPath string, outcome string) Result {
	factory := engine.NewContractIDFactory()
	signal := engine.IncidentSignal{
		Service: options.ServiceName,
		Source:  options.EnvFilePath,
		Kind:    "env_var_drift",
		Code:    "missing_env_var",
		Count:   len(findings),
		Start:   options.Now.Add(-time.Minute),
		End:     options.Now,
		Tags:    []string{"env"},
	}
	investigation := buildInvestigation(options, factory, signal, "Environment variable defaults can be applied")
	diagnosisID := factory.ID("diag_env_defaults", options.ServiceName, findingsSummary(findings))
	planID := factory.ID("rem_plan_env_defaults", diagnosisID)
	attemptID := factory.ID("rem_attempt_env_defaults", planID, outcome)
	receiptID := factory.ID("receipt_env_defaults", attemptID)

	statusMessage := "Dry run completed; no environment file was changed."
	status := "dry_run"
	action := "dry run only"
	if outcome == "succeeded" {
		statusMessage = "Explicit non-secret environment defaults were written."
		status = "healthy"
		action = "wrote explicit non-secret env defaults"
	}

	diagnosis := contractsv1.DiagnosisResult{
		ID:                   diagnosisID,
		ContractVersion:      contractsv1.ContractVersion,
		SchemaURL:            contractsv1.DiagnosisSchemaURL,
		Status:               contractsv1.DiagnosisStatusComplete,
		Summary:              "Missing non-secret environment variables can be restored from explicit defaults.",
		Confidence:           0.8,
		SuspectedRootCause:   findingsSummary(findings),
		AffectedServices:     []string{options.ServiceName},
		EvidenceItems:        []contractsv1.EvidenceItem{envEvidence(options, factory, findingsSummary(findings))},
		Recommendations:      []contractsv1.RunbookRecommendation{{ID: factory.ID("rec_env_defaults", diagnosisID), Title: "Write explicit non-secret defaults", Reason: "The policy includes non-secret defaults approved for local env-file repair.", Confidence: 0.8, Steps: []string{"Record rollback content.", "Write allowed defaults to the explicit env file.", "Verify the service."}, RequiredPermissions: []string{"env_file:write"}, EstimatedRisk: contractsv1.SafetyLowRisk, RequiresApproval: false}},
		SafetyClassification: contractsv1.SafetyLowRisk,
		DisplayStatus:        "Environment defaults diagnosed",
		UserMessage:          "I found missing non-secret environment variables with explicit defaults.",
		NextActions:          []contractsv1.NextAction{{ID: "next_apply_env_defaults", Label: "Apply env defaults", ActionType: "apply_remediation", Description: "Write explicit non-secret defaults to the env file.", Enabled: true}},
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: factory.ID("tl_env_diag", diagnosisID), Type: "diagnosis.completed", Message: "Environment variable diagnosis completed.", Severity: "info", Timestamp: options.Now}},
		ExternalRefs:         []contractsv1.ExternalRef{},
		KnowledgeRefs:        []contractsv1.KnowledgeRef{},
		CreatedAt:            options.Now,
	}
	plan := contractsv1.RemediationPlan{
		ID:                planID,
		ContractVersion:   contractsv1.ContractVersion,
		SchemaURL:         contractsv1.RemediationPlanSchemaURL,
		DiagnosisResultID: diagnosisID,
		Summary:           "Write explicit non-secret environment defaults.",
		FixPreview:        contractsv1.DiffPreview{Before: findingsSummary(findings), After: operationsSummary(operations)},
		RollbackPlan: contractsv1.RollbackPlan{
			ID:                   factory.ID("rollback_env_defaults", planID),
			RollbackType:         contractsv1.RollbackRestoreConfig,
			SnapshotRefs:         []string{defaultString(rollbackPath, "env_file_rollback_manifest")},
			RestoreSteps:         []string{"Restore the previous env file content from the rollback manifest.", "Verify service state."},
			Limitations:          []string{"Rollback restores the env file content only; it does not change process environment already loaded by a running service."},
			RiskLevel:            contractsv1.SafetyLowRisk,
			RequiresManualReview: false,
		},
		RiskLevel:        contractsv1.SafetyLowRisk,
		ApprovalRequired: false,
		Status:           contractsv1.RemediationStatusApproved,
		DisplayStatus:    "Environment default fix approved automatically",
		UserMessage:      "This low-risk env fix writes only explicit non-secret defaults.",
		NextActions:      []contractsv1.NextAction{{ID: "next_execute_env_defaults", Label: "Execute env fix", ActionType: "execute_remediation", Description: "Write defaults and verify service state.", Enabled: true}},
		TimelineEvents:   []contractsv1.TimelineEvent{{ID: factory.ID("tl_env_plan", planID), Type: "remediation.plan_created", Message: "Environment default remediation plan created.", Severity: "info", Timestamp: options.Now}},
		ExternalRefs:     []contractsv1.ExternalRef{},
		KnowledgeRefs:    []contractsv1.KnowledgeRef{},
		CreatedAt:        options.Now,
	}
	started := options.Now.Add(time.Second)
	finished := options.Now.Add(2 * time.Second)
	attempt := contractsv1.RemediationAttempt{
		ID:                  attemptID,
		ContractVersion:     contractsv1.ContractVersion,
		SchemaURL:           contractsv1.RemediationAttemptSchemaURL,
		RemediationPlanID:   planID,
		ApprovalRequestID:   "auto_approved_non_secret_env_defaults",
		Status:              contractsv1.RemediationStatusSucceeded,
		ExecutionStartedAt:  &started,
		ExecutionFinishedAt: &finished,
		MonitorSummary:      contractsv1.MonitorSummary{Status: status, Message: statusMessage, Signals: []string{fmt.Sprintf("operations=%d", len(operations))}, Duration: "1s"},
		DisplayStatus:       "Environment remediation recorded",
		UserMessage:         statusMessage,
		TimelineEvents:      []contractsv1.TimelineEvent{{ID: factory.ID("tl_env_attempt", attemptID), Type: "remediation." + outcome, Message: statusMessage, Severity: "info", Timestamp: finished}},
		ExternalRefs:        []contractsv1.ExternalRef{},
	}
	receipt := contractsv1.Receipt{
		ID:                   receiptID,
		DiagnosisID:          diagnosisID,
		RemediationPlanID:    planID,
		RemediationAttemptID: attemptID,
		ActionTaken:          action,
		Actor:                "ai-logfixer-env-diagnostics",
		Approver:             "auto_approved_non_secret_env_defaults",
		Timestamp:            options.Now.Add(3 * time.Second),
		BeforeState:          findingsSummary(findings),
		AfterState:           operationsSummary(operations),
		Outcome:              outcome,
		Summary:              "AI LogFixer diagnosed missing env vars and handled only explicit non-secret defaults.",
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: factory.ID("tl_env_receipt", receiptID), Type: "receipt.created", Message: "Receipt recorded for env diagnostics.", Severity: "info", Timestamp: options.Now.Add(3 * time.Second)}},
		ExternalRefs:         []contractsv1.ExternalRef{},
		KnowledgeRefs:        []contractsv1.KnowledgeRef{},
	}
	return Result{InvestigationRequest: investigation, Diagnosis: diagnosis, RemediationPlan: plan, Attempt: attempt, Receipt: receipt, Findings: findings, Operations: operations, RollbackPath: rollbackPath}
}

func buildInvestigation(options Options, factory engine.ContractIDFactory, signal engine.IncidentSignal, message string) contractsv1.InvestigationRequest {
	return contractsv1.InvestigationRequest{
		ID:              factory.ID("inv_req_env", options.ServiceName, message),
		ContractVersion: contractsv1.ContractVersion,
		SchemaURL:       contractsv1.InvestigationRequestSchemaURL,
		SourceType:      contractsv1.SourceTypeAutomatic,
		SourceName:      "ai-logfixer-env-diagnostics",
		RequestedBy:     "ai-logfixer",
		Service:         options.ServiceName,
		Symptom:         message,
		ErrorCode:       signal.ErrorCode(),
		TimeWindow:      contractsv1.TimeWindow{Start: signal.Start, End: signal.End},
		SignalFingerprint: contractsv1.SignalFingerprint{
			Service:       options.ServiceName,
			Symptom:       "missing_env_var",
			ErrorCode:     signal.ErrorCode(),
			Source:        options.EnvFilePath,
			DeployVersion: "unknown",
			Tags:          signal.Tags,
		},
		DisplayStatus: "Environment investigation started automatically",
		UserMessage:   message,
		ExternalRefs:  []contractsv1.ExternalRef{},
		KnowledgeRefs: []contractsv1.KnowledgeRef{},
		CreatedAt:     options.Now,
	}
}

func envEvidence(options Options, factory engine.ContractIDFactory, summary string) contractsv1.EvidenceItem {
	return contractsv1.EvidenceItem{
		ID:             factory.ID("ev_env", options.ServiceName, summary),
		Type:           contractsv1.EvidenceTypeConfig,
		Source:         defaultString(options.EnvFilePath, "environment"),
		Timestamp:      options.Now,
		Title:          "Environment variable policy evidence",
		Summary:        summary,
		RawExcerpt:     summary,
		RedactionState: contractsv1.RedactionStateRedacted,
		RelatedIDs:     []string{},
		UIHints:        contractsv1.UIHints{Icon: "key-round", Tone: "warning", Sections: []string{"env"}},
		ExternalRefs:   []contractsv1.ExternalRef{},
		KnowledgeRefs:  []contractsv1.KnowledgeRef{},
	}
}

func validateResult(result Result) error {
	return errors.Join(
		result.InvestigationRequest.Validate(),
		result.Diagnosis.Validate(),
		result.RemediationPlan.Validate(),
		result.Attempt.Validate(),
		result.Receipt.Validate(),
	)
}

func findingsSummary(findings []Finding) string {
	if len(findings) == 0 {
		return "no missing required environment variables"
	}
	values := make([]string, 0, len(findings))
	for _, finding := range findings {
		kind := "non_secret"
		if finding.Secret {
			kind = "secret"
		}
		values = append(values, finding.Name+" "+finding.Problem+" "+kind)
	}
	return strings.Join(values, "; ")
}

func operationsSummary(operations []Operation) string {
	if len(operations) == 0 {
		return "no automatic env changes"
	}
	values := make([]string, 0, len(operations))
	for _, operation := range operations {
		values = append(values, operation.Action+" "+operation.Name+" to "+operation.Path)
	}
	return strings.Join(values, "; ")
}

func defaultString(value string, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
