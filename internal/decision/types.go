package decision

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	DecisionAllow           = "allow"
	DecisionDeny            = "deny"
	DecisionAutoDeny        = "auto_deny"
	DecisionRequireApproval = "require_approval"
	DecisionReviewRequired  = "review_required"
	DecisionFailClosed      = "fail_closed"
	DecisionFailed          = "failed"
	DecisionForceReview     = "force_review_required"
	DecisionBlock           = "block"
	DecisionPass            = "pass"
)

const (
	RiskLow      = "low"
	RiskMedium   = "medium"
	RiskHigh     = "high"
	RiskCritical = "critical"
)

const (
	EffectRead          = "read"
	EffectWrite         = "write"
	EffectExternalWrite = "external_write"
	EffectIrreversible  = "irreversible"
)

const (
	PhasePreContext = "PRE_CONTEXT"
	PhasePreTool    = "PRE_TOOL"
	PhasePreResume  = "PRE_RESUME"
	PhasePreRelease = "PRE_RELEASE"
)

const (
	ErrCodeDecisionDCUExceeded       = "DECISION_DCU_EXCEEDED"
	ErrCodeDecisionDCUProfileMissing = "DECISION_DCU_PROFILE_MISSING"
	ErrCodeFreezeInputMissing        = "FREEZE_INPUT_MISSING"
	ErrCodeFreezeForbiddenDynamic    = "FREEZE_FORBIDDEN_DYNAMIC_FIELD"
	ErrCodeFeatureSignalMissing      = "FEATURE_SIGNAL_CONTRACT_MISSING"
	ErrCodeFeatureSignalPinMismatch  = "FEATURE_SIGNAL_CONTRACT_PIN_MISMATCH"
	ErrCodeDecisionGraphRequired     = "DECISION_GRAPH_REQUIRED"
	ErrCodeSoftRetryExhausted        = "DECISION_RETRY_EXHAUSTED"
	ErrCodeApprovalUnavailable       = "APPROVAL_UNAVAILABLE"
	ErrCodePolicyEngineUnavailable   = "POLICY_ENGINE_UNAVAILABLE"
	ErrCodeDecisionHashInvalid       = "DECISION_HASH_INVALID"
)

const (
	EnforcementObserveOnly  = "observe_only"
	EnforcementAlert        = "alert"
	EnforcementBlockRelease = "block_release"
	EnforcementBlockRuntime = "block_runtime"
	EnforcementMissing      = "missing_metric"
)

const (
	DecisionHashKindStable   = "stable"
	DecisionHashKindFallback = "fallback"
	DecisionHashKindError    = "error"
)

type SoftFailureStage uint8

const (
	SoftFailureStageNone SoftFailureStage = iota
	SoftFailureStageReset
	SoftFailureStageRetrying
	SoftFailureStageExhausted
	SoftFailureStageEscalated
)

var softFailureStageToString = map[SoftFailureStage]string{
	SoftFailureStageNone:      "none",
	SoftFailureStageReset:     "reset",
	SoftFailureStageRetrying:  "retrying",
	SoftFailureStageExhausted: "exhausted",
	SoftFailureStageEscalated: "escalated",
}

var softFailureStageFromString = map[string]SoftFailureStage{
	"none":      SoftFailureStageNone,
	"reset":     SoftFailureStageReset,
	"retrying":  SoftFailureStageRetrying,
	"exhausted": SoftFailureStageExhausted,
	"escalated": SoftFailureStageEscalated,
}

func (s SoftFailureStage) String() string {
	if v, ok := softFailureStageToString[s]; ok {
		return v
	}
	return "unknown"
}

func (s SoftFailureStage) IsValid() bool {
	_, ok := softFailureStageToString[s]
	return ok
}

func ParseSoftFailureStage(raw string) (SoftFailureStage, bool) {
	stage, ok := softFailureStageFromString[strings.ToLower(strings.TrimSpace(raw))]
	return stage, ok
}

func (s SoftFailureStage) MarshalJSON() ([]byte, error) {
	if !s.IsValid() {
		return nil, fmt.Errorf("invalid soft failure stage: %d", s)
	}
	return json.Marshal(s.String())
}

func (s *SoftFailureStage) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("soft failure stage target is nil")
	}
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	stage, ok := ParseSoftFailureStage(raw)
	if !ok {
		return fmt.Errorf("invalid soft failure stage: %q", raw)
	}
	*s = stage
	return nil
}

// FrozenInput is the Phase 1 mandatory freeze object whitelist subset.
type FrozenInput struct {
	ContextCandidatesSnapshotRef       string `json:"context_candidates_snapshot_ref"`
	PolicyBundleSnapshotRef            string `json:"policy_bundle_snapshot_ref"`
	FeatureSnapshotID                  string `json:"feature_snapshot_id"`
	ApprovalRoutingSnapshotRef         string `json:"approval_routing_snapshot_ref"`
	QuotaSnapshotRef                   string `json:"quota_snapshot_ref"`
	SchedulerAdmissionInputSnapshotRef string `json:"scheduler_admission_input_snapshot_ref"`
}

