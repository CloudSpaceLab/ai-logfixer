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

func (r Receipt) Validate() error {
	var errs []error

	require(&errs, r.ID != "", "id is required")
	require(&errs, r.DiagnosisID != "", "diagnosis_id is required")
	require(&errs, r.ActionTaken != "", "action_taken is required")
	require(&errs, r.Actor != "", "actor is required")
	require(&errs, !r.Timestamp.IsZero(), "timestamp is required")
	require(&errs, r.BeforeState != "", "before_state is required")
	require(&errs, r.AfterState != "", "after_state is required")

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
