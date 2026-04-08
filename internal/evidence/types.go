package evidence

import "time"

const (
	RiskLow      = "low"
	RiskMedium   = "medium"
	RiskHigh     = "high"
	RiskCritical = "critical"
)

const (
	Tier0 = "tier0"
	Tier1 = "tier1"
	Tier2 = "tier2"
	Tier3 = "tier3"
)

const (
	GradeAudit       = "audit"
	GradeOperational = "operational"
)

const (
	BackpressureLevel0 = 0
	BackpressureLevel1 = 1
	BackpressureLevel2 = 2
	BackpressureLevel3 = 3
)

type Config struct {
	Clock                         func() time.Time
	MaxEventsPerRun               int
	MaxEvidenceBytesPerRun        int
	EventRetention                time.Duration
	WarmRetention                 time.Duration
	ColdRetention                 time.Duration
	OutboxMaxEvents               int
	OutboxRetention               time.Duration
	HighLoadThreshold             float64
	Tier2HighLoadDropRate         float64
	Tier3HighLoadDropRate         float64
	DecisionLogHistoryMax         int
	GlobalAnchorHistoryMax        int
	ExternalAnchorAttestor        ExternalAnchorAttestor
	RequireExternalAnchorForAudit bool
}

type SourceRegistration struct {
	SourceComponent string `json:"source_component"`
	MinSchema       int    `json:"min_schema"`
	DefaultTier     string `json:"default_tier"`
}

type UsageInput struct {
	ResourceType string  `json:"resource_type"`
	UsageAmount  float64 `json:"usage_amount"`
	Unit         string  `json:"unit"`
	CostAmount   float64 `json:"cost_amount,omitempty"`
}

type IngestEventRequest struct {
	EventID                  string                 `json:"event_id"`
	EventType                string                 `json:"event_type"`
	SchemaVersion            int                    `json:"schema_version"`
	SourceComponent          string                 `json:"source_component"`
	RunID                    string                 `json:"run_id"`
	StepID                   string                 `json:"step_id,omitempty"`
	DecisionID               string                 `json:"decision_id,omitempty"`
	TenantID                 string                 `json:"tenant_id,omitempty"`
	ProjectID                string                 `json:"project_id,omitempty"`
	WorkflowID               string                 `json:"workflow_id,omitempty"`
	RiskTier                 string                 `json:"risk_tier,omitempty"`
	EvidenceTier             string                 `json:"evidence_tier,omitempty"`
	EvidenceGrade            string                 `json:"evidence_grade,omitempty"`
	PayloadIntegrityRequired bool                   `json:"payload_integrity_required,omitempty"`
	EventTS                  time.Time              `json:"event_ts,omitempty"`
	PayloadRef               string                 `json:"payload_ref,omitempty"`
	PayloadHash              string                 `json:"payload_hash,omitempty"`
	PayloadHashValid         bool                   `json:"payload_hash_valid"`
	Payload                  map[string]interface{} `json:"payload,omitempty"`
	SystemLoad               float64                `json:"system_load,omitempty"`
	Usage                    *UsageInput            `json:"usage,omitempty"`
}

type CanonicalEvent struct {
	EventID                  string                 `json:"event_id"`
	EventType                string                 `json:"event_type"`
	SchemaVersion            int                    `json:"schema_version"`
	SourceComponent          string                 `json:"source_component"`
	RunID                    string                 `json:"run_id"`
	StepID                   string                 `json:"step_id,omitempty"`
	DecisionID               string                 `json:"decision_id,omitempty"`
	TenantID                 string                 `json:"tenant_id,omitempty"`
	ProjectID                string                 `json:"project_id,omitempty"`
	WorkflowID               string                 `json:"workflow_id,omitempty"`
	RiskTier                 string                 `json:"risk_tier,omitempty"`
	EvidenceTier             string                 `json:"evidence_tier"`
	EvidenceGrade            string                 `json:"evidence_grade"`
	PayloadIntegrityRequired bool                   `json:"payload_integrity_required,omitempty"`
	EventTS                  time.Time              `json:"event_ts"`
	PayloadRef               string                 `json:"payload_ref,omitempty"`
	PayloadHash              string                 `json:"payload_hash,omitempty"`
	PayloadHashValid         bool                   `json:"payload_hash_valid"`
	PayloadTombstoned        bool                   `json:"payload_tombstoned,omitempty"`
	RedactionReason          string                 `json:"redaction_reason,omitempty"`
	Payload                  map[string]interface{} `json:"payload,omitempty"`
	Usage                    *UsageInput            `json:"usage,omitempty"`
	IntegritySeq             int                    `json:"integrity_seq,omitempty"`
	IntegrityPrevHash        string                 `json:"integrity_prev_hash,omitempty"`
	IntegrityHash            string                 `json:"integrity_hash,omitempty"`
	CanonicalHash            string                 `json:"canonical_hash"`
	EstimatedBytes           int                    `json:"estimated_bytes"`
	Degraded                 bool                   `json:"degraded,omitempty"`
	Dropped                  bool                   `json:"dropped,omitempty"`
	DropReason               string                 `json:"drop_reason,omitempty"`
}

