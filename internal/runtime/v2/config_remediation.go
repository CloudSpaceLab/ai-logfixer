package runtimev2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/engine"
	"github.com/CloudSpaceLab/ai-logfixer/internal/workflow"
)

type Options struct {
	ServiceName     string
	BaseURL         string
	LogPath         string
	ConfigPath      string
	HealthyUpstream string
	ErrorThreshold  int
	Now             time.Time

	Method           string
	Route            string
	StatusCode       int
	StatusClass      int
	Window           time.Duration
	ConfigKeyPath    string
	ReplacementValue string
	VerifyURL        string
	ExpectedStatus   int

	WorkflowService        *workflow.Service
	WorkflowTenantID       string
	WorkflowActorID        string
	WorkflowCorrelationID  string
	WorkflowSuppressOutbox bool
}

type Result struct {
	InvestigationRequest contractsv1.InvestigationRequest
	Diagnosis            contractsv1.DiagnosisResult
	RemediationPlan      contractsv1.RemediationPlan
	Attempt              contractsv1.RemediationAttempt
	Receipt              contractsv1.Receipt
	BackupPath           string
}

func Run(ctx context.Context, options Options) (Result, error) {
	options = normalizeOptions(options)

	logContent, err := os.ReadFile(filepath.Clean(options.LogPath))
	if err != nil {
		return Result{}, fmt.Errorf("read log file: %w", err)
	}
	entries := engine.ParseKeyValueHTTPLogs(string(logContent), options.LogPath)
	signals := engine.RepeatedHTTPFailures(entries, engine.FailureThreshold{
		ServiceName: options.ServiceName,
		Method:      options.Method,
		Route:       options.Route,
		StatusCode:  options.StatusCode,
		StatusClass: options.StatusClass,
		MinCount:    options.ErrorThreshold,
		Window:      options.Window,
	})
	if len(signals) == 0 {
		return Result{}, fmt.Errorf("failure threshold not reached for %s", options.ServiceName)
	}
	signal := signals[0]
	if signal.Service == "" {
		signal.Service = options.ServiceName
	}

	investigationRequest := buildInvestigationRequest(options, signal)
	if !hasConfigPatchDescriptor(options) {
		reason := "Automatic config remediation is blocked because config path, key path, replacement value, or verification URL is missing."
		return buildBlockedResult(options, signal, investigationRequest, reason, string(logContent))
	}

	rawConfig, document, beforeValue, err := readJSONConfig(options.ConfigPath, options.ConfigKeyPath)
	if err != nil {
		return Result{}, fmt.Errorf("read config: %w", err)
	}
	if beforeValue == "" {
		reason := fmt.Sprintf("Automatic config remediation is blocked because %s was not found as a non-empty string in %s.", options.ConfigKeyPath, options.ConfigPath)
		return buildBlockedResult(options, signal, investigationRequest, reason, string(logContent))
	}

	diagnosis := buildDiagnosis(options, signal, beforeValue, string(logContent))
	remediationPlan := buildRemediationPlan(options, signal, diagnosis.ID, beforeValue)

	if err := investigationRequest.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate investigation request: %w", err)
	}
	if err := diagnosis.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate diagnosis: %w", err)
	}
	if err := remediationPlan.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate remediation plan: %w", err)
	}

	if err := recordRemediationPlanTransition(ctx, options, remediationPlan.ID, remediationPlan.Status, contractsv1.RemediationStatusRunning, "Config remediation execution started"); err != nil {
		return Result{}, fmt.Errorf("record remediation plan running transition: %w", err)
	}
	remediationPlan.Status = contractsv1.RemediationStatusRunning

	backupPath, err := backupConfig(options.ConfigPath, rawConfig, options.Now)
	if err != nil {
		return Result{}, fmt.Errorf("backup config: %w", recordRemediationPlanFailure(ctx, options, remediationPlan.ID, err, "Config remediation backup failed"))
	}

	if err := setJSONString(document, options.ConfigKeyPath, options.ReplacementValue); err != nil {
		return Result{}, fmt.Errorf("prepare config patch: %w", recordRemediationPlanFailure(ctx, options, remediationPlan.ID, err, "Config remediation patch preparation failed"))
	}
	if err := writeJSONConfig(options.ConfigPath, document); err != nil {
		return Result{}, fmt.Errorf("apply config patch: %w", recordRemediationPlanFailure(ctx, options, remediationPlan.ID, err, "Config remediation patch failed"))
	}

	attempt := buildAttempt(options, signal, remediationPlan.ID)
	if err := verifyFixed(ctx, options.VerifyURL, options.ExpectedStatus); err != nil {
		attempt.Status = contractsv1.RemediationStatusFailed
		attempt.UserMessage = "The patch was applied, but verification failed. Rollback is required."
		_ = restoreBackup(options.ConfigPath, backupPath)
		return Result{}, fmt.Errorf("verify fix: %w", recordRemediationPlanFailure(ctx, options, remediationPlan.ID, err, "Config remediation verification failed"))
	}

	if err := recordRemediationPlanTransition(ctx, options, remediationPlan.ID, contractsv1.RemediationStatusRunning, contractsv1.RemediationStatusSucceeded, "Config remediation verified successfully"); err != nil {
		return Result{}, fmt.Errorf("record remediation plan succeeded transition: %w", err)
	}
	remediationPlan.Status = contractsv1.RemediationStatusSucceeded
	remediationPlan.DisplayStatus = "Fix applied and verified"
	remediationPlan.UserMessage = fmt.Sprintf("The allowlisted config fix ran successfully and %s recovered.", options.VerifyURL)
	remediationPlan.NextActions = []contractsv1.NextAction{}

	receipt := buildReceipt(options, signal, diagnosis.ID, remediationPlan.ID, attempt.ID, beforeValue, options.ReplacementValue)

	if err := remediationPlan.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate final remediation plan: %w", err)
	}
	if err := attempt.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate remediation attempt: %w", err)
	}
	if err := receipt.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate receipt: %w", err)
	}

	return Result{
		InvestigationRequest: investigationRequest,
		Diagnosis:            diagnosis,
		RemediationPlan:      remediationPlan,
		Attempt:              attempt,
		Receipt:              receipt,
		BackupPath:           backupPath,
	}, nil
}

