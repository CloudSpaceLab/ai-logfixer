package v1

import (
	"errors"
	"fmt"
)

func (d DiagnosisResult) Validate() error {
	var errs []error

	require(&errs, d.ID != "", "id is required")
	require(&errs, d.ContractVersion == ContractVersion, "contract_version must be v1")
	require(&errs, d.SchemaURL == DiagnosisSchemaURL, "schema_url must match diagnosis schema URL")
	require(&errs, d.Status != "", "status is required")
	require(&errs, validDiagnosisStatus(d.Status), fmt.Sprintf("unsupported diagnosis status %q", d.Status))
	require(&errs, d.Summary != "", "summary is required")
	require(&errs, validConfidence(d.Confidence), "confidence must be between 0 and 1")
	require(&errs, d.SuspectedRootCause != "", "suspected_root_cause is required")
	require(&errs, len(d.AffectedServices) > 0, "affected_services must not be empty")
	require(&errs, validSafety(d.SafetyClassification), fmt.Sprintf("unsupported safety_classification %q", d.SafetyClassification))
	require(&errs, d.DisplayStatus != "", "display_status is required")
	require(&errs, d.UserMessage != "", "user_message is required")
	require(&errs, !d.CreatedAt.IsZero(), "created_at is required")

	if d.Status == DiagnosisStatusComplete {
		require(&errs, len(d.EvidenceItems) > 0, "complete diagnosis requires evidence")
	}

	for index, evidence := range d.EvidenceItems {
		errs = append(errs, prefixErr(fmt.Sprintf("evidence_items[%d]", index), evidence.Validate())...)
	}
	for index, recommendation := range d.Recommendations {
		errs = append(errs, prefixErr(fmt.Sprintf("recommendations[%d]", index), recommendation.Validate())...)
	}
	if d.PatchPlan != nil {
		errs = append(errs, prefixErr("patch_plan", d.PatchPlan.Validate())...)
	}
	if d.RollbackPlan != nil {
		errs = append(errs, prefixErr("rollback_plan", d.RollbackPlan.Validate())...)
	}
	for index, ref := range d.ExternalRefs {
		errs = append(errs, prefixErr(fmt.Sprintf("external_refs[%d]", index), ref.Validate())...)
	}
	for index, ref := range d.KnowledgeRefs {
		errs = append(errs, prefixErr(fmt.Sprintf("knowledge_refs[%d]", index), ref.Validate())...)
	}

	return errors.Join(errs...)
}

func (e EvidenceItem) Validate() error {
	var errs []error

	require(&errs, e.ID != "", "id is required")
	require(&errs, validEvidenceType(e.Type), fmt.Sprintf("unsupported evidence type %q", e.Type))
	require(&errs, e.Source != "", "source is required")
	require(&errs, !e.Timestamp.IsZero(), "timestamp is required")
	require(&errs, e.Title != "", "title is required")
	require(&errs, e.Summary != "", "summary is required")
	require(&errs, validRedactionState(e.RedactionState), fmt.Sprintf("unsupported redaction_state %q", e.RedactionState))
	if e.RawExcerpt != "" && (e.RedactionState == RedactionStateFailed || e.RedactionState == RedactionStateUnknown) {
		errs = append(errs, errors.New("raw_excerpt cannot be displayed when redaction_state is failed or unknown"))
	}
	for index, ref := range e.ExternalRefs {
		errs = append(errs, prefixErr(fmt.Sprintf("external_refs[%d]", index), ref.Validate())...)
	}
	for index, ref := range e.KnowledgeRefs {
		errs = append(errs, prefixErr(fmt.Sprintf("knowledge_refs[%d]", index), ref.Validate())...)
	}

	return errors.Join(errs...)
}

