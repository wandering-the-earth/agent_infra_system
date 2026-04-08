package run

import "time"

const (
	StateRunning             = "RUNNING"
	StateParked              = "PARKED"
	StateAwaitingApproval    = "AWAITING_APPROVAL"
	StateReconciling         = "RECONCILING"
	StateCompleted           = "COMPLETED"
	StateFailed              = "FAILED"
	StateSafeguardHold       = "SAFEGUARD_HOLD"
	StateForceReviewRequired = "FORCE_REVIEW_REQUIRED"
	StateForceAbort          = "FORCE_ABORT"
	StateAborted             = "ABORTED"
)

const (
	TokenTypePark     = "park"
	TokenTypeApproval = "approval"
	TokenTypeCallback = "callback"
)

const (
	RootTypeCompleted  = "completed_root"
	RootTypePartial    = "partial_root"
	RootTypeReconciled = "reconciled_root"
	RootTypeReviewHold = "review_hold_root"
	RootTypeAborted    = "aborted_root"
)

const (
	StepTypeNormal    = "normal"
	StepTypeSynthetic = "synthetic"
)

const (
	RiskLow      = "low"
	RiskMedium   = "medium"
	RiskHigh     = "high"
	RiskCritical = "critical"
)

type DecisionPort interface {
	IsDecisionConfirmed(decisionID, runID, stepID string) bool
	IsDecisionConfirmedWithOwner(decisionID, runID, stepID string, attemptIndex int, phase string) bool
	GetDecisionReference(decisionID string) (DecisionReference, bool)
	ValidateExecutionReceiptForRun(runID, stepID, tenantID, executionReceiptRef string, usedResources map[string]float64, receivedAt time.Time) (bool, []string)
}

type Config struct {
	Clock                  func() time.Time
	ContinuationTTL        time.Duration
	ContinuationRetention  time.Duration
	IdempotencyTTL         time.Duration
	RunRetention           time.Duration
	CreateRequestRetention time.Duration
	OutboxMaxEvents        int
	OutboxRetention        time.Duration
	RunMaxLifetime         map[string]time.Duration
	ZombieNoProgressWindow time.Duration
	SweepBatchLimit        int
}

type SnapshotInput struct {
	PolicyBundleID         string `json:"policy_bundle_id"`
	ModelProfileID         string `json:"model_profile_id"`
	DependencyBundleID     string `json:"dependency_bundle_id"`
	SkillBundleSetHash     string `json:"skill_bundle_set_hash"`
	ContextPolicySpaceHash string `json:"context_policy_space_hash"`
}

type CreateRunRequest struct {
	// RequestID is idempotent per tenant. Current kernel semantics:
	// `tenant_id + request_id` maps to the same run while the run-scoped create cache exists.
	// Once cache entry expires/removed, the same request_id may be reused to create a new run.
	RequestID       string        `json:"request_id"`
	TenantID        string        `json:"tenant_id"`
	WorkflowID      string        `json:"workflow_id"`
	WorkflowVersion string        `json:"workflow_version"`
	RiskTier        string        `json:"risk_tier"`
	InputPayloadRef string        `json:"input_payload_ref"`
	Snapshot        SnapshotInput `json:"snapshot"`
}

type CreateRunResponse struct {
	RunID                  string    `json:"run_id"`
	SnapshotHash           string    `json:"snapshot_hash"`
	ContextPolicySpaceHash string    `json:"context_policy_space_hash"`
	InitialState           string    `json:"initial_state"`
	CreatedAt              time.Time `json:"created_at"`
}

type DecisionRef struct {
	DecisionID       string `json:"decision_id"`
	DecisionHash     string `json:"decision_hash"`
	DecisionHashKind string `json:"decision_hash_kind,omitempty"` // expected stable|fallback|error
}

type DecisionObligation struct {
	Type       string `json:"type"`
	Target     string `json:"target,omitempty"`
	Value      string `json:"value"`
	Phase      string `json:"phase,omitempty"`
	Strictness int    `json:"strictness,omitempty"`
}

