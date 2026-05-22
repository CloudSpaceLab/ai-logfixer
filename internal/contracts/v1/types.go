package v1

import "time"

const (
	ContractVersion                = "v1"
	DiagnosisSchemaURL             = "https://github.com/CloudSpaceLab/ai-logfixer/contracts/v1/schemas/diagnosis-result.schema.json"
	InvestigationRequestSchemaURL  = "https://github.com/CloudSpaceLab/ai-logfixer/contracts/v1/schemas/investigation-request.schema.json"
	InvestigationDecisionSchemaURL = "https://github.com/CloudSpaceLab/ai-logfixer/contracts/v1/schemas/investigation-decision.schema.json"
	RemediationPlanSchemaURL       = "https://github.com/CloudSpaceLab/ai-logfixer/contracts/v1/schemas/remediation-plan.schema.json"
	RemediationAttemptSchemaURL    = "https://github.com/CloudSpaceLab/ai-logfixer/contracts/v1/schemas/remediation-attempt.schema.json"
)

type DiagnosisStatus string

const (
	DiagnosisStatusPending           DiagnosisStatus = "pending"
	DiagnosisStatusComplete          DiagnosisStatus = "complete"
	DiagnosisStatusFailed            DiagnosisStatus = "failed"
	DiagnosisStatusNeedsMoreData     DiagnosisStatus = "needs_more_data"
	DiagnosisStatusUnsupportedSource DiagnosisStatus = "unsupported_source"
	DiagnosisStatusBlockedBySafety   DiagnosisStatus = "blocked_by_safety"
)

type EvidenceType string

const (
	EvidenceTypeLog    EvidenceType = "log"
	EvidenceTypeTrace  EvidenceType = "trace"
	EvidenceTypeMetric EvidenceType = "metric"
	EvidenceTypeDB     EvidenceType = "db"
	EvidenceTypeConfig EvidenceType = "config"
	EvidenceTypeCVE    EvidenceType = "cve"
)

type RedactionState string

const (
	RedactionStateRedacted  RedactionState = "redacted"
	RedactionStateNotNeeded RedactionState = "not_needed"
	RedactionStateUnknown   RedactionState = "unknown"
	RedactionStateFailed    RedactionState = "failed"
)

type SafetyClassification string

const (
	SafetyReadOnly     SafetyClassification = "read_only"
	SafetyLowRisk      SafetyClassification = "low_risk"
	SafetyMediumRisk   SafetyClassification = "medium_risk"
	SafetyHighRisk     SafetyClassification = "high_risk"
	SafetyCriticalRisk SafetyClassification = "critical_risk"
	SafetyBlocked      SafetyClassification = "blocked"
)

type PatchTargetType string

const (
	PatchTargetFile           PatchTargetType = "file"
	PatchTargetDBSchema       PatchTargetType = "db_schema"
	PatchTargetConfig         PatchTargetType = "config"
	PatchTargetDependency     PatchTargetType = "dependency"
	PatchTargetRuntimeSetting PatchTargetType = "runtime_setting"
)

type RollbackType string

const (
	RollbackSnapshot      RollbackType = "snapshot"
	RollbackReversePatch  RollbackType = "reverse_patch"
	RollbackMigrationDown RollbackType = "migration_down"
	RollbackRestoreConfig RollbackType = "restore_config"
	RollbackManualOnly    RollbackType = "manual_only"
	RollbackUnavailable   RollbackType = "unavailable"
)

type SourceType string

const (
	SourceTypeAutomatic   SourceType = "automatic"
	SourceTypeManual      SourceType = "manual"
	SourceTypeIntegration SourceType = "integration"
)

type InvestigationDecisionType string

const (
	InvestigationDecisionStartNew        InvestigationDecisionType = "start_new"
	InvestigationDecisionAttachDuplicate InvestigationDecisionType = "attach_duplicate"
	InvestigationDecisionLinkRelated     InvestigationDecisionType = "link_related"
	InvestigationDecisionQueue           InvestigationDecisionType = "queue"
	InvestigationDecisionReject          InvestigationDecisionType = "reject"
)

type InvestigationStatus string

const (
	InvestigationStatusRequested     InvestigationStatus = "requested"
	InvestigationStatusFingerprinted InvestigationStatus = "fingerprinted"
	InvestigationStatusLinked        InvestigationStatus = "linked"
	InvestigationStatusQueued        InvestigationStatus = "queued"
	InvestigationStatusRunning       InvestigationStatus = "running"
	InvestigationStatusNeedsMoreData InvestigationStatus = "needs_more_data"
	InvestigationStatusCompleted     InvestigationStatus = "completed"
	InvestigationStatusFailed        InvestigationStatus = "failed"
	InvestigationStatusRejected      InvestigationStatus = "rejected"
)

