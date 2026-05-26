package intake

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/engine"
	"github.com/CloudSpaceLab/ai-logfixer/internal/store"
)

func TestStartNewInvestigationPersistsDurableIntakeRecords(t *testing.T) {
	t.Parallel()

	fake := newFakeStore()
	svc := NewService(fake)
	now := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)
	svc.SetClock(func() time.Time { return now })
	svc.SetOutboxIDGenerator(func() (string, error) {
		return "11111111-1111-4111-8111-111111111111", nil
	})

	start := now.Add(-2 * time.Minute)
	result, err := svc.StartNewInvestigation(context.Background(), StartInput{
		TenantID:      "tenant-1",
		EnvironmentID: "env-1",
		ServiceID:     "service-1",
		ServiceName:   "checkout-api",
		SourceName:    "http-log-detector",
		RequestedBy:   "ai-logfixer",
		ActorID:       "detector-worker-1",
		CorrelationID: "corr-1",
		Signal: engine.IncidentSignal{
			Service:     "checkout-api",
			Source:      "/var/log/checkout/access.log",
			Kind:        "http_failure",
			Method:      "GET",
			Route:       "/checkout",
			StatusCode:  503,
			StatusClass: 500,
			Count:       4,
			Start:       start,
			End:         now,
			Tags:        []string{"http", "status_class=500", "deploy=v42"},
		},
	})
	if err != nil {
		t.Fatalf("expected durable intake to succeed: %v", err)
	}

	if fake.calls != 1 {
		t.Fatalf("expected one transaction, got %d", fake.calls)
	}
	if len(fake.tx.requests.records) != 1 {
		t.Fatalf("expected one request record, got %d", len(fake.tx.requests.records))
	}
	if len(fake.tx.signalEvents.records) != 1 {
		t.Fatalf("expected one signal event, got %d", len(fake.tx.signalEvents.records))
	}
	if len(fake.tx.signalFingerprints.records) != 1 {
		t.Fatalf("expected one signal fingerprint, got %d", len(fake.tx.signalFingerprints.records))
	}
	if len(fake.tx.clusters.records) != 1 {
		t.Fatalf("expected one cluster record, got %d", len(fake.tx.clusters.records))
	}
	if len(fake.tx.branches.records) != 1 {
		t.Fatalf("expected one branch record, got %d", len(fake.tx.branches.records))
	}
	if len(fake.tx.decisions.records) != 1 {
		t.Fatalf("expected one decision record, got %d", len(fake.tx.decisions.records))
	}

	requestRecord := fake.tx.requests.records[0]
	if requestRecord.ID != result.InvestigationRequest.ID {
		t.Fatalf("request record id mismatch: got %s want %s", requestRecord.ID, result.InvestigationRequest.ID)
	}
	if requestRecord.IdempotencyKey != result.InvestigationRequest.ID {
		t.Fatalf("expected request idempotency key to default to request id, got %s", requestRecord.IdempotencyKey)
	}
	signalEvent := fake.tx.signalEvents.records[0]
	if signalEvent.FingerprintHash == "" || signalEvent.IdempotencyKey == "" {
		t.Fatalf("expected signal event fingerprint/idempotency, got %+v", signalEvent)
	}
	fingerprint := fake.tx.signalFingerprints.records[0]
	if fingerprint.FingerprintHash != signalEvent.FingerprintHash || fingerprint.SampleEventID != signalEvent.ID {
		t.Fatalf("fingerprint not linked to signal event: event=%+v fingerprint=%+v", signalEvent, fingerprint)
	}
	if fingerprint.OccurrenceCount != 4 {
		t.Fatalf("expected occurrence count 4, got %d", fingerprint.OccurrenceCount)
	}
	if got, want := result.InvestigationRequest.SignalFingerprint.DeployVersion, "v42"; got != want {
		t.Fatalf("unexpected deploy version: got %s want %s", got, want)
	}
	if err := result.InvestigationRequest.Validate(); err != nil {
		t.Fatalf("request should be contract-valid: %v", err)
	}
	if err := result.Decision.Validate(); err != nil {
		t.Fatalf("decision should be contract-valid: %v", err)
	}

	branchRecord := fake.tx.branches.records[0]
	if branchRecord.Relations.RequestID != result.InvestigationRequest.ID || branchRecord.Relations.ClusterID != result.Cluster.ID {
		t.Fatalf("branch relations not wired: %+v", branchRecord.Relations)
	}
	decisionRecord := fake.tx.decisions.records[0]
	if decisionRecord.Relations.BranchID != result.Branch.ID || decisionRecord.Status != "start_new" {
		t.Fatalf("decision record not wired: status=%s relations=%+v", decisionRecord.Status, decisionRecord.Relations)
	}
	if len(fake.tx.audit.events) != 1 {
		t.Fatalf("expected one audit event, got %d", len(fake.tx.audit.events))
	}
	if fake.tx.audit.events[0].EventType != EventInvestigationStarted {
		t.Fatalf("unexpected audit event type: %s", fake.tx.audit.events[0].EventType)
	}
	if len(fake.tx.outbox.events) != 1 {
		t.Fatalf("expected one outbox event, got %d", len(fake.tx.outbox.events))
	}
	outbox := fake.tx.outbox.events[0]
	if outbox.ID != "11111111-1111-4111-8111-111111111111" || outbox.EventType != EventInvestigationStarted {
		t.Fatalf("unexpected outbox event: %+v", outbox)
	}
	var payload map[string]string
	if err := json.Unmarshal(outbox.PayloadJSON, &payload); err != nil {
		t.Fatalf("outbox payload should be json: %v", err)
	}
	if payload["request_id"] != result.InvestigationRequest.ID || payload["decision_id"] != result.Decision.ID {
		t.Fatalf("outbox payload missing intake ids: %+v", payload)
	}
}