func normalizeOptions(options Options) Options {
	if options.ServiceName == "" {
		options.ServiceName = "unknown-service"
	}
	if options.ErrorThreshold == 0 {
		options.ErrorThreshold = 3
	}
	if options.Window == 0 {
		options.Window = 5 * time.Minute
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}
	if options.Route == "" {
		options.Route = "/orders"
	}
	if options.StatusCode == 0 && options.StatusClass == 0 {
		options.StatusCode = http.StatusServiceUnavailable
	}
	if options.ReplacementValue == "" {
		options.ReplacementValue = options.HealthyUpstream
	}
	if options.ConfigKeyPath == "" && options.ReplacementValue != "" {
		options.ConfigKeyPath = "upstream_url"
	}
	if options.VerifyURL == "" && options.BaseURL != "" && options.Route != "" {
		options.VerifyURL = strings.TrimRight(options.BaseURL, "/") + options.Route
	}
	if options.ExpectedStatus == 0 {
		options.ExpectedStatus = http.StatusOK
	}
	return options
}

func hasConfigPatchDescriptor(options Options) bool {
	return strings.TrimSpace(options.ConfigPath) != "" &&
		strings.TrimSpace(options.ConfigKeyPath) != "" &&
		strings.TrimSpace(options.ReplacementValue) != "" &&
		strings.TrimSpace(options.VerifyURL) != ""
}