type FreezeLayer struct {
	Frozen      FrozenInput `json:"frozen"`
	DynamicUsed []string    `json:"dynamic_used,omitempty"`
}

// RuntimeDecisionRequest is a deterministic input contract for runtime decision.
type RuntimeDecisionRequest struct {
	RequestID               string      `json:"request_id"`
	TraceID                 string      `json:"trace_id,omitempty"`
	TenantID                string      `json:"tenant_id"`
	WorkflowID              string      `json:"workflow_id"`
	RunID                   string      `json:"run_id"`
	StepID                  string      `json:"step_id"`
	RiskTier                string      `json:"risk_tier"`
	EffectType              string      `json:"effect_type"`
	DecisionGraphID         string      `json:"decision_graph_id,omitempty"`
	ParentDecisionNodeIDs   []string    `json:"parent_decision_node_ids,omitempty"`
	ApprovalSystemAvailable bool        `json:"approval_system_available"`
	Freeze                  FreezeLayer `json:"freeze"`
	Phase                   string      `json:"phase,omitempty"`
	PolicyEngineAvailable   *bool       `json:"policy_engine_available,omitempty"`
	EvidenceFingerprint     string      `json:"evidence_fingerprint,omitempty"`
	AttemptIndex            int         `json:"attempt_index,omitempty"`
	DCUInput                DCUInput    `json:"dcu_input,omitempty"`
	FeatureVersion          string      `json:"feature_version,omitempty"`
	FeatureSchemaVersion    string      `json:"feature_schema_version,omitempty"`
	FeatureEvidenceRef      string      `json:"feature_evidence_ref,omitempty"`
	FeatureProducerID       string      `json:"feature_producer_id,omitempty"`
	FeatureFreshnessMS      int         `json:"feature_freshness_ms,omitempty"`
	FeatureTTLMS            int         `json:"feature_ttl_ms,omitempty"`
	FeatureDriftScore       float64     `json:"feature_drift_score,omitempty"`
	FeatureContractScope    ScopeRef    `json:"feature_contract_scope,omitempty"`
	FeatureContractHashPin  string      `json:"feature_contract_hash_pin,omitempty"`
}

type PolicyInput struct {
	RiskTier   string `json:"risk_tier"`
	EffectType string `json:"effect_type"`
}

type PolicyRule struct {
	RuleID      string       `json:"rule_id"`
	Phase       string       `json:"phase"`
	Priority    int          `json:"priority"`
	Strictness  int          `json:"strictness"`
	Decision    string       `json:"decision,omitempty"`
	Match       PolicyMatch  `json:"match"`
	Obligations []Obligation `json:"obligations,omitempty"`
	FailureMode string       `json:"failure_mode,omitempty"`
}

type PolicyMatch struct {
	RiskTiers   []string `json:"risk_tiers,omitempty"`
	EffectTypes []string `json:"effect_types,omitempty"`
}

type PolicyEvaluation struct {
	MatchedRuleIDs        []string                 `json:"matched_rule_ids"`
	Decision              string                   `json:"decision,omitempty"`
	Obligations           []Obligation             `json:"obligations,omitempty"`
	Conflicts             []string                 `json:"conflicts,omitempty"`
	EffectiveRuleID       string                   `json:"effective_rule_id,omitempty"`
	DecisionOverrideTrace []PolicyDecisionOverride `json:"decision_override_trace,omitempty"`
	ConflictingRulePairs  []string                 `json:"conflicting_rule_pairs,omitempty"`
}

type PolicyDecisionOverride struct {
	FromRuleID   string `json:"from_rule_id"`
	FromDecision string `json:"from_decision"`
	ToRuleID     string `json:"to_rule_id"`
	ToDecision   string `json:"to_decision"`
	Reason       string `json:"reason"`
}

type ObligationPlan struct {
	Obligations      []Obligation           `json:"obligations"`
	Conflicts        []string               `json:"conflicts"`
	ResolutionTraces []ObligationResolution `json:"resolution_traces,omitempty"`
}

type ObligationResolution struct {
	Key                string `json:"key"`
	PreviousPhase      string `json:"previous_phase,omitempty"`
	PreviousStrictness int    `json:"previous_strictness,omitempty"`
	IncomingPhase      string `json:"incoming_phase"`
	IncomingStrictness int    `json:"incoming_strictness"`
	Outcome            string `json:"outcome"`
	Reason             string `json:"reason,omitempty"`
}

type DCUInput struct {
	FeatureReads        int `json:"feature_reads"`
	RuleEvals           int `json:"rule_evals"`
	DependencyCalls     int `json:"dependency_calls"`
	ConflictResolutions int `json:"conflict_resolutions"`
}

type DCUBreakdown struct {
	FeatureReadsCost       int `json:"feature_reads_cost"`
	RuleEvalCost           int `json:"rule_eval_cost"`
	DependencyCallCost     int `json:"dependency_call_cost"`
	ConflictResolutionCost int `json:"conflict_resolution_cost"`
	Total                  int `json:"total"`
}

type DCUProfile struct {
	ProfileID string `json:"profile_id"`
	Limit     int    `json:"limit"`
}

