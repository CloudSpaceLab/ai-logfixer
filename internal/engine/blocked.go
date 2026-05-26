package engine

import (
	"strings"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
)

type BlockedPlanBuilder struct {
	IDFactory ContractIDFactory
	Now       time.Time
	Source    string
	Actor     string
}

func (b BlockedPlanBuilder) RemediationPlan(diagnosisID string, signal IncidentSignal, reason string) contractsv1.RemediationPlan {
	now := b.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	factory := b.IDFactory
	if reason == "" {
		reason = "Automatic remediation is blocked because no safe allowlisted patch is available."
	}
	parts := append(signal.StableParts(), diagnosisID, reason)
	return contractsv1.RemediationPlan{
		ID:                factory.ID("rem_plan_blocked", parts...),
		ContractVersion:   contractsv1.ContractVersion,
		SchemaURL:         contractsv1.RemediationPlanSchemaURL,
		DiagnosisResultID: diagnosisID,
		Summary:           "Automatic remediation blocked for " + signal.RouteLabel() + ".",
		FixPreview: contractsv1.DiffPreview{
			Before: blockedBefore(signal),
			After:  "No automatic change; manual review or an allowlisted remediator is required.",
		},
		RollbackPlan: contractsv1.RollbackPlan{
			ID:                   factory.ID("rollback_blocked", parts...),
			RollbackType:         contractsv1.RollbackUnavailable,
			SnapshotRefs:         []string{},
			RestoreSteps:         []string{},
			Limitations:          []string{"No automatic patch was applied, so AI LogFixer has no generated change to roll back."},
			RiskLevel:            contractsv1.SafetyBlocked,
			RequiresManualReview: true,
		},
		RiskLevel:        contractsv1.SafetyBlocked,
		ApprovalRequired: true,
		Status:           contractsv1.RemediationStatusEscalated,
		DisplayStatus:    "Automatic fix blocked",
		UserMessage:      reason,
		NextActions: []contractsv1.NextAction{
			{
				ID:          "next_manual_review",
				Label:       "Review incident",
				ActionType:  "manual_review",
				Description: "Review evidence and provide an explicit remediation path or approval policy.",
				Enabled:     true,
			},
		},
		TimelineEvents: []contractsv1.TimelineEvent{
			{
				ID:        factory.ID("tl_blocked_plan", parts...),
				Type:      "remediation.escalated",
				Message:   "Automatic remediation blocked by safety policy.",
				Severity:  "warning",
				Timestamp: now,
			},
		},
		ExternalRefs:  []contractsv1.ExternalRef{},
		KnowledgeRefs: []contractsv1.KnowledgeRef{},
		CreatedAt:     now,
	}
}

func (b BlockedPlanBuilder) EscalatedAttempt(planID string, signal IncidentSignal, reason string) contractsv1.RemediationAttempt {
	now := b.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	started := now
	finished := now.Add(time.Nanosecond)
	factory := b.IDFactory
	parts := append(signal.StableParts(), planID, reason)
	return contractsv1.RemediationAttempt{
		ID:                  factory.ID("rem_attempt_blocked", parts...),
		ContractVersion:     contractsv1.ContractVersion,
		SchemaURL:           contractsv1.RemediationAttemptSchemaURL,
		RemediationPlanID:   planID,
		ApprovalRequestID:   "manual_review_required",
		Status:              contractsv1.RemediationStatusEscalated,
		ExecutionStartedAt:  &started,
		ExecutionFinishedAt: &finished,
		MonitorSummary: contractsv1.MonitorSummary{
			Status:   "not_applied",
			Message:  reason,
			Signals:  []string{"automatic_fix_blocked", "changes_applied=false"},
			Duration: "0s",
		},
		DisplayStatus:  "Escalated without changes",
		UserMessage:    reason + " No files, database schema, or config were changed.",
		TimelineEvents: []contractsv1.TimelineEvent{{ID: factory.ID("tl_blocked_attempt", parts...), Type: "remediation.escalated", Message: "Automatic remediation requires manual review.", Severity: "warning", Timestamp: finished}},
		ExternalRefs:   []contractsv1.ExternalRef{},
	}
}

func (b BlockedPlanBuilder) EscalatedReceipt(diagnosisID string, planID string, attemptID string, signal IncidentSignal, reason string) contractsv1.Receipt {
	now := b.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	factory := b.IDFactory
	parts := append(signal.StableParts(), diagnosisID, planID, attemptID, reason)
	actor := b.Actor
	if actor == "" {
		actor = "ai-logfixer"
	}
	return contractsv1.Receipt{
		ID:                   factory.ID("receipt_blocked", parts...),
		DiagnosisID:          diagnosisID,
		RemediationPlanID:    planID,
		RemediationAttemptID: attemptID,
		ActionTaken:          "classified incident; no automatic patch applied",
		Actor:                actor,
		Timestamp:            now.Add(2 * time.Nanosecond),
		BeforeState:          blockedBefore(signal),
		AfterState:           "unchanged; manual review required",
		Outcome:              "escalated",
		Summary:              reason,
		TimelineEvents:       []contractsv1.TimelineEvent{{ID: factory.ID("tl_blocked_receipt", parts...), Type: "receipt.created", Message: "Receipt recorded for blocked automatic remediation.", Severity: "warning", Timestamp: now.Add(2 * time.Nanosecond)}},
		ExternalRefs:         []contractsv1.ExternalRef{},
		KnowledgeRefs:        []contractsv1.KnowledgeRef{},
	}
}

func blockedBefore(signal IncidentSignal) string {
	parts := []string{}
	if signal.Service != "" {
		parts = append(parts, "service="+signal.Service)
	}
	if label := signal.RouteLabel(); label != "" {
		parts = append(parts, "signal="+label)
	}
	if code := signal.ErrorCode(); code != "" {
		parts = append(parts, "error_code="+code)
	}
	if signal.Count > 0 {
		parts = append(parts, "count="+stringInt(signal.Count))
	}
	if len(parts) == 0 {
		return "incident evidence detected"
	}
	return strings.Join(parts, " ")
}

func stringInt(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[index:])
}