type DecisionReference struct {
	DecisionID   string               `json:"decision_id"`
	RunID        string               `json:"run_id,omitempty"`
	StepID       string               `json:"step_id,omitempty"`
	AttemptIndex int                  `json:"attempt_index,omitempty"`
	Phase        string               `json:"phase,omitempty"`
	Decision     string               `json:"decision,omitempty"`
	Obligations  []DecisionObligation `json:"obligations,omitempty"`
	DecisionHash string               `json:"decision_hash"`
	HashValid    bool                 `json:"hash_valid"`
	HashKind     string               `json:"hash_kind,omitempty"`
}

type AdvanceRunRequest struct {
	ExpectedStateVersion   int64              `json:"expected_state_version"`
	StepID                 string             `json:"step_id"`
	StepVersion            string             `json:"step_version"`
	IdempotencyKey         string             `json:"idempotency_key"`
	DecisionRef            DecisionRef        `json:"decision_ref"`
	ExecutionReceiptRef    string             `json:"execution_receipt_ref"`
	ExecutionUsedResources map[string]float64 `json:"execution_used_resources,omitempty"`
	ExecutionReceiptAt     time.Time          `json:"execution_receipt_at,omitempty"`
	IncomingStepSeqID      int64              `json:"incoming_step_seq_id"`
	InputHash              string             `json:"input_hash"`
	OutputHash             string             `json:"output_hash"`
	NextState              string             `json:"next_state,omitempty"`
}

type AdvanceRunResponse struct {
	NewState                string `json:"new_state"`
	NextAction              string `json:"next_action"`
	StateVersion            int64  `json:"state_version"`
	StepHash                string `json:"step_hash"`
	RunIntegrityRoot        string `json:"run_integrity_root"`
	RunIntegrityRootVersion int64  `json:"run_integrity_root_version"`
	IntegrityRootType       string `json:"integrity_root_type,omitempty"`
	CurrentStepSeqID        int64  `json:"current_step_seq_id"`
}

type ParkRunRequest struct {
	ExpectedStateVersion int64  `json:"expected_state_version"`
	StepID               string `json:"step_id"`
	IncomingStepSeqID    int64  `json:"incoming_step_seq_id"`
	TokenType            string `json:"token_type"`
	ParkReason           string `json:"park_reason"`
	TTLSeconds           int64  `json:"ttl_seconds,omitempty"`
}

type ParkRunResponse struct {
	RunID                string    `json:"run_id"`
	NewState             string    `json:"new_state"`
	StateVersion         int64     `json:"state_version"`
	ContinuationToken    string    `json:"continuation_token"`
	ContinuationType     string    `json:"continuation_type"`
	ExpiresAt            time.Time `json:"expires_at"`
	PartialIntegrityRoot string    `json:"partial_integrity_root"`
}

type ResumeRunRequest struct {
	ContinuationToken    string `json:"continuation_token"`
	ResumeReason         string `json:"resume_reason"`
	ReceiptRef           string `json:"receipt_ref"`
	IncomingStepSeqID    int64  `json:"incoming_step_seq_id"`
	ExpectedSnapshotHash string `json:"expected_snapshot_hash"`
}

type ResumeRunResponse struct {
	ResumeStatus string `json:"resume_status"`
	NewState     string `json:"new_state"`
	StateVersion int64  `json:"state_version"`
}

type AbortRunRequest struct {
	Reason string `json:"reason"`
}

type RunSnapshot struct {
	PolicyBundleID         string    `json:"policy_bundle_id"`
	ModelProfileID         string    `json:"model_profile_id"`
	DependencyBundleID     string    `json:"dependency_bundle_id"`
	SkillBundleSetHash     string    `json:"skill_bundle_set_hash"`
	ContextPolicySpaceHash string    `json:"context_policy_space_hash"`
	SnapshotHash           string    `json:"snapshot_hash"`
	CreatedAt              time.Time `json:"created_at"`
}

type RootAnchors struct {
	CompletedRoot  string `json:"completed_root,omitempty"`
	PartialRoot    string `json:"partial_root,omitempty"`
	ReconciledRoot string `json:"reconciled_root,omitempty"`
	ReviewHoldRoot string `json:"review_hold_root,omitempty"`
	AbortedRoot    string `json:"aborted_root,omitempty"`
}