type IngestEventResponse struct {
	Accepted    bool      `json:"accepted"`
	Deduped     bool      `json:"deduped"`
	Dropped     bool      `json:"dropped"`
	Degraded    bool      `json:"degraded"`
	EventID     string    `json:"event_id"`
	RunID       string    `json:"run_id"`
	ReasonCodes []string  `json:"reason_codes,omitempty"`
	IngestedAt  time.Time `json:"ingested_at"`
}

type DecisionLog struct {
	DecisionID                string    `json:"decision_id"`
	Version                   int       `json:"version"`
	SourceEventID             string    `json:"source_event_id,omitempty"`
	SourceSchemaVersion       int       `json:"source_schema_version,omitempty"`
	SourceComponent           string    `json:"source_component,omitempty"`
	SourceEventTS             time.Time `json:"source_event_ts,omitempty"`
	DerivedFromEventID        string    `json:"derived_from_event_id,omitempty"`
	SupersededByVersion       int       `json:"superseded_by_version,omitempty"`
	RunID                     string    `json:"run_id"`
	DecisionType              string    `json:"decision_type"`
	DecisionValue             string    `json:"decision_value"`
	DecisionConfidence        float64   `json:"decision_confidence,omitempty"`
	PayloadIntegrityScore     float64   `json:"payload_integrity_score,omitempty"`
	EvidenceCompletenessScore float64   `json:"evidence_completeness_score,omitempty"`
	RationaleRef              string    `json:"rationale_ref,omitempty"`
	CreatedAt                 time.Time `json:"created_at"`
}

type DecisionGraphNode struct {
	NodeID    string    `json:"node_id"`
	RunID     string    `json:"run_id"`
	NodeType  string    `json:"node_type"`
	NodeRef   string    `json:"node_ref"`
	CreatedAt time.Time `json:"created_at"`
}

type DecisionGraphEdge struct {
	EdgeID     string    `json:"edge_id"`
	RunID      string    `json:"run_id"`
	FromNodeID string    `json:"from_node_id"`
	ToNodeID   string    `json:"to_node_id"`
	EdgeType   string    `json:"edge_type"`
	CreatedAt  time.Time `json:"created_at"`
}

type DecisionGraph struct {
	RunID string              `json:"run_id"`
	Nodes []DecisionGraphNode `json:"nodes"`
	Edges []DecisionGraphEdge `json:"edges"`
}

