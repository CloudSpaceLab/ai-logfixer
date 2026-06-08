package restart

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/engine"
)

const actor = "ai-logfixer-restart-resolver"

type Options struct {
	ServiceName    string
	LogPath        string
	Method         string
	Route          string
	StatusCode     int
	StatusClass    int
	ErrorThreshold int
	Window         time.Duration
	ActionName     string
	Policy         Policy
	Verification   Verification
	Now            time.Time
}

type Policy struct {
	AllowedActions []Action
}

type Action struct {
	Name        string
	ServiceName string
	Command     Command
}

type Command struct {
	Path string
	Args []string
	Dir  string
	Env  []string
}

type Verification struct {
	HTTP    *HTTPVerification
	Command *CommandVerification
}

type HTTPVerification struct {
	URL            string
	ExpectedStatus int
	BodyContains   string
}

type CommandVerification struct {
	Command        Command
	OutputContains string
}

type Result struct {
	InvestigationRequest contractsv1.InvestigationRequest
	Diagnosis            contractsv1.DiagnosisResult
	RemediationPlan      contractsv1.RemediationPlan
	Attempt              contractsv1.RemediationAttempt
	Receipt              contractsv1.Receipt
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
	action, ok := findAllowedAction(options)
	if !ok {
		reason := fmt.Sprintf("Automatic restart/reload remediation is blocked because no allowlisted restart/reload action exists for service %s and action %s.", options.ServiceName, options.ActionName)
		return buildBlockedResult(options, signal, investigationRequest, reason, string(logContent))
	}
	if err := validateCommand(action.Command); err != nil {
		reason := fmt.Sprintf("Automatic restart/reload remediation is blocked because the allowlisted command is unsafe or incomplete: %v.", err)
		return buildBlockedResult(options, signal, investigationRequest, reason, string(logContent))
	}
	if err := validateVerification(options.Verification); err != nil {
		reason := fmt.Sprintf("Automatic restart/reload remediation is blocked because verification is incomplete: %v.", err)
		return buildBlockedResult(options, signal, investigationRequest, reason, string(logContent))
	}

	beforeState := beforeEvidence(signal)
	diagnosis := buildDiagnosis(options, signal, action, string(logContent))
	plan := buildRemediationPlan(options, signal, diagnosis.ID, action, beforeState)
	if err := validatePreExecution(investigationRequest, diagnosis, plan); err != nil {
		return Result{}, err
	}

	if _, err := runCommand(ctx, action.Command); err != nil {
		return Result{}, fmt.Errorf("execute allowlisted restart/reload command: %w", err)
	}
	afterState, err := verify(ctx, options.Verification)
	if err != nil {
		return Result{}, fmt.Errorf("verify restart/reload recovery: %w", err)
	}

	plan.Status = contractsv1.RemediationStatusSucceeded
	plan.DisplayStatus = "Restart/reload applied and verified"
	plan.UserMessage = "The allowlisted restart/reload command ran and verification passed."
	plan.NextActions = []contractsv1.NextAction{}
	plan.TimelineEvents = append(plan.TimelineEvents, contractsv1.TimelineEvent{
		ID:        engine.StableID("tl_restart_plan_succeeded", plan.ID, afterState),
		Type:      "remediation.succeeded",
		Message:   "Restart/reload command verified recovery.",
		Severity:  "info",
		Timestamp: options.Now.Add(2 * time.Second),
	})
	attempt := buildAttempt(options, signal, plan.ID, action, afterState)
	receipt := buildReceipt(options, signal, diagnosis.ID, plan.ID, attempt.ID, action, beforeState, afterState)

	if err := validatePostExecution(plan, attempt, receipt); err != nil {
		return Result{}, err
	}
	return Result{
		InvestigationRequest: investigationRequest,
		Diagnosis:            diagnosis,
		RemediationPlan:      plan,
		Attempt:              attempt,
		Receipt:              receipt,
	}, nil
}