func (r RunbookRecommendation) Validate() error {
	var errs []error

	require(&errs, r.ID != "", "id is required")
	require(&errs, r.Title != "", "title is required")
	require(&errs, r.Reason != "", "reason is required")
	require(&errs, validConfidence(r.Confidence), "confidence must be between 0 and 1")
	require(&errs, len(r.Steps) > 0, "steps must not be empty")
	require(&errs, validSafety(r.EstimatedRisk), fmt.Sprintf("unsupported estimated_risk %q", r.EstimatedRisk))
	if r.EstimatedRisk == SafetyHighRisk || r.EstimatedRisk == SafetyCriticalRisk {
		require(&errs, r.RequiresApproval, fmt.Sprintf("%s recommendations require approval", r.EstimatedRisk))
	}

	return errors.Join(errs...)
}

func (p PatchPlan) Validate() error {
	var errs []error

	require(&errs, p.ID != "", "id is required")
	require(&errs, validPatchTargetType(p.TargetType), fmt.Sprintf("unsupported target_type %q", p.TargetType))
	require(&errs, len(p.TargetRefs) > 0, "target_refs must not be empty")
	require(&errs, p.DiffPreview.Before != "", "diff_preview.before is required")
	require(&errs, p.DiffPreview.After != "", "diff_preview.after is required")
	require(&errs, validSafety(p.RiskLevel), fmt.Sprintf("unsupported risk_level %q", p.RiskLevel))
	if p.RiskLevel == SafetyHighRisk || p.RiskLevel == SafetyCriticalRisk {
		require(&errs, p.RequiresApproval, fmt.Sprintf("%s patch plans require approval", p.RiskLevel))
	}
	if p.RiskLevel == SafetyBlocked {
		require(&errs, len(p.BlockedReasons) > 0, "blocked patch plans require blocked_reasons")
	}

	return errors.Join(errs...)
}

func (r RollbackPlan) Validate() error {
	var errs []error

	require(&errs, r.ID != "", "id is required")
	require(&errs, validRollbackType(r.RollbackType), fmt.Sprintf("unsupported rollback_type %q", r.RollbackType))
	require(&errs, validSafety(r.RiskLevel), fmt.Sprintf("unsupported risk_level %q", r.RiskLevel))
	if r.RollbackType == RollbackUnavailable {
		require(&errs, len(r.Limitations) > 0, "unavailable rollback plans require limitations")
	} else {
		require(&errs, len(r.RestoreSteps) > 0, "restore_steps must not be empty")
	}
	if r.RiskLevel == SafetyHighRisk || r.RiskLevel == SafetyCriticalRisk {
		require(&errs, r.RequiresManualReview, fmt.Sprintf("%s rollback plans require manual review", r.RiskLevel))
	}

	return errors.Join(errs...)
}

func (r InvestigationRequest) Validate() error {
	var errs []error

	require(&errs, r.ID != "", "id is required")
	require(&errs, r.ContractVersion == ContractVersion, "contract_version must be v1")
	require(&errs, r.SchemaURL == InvestigationRequestSchemaURL, "schema_url must match investigation request schema URL")
	require(&errs, validSourceType(r.SourceType), fmt.Sprintf("unsupported source_type %q", r.SourceType))
	require(&errs, r.SourceName != "", "source_name is required")
	require(&errs, r.Service != "", "service is required")
	require(&errs, r.Symptom != "", "symptom is required")
	require(&errs, !r.TimeWindow.Start.IsZero(), "time_window.start is required")
	require(&errs, !r.TimeWindow.End.IsZero(), "time_window.end is required")
	require(&errs, r.TimeWindow.End.After(r.TimeWindow.Start), "time_window.end must be after time_window.start")
	require(&errs, r.SignalFingerprint.Service != "", "signal_fingerprint.service is required")
	require(&errs, r.DisplayStatus != "", "display_status is required")
	require(&errs, r.UserMessage != "", "user_message is required")
	require(&errs, !r.CreatedAt.IsZero(), "created_at is required")
	for index, ref := range r.ExternalRefs {
		errs = append(errs, prefixErr(fmt.Sprintf("external_refs[%d]", index), ref.Validate())...)
	}
	for index, ref := range r.KnowledgeRefs {
		errs = append(errs, prefixErr(fmt.Sprintf("knowledge_refs[%d]", index), ref.Validate())...)
	}

	return errors.Join(errs...)
}

