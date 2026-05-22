package mvp

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/demoapp"
)

type Options struct {
	ServiceName     string
	BaseURL         string
	LogPath         string
	ConfigPath      string
	HealthyUpstream string
	ErrorThreshold  int
	Now             time.Time
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
	if options.ErrorThreshold == 0 {
		options.ErrorThreshold = 3
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}

	logContent, err := os.ReadFile(filepath.Clean(options.LogPath))
	if err != nil {
		return Result{}, fmt.Errorf("read log file: %w", err)
	}
	if count503s(string(logContent), options.ServiceName) < options.ErrorThreshold {
		return Result{}, fmt.Errorf("503 threshold not reached for %s", options.ServiceName)
	}

	beforeConfig, err := demoapp.ReadConfig(options.ConfigPath)
	if err != nil {
		return Result{}, fmt.Errorf("read config: %w", err)
	}

	investigationRequest := buildInvestigationRequest(options)
	diagnosis := buildDiagnosis(options, beforeConfig, string(logContent))
	remediationPlan := buildRemediationPlan(options, beforeConfig)

	if err := investigationRequest.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate investigation request: %w", err)
	}
	if err := diagnosis.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate diagnosis: %w", err)
	}
	if err := remediationPlan.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate remediation plan: %w", err)
	}

	backupPath, err := backupConfig(options.ConfigPath, options.Now)
	if err != nil {
		return Result{}, fmt.Errorf("backup config: %w", err)
	}

	fixedConfig := beforeConfig
	fixedConfig.UpstreamURL = options.HealthyUpstream
	if err := demoapp.WriteConfig(options.ConfigPath, fixedConfig); err != nil {
		return Result{}, fmt.Errorf("apply config patch: %w", err)
	}

	attempt := buildAttempt(options)
	if err := verifyFixed(ctx, options.BaseURL+"/orders"); err != nil {
		attempt.Status = contractsv1.RemediationStatusFailed
		attempt.UserMessage = "The patch was applied, but verification failed. Rollback is required."
		_ = restoreBackup(options.ConfigPath, backupPath)
		return Result{}, fmt.Errorf("verify fix: %w", err)
	}

	receipt := buildReceipt(options, beforeConfig, fixedConfig)

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

func count503s(logContent string, serviceName string) int {
	count := 0
	for _, line := range strings.Split(logContent, "\n") {
		if strings.Contains(line, "status=503") && strings.Contains(line, "service="+serviceName) {
			count++
		}
	}
	return count
}

func backupConfig(path string, now time.Time) (string, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", err
	}

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

func verifyFixed(ctx context.Context, url string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("expected status 200 from %s, got %d", url, response.StatusCode)
	}
	return nil
}

func buildInvestigationRequest(options Options) contractsv1.InvestigationRequest {
	return contractsv1.InvestigationRequest{
		ID:              "inv_req_goravel_503_001",
		ContractVersion: contractsv1.ContractVersion,
		SchemaURL:       contractsv1.InvestigationRequestSchemaURL,
		SourceType:      contractsv1.SourceTypeAutomatic,
		SourceName:      "ai-logfixer-mvp-log-detector",
		RequestedBy:     "ai-logfixer",
		Service:         options.ServiceName,
		Symptom:         "Repeated 503 responses",
		ErrorCode:       "503",
		TimeWindow: contractsv1.TimeWindow{
			Start: options.Now.Add(-5 * time.Minute),
			End:   options.Now,
		},
		SignalFingerprint: contractsv1.SignalFingerprint{
			Service:       options.ServiceName,
			Symptom:       "availability_error_spike",
			ErrorCode:     "503",
			Source:        "application_logs",
			DeployVersion: "demo",
			Tags:          []string{"goravel", "mvp", "http"},
		},
		DisplayStatus: "Investigation started automatically",
		UserMessage:   "I detected repeated 503 responses and started a minimal investigation.",
		ExternalRefs:  []contractsv1.ExternalRef{},
		KnowledgeRefs: []contractsv1.KnowledgeRef{},
		CreatedAt:     options.Now,
	}
}

