package intake

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	contractsv1 "github.com/CloudSpaceLab/ai-logfixer/internal/contracts/v1"
	"github.com/CloudSpaceLab/ai-logfixer/internal/engine"
	"github.com/CloudSpaceLab/ai-logfixer/internal/store"
)

const (
	ResourceSignalEvent           = "signal_event"
	ResourceSignalFingerprint     = "signal_fingerprint"
	ResourceInvestigationRequest  = "investigation_request"
	ResourceInvestigationCluster  = "investigation_cluster"
	ResourceInvestigationBranch   = "investigation_branch"
	ResourceInvestigationDecision = "investigation_decision"

	EventInvestigationStarted = "investigation.started"
)

type IDGenerator func() (string, error)

type Service struct {
	store store.WorkflowStore
	now   func() time.Time
	newID IDGenerator
	ids   engine.ContractIDFactory
}

func NewService(workflowStore store.WorkflowStore) *Service {
	return &Service{
		store: workflowStore,
		now:   time.Now,
		newID: newUUID,
		ids:   engine.NewContractIDFactory(),
	}
}

func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

func (s *Service) SetOutboxIDGenerator(newID IDGenerator) {
	if newID != nil {
		s.newID = newID
	}
}

type StartInput struct {
	TenantID       string
	EnvironmentID  string
	ServiceID      string
	ServiceName    string
	SourceName     string
	RequestedBy    string
	ActorID        string
	CorrelationID  string
	IdempotencyKey string
	SuppressOutbox bool
	Signal         engine.IncidentSignal
	ExternalRefs   []contractsv1.ExternalRef
	KnowledgeRefs  []contractsv1.KnowledgeRef
}

type StartResult struct {
	Signal               engine.IncidentSignal
	InvestigationRequest contractsv1.InvestigationRequest
	Cluster              contractsv1.InvestigationCluster
	Branch               contractsv1.InvestigationBranch
	Decision             contractsv1.InvestigationDecision
}

func (s *Service) StartNewInvestigation(ctx context.Context, input StartInput) (StartResult, error) {
	if s == nil || s.store == nil {
		return StartResult{}, errors.New("intake store is required")
	}
	result, now, input, err := s.plan(input)
	if err != nil {
		return StartResult{}, err
	}

	err = s.store.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		signalEvent, err := tx.SignalEvents().Create(ctx, buildSignalEvent(input, result, now))
		if err != nil {
			return fmt.Errorf("create signal event: %w", err)
		}
		if _, err := tx.SignalFingerprints().Upsert(ctx, buildSignalFingerprint(input, result, signalEvent, now)); err != nil {
			return fmt.Errorf("upsert signal fingerprint: %w", err)
		}
		if err := tx.InvestigationRequests().Create(ctx, store.ContractRecord[contractsv1.InvestigationRequest]{
			ID:              result.InvestigationRequest.ID,
			TenantID:        input.TenantID,
			EnvironmentID:   input.EnvironmentID,
			ServiceID:       input.ServiceID,
			ContractVersion: contractsv1.ContractVersion,
			Status:          string(contractsv1.InvestigationStatusRequested),
			IdempotencyKey:  firstNonEmpty(input.IdempotencyKey, result.InvestigationRequest.ID),
			Payload:         result.InvestigationRequest,
			CreatedAt:       now,
			UpdatedAt:       now,
		}); err != nil {
			return fmt.Errorf("create investigation request: %w", err)
		}
		if err := tx.InvestigationClusters().Create(ctx, store.ContractRecord[contractsv1.InvestigationCluster]{
			ID:              result.Cluster.ID,
			TenantID:        input.TenantID,
			EnvironmentID:   input.EnvironmentID,
			ServiceID:       input.ServiceID,
			ContractVersion: contractsv1.ContractVersion,
			Status:          string(result.Cluster.Status),
			Relations:       store.ContractRelations{RequestID: result.InvestigationRequest.ID},
			Payload:         result.Cluster,
			CreatedAt:       now,
			UpdatedAt:       now,
		}); err != nil {
			return fmt.Errorf("create investigation cluster: %w", err)
		}
		if err := tx.InvestigationBranches().Create(ctx, store.ContractRecord[contractsv1.InvestigationBranch]{
			ID:              result.Branch.ID,
			TenantID:        input.TenantID,
			EnvironmentID:   input.EnvironmentID,
			ServiceID:       input.ServiceID,
			ContractVersion: contractsv1.ContractVersion,
			Status:          string(result.Branch.Status),
			Relations: store.ContractRelations{
				RequestID: result.InvestigationRequest.ID,
				ClusterID: result.Cluster.ID,
			},
			Payload:   result.Branch,
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			return fmt.Errorf("create investigation branch: %w", err)
		}
		if err := tx.InvestigationDecisions().Create(ctx, store.ContractRecord[contractsv1.InvestigationDecision]{
			ID:              result.Decision.ID,
			TenantID:        input.TenantID,
			ContractVersion: contractsv1.ContractVersion,
			Status:          string(result.Decision.Decision),
			Relations: store.ContractRelations{
				RequestID: result.InvestigationRequest.ID,
				ClusterID: result.Cluster.ID,
				BranchID:  result.Branch.ID,
			},
			Payload:   result.Decision,
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			return fmt.Errorf("create investigation decision: %w", err)
		}
		if err := appendAudit(ctx, tx, input, result, now); err != nil {
			return err
		}
		if input.SuppressOutbox {
			return nil
		}
		outboxID, err := s.newID()
		if err != nil {
			return fmt.Errorf("create outbox id: %w", err)
		}
		return appendOutbox(ctx, tx, input, result, outboxID, now)
	})
	if err != nil {
		return StartResult{}, err
	}
	return result, nil
}