func normalizeOptions(options Options) Options {
	if strings.TrimSpace(options.ServiceName) == "" {
		options.ServiceName = "unknown-service"
	}
	if strings.TrimSpace(options.Method) == "" {
		options.Method = http.MethodGet
	}
	if options.ErrorThreshold == 0 {
		options.ErrorThreshold = 3
	}
	if options.Window == 0 {
		options.Window = 5 * time.Minute
	}
	if options.StatusCode == 0 && options.StatusClass == 0 {
		options.StatusCode = http.StatusServiceUnavailable
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}
	if strings.TrimSpace(options.ActionName) == "" && len(options.Policy.AllowedActions) == 1 {
		options.ActionName = options.Policy.AllowedActions[0].Name
	}
	return options
}

func findAllowedAction(options Options) (Action, bool) {
	for _, action := range options.Policy.AllowedActions {
		if action.Name != options.ActionName {
			continue
		}
		if action.ServiceName != options.ServiceName {
			continue
		}
		return action, true
	}
	return Action{}, false
}

func validateCommand(command Command) error {
	if strings.TrimSpace(command.Path) == "" {
		return errors.New("command path is required")
	}
	base := filepath.Base(command.Path)
	switch base {
	case "kill", "pkill", "killall", "sh", "bash", "dash", "zsh":
		return fmt.Errorf("%s is not permitted; provide a dedicated allowlisted executable or script path", base)
	}
	return nil
}

func validateVerification(verification Verification) error {
	if verification.HTTP == nil && verification.Command == nil {
		return errors.New("verify URL/status or verify command is required")
	}
	if verification.HTTP != nil && verification.Command != nil {
		return errors.New("provide either HTTP verification or command verification, not both")
	}
	if verification.HTTP != nil {
		if strings.TrimSpace(verification.HTTP.URL) == "" {
			return errors.New("verify URL is required")
		}
		return nil
	}
	if err := validateCommand(verification.Command.Command); err != nil {
		return err
	}
	return nil
}