type DecisionAudit struct {
	ErrorCodes            []string     `json:"error_codes,omitempty"`
	DCUUsed               int          `json:"dcu_used,omitempty"`
	DCULimit              int          `json:"dcu_limit,omitempty"`
	DCUProfileID          string       `json:"dcu_profile_id,omitempty"`
	DCURejectReason       string       `json:"dcu_reject_reason,omitempty"`
	DCUComponentBreakdown DCUBreakdown `json:"dcu_component_breakdown,omitempty"`
	TerminalReason        string       `json:"terminal_reason,omitempty"`
	AttemptIndex          int          `json:"attempt_index,omitempty"`
}

type DecisionHashRef struct {
	Value string `json:"value,omitempty"`
	Valid bool   `json:"valid"`
	Kind  string `json:"kind,omitempty"` // stable | fallback | error
}

type FeatureSignalContract struct {
	RequiredFields     []string `json:"required_fields"`
	SchemaVersion      string   `json:"schema_version,omitempty"`
	TrustedProducerIDs []string `json:"trusted_producer_ids,omitempty"`
	MaxFreshnessMS     int      `json:"max_freshness_ms,omitempty"`
	MaxDriftScore      float64  `json:"max_drift_score,omitempty"`
}

type FeatureSignalContractBinding struct {
	Scope            ScopeRef  `json:"scope"`
	ScopeMatchLevel  string    `json:"scope_match_level,omitempty"` // project|workspace|org|global
	KeyMatchLevel    string    `json:"key_match_level,omitempty"`   // exact|risk_wildcard|phase_wildcard|global_wildcard
	ContractKey      string    `json:"contract_key"`
	StorageKey       string    `json:"storage_key,omitempty"`
	Version          int       `json:"version"`
	StoreVersion     uint64    `json:"store_version,omitempty"`
	ContractHash     string    `json:"contract_hash"`
	SchemaVersion    string    `json:"schema_version,omitempty"`
	ResolutionPath   []string  `json:"resolution_path,omitempty"`
	ResolvedAt       time.Time `json:"resolved_at,omitempty"`
	RequiredFields   []string  `json:"required_fields,omitempty"`
	MaxFreshnessMS   int       `json:"max_freshness_ms,omitempty"`
	MaxDriftScore    float64   `json:"max_drift_score,omitempty"`
	TrustedProducers []string  `json:"trusted_producers,omitempty"`
}

type Obligation struct {
	Type       string `json:"type"`
	Target     string `json:"target,omitempty"`
	Value      string `json:"value"`
	Phase      string `json:"phase"`
	Strictness int    `json:"strictness,omitempty"`
}

type RuntimeDecisionResponse struct {
	DecisionID             string                       `json:"decision_id"`
	Decision               string                       `json:"decision"`
	DecisionSubType        string                       `json:"decision_sub_type,omitempty"`
	ReasonCodes            []string                     `json:"reason_codes"`
	MatchedRuleIDs         []string                     `json:"matched_rule_ids"`
	Obligations            []Obligation                 `json:"obligations"`
	FrozenInputHash        string                       `json:"frozen_input_hash"`
	TraceID                string                       `json:"trace_id,omitempty"`
	DecisionGraphID        string                       `json:"decision_graph_id,omitempty"`
	DecisionNodeID         string                       `json:"decision_node_id,omitempty"`
	ParentDecisionNodeIDs  []string                     `json:"parent_decision_node_ids,omitempty"`
	DecisionHash           string                       `json:"decision_hash"`
	DecisionHashIsFallback bool                         `json:"decision_hash_is_fallback,omitempty"`
	DecisionHashRef        DecisionHashRef              `json:"decision_hash_ref,omitempty"`
	FeatureContractBinding FeatureSignalContractBinding `json:"feature_contract_binding,omitempty"`
	SoftFailureStage       SoftFailureStage             `json:"soft_failure_stage,omitempty"`
	Audit                  DecisionAudit                `json:"audit"`
}

type ScheduleAdmissionRequest struct {
	RequestID          string             `json:"request_id"`
	RunID              string             `json:"run_id"`
	StepID             string             `json:"step_id"`
	TenantID           string             `json:"tenant_id"`
	RiskTier           string             `json:"risk_tier"`
	PriorityClass      string             `json:"priority_class"`
	RequestedResources map[string]float64 `json:"requested_resources"`
	QuotaRemaining     map[string]float64 `json:"quota_remaining"`
	IsVIP              bool               `json:"is_vip"`
	AllowPreempt       bool               `json:"allow_preempt"`
}

type DispatchTicket struct {
	TicketID         string             `json:"ticket_id"`
	RunID            string             `json:"run_id"`
	StepID           string             `json:"step_id"`
	TenantID         string             `json:"tenant_id"`
	AllowedResources map[string]float64 `json:"allowed_resources"`
	PriorityClass    string             `json:"priority_class"`
	ExpiresAt        time.Time          `json:"expires_at"`
	DecisionHash     string             `json:"decision_hash"`
	DecisionHashKind string             `json:"decision_hash_kind,omitempty"`
	Preemptable      bool               `json:"preemptable"`
}