func TestStartNewInvestigationValidationFailureDoesNotOpenTransaction(t *testing.T) {
	t.Parallel()

	fake := newFakeStore()
	svc := NewService(fake)
	_, err := svc.StartNewInvestigation(context.Background(), StartInput{
		EnvironmentID: "env-1",
		ServiceID:     "service-1",
		ServiceName:   "checkout-api",
		Signal: engine.IncidentSignal{
			Service: "checkout-api",
			Source:  "access.log",
			Kind:    "http_failure",
		},
	})
	if err == nil {
		t.Fatal("expected missing tenant validation error")
	}
	if fake.calls != 0 {
		t.Fatalf("expected no transaction on validation failure, got %d", fake.calls)
	}
}

func TestStartNewInvestigationCanSuppressOutbox(t *testing.T) {
	t.Parallel()

	fake := newFakeStore()
	svc := NewService(fake)
	_, err := svc.StartNewInvestigation(context.Background(), validStartInput("corr-suppressed", true))
	if err != nil {
		t.Fatalf("expected intake to succeed: %v", err)
	}
	if len(fake.tx.audit.events) != 1 {
		t.Fatalf("expected audit event even when outbox is suppressed, got %d", len(fake.tx.audit.events))
	}
	if len(fake.tx.outbox.events) != 0 {
		t.Fatalf("expected no outbox events when suppressed, got %d", len(fake.tx.outbox.events))
	}
}

func TestStartNewInvestigationUsesStableContractIDs(t *testing.T) {
	t.Parallel()

	input := validStartInput("corr-stable", true)
	now := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)

	firstStore := newFakeStore()
	first := NewService(firstStore)
	first.SetClock(func() time.Time { return now })
	firstResult, err := first.StartNewInvestigation(context.Background(), input)
	if err != nil {
		t.Fatalf("first intake failed: %v", err)
	}

	secondStore := newFakeStore()
	second := NewService(secondStore)
	second.SetClock(func() time.Time { return now })
	secondResult, err := second.StartNewInvestigation(context.Background(), input)
	if err != nil {
		t.Fatalf("second intake failed: %v", err)
	}

	if firstResult.InvestigationRequest.ID != secondResult.InvestigationRequest.ID {
		t.Fatalf("request ids should be stable: got %s and %s", firstResult.InvestigationRequest.ID, secondResult.InvestigationRequest.ID)
	}
	if firstResult.Cluster.ID != secondResult.Cluster.ID || firstResult.Branch.ID != secondResult.Branch.ID || firstResult.Decision.ID != secondResult.Decision.ID {
		t.Fatalf("cluster/branch/decision ids should be stable: first=%s/%s/%s second=%s/%s/%s",
			firstResult.Cluster.ID, firstResult.Branch.ID, firstResult.Decision.ID,
			secondResult.Cluster.ID, secondResult.Branch.ID, secondResult.Decision.ID)
	}
}