func (s *Service) PlanStartNewInvestigation(input StartInput) (StartResult, error) {
	result, _, _, err := s.plan(input)
	return result, err
}

func (s *Service) plan(input StartInput) (StartResult, time.Time, StartInput, error) {
	if s == nil {
		return StartResult{}, time.Time{}, StartInput{}, errors.New("intake service is required")
	}
	now := s.now().UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	input = normalizeInput(input, now)
	if err := validateInput(input); err != nil {
		return StartResult{}, time.Time{}, StartInput{}, err
	}

	result := buildStartResult(input, now, s.ids)
	if err := validateResult(result); err != nil {
		return StartResult{}, time.Time{}, StartInput{}, err
	}
	return result, now, input, nil
}

func normalizeInput(input StartInput, now time.Time) StartInput {
	if input.SourceName == "" {
		input.SourceName = "signal-detector"
	}
	if input.RequestedBy == "" {
		input.RequestedBy = "ai-logfixer"
	}
	if input.ActorID == "" {
		input.ActorID = input.RequestedBy
	}
	if input.ServiceName == "" {
		input.ServiceName = input.Signal.Service
	}
	if input.Signal.Service == "" {
		input.Signal.Service = input.ServiceName
	}
	if input.Signal.Source == "" {
		input.Signal.Source = input.SourceName
	}
	if input.Signal.Kind == "" {
		input.Signal.Kind = "signal"
	}
	if input.Signal.Count <= 0 {
		input.Signal.Count = 1
	}
	if input.Signal.StatusClass == 0 && input.Signal.StatusCode > 0 {
		input.Signal.StatusClass = (input.Signal.StatusCode / 100) * 100
	}
	if input.Signal.Start.IsZero() {
		input.Signal.Start = now.Add(-time.Nanosecond)
	}
	if input.Signal.End.IsZero() || !input.Signal.End.After(input.Signal.Start) {
		input.Signal.End = input.Signal.Start.Add(time.Nanosecond)
	}
	if len(input.Signal.Tags) == 0 {
		input.Signal.Tags = []string{input.Signal.Kind}
	}
	if input.ExternalRefs == nil {
		input.ExternalRefs = []contractsv1.ExternalRef{}
	}
	if input.KnowledgeRefs == nil {
		input.KnowledgeRefs = []contractsv1.KnowledgeRef{}
	}
	return input
}

func validateInput(input StartInput) error {
	var errs []error
	require(&errs, input.TenantID != "", "tenant id is required")
	require(&errs, input.EnvironmentID != "", "environment id is required")
	require(&errs, input.ServiceID != "", "service id is required")
	require(&errs, input.ServiceName != "", "service name is required")
	require(&errs, input.Signal.Kind != "", "signal kind is required")
	require(&errs, input.Signal.Source != "", "signal source is required")
	require(&errs, input.Signal.End.After(input.Signal.Start), "signal end must be after signal start")
	return errors.Join(errs...)
}