type LedgerEntry struct {
	LedgerID     string    `json:"ledger_id"`
	RunID        string    `json:"run_id"`
	StepID       string    `json:"step_id,omitempty"`
	TenantID     string    `json:"tenant_id,omitempty"`
	ProjectID    string    `json:"project_id,omitempty"`
	WorkflowID   string    `json:"workflow_id,omitempty"`
	ResourceType string    `json:"resource_type"`
	UsageAmount  float64   `json:"usage_amount"`
	Unit         string    `json:"unit"`
	CostAmount   float64   `json:"cost_amount,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type RunEvidenceSummary struct {
	RunID                 string         `json:"run_id"`
	TotalEvents           int            `json:"total_events"`
	TotalBytes            int            `json:"total_bytes"`
	BySource              map[string]int `json:"by_source"`
	ByTier                map[string]int `json:"by_tier"`
	ByGrade               map[string]int `json:"by_grade"`
	LastEventTS           time.Time      `json:"last_event_ts,omitempty"`
	HighRiskGraphComplete bool           `json:"high_risk_graph_complete"`
}

type RootCausePackMode string

const (
	RootCausePackModeMinimal RootCausePackMode = "minimal"
	RootCausePackModeFull    RootCausePackMode = "full"
)

type ReplayPackMode string

const (
	ReplayPackModeMinimal ReplayPackMode = "minimal"
	ReplayPackModeFull    ReplayPackMode = "full"
)

type TimelineItem struct {
	EventID   string    `json:"event_id"`
	EventType string    `json:"event_type"`
	EventTS   time.Time `json:"event_ts"`
}

type KeyEvidence struct {
	EventID    string `json:"event_id"`
	EventType  string `json:"event_type"`
	PayloadRef string `json:"payload_ref,omitempty"`
}

type RootCausePack struct {
	RunID         string            `json:"run_id"`
	Mode          RootCausePackMode `json:"mode"`
	FirstBadNode  string            `json:"first_bad_node,omitempty"`
	CriticalPath  []string          `json:"critical_path"`
	KeyEvidences  []KeyEvidence     `json:"key_evidences"`
	Timeline      []TimelineItem    `json:"timeline"`
	DecisionGraph *DecisionGraph    `json:"decision_graph,omitempty"`
	Ledger        []LedgerEntry     `json:"ledger,omitempty"`
	GeneratedAt   time.Time         `json:"generated_at"`
}

type ReplayEvent struct {
	EventID           string                 `json:"event_id"`
	EventType         string                 `json:"event_type"`
	SchemaVersion     int                    `json:"schema_version"`
	SourceComponent   string                 `json:"source_component"`
	EventTS           time.Time              `json:"event_ts"`
	RunID             string                 `json:"run_id"`
	StepID            string                 `json:"step_id,omitempty"`
	DecisionID        string                 `json:"decision_id,omitempty"`
	PayloadRef        string                 `json:"payload_ref,omitempty"`
	PayloadHash       string                 `json:"payload_hash,omitempty"`
	PayloadHashValid  bool                   `json:"payload_hash_valid"`
	PayloadTombstoned bool                   `json:"payload_tombstoned,omitempty"`
	Payload           map[string]interface{} `json:"payload,omitempty"`
	Redacted          bool                   `json:"redacted,omitempty"`
	RedactionReason   string                 `json:"redaction_reason,omitempty"`
}

type ReplayPack struct {
	RunID                   string         `json:"run_id"`
	Mode                    ReplayPackMode `json:"mode"`
	ManifestVersion         int            `json:"manifest_version"`
	EventCount              int            `json:"event_count"`
	Events                  []ReplayEvent  `json:"events"`
	SnapshotRefs            []string       `json:"snapshot_refs,omitempty"`
	PolicyBundleRefs        []string       `json:"policy_bundle_refs,omitempty"`
	FeatureSnapshotRefs     []string       `json:"feature_snapshot_refs,omitempty"`
	AdapterReceiptRefs      []string       `json:"adapter_receipt_refs,omitempty"`
	DecisionRefs            []string       `json:"decision_refs,omitempty"`
	DecisionGraph           *DecisionGraph `json:"decision_graph,omitempty"`
	Ledger                  []LedgerEntry  `json:"ledger,omitempty"`
	IntegrityRootHash       string         `json:"integrity_root_hash,omitempty"`
	GlobalIntegrityRootHash string         `json:"global_integrity_root_hash,omitempty"`
	GeneratedAt             time.Time      `json:"generated_at"`
	ReasonCodes             []string       `json:"reason_codes,omitempty"`
}

type ArchiveSummary struct {
	RunID          string `json:"run_id,omitempty"`
	HotEventCount  int    `json:"hot_event_count"`
	WarmEventCount int    `json:"warm_event_count"`
	ColdEventCount int    `json:"cold_event_count"`
}

type ArchiveExport struct {
	RunID         string           `json:"run_id"`
	Tier          string           `json:"tier"`
	EventCount    int              `json:"event_count"`
	Events        []CanonicalEvent `json:"events"`
	GeneratedAt   time.Time        `json:"generated_at"`
	IntegrityHash string           `json:"integrity_hash,omitempty"`
	ChainRootHash string           `json:"chain_root_hash,omitempty"`
	Contract      ExportContract   `json:"contract"`
}

type IntegrityAnchor struct {
	AnchorID    string               `json:"anchor_id"`
	RootHash    string               `json:"root_hash"`
	EventCount  int                  `json:"event_count"`
	Reason      string               `json:"reason,omitempty"`
	CreatedAt   time.Time            `json:"created_at"`
	AnchorKind  string               `json:"anchor_kind,omitempty"` // kernel_internal | external_attested | external_attest_failed
	Attestation *ExternalAttestation `json:"attestation,omitempty"`
}

type ExternalAnchorRequest struct {
	AnchorID   string    `json:"anchor_id"`
	RootHash   string    `json:"root_hash"`
	EventCount int       `json:"event_count"`
	Reason     string    `json:"reason,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type ExternalAttestation struct {
	Provider       string    `json:"provider"`
	AttestationRef string    `json:"attestation_ref,omitempty"`
	Signature      string    `json:"signature,omitempty"`
	WormRef        string    `json:"worm_ref,omitempty"`
	AttestedAt     time.Time `json:"attested_at"`
	Status         string    `json:"status"` // attested | failed
	ErrorCode      string    `json:"error_code,omitempty"`
	ErrorMessage   string    `json:"error_message,omitempty"`
}

type ExternalAnchorAttestor func(req ExternalAnchorRequest) (ExternalAttestation, error)