type ScheduleAdmissionResponse struct {
	DecisionID             string          `json:"decision_id,omitempty"`
	Decision               string          `json:"decision"`
	QueueClass             string          `json:"queue_class"`
	Preemptable            bool            `json:"preemptable"`
	Ticket                 *DispatchTicket `json:"ticket,omitempty"`
	ReasonCodes            []string        `json:"reason_codes"`
	DecisionHash           string          `json:"decision_hash"`
	DecisionHashIsFallback bool            `json:"decision_hash_is_fallback,omitempty"`
	DecisionHashRef        DecisionHashRef `json:"decision_hash_ref,omitempty"`
}

type ExecutionReceipt struct {
	TicketID      string             `json:"ticket_id"`
	RunID         string             `json:"run_id"`
	StepID        string             `json:"step_id"`
	TenantID      string             `json:"tenant_id"`
	UsedResources map[string]float64 `json:"used_resources"`
	ReceivedAt    time.Time          `json:"received_at"`
}

type ExecutionReceiptResult struct {
	Accepted    bool     `json:"accepted"`
	ReasonCodes []string `json:"reason_codes,omitempty"`
}

type ReleaseEvidence struct {
	EvalPass              bool    `json:"eval_pass"`
	PolicyRegressionPass  bool    `json:"policy_regression_pass"`
	ReplayConsistencyPass bool    `json:"replay_consistency_pass"`
	IncidentTrendScore    float64 `json:"incident_trend_score"`
	HumanSignoff          bool    `json:"human_signoff"`
}

type ReleaseDecisionRequest struct {
	RequestID              string          `json:"request_id"`
	RiskTier               string          `json:"risk_tier"`
	FastPathRequested      bool            `json:"fast_path_requested"`
	ChangeScopeWhitelisted bool            `json:"change_scope_whitelisted"`
	Evidence               ReleaseEvidence `json:"evidence"`
}

type ReleaseDecisionResponse struct {
	DecisionID             string          `json:"decision_id,omitempty"`
	FinalDecision          string          `json:"final_decision"`
	DecisionConfidence     float64         `json:"decision_confidence"`
	EvidenceCompleteness   float64         `json:"evidence_completeness"`
	BlockingReasons        []string        `json:"blocking_reasons,omitempty"`
	ReasonCodes            []string        `json:"reason_codes,omitempty"`
	DecisionHash           string          `json:"decision_hash"`
	DecisionHashIsFallback bool            `json:"decision_hash_is_fallback,omitempty"`
	DecisionHashRef        DecisionHashRef `json:"decision_hash_ref,omitempty"`
}

type ApprovalCase struct {
	CaseID              string    `json:"case_id"`
	RunID               string    `json:"run_id"`
	StepID              string    `json:"step_id"`
	DecisionID          string    `json:"decision_id"`
	DecisionRequestedAt time.Time `json:"decision_requested_at,omitempty"`
	RiskTier            string    `json:"risk_tier"`
	Status              string    `json:"status"`
	CreatedAt           time.Time `json:"created_at"`
	ApprovalHardTimeout time.Time `json:"approval_hard_timeout"`
	Mode                string    `json:"mode"`
	ResolvedAt          time.Time `json:"resolved_at,omitempty"`
	TerminalReason      string    `json:"terminal_reason,omitempty"`
}

type ApprovalCreateRequest struct {
	// Approval case creation is intentionally non-idempotent by default.
	// Each CreateApprovalCase call creates a new review instance.
	RunID      string `json:"run_id"`
	StepID     string `json:"step_id"`
	DecisionID string `json:"decision_id"`
	RiskTier   string `json:"risk_tier"`
	Mode       string `json:"mode"`
}

type ApprovalDecisionRequest struct {
	Decision string `json:"decision"`
}

type ApprovalDecisionResponse struct {
	CaseID                     string   `json:"case_id"`
	Status                     string   `json:"status"`
	PendingStatus              string   `json:"pending_status,omitempty"`
	RunUnlockStatus            string   `json:"run_unlock_status,omitempty"`
	ApprovalEffectiveLatencyMS int64    `json:"approval_effective_latency_ms,omitempty"`
	ReasonCodes                []string `json:"reason_codes,omitempty"`
}

type ApprovalRunUnlockSignal struct {
	CaseID        string    `json:"case_id"`
	RunID         string    `json:"run_id"`
	StepID        string    `json:"step_id"`
	DecisionID    string    `json:"decision_id"`
	RiskTier      string    `json:"risk_tier"`
	Outcome       string    `json:"outcome"`
	PendingStatus string    `json:"pending_status"`
	AttemptIndex  int       `json:"attempt_index,omitempty"`
	Phase         string    `json:"phase,omitempty"`
	OwnerKey      string    `json:"owner_key,omitempty"`
	RequestedAt   time.Time `json:"requested_at"`
	ResolvedAt    time.Time `json:"resolved_at"`
}

type ApprovalRunUnlockDispatchResult struct {
	DispatchStatus            string    `json:"dispatch_status"`
	BusinessActionUnblocked   bool      `json:"business_action_unblocked"`
	BusinessActionUnblockedAt time.Time `json:"business_action_unblocked_at,omitempty"`
	ReasonCodes               []string  `json:"reason_codes,omitempty"`
}

