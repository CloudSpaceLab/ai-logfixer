package domain

import (
	"fmt"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
)

func CanMoveInvestigation(from contractsv1.InvestigationStatus, to contractsv1.InvestigationStatus) bool {
	if from == to {
		return true
	}
	switch from {
	case contractsv1.InvestigationStatusRequested:
		return to == contractsv1.InvestigationStatusFingerprinted ||
			to == contractsv1.InvestigationStatusRejected
	case contractsv1.InvestigationStatusFingerprinted:
		return to == contractsv1.InvestigationStatusLinked ||
			to == contractsv1.InvestigationStatusQueued ||
			to == contractsv1.InvestigationStatusRunning ||
			to == contractsv1.InvestigationStatusRejected
	case contractsv1.InvestigationStatusLinked:
		return to == contractsv1.InvestigationStatusRunning ||
			to == contractsv1.InvestigationStatusCompleted ||
			to == contractsv1.InvestigationStatusFailed
	case contractsv1.InvestigationStatusQueued:
		return to == contractsv1.InvestigationStatusRunning ||
			to == contractsv1.InvestigationStatusRejected
	case contractsv1.InvestigationStatusRunning:
		return to == contractsv1.InvestigationStatusNeedsMoreData ||
			to == contractsv1.InvestigationStatusCompleted ||
			to == contractsv1.InvestigationStatusFailed
	case contractsv1.InvestigationStatusNeedsMoreData:
		return to == contractsv1.InvestigationStatusRunning ||
			to == contractsv1.InvestigationStatusCompleted ||
			to == contractsv1.InvestigationStatusFailed
	default:
		return false
	}
}

func ValidateInvestigationTransition(from contractsv1.InvestigationStatus, to contractsv1.InvestigationStatus) error {
	if CanMoveInvestigation(from, to) {
		return nil
	}
	return fmt.Errorf("invalid investigation transition %q -> %q", from, to)
}

func CanMoveRemediation(from contractsv1.RemediationStatus, to contractsv1.RemediationStatus) bool {
	if from == to {
		return true
	}
	switch from {
	case contractsv1.RemediationStatusPending:
		return to == contractsv1.RemediationStatusPlanning
	case contractsv1.RemediationStatusPlanning:
		return to == contractsv1.RemediationStatusAwaitingApproval ||
			to == contractsv1.RemediationStatusApproved ||
			to == contractsv1.RemediationStatusDenied ||
			to == contractsv1.RemediationStatusRunning ||
			to == contractsv1.RemediationStatusEscalated
	case contractsv1.RemediationStatusAwaitingApproval:
		return to == contractsv1.RemediationStatusApproved ||
			to == contractsv1.RemediationStatusDenied
	case contractsv1.RemediationStatusApproved:
		return to == contractsv1.RemediationStatusRunning
	case contractsv1.RemediationStatusRunning:
		return to == contractsv1.RemediationStatusMonitoring ||
			to == contractsv1.RemediationStatusSucceeded ||
			to == contractsv1.RemediationStatusFailed ||
			to == contractsv1.RemediationStatusRolledBack ||
			to == contractsv1.RemediationStatusEscalated
	case contractsv1.RemediationStatusMonitoring:
		return to == contractsv1.RemediationStatusSucceeded ||
			to == contractsv1.RemediationStatusFailed
	case contractsv1.RemediationStatusFailed:
		return to == contractsv1.RemediationStatusRolledBack ||
			to == contractsv1.RemediationStatusEscalated
	default:
		return false
	}
}

func ValidateRemediationTransition(from contractsv1.RemediationStatus, to contractsv1.RemediationStatus) error {
	if CanMoveRemediation(from, to) {
		return nil
	}
	return fmt.Errorf("invalid remediation transition %q -> %q", from, to)
}

func CanMoveApproval(from contractsv1.ApprovalStatus, to contractsv1.ApprovalStatus) bool {
	if from == to {
		return true
	}
	return from == contractsv1.ApprovalStatusPending &&
		(to == contractsv1.ApprovalStatusApproved ||
			to == contractsv1.ApprovalStatusDenied ||
			to == contractsv1.ApprovalStatusExpired)
}

func ValidateApprovalTransition(from contractsv1.ApprovalStatus, to contractsv1.ApprovalStatus) error {
	if CanMoveApproval(from, to) {
		return nil
	}
	return fmt.Errorf("invalid approval transition %q -> %q", from, to)
}