func runCommand(ctx context.Context, command Command) (string, error) {
	cmd := exec.CommandContext(ctx, command.Path, command.Args...)
	if command.Dir != "" {
		cmd.Dir = command.Dir
	}
	if len(command.Env) > 0 {
		cmd.Env = append(os.Environ(), command.Env...)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(output)), fmt.Errorf("%s failed: %w: %s", commandLabel(command), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func verify(ctx context.Context, verification Verification) (string, error) {
	if verification.HTTP != nil {
		return verifyHTTP(ctx, *verification.HTTP)
	}
	output, err := runCommand(ctx, verification.Command.Command)
	if err != nil {
		return "", err
	}
	if verification.Command.OutputContains != "" && !strings.Contains(output, verification.Command.OutputContains) {
		return "", fmt.Errorf("verification command output did not contain %q", verification.Command.OutputContains)
	}
	if output == "" {
		output = "verification command exited successfully"
	}
	return fmt.Sprintf("command %s output=%q", commandLabel(verification.Command.Command), output), nil
}

func verifyHTTP(ctx context.Context, verification HTTPVerification) (string, error) {
	expectedStatus := verification.ExpectedStatus
	if expectedStatus == 0 {
		expectedStatus = http.StatusOK
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, verification.URL, nil)
	if err != nil {
		return "", err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return "", err
	}
	body := strings.TrimSpace(string(raw))
	if response.StatusCode != expectedStatus {
		return "", fmt.Errorf("expected HTTP %d from %s, got HTTP %d: %s", expectedStatus, verification.URL, response.StatusCode, body)
	}
	if verification.BodyContains != "" && !strings.Contains(body, verification.BodyContains) {
		return "", fmt.Errorf("HTTP verification body did not contain %q", verification.BodyContains)
	}
	return fmt.Sprintf("HTTP %d from %s body=%q", response.StatusCode, verification.URL, body), nil
}

func validatePreExecution(investigationRequest contractsv1.InvestigationRequest, diagnosis contractsv1.DiagnosisResult, plan contractsv1.RemediationPlan) error {
	if err := investigationRequest.Validate(); err != nil {
		return fmt.Errorf("validate investigation request: %w", err)
	}
	if err := diagnosis.Validate(); err != nil {
		return fmt.Errorf("validate diagnosis: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("validate remediation plan: %w", err)
	}
	return nil
}

func validatePostExecution(plan contractsv1.RemediationPlan, attempt contractsv1.RemediationAttempt, receipt contractsv1.Receipt) error {
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("validate final remediation plan: %w", err)
	}
	if err := attempt.Validate(); err != nil {
		return fmt.Errorf("validate remediation attempt: %w", err)
	}
	if err := receipt.Validate(); err != nil {
		return fmt.Errorf("validate receipt: %w", err)
	}
	return nil
}

func buildInvestigationRequest(options Options, signal engine.IncidentSignal) contractsv1.InvestigationRequest {
	factory := engine.NewContractIDFactory()
	parts := signal.StableParts()
	return contractsv1.InvestigationRequest{
		ID:              factory.ID("inv_req_restart_reload", parts...),
		ContractVersion: contractsv1.ContractVersion,
		SchemaURL:       contractsv1.InvestigationRequestSchemaURL,
		SourceType:      contractsv1.SourceTypeAutomatic,
		SourceName:      actor,
		RequestedBy:     "ai-logfixer",
		Service:         options.ServiceName,
		Symptom:         fmt.Sprintf("Repeated HTTP %s responses after config or deploy change; restart/reload may be required", signal.ErrorCode()),
		ErrorCode:       signal.ErrorCode(),
		TimeWindow: contractsv1.TimeWindow{
			Start: signal.Start,
			End:   signal.End,
		},
		SignalFingerprint: contractsv1.SignalFingerprint{
			Service:       options.ServiceName,
			Symptom:       "restart_reload_drift",
			ErrorCode:     signal.ErrorCode(),
			Source:        options.LogPath,
			DeployVersion: "unknown",
			Tags:          append([]string{"restart-reload"}, signal.Tags...),
		},
		DisplayStatus: "Restart/reload investigation started",
		UserMessage:   "I detected repeated runtime failures and checked for an explicit restart/reload policy.",
		ExternalRefs:  []contractsv1.ExternalRef{},
		KnowledgeRefs: []contractsv1.KnowledgeRef{},
		CreatedAt:     options.Now,
	}
}

func buildDiagnosis(options Options, signal engine.IncidentSignal, action Action, logContent string) contractsv1.DiagnosisResult {
	factory := engine.NewContractIDFactory()
	parts := append(signal.StableParts(), options.ActionName, commandLabel(action.Command))
	logEvidenceID := factory.ID("ev_restart_logs", parts...)
	policyEvidenceID := factory.ID("ev_restart_policy", parts...)
	patchID := factory.ID("patch_restart_action", parts...)
	rollbackID := factory.ID("rollback_restart_action", parts...)
	return contractsv1.DiagnosisResult{
		ID:                 factory.ID("diag_restart_reload", parts...),
		ContractVersion:    contractsv1.ContractVersion,
		SchemaURL:          contractsv1.DiagnosisSchemaURL,
		Status:             contractsv1.DiagnosisStatusComplete,
		Summary:            "Repeated HTTP failures match the restart/reload drift family and have an explicit allowlisted action.",
		Confidence:         0.78,
		SuspectedRootCause: "The service appears to be serving stale runtime state; an allowlisted restart/reload is required before verification.",
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
				RelatedIDs:     []string{policyEvidenceID},
				UIHints:        contractsv1.UIHints{Icon: "rotate-cw", Tone: "warning", Sections: []string{"logs", "runtime"}},
				ExternalRefs:   []contractsv1.ExternalRef{},
				KnowledgeRefs:  []contractsv1.KnowledgeRef{},
			},
			{
				ID:             policyEvidenceID,
				Type:           contractsv1.EvidenceTypeConfig,
				Source:         "restart-policy",
				Timestamp:      options.Now,
				Title:          "Allowlisted restart/reload action",
				Summary:        fmt.Sprintf("Policy allows action %q for service %s.", action.Name, action.ServiceName),
				RawExcerpt:     fmt.Sprintf("action=%s command=%s", action.Name, commandLabel(action.Command)),
				RedactionState: contractsv1.RedactionStateRedacted,
				RelatedIDs:     []string{logEvidenceID},
				UIHints:        contractsv1.UIHints{Icon: "shield-check", Tone: "info", Sections: []string{"policy"}},
				ExternalRefs:   []contractsv1.ExternalRef{},
				KnowledgeRefs:  []contractsv1.KnowledgeRef{},
			},
		},
		Recommendations: []contractsv1.RunbookRecommendation{
			{
				ID:                  factory.ID("rec_restart_reload", parts...),
				Title:               "Run allowlisted restart/reload",
				Reason:              "The operator supplied an explicit policy action and verification probe for this runtime drift family.",
				Confidence:          0.76,
				Steps:               []string{"Record the stale-state evidence.", "Execute only the allowlisted restart/reload command.", "Verify recovery with the configured probe.", "Record that restart rollback is unavailable."},
				RequiredPermissions: []string{"service:restart_reload", "service:verify"},
				EstimatedRisk:       contractsv1.SafetyMediumRisk,
				RequiresApproval:    false,
			},
		},
		PatchPlan: &contractsv1.PatchPlan{
			ID:         patchID,
			TargetType: contractsv1.PatchTargetRuntimeSetting,
			TargetRefs: []string{options.ServiceName, action.Name},
			DiffPreview: contractsv1.DiffPreview{
				Before: beforeEvidence(signal),
				After:  "execute allowlisted " + action.Name + " for " + options.ServiceName,
			},
			RiskLevel:        contractsv1.SafetyMediumRisk,
			RequiresApproval: false,
			BlockedReasons:   []string{},
		},
		RollbackPlan:         restartRollbackPlan(rollbackID),
		SafetyClassification: contractsv1.SafetyMediumRisk,
		DisplayStatus:        "Allowlisted restart/reload diagnosed",
		UserMessage:          "I found repeated stale-runtime failures and an explicit restart/reload policy action.",
		NextActions:          []contractsv1.NextAction{{ID: "next_execute_restart_reload", Label: "Run restart/reload", ActionType: "execute_remediation", Description: "Execute the allowlisted command and verify recovery.", Enabled: true}},
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: factory.ID("tl_restart_diag", parts...), Type: "diagnosis.completed", Message: "Restart/reload diagnosis completed.", Severity: "info", Timestamp: options.Now}},
		ExternalRefs:         []contractsv1.ExternalRef{},
		KnowledgeRefs:        []contractsv1.KnowledgeRef{},
		CreatedAt:            options.Now,
	}
}

