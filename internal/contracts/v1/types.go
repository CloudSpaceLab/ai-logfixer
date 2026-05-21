package v1

import "time"

const (
	ContractVersion    = "v1"
	DiagnosisSchemaURL = "https://github.com/CloudSpaceLab/ai-logfixer/contracts/v1/schemas/diagnosis-result.schema.json"
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

type Receipt struct {
	ID                string    `json:"id"`
	DiagnosisID       string    `json:"diagnosis_id"`
	ActionTaken       string    `json:"action_taken"`
	Actor             string    `json:"actor"`
	Approver          string    `json:"approver"`
	Timestamp         time.Time `json:"timestamp"`
	BeforeState       string    `json:"before_state"`
	AfterState        string    `json:"after_state"`
	AuditRefs         []string  `json:"audit_refs"`
	KnowledgeTreeRefs []string  `json:"knowledge_tree_refs"`
}
