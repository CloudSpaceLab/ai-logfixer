package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
)

var (
	ErrConflict  = errors.New("store: optimistic update conflict")
	ErrLeaseHeld = errors.New("store: workflow lease is held")
	ErrNotFound  = errors.New("store: record not found")
)

type WorkflowStore interface {
	WithinTx(ctx context.Context, fn func(context.Context, Tx) error) error
}

type Tx interface {
	InvestigationRequests() InvestigationRequestRepository
	InvestigationClusters() InvestigationClusterRepository
	InvestigationBranches() InvestigationBranchRepository
	InvestigationDecisions() InvestigationDecisionRepository
	DiagnosisResults() DiagnosisResultRepository
	RemediationPlans() RemediationPlanRepository
	ApprovalRequests() ApprovalRequestRepository
	RemediationAttempts() RemediationAttemptRepository
	Receipts() ReceiptRepository
	AuditEvents() AuditEventRepository
	WorkflowLeases() WorkflowLeaseRepository
	OutboxEvents() OutboxEventRepository
}

type ContractRecord[T any] struct {
	ID              string
	TenantID        string
	EnvironmentID   string
	ServiceID       string
	ContractVersion string
	Status          string
	Relations       ContractRelations
	IdempotencyKey  string
	ExecutorType    string
	Payload         T
	PayloadJSON     json.RawMessage
	LockVersion     int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type ContractRelations struct {
	RequestID            string
	ClusterID            string
	BranchID             string
	DiagnosisResultID    string
	RollbackPlanID       string
	RemediationPlanID    string
	ApprovalRequestID    string
	RemediationAttemptID string
}

type InvestigationRequestRepository interface {
	Create(ctx context.Context, record ContractRecord[contractsv1.InvestigationRequest]) error
	Get(ctx context.Context, tenantID string, id string) (ContractRecord[contractsv1.InvestigationRequest], error)
}

type InvestigationClusterRepository interface {
	Create(ctx context.Context, record ContractRecord[contractsv1.InvestigationCluster]) error
	Get(ctx context.Context, tenantID string, id string) (ContractRecord[contractsv1.InvestigationCluster], error)
	UpdateStatus(ctx context.Context, tenantID string, id string, from contractsv1.InvestigationStatus, to contractsv1.InvestigationStatus) error
}

type InvestigationBranchRepository interface {
	Create(ctx context.Context, record ContractRecord[contractsv1.InvestigationBranch]) error
	Get(ctx context.Context, tenantID string, id string) (ContractRecord[contractsv1.InvestigationBranch], error)
	UpdateStatus(ctx context.Context, tenantID string, id string, from contractsv1.InvestigationStatus, to contractsv1.InvestigationStatus) error
}

type InvestigationDecisionRepository interface {
	Create(ctx context.Context, record ContractRecord[contractsv1.InvestigationDecision]) error
	Get(ctx context.Context, tenantID string, id string) (ContractRecord[contractsv1.InvestigationDecision], error)
}

type DiagnosisResultRepository interface {
	Create(ctx context.Context, record ContractRecord[contractsv1.DiagnosisResult]) error
	Get(ctx context.Context, tenantID string, id string) (ContractRecord[contractsv1.DiagnosisResult], error)
}

type RemediationPlanRepository interface {
	Create(ctx context.Context, record ContractRecord[contractsv1.RemediationPlan]) error
	Get(ctx context.Context, tenantID string, id string) (ContractRecord[contractsv1.RemediationPlan], error)
	UpdateStatus(ctx context.Context, tenantID string, id string, from contractsv1.RemediationStatus, to contractsv1.RemediationStatus) error
}

type ApprovalRequestRepository interface {
	Create(ctx context.Context, record ContractRecord[contractsv1.ApprovalRequest]) error
	Get(ctx context.Context, tenantID string, id string) (ContractRecord[contractsv1.ApprovalRequest], error)
	UpdateStatus(ctx context.Context, tenantID string, id string, from contractsv1.ApprovalStatus, to contractsv1.ApprovalStatus, actorID string, decidedAt time.Time) error
}

type RemediationAttemptRepository interface {
	Create(ctx context.Context, record ContractRecord[contractsv1.RemediationAttempt]) error
	Get(ctx context.Context, tenantID string, id string) (ContractRecord[contractsv1.RemediationAttempt], error)
	UpdateStatus(ctx context.Context, tenantID string, id string, from contractsv1.RemediationStatus, to contractsv1.RemediationStatus) error
}

type ReceiptRepository interface {
	Create(ctx context.Context, record ContractRecord[contractsv1.Receipt]) error
	Get(ctx context.Context, tenantID string, id string) (ContractRecord[contractsv1.Receipt], error)
}

type AuditEvent struct {
	ID            string
	TenantID      string
	ActorID       string
	ResourceType  string
	ResourceID    string
	EventType     string
	Message       string
	BeforeState   string
	AfterState    string
	CorrelationID string
	CreatedAt     time.Time
	MetadataJSON  json.RawMessage
}

type AuditEventRepository interface {
	Append(ctx context.Context, event AuditEvent) error
}

type WorkflowLease struct {
	ResourceType string
	ResourceID   string
	OwnerID      string
	FencingToken int64
	ExpiresAt    time.Time
}

type WorkflowLeaseRepository interface {
	Acquire(ctx context.Context, lease WorkflowLease) (WorkflowLease, error)
	Renew(ctx context.Context, lease WorkflowLease, now time.Time, ttl time.Duration) (WorkflowLease, error)
	Release(ctx context.Context, lease WorkflowLease) error
}

type OutboxEvent struct {
	ID               string
	TenantID         string
	EventType        string
	ResourceType     string
	ResourceID       string
	Status           string
	AttemptCount     int
	NextAttemptAt    time.Time
	PayloadJSON      json.RawMessage
	IdempotencyKey   string
	CreatedAt        time.Time
	PublishedAt      *time.Time
	LastErrorMessage string
}

type OutboxEventRepository interface {
	Append(ctx context.Context, event OutboxEvent) error
	ClaimDue(ctx context.Context, tenantID string, ownerID string, limit int, now time.Time) ([]OutboxEvent, error)
	MarkPublished(ctx context.Context, tenantID string, id string, publishedAt time.Time) error
	MarkFailed(ctx context.Context, tenantID string, id string, nextAttemptAt time.Time, message string) error
}