type RemediationStatus string

const (
	RemediationStatusPending          RemediationStatus = "pending"
	RemediationStatusPlanning         RemediationStatus = "planning"
	RemediationStatusAwaitingApproval RemediationStatus = "awaiting_approval"
	RemediationStatusApproved         RemediationStatus = "approved"
	RemediationStatusDenied           RemediationStatus = "denied"
	RemediationStatusRunning          RemediationStatus = "running"
	RemediationStatusMonitoring       RemediationStatus = "monitoring"
	RemediationStatusSucceeded        RemediationStatus = "succeeded"
	RemediationStatusFailed           RemediationStatus = "failed"
	RemediationStatusRolledBack       RemediationStatus = "rolled_back"
	RemediationStatusEscalated        RemediationStatus = "escalated"
)

type ApprovalStatus string

const (
	ApprovalStatusPending  ApprovalStatus = "pending"
	ApprovalStatusApproved ApprovalStatus = "approved"
	ApprovalStatusDenied   ApprovalStatus = "denied"
	ApprovalStatusExpired  ApprovalStatus = "expired"
)

type TimeWindow struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type SignalFingerprint struct {
	Service       string   `json:"service"`
	Symptom       string   `json:"symptom"`
	ErrorCode     string   `json:"error_code"`
	Source        string   `json:"source"`
	DeployVersion string   `json:"deploy_version"`
	Tags          []string `json:"tags"`
}

type ExternalRef struct {
	System   string            `json:"system"`
	Type     string            `json:"type"`
	ID       string            `json:"id"`
	URL      string            `json:"url"`
	Metadata map[string]string `json:"metadata"`
}

type KnowledgeRef struct {
	GraphID      string  `json:"graph_id"`
	NodeID       string  `json:"node_id"`
	NodeType     string  `json:"node_type"`
	Relationship string  `json:"relationship"`
	Confidence   float64 `json:"confidence"`
	Source       string  `json:"source"`
}