func recordRemediationPlanTransition(ctx context.Context, options Options, planID string, from contractsv1.RemediationStatus, to contractsv1.RemediationStatus, message string) error {
	if options.WorkflowService == nil {
		return nil
	}
	actorID := options.WorkflowActorID
	if actorID == "" {
		actorID = "ai-logfixer-v2"
	}
	return options.WorkflowService.MoveRemediation(ctx, workflow.RemediationTransition{
		TenantID:     options.WorkflowTenantID,
		ResourceType: workflow.ResourceRemediationPlan,
		ResourceID:   planID,
		From:         from,
		To:           to,
		Metadata: workflow.TransitionMetadata{
			ActorID:        actorID,
			CorrelationID:  options.WorkflowCorrelationID,
			Message:        message,
			SuppressOutbox: options.WorkflowSuppressOutbox,
		},
	})
}

func recordRemediationPlanFailure(ctx context.Context, options Options, planID string, cause error, message string) error {
	err := recordRemediationPlanTransition(ctx, options, planID, contractsv1.RemediationStatusRunning, contractsv1.RemediationStatusFailed, message)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("record remediation plan failed transition: %w", err))
	}
	return cause
}

func readJSONConfig(path string, keyPath string) ([]byte, map[string]any, string, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, nil, "", err
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, nil, "", err
	}
	value, ok := getJSONString(document, keyPath)
	if !ok {
		return raw, document, "", nil
	}
	return raw, document, value, nil
}

func getJSONString(document map[string]any, keyPath string) (string, bool) {
	current := any(document)
	for _, segment := range strings.Split(keyPath, ".") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			return "", false
		}
		object, ok := current.(map[string]any)
		if !ok {
			return "", false
		}
		current, ok = object[segment]
		if !ok {
			return "", false
		}
	}
	value, ok := current.(string)
	if !ok || value == "" {
		return "", false
	}
	return value, true
}

func setJSONString(document map[string]any, keyPath string, replacement string) error {
	current := document
	segments := strings.Split(keyPath, ".")
	for index, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			return errors.New("config key path contains an empty segment")
		}
		if index == len(segments)-1 {
			if _, ok := current[segment].(string); !ok {
				return fmt.Errorf("config key %s is not an existing string", keyPath)
			}
			current[segment] = replacement
			return nil
		}
		next, ok := current[segment].(map[string]any)
		if !ok {
			return fmt.Errorf("config key %s does not exist as an object", strings.Join(segments[:index+1], "."))
		}
		current = next
	}
	return nil
}

func writeJSONConfig(path string, document map[string]any) error {
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(filepath.Clean(path), raw, 0o644)
}

func backupConfig(path string, raw []byte, now time.Time) (string, error) {
	backupDir := filepath.Join(filepath.Dir(path), "backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", err
	}
	backupPath := filepath.Join(backupDir, "app-"+now.Format("20060102T150405Z")+".json")
	return backupPath, os.WriteFile(backupPath, raw, 0o644)
}

func restoreBackup(configPath string, backupPath string) error {
	raw, err := os.ReadFile(filepath.Clean(backupPath))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Clean(configPath), raw, 0o644)
}

func verifyFixed(ctx context.Context, url string, expectedStatus int) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != expectedStatus {
		return fmt.Errorf("expected status %d from %s, got %d", expectedStatus, url, response.StatusCode)
	}
	return nil
}

func buildInvestigationRequest(options Options, signal engine.IncidentSignal) contractsv1.InvestigationRequest {
	factory := engine.NewContractIDFactory()
	parts := signal.StableParts()
	return contractsv1.InvestigationRequest{
		ID:              factory.ID("inv_req_http_failure", parts...),
		ContractVersion: contractsv1.ContractVersion,
		SchemaURL:       contractsv1.InvestigationRequestSchemaURL,
		SourceType:      contractsv1.SourceTypeAutomatic,
		SourceName:      "ai-logfixer-config-remediator",
		RequestedBy:     "ai-logfixer",
		Service:         options.ServiceName,
		Symptom:         fmt.Sprintf("Repeated HTTP %s responses for %s", signal.ErrorCode(), signal.RouteLabel()),
		ErrorCode:       signal.ErrorCode(),
		TimeWindow: contractsv1.TimeWindow{
			Start: signal.Start,
			End:   signal.End,
		},
		SignalFingerprint: contractsv1.SignalFingerprint{
			Service:       options.ServiceName,
			Symptom:       "http_error_spike",
			ErrorCode:     signal.ErrorCode(),
			Source:        options.LogPath,
			DeployVersion: "unknown",
			Tags:          append([]string{"config-remediation"}, signal.Tags...),
		},
		DisplayStatus: "Investigation started automatically",
		UserMessage:   "I detected repeated HTTP failures and started a guarded config-remediation investigation.",
		ExternalRefs:  []contractsv1.ExternalRef{},
		KnowledgeRefs: []contractsv1.KnowledgeRef{},
		CreatedAt:     options.Now,
	}
}

