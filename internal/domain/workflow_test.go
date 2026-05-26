package domain

import (
	"testing"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
)

func TestInvestigationTransitions(t *testing.T) {
	t.Parallel()

	valid := []struct {
		from contractsv1.InvestigationStatus
		to   contractsv1.InvestigationStatus
	}{
		{contractsv1.InvestigationStatusRequested, contractsv1.InvestigationStatusFingerprinted},
		{contractsv1.InvestigationStatusFingerprinted, contractsv1.InvestigationStatusRunning},
		{contractsv1.InvestigationStatusFingerprinted, contractsv1.InvestigationStatusQueued},
		{contractsv1.InvestigationStatusQueued, contractsv1.InvestigationStatusRunning},
		{contractsv1.InvestigationStatusRunning, contractsv1.InvestigationStatusCompleted},
		{contractsv1.InvestigationStatusRunning, contractsv1.InvestigationStatusNeedsMoreData},
		{contractsv1.InvestigationStatusNeedsMoreData, contractsv1.InvestigationStatusRunning},
		{contractsv1.InvestigationStatusCompleted, contractsv1.InvestigationStatusCompleted},
	}
	for _, item := range valid {
		if err := ValidateInvestigationTransition(item.from, item.to); err != nil {
			t.Fatalf("expected %s -> %s to be valid: %v", item.from, item.to, err)
		}
	}

	invalid := []struct {
		from contractsv1.InvestigationStatus
		to   contractsv1.InvestigationStatus
	}{
		{contractsv1.InvestigationStatusRequested, contractsv1.InvestigationStatusCompleted},
		{contractsv1.InvestigationStatusCompleted, contractsv1.InvestigationStatusRunning},
		{contractsv1.InvestigationStatusRejected, contractsv1.InvestigationStatusRunning},
	}
	for _, item := range invalid {
		if err := ValidateInvestigationTransition(item.from, item.to); err == nil {
			t.Fatalf("expected %s -> %s to be invalid", item.from, item.to)
		}
	}
}

func TestRemediationTransitions(t *testing.T) {
	t.Parallel()

	valid := []struct {
		from contractsv1.RemediationStatus
		to   contractsv1.RemediationStatus
	}{
		{contractsv1.RemediationStatusPending, contractsv1.RemediationStatusPlanning},
		{contractsv1.RemediationStatusPlanning, contractsv1.RemediationStatusAwaitingApproval},
		{contractsv1.RemediationStatusAwaitingApproval, contractsv1.RemediationStatusApproved},
		{contractsv1.RemediationStatusApproved, contractsv1.RemediationStatusRunning},
		{contractsv1.RemediationStatusRunning, contractsv1.RemediationStatusMonitoring},
		{contractsv1.RemediationStatusMonitoring, contractsv1.RemediationStatusSucceeded},
		{contractsv1.RemediationStatusRunning, contractsv1.RemediationStatusFailed},
		{contractsv1.RemediationStatusFailed, contractsv1.RemediationStatusRolledBack},
		{contractsv1.RemediationStatusSucceeded, contractsv1.RemediationStatusSucceeded},
	}
	for _, item := range valid {
		if err := ValidateRemediationTransition(item.from, item.to); err != nil {
			t.Fatalf("expected %s -> %s to be valid: %v", item.from, item.to, err)
		}
	}

	invalid := []struct {
		from contractsv1.RemediationStatus
		to   contractsv1.RemediationStatus
	}{
		{contractsv1.RemediationStatusAwaitingApproval, contractsv1.RemediationStatusRunning},
		{contractsv1.RemediationStatusSucceeded, contractsv1.RemediationStatusRunning},
		{contractsv1.RemediationStatusDenied, contractsv1.RemediationStatusApproved},
	}
	for _, item := range invalid {
		if err := ValidateRemediationTransition(item.from, item.to); err == nil {
			t.Fatalf("expected %s -> %s to be invalid", item.from, item.to)
		}
	}
}

func TestApprovalTransitions(t *testing.T) {
	t.Parallel()

	if err := ValidateApprovalTransition(contractsv1.ApprovalStatusPending, contractsv1.ApprovalStatusApproved); err != nil {
		t.Fatalf("expected pending -> approved to be valid: %v", err)
	}
	if err := ValidateApprovalTransition(contractsv1.ApprovalStatusPending, contractsv1.ApprovalStatusExpired); err != nil {
		t.Fatalf("expected pending -> expired to be valid: %v", err)
	}
	if err := ValidateApprovalTransition(contractsv1.ApprovalStatusApproved, contractsv1.ApprovalStatusDenied); err == nil {
		t.Fatal("expected approved -> denied to be invalid")
	}
}