type ExportContract struct {
	IntegrityKind           string `json:"integrity_kind"` // export_digest | chain_digest
	ContainsTombstoned      bool   `json:"contains_tombstoned"`
	ContainsRedacted        bool   `json:"contains_redacted"`
	ReplayEligible          bool   `json:"replay_eligible"`
	AuditArtifactEligible   bool   `json:"audit_artifact_eligible"`
	ExternalAnchorRequired  bool   `json:"external_anchor_required,omitempty"`
	ExternalAnchorSatisfied bool   `json:"external_anchor_satisfied,omitempty"`
	ExternalAnchorID        string `json:"external_anchor_id,omitempty"`
	ExternalAnchorKind      string `json:"external_anchor_kind,omitempty"`
	ContractVersion         int    `json:"contract_version"`
}

type IntegrityVerifyResult struct {
	RunID                   string    `json:"run_id"`
	Verified                bool      `json:"verified"`
	EventCount              int       `json:"event_count"`
	IntegrityRootHash       string    `json:"integrity_root_hash,omitempty"`
	GlobalIntegrityRootHash string    `json:"global_integrity_root_hash,omitempty"`
	GlobalVerified          bool      `json:"global_verified,omitempty"`
	FailureEventID          string    `json:"failure_event_id,omitempty"`
	FailureReason           string    `json:"failure_reason,omitempty"`
	VerifiedAt              time.Time `json:"verified_at"`
}

type DSARDeleteRequest struct {
	RequestID        string    `json:"request_id"`
	TenantID         string    `json:"tenant_id"`
	RunID            string    `json:"run_id,omitempty"`
	PayloadRefPrefix string    `json:"payload_ref_prefix,omitempty"`
	RequestedAt      time.Time `json:"requested_at,omitempty"`
}

type DSARDeleteResponse struct {
	RequestID      string    `json:"request_id"`
	TenantID       string    `json:"tenant_id"`
	RunID          string    `json:"run_id,omitempty"`
	DeletedEvents  int       `json:"deleted_events"`
	RedactedEvents int       `json:"redacted_events"`
	Tombstones     int       `json:"tombstones"`
	CompletedAt    time.Time `json:"completed_at"`
	ReasonCodes    []string  `json:"reason_codes,omitempty"`
}

type LedgerAggregateRequest struct {
	RunID         string    `json:"run_id,omitempty"`
	TenantID      string    `json:"tenant_id,omitempty"`
	WorkflowID    string    `json:"workflow_id,omitempty"`
	ResourceType  string    `json:"resource_type,omitempty"`
	WindowStart   time.Time `json:"window_start,omitempty"`
	WindowEnd     time.Time `json:"window_end,omitempty"`
	GroupBy       string    `json:"group_by,omitempty"` // run|tenant|workflow|resource_type
	DetectAnomaly bool      `json:"detect_anomaly,omitempty"`
}

type LedgerAggregateRow struct {
	GroupKey      string  `json:"group_key"`
	EntryCount    int     `json:"entry_count"`
	TotalUsage    float64 `json:"total_usage"`
	TotalCost     float64 `json:"total_cost"`
	AvgUsage      float64 `json:"avg_usage"`
	AvgCost       float64 `json:"avg_cost"`
	MaxUsage      float64 `json:"max_usage"`
	AnomalyScore  float64 `json:"anomaly_score,omitempty"`
	AnomalyReason string  `json:"anomaly_reason,omitempty"`
}

type LedgerAggregateResponse struct {
	Rows        []LedgerAggregateRow `json:"rows"`
	GeneratedAt time.Time            `json:"generated_at"`
}

type KernelEvent struct {
	EventID          string                 `json:"event_id"`
	EventType        string                 `json:"event_type"`
	SchemaVersion    int                    `json:"schema_version"`
	SourceComponent  string                 `json:"source_component"`
	EventTS          time.Time              `json:"event_ts"`
	RunID            string                 `json:"run_id,omitempty"`
	PayloadRef       string                 `json:"payload_ref,omitempty"`
	PayloadHash      string                 `json:"payload_hash,omitempty"`
	PayloadHashValid bool                   `json:"payload_hash_valid"`
	Payload          map[string]interface{} `json:"payload,omitempty"`
}

type SweepResult struct {
	DeletedEvents int `json:"deleted_events"`
	WarmArchived  int `json:"warm_archived"`
	ColdArchived  int `json:"cold_archived"`
	OutboxTrimmed int `json:"outbox_trimmed"`
}

type MetricsSnapshot struct {
	Counters map[string]float64 `json:"counters"`
	Rates    map[string]float64 `json:"rates,omitempty"`
	Gauges   map[string]float64 `json:"gauges,omitempty"`
}

type KernelError struct {
	StatusCode  int      `json:"status_code"`
	Code        string   `json:"code"`
	Message     string   `json:"message"`
	ReasonCodes []string `json:"reason_codes,omitempty"`
}

func (e *KernelError) Error() string {
	return e.Message
}