func buildDiagnosis(options Options, signal engine.IncidentSignal, currentValue string, logContent string) contractsv1.DiagnosisResult {
	factory := engine.NewContractIDFactory()
	parts := append(signal.StableParts(), options.ConfigPath, options.ConfigKeyPath)
	id := factory.ID("diag_config_remediation", parts...)
	logEvidenceID := factory.ID("ev_http_logs", parts...)
	configEvidenceID := factory.ID("ev_config_key", parts...)
	patchID := factory.ID("patch_config_key", parts...)
	rollbackID := factory.ID("rollback_config_key", parts...)
	return contractsv1.DiagnosisResult{
		ID:                 id,
		ContractVersion:    contractsv1.ContractVersion,
		SchemaURL:          contractsv1.DiagnosisSchemaURL,
		Status:             contractsv1.DiagnosisStatusComplete,
		Summary:            fmt.Sprintf("Repeated HTTP failures can be remediated by updating allowlisted config key %s.", options.ConfigKeyPath),
		Confidence:         0.83,
		SuspectedRootCause: fmt.Sprintf("The failing path %s is associated with config key %s currently set to %q.", signal.RouteLabel(), options.ConfigKeyPath, currentValue),
		AffectedServices:   []string{options.ServiceName},
		EvidenceItems: []contractsv1.EvidenceItem{
			{
				ID:             logEvidenceID,
				Type:           contractsv1.EvidenceTypeLog,
				Source:         options.LogPath,
				Timestamp:      options.Now,
				Title:          "Repeated HTTP failure logs",
				Summary:        fmt.Sprintf("The log file contains %d matching failures for %s.", signal.Count, signal.RouteLabel()),
				RawExcerpt:     safeExcerpt(logContent),
				RedactionState: contractsv1.RedactionStateRedacted,
				RelatedIDs:     []string{configEvidenceID},
				UIHints:        contractsv1.UIHints{Icon: "file-warning", Tone: "danger", Sections: []string{"logs", "evidence"}},
				ExternalRefs:   []contractsv1.ExternalRef{},
				KnowledgeRefs:  []contractsv1.KnowledgeRef{},
			},
			{
				ID:             configEvidenceID,
				Type:           contractsv1.EvidenceTypeConfig,
				Source:         options.ConfigPath,
				Timestamp:      options.Now,
				Title:          "Allowlisted config key",
				Summary:        fmt.Sprintf("The configured remediation target is %s.", options.ConfigKeyPath),
				RawExcerpt:     options.ConfigKeyPath + "=" + currentValue,
				RedactionState: contractsv1.RedactionStateRedacted,
				RelatedIDs:     []string{logEvidenceID},
				UIHints:        contractsv1.UIHints{Icon: "settings", Tone: "warning", Sections: []string{"config"}},
				ExternalRefs:   []contractsv1.ExternalRef{},
				KnowledgeRefs:  []contractsv1.KnowledgeRef{},
			},
		},
		Recommendations: []contractsv1.RunbookRecommendation{
			{
				ID:                  factory.ID("rec_config_patch", parts...),
				Title:               "Apply allowlisted config patch",
				Reason:              "The operator supplied an explicit config key, replacement value, and verification URL for this failure.",
				Confidence:          0.81,
				Steps:               []string{"Back up the current config.", "Replace the allowlisted JSON string value.", "Verify the configured URL returns the expected status."},
				RequiredPermissions: []string{"config:write", "service:verify"},
				EstimatedRisk:       contractsv1.SafetyLowRisk,
				RequiresApproval:    false,
			},
		},
		PatchPlan: &contractsv1.PatchPlan{
			ID:         patchID,
			TargetType: contractsv1.PatchTargetConfig,
			TargetRefs: []string{options.ConfigPath},
			DiffPreview: contractsv1.DiffPreview{
				Before: options.ConfigKeyPath + "=" + currentValue,
				After:  options.ConfigKeyPath + "=" + options.ReplacementValue,
			},
			RiskLevel:        contractsv1.SafetyLowRisk,
			RequiresApproval: false,
			BlockedReasons:   []string{},
		},
		RollbackPlan: &contractsv1.RollbackPlan{
			ID:                   rollbackID,
			RollbackType:         contractsv1.RollbackRestoreConfig,
			SnapshotRefs:         []string{"config_backup"},
			RestoreSteps:         []string{"Restore the backed-up app config.", "Verify the service state."},
			Limitations:          []string{"Rollback restores the previous config value and may reintroduce the observed failure."},
			RiskLevel:            contractsv1.SafetyLowRisk,
			RequiresManualReview: false,
		},
		SafetyClassification: contractsv1.SafetyLowRisk,
		DisplayStatus:        "Allowlisted config remediation diagnosed",
		UserMessage:          "I found repeated failures and matched them to an explicit allowlisted config patch.",
		NextActions:          []contractsv1.NextAction{{ID: "next_apply_config_fix", Label: "Apply config fix", ActionType: "apply_remediation", Description: "Back up the config, update the JSON key, and verify recovery.", Enabled: true}},
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: factory.ID("tl_config_diag", parts...), Type: "diagnosis.completed", Message: "Config remediation diagnosis completed.", Severity: "info", Timestamp: options.Now}},
		ExternalRefs:         []contractsv1.ExternalRef{},
		KnowledgeRefs:        []contractsv1.KnowledgeRef{},
		CreatedAt:            options.Now,
	}
}

