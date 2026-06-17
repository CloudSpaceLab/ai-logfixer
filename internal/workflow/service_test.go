package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/store"
)

func TestMoveRemediationWritesAuditAndOutboxInOneTransaction(t *testing.T) {
	t.Parallel()

	fake := newFakeWorkflowStore()
	svc := NewService(fake)
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	svc.SetClock(func() time.Time { return now })
	svc.SetIDGenerator(func() (string, error) {
		return "11111111-1111-4111-8111-111111111111", nil
	})

	err := svc.MoveRemediation(context.Background(), RemediationTransition{
		TenantID:     "tenant-1",
		ResourceType: ResourceRemediationPlan,
		ResourceID:   "plan-1",
		From:         contractsv1.RemediationStatusPlanning,
		To:           contractsv1.RemediationStatusAwaitingApproval,
		Metadata: TransitionMetadata{
			ActorID:       "operator-1",
			CorrelationID: "corr-1",
			Message:       "approval required before execution",
		},
	})
	if err != nil {
		t.Fatalf("expected transition to succeed: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("expected one store transaction, got %d", fake.calls)
	}
	if got, want := fake.tx.remediationPlans.last.to, contractsv1.RemediationStatusAwaitingApproval; got != want {
		t.Fatalf("unexpected remediation target status: got %s want %s", got, want)
	}
	if len(fake.tx.audit.events) != 1 {
		t.Fatalf("expected one audit event, got %d", len(fake.tx.audit.events))
	}
	audit := fake.tx.audit.events[0]
	if audit.ActorID != "operator-1" || audit.BeforeState != "planning" || audit.AfterState != "awaiting_approval" {
		t.Fatalf("unexpected audit event: %+v", audit)
	}
	if len(fake.tx.outbox.events) != 1 {
		t.Fatalf("expected one outbox event, got %d", len(fake.tx.outbox.events))
	}
	outbox := fake.tx.outbox.events[0]
	if outbox.ID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("unexpected outbox id: %s", outbox.ID)
	}
	if outbox.IdempotencyKey != "tenant-1:remediation_plan:plan-1:planning:awaiting_approval" {
		t.Fatalf("unexpected idempotency key: %s", outbox.IdempotencyKey)
	}
	var payload map[string]string
	if err := json.Unmarshal(outbox.PayloadJSON, &payload); err != nil {
		t.Fatalf("expected JSON outbox payload: %v", err)
	}
	if payload["resource_id"] != "plan-1" || payload["after_state"] != "awaiting_approval" {
		t.Fatalf("unexpected outbox payload: %+v", payload)
	}
}

func TestInvalidRemediationTransitionDoesNotOpenTransaction(t *testing.T) {
	t.Parallel()

	fake := newFakeWorkflowStore()
	svc := NewService(fake)
	err := svc.MoveRemediation(context.Background(), RemediationTransition{
		TenantID:     "tenant-1",
		ResourceType: ResourceRemediationPlan,
		ResourceID:   "plan-1",
		From:         contractsv1.RemediationStatusSucceeded,
		To:           contractsv1.RemediationStatusRunning,
	})
	if err == nil {
		t.Fatal("expected invalid transition error")
	}
	if fake.calls != 0 {
		t.Fatalf("expected no store transaction for invalid transition, got %d", fake.calls)
	}
}

func TestDecideApprovalUsesActorForAuditWhenMetadataActorIsEmpty(t *testing.T) {
	t.Parallel()

	fake := newFakeWorkflowStore()
	svc := NewService(fake)
	decidedAt := time.Date(2026, 5, 25, 13, 0, 0, 0, time.UTC)
	svc.SetIDGenerator(func() (string, error) {
		return "22222222-2222-4222-8222-222222222222", nil
	})

	err := svc.DecideApproval(context.Background(), ApprovalDecision{
		TenantID:          "tenant-1",
		ApprovalRequestID: "approval-1",
		From:              contractsv1.ApprovalStatusPending,
		To:                contractsv1.ApprovalStatusApproved,
		ActorID:           "approver-1",
		DecidedAt:         decidedAt,
	})
	if err != nil {
		t.Fatalf("expected approval decision to succeed: %v", err)
	}
	if fake.tx.approvals.actorID != "approver-1" {
		t.Fatalf("unexpected approval actor: %s", fake.tx.approvals.actorID)
	}
	if !fake.tx.approvals.decidedAt.Equal(decidedAt) {
		t.Fatalf("unexpected decided_at: %s", fake.tx.approvals.decidedAt)
	}
	if len(fake.tx.audit.events) != 1 {
		t.Fatalf("expected one audit event, got %d", len(fake.tx.audit.events))
	}
	if fake.tx.audit.events[0].ActorID != "approver-1" {
		t.Fatalf("expected audit actor to inherit approval actor, got %s", fake.tx.audit.events[0].ActorID)
	}
}