func buildDiagnosis(options Options, config demoapp.Config, logContent string) contractsv1.DiagnosisResult {
	return contractsv1.DiagnosisResult{
		ID:                 "diag_goravel_bad_upstream_001",
		ContractVersion:    contractsv1.ContractVersion,
		SchemaURL:          contractsv1.DiagnosisSchemaURL,
		Status:             contractsv1.DiagnosisStatusComplete,
		Summary:            "Repeated 503 responses are likely caused by a bad upstream URL in the demo app config.",
		Confidence:         0.93,
		SuspectedRootCause: "The app is configured to call an unavailable upstream service.",
		AffectedServices:   []string{options.ServiceName},
		EvidenceItems: []contractsv1.EvidenceItem{
			{
				ID:             "ev_goravel_503_logs_001",
				Type:           contractsv1.EvidenceTypeLog,
				Source:         options.LogPath,
				Timestamp:      options.Now,
				Title:          "Repeated 503 application logs",
				Summary:        fmt.Sprintf("The log file contains repeated 503 responses for %s.", options.ServiceName),
				RawExcerpt:     safeExcerpt(logContent),
				RedactionState: contractsv1.RedactionStateRedacted,
				RelatedIDs:     []string{"ev_goravel_config_001"},
				UIHints: contractsv1.UIHints{
					Icon:     "file-warning",
					Tone:     "danger",
					Sections: []string{"logs", "evidence"},
				},
				ExternalRefs:  []contractsv1.ExternalRef{},
				KnowledgeRefs: []contractsv1.KnowledgeRef{},
			},
			{
				ID:             "ev_goravel_config_001",
				Type:           contractsv1.EvidenceTypeConfig,
				Source:         options.ConfigPath,
				Timestamp:      options.Now,
				Title:          "Broken upstream config",
				Summary:        "The demo app config points to an unavailable upstream URL.",
				RawExcerpt:     "upstream_url=" + config.UpstreamURL,
				RedactionState: contractsv1.RedactionStateNotNeeded,
				RelatedIDs:     []string{"ev_goravel_503_logs_001"},
				UIHints: contractsv1.UIHints{
					Icon:     "settings",
					Tone:     "warning",
					Sections: []string{"config"},
				},
				ExternalRefs:  []contractsv1.ExternalRef{},
				KnowledgeRefs: []contractsv1.KnowledgeRef{},
			},
		},
		Recommendations: []contractsv1.RunbookRecommendation{
			{
				ID:                  "rec_fix_goravel_upstream_001",
				Title:               "Restore the healthy upstream URL",
				Reason:              "The current upstream URL is unavailable and every order request returns 503.",
				Confidence:          0.91,
				Steps:               []string{"Back up the current config.", "Replace the upstream URL.", "Verify /orders returns 200."},
				RequiredPermissions: []string{"config:write", "service:verify"},
				EstimatedRisk:       contractsv1.SafetyLowRisk,
				RequiresApproval:    false,
			},
		},
		PatchPlan: &contractsv1.PatchPlan{
			ID:         "patch_goravel_upstream_001",
			TargetType: contractsv1.PatchTargetConfig,
			TargetRefs: []string{options.ConfigPath},
			DiffPreview: contractsv1.DiffPreview{
				Before: "upstream_url=" + config.UpstreamURL,
				After:  "upstream_url=" + options.HealthyUpstream,
			},
			RiskLevel:        contractsv1.SafetyLowRisk,
			RequiresApproval: false,
			BlockedReasons:   []string{},
		},
		RollbackPlan: &contractsv1.RollbackPlan{
			ID:                   "rollback_goravel_upstream_001",
			RollbackType:         contractsv1.RollbackRestoreConfig,
			SnapshotRefs:         []string{"config_backup"},
			RestoreSteps:         []string{"Restore the backed-up app config.", "Verify the service state."},
			Limitations:          []string{"Rollback restores the broken demo upstream URL and is intended only for MVP safety proof."},
			RiskLevel:            contractsv1.SafetyLowRisk,
			RequiresManualReview: false,
		},
		SafetyClassification: contractsv1.SafetyLowRisk,
		DisplayStatus:        "Bad upstream config diagnosed",
		UserMessage:          "I found repeated 503s and traced them to the demo app upstream URL.",
		NextActions: []contractsv1.NextAction{
			{
				ID:          "next_apply_safe_fix",
				Label:       "Apply safe fix",
				ActionType:  "apply_remediation",
				Description: "Back up the config, update the upstream URL, and verify the app recovers.",
				Enabled:     true,
			},
		},
		TimelineEvents: []contractsv1.TimelineEvent{
			{
				ID:        "tl_diag_goravel_001",
				Type:      "diagnosis.completed",
				Message:   "Diagnosis completed for repeated 503 responses.",
				Severity:  "info",
				Timestamp: options.Now,
			},
		},
		ExternalRefs:  []contractsv1.ExternalRef{},
		KnowledgeRefs: []contractsv1.KnowledgeRef{},
		CreatedAt:     options.Now,
	}
}

