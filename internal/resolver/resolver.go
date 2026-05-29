package resolver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CloudSpaceLab/ai-logfixer/internal/agentfix"
	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/engine"
)

type Options struct {
	ServiceName        string
	TargetDir          string
	StackTrace         string
	Message            string
	Apply              bool
	ValidationCommands []string
	AgentCommand       string
	AgentModel         string
	AgentName          string
	KeepAgentWorkdir   bool
	MaxChangedFiles    int
	Now                time.Time
	AgentRunner        agentfix.AgentRunner
}

type Result struct {
	Profile              StackProfile                     `json:"profile"`
	StackTrace           ParsedStackTrace                 `json:"stack_trace"`
	SourceOwner          SourceOwner                      `json:"source_owner"`
	Context              ContextBundle                    `json:"context"`
	InvestigationRequest contractsv1.InvestigationRequest `json:"investigation_request"`
	Diagnosis            contractsv1.DiagnosisResult      `json:"diagnosis"`
	RemediationPlan      contractsv1.RemediationPlan      `json:"remediation_plan"`
	Attempt              contractsv1.RemediationAttempt   `json:"attempt"`
	Receipt              contractsv1.Receipt              `json:"receipt"`
	AgentResult          *agentfix.Result                 `json:"agent_result,omitempty"`
}

type StackProfile struct {
	Language        string   `json:"language"`
	Framework       string   `json:"framework"`
	PackageManager  string   `json:"package_manager"`
	DependencyFiles []string `json:"dependency_files"`
	Entrypoints     []string `json:"entrypoints"`
}

type ParsedStackTrace struct {
	Raw    string       `json:"raw"`
	Frames []StackFrame `json:"frames"`
}