func buildRemediationPlan(options Options, signal engine.IncidentSignal, diagnosisID string, action Action, beforeState string) contractsv1.RemediationPlan {
	factory := engine.NewContractIDFactory()
	parts := append(signal.StableParts(), diagnosisID, action.Name, commandLabel(action.Command))
	return contractsv1.RemediationPlan{
		ID:                factory.ID("rem_plan_restart_reload", parts...),
		ContractVersion:   contractsv1.ContractVersion,
		SchemaURL:         contractsv1.RemediationPlanSchemaURL,
		DiagnosisResultID: diagnosisID,
		Summary:           fmt.Sprintf("Execute allowlisted restart/reload action %q for %s and verify recovery.", action.Name, options.ServiceName),
		FixPreview: contractsv1.DiffPreview{
			Before: beforeState,
			After:  "verification probe healthy after " + action.Name,
		},
		RollbackPlan:     *restartRollbackPlan(factory.ID("rollback_restart_plan", parts...)),
		RiskLevel:        contractsv1.SafetyMediumRisk,
		ApprovalRequired: false,
		Status:           contractsv1.RemediationStatusApproved,
		DisplayStatus:    "Allowlisted restart/reload approved automatically",
		UserMessage:      "The restart/reload action is constrained to an explicit policy command and verification probe.",
		NextActions:      []contractsv1.NextAction{{ID: "next_run_restart_reload", Label: "Run restart/reload", ActionType: "execute_remediation", Description: "Run the allowlisted command and verify the service.", Enabled: true}},
		TimelineEvents:   []contractsv1.TimelineEvent{{ID: factory.ID("tl_restart_plan", parts...), Type: "remediation.plan_created", Message: "Restart/reload remediation plan created.", Severity: "info", Timestamp: options.Now}},
		ExternalRefs:     []contractsv1.ExternalRef{},
		KnowledgeRefs:    []contractsv1.KnowledgeRef{},
		CreatedAt:        options.Now,
	}
}

