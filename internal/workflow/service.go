package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/domain"
	"github.com/CloudSpaceLab/ai-logfixer/internal/store"
)

const (
	ResourceInvestigationCluster = "investigation_cluster"
	ResourceInvestigationBranch  = "investigation_branch"
	ResourceRemediationPlan      = "remediation_plan"
	ResourceRemediationAttempt   = "remediation_attempt"
	ResourceApprovalRequest      = "approval_request"

	EventWorkflowTransitioned = "workflow.transitioned"
)

type IDGenerator func() (string, error)

type Service struct {
	store store.WorkflowStore
	now   func() time.Time
	newID IDGenerator
}

func NewService(workflowStore store.WorkflowStore) *Service {
	return &Service{
		store: workflowStore,
		now:   time.Now,
		newID: newUUID,
	}
}

func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

func (s *Service) SetIDGenerator(newID IDGenerator) {
	if newID != nil {
		s.newID = newID
	}
}

type TransitionMetadata struct {
	ActorID        string
	CorrelationID  string
	Message        string
	SuppressOutbox bool
}

type InvestigationTransition struct {
	TenantID     string
	ResourceType string
	ResourceID   string
	From         contractsv1.InvestigationStatus
	To           contractsv1.InvestigationStatus
	Metadata     TransitionMetadata
}

type RemediationTransition struct {
	TenantID     string
	ResourceType string
	ResourceID   string
	From         contractsv1.RemediationStatus
	To           contractsv1.RemediationStatus
	Metadata     TransitionMetadata
}

type ApprovalDecision struct {
	TenantID          string
	ApprovalRequestID string
	From              contractsv1.ApprovalStatus
	To                contractsv1.ApprovalStatus
	ActorID           string
	DecidedAt         time.Time
	Metadata          TransitionMetadata
}

func (s *Service) MoveInvestigation(ctx context.Context, transition InvestigationTransition) error {
	if err := requireTransition(transition.TenantID, transition.ResourceType, transition.ResourceID); err != nil {
		return err
	}
	if err := domain.ValidateInvestigationTransition(transition.From, transition.To); err != nil {
		return err
	}
	return s.store.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		switch transition.ResourceType {
		case ResourceInvestigationCluster:
			if err := tx.InvestigationClusters().UpdateStatus(ctx, transition.TenantID, transition.ResourceID, transition.From, transition.To); err != nil {
				return err
			}
		case ResourceInvestigationBranch:
			if err := tx.InvestigationBranches().UpdateStatus(ctx, transition.TenantID, transition.ResourceID, transition.From, transition.To); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported investigation resource type %q", transition.ResourceType)
		}
		return s.appendTransitionEvents(ctx, tx, transitionEvent{
			tenantID:     transition.TenantID,
			resourceType: transition.ResourceType,
			resourceID:   transition.ResourceID,
			from:         string(transition.From),
			to:           string(transition.To),
			metadata:     transition.Metadata,
		})
	})
}

func (s *Service) MoveRemediation(ctx context.Context, transition RemediationTransition) error {
	if err := requireTransition(transition.TenantID, transition.ResourceType, transition.ResourceID); err != nil {
		return err
	}
	if err := domain.ValidateRemediationTransition(transition.From, transition.To); err != nil {
		return err
	}
	return s.store.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		switch transition.ResourceType {
		case ResourceRemediationPlan:
			if err := tx.RemediationPlans().UpdateStatus(ctx, transition.TenantID, transition.ResourceID, transition.From, transition.To); err != nil {
				return err
			}
		case ResourceRemediationAttempt:
			if err := tx.RemediationAttempts().UpdateStatus(ctx, transition.TenantID, transition.ResourceID, transition.From, transition.To); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported remediation resource type %q", transition.ResourceType)
		}
		return s.appendTransitionEvents(ctx, tx, transitionEvent{
			tenantID:     transition.TenantID,
			resourceType: transition.ResourceType,
			resourceID:   transition.ResourceID,
			from:         string(transition.From),
			to:           string(transition.To),
			metadata:     transition.Metadata,
		})
	})
}