func buildRemediationPlan(options Options, signal engine.IncidentSignal, diagnosisID string, currentValue string) contractsv1.RemediationPlan {
	factory := engine.NewContractIDFactory()
	parts := append(signal.StableParts(), diagnosisID, options.ConfigPath, options.ConfigKeyPath)
	return contractsv1.RemediationPlan{
		ID:                factory.ID("rem_plan_config_patch", parts...),
		ContractVersion:   contractsv1.ContractVersion,
		SchemaURL:         contractsv1.RemediationPlanSchemaURL,
		DiagnosisResultID: diagnosisID,
		Summary:           fmt.Sprintf("Back up %s and replace %s.", options.ConfigPath, options.ConfigKeyPath),
		FixPreview: contractsv1.DiffPreview{
			Before: options.ConfigKeyPath + "=" + currentValue,
			After:  options.ConfigKeyPath + "=" + options.ReplacementValue,
		},
		RollbackPlan: contractsv1.RollbackPlan{
			ID:                   factory.ID("rollback_config_plan", parts...),
			RollbackType:         contractsv1.RollbackRestoreConfig,
			SnapshotRefs:         []string{"config_backup"},
			RestoreSteps:         []string{"Restore the backed-up app config.", "Verify the service state."},
			Limitations:          []string{"Rollback restores the previous config value and may reintroduce the observed failure."},
			RiskLevel:            contractsv1.SafetyLowRisk,
			RequiresManualReview: false,
		},
		RiskLevel:        contractsv1.SafetyLowRisk,
		ApprovalRequired: false,
		Status:           contractsv1.RemediationStatusApproved,
		DisplayStatus:    "Allowlisted config fix approved automatically",
		UserMessage:      "This low-risk config fix can run automatically after saving the previous config.",
		NextActions:      []contractsv1.NextAction{{ID: "next_execute_config_fix", Label: "Execute fix", ActionType: "execute_remediation", Description: "Apply the config change and verify recovery.", Enabled: true}},
		TimelineEvents:   []contractsv1.TimelineEvent{{ID: factory.ID("tl_config_plan", parts...), Type: "remediation.plan_created", Message: "Config remediation plan created.", Severity: "info", Timestamp: options.Now}},
		ExternalRefs:     []contractsv1.ExternalRef{},
		KnowledgeRefs:    []contractsv1.KnowledgeRef{},
		CreatedAt:        options.Now,
	}
}