func TestSignalFingerprintIgnoresCorrelationID(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)
	firstStore := newFakeStore()
	first := NewService(firstStore)
	first.SetClock(func() time.Time { return now })
	if _, err := first.StartNewInvestigation(context.Background(), validStartInput("corr-one", true)); err != nil {
		t.Fatalf("first intake failed: %v", err)
	}

	secondStore := newFakeStore()
	second := NewService(secondStore)
	second.SetClock(func() time.Time { return now })
	if _, err := second.StartNewInvestigation(context.Background(), validStartInput("corr-two", true)); err != nil {
		t.Fatalf("second intake failed: %v", err)
	}

	firstHash := firstStore.tx.signalEvents.records[0].FingerprintHash
	secondHash := secondStore.tx.signalEvents.records[0].FingerprintHash
	if firstHash != secondHash {
		t.Fatalf("fingerprint hash should ignore correlation id: got %s and %s", firstHash, secondHash)
	}
}

func validStartInput(correlationID string, suppressOutbox bool) StartInput {
	now := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)
	return StartInput{
		TenantID:       "tenant-1",
		EnvironmentID:  "env-1",
		ServiceID:      "service-1",
		ServiceName:    "checkout-api",
		CorrelationID:  correlationID,
		SuppressOutbox: suppressOutbox,
		Signal: engine.IncidentSignal{
			Service:     "checkout-api",
			Source:      "access.log",
			Kind:        "http_failure",
			Method:      "GET",
			Route:       "/checkout",
			StatusCode:  503,
			StatusClass: 500,
			Count:       3,
			Start:       now.Add(-time.Minute),
			End:         now,
			Tags:        []string{"http", "status_class=500"},
		},
	}
}

func newFakeStore() *fakeStore {
	return &fakeStore{tx: &fakeTx{}}
}

type fakeStore struct {
	tx    *fakeTx
	calls int
	err   error
}

func (s *fakeStore) WithinTx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	s.calls++
	if s.err != nil {
		return s.err
	}
	return fn(ctx, s.tx)
}

type fakeTx struct {
	signalEvents       fakeSignalEventRepo
	signalFingerprints fakeSignalFingerprintRepo
	requests           fakeRequestRepo
	clusters           fakeClusterRepo
	branches           fakeBranchRepo
	decisions          fakeDecisionRepo
	audit              fakeAuditRepo
	outbox             fakeOutboxRepo
}

func (t *fakeTx) SignalEvents() store.SignalEventRepository {
	return &t.signalEvents
}

func (t *fakeTx) SignalFingerprints() store.SignalFingerprintRepository {
	return &t.signalFingerprints
}

func (t *fakeTx) InvestigationRequests() store.InvestigationRequestRepository {
	return &t.requests
}

func (t *fakeTx) InvestigationClusters() store.InvestigationClusterRepository {
	return &t.clusters
}

func (t *fakeTx) InvestigationBranches() store.InvestigationBranchRepository {
	return &t.branches
}

func (t *fakeTx) InvestigationDecisions() store.InvestigationDecisionRepository {
	return &t.decisions
}

func (t *fakeTx) DiagnosisResults() store.DiagnosisResultRepository {
	return nil
}

func (t *fakeTx) RemediationPlans() store.RemediationPlanRepository {
	return nil
}

func (t *fakeTx) ApprovalRequests() store.ApprovalRequestRepository {
	return nil
}