func (s *Service) DecideApproval(ctx context.Context, decision ApprovalDecision) error {
	if err := requireTransition(decision.TenantID, ResourceApprovalRequest, decision.ApprovalRequestID); err != nil {
		return err
	}
	if decision.ActorID == "" {
		return fmt.Errorf("approval actor id is required")
	}
	decidedAt := decision.DecidedAt
	if decidedAt.IsZero() {
		decidedAt = s.now()
	}
	if err := domain.ValidateApprovalTransition(decision.From, decision.To); err != nil {
		return err
	}
	return s.store.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.ApprovalRequests().UpdateStatus(ctx, decision.TenantID, decision.ApprovalRequestID, decision.From, decision.To, decision.ActorID, decidedAt); err != nil {
			return err
		}
		metadata := decision.Metadata
		if metadata.ActorID == "" {
			metadata.ActorID = decision.ActorID
		}
		return s.appendTransitionEvents(ctx, tx, transitionEvent{
			tenantID:     decision.TenantID,
			resourceType: ResourceApprovalRequest,
			resourceID:   decision.ApprovalRequestID,
			from:         string(decision.From),
			to:           string(decision.To),
			metadata:     metadata,
		})
	})
}

type transitionEvent struct {
	tenantID     string
	resourceType string
	resourceID   string
	from         string
	to           string
	metadata     TransitionMetadata
}

func (s *Service) appendTransitionEvents(ctx context.Context, tx store.Tx, event transitionEvent) error {
	now := s.now()
	message := event.metadata.Message
	if message == "" {
		message = fmt.Sprintf("%s moved from %s to %s", event.resourceType, event.from, event.to)
	}

	if err := tx.AuditEvents().Append(ctx, store.AuditEvent{
		TenantID:      event.tenantID,
		ActorID:       event.metadata.ActorID,
		ResourceType:  event.resourceType,
		ResourceID:    event.resourceID,
		EventType:     EventWorkflowTransitioned,
		Message:       message,
		BeforeState:   event.from,
		AfterState:    event.to,
		CorrelationID: event.metadata.CorrelationID,
		CreatedAt:     now,
	}); err != nil {
		return err
	}

	if event.metadata.SuppressOutbox {
		return nil
	}

	outboxID, err := s.newID()
	if err != nil {
		return fmt.Errorf("create outbox id: %w", err)
	}
	payload, err := json.Marshal(map[string]string{
		"tenant_id":      event.tenantID,
		"resource_type":  event.resourceType,
		"resource_id":    event.resourceID,
		"before_state":   event.from,
		"after_state":    event.to,
		"correlation_id": event.metadata.CorrelationID,
	})
	if err != nil {
		return fmt.Errorf("marshal transition outbox payload: %w", err)
	}
	return tx.OutboxEvents().Append(ctx, store.OutboxEvent{
		ID:             outboxID,
		TenantID:       event.tenantID,
		EventType:      EventWorkflowTransitioned,
		ResourceType:   event.resourceType,
		ResourceID:     event.resourceID,
		Status:         "pending",
		NextAttemptAt:  now,
		PayloadJSON:    payload,
		IdempotencyKey: transitionIdempotencyKey(event),
		CreatedAt:      now,
	})
}

func transitionIdempotencyKey(event transitionEvent) string {
	return fmt.Sprintf("%s:%s:%s:%s:%s", event.tenantID, event.resourceType, event.resourceID, event.from, event.to)
}

func requireTransition(tenantID string, resourceType string, resourceID string) error {
	if tenantID == "" {
		return fmt.Errorf("tenant id is required")
	}
	if resourceType == "" {
		return fmt.Errorf("resource type is required")
	}
	if resourceID == "" {
		return fmt.Errorf("resource id is required")
	}
	return nil
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	out := make([]byte, 36)
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out), nil
}