func newFakeWorkflowStore() *fakeWorkflowStore {
	return &fakeWorkflowStore{tx: &fakeTx{}}
}

type fakeWorkflowStore struct {
	tx    *fakeTx
	calls int
	err   error
}

func (s *fakeWorkflowStore) WithinTx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	s.calls++
	if s.err != nil {
		return s.err
	}
	return fn(ctx, s.tx)
}

type fakeTx struct {
	remediationPlans      fakeRemediationPlanRepo
	remediationAttempts   fakeRemediationAttemptRepo
	investigationClusters fakeInvestigationClusterRepo
	investigationBranches fakeInvestigationBranchRepo
	approvals             fakeApprovalRepo
	audit                 fakeAuditRepo
	outbox                fakeOutboxRepo
}

func (t *fakeTx) SignalEvents() store.SignalEventRepository {
	return nil
}

func (t *fakeTx) SignalFingerprints() store.SignalFingerprintRepository {
	return nil
}

func (t *fakeTx) InvestigationRequests() store.InvestigationRequestRepository {
	return nil
}

func (t *fakeTx) InvestigationClusters() store.InvestigationClusterRepository {
	return &t.investigationClusters
}

func (t *fakeTx) InvestigationBranches() store.InvestigationBranchRepository {
	return &t.investigationBranches
}

func (t *fakeTx) InvestigationDecisions() store.InvestigationDecisionRepository {
	return nil
}

func (t *fakeTx) DiagnosisResults() store.DiagnosisResultRepository {
	return nil
}

func (t *fakeTx) RemediationPlans() store.RemediationPlanRepository {
	return &t.remediationPlans
}

func (t *fakeTx) ApprovalRequests() store.ApprovalRequestRepository {
	return &t.approvals
}

func (t *fakeTx) RemediationAttempts() store.RemediationAttemptRepository {
	return &t.remediationAttempts
}

func (t *fakeTx) Receipts() store.ReceiptRepository {
	return nil
}

func (t *fakeTx) AuditEvents() store.AuditEventRepository {
	return &t.audit
}

func (t *fakeTx) WorkflowLeases() store.WorkflowLeaseRepository {
	return nil
}

func (t *fakeTx) OutboxEvents() store.OutboxEventRepository {
	return &t.outbox
}

type remediationCall struct {
	tenantID string
	id       string
	from     contractsv1.RemediationStatus
	to       contractsv1.RemediationStatus
}

type fakeRemediationPlanRepo struct {
	last remediationCall
	err  error
}

func (r *fakeRemediationPlanRepo) Create(context.Context, store.ContractRecord[contractsv1.RemediationPlan]) error {
	return errors.New("unexpected Create call")
}

func (r *fakeRemediationPlanRepo) Get(context.Context, string, string) (store.ContractRecord[contractsv1.RemediationPlan], error) {
	return store.ContractRecord[contractsv1.RemediationPlan]{}, errors.New("unexpected Get call")
}

func (r *fakeRemediationPlanRepo) UpdateStatus(_ context.Context, tenantID string, id string, from contractsv1.RemediationStatus, to contractsv1.RemediationStatus) error {
	r.last = remediationCall{tenantID: tenantID, id: id, from: from, to: to}
	return r.err
}

type fakeRemediationAttemptRepo struct {
	last remediationCall
	err  error
}

func (r *fakeRemediationAttemptRepo) Create(context.Context, store.ContractRecord[contractsv1.RemediationAttempt]) error {
	return errors.New("unexpected Create call")
}