type StackFrame struct {
	Function string `json:"function"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Language string `json:"language"`
}

type SourceOwner struct {
	File       string  `json:"file"`
	Function   string  `json:"function"`
	Line       int     `json:"line"`
	Language   string  `json:"language"`
	Framework  string  `json:"framework"`
	Confidence float64 `json:"confidence"`
}

type ContextBundle struct {
	SourceExcerpt   string            `json:"source_excerpt"`
	DependencyFiles map[string]string `json:"dependency_files"`
	ConfigFiles     map[string]string `json:"config_files"`
}

func Run(ctx context.Context, options Options) (Result, error) {
	if strings.TrimSpace(options.TargetDir) == "" {
		return Result{}, errors.New("target directory is required")
	}
	targetDir, err := filepath.Abs(filepath.Clean(options.TargetDir))
	if err != nil {
		return Result{}, fmt.Errorf("resolve target directory: %w", err)
	}
	options.TargetDir = targetDir
	if strings.TrimSpace(options.ServiceName) == "" {
		options.ServiceName = filepath.Base(targetDir)
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}

	profile, err := ProfileTarget(targetDir)
	if err != nil {
		return Result{}, err
	}
	trace := ParseStackTrace(options.StackTrace, profile.Language)
	owner := SourceOwnerFromTrace(trace, profile)
	contextBundle := BuildContext(targetDir, owner, profile)
	ids := engine.NewContractIDFactory()
	request := buildInvestigationRequest(ids, options, profile)
	diagnosis := buildDiagnosis(ids, options, profile, trace, owner, contextBundle)
	result := Result{
		Profile:              profile,
		StackTrace:           trace,
		SourceOwner:          owner,
		Context:              contextBundle,
		InvestigationRequest: request,
		Diagnosis:            diagnosis,
	}

	if owner.File == "" {
		plan, attempt, receipt := buildEscalatedContracts(ids, options, diagnosis.ID, "AI LogFixer could not identify an application source frame from the available stack trace.")
		result.RemediationPlan = plan
		result.Attempt = attempt
		result.Receipt = receipt
		return result, nil
	}
	if options.AgentRunner == nil && strings.TrimSpace(options.AgentCommand) == "" {
		plan, attempt, receipt := buildEscalatedContracts(ids, options, diagnosis.ID, "No AI agent command or runner was configured, so AI LogFixer stopped after diagnosis.")
		result.RemediationPlan = plan
		result.Attempt = attempt
		result.Receipt = receipt
		return result, nil
	}

	agentResult, runErr := agentfix.Run(ctx, agentfix.Options{
		TargetDir:          targetDir,
		Prompt:             BuildAIPrompt(options, profile, trace, owner, contextBundle),
		AgentCommand:       options.AgentCommand,
		AgentModel:         options.AgentModel,
		AgentName:          options.AgentName,
		ValidationCommands: options.ValidationCommands,
		ExcludePaths:       defaultExcludePaths(),
		Apply:              options.Apply,
		Now:                options.Now,
		KeepWorkdir:        options.KeepAgentWorkdir,
		MaxChangedFiles:    options.MaxChangedFiles,
		AutoPHPLint:        profile.Language == "php",
		AgentRunner:        options.AgentRunner,
	})
	result.AgentResult = &agentResult
	if runErr != nil {
		plan, attempt, receipt := buildFailedContracts(ids, options, diagnosis.ID, "AI agent execution failed: "+runErr.Error())
		result.RemediationPlan = plan
		result.Attempt = attempt
		result.Receipt = receipt
		return result, nil
	}
	if len(agentResult.Changes) == 0 {
		plan, attempt, receipt := buildEscalatedContracts(ids, options, diagnosis.ID, "AI agent completed without producing a patch.")
		result.RemediationPlan = plan
		result.Attempt = attempt
		result.Receipt = receipt
		return result, nil
	}
	if !agentResult.ValidationPassed {
		plan, attempt, receipt := buildFailedContracts(ids, options, diagnosis.ID, "AI agent produced a patch, but validation failed in the sandbox.")
		result.RemediationPlan = plan
		result.Attempt = attempt
		result.Receipt = receipt
		return result, nil
	}
	if !options.Apply {
		plan, attempt, receipt := buildDryRunContracts(ids, options, diagnosis.ID, agentResult)
		result.RemediationPlan = plan
		result.Attempt = attempt
		result.Receipt = receipt
		return result, nil
	}
	if !agentResult.Applied {
		plan, attempt, receipt := buildFailedContracts(ids, options, diagnosis.ID, "AI agent patch validated in the sandbox but was not applied to the target.")
		result.RemediationPlan = plan
		result.Attempt = attempt
		result.Receipt = receipt
		return result, nil
	}

	plan, attempt, receipt := buildSucceededContracts(ids, options, diagnosis.ID, agentResult)
	result.RemediationPlan = plan
	result.Attempt = attempt
	result.Receipt = receipt
	return result, nil
}

func ProfileTarget(targetDir string) (StackProfile, error) {
	var profile StackProfile
	addDependency := func(path string) {
		if _, err := os.Stat(filepath.Join(targetDir, path)); err == nil {
			profile.DependencyFiles = append(profile.DependencyFiles, path)
		}
	}
	addEntrypoint := func(path string) {
		if _, err := os.Stat(filepath.Join(targetDir, path)); err == nil {
			profile.Entrypoints = append(profile.Entrypoints, path)
		}
	}

	switch {
	case exists(targetDir, "composer.json"):
		profile.Language = "php"
		profile.PackageManager = "composer"
		addDependency("composer.json")
		addEntrypoint("artisan")
		if exists(targetDir, "artisan") || exists(targetDir, "app/Http/Controllers") {
			profile.Framework = "laravel"
		}
	case exists(targetDir, "pyproject.toml") || exists(targetDir, "requirements.txt") || exists(targetDir, "manage.py"):
		profile.Language = "python"
		profile.PackageManager = pythonPackageManager(targetDir)
		addDependency("pyproject.toml")
		addDependency("requirements.txt")
		addEntrypoint("manage.py")
		if exists(targetDir, "manage.py") {
			profile.Framework = "django"
		} else {
			profile.Framework = "fastapi"
		}
	case exists(targetDir, "package.json"):
		profile.Language = "node"
		profile.PackageManager = "npm"
		addDependency("package.json")
		addDependency("pnpm-lock.yaml")
		addDependency("yarn.lock")
		profile.Framework = nodeFramework(targetDir)
	case exists(targetDir, "Gemfile"):
		profile.Language = "ruby"
		profile.PackageManager = "bundler"
		addDependency("Gemfile")
		addDependency("Gemfile.lock")
		addEntrypoint("config/routes.rb")
		if exists(targetDir, "config/routes.rb") || exists(targetDir, "app/controllers") {
			profile.Framework = "rails"
		}
	case exists(targetDir, "go.mod"):
		profile.Language = "go"
		profile.PackageManager = "go"
		addDependency("go.mod")
		if exists(targetDir, "routes/web.go") {
			profile.Framework = "goravel"
		}
	default:
		return StackProfile{}, fmt.Errorf("could not profile target stack at %s", targetDir)
	}
	if profile.Framework == "" {
		profile.Framework = profile.Language
	}
	sort.Strings(profile.DependencyFiles)
	sort.Strings(profile.Entrypoints)
	return profile, nil
}

func ParseStackTrace(raw string, language string) ParsedStackTrace {
	trace := ParsedStackTrace{Raw: raw}
	parsers := []func(string) []StackFrame{
		parsePythonFrames,
		parseNodeFrames,
		parseRubyFrames,
		parsePHPFrames,
		parseGoFrames,
	}
	for _, parser := range parsers {
		for _, frame := range parser(raw) {
			if frame.Language == "" {
				frame.Language = language
			}
			trace.Frames = append(trace.Frames, frame)
		}
	}
	trace.Frames = dedupeFrames(trace.Frames)
	return trace
}

func SourceOwnerFromTrace(trace ParsedStackTrace, profile StackProfile) SourceOwner {
	for _, frame := range trace.Frames {
		if isApplicationFrame(frame.File) {
			return SourceOwner{
				File:       filepath.Clean(frame.File),
				Function:   frame.Function,
				Line:       frame.Line,
				Language:   profile.Language,
				Framework:  profile.Framework,
				Confidence: 0.88,
			}
		}
	}
	if len(trace.Frames) == 0 {
		return SourceOwner{Language: profile.Language, Framework: profile.Framework}
	}
	first := trace.Frames[0]
	return SourceOwner{
		File:       filepath.Clean(first.File),
		Function:   first.Function,
		Line:       first.Line,
		Language:   profile.Language,
		Framework:  profile.Framework,
		Confidence: 0.62,
	}
}

func BuildContext(targetDir string, owner SourceOwner, profile StackProfile) ContextBundle {
	bundle := ContextBundle{
		DependencyFiles: map[string]string{},
		ConfigFiles:     map[string]string{},
	}
	if owner.File != "" {
		if raw, err := os.ReadFile(filepath.Clean(owner.File)); err == nil {
			bundle.SourceExcerpt = excerptAroundLine(string(raw), owner.Line, 12)
		}
	}
	for _, rel := range profile.DependencyFiles {
		if raw, err := os.ReadFile(filepath.Join(targetDir, filepath.FromSlash(rel))); err == nil {
			bundle.DependencyFiles[rel] = redactSecrets(string(raw))
		}
	}
	for _, rel := range []string{".env", "config/database.php", "config/app.php", "package.json", "pyproject.toml", "Gemfile"} {
		path := filepath.Join(targetDir, filepath.FromSlash(rel))
		if raw, err := os.ReadFile(path); err == nil {
			bundle.ConfigFiles[rel] = redactSecrets(string(raw))
		}
	}
	return bundle
}

func BuildAIPrompt(options Options, profile StackProfile, trace ParsedStackTrace, owner SourceOwner, contextBundle ContextBundle) string {
	return strings.TrimSpace(fmt.Sprintf(`AI LogFixer universal resolver task

Service: %s
Language: %s
Framework: %s
Message: %s
Source owner: %s:%d %s

Goal:
- Identify the smallest safe fix for the observed failure.
- Modify only the staging copy.
- Preserve behavior outside the failing path.
- Do not touch secrets, generated dependency folders, or unrelated files.
- Leave the app in a state that passes the validation commands.

Stack trace:
%s

Source excerpt:
%s
`, options.ServiceName, profile.Language, profile.Framework, redactSecrets(options.Message), owner.File, owner.Line, owner.Function, redactSecrets(trace.Raw), contextBundle.SourceExcerpt))
}

func buildInvestigationRequest(ids engine.ContractIDFactory, options Options, profile StackProfile) contractsv1.InvestigationRequest {
	start := options.Now.Add(-5 * time.Minute)
	return contractsv1.InvestigationRequest{
		ID:              ids.ID("inv_req_universal_resolver", options.ServiceName, profile.Language, profile.Framework, options.Message),
		ContractVersion: contractsv1.ContractVersion,
		SchemaURL:       contractsv1.InvestigationRequestSchemaURL,
		SourceType:      contractsv1.SourceTypeAutomatic,
		SourceName:      "ai-logfixer-universal-resolver",
		RequestedBy:     "ai-logfixer",
		Service:         options.ServiceName,
		Symptom:         "runtime_failure",
		ErrorCode:       profile.Language + "_" + profile.Framework + "_runtime_error",
		TimeWindow:      contractsv1.TimeWindow{Start: start, End: options.Now},
		SignalFingerprint: contractsv1.SignalFingerprint{
			Service:   options.ServiceName,
			Symptom:   "runtime_failure",
			ErrorCode: profile.Language + "_" + profile.Framework + "_runtime_error",
			Source:    "stack_trace",
			Tags:      []string{profile.Language, profile.Framework},
		},
		DisplayStatus: "Investigation requested",
		UserMessage:   "AI LogFixer detected a runtime failure and started a universal resolver investigation.",
		CreatedAt:     options.Now,
	}
}

func buildDiagnosis(ids engine.ContractIDFactory, options Options, profile StackProfile, trace ParsedStackTrace, owner SourceOwner, contextBundle ContextBundle) contractsv1.DiagnosisResult {
	diagnosisID := ids.ID("diag_universal_resolver", options.ServiceName, owner.File, options.Message)
	evidence := []contractsv1.EvidenceItem{
		{
			ID:             ids.ID("ev_stack_trace", options.ServiceName, options.Message),
			Type:           contractsv1.EvidenceTypeTrace,
			Source:         "stack_trace",
			Timestamp:      options.Now,
			Title:          "Runtime stack trace",
			Summary:        "AI LogFixer parsed the runtime failure stack trace.",
			RawExcerpt:     truncate(redactSecrets(trace.Raw), 1800),
			RedactionState: contractsv1.RedactionStateRedacted,
			UIHints:        contractsv1.UIHints{Icon: "trace", Tone: "warning", Sections: []string{"evidence"}},
		},
	}
	if owner.File != "" {
		evidence = append(evidence, contractsv1.EvidenceItem{
			ID:             ids.ID("ev_source_owner", owner.File, owner.Function),
			Type:           contractsv1.EvidenceTypeConfig,
			Source:         owner.File,
			Timestamp:      options.Now,
			Title:          "Likely owning source file",
			Summary:        fmt.Sprintf("The likely application frame is %s:%d.", owner.File, owner.Line),
			RawExcerpt:     truncate(redactSecrets(contextBundle.SourceExcerpt), 1800),
			RedactionState: contractsv1.RedactionStateRedacted,
			UIHints:        contractsv1.UIHints{Icon: "file", Tone: "info", Sections: []string{"source"}},
		})
	}
	status := contractsv1.DiagnosisStatusComplete
	root := "Runtime failure localized to the application source frame."
	if owner.File == "" {
		status = contractsv1.DiagnosisStatusNeedsMoreData
		root = "AI LogFixer could not localize the runtime failure to an application source frame."
	}
	return contractsv1.DiagnosisResult{
		ID:                   diagnosisID,
		ContractVersion:      contractsv1.ContractVersion,
		SchemaURL:            contractsv1.DiagnosisSchemaURL,
		Status:               status,
		Summary:              fmt.Sprintf("%s/%s runtime failure diagnosed by universal resolver.", profile.Language, profile.Framework),
		Confidence:           owner.Confidence,
		SuspectedRootCause:   root,
		AffectedServices:     []string{options.ServiceName},
		EvidenceItems:        evidence,
		Recommendations:      []contractsv1.RunbookRecommendation{resolverRecommendation(ids, owner.File != "")},
		SafetyClassification: contractsv1.SafetyMediumRisk,
		DisplayStatus:        "Diagnosis complete",
		UserMessage:          "AI LogFixer collected stack and source context for the failing runtime path.",
		NextActions:          []contractsv1.NextAction{{ID: "next_run_ai_patch_sandbox", Label: "Run AI sandbox repair", ActionType: "run_sandbox_remediation", Description: "Ask the AI repair agent to patch a staging copy and run validations.", Enabled: owner.File != ""}},
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: ids.ID("tl_diag", diagnosisID), Type: "diagnosis.completed", Message: "Universal resolver diagnosis completed.", Severity: "info", Timestamp: options.Now}},
		CreatedAt:            options.Now,
	}
}

func resolverRecommendation(ids engine.ContractIDFactory, enabled bool) contractsv1.RunbookRecommendation {
	risk := contractsv1.SafetyMediumRisk
	return contractsv1.RunbookRecommendation{
		ID:                  ids.ID("rec_universal_ai_patch", strconv.FormatBool(enabled)),
		Title:               "Use AI sandbox remediation",
		Reason:              "The issue is localized enough for an AI agent to attempt a patch in a staging copy before any target changes.",
		Confidence:          0.78,
		Steps:               []string{"Create a staging copy", "Run AI repair agent", "Validate changed files", "Apply only if sandbox validation passes"},
		RequiredPermissions: []string{"read_source", "write_staging_copy", "run_validation"},
		EstimatedRisk:       risk,
		RequiresApproval:    true,
	}
}

func buildSucceededContracts(ids engine.ContractIDFactory, options Options, diagnosisID string, agentResult agentfix.Result) (contractsv1.RemediationPlan, contractsv1.RemediationAttempt, contractsv1.Receipt) {
	planID := ids.ID("rem_plan_universal_succeeded", diagnosisID)
	started := options.Now.Add(1 * time.Second)
	finished := options.Now.Add(3 * time.Second)
	plan := basePlan(ids, options, diagnosisID, planID, contractsv1.RemediationStatusSucceeded, contractsv1.SafetyMediumRisk, false, "AI sandbox patch validated and was applied to the target.", agentResult)
	attempt := contractsv1.RemediationAttempt{
		ID:                  ids.ID("rem_attempt_universal_succeeded", planID),
		ContractVersion:     contractsv1.ContractVersion,
		SchemaURL:           contractsv1.RemediationAttemptSchemaURL,
		RemediationPlanID:   planID,
		ApprovalRequestID:   "approved_universal_resolver_policy",
		Status:              contractsv1.RemediationStatusSucceeded,
		ExecutionStartedAt:  &started,
		ExecutionFinishedAt: &finished,
		MonitorSummary:      contractsv1.MonitorSummary{Status: "passed", Message: "Sandbox validation passed and the target patch was applied.", Signals: []string{"validation_passed", "patch_applied"}, Duration: "2s"},
		DisplayStatus:       "Remediation succeeded",
		UserMessage:         "AI LogFixer applied the validated sandbox patch and recorded rollback metadata.",
		TimelineEvents:      []contractsv1.TimelineEvent{{ID: ids.ID("tl_attempt_success", planID), Type: "remediation.succeeded", Message: "Universal resolver remediation succeeded.", Severity: "info", Timestamp: finished}},
	}
	receipt := contractsv1.Receipt{
		ID:                   ids.ID("receipt_universal_succeeded", planID),
		DiagnosisID:          diagnosisID,
		RemediationPlanID:    planID,
		RemediationAttemptID: attempt.ID,
		ActionTaken:          "Applied AI-generated patch after sandbox validation.",
		Actor:                "ai-logfixer-universal-resolver",
		Approver:             "approved_universal_resolver_policy",
		Timestamp:            finished,
		BeforeState:          "Runtime failure observed.",
		AfterState:           "Patch applied; validations passed; rollback manifest recorded.",
		Outcome:              "succeeded",
		Summary:              "AI LogFixer applied a sandbox-validated patch and saved rollback metadata.",
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: ids.ID("tl_receipt_success", planID), Type: "receipt.created", Message: "Universal resolver receipt recorded.", Severity: "info", Timestamp: finished}},
	}
	return plan, attempt, receipt
}

func buildDryRunContracts(ids engine.ContractIDFactory, options Options, diagnosisID string, agentResult agentfix.Result) (contractsv1.RemediationPlan, contractsv1.RemediationAttempt, contractsv1.Receipt) {
	planID := ids.ID("rem_plan_universal_dry_run", diagnosisID)
	plan := basePlan(ids, options, diagnosisID, planID, contractsv1.RemediationStatusAwaitingApproval, contractsv1.SafetyMediumRisk, true, "AI sandbox patch validated but was not applied.", agentResult)
	attempt, receipt := baseTerminalAttemptAndReceipt(ids, options, diagnosisID, planID, contractsv1.RemediationStatusSucceeded, "dry_run", "AI patch validated in sandbox; target unchanged.")
	return plan, attempt, receipt
}

func buildEscalatedContracts(ids engine.ContractIDFactory, options Options, diagnosisID string, reason string) (contractsv1.RemediationPlan, contractsv1.RemediationAttempt, contractsv1.Receipt) {
	planID := ids.ID("rem_plan_universal_escalated", diagnosisID, reason)
	plan := blockedPlan(ids, options, diagnosisID, planID, contractsv1.RemediationStatusEscalated, reason)
	attempt, receipt := baseTerminalAttemptAndReceipt(ids, options, diagnosisID, planID, contractsv1.RemediationStatusEscalated, "escalated", reason)
	return plan, attempt, receipt
}

func buildFailedContracts(ids engine.ContractIDFactory, options Options, diagnosisID string, reason string) (contractsv1.RemediationPlan, contractsv1.RemediationAttempt, contractsv1.Receipt) {
	planID := ids.ID("rem_plan_universal_failed", diagnosisID, reason)
	plan := blockedPlan(ids, options, diagnosisID, planID, contractsv1.RemediationStatusFailed, reason)
	attempt, receipt := baseTerminalAttemptAndReceipt(ids, options, diagnosisID, planID, contractsv1.RemediationStatusFailed, "failed", reason)
	return plan, attempt, receipt
}

func basePlan(ids engine.ContractIDFactory, options Options, diagnosisID string, planID string, status contractsv1.RemediationStatus, risk contractsv1.SafetyClassification, approval bool, message string, agentResult agentfix.Result) contractsv1.RemediationPlan {
	changed := changedPaths(agentResult.Changes)
	if len(changed) == 0 {
		changed = []string{"sandbox_patch"}
	}
	return contractsv1.RemediationPlan{
		ID:                planID,
		ContractVersion:   contractsv1.ContractVersion,
		SchemaURL:         contractsv1.RemediationPlanSchemaURL,
		DiagnosisResultID: diagnosisID,
		Summary:           message,
		FixPreview:        contractsv1.DiffPreview{Before: "Runtime failure observed before remediation.", After: "AI-generated patch passed sandbox validation."},
		RollbackPlan: contractsv1.RollbackPlan{
			ID:           ids.ID("rollback_universal", planID),
			RollbackType: contractsv1.RollbackSnapshot,
			SnapshotRefs: []string{agentResult.ManifestPath},
			RestoreSteps: []string{"Run ai-logfixer-rollback with the generated manifest.", agentResult.RollbackCommand},
			RiskLevel:    contractsv1.SafetyLowRisk,
		},
		RiskLevel:        risk,
		ApprovalRequired: approval,
		Status:           status,
		DisplayStatus:    string(status),
		UserMessage:      message,
		NextActions:      []contractsv1.NextAction{{ID: "next_review_receipt", Label: "Review receipt", ActionType: "review_receipt", Description: "Review validation results, changes, and rollback metadata.", Enabled: true}},
		TimelineEvents:   []contractsv1.TimelineEvent{{ID: ids.ID("tl_plan", planID), Type: "remediation.plan_created", Message: message, Severity: "info", Timestamp: options.Now}},
		CreatedAt:        options.Now,
	}
}

func blockedPlan(ids engine.ContractIDFactory, options Options, diagnosisID string, planID string, status contractsv1.RemediationStatus, reason string) contractsv1.RemediationPlan {
	return contractsv1.RemediationPlan{
		ID:                planID,
		ContractVersion:   contractsv1.ContractVersion,
		SchemaURL:         contractsv1.RemediationPlanSchemaURL,
		DiagnosisResultID: diagnosisID,
		Summary:           reason,
		FixPreview:        contractsv1.DiffPreview{Before: "Runtime failure observed.", After: "No patch applied; remediation requires more capability or approval."},
		RollbackPlan: contractsv1.RollbackPlan{
			ID:           ids.ID("rollback_universal_blocked", planID),
			RollbackType: contractsv1.RollbackUnavailable,
			Limitations:  []string{"No target patch was applied, so AI LogFixer has no generated change to roll back."},
			RiskLevel:    contractsv1.SafetyBlocked,
		},
		RiskLevel:        contractsv1.SafetyBlocked,
		ApprovalRequired: false,
		Status:           status,
		DisplayStatus:    string(status),
		UserMessage:      reason,
		NextActions:      []contractsv1.NextAction{{ID: "next_configure_ai_agent", Label: "Configure AI agent", ActionType: "configure_agent", Description: "Provide an AI agent command or integration to attempt sandbox remediation.", Enabled: true}},
		TimelineEvents:   []contractsv1.TimelineEvent{{ID: ids.ID("tl_blocked_plan", planID), Type: "remediation.escalated", Message: reason, Severity: "warning", Timestamp: options.Now}},
		CreatedAt:        options.Now,
	}
}

func baseTerminalAttemptAndReceipt(ids engine.ContractIDFactory, options Options, diagnosisID string, planID string, status contractsv1.RemediationStatus, outcome string, reason string) (contractsv1.RemediationAttempt, contractsv1.Receipt) {
	started := options.Now.Add(1 * time.Second)
	finished := options.Now.Add(2 * time.Second)
	attempt := contractsv1.RemediationAttempt{
		ID:                  ids.ID("rem_attempt_universal", planID, outcome),
		ContractVersion:     contractsv1.ContractVersion,
		SchemaURL:           contractsv1.RemediationAttemptSchemaURL,
		RemediationPlanID:   planID,
		ApprovalRequestID:   "universal_resolver_policy",
		Status:              status,
		ExecutionStartedAt:  &started,
		ExecutionFinishedAt: &finished,
		MonitorSummary:      contractsv1.MonitorSummary{Status: outcome, Message: reason, Signals: []string{outcome}, Duration: "1s"},
		DisplayStatus:       string(status),
		UserMessage:         reason,
		TimelineEvents:      []contractsv1.TimelineEvent{{ID: ids.ID("tl_attempt", planID, outcome), Type: "remediation." + outcome, Message: reason, Severity: "warning", Timestamp: finished}},
	}
	receipt := contractsv1.Receipt{
		ID:                   ids.ID("receipt_universal", planID, outcome),
		DiagnosisID:          diagnosisID,
		RemediationPlanID:    planID,
		RemediationAttemptID: attempt.ID,
		ActionTaken:          reason,
		Actor:                "ai-logfixer-universal-resolver",
		Approver:             "universal_resolver_policy",
		Timestamp:            finished,
		BeforeState:          "Runtime failure observed.",
		AfterState:           reason,
		Outcome:              outcome,
		Summary:              reason,
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: ids.ID("tl_receipt", planID, outcome), Type: "receipt.created", Message: "Universal resolver receipt recorded.", Severity: "info", Timestamp: finished}},
	}
	return attempt, receipt
}

func parsePythonFrames(raw string) []StackFrame {
	re := regexp.MustCompile(`File "([^"]+)", line ([0-9]+), in ([^\n]+)`)
	var frames []StackFrame
	for _, match := range re.FindAllStringSubmatch(raw, -1) {
		frames = append(frames, StackFrame{File: match[1], Line: atoi(match[2]), Function: strings.TrimSpace(match[3]), Language: "python"})
	}
	return frames
}