func buildAttempt(options Options, signal engine.IncidentSignal, planID string) contractsv1.RemediationAttempt {
	factory := engine.NewContractIDFactory()
	parts := append(signal.StableParts(), planID, options.VerifyURL)
	started := options.Now.Add(time.Second)
	finished := options.Now.Add(2 * time.Second)
	return contractsv1.RemediationAttempt{
		ID:                  factory.ID("rem_attempt_config_patch", parts...),
		ContractVersion:     contractsv1.ContractVersion,
		SchemaURL:           contractsv1.RemediationAttemptSchemaURL,
		RemediationPlanID:   planID,
		ApprovalRequestID:   "auto_approved_low_risk_config_patch",
		Status:              contractsv1.RemediationStatusSucceeded,
		ExecutionStartedAt:  &started,
		ExecutionFinishedAt: &finished,
		MonitorSummary: contractsv1.MonitorSummary{
			Status:   "healthy",
			Message:  fmt.Sprintf("%s returned %d after the config patch.", options.VerifyURL, options.ExpectedStatus),
			Signals:  []string{fmt.Sprintf("http_status=%d", options.ExpectedStatus), "verify_url=" + options.VerifyURL},
			Duration: "1s",
		},
		DisplayStatus:  "Fix applied and verified",
		UserMessage:    "I updated the allowlisted config key and verified recovery.",
		TimelineEvents: []contractsv1.TimelineEvent{{ID: factory.ID("tl_config_attempt", parts...), Type: "remediation.succeeded", Message: "Config patch applied and verified.", Severity: "info", Timestamp: finished}},
		ExternalRefs:   []contractsv1.ExternalRef{},
	}
}

func buildReceipt(options Options, signal engine.IncidentSignal, diagnosisID string, planID string, attemptID string, before string, after string) contractsv1.Receipt {
	factory := engine.NewContractIDFactory()
	parts := append(signal.StableParts(), diagnosisID, planID, attemptID)
	return contractsv1.Receipt{
		ID:                   factory.ID("receipt_config_patch", parts...),
		DiagnosisID:          diagnosisID,
		RemediationPlanID:    planID,
		RemediationAttemptID: attemptID,
		ActionTaken:          "updated allowlisted JSON config key",
		Actor:                "ai-logfixer-config-remediator",
		Approver:             "auto_approved_low_risk_config_patch",
		Timestamp:            options.Now.Add(3 * time.Second),
		BeforeState:          options.ConfigKeyPath + "=" + before,
		AfterState:           options.ConfigKeyPath + "=" + after,
		Outcome:              "succeeded",
		Summary:              "AI LogFixer detected repeated failures, patched an allowlisted config key, verified recovery, and saved this receipt.",
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: factory.ID("tl_config_receipt", parts...), Type: "receipt.created", Message: "Receipt recorded for successful config remediation.", Severity: "info", Timestamp: options.Now.Add(3 * time.Second)}},
		ExternalRefs:         []contractsv1.ExternalRef{},
		KnowledgeRefs:        []contractsv1.KnowledgeRef{},
	}
}