func (t *fakeTx) RemediationAttempts() store.RemediationAttemptRepository {
	return nil
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

type fakeSignalEventRepo struct {
	records []store.SignalEvent
}

func (r *fakeSignalEventRepo) Create(_ context.Context, event store.SignalEvent) (store.SignalEvent, error) {
	if event.ID == "" {
		event.ID = "signal-event-1"
	}
	r.records = append(r.records, event)
	return event, nil
}

func (r *fakeSignalEventRepo) Get(context.Context, string, string) (store.SignalEvent, error) {
	return store.SignalEvent{}, errors.New("unexpected Get call")
}

type fakeSignalFingerprintRepo struct {
	records []store.SignalFingerprint
}

func (r *fakeSignalFingerprintRepo) Upsert(_ context.Context, fingerprint store.SignalFingerprint) (store.SignalFingerprint, error) {
	if fingerprint.ID == "" {
		fingerprint.ID = "signal-fingerprint-1"
	}
	r.records = append(r.records, fingerprint)
	return fingerprint, nil
}

func (r *fakeSignalFingerprintRepo) GetByHash(context.Context, string, string, string) (store.SignalFingerprint, error) {
	return store.SignalFingerprint{}, errors.New("unexpected GetByHash call")
}

type fakeRequestRepo struct {
	records []store.ContractRecord[contractsv1.InvestigationRequest]
}

func (r *fakeRequestRepo) Create(_ context.Context, record store.ContractRecord[contractsv1.InvestigationRequest]) error {
	r.records = append(r.records, record)
	return nil
}

func (r *fakeRequestRepo) Get(context.Context, string, string) (store.ContractRecord[contractsv1.InvestigationRequest], error) {
	return store.ContractRecord[contractsv1.InvestigationRequest]{}, errors.New("unexpected Get call")
}

type fakeClusterRepo struct {
	records []store.ContractRecord[contractsv1.InvestigationCluster]
}

func (r *fakeClusterRepo) Create(_ context.Context, record store.ContractRecord[contractsv1.InvestigationCluster]) error {
	r.records = append(r.records, record)
	return nil
}

func (r *fakeClusterRepo) Get(context.Context, string, string) (store.ContractRecord[contractsv1.InvestigationCluster], error) {
	return store.ContractRecord[contractsv1.InvestigationCluster]{}, errors.New("unexpected Get call")
}

func (r *fakeClusterRepo) UpdateStatus(context.Context, string, string, contractsv1.InvestigationStatus, contractsv1.InvestigationStatus) error {
	return errors.New("unexpected UpdateStatus call")
}

type fakeBranchRepo struct {
	records []store.ContractRecord[contractsv1.InvestigationBranch]
}

func (r *fakeBranchRepo) Create(_ context.Context, record store.ContractRecord[contractsv1.InvestigationBranch]) error {
	r.records = append(r.records, record)
	return nil
}

func (r *fakeBranchRepo) Get(context.Context, string, string) (store.ContractRecord[contractsv1.InvestigationBranch], error) {
	return store.ContractRecord[contractsv1.InvestigationBranch]{}, errors.New("unexpected Get call")
}

func (r *fakeBranchRepo) UpdateStatus(context.Context, string, string, contractsv1.InvestigationStatus, contractsv1.InvestigationStatus) error {
	return errors.New("unexpected UpdateStatus call")
}

type fakeDecisionRepo struct {
	records []store.ContractRecord[contractsv1.InvestigationDecision]
}

func (r *fakeDecisionRepo) Create(_ context.Context, record store.ContractRecord[contractsv1.InvestigationDecision]) error {
	r.records = append(r.records, record)
	return nil
}

func (r *fakeDecisionRepo) Get(context.Context, string, string) (store.ContractRecord[contractsv1.InvestigationDecision], error) {
	return store.ContractRecord[contractsv1.InvestigationDecision]{}, errors.New("unexpected Get call")
}

type fakeAuditRepo struct {
	events []store.AuditEvent
}

func (r *fakeAuditRepo) Append(_ context.Context, event store.AuditEvent) error {
	r.events = append(r.events, event)
	return nil
}

type fakeOutboxRepo struct {
	events []store.OutboxEvent
}

func (r *fakeOutboxRepo) Append(_ context.Context, event store.OutboxEvent) error {
	r.events = append(r.events, event)
	return nil
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