func parseNodeFrames(raw string) []StackFrame {
	re := regexp.MustCompile(`(?m)^\s*at\s+(?:(.*?)\s+\()?([^()\n]+?):([0-9]+):([0-9]+)\)?`)
	var frames []StackFrame
	for _, match := range re.FindAllStringSubmatch(raw, -1) {
		frames = append(frames, StackFrame{Function: strings.TrimSpace(match[1]), File: strings.TrimSpace(match[2]), Line: atoi(match[3]), Column: atoi(match[4]), Language: "node"})
	}
	return frames
}

func parseRubyFrames(raw string) []StackFrame {
	re := regexp.MustCompile(`(?m)^\s*([^:\n]+(?:/[^:\n]+)*\.rb):([0-9]+):in ['\x60]([^'\x60]+)['\x60]`)
	var frames []StackFrame
	for _, match := range re.FindAllStringSubmatch(raw, -1) {
		frames = append(frames, StackFrame{File: strings.TrimSpace(match[1]), Line: atoi(match[2]), Function: strings.TrimSpace(match[3]), Language: "ruby"})
	}
	return frames
}

func parsePHPFrames(raw string) []StackFrame {
	var frames []StackFrame
	inRe := regexp.MustCompile(`\sin\s+(.+?\.php):([0-9]+)`)
	for _, match := range inRe.FindAllStringSubmatch(raw, -1) {
		frames = append(frames, StackFrame{File: strings.TrimSpace(match[1]), Line: atoi(match[2]), Language: "php"})
	}
	stackRe := regexp.MustCompile(`(?m)^#[0-9]+\s+(.+?\.php)\(([0-9]+)\):\s*([^\n]+)`)
	for _, match := range stackRe.FindAllStringSubmatch(raw, -1) {
		frames = append(frames, StackFrame{File: strings.TrimSpace(match[1]), Line: atoi(match[2]), Function: strings.TrimSpace(match[3]), Language: "php"})
	}
	return frames
}