func buildAttempt(options Options, signal engine.IncidentSignal, planID string, action Action, afterState string) contractsv1.RemediationAttempt {
	factory := engine.NewContractIDFactory()
	parts := append(signal.StableParts(), planID, action.Name)
	started := options.Now.Add(time.Second)
	finished := options.Now.Add(2 * time.Second)
	return contractsv1.RemediationAttempt{
		ID:                  factory.ID("rem_attempt_restart_reload", parts...),
		ContractVersion:     contractsv1.ContractVersion,
		SchemaURL:           contractsv1.RemediationAttemptSchemaURL,
		RemediationPlanID:   planID,
		ApprovalRequestID:   "policy_allowlisted_restart_reload",
		Status:              contractsv1.RemediationStatusSucceeded,
		ExecutionStartedAt:  &started,
		ExecutionFinishedAt: &finished,
		MonitorSummary: contractsv1.MonitorSummary{
			Status:   "healthy",
			Message:  afterState,
			Signals:  []string{"restart_reload_executed=true", "verification_passed=true", "action=" + action.Name},
			Duration: "1s",
		},
		DisplayStatus:  "Restart/reload applied and verified",
		UserMessage:    "I ran the allowlisted restart/reload command and verified the service recovered.",
		TimelineEvents: []contractsv1.TimelineEvent{{ID: factory.ID("tl_restart_attempt", parts...), Type: "remediation.succeeded", Message: "Restart/reload command applied and verified.", Severity: "info", Timestamp: finished}},
		ExternalRefs:   []contractsv1.ExternalRef{},
	}
}

func buildReceipt(options Options, signal engine.IncidentSignal, diagnosisID string, planID string, attemptID string, action Action, beforeState string, afterState string) contractsv1.Receipt {
	factory := engine.NewContractIDFactory()
	parts := append(signal.StableParts(), diagnosisID, planID, attemptID, action.Name)
	return contractsv1.Receipt{
		ID:                   factory.ID("receipt_restart_reload", parts...),
		DiagnosisID:          diagnosisID,
		RemediationPlanID:    planID,
		RemediationAttemptID: attemptID,
		ActionTaken:          "executed allowlisted restart/reload command",
		Actor:                actor,
		Approver:             "policy_allowlisted_restart_reload",
		Timestamp:            options.Now.Add(3 * time.Second),
		BeforeState:          beforeState,
		AfterState:           afterState,
		Outcome:              "succeeded",
		Summary:              "AI LogFixer detected stale runtime state, executed only the allowlisted restart/reload command, verified recovery, and recorded that the restart itself cannot be undone.",
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: factory.ID("tl_restart_receipt", parts...), Type: "receipt.created", Message: "Receipt recorded for successful restart/reload remediation.", Severity: "info", Timestamp: options.Now.Add(3 * time.Second)}},
		ExternalRefs:         []contractsv1.ExternalRef{},
		KnowledgeRefs:        []contractsv1.KnowledgeRef{},
	}
}