type RunView struct {
	RunID                 string `json:"run_id"`
	TenantID              string `json:"tenant_id"`
	WorkflowID            string `json:"workflow_id"`
	WorkflowVersion       string `json:"workflow_version"`
	State                 string `json:"state"`
	ActiveStepID          string `json:"active_step_id"`
	StateVersion          int64  `json:"state_version"`
	RiskTier              string `json:"risk_tier"`
	IntegrityChainVersion string `json:"integrity_chain_version"`
	LastStepHash          string `json:"last_step_hash"`
	RunIntegrityRoot      string `json:"run_integrity_root"`
	// RunIntegrityRootVersion increments only when RunIntegrityRoot value changes.
	// Root type transitions with unchanged root hash do not bump this version.
	RunIntegrityRootVersion int64             `json:"run_integrity_root_version"`
	IntegrityRootType       string            `json:"integrity_root_type,omitempty"`
	TerminalReason          string            `json:"terminal_reason,omitempty"`
	TerminalSource          string            `json:"terminal_source,omitempty"`
	AttachedTags            map[string]string `json:"attached_tags,omitempty"`
	CurrentStepSeqID        int64             `json:"current_step_seq_id"`
	CreatedAt               time.Time         `json:"created_at"`
	UpdatedAt               time.Time         `json:"updated_at"`
	LastProgressAt          time.Time         `json:"last_progress_at"`
	Snapshot                RunSnapshot       `json:"snapshot"`
	RootAnchors             RootAnchors       `json:"root_anchors"`
}

type StepIntegrity struct {
	RunID                 string    `json:"run_id"`
	StepID                string    `json:"step_id"`
	StepSeqID             int64     `json:"step_seq_id"`
	StepType              string    `json:"step_type"` // normal | synthetic
	SyntheticType         string    `json:"synthetic_type,omitempty"`
	IntegrityChainVersion string    `json:"integrity_chain_version"`
	PreviousStepHash      string    `json:"previous_step_hash"`
	DecisionHash          string    `json:"decision_hash"`
	InputHash             string    `json:"input_hash"`
	OutputHash            string    `json:"output_hash"`
	StepHash              string    `json:"step_hash"`
	CreatedAt             time.Time `json:"created_at"`
}

type Continuation struct {
	ContinuationID string     `json:"continuation_id"`
	RunID          string     `json:"run_id"`
	StepID         string     `json:"step_id"`
	StepSeqID      int64      `json:"step_seq_id"`
	TokenType      string     `json:"token_type"`
	TokenHash      string     `json:"token_hash"`
	ExpiresAt      time.Time  `json:"expires_at"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	ConsumedAt     *time.Time `json:"consumed_at,omitempty"`
}

type KernelEvent struct {
	EventID                  string                 `json:"event_id"`
	EventType                string                 `json:"event_type"`
	SchemaVersion            int                    `json:"schema_version"`
	SourceComponent          string                 `json:"source_component"`
	EvidenceGrade            string                 `json:"evidence_grade,omitempty"`
	PayloadIntegrityRequired bool                   `json:"payload_integrity_required,omitempty"`
	RunID                    string                 `json:"run_id"`
	StepID                   string                 `json:"step_id,omitempty"`
	EventTS                  time.Time              `json:"event_ts"`
	PayloadRef               string                 `json:"payload_ref,omitempty"`
	PayloadHash              string                 `json:"payload_hash,omitempty"`
	PayloadHashValid         bool                   `json:"payload_hash_valid"`
	Payload                  map[string]interface{} `json:"payload,omitempty"`
}

type SweepResult struct {
	Scanned                  int `json:"scanned"`
	ForceAbortCount          int `json:"force_abort_count"`
	ForceReviewRequiredCount int `json:"force_review_required_count"`
	ZombieTerminatedCount    int `json:"zombie_terminated_count"`
	SkippedIntegrityErrors   int `json:"skipped_integrity_errors,omitempty"`
	DeletedContinuations     int `json:"deleted_continuations,omitempty"`
	DeletedIdempotency       int `json:"deleted_idempotency,omitempty"`
	DeletedRuns              int `json:"deleted_runs,omitempty"`
	DeletedCreateRequests    int `json:"deleted_create_requests,omitempty"`
	OutboxTrimmed            int `json:"outbox_trimmed,omitempty"`
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