type ApprovalRunUnlockPort interface {
	DispatchApprovalRunUnlock(signal ApprovalRunUnlockSignal) (ApprovalRunUnlockDispatchResult, error)
}

type RunStateReference struct {
	RunID        string    `json:"run_id"`
	State        string    `json:"state"`
	StateVersion int64     `json:"state_version,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

type RunStatePort interface {
	GetRunState(runID string) (RunStateReference, bool)
}

type PendingDecision struct {
	DecisionID     string    `json:"decision_id"`
	RunID          string    `json:"run_id"`
	StepID         string    `json:"step_id"`
	RiskTier       string    `json:"risk_tier"`
	AttemptIndex   int       `json:"attempt_index,omitempty"`
	Phase          string    `json:"phase,omitempty"`
	OwnerKey       string    `json:"owner_key,omitempty"` // run_id|step_id|attempt|phase
	CreatedAt      time.Time `json:"created_at"`
	LastUpdatedAt  time.Time `json:"last_updated_at"`
	RepairAttempts int       `json:"repair_attempts"`
	Status         string    `json:"status"`
}

type ConfirmRunAdvanceRequest struct {
	DecisionID   string `json:"decision_id"`
	RunID        string `json:"run_id"`
	StepID       string `json:"step_id"`
	AttemptIndex int    `json:"attempt_index,omitempty"`
	Phase        string `json:"phase,omitempty"`
	OwnerKey     string `json:"owner_key,omitempty"`
	Success      bool   `json:"success"`
}

type ConfirmRunAdvanceResponse struct {
	Status      string   `json:"status"`
	ReasonCodes []string `json:"reason_codes,omitempty"`
}

type PendingRepairResult struct {
	Scanned       int      `json:"scanned"`
	Repaired      int      `json:"repaired"`
	SafeguardHold int      `json:"safeguard_hold"`
	Escalated     int      `json:"escalated"`
	ReasonCodes   []string `json:"reason_codes,omitempty"`
}

type SideEffectGateRequest struct {
	DecisionID             string             `json:"decision_id"`
	RunID                  string             `json:"run_id"`
	StepID                 string             `json:"step_id"`
	TenantID               string             `json:"tenant_id"`
	// RunState is optional caller context; final gate uses authoritative run state from RunStatePort.
	RunState               string             `json:"run_state"`
	ExecutionReceiptRef    string             `json:"execution_receipt_ref"`
	ExecutionUsedResources map[string]float64 `json:"execution_used_resources,omitempty"`
	ExecutionReceiptAt     time.Time          `json:"execution_receipt_at,omitempty"`
}

type Event struct {
	EventID                  string                 `json:"event_id"`
	EventType                string                 `json:"event_type"`
	SchemaVersion            int                    `json:"schema_version"`
	SourceComponent          string                 `json:"source_component"`
	EvidenceGrade            string                 `json:"evidence_grade,omitempty"`
	PayloadIntegrityRequired bool                   `json:"payload_integrity_required,omitempty"`
	DecisionID               string                 `json:"decision_id,omitempty"`
	RunID                    string                 `json:"run_id,omitempty"`
	StepID                   string                 `json:"step_id,omitempty"`
	EventTS                  time.Time              `json:"event_ts"`
	PayloadRef               string                 `json:"payload_ref,omitempty"`
	PayloadHash              string                 `json:"payload_hash,omitempty"`
	PayloadHashValid         bool                   `json:"payload_hash_valid"`
	Payload                  map[string]interface{} `json:"payload,omitempty"`
}

type StoredDecision struct {
	DecisionID             string                 `json:"decision_id"`
	DecisionType           string                 `json:"decision_type"`
	DecisionKey            string                 `json:"decision_key,omitempty"` // semantic identity key, hash-independent
	Version                int                    `json:"version"`
	DecisionHash           string                 `json:"decision_hash,omitempty"`
	DecisionHashIsFallback bool                   `json:"decision_hash_is_fallback,omitempty"`
	DecisionHashRef        DecisionHashRef        `json:"decision_hash_ref,omitempty"`
	RunID                  string                 `json:"run_id,omitempty"`
	StepID                 string                 `json:"step_id,omitempty"`
	AttemptIndex           int                    `json:"attempt_index,omitempty"`
	Phase                  string                 `json:"phase,omitempty"`
	CreatedAt              time.Time              `json:"created_at"`
	Request                map[string]interface{} `json:"request"`
	Response               map[string]interface{} `json:"response"`
	RequestRaw             json.RawMessage        `json:"request_raw,omitempty"`
	ResponseRaw            json.RawMessage        `json:"response_raw,omitempty"`
}

type DecisionReference struct {
	DecisionID      string          `json:"decision_id"`
	RunID           string          `json:"run_id,omitempty"`
	StepID          string          `json:"step_id,omitempty"`
	AttemptIndex    int             `json:"attempt_index,omitempty"`
	Phase           string          `json:"phase,omitempty"`
	Decision        string          `json:"decision,omitempty"`
	Obligations     []Obligation    `json:"obligations,omitempty"`
	DecisionHashRef DecisionHashRef `json:"decision_hash_ref"`
}

type EnforcementThreshold struct {
	ObserveOnly  float64 `json:"observe_only"`
	Alert        float64 `json:"alert"`
	BlockRelease float64 `json:"block_release"`
	BlockRuntime float64 `json:"block_runtime"`
	Direction    string  `json:"direction"` // gt or lt
}

type MetricsSnapshot struct {
	Counters          map[string]float64 `json:"counters"`
	Rates             map[string]float64 `json:"rates"`
	Ratios            map[string]float64 `json:"ratios,omitempty"`
	Quantiles         map[string]float64 `json:"quantiles,omitempty"`
	EnforcementLevels map[string]string  `json:"enforcement_levels"`
}

type TerminalizePendingDecisionRequest struct {
	DecisionID string `json:"decision_id"`
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
	Actor      string `json:"actor,omitempty"`
}

type TerminalizePendingDecisionResponse struct {
	Status      string   `json:"status"`
	ReasonCodes []string `json:"reason_codes,omitempty"`
}

type ScopeRef struct {
	OrgID       string `json:"org_id"`
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
}

type FeatureSignalContractPublishRequest struct {
	RiskTier            string    `json:"risk_tier"`
	Phase               string    `json:"phase,omitempty"`
	RequiredFields      []string  `json:"required_fields"`
	SchemaVersion       string    `json:"schema_version,omitempty"`
	TrustedProducerIDs  []string  `json:"trusted_producer_ids,omitempty"`
	MaxFreshnessMS      int       `json:"max_freshness_ms,omitempty"`
	MaxDriftScore       float64   `json:"max_drift_score,omitempty"`
	Reason              string    `json:"reason"`
	Actor               string    `json:"actor,omitempty"`
	Scope               ScopeRef  `json:"scope,omitempty"`
	DryRun              bool      `json:"dry_run,omitempty"`
	AllowBreakingChange bool      `json:"allow_breaking_change,omitempty"`
	ActivateAt          time.Time `json:"activate_at,omitempty"`
}

type FeatureSignalContractView struct {
	ContractKey        string    `json:"contract_key"`
	StorageKey         string    `json:"storage_key,omitempty"`
	RiskTier           string    `json:"risk_tier"`
	Phase              string    `json:"phase,omitempty"`
	RequiredFields     []string  `json:"required_fields"`
	SchemaVersion      string    `json:"schema_version,omitempty"`
	TrustedProducerIDs []string  `json:"trusted_producer_ids,omitempty"`
	MaxFreshnessMS     int       `json:"max_freshness_ms,omitempty"`
	MaxDriftScore      float64   `json:"max_drift_score,omitempty"`
	Scope              ScopeRef  `json:"scope"`
	Version            int       `json:"version,omitempty"`
	ContractHash       string    `json:"contract_hash,omitempty"`
	Status             string    `json:"status,omitempty"`
	ActivationAt       time.Time `json:"activation_at,omitempty"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type FeatureSignalContractValidateResponse struct {
	Valid              bool     `json:"valid"`
	ContractKey        string   `json:"contract_key,omitempty"`
	NormalizedRiskTier string   `json:"normalized_risk_tier,omitempty"`
	NormalizedPhase    string   `json:"normalized_phase,omitempty"`
	NormalizedFields   []string `json:"normalized_fields,omitempty"`
	ReasonCodes        []string `json:"reason_codes,omitempty"`
}

type FeatureSignalContractVersion struct {
	Version            int       `json:"version"`
	ContractKey        string    `json:"contract_key"`
	StorageKey         string    `json:"storage_key,omitempty"`
	RiskTier           string    `json:"risk_tier"`
	Phase              string    `json:"phase,omitempty"`
	RequiredFields     []string  `json:"required_fields"`
	SchemaVersion      string    `json:"schema_version,omitempty"`
	TrustedProducerIDs []string  `json:"trusted_producer_ids,omitempty"`
	MaxFreshnessMS     int       `json:"max_freshness_ms,omitempty"`
	MaxDriftScore      float64   `json:"max_drift_score,omitempty"`
	Scope              ScopeRef  `json:"scope"`
	ContractHash       string    `json:"contract_hash,omitempty"`
	Status             string    `json:"status,omitempty"`
	ActivationAt       time.Time `json:"activation_at,omitempty"`
	Reason             string    `json:"reason,omitempty"`
	Actor              string    `json:"actor,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
}

type FeatureSignalContractRollbackRequest struct {
	RiskTier      string   `json:"risk_tier"`
	Phase         string   `json:"phase,omitempty"`
	TargetVersion int      `json:"target_version,omitempty"`
	Reason        string   `json:"reason"`
	Actor         string   `json:"actor,omitempty"`
	Scope         ScopeRef `json:"scope,omitempty"`
}

type FeatureSignalContractRollbackRecord struct {
	RollbackID             string    `json:"rollback_id"`
	ContractKey            string    `json:"contract_key"`
	StorageKey             string    `json:"storage_key,omitempty"`
	Scope                  ScopeRef  `json:"scope"`
	PreviousVersion        int       `json:"previous_version"`
	TargetVersion          int       `json:"target_version"`
	PreviousRequiredFields []string  `json:"previous_required_fields,omitempty"`
	TargetRequiredFields   []string  `json:"target_required_fields,omitempty"`
	PreviousContractHash   string    `json:"previous_contract_hash,omitempty"`
	TargetContractHash     string    `json:"target_contract_hash,omitempty"`
	Reason                 string    `json:"reason"`
	Actor                  string    `json:"actor,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
}

type FeatureDefinitionCreateRequest struct {
	FeatureID   string   `json:"feature_id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Owner       string   `json:"owner"`
	Scope       ScopeRef `json:"scope"`
}

type FeatureDefinition struct {
	FeatureID     string    `json:"feature_id"`
	Name          string    `json:"name"`
	Description   string    `json:"description,omitempty"`
	Owner         string    `json:"owner"`
	Scope         ScopeRef  `json:"scope"`
	ActiveVersion string    `json:"active_version,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type FeatureVersionCreateRequest struct {
	Version            string   `json:"version"`
	ProducerID         string   `json:"producer_id"`
	SchemaVersion      string   `json:"schema_version"`
	EvidenceRef        string   `json:"evidence_ref"`
	UpstreamFeatureIDs []string `json:"upstream_feature_ids,omitempty"`
	DerivationType     string   `json:"derivation_type,omitempty"`
	CriticalPath       bool     `json:"critical_path"`
	DriftScore         float64  `json:"drift_score,omitempty"`
	Scope              ScopeRef `json:"scope"`
}

type FeatureVersion struct {
	FeatureID          string    `json:"feature_id"`
	Version            string    `json:"version"`
	ProducerID         string    `json:"producer_id"`
	SchemaVersion      string    `json:"schema_version"`
	EvidenceRef        string    `json:"evidence_ref"`
	UpstreamFeatureIDs []string  `json:"upstream_feature_ids,omitempty"`
	DerivationType     string    `json:"derivation_type,omitempty"`
	CriticalPath       bool      `json:"critical_path"`
	DriftScore         float64   `json:"drift_score,omitempty"`
	Scope              ScopeRef  `json:"scope"`
	CreatedAt          time.Time `json:"created_at"`
}

type FeatureVersionPublishRequest struct {
	TargetVersion string   `json:"target_version"`
	Reason        string   `json:"reason"`
	Scope         ScopeRef `json:"scope"`
}

type FeatureVersionPublishResponse struct {
	FeatureID       string    `json:"feature_id"`
	PreviousVersion string    `json:"previous_version,omitempty"`
	TargetVersion   string    `json:"target_version"`
	Published       bool      `json:"published"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type FeatureSnapshotBuildRequest struct {
	FeatureID     string            `json:"feature_id"`
	Version       string            `json:"version"`
	ProducerID    string            `json:"producer_id"`
	EvidenceRef   string            `json:"evidence_ref"`
	FreshnessTS   time.Time         `json:"freshness_ts"`
	TTLMS         int               `json:"ttl_ms"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	Scope         ScopeRef          `json:"scope"`
	RequestedBy   string            `json:"requested_by,omitempty"`
	SignatureAlgo string            `json:"signature_algo,omitempty"`
}

type FeatureSnapshot struct {
	SnapshotID     string            `json:"snapshot_id"`
	FeatureID      string            `json:"feature_id"`
	Version        string            `json:"version"`
	ProducerID     string            `json:"producer_id"`
	EvidenceRef    string            `json:"evidence_ref"`
	FreshnessTS    time.Time         `json:"freshness_ts"`
	TTLMS          int               `json:"ttl_ms"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Scope          ScopeRef          `json:"scope"`
	FeatureVersion string            `json:"feature_version"`
	Signature      string            `json:"signature"`
	SignatureAlgo  string            `json:"signature_algo"`
	CreatedAt      time.Time         `json:"created_at"`
}

type FeatureSnapshotFreshnessValidateRequest struct {
	SnapshotID string    `json:"snapshot_id"`
	RiskTier   string    `json:"risk_tier"`
	At         time.Time `json:"at,omitempty"`
	Scope      ScopeRef  `json:"scope"`
}

type FeatureSnapshotFreshnessValidateResponse struct {
	SnapshotID     string `json:"snapshot_id"`
	Fresh          bool   `json:"fresh"`
	Stale          bool   `json:"stale"`
	ReasonCode     string `json:"reason_code,omitempty"`
	RequiredAction string `json:"required_action,omitempty"`
	AgeMS          int64  `json:"age_ms"`
	TTLMS          int    `json:"ttl_ms"`
}

type FeatureSnapshotEvidence struct {
	SnapshotID     string    `json:"snapshot_id"`
	FeatureID      string    `json:"feature_id"`
	FeatureVersion string    `json:"feature_version"`
	ProducerID     string    `json:"producer_id"`
	EvidenceRef    string    `json:"evidence_ref"`
	Signature      string    `json:"signature"`
	Scope          ScopeRef  `json:"scope"`
	CreatedAt      time.Time `json:"created_at"`
}

type FeatureVersionSummary struct {
	Version       string    `json:"version"`
	ProducerID    string    `json:"producer_id"`
	DriftScore    float64   `json:"drift_score"`
	CriticalPath  bool      `json:"critical_path"`
	UpstreamCount int       `json:"upstream_count"`
	CreatedAt     time.Time `json:"created_at"`
}

type FeatureDriftReport struct {
	FeatureID        string                  `json:"feature_id"`
	Scope            ScopeRef                `json:"scope"`
	LatestDriftScore float64                 `json:"latest_drift_score"`
	Trend            string                  `json:"trend"`
	CurrentVersion   string                  `json:"current_version,omitempty"`
	Versions         []FeatureVersionSummary `json:"versions"`
	GeneratedAt      time.Time               `json:"generated_at"`
}

type FeatureRollbackRequest struct {
	TargetVersion string   `json:"target_version"`
	Reason        string   `json:"reason"`
	Scope         ScopeRef `json:"scope"`
}

type FeatureRollbackRecord struct {
	RollbackID      string    `json:"rollback_id"`
	FeatureID       string    `json:"feature_id"`
	PreviousVersion string    `json:"previous_version,omitempty"`
	TargetVersion   string    `json:"target_version"`
	Reason          string    `json:"reason"`
	Scope           ScopeRef  `json:"scope"`
	CreatedAt       time.Time `json:"created_at"`
}

type FeatureDependencyEdge struct {
	From           string `json:"from"`
	To             string `json:"to"`
	DerivationType string `json:"derivation_type,omitempty"`
}

type FeatureDependencyGraph struct {
	FeatureID   string                  `json:"feature_id"`
	Scope       ScopeRef                `json:"scope"`
	Current     string                  `json:"current_version,omitempty"`
	Nodes       []string                `json:"nodes"`
	Edges       []FeatureDependencyEdge `json:"edges"`
	GeneratedAt time.Time               `json:"generated_at"`
}

type ApprovalOrgHealth struct {
	Scope                   ScopeRef  `json:"scope"`
	ActiveApproverRatio     float64   `json:"active_approver_ratio"`
	DelegateFreshness       float64   `json:"delegate_freshness"`
	OverrideDependenceRate  float64   `json:"override_dependence_rate"`
	StaleApproverGroupRatio float64   `json:"stale_approver_group_ratio"`
	RouteToNoActionCases    int       `json:"route_to_no_action_cases"`
	Status                  string    `json:"status"`
	UpdatedAt               time.Time `json:"updated_at"`
}

type ApprovalOrgHealthRemediationRequest struct {
	Scope   ScopeRef `json:"scope"`
	Actions []string `json:"actions"`
	Reason  string   `json:"reason"`
	Actor   string   `json:"actor,omitempty"`
}

type ApprovalOrgHealthRecomputeRequest struct {
	Scope                   ScopeRef `json:"scope"`
	ActiveApproverRatio     float64  `json:"active_approver_ratio"`
	DelegateFreshness       float64  `json:"delegate_freshness"`
	OverrideDependenceRate  float64  `json:"override_dependence_rate"`
	StaleApproverGroupRatio float64  `json:"stale_approver_group_ratio"`
	RouteToNoActionCases    int      `json:"route_to_no_action_cases"`
	Reason                  string   `json:"reason"`
	Actor                   string   `json:"actor,omitempty"`
}

type ApprovalOrgHealthRecomputeResponse struct {
	Accepted  bool      `json:"accepted"`
	ReportID  string    `json:"report_id"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ApprovalOrgHealthRemediationResponse struct {
	Accepted       bool      `json:"accepted"`
	ReportID       string    `json:"report_id"`
	Status         string    `json:"status"`
	AppliedActions []string  `json:"applied_actions"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type ApprovalOrgHealthReport struct {
	ReportID        string    `json:"report_id"`
	Scope           ScopeRef  `json:"scope"`
	Status          string    `json:"status"`
	Recommendations []string  `json:"recommendations"`
	GeneratedAt     time.Time `json:"generated_at"`
}

type EnforcementMatrixValidateRequest struct {
	Matrix map[string]EnforcementThreshold `json:"matrix"`
}

type EnforcementMatrixValidateResponse struct {
	Valid  bool     `json:"valid"`
	Errors []string `json:"errors,omitempty"`
}

type EnforcementMatrixPublishRequest struct {
	Matrix map[string]EnforcementThreshold `json:"matrix"`
	Reason string                          `json:"reason"`
}

type EnforcementMatrixPublishResponse struct {
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
	Reason    string    `json:"reason"`
}

type EnforcementMatrixView struct {
	Version   int                             `json:"version"`
	UpdatedAt time.Time                       `json:"updated_at"`
	Matrix    map[string]EnforcementThreshold `json:"matrix"`
}

type ControlError struct {
	StatusCode  int      `json:"status_code"`
	Code        string   `json:"code"`
	Message     string   `json:"message"`
	ReasonCodes []string `json:"reason_codes,omitempty"`
}

func (e *ControlError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}