func buildStartResult(input StartInput, now time.Time, factory engine.ContractIDFactory) StartResult {
	signal := input.Signal
	parts := stableParts(input)
	requestID := factory.ID("inv_req_signal", parts...)
	clusterID := factory.ID("inv_cluster_signal", parts...)
	branchID := factory.ID("inv_branch_signal", parts...)
	decisionID := factory.ID("inv_decision_signal", parts...)
	symptom := signalSymptom(signal)
	fingerprintSymptom := signal.Kind
	if fingerprintSymptom == "http_failure" && signal.StatusClass > 0 {
		fingerprintSymptom = "http_failure_" + strconv.Itoa(signal.StatusClass)
	}

	request := contractsv1.InvestigationRequest{
		ID:              requestID,
		ContractVersion: contractsv1.ContractVersion,
		SchemaURL:       contractsv1.InvestigationRequestSchemaURL,
		SourceType:      contractsv1.SourceTypeAutomatic,
		SourceName:      input.SourceName,
		RequestedBy:     input.RequestedBy,
		Service:         input.ServiceName,
		Symptom:         symptom,
		ErrorCode:       signal.ErrorCode(),
		TimeWindow:      contractsv1.TimeWindow{Start: signal.Start, End: signal.End},
		SignalFingerprint: contractsv1.SignalFingerprint{
			Service:       input.ServiceName,
			Symptom:       fingerprintSymptom,
			ErrorCode:     signal.ErrorCode(),
			Source:        signal.Source,
			DeployVersion: deployVersion(signal.Tags),
			Tags:          signal.Tags,
		},
		DisplayStatus: "Investigation started automatically",
		UserMessage:   fmt.Sprintf("I detected %s and started an investigation.", strings.ToLower(symptom)),
		ExternalRefs:  input.ExternalRefs,
		KnowledgeRefs: input.KnowledgeRefs,
		CreatedAt:     now,
	}

	branch := contractsv1.InvestigationBranch{
		ID:                branchID,
		ClusterID:         clusterID,
		BranchType:        branchType(signal),
		Symptom:           symptom,
		Status:            contractsv1.InvestigationStatusRunning,
		SourceRequestIDs:  []string{requestID},
		DiagnosisResultID: "",
		RemediationPlanID: "",
		DisplayStatus:     "Investigation running",
		UserMessage:       fmt.Sprintf("I opened a focused branch for %s.", strings.ToLower(symptom)),
		TimelineEvents: []contractsv1.TimelineEvent{
			{
				ID:        factory.ID("tl_signal_branch_started", parts...),
				Type:      EventInvestigationStarted,
				Message:   "Investigation branch started from an automatic signal.",
				Severity:  "info",
				Timestamp: now,
			},
		},
		KnowledgeRefs: input.KnowledgeRefs,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	cluster := contractsv1.InvestigationCluster{
		ID:             clusterID,
		Status:         contractsv1.InvestigationStatusRunning,
		PrimaryService: input.ServiceName,
		Summary:        fmt.Sprintf("%s is under investigation after %d related signal(s).", input.ServiceName, signal.Count),
		ActiveBranches: []contractsv1.InvestigationBranch{branch},
		QueuedBranches: []contractsv1.InvestigationBranch{},
		TimelineEvents: []contractsv1.TimelineEvent{
			{
				ID:        factory.ID("tl_signal_cluster_started", parts...),
				Type:      EventInvestigationStarted,
				Message:   "Investigation cluster created from an automatic signal.",
				Severity:  "info",
				Timestamp: now,
			},
		},
		NextActions: []contractsv1.NextAction{
			{
				ID:          "next_open_investigation",
				Label:       "Open investigation",
				ActionType:  "open_investigation_cluster",
				Description: "Review the automatically started investigation.",
				Enabled:     true,
			},
		},
		ExternalRefs:  input.ExternalRefs,
		KnowledgeRefs: input.KnowledgeRefs,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	decision := contractsv1.InvestigationDecision{
		ID:              decisionID,
		ContractVersion: contractsv1.ContractVersion,
		SchemaURL:       contractsv1.InvestigationDecisionSchemaURL,
		RequestID:       requestID,
		Decision:        contractsv1.InvestigationDecisionStartNew,
		Explanation:     "No active correlation check is available in this intake slice, so the detector started a new investigation cluster for the signal.",
		UserMessage:     "I started a new investigation for the detected signal.",
		ClusterID:       clusterID,
		BranchID:        branchID,
		NextActions: []contractsv1.NextAction{
			{
				ID:          "next_open_branch",
				Label:       "Open branch",
				ActionType:  "open_investigation_branch",
				Description: "Open the focused branch created for this signal.",
				Enabled:     true,
			},
		},
		CreatedAt: now,
	}

	return StartResult{
		Signal:               signal,
		InvestigationRequest: request,
		Cluster:              cluster,
		Branch:               branch,
		Decision:             decision,
	}
}

func validateResult(result StartResult) error {
	if err := result.InvestigationRequest.Validate(); err != nil {
		return fmt.Errorf("validate investigation request: %w", err)
	}
	if err := validateCluster(result.Cluster); err != nil {
		return fmt.Errorf("validate investigation cluster: %w", err)
	}
	if err := validateBranch(result.Branch); err != nil {
		return fmt.Errorf("validate investigation branch: %w", err)
	}
	if err := result.Decision.Validate(); err != nil {
		return fmt.Errorf("validate investigation decision: %w", err)
	}
	return nil
}

func validateCluster(cluster contractsv1.InvestigationCluster) error {
	var errs []error
	require(&errs, cluster.ID != "", "id is required")
	require(&errs, cluster.Status != "", "status is required")
	require(&errs, cluster.PrimaryService != "", "primary_service is required")
	require(&errs, cluster.Summary != "", "summary is required")
	require(&errs, !cluster.CreatedAt.IsZero(), "created_at is required")
	require(&errs, !cluster.UpdatedAt.IsZero(), "updated_at is required")
	return errors.Join(errs...)
}

func validateBranch(branch contractsv1.InvestigationBranch) error {
	var errs []error
	require(&errs, branch.ID != "", "id is required")
	require(&errs, branch.ClusterID != "", "cluster_id is required")
	require(&errs, branch.BranchType != "", "branch_type is required")
	require(&errs, branch.Symptom != "", "symptom is required")
	require(&errs, branch.Status != "", "status is required")
	require(&errs, branch.DisplayStatus != "", "display_status is required")
	require(&errs, branch.UserMessage != "", "user_message is required")
	require(&errs, !branch.CreatedAt.IsZero(), "created_at is required")
	require(&errs, !branch.UpdatedAt.IsZero(), "updated_at is required")
	return errors.Join(errs...)
}

func buildSignalEvent(input StartInput, result StartResult, now time.Time) store.SignalEvent {
	payload, _ := json.Marshal(result.Signal)
	return store.SignalEvent{
		TenantID:        input.TenantID,
		EnvironmentID:   input.EnvironmentID,
		ServiceID:       input.ServiceID,
		Source:          result.Signal.Source,
		Severity:        signalSeverity(result.Signal),
		Route:           result.Signal.Route,
		Method:          result.Signal.Method,
		StatusCode:      result.Signal.StatusCode,
		ErrorClass:      result.Signal.ErrorCode(),
		FingerprintHash: fingerprintHash(input),
		IdempotencyKey:  "signal-event:" + result.InvestigationRequest.ID,
		ObservedAt:      result.Signal.End,
		ReceivedAt:      now,
		PayloadJSON:     payload,
	}
}

func buildSignalFingerprint(input StartInput, result StartResult, event store.SignalEvent, now time.Time) store.SignalFingerprint {
	metadata, _ := json.Marshal(map[string]any{
		"kind":           result.Signal.Kind,
		"method":         result.Signal.Method,
		"route":          result.Signal.Route,
		"error_code":     result.Signal.ErrorCode(),
		"status_class":   result.Signal.StatusClass,
		"signature":      result.Signal.Signature,
		"request_id":     result.InvestigationRequest.ID,
		"cluster_id":     result.Cluster.ID,
		"branch_id":      result.Branch.ID,
		"correlation_id": input.CorrelationID,
	})
	return store.SignalFingerprint{
		TenantID:        input.TenantID,
		EnvironmentID:   input.EnvironmentID,
		ServiceID:       input.ServiceID,
		FingerprintHash: fingerprintHash(input),
		Status:          "open",
		FirstSeenAt:     result.Signal.Start,
		LastSeenAt:      result.Signal.End,
		OccurrenceCount: int64(result.Signal.Count),
		SampleEventID:   event.ID,
		MetadataJSON:    metadata,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func appendAudit(ctx context.Context, tx store.Tx, input StartInput, result StartResult, now time.Time) error {
	metadata, err := json.Marshal(map[string]string{
		"request_id":     result.InvestigationRequest.ID,
		"cluster_id":     result.Cluster.ID,
		"branch_id":      result.Branch.ID,
		"decision_id":    result.Decision.ID,
		"signal_kind":    result.Signal.Kind,
		"signal_source":  result.Signal.Source,
		"correlation_id": input.CorrelationID,
	})
	if err != nil {
		return fmt.Errorf("marshal audit metadata: %w", err)
	}
	if err := tx.AuditEvents().Append(ctx, store.AuditEvent{
		TenantID:      input.TenantID,
		ActorID:       input.ActorID,
		ResourceType:  ResourceInvestigationCluster,
		ResourceID:    result.Cluster.ID,
		EventType:     EventInvestigationStarted,
		Message:       result.Decision.Explanation,
		AfterState:    string(result.Cluster.Status),
		CorrelationID: input.CorrelationID,
		MetadataJSON:  metadata,
		CreatedAt:     now,
	}); err != nil {
		return fmt.Errorf("append intake audit event: %w", err)
	}
	return nil
}

func appendOutbox(ctx context.Context, tx store.Tx, input StartInput, result StartResult, outboxID string, now time.Time) error {
	payload, err := json.Marshal(map[string]string{
		"tenant_id":      input.TenantID,
		"environment_id": input.EnvironmentID,
		"service_id":     input.ServiceID,
		"request_id":     result.InvestigationRequest.ID,
		"cluster_id":     result.Cluster.ID,
		"branch_id":      result.Branch.ID,
		"decision_id":    result.Decision.ID,
		"event_type":     EventInvestigationStarted,
		"correlation_id": input.CorrelationID,
	})
	if err != nil {
		return fmt.Errorf("marshal intake outbox payload: %w", err)
	}
	if err := tx.OutboxEvents().Append(ctx, store.OutboxEvent{
		ID:             outboxID,
		TenantID:       input.TenantID,
		EventType:      EventInvestigationStarted,
		ResourceType:   ResourceInvestigationCluster,
		ResourceID:     result.Cluster.ID,
		Status:         "pending",
		NextAttemptAt:  now,
		PayloadJSON:    payload,
		IdempotencyKey: "signal-intake:" + input.TenantID + ":" + result.InvestigationRequest.ID,
		CreatedAt:      now,
	}); err != nil {
		return fmt.Errorf("append intake outbox event: %w", err)
	}
	return nil
}

func stableParts(input StartInput) []string {
	parts := append([]string{
		input.TenantID,
		input.EnvironmentID,
		input.ServiceID,
		input.ServiceName,
		input.CorrelationID,
	}, input.Signal.StableParts()...)
	if input.IdempotencyKey != "" {
		parts = append(parts, input.IdempotencyKey)
	}
	return parts
}

func fingerprintHash(input StartInput) string {
	return engine.StableID("sig", fingerprintParts(input)...)
}

func fingerprintParts(input StartInput) []string {
	return append([]string{
		input.TenantID,
		input.EnvironmentID,
		input.ServiceID,
		input.ServiceName,
	}, input.Signal.StableParts()...)
}

func signalSymptom(signal engine.IncidentSignal) string {
	if signal.Kind == "http_failure" {
		code := signal.ErrorCode()
		label := signal.RouteLabel()
		if label != "" {
			return fmt.Sprintf("Repeated HTTP %s responses for %s", code, label)
		}
		return fmt.Sprintf("Repeated HTTP %s responses", code)
	}
	if signal.Signature != "" {
		return "Repeated log signature: " + truncate(signal.Signature, 96)
	}
	if signal.Kind != "" {
		return "Repeated " + strings.ReplaceAll(signal.Kind, "_", " ") + " signal"
	}
	return "Repeated incident signal"
}

func signalSeverity(signal engine.IncidentSignal) string {
	if signal.StatusCode >= 500 || signal.StatusClass >= 500 {
		return "error"
	}
	if signal.StatusCode >= 400 || signal.StatusClass >= 400 {
		return "warning"
	}
	if signal.Signature != "" || signal.Code != "" {
		return "error"
	}
	return "info"
}

func branchType(signal engine.IncidentSignal) string {
	switch signal.Kind {
	case "http_failure":
		return "availability"
	case "":
		return "general"
	default:
		return strings.ReplaceAll(signal.Kind, " ", "_")
	}
}

func deployVersion(tags []string) string {
	for _, tag := range tags {
		if strings.HasPrefix(tag, "deploy=") {
			return strings.TrimPrefix(tag, "deploy=")
		}
		if strings.HasPrefix(tag, "deploy_version=") {
			return strings.TrimPrefix(tag, "deploy_version=")
		}
	}
	return ""
}

func truncate(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}

func require(errs *[]error, condition bool, message string) {
	if !condition {
		*errs = append(*errs, errors.New(message))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
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
