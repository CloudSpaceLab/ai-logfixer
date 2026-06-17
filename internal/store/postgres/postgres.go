package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/domain"
	"github.com/CloudSpaceLab/ai-logfixer/internal/store"
)

var _ store.WorkflowStore = (*Store)(nil)
var _ store.Tx = (*workflowTx)(nil)

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) WithinTx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin workflow transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if err := fn(ctx, &workflowTx{q: tx}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit workflow transaction: %w", err)
	}
	committed = true
	return nil
}

type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type workflowTx struct {
	q queryer
}

func (t *workflowTx) SignalEvents() store.SignalEventRepository {
	return signalEventRepo{q: t.q}
}

func (t *workflowTx) SignalFingerprints() store.SignalFingerprintRepository {
	return signalFingerprintRepo{q: t.q}
}

func (t *workflowTx) InvestigationRequests() store.InvestigationRequestRepository {
	return investigationRequestRepo{q: t.q}
}

func (t *workflowTx) InvestigationClusters() store.InvestigationClusterRepository {
	return investigationClusterRepo{q: t.q}
}

func (t *workflowTx) InvestigationBranches() store.InvestigationBranchRepository {
	return investigationBranchRepo{q: t.q}
}

func (t *workflowTx) InvestigationDecisions() store.InvestigationDecisionRepository {
	return investigationDecisionRepo{q: t.q}
}

func (t *workflowTx) DiagnosisResults() store.DiagnosisResultRepository {
	return diagnosisResultRepo{q: t.q}
}

func (t *workflowTx) RemediationPlans() store.RemediationPlanRepository {
	return remediationPlanRepo{q: t.q}
}

func (t *workflowTx) ApprovalRequests() store.ApprovalRequestRepository {
	return approvalRequestRepo{q: t.q}
}

func (t *workflowTx) RemediationAttempts() store.RemediationAttemptRepository {
	return remediationAttemptRepo{q: t.q}
}

func (t *workflowTx) Receipts() store.ReceiptRepository {
	return receiptRepo{q: t.q}
}

func (t *workflowTx) AuditEvents() store.AuditEventRepository {
	return auditEventRepo{q: t.q}
}

func (t *workflowTx) WorkflowLeases() store.WorkflowLeaseRepository {
	return workflowLeaseRepo{q: t.q}
}

func (t *workflowTx) OutboxEvents() store.OutboxEventRepository {
	return outboxEventRepo{q: t.q}
}

type investigationRequestRepo struct {
	q queryer
}

type signalEventRepo struct {
	q queryer
}

func (r signalEventRepo) Create(ctx context.Context, event store.SignalEvent) (store.SignalEvent, error) {
	if err := require("signal event tenant id", event.TenantID); err != nil {
		return store.SignalEvent{}, err
	}
	if err := require("signal event environment id", event.EnvironmentID); err != nil {
		return store.SignalEvent{}, err
	}
	if err := require("signal event service id", event.ServiceID); err != nil {
		return store.SignalEvent{}, err
	}
	if err := require("signal event source", event.Source); err != nil {
		return store.SignalEvent{}, err
	}
	if err := require("signal event idempotency key", event.IdempotencyKey); err != nil {
		return store.SignalEvent{}, err
	}
	observedAt := firstTime(event.ObservedAt, time.Now())
	receivedAt := firstTime(event.ReceivedAt, observedAt)
	row := r.q.QueryRowContext(ctx, `
INSERT INTO signal_events (
    id, tenant_id, environment_id, service_id, source, severity, route, method,
    status_code, error_class, fingerprint_hash, idempotency_key, observed_at,
    received_at, payload_json
) VALUES (
    COALESCE(NULLIF($1, '')::uuid, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8,
    $9, $10, $11, $12, $13, $14, $15
)
ON CONFLICT (tenant_id, idempotency_key)
DO UPDATE SET received_at = EXCLUDED.received_at
RETURNING id::text, tenant_id::text, environment_id::text, service_id::text,
          source, COALESCE(severity, ''), COALESCE(route, ''), COALESCE(method, ''),
          COALESCE(status_code, 0), COALESCE(error_class, ''),
          COALESCE(fingerprint_hash, ''), idempotency_key, observed_at, received_at,
          payload_json`,
		event.ID,
		event.TenantID,
		event.EnvironmentID,
		event.ServiceID,
		event.Source,
		nullString(event.Severity),
		nullString(event.Route),
		nullString(event.Method),
		nullInt(event.StatusCode),
		nullString(event.ErrorClass),
		nullString(event.FingerprintHash),
		event.IdempotencyKey,
		observedAt,
		receivedAt,
		jsonString(event.PayloadJSON),
	)
	return scanSignalEvent(row)
}