func (r *fakeRemediationAttemptRepo) Get(context.Context, string, string) (store.ContractRecord[contractsv1.RemediationAttempt], error) {
	return store.ContractRecord[contractsv1.RemediationAttempt]{}, errors.New("unexpected Get call")
}

func (r *fakeRemediationAttemptRepo) UpdateStatus(_ context.Context, tenantID string, id string, from contractsv1.RemediationStatus, to contractsv1.RemediationStatus) error {
	r.last = remediationCall{tenantID: tenantID, id: id, from: from, to: to}
	return r.err
}

type investigationCall struct {
	tenantID string
	id       string
	from     contractsv1.InvestigationStatus
	to       contractsv1.InvestigationStatus
}

type fakeInvestigationClusterRepo struct {
	last investigationCall
	err  error
}

func (r *fakeInvestigationClusterRepo) Create(context.Context, store.ContractRecord[contractsv1.InvestigationCluster]) error {
	return errors.New("unexpected Create call")
}

func (r *fakeInvestigationClusterRepo) Get(context.Context, string, string) (store.ContractRecord[contractsv1.InvestigationCluster], error) {
	return store.ContractRecord[contractsv1.InvestigationCluster]{}, errors.New("unexpected Get call")
}

func (r *fakeInvestigationClusterRepo) UpdateStatus(_ context.Context, tenantID string, id string, from contractsv1.InvestigationStatus, to contractsv1.InvestigationStatus) error {
	r.last = investigationCall{tenantID: tenantID, id: id, from: from, to: to}
	return r.err
}

type fakeInvestigationBranchRepo struct {
	last investigationCall
	err  error
}

func (r *fakeInvestigationBranchRepo) Create(context.Context, store.ContractRecord[contractsv1.InvestigationBranch]) error {
	return errors.New("unexpected Create call")
}

func (r *fakeInvestigationBranchRepo) Get(context.Context, string, string) (store.ContractRecord[contractsv1.InvestigationBranch], error) {
	return store.ContractRecord[contractsv1.InvestigationBranch]{}, errors.New("unexpected Get call")
}

func (r *fakeInvestigationBranchRepo) UpdateStatus(_ context.Context, tenantID string, id string, from contractsv1.InvestigationStatus, to contractsv1.InvestigationStatus) error {
	r.last = investigationCall{tenantID: tenantID, id: id, from: from, to: to}
	return r.err
}

type fakeApprovalRepo struct {
	actorID   string
	decidedAt time.Time
	err       error
}

func (r *fakeApprovalRepo) Create(context.Context, store.ContractRecord[contractsv1.ApprovalRequest]) error {
	return errors.New("unexpected Create call")
}

func (r *fakeApprovalRepo) Get(context.Context, string, string) (store.ContractRecord[contractsv1.ApprovalRequest], error) {
	return store.ContractRecord[contractsv1.ApprovalRequest]{}, errors.New("unexpected Get call")
}

func (r *fakeApprovalRepo) UpdateStatus(_ context.Context, _ string, _ string, _ contractsv1.ApprovalStatus, _ contractsv1.ApprovalStatus, actorID string, decidedAt time.Time) error {
	r.actorID = actorID
	r.decidedAt = decidedAt
	return r.err
}

type fakeAuditRepo struct {
	events []store.AuditEvent
	err    error
}

func (r *fakeAuditRepo) Append(_ context.Context, event store.AuditEvent) error {
	r.events = append(r.events, event)
	return r.err
}

type fakeOutboxRepo struct {
	events []store.OutboxEvent
	err    error
}

func (r *fakeOutboxRepo) Append(_ context.Context, event store.OutboxEvent) error {
	r.events = append(r.events, event)
	return r.err
}

func (r *fakeOutboxRepo) ClaimDue(context.Context, string, string, int, time.Time) ([]store.OutboxEvent, error) {
	return nil, errors.New("unexpected ClaimDue call")
}

func (r *fakeOutboxRepo) MarkPublished(context.Context, string, string, time.Time) error {
	return errors.New("unexpected MarkPublished call")
}

func (r *fakeOutboxRepo) MarkFailed(context.Context, string, string, time.Time, string) error {
	return errors.New("unexpected MarkFailed call")
}