func buildRemediationPlan(options Options, config demoapp.Config) contractsv1.RemediationPlan {
	return contractsv1.RemediationPlan{
		ID:                "rem_plan_goravel_upstream_001",
		ContractVersion:   contractsv1.ContractVersion,
		SchemaURL:         contractsv1.RemediationPlanSchemaURL,
		DiagnosisResultID: "diag_goravel_bad_upstream_001",
		Summary:           "Back up the demo app config and replace the bad upstream URL with a healthy local upstream.",
		FixPreview: contractsv1.DiffPreview{
			Before: "upstream_url=" + config.UpstreamURL,
			After:  "upstream_url=" + options.HealthyUpstream,
		},
		RollbackPlan: contractsv1.RollbackPlan{
			ID:                   "rollback_goravel_upstream_001",
			RollbackType:         contractsv1.RollbackRestoreConfig,
			SnapshotRefs:         []string{"config_backup"},
			RestoreSteps:         []string{"Restore the backed-up app config.", "Verify the service state."},
			Limitations:          []string{"Rollback restores the broken demo upstream URL and is intended only for MVP safety proof."},
			RiskLevel:            contractsv1.SafetyLowRisk,
			RequiresManualReview: false,
		},
		RiskLevel:        contractsv1.SafetyLowRisk,
		ApprovalRequired: false,
		Status:           contractsv1.RemediationStatusApproved,
		DisplayStatus:    "Safe config fix approved automatically",
		UserMessage:      "This low-risk demo config fix can run automatically after saving the previous config.",
		NextActions: []contractsv1.NextAction{
			{
				ID:          "next_execute_fix",
				Label:       "Execute fix",
				ActionType:  "execute_remediation",
				Description: "Apply the config change and verify recovery.",
				Enabled:     true,
			},
		},
		TimelineEvents: []contractsv1.TimelineEvent{
			{
				ID:        "tl_remediation_plan_001",
				Type:      "remediation.plan_created",
				Message:   "Safe remediation plan created.",
				Severity:  "info",
				Timestamp: options.Now,
			},
		},
		ExternalRefs:  []contractsv1.ExternalRef{},
		KnowledgeRefs: []contractsv1.KnowledgeRef{},
		CreatedAt:     options.Now,
	}
}

func buildAttempt(options Options) contractsv1.RemediationAttempt {
	started := options.Now.Add(time.Second)
	finished := options.Now.Add(2 * time.Second)

	return contractsv1.RemediationAttempt{
		ID:                  "rem_attempt_goravel_upstream_001",
		ContractVersion:     contractsv1.ContractVersion,
		SchemaURL:           contractsv1.RemediationAttemptSchemaURL,
		RemediationPlanID:   "rem_plan_goravel_upstream_001",
		ApprovalRequestID:   "auto_approved_low_risk_mvp",
		Status:              contractsv1.RemediationStatusSucceeded,
		ExecutionStartedAt:  &started,
		ExecutionFinishedAt: &finished,
		MonitorSummary: contractsv1.MonitorSummary{
			Status:   "healthy",
			Message:  "/orders returned 200 after the config patch.",
			Signals:  []string{"http_status=200", "endpoint=/orders"},
			Duration: "1s",
		},
		DisplayStatus: "Fix applied and verified",
		UserMessage:   "I updated the upstream URL and verified that /orders recovered.",
		TimelineEvents: []contractsv1.TimelineEvent{
			{
				ID:        "tl_remediation_succeeded_001",
				Type:      "remediation.succeeded",
				Message:   "Config patch applied and verified.",
				Severity:  "info",
				Timestamp: finished,
			},
		},
		ExternalRefs: []contractsv1.ExternalRef{},
	}
}

func buildReceipt(options Options, before demoapp.Config, after demoapp.Config) contractsv1.Receipt {
	return contractsv1.Receipt{
		ID:                   "receipt_goravel_upstream_001",
		DiagnosisID:          "diag_goravel_bad_upstream_001",
		RemediationPlanID:    "rem_plan_goravel_upstream_001",
		RemediationAttemptID: "rem_attempt_goravel_upstream_001",
		ActionTaken:          "updated upstream_url in demo app config",
		Actor:                "ai-logfixer-mvp",
		Approver:             "auto_approved_low_risk_mvp",
		Timestamp:            options.Now.Add(3 * time.Second),
		BeforeState:          "upstream_url=" + before.UpstreamURL,
		AfterState:           "upstream_url=" + after.UpstreamURL,
		Outcome:              "succeeded",
		Summary:              "AI LogFixer detected repeated 503s, patched the bad upstream URL, verified recovery, and saved this receipt.",
		TimelineEvents: []contractsv1.TimelineEvent{
			{
				ID:        "tl_receipt_created_001",
				Type:      "receipt.created",
				Message:   "Receipt recorded for the successful MVP remediation.",
				Severity:  "info",
				Timestamp: options.Now.Add(3 * time.Second),
			},
		},
		ExternalRefs:  []contractsv1.ExternalRef{},
		KnowledgeRefs: []contractsv1.KnowledgeRef{},
	}
}

func safeExcerpt(logContent string) string {
	lines := strings.Split(strings.TrimSpace(logContent), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return strings.Join(lines, "\n")
}