func parseGoFrames(raw string) []StackFrame {
	lines := strings.Split(raw, "\n")
	var frames []StackFrame
	for index := 0; index < len(lines)-1; index++ {
		fn := strings.TrimSpace(lines[index])
		fileLine := strings.TrimSpace(lines[index+1])
		if !strings.Contains(fileLine, ".go:") || strings.HasPrefix(fn, "panic:") || strings.HasPrefix(fn, "goroutine ") {
			continue
		}
		file, line := splitFileLine(fileLine)
		if file == "" {
			continue
		}
		frames = append(frames, StackFrame{Function: fn, File: file, Line: line, Language: "go"})
	}
	return frames
}

func dedupeFrames(frames []StackFrame) []StackFrame {
	seen := map[string]bool{}
	var unique []StackFrame
	for _, frame := range frames {
		key := frame.File + ":" + strconv.Itoa(frame.Line) + ":" + frame.Function
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, frame)
	}
	return unique
}

func isApplicationFrame(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	for _, ignored := range []string{"/node_modules/", "/vendor/", "/site-packages/", "/dist-packages/", "/gems/", "/pkg/mod/", "/.venv/"} {
		if strings.Contains(lower, ignored) {
			return false
		}
	}
	return path != ""
}

func exists(targetDir string, rel string) bool {
	_, err := os.Stat(filepath.Join(targetDir, filepath.FromSlash(rel)))
	return err == nil
}