func (d InvestigationDecision) Validate() error {
	var errs []error

	require(&errs, d.ID != "", "id is required")
	require(&errs, d.ContractVersion == ContractVersion, "contract_version must be v1")
	require(&errs, d.SchemaURL == InvestigationDecisionSchemaURL, "schema_url must match investigation decision schema URL")
	require(&errs, d.RequestID != "", "request_id is required")
	require(&errs, validInvestigationDecision(d.Decision), fmt.Sprintf("unsupported decision %q", d.Decision))
	require(&errs, !d.CreatedAt.IsZero(), "created_at is required")
	if d.Decision == InvestigationDecisionReject {
		require(&errs, d.Explanation != "", "rejected investigation decisions require explanation")
		require(&errs, d.UserMessage != "", "rejected investigation decisions require user_message")
	}
	if d.Decision == InvestigationDecisionQueue {
		require(&errs, d.CapacitySnapshot.QueueLimit > 0, "queued investigation decisions require capacity context")
	}
	if d.Decision == InvestigationDecisionStartNew || d.Decision == InvestigationDecisionAttachDuplicate || d.Decision == InvestigationDecisionLinkRelated || d.Decision == InvestigationDecisionQueue {
		require(&errs, d.ClusterID != "", fmt.Sprintf("%s investigation decisions require cluster_id", d.Decision))
	}

	return errors.Join(errs...)
}

func (p RemediationPlan) Validate() error {
	var errs []error

	require(&errs, p.ID != "", "id is required")
	require(&errs, p.ContractVersion == ContractVersion, "contract_version must be v1")
	require(&errs, p.SchemaURL == RemediationPlanSchemaURL, "schema_url must match remediation plan schema URL")
	require(&errs, p.DiagnosisResultID != "", "diagnosis_result_id is required")
	require(&errs, p.Summary != "", "summary is required")
	require(&errs, p.FixPreview.Before != "", "fix_preview.before is required")
	require(&errs, p.FixPreview.After != "", "fix_preview.after is required")
	require(&errs, validSafety(p.RiskLevel), fmt.Sprintf("unsupported risk_level %q", p.RiskLevel))
	if p.RiskLevel == SafetyHighRisk || p.RiskLevel == SafetyCriticalRisk {
		require(&errs, p.ApprovalRequired, fmt.Sprintf("%s remediation plans require approval", p.RiskLevel))
	}
	if p.RiskLevel == SafetyBlocked {
		require(&errs, p.UserMessage != "", "blocked remediation plans must explain why they are blocked")
	}
	require(&errs, validRemediationStatus(p.Status), fmt.Sprintf("unsupported status %q", p.Status))
	require(&errs, p.DisplayStatus != "", "display_status is required")
	require(&errs, p.UserMessage != "", "user_message is required")
	require(&errs, !p.CreatedAt.IsZero(), "created_at is required")
	errs = append(errs, prefixErr("rollback_plan", p.RollbackPlan.Validate())...)
	for index, ref := range p.ExternalRefs {
		errs = append(errs, prefixErr(fmt.Sprintf("external_refs[%d]", index), ref.Validate())...)
	}
	for index, ref := range p.KnowledgeRefs {
		errs = append(errs, prefixErr(fmt.Sprintf("knowledge_refs[%d]", index), ref.Validate())...)
	}

	return errors.Join(errs...)
}