type NextAction struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	ActionType  string `json:"action_type"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

type TimelineEvent struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	Severity  string    `json:"severity"`
	Timestamp time.Time `json:"timestamp"`
}

type UIHints struct {
	Icon     string   `json:"icon"`
	Tone     string   `json:"tone"`
	Sections []string `json:"sections"`
}

type CapacitySnapshot struct {
	MaxGlobalActiveInvestigations      int `json:"max_global_active_investigations"`
	CurrentGlobalActiveInvestigations  int `json:"current_global_active_investigations"`
	MaxPerServiceActiveInvestigations  int `json:"max_per_service_active_investigations"`
	CurrentServiceActiveInvestigations int `json:"current_service_active_investigations"`
	MaxRelatedBranchesPerCluster       int `json:"max_related_branches_per_cluster"`
	CurrentRelatedBranches             int `json:"current_related_branches"`
	QueueLimit                         int `json:"queue_limit"`
	CurrentQueueDepth                  int `json:"current_queue_depth"`
}

type DiagnosisResult struct {
	ID                   string                  `json:"id"`
	ContractVersion      string                  `json:"contract_version"`
	SchemaURL            string                  `json:"schema_url"`
	Status               DiagnosisStatus         `json:"status"`
	Summary              string                  `json:"summary"`
	Confidence           float64                 `json:"confidence"`
	SuspectedRootCause   string                  `json:"suspected_root_cause"`
	AffectedServices     []string                `json:"affected_services"`
	EvidenceItems        []EvidenceItem          `json:"evidence_items"`
	Recommendations      []RunbookRecommendation `json:"recommendations"`
	PatchPlan            *PatchPlan              `json:"patch_plan"`
	RollbackPlan         *RollbackPlan           `json:"rollback_plan"`
	SafetyClassification SafetyClassification    `json:"safety_classification"`
	DisplayStatus        string                  `json:"display_status"`
	UserMessage          string                  `json:"user_message"`
	NextActions          []NextAction            `json:"next_actions"`
	TimelineEvents       []TimelineEvent         `json:"timeline_events"`
	ExternalRefs         []ExternalRef           `json:"external_refs"`
	KnowledgeRefs        []KnowledgeRef          `json:"knowledge_refs"`
	CreatedAt            time.Time               `json:"created_at"`
}

type EvidenceItem struct {
	ID             string         `json:"id"`
	Type           EvidenceType   `json:"type"`
	Source         string         `json:"source"`
	Timestamp      time.Time      `json:"timestamp"`
	Title          string         `json:"title"`
	Summary        string         `json:"summary"`
	RawExcerpt     string         `json:"raw_excerpt"`
	RedactionState RedactionState `json:"redaction_state"`
	RelatedIDs     []string       `json:"related_ids"`
	UIHints        UIHints        `json:"ui_hints"`
	ExternalRefs   []ExternalRef  `json:"external_refs"`
	KnowledgeRefs  []KnowledgeRef `json:"knowledge_refs"`
}

type RunbookRecommendation struct {
	ID                  string               `json:"id"`
	Title               string               `json:"title"`
	Reason              string               `json:"reason"`
	Confidence          float64              `json:"confidence"`
	Steps               []string             `json:"steps"`
	RequiredPermissions []string             `json:"required_permissions"`
	EstimatedRisk       SafetyClassification `json:"estimated_risk"`
	RequiresApproval    bool                 `json:"requires_approval"`
}

type PatchPlan struct {
	ID               string               `json:"id"`
	TargetType       PatchTargetType      `json:"target_type"`
	TargetRefs       []string             `json:"target_refs"`
	DiffPreview      DiffPreview          `json:"diff_preview"`
	RiskLevel        SafetyClassification `json:"risk_level"`
	RequiresApproval bool                 `json:"requires_approval"`
	BlockedReasons   []string             `json:"blocked_reasons"`
}

type DiffPreview struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

type RollbackPlan struct {
	ID                   string               `json:"id"`
	RollbackType         RollbackType         `json:"rollback_type"`
	SnapshotRefs         []string             `json:"snapshot_refs"`
	RestoreSteps         []string             `json:"restore_steps"`
	Limitations          []string             `json:"limitations"`
	RiskLevel            SafetyClassification `json:"risk_level"`
	RequiresManualReview bool                 `json:"requires_manual_review"`
}

type InvestigationRequest struct {
	ID                string            `json:"id"`
	ContractVersion   string            `json:"contract_version"`
	SchemaURL         string            `json:"schema_url"`
	SourceType        SourceType        `json:"source_type"`
	SourceName        string            `json:"source_name"`
	RequestedBy       string            `json:"requested_by"`
	Service           string            `json:"service"`
	Symptom           string            `json:"symptom"`
	ErrorCode         string            `json:"error_code"`
	TimeWindow        TimeWindow        `json:"time_window"`
	SignalFingerprint SignalFingerprint `json:"signal_fingerprint"`
	DisplayStatus     string            `json:"display_status"`
	UserMessage       string            `json:"user_message"`
	ExternalRefs      []ExternalRef     `json:"external_refs"`
	KnowledgeRefs     []KnowledgeRef    `json:"knowledge_refs"`
	CreatedAt         time.Time         `json:"created_at"`
}

type InvestigationCluster struct {
	ID             string                `json:"id"`
	Status         InvestigationStatus   `json:"status"`
	PrimaryService string                `json:"primary_service"`
	Summary        string                `json:"summary"`
	ActiveBranches []InvestigationBranch `json:"active_branches"`
	QueuedBranches []InvestigationBranch `json:"queued_branches"`
	TimelineEvents []TimelineEvent       `json:"timeline_events"`
	NextActions    []NextAction          `json:"next_actions"`
	ExternalRefs   []ExternalRef         `json:"external_refs"`
	KnowledgeRefs  []KnowledgeRef        `json:"knowledge_refs"`
	CreatedAt      time.Time             `json:"created_at"`
	UpdatedAt      time.Time             `json:"updated_at"`
}

type InvestigationBranch struct {
	ID                string              `json:"id"`
	ClusterID         string              `json:"cluster_id"`
	BranchType        string              `json:"branch_type"`
	Symptom           string              `json:"symptom"`
	Status            InvestigationStatus `json:"status"`
	SourceRequestIDs  []string            `json:"source_request_ids"`
	DiagnosisResultID string              `json:"diagnosis_result_id"`
	RemediationPlanID string              `json:"remediation_plan_id"`
	DisplayStatus     string              `json:"display_status"`
	UserMessage       string              `json:"user_message"`
	TimelineEvents    []TimelineEvent     `json:"timeline_events"`
	KnowledgeRefs     []KnowledgeRef      `json:"knowledge_refs"`
	CreatedAt         time.Time           `json:"created_at"`
	UpdatedAt         time.Time           `json:"updated_at"`
}

type InvestigationDecision struct {
	ID               string                    `json:"id"`
	ContractVersion  string                    `json:"contract_version"`
	SchemaURL        string                    `json:"schema_url"`
	RequestID        string                    `json:"request_id"`
	Decision         InvestigationDecisionType `json:"decision"`
	Explanation      string                    `json:"explanation"`
	UserMessage      string                    `json:"user_message"`
	ClusterID        string                    `json:"cluster_id"`
	BranchID         string                    `json:"branch_id"`
	CapacitySnapshot CapacitySnapshot          `json:"capacity_snapshot"`
	NextActions      []NextAction              `json:"next_actions"`
	CreatedAt        time.Time                 `json:"created_at"`
}

type RemediationPlan struct {
	ID                string               `json:"id"`
	ContractVersion   string               `json:"contract_version"`
	SchemaURL         string               `json:"schema_url"`
	DiagnosisResultID string               `json:"diagnosis_result_id"`
	Summary           string               `json:"summary"`
	FixPreview        DiffPreview          `json:"fix_preview"`
	RollbackPlan      RollbackPlan         `json:"rollback_plan"`
	RiskLevel         SafetyClassification `json:"risk_level"`
	ApprovalRequired  bool                 `json:"approval_required"`
	Status            RemediationStatus    `json:"status"`
	DisplayStatus     string               `json:"display_status"`
	UserMessage       string               `json:"user_message"`
	NextActions       []NextAction         `json:"next_actions"`
	TimelineEvents    []TimelineEvent      `json:"timeline_events"`
	ExternalRefs      []ExternalRef        `json:"external_refs"`
	KnowledgeRefs     []KnowledgeRef       `json:"knowledge_refs"`
	CreatedAt         time.Time            `json:"created_at"`
}

type ApprovalRequest struct {
	ID                string               `json:"id"`
	RemediationPlanID string               `json:"remediation_plan_id"`
	Reason            string               `json:"reason"`
	RiskLevel         SafetyClassification `json:"risk_level"`
	RequestedBy       string               `json:"requested_by"`
	Status            ApprovalStatus       `json:"status"`
	Approver          string               `json:"approver"`
	DecidedAt         *time.Time           `json:"decided_at"`
	UserMessage       string               `json:"user_message"`
	NextActions       []NextAction         `json:"next_actions"`
	ExternalRefs      []ExternalRef        `json:"external_refs"`
}

type MonitorSummary struct {
	Status   string   `json:"status"`
	Message  string   `json:"message"`
	Signals  []string `json:"signals"`
	Duration string   `json:"duration"`
}

type RemediationAttempt struct {
	ID                  string            `json:"id"`
	ContractVersion     string            `json:"contract_version"`
	SchemaURL           string            `json:"schema_url"`
	RemediationPlanID   string            `json:"remediation_plan_id"`
	ApprovalRequestID   string            `json:"approval_request_id"`
	Status              RemediationStatus `json:"status"`
	ExecutionStartedAt  *time.Time        `json:"execution_started_at"`
	ExecutionFinishedAt *time.Time        `json:"execution_finished_at"`
	MonitorSummary      MonitorSummary    `json:"monitor_summary"`
	RollbackAttemptID   string            `json:"rollback_attempt_id"`
	DisplayStatus       string            `json:"display_status"`
	UserMessage         string            `json:"user_message"`
	TimelineEvents      []TimelineEvent   `json:"timeline_events"`
	ExternalRefs        []ExternalRef     `json:"external_refs"`
}

type RemediationEvent struct {
	ID                   string        `json:"id"`
	RemediationAttemptID string        `json:"remediation_attempt_id"`
	EventType            string        `json:"event_type"`
	Message              string        `json:"message"`
	Severity             string        `json:"severity"`
	Timestamp            time.Time     `json:"timestamp"`
	UIHints              UIHints       `json:"ui_hints"`
	ExternalRefs         []ExternalRef `json:"external_refs"`
}

type Receipt struct {
	ID                   string          `json:"id"`
	DiagnosisID          string          `json:"diagnosis_id"`
	RemediationPlanID    string          `json:"remediation_plan_id"`
	RemediationAttemptID string          `json:"remediation_attempt_id"`
	ActionTaken          string          `json:"action_taken"`
	Actor                string          `json:"actor"`
	Approver             string          `json:"approver"`
	Timestamp            time.Time       `json:"timestamp"`
	BeforeState          string          `json:"before_state"`
	AfterState           string          `json:"after_state"`
	Outcome              string          `json:"outcome"`
	Summary              string          `json:"summary"`
	TimelineEvents       []TimelineEvent `json:"timeline_events"`
	ExternalRefs         []ExternalRef   `json:"external_refs"`
	KnowledgeRefs        []KnowledgeRef  `json:"knowledge_refs"`
}