func pythonPackageManager(targetDir string) string {
	switch {
	case exists(targetDir, "uv.lock"):
		return "uv"
	case exists(targetDir, "poetry.lock"):
		return "poetry"
	default:
		return "pip"
	}
}

func nodeFramework(targetDir string) string {
	raw, err := os.ReadFile(filepath.Join(targetDir, "package.json"))
	if err != nil {
		return "node"
	}
	content := strings.ToLower(string(raw))
	switch {
	case strings.Contains(content, "@nestjs/"):
		return "nestjs"
	case strings.Contains(content, "express"):
		return "express"
	case strings.Contains(content, "next"):
		return "nextjs"
	default:
		return "node"
	}
}

func excerptAroundLine(content string, line int, radius int) string {
	lines := strings.Split(content, "\n")
	if line <= 0 || line > len(lines) {
		return truncate(redactSecrets(content), 1800)
	}
	start := line - radius
	if start < 1 {
		start = 1
	}
	end := line + radius
	if end > len(lines) {
		end = len(lines)
	}
	var builder strings.Builder
	for index := start; index <= end; index++ {
		builder.WriteString(fmt.Sprintf("%4d | %s\n", index, lines[index-1]))
	}
	return redactSecrets(builder.String())
}

func redactSecrets(value string) string {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)(password|passwd|pwd|token|secret|api[_-]?key)\s*[:=]\s*["']?[^"'\s]+`),
		regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._\-]+`),
	}
	out := value
	for _, pattern := range patterns {
		out = pattern.ReplaceAllString(out, "$1=[REDACTED]")
	}
	return out
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "\n[truncated]"
}

func splitFileLine(value string) (string, int) {
	value = strings.Fields(value)[0]
	index := strings.LastIndex(value, ":")
	if index < 0 {
		return "", 0
	}
	return strings.TrimSpace(value[:index]), atoi(value[index+1:])
}

func atoi(value string) int {
	parsed, _ := strconv.Atoi(strings.TrimSpace(value))
	return parsed
}

func defaultExcludePaths() []string {
	return []string{".git", "node_modules", "vendor", ".venv", "__pycache__", "tmp", "storage/logs"}
}

func changedPaths(changes []agentfix.Change) []string {
	paths := make([]string, 0, len(changes))
	for _, change := range changes {
		paths = append(paths, change.Path)
	}
	sort.Strings(paths)
	return paths
}