func (a ApprovalRequest) Validate() error {
	var errs []error

	require(&errs, a.ID != "", "id is required")
	require(&errs, a.RemediationPlanID != "", "remediation_plan_id is required")
	require(&errs, a.Reason != "", "reason is required")
	require(&errs, validSafety(a.RiskLevel), fmt.Sprintf("unsupported risk_level %q", a.RiskLevel))
	require(&errs, a.RequestedBy != "", "requested_by is required")
	require(&errs, validApprovalStatus(a.Status), fmt.Sprintf("unsupported status %q", a.Status))
	require(&errs, a.UserMessage != "", "user_message is required")
	if a.Status == ApprovalStatusApproved || a.Status == ApprovalStatusDenied {
		require(&errs, a.Approver != "", "decided approval requests require approver")
		require(&errs, a.DecidedAt != nil, "decided approval requests require decided_at")
	}

	return errors.Join(errs...)
}

func (a RemediationAttempt) Validate() error {
	var errs []error

	require(&errs, a.ID != "", "id is required")
	require(&errs, a.ContractVersion == ContractVersion, "contract_version must be v1")
	require(&errs, a.SchemaURL == RemediationAttemptSchemaURL, "schema_url must match remediation attempt schema URL")
	require(&errs, a.RemediationPlanID != "", "remediation_plan_id is required")
	require(&errs, validRemediationStatus(a.Status), fmt.Sprintf("unsupported status %q", a.Status))
	require(&errs, a.DisplayStatus != "", "display_status is required")
	require(&errs, a.UserMessage != "", "user_message is required")
	if a.Status == RemediationStatusRunning || a.Status == RemediationStatusMonitoring || a.Status == RemediationStatusSucceeded || a.Status == RemediationStatusFailed || a.Status == RemediationStatusRolledBack || a.Status == RemediationStatusEscalated {
		require(&errs, a.ApprovalRequestID != "", "remediation attempts cannot run without required approval")
		require(&errs, a.ExecutionStartedAt != nil, "running remediation attempts require execution_started_at")
	}
	if a.Status == RemediationStatusSucceeded {
		require(&errs, a.MonitorSummary.Status != "", "succeeded remediation attempts require monitoring evidence")
	}
	for index, ref := range a.ExternalRefs {
		errs = append(errs, prefixErr(fmt.Sprintf("external_refs[%d]", index), ref.Validate())...)
	}

	return errors.Join(errs...)
}

func (r RemediationEvent) Validate() error {
	var errs []error

	require(&errs, r.ID != "", "id is required")
	require(&errs, r.RemediationAttemptID != "", "remediation_attempt_id is required")
	require(&errs, r.EventType != "", "event_type is required")
	require(&errs, r.Message != "", "message is required")
	require(&errs, r.Severity != "", "severity is required")
	require(&errs, !r.Timestamp.IsZero(), "timestamp is required")

	return errors.Join(errs...)
}

func (r Receipt) Validate() error {
	var errs []error

	require(&errs, r.ID != "", "id is required")
	require(&errs, r.DiagnosisID != "", "diagnosis_id is required")
	require(&errs, r.RemediationPlanID != "", "remediation_plan_id is required")
	require(&errs, r.RemediationAttemptID != "", "remediation_attempt_id is required")
	require(&errs, r.ActionTaken != "", "action_taken is required")
	require(&errs, r.Actor != "", "actor is required")
	require(&errs, !r.Timestamp.IsZero(), "timestamp is required")
	require(&errs, r.BeforeState != "", "before_state is required")
	require(&errs, r.AfterState != "", "after_state is required")
	require(&errs, r.Outcome != "", "outcome is required")
	require(&errs, r.Summary != "", "summary is required")
	for index, ref := range r.ExternalRefs {
		errs = append(errs, prefixErr(fmt.Sprintf("external_refs[%d]", index), ref.Validate())...)
	}
	for index, ref := range r.KnowledgeRefs {
		errs = append(errs, prefixErr(fmt.Sprintf("knowledge_refs[%d]", index), ref.Validate())...)
	}

	return errors.Join(errs...)
}