func (r signalEventRepo) Get(ctx context.Context, tenantID string, id string) (store.SignalEvent, error) {
	row := r.q.QueryRowContext(ctx, `
SELECT id::text, tenant_id::text, environment_id::text, service_id::text,
       source, COALESCE(severity, ''), COALESCE(route, ''), COALESCE(method, ''),
       COALESCE(status_code, 0), COALESCE(error_class, ''),
       COALESCE(fingerprint_hash, ''), idempotency_key, observed_at, received_at,
       payload_json
FROM signal_events
WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	return scanSignalEvent(row)
}

type signalFingerprintRepo struct {
	q queryer
}

func (r signalFingerprintRepo) Upsert(ctx context.Context, fingerprint store.SignalFingerprint) (store.SignalFingerprint, error) {
	if err := require("signal fingerprint tenant id", fingerprint.TenantID); err != nil {
		return store.SignalFingerprint{}, err
	}
	if err := require("signal fingerprint environment id", fingerprint.EnvironmentID); err != nil {
		return store.SignalFingerprint{}, err
	}
	if err := require("signal fingerprint service id", fingerprint.ServiceID); err != nil {
		return store.SignalFingerprint{}, err
	}
	if err := require("signal fingerprint hash", fingerprint.FingerprintHash); err != nil {
		return store.SignalFingerprint{}, err
	}
	status := firstNonEmpty(fingerprint.Status, "open")
	firstSeenAt := firstTime(fingerprint.FirstSeenAt, time.Now())
	lastSeenAt := firstTime(fingerprint.LastSeenAt, firstSeenAt)
	if lastSeenAt.Before(firstSeenAt) {
		lastSeenAt = firstSeenAt
	}
	occurrenceCount := fingerprint.OccurrenceCount
	if occurrenceCount == 0 {
		occurrenceCount = 1
	}
	row := r.q.QueryRowContext(ctx, `
INSERT INTO signal_fingerprints (
    id, tenant_id, environment_id, service_id, fingerprint_hash, status,
    first_seen_at, last_seen_at, occurrence_count, sample_event_id,
    metadata_json, created_at, updated_at
) VALUES (
    COALESCE(NULLIF($1, '')::uuid, gen_random_uuid()), $2, $3, $4, $5, $6,
    $7, $8, $9, NULLIF($10, '')::uuid, $11, $12, $12
)
ON CONFLICT (tenant_id, service_id, fingerprint_hash)
DO UPDATE SET
    status = EXCLUDED.status,
    first_seen_at = LEAST(signal_fingerprints.first_seen_at, EXCLUDED.first_seen_at),
    last_seen_at = GREATEST(signal_fingerprints.last_seen_at, EXCLUDED.last_seen_at),
    occurrence_count = signal_fingerprints.occurrence_count + EXCLUDED.occurrence_count,
    sample_event_id = COALESCE(EXCLUDED.sample_event_id, signal_fingerprints.sample_event_id),
    metadata_json = signal_fingerprints.metadata_json || EXCLUDED.metadata_json,
    updated_at = EXCLUDED.updated_at
RETURNING id::text, tenant_id::text, environment_id::text, service_id::text,
          fingerprint_hash, status, first_seen_at, last_seen_at, occurrence_count,
          COALESCE(sample_event_id::text, ''), metadata_json, created_at, updated_at`,
		fingerprint.ID,
		fingerprint.TenantID,
		fingerprint.EnvironmentID,
		fingerprint.ServiceID,
		fingerprint.FingerprintHash,
		status,
		firstSeenAt,
		lastSeenAt,
		occurrenceCount,
		fingerprint.SampleEventID,
		jsonString(fingerprint.MetadataJSON),
		firstTime(fingerprint.UpdatedAt, fingerprint.CreatedAt, time.Now()),
	)
	return scanSignalFingerprint(row)
}

func (r signalFingerprintRepo) GetByHash(ctx context.Context, tenantID string, serviceID string, fingerprintHash string) (store.SignalFingerprint, error) {
	row := r.q.QueryRowContext(ctx, `
SELECT id::text, tenant_id::text, environment_id::text, service_id::text,
       fingerprint_hash, status, first_seen_at, last_seen_at, occurrence_count,
       COALESCE(sample_event_id::text, ''), metadata_json, created_at, updated_at
FROM signal_fingerprints
WHERE tenant_id = $1 AND service_id = $2 AND fingerprint_hash = $3`, tenantID, serviceID, fingerprintHash)
	return scanSignalFingerprint(row)
}

func (r investigationRequestRepo) Create(ctx context.Context, record store.ContractRecord[contractsv1.InvestigationRequest]) error {
	payloadJSON, err := payloadJSON(record)
	if err != nil {
		return err
	}
	id := firstNonEmpty(record.ID, record.Payload.ID)
	if err := require("investigation request id", id); err != nil {
		return err
	}
	createdAt := firstTime(record.CreatedAt, record.Payload.CreatedAt, time.Now())
	status := firstNonEmpty(record.Status, string(contractsv1.InvestigationStatusRequested))
	idempotencyKey := firstNonEmpty(record.IdempotencyKey, id)
	source := firstNonEmpty(string(record.Payload.SourceType), record.Payload.SourceName, "unknown")

	_, err = r.q.ExecContext(ctx, `
INSERT INTO investigation_requests (
    id, tenant_id, environment_id, service_id, contract_version, status, source,
    idempotency_key, requested_at, created_at, updated_at, payload_json
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9, $9, $10)`,
		id,
		record.TenantID,
		record.EnvironmentID,
		record.ServiceID,
		contractVersion(record.ContractVersion, record.Payload.ContractVersion),
		status,
		source,
		idempotencyKey,
		createdAt,
		payloadJSON,
	)
	return wrapExec("create investigation request", err)
}

func (r investigationRequestRepo) Get(ctx context.Context, tenantID string, id string) (store.ContractRecord[contractsv1.InvestigationRequest], error) {
	return getContractRecord[contractsv1.InvestigationRequest](ctx, r.q, `
SELECT id, tenant_id::text, environment_id::text, service_id::text, contract_version, status,
       payload_json, lock_version, created_at, updated_at
FROM investigation_requests
WHERE tenant_id = $1 AND id = $2`, tenantID, id)
}

type investigationClusterRepo struct {
	q queryer
}

func (r investigationClusterRepo) Create(ctx context.Context, record store.ContractRecord[contractsv1.InvestigationCluster]) error {
	payloadJSON, err := payloadJSON(record)
	if err != nil {
		return err
	}
	id := firstNonEmpty(record.ID, record.Payload.ID)
	status := firstNonEmpty(record.Status, string(record.Payload.Status))
	createdAt := firstTime(record.CreatedAt, record.Payload.CreatedAt, time.Now())
	updatedAt := firstTime(record.UpdatedAt, record.Payload.UpdatedAt, createdAt)
	if err := require("investigation cluster id", id); err != nil {
		return err
	}
	if err := require("investigation cluster status", status); err != nil {
		return err
	}

	_, err = r.q.ExecContext(ctx, `
INSERT INTO investigation_clusters (
    id, tenant_id, environment_id, service_id, contract_version, status, summary,
    created_at, updated_at, payload_json
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		id,
		record.TenantID,
		record.EnvironmentID,
		record.ServiceID,
		contractVersion(record.ContractVersion, ""),
		status,
		record.Payload.Summary,
		createdAt,
		updatedAt,
		payloadJSON,
	)
	return wrapExec("create investigation cluster", err)
}

func (r investigationClusterRepo) Get(ctx context.Context, tenantID string, id string) (store.ContractRecord[contractsv1.InvestigationCluster], error) {
	return getContractRecord[contractsv1.InvestigationCluster](ctx, r.q, `
SELECT id, tenant_id::text, environment_id::text, service_id::text, contract_version, status,
       payload_json, lock_version, created_at, updated_at
FROM investigation_clusters
WHERE tenant_id = $1 AND id = $2`, tenantID, id)
}

func (r investigationClusterRepo) UpdateStatus(ctx context.Context, tenantID string, id string, from contractsv1.InvestigationStatus, to contractsv1.InvestigationStatus) error {
	if err := domain.ValidateInvestigationTransition(from, to); err != nil {
		return err
	}
	return updateStatus(ctx, r.q, "investigation_clusters", tenantID, id, string(from), string(to))
}

type investigationBranchRepo struct {
	q queryer
}

func (r investigationBranchRepo) Create(ctx context.Context, record store.ContractRecord[contractsv1.InvestigationBranch]) error {
	payloadJSON, err := payloadJSON(record)
	if err != nil {
		return err
	}
	id := firstNonEmpty(record.ID, record.Payload.ID)
	clusterID := firstNonEmpty(record.Relations.ClusterID, record.Payload.ClusterID)
	status := firstNonEmpty(record.Status, string(record.Payload.Status))
	createdAt := firstTime(record.CreatedAt, record.Payload.CreatedAt, time.Now())
	updatedAt := firstTime(record.UpdatedAt, record.Payload.UpdatedAt, createdAt)
	if err := require("investigation branch id", id); err != nil {
		return err
	}
	if err := require("investigation branch cluster id", clusterID); err != nil {
		return err
	}
	if err := require("investigation branch status", status); err != nil {
		return err
	}

	_, err = r.q.ExecContext(ctx, `
INSERT INTO investigation_branches (
    id, tenant_id, environment_id, service_id, cluster_id, request_id, contract_version,
    status, hypothesis, created_at, updated_at, payload_json
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		id,
		record.TenantID,
		record.EnvironmentID,
		record.ServiceID,
		clusterID,
		nullString(record.Relations.RequestID),
		contractVersion(record.ContractVersion, ""),
		status,
		record.Payload.Symptom,
		createdAt,
		updatedAt,
		payloadJSON,
	)
	return wrapExec("create investigation branch", err)
}

func (r investigationBranchRepo) Get(ctx context.Context, tenantID string, id string) (store.ContractRecord[contractsv1.InvestigationBranch], error) {
	return getContractRecord[contractsv1.InvestigationBranch](ctx, r.q, `
SELECT id, tenant_id::text, environment_id::text, service_id::text, contract_version, status,
       payload_json, lock_version, created_at, updated_at
FROM investigation_branches
WHERE tenant_id = $1 AND id = $2`, tenantID, id)
}

func (r investigationBranchRepo) UpdateStatus(ctx context.Context, tenantID string, id string, from contractsv1.InvestigationStatus, to contractsv1.InvestigationStatus) error {
	if err := domain.ValidateInvestigationTransition(from, to); err != nil {
		return err
	}
	return updateStatus(ctx, r.q, "investigation_branches", tenantID, id, string(from), string(to))
}

type investigationDecisionRepo struct {
	q queryer
}

func (r investigationDecisionRepo) Create(ctx context.Context, record store.ContractRecord[contractsv1.InvestigationDecision]) error {
	payloadJSON, err := payloadJSON(record)
	if err != nil {
		return err
	}
	id := firstNonEmpty(record.ID, record.Payload.ID)
	branchID := firstNonEmpty(record.Relations.BranchID, record.Payload.BranchID)
	decision := string(record.Payload.Decision)
	createdAt := firstTime(record.CreatedAt, record.Payload.CreatedAt, time.Now())
	if err := require("investigation decision id", id); err != nil {
		return err
	}
	if err := require("investigation decision branch id", branchID); err != nil {
		return err
	}
	if err := require("investigation decision", decision); err != nil {
		return err
	}

	_, err = r.q.ExecContext(ctx, `
INSERT INTO investigation_decisions (
    id, tenant_id, branch_id, contract_version, decision, reason, created_at, payload_json
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id,
		record.TenantID,
		branchID,
		contractVersion(record.ContractVersion, record.Payload.ContractVersion),
		decision,
		record.Payload.Explanation,
		createdAt,
		payloadJSON,
	)
	return wrapExec("create investigation decision", err)
}

func (r investigationDecisionRepo) Get(ctx context.Context, tenantID string, id string) (store.ContractRecord[contractsv1.InvestigationDecision], error) {
	return getContractRecord[contractsv1.InvestigationDecision](ctx, r.q, `
SELECT id, tenant_id::text, '' AS environment_id, '' AS service_id, contract_version, decision AS status,
       payload_json, 0::bigint AS lock_version, created_at, created_at AS updated_at
FROM investigation_decisions
WHERE tenant_id = $1 AND id = $2`, tenantID, id)
}

type diagnosisResultRepo struct {
	q queryer
}

func (r diagnosisResultRepo) Create(ctx context.Context, record store.ContractRecord[contractsv1.DiagnosisResult]) error {
	payloadJSON, err := payloadJSON(record)
	if err != nil {
		return err
	}
	id := firstNonEmpty(record.ID, record.Payload.ID)
	branchID := record.Relations.BranchID
	status := firstNonEmpty(record.Status, string(record.Payload.Status))
	createdAt := firstTime(record.CreatedAt, record.Payload.CreatedAt, time.Now())
	if err := require("diagnosis result id", id); err != nil {
		return err
	}
	if err := require("diagnosis result branch id", branchID); err != nil {
		return err
	}
	if err := require("diagnosis result status", status); err != nil {
		return err
	}

	_, err = r.q.ExecContext(ctx, `
INSERT INTO diagnosis_results (
    id, tenant_id, environment_id, service_id, branch_id, contract_version, status,
    safety_classification, confidence, summary, created_at, updated_at, payload_json
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11, $12)`,
		id,
		record.TenantID,
		record.EnvironmentID,
		record.ServiceID,
		branchID,
		contractVersion(record.ContractVersion, record.Payload.ContractVersion),
		status,
		string(record.Payload.SafetyClassification),
		record.Payload.Confidence,
		record.Payload.Summary,
		createdAt,
		payloadJSON,
	)
	return wrapExec("create diagnosis result", err)
}

func (r diagnosisResultRepo) Get(ctx context.Context, tenantID string, id string) (store.ContractRecord[contractsv1.DiagnosisResult], error) {
	return getContractRecord[contractsv1.DiagnosisResult](ctx, r.q, `
SELECT id, tenant_id::text, environment_id::text, service_id::text, contract_version, status,
       payload_json, lock_version, created_at, updated_at
FROM diagnosis_results
WHERE tenant_id = $1 AND id = $2`, tenantID, id)
}

type remediationPlanRepo struct {
	q queryer
}

func (r remediationPlanRepo) Create(ctx context.Context, record store.ContractRecord[contractsv1.RemediationPlan]) error {
	payloadJSON, err := payloadJSON(record)
	if err != nil {
		return err
	}
	id := firstNonEmpty(record.ID, record.Payload.ID)
	diagnosisID := firstNonEmpty(record.Relations.DiagnosisResultID, record.Payload.DiagnosisResultID)
	rollbackPlanID := firstNonEmpty(record.Relations.RollbackPlanID, record.Payload.RollbackPlan.ID)
	status := firstNonEmpty(record.Status, string(record.Payload.Status))
	createdAt := firstTime(record.CreatedAt, record.Payload.CreatedAt, time.Now())
	if err := require("remediation plan id", id); err != nil {
		return err
	}
	if err := require("remediation plan diagnosis result id", diagnosisID); err != nil {
		return err
	}
	if err := require("remediation plan status", status); err != nil {
		return err
	}

	_, err = r.q.ExecContext(ctx, `
INSERT INTO remediation_plans (
    id, tenant_id, environment_id, service_id, diagnosis_result_id, rollback_plan_id,
    contract_version, status, risk_level, approval_required, summary, created_at,
    updated_at, payload_json
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12, $13)`,
		id,
		record.TenantID,
		record.EnvironmentID,
		record.ServiceID,
		diagnosisID,
		nullString(rollbackPlanID),
		contractVersion(record.ContractVersion, record.Payload.ContractVersion),
		status,
		string(record.Payload.RiskLevel),
		record.Payload.ApprovalRequired,
		record.Payload.Summary,
		createdAt,
		payloadJSON,
	)
	return wrapExec("create remediation plan", err)
}

func (r remediationPlanRepo) Get(ctx context.Context, tenantID string, id string) (store.ContractRecord[contractsv1.RemediationPlan], error) {
	return getContractRecord[contractsv1.RemediationPlan](ctx, r.q, `
SELECT id, tenant_id::text, environment_id::text, service_id::text, contract_version, status,
       payload_json, lock_version, created_at, updated_at
FROM remediation_plans
WHERE tenant_id = $1 AND id = $2`, tenantID, id)
}

func (r remediationPlanRepo) UpdateStatus(ctx context.Context, tenantID string, id string, from contractsv1.RemediationStatus, to contractsv1.RemediationStatus) error {
	if err := domain.ValidateRemediationTransition(from, to); err != nil {
		return err
	}
	return updateStatus(ctx, r.q, "remediation_plans", tenantID, id, string(from), string(to))
}

type approvalRequestRepo struct {
	q queryer
}

func (r approvalRequestRepo) Create(ctx context.Context, record store.ContractRecord[contractsv1.ApprovalRequest]) error {
	payloadJSON, err := payloadJSON(record)
	if err != nil {
		return err
	}
	id := firstNonEmpty(record.ID, record.Payload.ID)
	remediationPlanID := firstNonEmpty(record.Relations.RemediationPlanID, record.Payload.RemediationPlanID)
	status := firstNonEmpty(record.Status, string(record.Payload.Status))
	requestedAt := firstTime(record.CreatedAt, time.Now())
	if err := require("approval request id", id); err != nil {
		return err
	}
	if err := require("approval request remediation plan id", remediationPlanID); err != nil {
		return err
	}
	if err := require("approval request status", status); err != nil {
		return err
	}

	_, err = r.q.ExecContext(ctx, `
INSERT INTO approval_requests (
    id, tenant_id, environment_id, service_id, remediation_plan_id, contract_version,
    status, requested_by, decided_by, requested_at, decided_at, created_at, updated_at,
    payload_json
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $10, $10, $12)`,
		id,
		record.TenantID,
		record.EnvironmentID,
		record.ServiceID,
		remediationPlanID,
		contractVersion(record.ContractVersion, ""),
		status,
		record.Payload.RequestedBy,
		nullString(record.Payload.Approver),
		requestedAt,
		record.Payload.DecidedAt,
		payloadJSON,
	)
	return wrapExec("create approval request", err)
}

func (r approvalRequestRepo) Get(ctx context.Context, tenantID string, id string) (store.ContractRecord[contractsv1.ApprovalRequest], error) {
	return getContractRecord[contractsv1.ApprovalRequest](ctx, r.q, `
SELECT id, tenant_id::text, environment_id::text, service_id::text, contract_version, status,
       payload_json, lock_version, created_at, updated_at
FROM approval_requests
WHERE tenant_id = $1 AND id = $2`, tenantID, id)
}

func (r approvalRequestRepo) UpdateStatus(ctx context.Context, tenantID string, id string, from contractsv1.ApprovalStatus, to contractsv1.ApprovalStatus, actorID string, decidedAt time.Time) error {
	if err := domain.ValidateApprovalTransition(from, to); err != nil {
		return err
	}
	result, err := r.q.ExecContext(ctx, `
UPDATE approval_requests
SET status = $4, decided_by = $5, decided_at = $6, updated_at = now(), lock_version = lock_version + 1
WHERE tenant_id = $1 AND id = $2 AND status = $3`,
		tenantID, id, string(from), string(to), actorID, decidedAt)
	if err != nil {
		return fmt.Errorf("update approval_requests status: %w", err)
	}
	return requireRowsAffected(result)
}

type remediationAttemptRepo struct {
	q queryer
}

func (r remediationAttemptRepo) Create(ctx context.Context, record store.ContractRecord[contractsv1.RemediationAttempt]) error {
	payloadJSON, err := payloadJSON(record)
	if err != nil {
		return err
	}
	id := firstNonEmpty(record.ID, record.Payload.ID)
	remediationPlanID := firstNonEmpty(record.Relations.RemediationPlanID, record.Payload.RemediationPlanID)
	approvalRequestID := firstNonEmpty(record.Relations.ApprovalRequestID, record.Payload.ApprovalRequestID)
	status := firstNonEmpty(record.Status, string(record.Payload.Status))
	executorType := firstNonEmpty(record.ExecutorType, "unknown")
	idempotencyKey := firstNonEmpty(record.IdempotencyKey, id)
	createdAt := firstTime(record.CreatedAt, time.Now())
	if err := require("remediation attempt id", id); err != nil {
		return err
	}
	if err := require("remediation attempt remediation plan id", remediationPlanID); err != nil {
		return err
	}
	if err := require("remediation attempt status", status); err != nil {
		return err
	}

	_, err = r.q.ExecContext(ctx, `
INSERT INTO remediation_attempts (
    id, tenant_id, environment_id, service_id, remediation_plan_id, approval_request_id,
    contract_version, status, executor_type, idempotency_key, started_at, finished_at,
    rollback_attempt_id, created_at, updated_at, payload_json
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $14, $15)`,
		id,
		record.TenantID,
		record.EnvironmentID,
		record.ServiceID,
		remediationPlanID,
		nullString(approvalRequestID),
		contractVersion(record.ContractVersion, record.Payload.ContractVersion),
		status,
		executorType,
		idempotencyKey,
		record.Payload.ExecutionStartedAt,
		record.Payload.ExecutionFinishedAt,
		nullString(record.Payload.RollbackAttemptID),
		createdAt,
		payloadJSON,
	)
	return wrapExec("create remediation attempt", err)
}

func (r remediationAttemptRepo) Get(ctx context.Context, tenantID string, id string) (store.ContractRecord[contractsv1.RemediationAttempt], error) {
	return getContractRecord[contractsv1.RemediationAttempt](ctx, r.q, `
SELECT id, tenant_id::text, environment_id::text, service_id::text, contract_version, status,
       payload_json, lock_version, created_at, updated_at
FROM remediation_attempts
WHERE tenant_id = $1 AND id = $2`, tenantID, id)
}

func (r remediationAttemptRepo) UpdateStatus(ctx context.Context, tenantID string, id string, from contractsv1.RemediationStatus, to contractsv1.RemediationStatus) error {
	if err := domain.ValidateRemediationTransition(from, to); err != nil {
		return err
	}
	return updateStatus(ctx, r.q, "remediation_attempts", tenantID, id, string(from), string(to))
}

type receiptRepo struct {
	q queryer
}

func (r receiptRepo) Create(ctx context.Context, record store.ContractRecord[contractsv1.Receipt]) error {
	payloadJSON, err := payloadJSON(record)
	if err != nil {
		return err
	}
	id := firstNonEmpty(record.ID, record.Payload.ID)
	attemptID := firstNonEmpty(record.Relations.RemediationAttemptID, record.Payload.RemediationAttemptID)
	issuedAt := firstTime(record.CreatedAt, record.Payload.Timestamp, time.Now())
	if err := require("receipt id", id); err != nil {
		return err
	}
	if err := require("receipt remediation attempt id", attemptID); err != nil {
		return err
	}

	_, err = r.q.ExecContext(ctx, `
INSERT INTO receipts (
    id, tenant_id, environment_id, service_id, remediation_attempt_id, contract_version,
    outcome, issued_at, created_at, payload_json
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8, $9)`,
		id,
		record.TenantID,
		record.EnvironmentID,
		record.ServiceID,
		attemptID,
		contractVersion(record.ContractVersion, ""),
		record.Payload.Outcome,
		issuedAt,
		payloadJSON,
	)
	return wrapExec("create receipt", err)
}

func (r receiptRepo) Get(ctx context.Context, tenantID string, id string) (store.ContractRecord[contractsv1.Receipt], error) {
	return getContractRecord[contractsv1.Receipt](ctx, r.q, `
SELECT id, tenant_id::text, environment_id::text, service_id::text, contract_version, outcome AS status,
       payload_json, 0::bigint AS lock_version, created_at, created_at AS updated_at
FROM receipts
WHERE tenant_id = $1 AND id = $2`, tenantID, id)
}

type auditEventRepo struct {
	q queryer
}

func (r auditEventRepo) Append(ctx context.Context, event store.AuditEvent) error {
	createdAt := firstTime(event.CreatedAt, time.Now())
	_, err := r.q.ExecContext(ctx, `
INSERT INTO audit_events (
    id, tenant_id, actor_id, resource_type, resource_id, event_type, message,
    before_state, after_state, correlation_id, metadata_json, created_at
) VALUES (COALESCE(NULLIF($1, '')::uuid, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8, $9, $10, COALESCE($11::jsonb, '{}'::jsonb), $12)`,
		event.ID,
		event.TenantID,
		nullString(event.ActorID),
		event.ResourceType,
		event.ResourceID,
		event.EventType,
		event.Message,
		nullString(event.BeforeState),
		nullString(event.AfterState),
		nullString(event.CorrelationID),
		nullableJSON(event.MetadataJSON),
		createdAt,
	)
	return wrapExec("append audit event", err)
}

type workflowLeaseRepo struct {
	q queryer
}

func (r workflowLeaseRepo) Acquire(ctx context.Context, lease store.WorkflowLease) (store.WorkflowLease, error) {
	row := r.q.QueryRowContext(ctx, `
INSERT INTO workflow_leases (
    resource_type, resource_id, owner_id, fencing_token, expires_at, acquired_at, renewed_at
) VALUES ($1, $2, $3, 1, $4, now(), now())
ON CONFLICT (resource_type, resource_id)
DO UPDATE SET
    owner_id = EXCLUDED.owner_id,
    fencing_token = workflow_leases.fencing_token + 1,
    expires_at = EXCLUDED.expires_at,
    acquired_at = now(),
    renewed_at = now()
WHERE workflow_leases.expires_at <= now() OR workflow_leases.owner_id = EXCLUDED.owner_id
RETURNING resource_type, resource_id, owner_id, fencing_token, expires_at`,
		lease.ResourceType,
		lease.ResourceID,
		lease.OwnerID,
		lease.ExpiresAt,
	)
	return scanLease(row, store.ErrLeaseHeld)
}

func (r workflowLeaseRepo) Renew(ctx context.Context, lease store.WorkflowLease, now time.Time, ttl time.Duration) (store.WorkflowLease, error) {
	row := r.q.QueryRowContext(ctx, `
UPDATE workflow_leases
SET expires_at = $6, renewed_at = $5
WHERE resource_type = $1 AND resource_id = $2 AND owner_id = $3 AND fencing_token = $4 AND expires_at > $5
RETURNING resource_type, resource_id, owner_id, fencing_token, expires_at`,
		lease.ResourceType,
		lease.ResourceID,
		lease.OwnerID,
		lease.FencingToken,
		now,
		now.Add(ttl),
	)
	return scanLease(row, store.ErrLeaseHeld)
}

func (r workflowLeaseRepo) Release(ctx context.Context, lease store.WorkflowLease) error {
	result, err := r.q.ExecContext(ctx, `
DELETE FROM workflow_leases
WHERE resource_type = $1 AND resource_id = $2 AND owner_id = $3 AND fencing_token = $4`,
		lease.ResourceType,
		lease.ResourceID,
		lease.OwnerID,
		lease.FencingToken,
	)
	if err != nil {
		return fmt.Errorf("release workflow lease: %w", err)
	}
	return requireRowsAffected(result)
}

type outboxEventRepo struct {
	q queryer
}

func (r outboxEventRepo) Append(ctx context.Context, event store.OutboxEvent) error {
	id := event.ID
	if err := require("outbox event id", id); err != nil {
		return err
	}
	createdAt := firstTime(event.CreatedAt, time.Now())
	nextAttemptAt := firstTime(event.NextAttemptAt, createdAt)
	status := firstNonEmpty(event.Status, "pending")
	_, err := r.q.ExecContext(ctx, `
INSERT INTO outbox_events (
    id, tenant_id, event_type, resource_type, resource_id, status, attempt_count,
    next_attempt_at, payload_json, idempotency_key, created_at, published_at,
    last_error_message
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		id,
		event.TenantID,
		event.EventType,
		event.ResourceType,
		event.ResourceID,
		status,
		event.AttemptCount,
		nextAttemptAt,
		jsonString(event.PayloadJSON),
		event.IdempotencyKey,
		createdAt,
		event.PublishedAt,
		nullString(event.LastErrorMessage),
	)
	return wrapExec("append outbox event", err)
}

func (r outboxEventRepo) ClaimDue(ctx context.Context, tenantID string, ownerID string, limit int, now time.Time) ([]store.OutboxEvent, error) {
	rows, err := r.q.QueryContext(ctx, `
UPDATE outbox_events
SET status = 'claimed',
    attempt_count = attempt_count + 1,
    claimed_by = $2,
    claimed_at = $4
WHERE id IN (
    SELECT id
    FROM outbox_events
    WHERE tenant_id = $1 AND status IN ('pending', 'failed') AND next_attempt_at <= $4
    ORDER BY next_attempt_at ASC, created_at ASC
    LIMIT $3
    FOR UPDATE SKIP LOCKED
)
RETURNING id::text, tenant_id::text, event_type, resource_type, resource_id, status,
          attempt_count, next_attempt_at, payload_json, idempotency_key, created_at,
          published_at, last_error_message`,
		tenantID,
		ownerID,
		limit,
		now,
	)
	if err != nil {
		return nil, fmt.Errorf("claim due outbox events: %w", err)
	}
	defer rows.Close()

	var events []store.OutboxEvent
	for rows.Next() {
		event, err := scanOutboxEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan due outbox events: %w", err)
	}
	return events, nil
}

func (r outboxEventRepo) MarkPublished(ctx context.Context, tenantID string, id string, publishedAt time.Time) error {
	result, err := r.q.ExecContext(ctx, `
UPDATE outbox_events
SET status = 'published', published_at = $3, last_error_message = NULL
WHERE tenant_id = $1 AND id = $2`,
		tenantID, id, publishedAt)
	if err != nil {
		return fmt.Errorf("mark outbox event published: %w", err)
	}
	return requireRowsAffected(result)
}

func (r outboxEventRepo) MarkFailed(ctx context.Context, tenantID string, id string, nextAttemptAt time.Time, message string) error {
	result, err := r.q.ExecContext(ctx, `
UPDATE outbox_events
SET status = 'failed', next_attempt_at = $3, last_error_message = $4
WHERE tenant_id = $1 AND id = $2`,
		tenantID, id, nextAttemptAt, message)
	if err != nil {
		return fmt.Errorf("mark outbox event failed: %w", err)
	}
	return requireRowsAffected(result)
}

func getContractRecord[T any](ctx context.Context, q queryer, query string, args ...any) (store.ContractRecord[T], error) {
	var record store.ContractRecord[T]
	var raw []byte
	err := q.QueryRowContext(ctx, query, args...).Scan(
		&record.ID,
		&record.TenantID,
		&record.EnvironmentID,
		&record.ServiceID,
		&record.ContractVersion,
		&record.Status,
		&raw,
		&record.LockVersion,
		&record.CreatedAt,
		&record.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return record, store.ErrNotFound
	}
	if err != nil {
		return record, fmt.Errorf("get contract record: %w", err)
	}
	if err := json.Unmarshal(raw, &record.Payload); err != nil {
		return record, fmt.Errorf("unmarshal contract payload: %w", err)
	}
	record.PayloadJSON = append(record.PayloadJSON[:0], raw...)
	return record, nil
}

func payloadJSON[T any](record store.ContractRecord[T]) (string, error) {
	if len(record.PayloadJSON) > 0 {
		return string(record.PayloadJSON), nil
	}
	raw, err := json.Marshal(record.Payload)
	if err != nil {
		return "", fmt.Errorf("marshal contract payload: %w", err)
	}
	return string(raw), nil
}

func updateStatus(ctx context.Context, q queryer, table string, tenantID string, id string, from string, to string) error {
	query := fmt.Sprintf(`
UPDATE %s
SET status = $4, updated_at = now(), lock_version = lock_version + 1
WHERE tenant_id = $1 AND id = $2 AND status = $3`, table)
	result, err := q.ExecContext(ctx, query, tenantID, id, from, to)
	if err != nil {
		return fmt.Errorf("update %s status: %w", table, err)
	}
	return requireRowsAffected(result)
}

func requireRowsAffected(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read rows affected: %w", err)
	}
	if rows == 0 {
		return store.ErrConflict
	}
	return nil
}

func scanSignalEvent(row rowScanner) (store.SignalEvent, error) {
	var event store.SignalEvent
	var raw []byte
	err := row.Scan(
		&event.ID,
		&event.TenantID,
		&event.EnvironmentID,
		&event.ServiceID,
		&event.Source,
		&event.Severity,
		&event.Route,
		&event.Method,
		&event.StatusCode,
		&event.ErrorClass,
		&event.FingerprintHash,
		&event.IdempotencyKey,
		&event.ObservedAt,
		&event.ReceivedAt,
		&raw,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return event, store.ErrNotFound
	}
	if err != nil {
		return event, fmt.Errorf("scan signal event: %w", err)
	}
	event.PayloadJSON = append(event.PayloadJSON[:0], raw...)
	return event, nil
}

func scanSignalFingerprint(row rowScanner) (store.SignalFingerprint, error) {
	var fingerprint store.SignalFingerprint
	var raw []byte
	err := row.Scan(
		&fingerprint.ID,
		&fingerprint.TenantID,
		&fingerprint.EnvironmentID,
		&fingerprint.ServiceID,
		&fingerprint.FingerprintHash,
		&fingerprint.Status,
		&fingerprint.FirstSeenAt,
		&fingerprint.LastSeenAt,
		&fingerprint.OccurrenceCount,
		&fingerprint.SampleEventID,
		&raw,
		&fingerprint.CreatedAt,
		&fingerprint.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return fingerprint, store.ErrNotFound
	}
	if err != nil {
		return fingerprint, fmt.Errorf("scan signal fingerprint: %w", err)
	}
	fingerprint.MetadataJSON = append(fingerprint.MetadataJSON[:0], raw...)
	return fingerprint, nil
}

func scanLease(row *sql.Row, emptyErr error) (store.WorkflowLease, error) {
	var lease store.WorkflowLease
	err := row.Scan(&lease.ResourceType, &lease.ResourceID, &lease.OwnerID, &lease.FencingToken, &lease.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return lease, emptyErr
	}
	if err != nil {
		return lease, fmt.Errorf("scan workflow lease: %w", err)
	}
	return lease, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanOutboxEvent(row rowScanner) (store.OutboxEvent, error) {
	var event store.OutboxEvent
	var raw []byte
	var publishedAt sql.NullTime
	var lastError sql.NullString
	err := row.Scan(
		&event.ID,
		&event.TenantID,
		&event.EventType,
		&event.ResourceType,
		&event.ResourceID,
		&event.Status,
		&event.AttemptCount,
		&event.NextAttemptAt,
		&raw,
		&event.IdempotencyKey,
		&event.CreatedAt,
		&publishedAt,
		&lastError,
	)
	if err != nil {
		return event, fmt.Errorf("scan outbox event: %w", err)
	}
	event.PayloadJSON = append(event.PayloadJSON[:0], raw...)
	if publishedAt.Valid {
		event.PublishedAt = &publishedAt.Time
	}
	if lastError.Valid {
		event.LastErrorMessage = lastError.String
	}
	return event, nil
}

func contractVersion(values ...string) string {
	return firstNonEmpty(append(values, contractsv1.ContractVersion)...)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}

func jsonString(value json.RawMessage) string {
	if len(value) == 0 {
		return "{}"
	}
	return string(value)
}

func require(name string, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}

func wrapExec(operation string, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}