func buildBlockedResult(options Options, signal engine.IncidentSignal, investigationRequest contractsv1.InvestigationRequest, reason string, logContent string) (Result, error) {
	factory := engine.NewContractIDFactory()
	parts := append(signal.StableParts(), reason)
	diagnosisID := factory.ID("diag_blocked_restart_reload", parts...)
	diagnosis := contractsv1.DiagnosisResult{
		ID:                   diagnosisID,
		ContractVersion:      contractsv1.ContractVersion,
		SchemaURL:            contractsv1.DiagnosisSchemaURL,
		Status:               contractsv1.DiagnosisStatusComplete,
		Summary:              "Repeated HTTP failures were detected, but automatic restart/reload remediation is blocked.",
		Confidence:           0.72,
		SuspectedRootCause:   "AI LogFixer has stale-runtime evidence but no complete safe restart/reload policy.",
		AffectedServices:     []string{options.ServiceName},
		EvidenceItems:        []contractsv1.EvidenceItem{blockedLogEvidence(options, signal, logContent, factory.ID("ev_blocked_restart_logs", parts...))},
		Recommendations:      []contractsv1.RunbookRecommendation{blockedRecommendation(reason, factory.ID("rec_blocked_restart", parts...))},
		SafetyClassification: contractsv1.SafetyBlocked,
		DisplayStatus:        "Automatic restart/reload blocked",
		UserMessage:          reason,
		NextActions:          []contractsv1.NextAction{{ID: "next_manual_restart_review", Label: "Review restart policy", ActionType: "manual_review", Description: "Provide an explicit allowlisted restart/reload command and verification probe.", Enabled: true}},
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: factory.ID("tl_blocked_restart_diag", parts...), Type: "diagnosis.completed", Message: "Restart/reload remediation blocked by safety policy.", Severity: "warning", Timestamp: options.Now}},
		ExternalRefs:         []contractsv1.ExternalRef{},
		KnowledgeRefs:        []contractsv1.KnowledgeRef{},
		CreatedAt:            options.Now,
	}
	builder := engine.BlockedPlanBuilder{IDFactory: factory, Now: options.Now, Actor: actor}
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
		UIHints:        contractsv1.UIHints{Icon: "file-warning", Tone: "danger", Sections: []string{"logs", "runtime"}},
		ExternalRefs:   []contractsv1.ExternalRef{},
		KnowledgeRefs:  []contractsv1.KnowledgeRef{},
	}
}

func blockedRecommendation(reason string, id string) contractsv1.RunbookRecommendation {
	return contractsv1.RunbookRecommendation{
		ID:                  id,
		Title:               "Escalate for manual restart/reload review",
		Reason:              reason,
		Confidence:          0.72,
		Steps:               []string{"Review the recorded stale-runtime evidence.", "Provide an explicit allowlisted restart/reload command.", "Provide an explicit verification URL/status or verification command."},
		RequiredPermissions: []string{"logs:read", "restart_policy:write"},
		EstimatedRisk:       contractsv1.SafetyBlocked,
		RequiresApproval:    true,
	}
}

func restartRollbackPlan(id string) *contractsv1.RollbackPlan {
	return &contractsv1.RollbackPlan{
		ID:                   id,
		RollbackType:         contractsv1.RollbackUnavailable,
		SnapshotRefs:         []string{},
		RestoreSteps:         []string{},
		Limitations:          []string{"A restart/reload changes process runtime state and cannot be undone by AI LogFixer; rollback is limited to follow-up diagnosis or a new allowlisted action."},
		RiskLevel:            contractsv1.SafetyMediumRisk,
		RequiresManualReview: false,
	}
}

func beforeEvidence(signal engine.IncidentSignal) string {
	return fmt.Sprintf("HTTP %s x%d for %s from %s", signal.ErrorCode(), signal.Count, signal.RouteLabel(), signal.Source)
}

func commandLabel(command Command) string {
	if len(command.Args) == 0 {
		return command.Path
	}
	return command.Path + " " + strings.Join(redactArgs(command.Args), " ")
}

func redactArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.Contains(strings.ToLower(arg), "token") || strings.Contains(strings.ToLower(arg), "password") || strings.Contains(strings.ToLower(arg), "secret") {
			out = append(out, "<redacted>")
			continue
		}
		out = append(out, arg)
	}
	return out
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