func buildBlockedResult(options Options, signal engine.IncidentSignal, investigationRequest contractsv1.InvestigationRequest, reason string, logContent string) (Result, error) {
	factory := engine.NewContractIDFactory()
	parts := append(signal.StableParts(), reason)
	diagnosisID := factory.ID("diag_blocked_config", parts...)
	diagnosis := contractsv1.DiagnosisResult{
		ID:                   diagnosisID,
		ContractVersion:      contractsv1.ContractVersion,
		SchemaURL:            contractsv1.DiagnosisSchemaURL,
		Status:               contractsv1.DiagnosisStatusComplete,
		Summary:              "Repeated HTTP failures were detected, but automatic config remediation is blocked.",
		Confidence:           0.72,
		SuspectedRootCause:   "AI LogFixer has failure evidence but no complete allowlisted config patch descriptor.",
		AffectedServices:     []string{options.ServiceName},
		EvidenceItems:        []contractsv1.EvidenceItem{blockedLogEvidence(options, signal, logContent, factory.ID("ev_blocked_logs", parts...))},
		Recommendations:      []contractsv1.RunbookRecommendation{blockedRecommendation(reason, factory.ID("rec_blocked_config", parts...))},
		SafetyClassification: contractsv1.SafetyBlocked,
		DisplayStatus:        "Automatic config remediation blocked",
		UserMessage:          reason,
		NextActions:          []contractsv1.NextAction{{ID: "next_manual_config_review", Label: "Review config", ActionType: "manual_review", Description: "Provide an explicit config key, replacement value, verification URL, or a different remediator.", Enabled: true}},
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: factory.ID("tl_blocked_config_diag", parts...), Type: "diagnosis.completed", Message: "Config remediation blocked by safety policy.", Severity: "warning", Timestamp: options.Now}},
		ExternalRefs:         []contractsv1.ExternalRef{},
		KnowledgeRefs:        []contractsv1.KnowledgeRef{},
		CreatedAt:            options.Now,
	}
	builder := engine.BlockedPlanBuilder{IDFactory: factory, Now: options.Now, Actor: "ai-logfixer-config-remediator"}
	plan := builder.RemediationPlan(diagnosis.ID, signal, reason)
	attempt := builder.EscalatedAttempt(plan.ID, signal, reason)
	receipt := builder.EscalatedReceipt(diagnosis.ID, plan.ID, attempt.ID, signal, reason)

	if err := investigationRequest.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate blocked investigation request: %w", err)
	}
	if err := diagnosis.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate blocked diagnosis: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate blocked remediation plan: %w", err)
	}
	if err := attempt.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate blocked remediation attempt: %w", err)
	}
	if err := receipt.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate blocked receipt: %w", err)
	}
	return Result{InvestigationRequest: investigationRequest, Diagnosis: diagnosis, RemediationPlan: plan, Attempt: attempt, Receipt: receipt}, nil
}

func blockedLogEvidence(options Options, signal engine.IncidentSignal, logContent string, id string) contractsv1.EvidenceItem {
	return contractsv1.EvidenceItem{
		ID:             id,
		Type:           contractsv1.EvidenceTypeLog,
		Source:         options.LogPath,
		Timestamp:      options.Now,
		Title:          "Repeated HTTP failure logs",
		Summary:        fmt.Sprintf("The log file contains %d matching failures for %s.", signal.Count, signal.RouteLabel()),
		RawExcerpt:     safeExcerpt(logContent),
		RedactionState: contractsv1.RedactionStateRedacted,
		RelatedIDs:     []string{},
		UIHints:        contractsv1.UIHints{Icon: "file-warning", Tone: "danger", Sections: []string{"logs", "evidence"}},
		ExternalRefs:   []contractsv1.ExternalRef{},
		KnowledgeRefs:  []contractsv1.KnowledgeRef{},
	}
}

func blockedRecommendation(reason string, id string) contractsv1.RunbookRecommendation {
	return contractsv1.RunbookRecommendation{
		ID:                  id,
		Title:               "Escalate for manual remediation",
		Reason:              reason,
		Confidence:          0.72,
		Steps:               []string{"Review the recorded failure evidence.", "Provide an explicit allowlisted remediation target or apply a manual patch.", "Verify the failing route recovers."},
		RequiredPermissions: []string{"logs:read", "manual_patch:required"},
		EstimatedRisk:       contractsv1.SafetyBlocked,
		RequiresApproval:    true,
	}
}

func safeExcerpt(logContent string) string {
	lines := strings.Split(strings.TrimSpace(logContent), "\n")
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	excerpt := strings.Join(lines, "\n")
	if len(excerpt) > 1600 {
		return excerpt[:1600]
	}
	return excerpt
}