func (r ExternalRef) Validate() error {
	var errs []error

	require(&errs, r.System != "", "system is required")
	require(&errs, r.Type != "", "type is required")
	require(&errs, r.ID != "", "id is required")

	return errors.Join(errs...)
}

func (r KnowledgeRef) Validate() error {
	var errs []error

	require(&errs, r.GraphID != "", "graph_id is required")
	require(&errs, r.NodeID != "", "node_id is required")
	require(&errs, r.NodeType != "", "node_type is required")
	require(&errs, r.Relationship != "", "relationship is required")
	require(&errs, validConfidence(r.Confidence), "confidence must be between 0 and 1")
	require(&errs, r.Source != "", "source is required")

	return errors.Join(errs...)
}

func require(errs *[]error, condition bool, message string) {
	if !condition {
		*errs = append(*errs, errors.New(message))
	}
}

func prefixErr(prefix string, err error) []error {
	if err == nil {
		return nil
	}
	return []error{fmt.Errorf("%s: %w", prefix, err)}
}

func validConfidence(value float64) bool {
	return value >= 0 && value <= 1
}

func validDiagnosisStatus(value DiagnosisStatus) bool {
	switch value {
	case DiagnosisStatusPending, DiagnosisStatusComplete, DiagnosisStatusFailed, DiagnosisStatusNeedsMoreData, DiagnosisStatusUnsupportedSource, DiagnosisStatusBlockedBySafety:
		return true
	default:
		return false
	}
}

func validEvidenceType(value EvidenceType) bool {
	switch value {
	case EvidenceTypeLog, EvidenceTypeTrace, EvidenceTypeMetric, EvidenceTypeDB, EvidenceTypeConfig, EvidenceTypeCVE:
		return true
	default:
		return false
	}
}

func validRedactionState(value RedactionState) bool {
	switch value {
	case RedactionStateRedacted, RedactionStateNotNeeded, RedactionStateUnknown, RedactionStateFailed:
		return true
	default:
		return false
	}
}

func validSafety(value SafetyClassification) bool {
	switch value {
	case SafetyReadOnly, SafetyLowRisk, SafetyMediumRisk, SafetyHighRisk, SafetyCriticalRisk, SafetyBlocked:
		return true
	default:
		return false
	}
}

func validPatchTargetType(value PatchTargetType) bool {
	switch value {
	case PatchTargetFile, PatchTargetDBSchema, PatchTargetConfig, PatchTargetDependency, PatchTargetRuntimeSetting:
		return true
	default:
		return false
	}
}

func validRollbackType(value RollbackType) bool {
	switch value {
	case RollbackSnapshot, RollbackReversePatch, RollbackMigrationDown, RollbackRestoreConfig, RollbackManualOnly, RollbackUnavailable:
		return true
	default:
		return false
	}
}

func validSourceType(value SourceType) bool {
	switch value {
	case SourceTypeAutomatic, SourceTypeManual, SourceTypeIntegration:
		return true
	default:
		return false
	}
}

func validInvestigationDecision(value InvestigationDecisionType) bool {
	switch value {
	case InvestigationDecisionStartNew, InvestigationDecisionAttachDuplicate, InvestigationDecisionLinkRelated, InvestigationDecisionQueue, InvestigationDecisionReject:
		return true
	default:
		return false
	}
}

func validRemediationStatus(value RemediationStatus) bool {
	switch value {
	case RemediationStatusPending, RemediationStatusPlanning, RemediationStatusAwaitingApproval, RemediationStatusApproved, RemediationStatusDenied, RemediationStatusRunning, RemediationStatusMonitoring, RemediationStatusSucceeded, RemediationStatusFailed, RemediationStatusRolledBack, RemediationStatusEscalated:
		return true
	default:
		return false
	}
}

func validApprovalStatus(value ApprovalStatus) bool {
	switch value {
	case ApprovalStatusPending, ApprovalStatusApproved, ApprovalStatusDenied, ApprovalStatusExpired:
		return true
	default:
		return false
	}
}
