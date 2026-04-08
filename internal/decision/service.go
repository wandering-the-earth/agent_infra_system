package decision

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var marshalJSON = json.Marshal

var validRisk = map[string]struct{}{
	RiskLow:      {},
	RiskMedium:   {},
	RiskHigh:     {},
	RiskCritical: {},
}

var validEffect = map[string]struct{}{
	EffectRead:          {},
	EffectWrite:         {},
	EffectExternalWrite: {},
	EffectIrreversible:  {},
}

var allowedDynamicFields = map[string]struct{}{
	"trace_tags":           {},
	"debug_hints_low_risk": {},
}

var obligationOrder = map[string]int{
	"limit_param":      1,
	"attach_tag":       2,
	"require_template": 3,
	"emit_audit":       4,
}

var phaseOrder = map[string]int{
	PhasePreContext: 1,
	PhasePreTool:    2,
	PhasePreResume:  3,
	PhasePreRelease: 4,
}

const (
	pendingStatusPending         = "pending_decision"
	pendingStatusConfirmed       = "decision_confirmed"
	pendingStatusSafeguardHold   = "safeguard_hold"
	pendingStatusEscalatedOncall = "escalated_oncall"
	pendingStatusManuallyClosed  = "manually_closed"
)

const (
	cleanupTimeSourceWallClock = "wall_clock"
	cleanupTimeSourceEventTime = "event_time"
)

const (
	eventDecisionDCUProfileMissing                  = "decision_dcu_profile_missing_event"
	eventDecisionDCUExceeded                        = "decision_dcu_exceeded_event"
	eventFeatureStaleUsed                           = "feature_stale_used_event"
	eventFeatureSignalContractMissing               = "feature_signal_contract_missing_event"
	eventDecisionRuntimeEvaluated                   = "decision.runtime.evaluated"
	eventPendingDuplicateEvaluate                   = "pending_duplicate_evaluate"
	eventDecisionScheduleRejected                   = "decision.schedule.rejected"
	eventDecisionScheduleAdmitted                   = "decision.schedule.admitted"
	eventDecisionReleaseBlocked                     = "decision.release.blocked"
	eventApprovalCaseCreated                        = "approval.case.created"
	eventApprovalCaseDecided                        = "approval.case.decided"
	eventApprovalHardTimeout                        = "approval_hard_timeout_event"
	eventApprovalPendingUpdated                     = "approval.pending.updated"
	eventApprovalRunUnlockDispatch                  = "approval.run_unlock.dispatch"
	eventDecisionConfirmed                          = "decision_confirmed"
	eventPendingDecisionStillPending                = "pending_decision_still_pending"
	eventPendingDecisionRetryConfirm                = "pending_decision_retry_confirm"
	eventPendingDecisionCompareRunState             = "pending_decision_compare_run_state"
	eventPendingDecisionSafeguardHold               = "pending_decision_enter_safeguard_hold"
	eventPendingDecisionEscalateOncall              = "pending_decision_escalate_oncall"
	eventDecisionRetryExhausted                     = "decision_retry_exhausted_event"
	eventPendingDecisionTerminalized                = "pending_decision_terminalized"
	eventPendingOwnerMismatch                       = "pending_owner_mismatch"
	eventFeatureSignalContractSchedulerCommitFailed = "feature_signal_contract.scheduler.commit_failed"
)

type Config struct {
	Clock                      func() time.Time
	DCUProfiles                map[string]DCUProfile
	FeatureSignalContracts     map[string]FeatureSignalContract
	FeatureSignalContractStore FeatureSignalContractStore
	ApprovalRunUnlockPort      ApprovalRunUnlockPort
	RunStatePort               RunStatePort
	DecisionRetryLimitPerStep  int
	PendingDecisionTTL         time.Duration
	PendingRepairWorkerID      string
	TicketTTL                  time.Duration
	ApprovalHardTimeoutByRisk  map[string]time.Duration
	PolicyRules                []PolicyRule
	MetricMatrix               map[string]EnforcementThreshold
	OutboxMaxEvents            int
	OutboxRetention            time.Duration
	CleanupTimeSource          string
	TicketRetention            time.Duration
	PendingRetention           time.Duration
	ApprovalRetention          time.Duration
	DecisionRetention          time.Duration
	DecisionMaxHistoryVersions int
	RequireHighRiskContractPin bool
}

type Service struct {
	// Locking policy:
	// 1. Prefer single-domain lock per operation.
	// 2. If nested locking is unavoidable, only allow domain_lock -> outboxMu.
	// 3. Never acquire domain locks from inside outboxMu critical sections.
	runtimeMu  sync.RWMutex
	ticketMu   sync.RWMutex
	approvalMu sync.RWMutex
	outboxMu   sync.RWMutex

	cfg                            Config
	contractStore                  FeatureSignalContractStore
	approvalRunUnlockPort          ApprovalRunUnlockPort
	runStatePort                   RunStatePort
	contractCacheWatcherCancel     context.CancelFunc
	contractActivationWorkerCancel context.CancelFunc

	decisions          map[string]StoredDecision
	decisionHistory    map[string][]StoredDecision
	pending            map[string]*PendingDecision
	tickets            map[string]*DispatchTicket
	approvals          map[string]*ApprovalCase
	outbox             []Event
	maxObservedEventTS time.Time

	softFailures map[string]softFailureState

	featureDefinitions             map[string]FeatureDefinition
	featureVersions                map[string]map[string]FeatureVersion
	featureSnapshots               map[string]FeatureSnapshot
	featureRollbacks               map[string][]FeatureRollbackRecord
	featureSignalContractsScoped   map[string]FeatureSignalContract
	featureSignalContractMeta      map[string]time.Time
	featureSignalContractHistory   map[string][]FeatureSignalContractVersion
	featureSignalContractRollbacks map[string][]FeatureSignalContractRollbackRecord
	featureSignalContractScheduled map[string][]FeatureSignalContractVersion

	approvalOrgHealth  map[string]ApprovalOrgHealth
	approvalOrgReports map[string][]ApprovalOrgHealthReport

	counters map[string]float64
	// Kept as atomic because outbox trimming may happen outside runtime domain lock.
	outboxTrimmedTotal uint64
	eventSeq           uint64
	idSeq              uint64
	storeVersion       uint64
	matrixVersion      int
	matrixUpdatedAt    time.Time
}

// Kernel is the canonical Decision Kernel type; Service remains a compatibility alias.
type Kernel = Service

type softFailureState struct {
	RetryCount       int
	LastEvidence     string
	FirstReason      string
	LastReason       string
	LastAttemptIndex int
	LastUpdatedAt    time.Time
}

type pendingOwnerMeta struct {
	AttemptIndex int
	Phase        string
}

type CleanupResult struct {
	DeletedTickets   int `json:"deleted_tickets"`
	DeletedPending   int `json:"deleted_pending"`
	DeletedApprovals int `json:"deleted_approvals"`
	DeletedDecisions int `json:"deleted_decisions"`
	DeletedSoftFails int `json:"deleted_soft_failures"`
	OutboxTrimmed    int `json:"outbox_trimmed"`
}

// NewService is kept for backward compatibility. Prefer NewKernel for new code.
func NewService(cfg Config) *Service {
	return NewKernel(cfg)
}

// NewKernel keeps naming consistent with Run Kernel while preserving NewService compatibility.
func NewKernel(cfg Config) *Kernel {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.DecisionRetryLimitPerStep <= 0 {
		cfg.DecisionRetryLimitPerStep = 3
	}
	if cfg.PendingDecisionTTL <= 0 {
		cfg.PendingDecisionTTL = 10 * time.Minute
	}
	if cfg.PendingRepairWorkerID == "" {
		cfg.PendingRepairWorkerID = "repair-worker-default"
	}
	if cfg.TicketTTL <= 0 {
		cfg.TicketTTL = 5 * time.Minute
	}
	if len(cfg.DCUProfiles) == 0 {
		cfg.DCUProfiles = defaultDCUProfiles()
	}
	if len(cfg.FeatureSignalContracts) == 0 {
		cfg.FeatureSignalContracts = defaultFeatureSignalContracts()
	}
	if len(cfg.ApprovalHardTimeoutByRisk) == 0 {
		cfg.ApprovalHardTimeoutByRisk = map[string]time.Duration{
			RiskLow:      30 * time.Minute,
			RiskMedium:   45 * time.Minute,
			RiskHigh:     60 * time.Minute,
			RiskCritical: 90 * time.Minute,
		}
	}
	if len(cfg.MetricMatrix) == 0 {
		cfg.MetricMatrix = defaultMetricMatrix()
	}
	if len(cfg.PolicyRules) == 0 {
		cfg.PolicyRules = defaultPolicyRules()
	}
	if cfg.OutboxMaxEvents <= 0 {
		cfg.OutboxMaxEvents = 10000
	}
	if cfg.OutboxRetention <= 0 {
		cfg.OutboxRetention = 24 * time.Hour
	}
	cfg.CleanupTimeSource = strings.ToLower(strings.TrimSpace(cfg.CleanupTimeSource))
	if cfg.CleanupTimeSource == "" {
		cfg.CleanupTimeSource = cleanupTimeSourceWallClock
	}
	if cfg.CleanupTimeSource != cleanupTimeSourceWallClock && cfg.CleanupTimeSource != cleanupTimeSourceEventTime {
		cfg.CleanupTimeSource = cleanupTimeSourceWallClock
	}
	if cfg.TicketRetention <= 0 {
		cfg.TicketRetention = 1 * time.Hour
	}
	if cfg.PendingRetention <= 0 {
		cfg.PendingRetention = 24 * time.Hour
	}
	if cfg.ApprovalRetention <= 0 {
		cfg.ApprovalRetention = 72 * time.Hour
	}
	if cfg.DecisionRetention <= 0 {
		cfg.DecisionRetention = 72 * time.Hour
	}
	if cfg.DecisionMaxHistoryVersions <= 0 {
		cfg.DecisionMaxHistoryVersions = 128
	}
	if cfg.FeatureSignalContractStore == nil {
		cfg.FeatureSignalContractStore = NewInMemoryFeatureSignalContractStore(16, FeatureSignalContractStoreSnapshot{})
	}

	bootNow := cfg.Clock()

	kernel := &Kernel{
		cfg:                            cfg,
		decisions:                      make(map[string]StoredDecision),
		decisionHistory:                make(map[string][]StoredDecision),
		pending:                        make(map[string]*PendingDecision),
		tickets:                        make(map[string]*DispatchTicket),
		approvals:                      make(map[string]*ApprovalCase),
		outbox:                         make([]Event, 0, 16),
		softFailures:                   make(map[string]softFailureState),
		featureDefinitions:             make(map[string]FeatureDefinition),
		featureVersions:                make(map[string]map[string]FeatureVersion),
		featureSnapshots:               make(map[string]FeatureSnapshot),
		featureRollbacks:               make(map[string][]FeatureRollbackRecord),
		featureSignalContractsScoped:   make(map[string]FeatureSignalContract),
		featureSignalContractMeta:      make(map[string]time.Time),
		featureSignalContractHistory:   make(map[string][]FeatureSignalContractVersion),
		featureSignalContractRollbacks: make(map[string][]FeatureSignalContractRollbackRecord),
		featureSignalContractScheduled: make(map[string][]FeatureSignalContractVersion),
		approvalOrgHealth:              make(map[string]ApprovalOrgHealth),
		approvalOrgReports:             make(map[string][]ApprovalOrgHealthReport),
		counters:                       make(map[string]float64),
		contractStore:                  cfg.FeatureSignalContractStore,
		approvalRunUnlockPort:          cfg.ApprovalRunUnlockPort,
		runStatePort:                   cfg.RunStatePort,
		storeVersion:                   1,
		matrixVersion:                  1,
		matrixUpdatedAt:                bootNow,
	}
	for key := range cfg.FeatureSignalContracts {
		storageKey := featureSignalContractStorageKey(globalContractScope(), key)
		kernel.featureSignalContractsScoped[storageKey] = cfg.FeatureSignalContracts[key]
		kernel.featureSignalContractMeta[storageKey] = bootNow
		rt, phase := splitContractKey(key)
		kernel.featureSignalContractHistory[storageKey] = []FeatureSignalContractVersion{
			{
				Version:        1,
				ContractKey:    key,
				StorageKey:     storageKey,
				RiskTier:       rt,
				Phase:          phase,
				RequiredFields: append([]string(nil), cfg.FeatureSignalContracts[key].RequiredFields...),
				Scope:          globalContractScope(),
				ContractHash:   safeHashForContract(cfg.FeatureSignalContracts[key]),
				Status:         "active",
				ActivationAt:   bootNow,
				Reason:         "bootstrap_default",
				Actor:          "system",
				CreatedAt:      bootNow,
			},
		}
	}
	kernel.runtimeMu.Lock()
	if kernel.contractStore != nil {
		snap, err := kernel.contractStore.GetSnapshot(context.Background())
		if err == nil && snap.Revision > 0 {
			kernel.applyContractStoreSnapshotLocked(snap)
		} else {
			_ = kernel.publishContractSnapshotLocked(bootNow)
		}
	}
	kernel.runtimeMu.Unlock()
	return kernel
}

func (s *Service) SetApprovalRunUnlockPort(port ApprovalRunUnlockPort) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.approvalRunUnlockPort = port
}

func (s *Service) SetRunStatePort(port RunStatePort) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.runStatePort = port
}

func defaultDCUProfiles() map[string]DCUProfile {
	return map[string]DCUProfile{
		RiskLow:      {ProfileID: "dcu-low-v1", Limit: 60},
		RiskMedium:   {ProfileID: "dcu-medium-v1", Limit: 120},
		RiskHigh:     {ProfileID: "dcu-high-v1", Limit: 180},
		RiskCritical: {ProfileID: "dcu-critical-v1", Limit: 240},
	}
}

func defaultPolicyRules() []PolicyRule {
	return []PolicyRule{
		{
			RuleID:     "rule.high_risk_write.requires_approval",
			Phase:      PhasePreTool,
			Priority:   100,
			Strictness: 100,
			Decision:   DecisionRequireApproval,
			Match: PolicyMatch{
				RiskTiers:   []string{RiskHigh, RiskCritical},
				EffectTypes: []string{EffectExternalWrite, EffectIrreversible},
			},
			Obligations: []Obligation{
				{
					Type:       "require_template",
					Target:     "approval.template",
					Value:      "high_risk_write_template",
					Phase:      PhasePreTool,
					Strictness: 100,
				},
			},
		},
	}
}

func defaultFeatureSignalContracts() map[string]FeatureSignalContract {
	return map[string]FeatureSignalContract{
		"*|*": {
			RequiredFields: []string{"feature_version", "feature_evidence_ref", "feature_producer_id"},
		},
	}
}

func defaultMetricMatrix() map[string]EnforcementThreshold {
	return map[string]EnforcementThreshold{
		"pending_decision_age_p95": {
			ObserveOnly:  0.5,
			Alert:        0.75,
			BlockRelease: 1.0,
			BlockRuntime: 1.5,
			Direction:    "gt",
		},
		"decision_dcu_exceeded_rate": {
			ObserveOnly:  2,
			Alert:        5,
			BlockRelease: 8,
			BlockRuntime: 12,
			Direction:    "gt",
		},
		"feature_stale_rate": {
			ObserveOnly:  2,
			Alert:        5,
			BlockRelease: 8,
			BlockRuntime: 12,
			Direction:    "gt",
		},
		"freeze_whitelist_coverage_rate": {
			ObserveOnly:  99.9,
			Alert:        99.0,
			BlockRelease: 95.0,
			BlockRuntime: 90.0,
			Direction:    "lt",
		},
	}
}

func (s *Service) EvaluateRuntime(req RuntimeDecisionRequest) RuntimeDecisionResponse {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()

	norm := normalizeRuntimeRequest(req)
	now := s.cfg.Clock()
	decisionID, decisionIDOK := s.makeBestEffortID("dec_", struct {
		DecisionClass string `json:"decision_class"`
		TenantID      string `json:"tenant_id"`
		RunID         string `json:"run_id"`
		StepID        string `json:"step_id"`
		AttemptIndex  int    `json:"attempt_index"`
		Phase         string `json:"phase"`
	}{
		DecisionClass: "runtime_primary",
		TenantID:      norm.TenantID,
		RunID:         norm.RunID,
		StepID:        norm.StepID,
		AttemptIndex:  norm.AttemptIndex,
		Phase:         norm.Phase,
	})
	frozenInputHash, frozenHashOK := safeHash(norm.Freeze.Frozen)
	resp := RuntimeDecisionResponse{
		DecisionID:            decisionID,
		ReasonCodes:           make([]string, 0, 4),
		MatchedRuleIDs:        make([]string, 0, 4),
		Obligations:           make([]Obligation, 0, 2),
		FrozenInputHash:       frozenInputHash,
		TraceID:               defaultTraceID(norm),
		DecisionGraphID:       defaultDecisionGraphID(norm),
		DecisionNodeID:        "",
		ParentDecisionNodeIDs: append([]string(nil), norm.ParentDecisionNodeIDs...),
		Audit: DecisionAudit{
			AttemptIndex: norm.AttemptIndex,
		},
	}
	s.counters["runtime_eval_total"]++
	if !decisionIDOK || !frozenHashOK {
		resp.Decision = DecisionFailClosed
		resp.Audit.ErrorCodes = append(resp.Audit.ErrorCodes, ErrCodeDecisionHashInvalid)
		resp.ReasonCodes = append(resp.ReasonCodes, "decision_hash_invalid")
		if !decisionIDOK {
			resp.ReasonCodes = append(resp.ReasonCodes, "decision_id_hash_generation_failed")
		}
		if !frozenHashOK {
			resp.ReasonCodes = append(resp.ReasonCodes, "frozen_input_hash_generation_failed")
		}
		return s.finalizeRuntimeDecision(norm, resp, now)
	}

	if !isValidRisk(norm.RiskTier) {
		resp.Decision = DecisionFailClosed
		resp.Audit.ErrorCodes = append(resp.Audit.ErrorCodes, "INPUT_INVALID_RISK")
		resp.ReasonCodes = append(resp.ReasonCodes, "invalid_risk_tier")
		return s.finalizeRuntimeDecision(norm, resp, now)
	}
	if !isValidEffect(norm.EffectType) {
		resp.Decision = DecisionFailClosed
		resp.Audit.ErrorCodes = append(resp.Audit.ErrorCodes, "INPUT_INVALID_EFFECT")
		resp.ReasonCodes = append(resp.ReasonCodes, "invalid_effect_type")
		return s.finalizeRuntimeDecision(norm, resp, now)
	}
	if isHighOrCritical(norm.RiskTier) && strings.TrimSpace(norm.DecisionGraphID) == "" {
		resp.Decision = DecisionFailClosed
		resp.Audit.ErrorCodes = append(resp.Audit.ErrorCodes, ErrCodeDecisionGraphRequired)
		resp.ReasonCodes = append(resp.ReasonCodes, "decision_graph_required")
		return s.finalizeRuntimeDecision(norm, resp, now)
	}
	featureContract, featureContractBinding, contractFound := s.resolveFeatureSignalContractWithBinding(
		norm.FeatureContractScope,
		norm.RiskTier,
		norm.Phase,
		now,
	)
	resp.FeatureContractBinding = featureContractBinding
	if s.cfg.RequireHighRiskContractPin && isHighOrCritical(norm.RiskTier) && strings.TrimSpace(norm.FeatureContractHashPin) == "" {
		resp.Decision = DecisionFailClosed
		resp.Audit.ErrorCodes = append(resp.Audit.ErrorCodes, ErrCodeFeatureSignalPinMismatch)
		resp.ReasonCodes = append(resp.ReasonCodes, "feature_signal_contract_hash_pin_required")
		resp.MatchedRuleIDs = append(resp.MatchedRuleIDs, "rule.feature.signal_contract_pin_required")
		s.emitEvent(eventFeatureSignalContractMissing, resp.DecisionID, norm.RunID, norm.StepID, map[string]interface{}{
			"risk_tier":                norm.RiskTier,
			"contract_scope":           featureContractBinding.Scope,
			"contract_scope_level":     featureContractBinding.ScopeMatchLevel,
			"contract_key_level":       featureContractBinding.KeyMatchLevel,
			"contract_key":             featureContractBinding.ContractKey,
			"contract_storage_key":     featureContractBinding.StorageKey,
			"contract_version":         featureContractBinding.Version,
			"contract_store_version":   featureContractBinding.StoreVersion,
			"contract_hash":            featureContractBinding.ContractHash,
			"contract_schema_version":  featureContractBinding.SchemaVersion,
			"contract_resolution_path": append([]string(nil), featureContractBinding.ResolutionPath...),
			"contract_required_fields": append([]string(nil), featureContractBinding.RequiredFields...),
			"pin_required":             true,
		}, now)
		return s.finalizeRuntimeDecision(norm, resp, now)
	}
	if pin := strings.TrimSpace(norm.FeatureContractHashPin); pin != "" {
		if !contractFound || !strings.EqualFold(strings.TrimSpace(featureContractBinding.ContractHash), pin) {
			resp.Decision = DecisionFailClosed
			resp.Audit.ErrorCodes = append(resp.Audit.ErrorCodes, ErrCodeFeatureSignalPinMismatch)
			resp.ReasonCodes = append(resp.ReasonCodes, "feature_signal_contract_hash_pin_mismatch")
			resp.MatchedRuleIDs = append(resp.MatchedRuleIDs, "rule.feature.signal_contract_pin")
			s.emitEvent(eventFeatureSignalContractMissing, resp.DecisionID, norm.RunID, norm.StepID, map[string]interface{}{
				"risk_tier":                norm.RiskTier,
				"contract_scope":           featureContractBinding.Scope,
				"contract_scope_level":     featureContractBinding.ScopeMatchLevel,
				"contract_key_level":       featureContractBinding.KeyMatchLevel,
				"contract_key":             featureContractBinding.ContractKey,
				"contract_storage_key":     featureContractBinding.StorageKey,
				"contract_version":         featureContractBinding.Version,
				"contract_store_version":   featureContractBinding.StoreVersion,
				"contract_hash":            featureContractBinding.ContractHash,
				"contract_schema_version":  featureContractBinding.SchemaVersion,
				"contract_resolution_path": append([]string(nil), featureContractBinding.ResolutionPath...),
				"contract_required_fields": append([]string(nil), featureContractBinding.RequiredFields...),
				"pin_type":                 "hash",
				"pin_expected":             pin,
				"pin_actual":               featureContractBinding.ContractHash,
			}, now)
			return s.finalizeRuntimeDecision(norm, resp, now)
		}
	}
	if missing := missingFeatureSignalFieldsByContract(norm, featureContract.RequiredFields); len(missing) > 0 {
		resp.Audit.ErrorCodes = append(resp.Audit.ErrorCodes, ErrCodeFeatureSignalMissing)
		resp.ReasonCodes = append(resp.ReasonCodes, "feature_signal_contract_missing")
		resp.MatchedRuleIDs = append(resp.MatchedRuleIDs, "rule.feature.signal_contract")
		s.emitEvent(eventFeatureSignalContractMissing, resp.DecisionID, norm.RunID, norm.StepID, map[string]interface{}{
			"risk_tier":                norm.RiskTier,
			"missing_fields":           missing,
			"required_fields":          append([]string(nil), featureContract.RequiredFields...),
			"feature_version":          norm.FeatureVersion,
			"feature_schema_version":   norm.FeatureSchemaVersion,
			"contract_scope":           featureContractBinding.Scope,
			"contract_scope_level":     featureContractBinding.ScopeMatchLevel,
			"contract_key_level":       featureContractBinding.KeyMatchLevel,
			"contract_key":             featureContractBinding.ContractKey,
			"contract_storage_key":     featureContractBinding.StorageKey,
			"contract_version":         featureContractBinding.Version,
			"contract_store_version":   featureContractBinding.StoreVersion,
			"contract_hash":            featureContractBinding.ContractHash,
			"contract_schema_version":  featureContractBinding.SchemaVersion,
			"contract_resolution_path": append([]string(nil), featureContractBinding.ResolutionPath...),
			"contract_required_fields": append([]string(nil), featureContractBinding.RequiredFields...),
		}, now)
		if isHighOrCritical(norm.RiskTier) {
			resp.Decision = DecisionFailClosed
		} else {
			resp.Decision = DecisionReviewRequired
		}
		resp = s.applySoftFailureGuard(norm, resp, now)
		return s.finalizeRuntimeDecision(norm, resp, now)
	}
	if trust := evaluateFeatureTrustContract(norm, featureContract); trust.Blocked {
		resp.Decision = trust.Decision
		resp.ReasonCodes = append(resp.ReasonCodes, trust.ReasonCode)
		resp.MatchedRuleIDs = append(resp.MatchedRuleIDs, "rule.feature.trust_contract")
		if trust.Decision == "" {
			resp.Decision = DecisionReviewRequired
		}
		s.emitEvent(eventFeatureSignalContractMissing, resp.DecisionID, norm.RunID, norm.StepID, map[string]interface{}{
			"risk_tier":                norm.RiskTier,
			"contract_scope":           featureContractBinding.Scope,
			"contract_scope_level":     featureContractBinding.ScopeMatchLevel,
			"contract_key_level":       featureContractBinding.KeyMatchLevel,
			"contract_key":             featureContractBinding.ContractKey,
			"contract_storage_key":     featureContractBinding.StorageKey,
			"contract_version":         featureContractBinding.Version,
			"contract_store_version":   featureContractBinding.StoreVersion,
			"contract_hash":            featureContractBinding.ContractHash,
			"contract_schema_version":  featureContractBinding.SchemaVersion,
			"contract_required_fields": append([]string(nil), featureContractBinding.RequiredFields...),
			"reason_code":              trust.ReasonCode,
			"feature_version":          norm.FeatureVersion,
			"feature_schema_version":   norm.FeatureSchemaVersion,
			"feature_producer_id":      norm.FeatureProducerID,
			"feature_freshness_ms":     norm.FeatureFreshnessMS,
			"feature_drift_score":      norm.FeatureDriftScore,
		}, now)
		resp = s.applySoftFailureGuard(norm, resp, now)
		return s.finalizeRuntimeDecision(norm, resp, now)
	}

	profile, ok := s.cfg.DCUProfiles[norm.RiskTier]
	if !ok {
		resp.Decision = DecisionFailClosed
		resp.Audit.ErrorCodes = append(resp.Audit.ErrorCodes, ErrCodeDecisionDCUProfileMissing)
		resp.ReasonCodes = append(resp.ReasonCodes, "dcu_profile_missing")
		s.emitEvent(eventDecisionDCUProfileMissing, resp.DecisionID, norm.RunID, norm.StepID, map[string]interface{}{
			"risk_tier": norm.RiskTier,
		}, now)
		return s.finalizeRuntimeDecision(norm, resp, now)
	}

	breakdown := calcDCU(norm.DCUInput)
	resp.Audit.DCUComponentBreakdown = breakdown
	resp.Audit.DCUUsed = breakdown.Total
	resp.Audit.DCULimit = profile.Limit
	resp.Audit.DCUProfileID = profile.ProfileID
	if breakdown.Total > profile.Limit {
		resp.Audit.ErrorCodes = append(resp.Audit.ErrorCodes, ErrCodeDecisionDCUExceeded)
		s.counters["decision_dcu_exceeded_total"]++
		switch norm.RiskTier {
		case RiskCritical:
			resp.Decision = DecisionFailClosed
			resp.Audit.DCURejectReason = "critical_budget_exceeded_fail_closed"
		case RiskHigh:
			resp.Decision = DecisionReviewRequired
			resp.Audit.DCURejectReason = "high_budget_exceeded_review_required"
		default:
			resp.Decision = DecisionReviewRequired
			resp.Audit.DCURejectReason = "degrade_to_minimal_decision_path"
		}
		resp.ReasonCodes = append(resp.ReasonCodes, "decision_dcu_exceeded")
		resp.MatchedRuleIDs = append(resp.MatchedRuleIDs, "rule.dcu.exceeded")
		s.emitEvent(eventDecisionDCUExceeded, resp.DecisionID, norm.RunID, norm.StepID, map[string]interface{}{
			"dcu_used":   breakdown.Total,
			"dcu_limit":  profile.Limit,
			"risk_tier":  norm.RiskTier,
			"profile_id": profile.ProfileID,
		}, now)
		resp = s.applySoftFailureGuard(norm, resp, now)
		return s.finalizeRuntimeDecision(norm, resp, now)
	}

	missingFrozen := missingFrozenFields(norm.Freeze.Frozen)
	forbiddenDynamic := forbiddenDynamicFields(norm.Freeze.DynamicUsed)
	s.counters["freeze_whitelist_checks_total"]++
	if len(missingFrozen) == 0 && len(forbiddenDynamic) == 0 {
		s.counters["freeze_whitelist_pass_total"]++
	}

	if len(missingFrozen) > 0 {
		resp.Audit.ErrorCodes = append(resp.Audit.ErrorCodes, ErrCodeFreezeInputMissing)
		resp.ReasonCodes = append(resp.ReasonCodes, "freeze_input_missing")
		resp.MatchedRuleIDs = append(resp.MatchedRuleIDs, "rule.freeze.missing")
		if isHighOrCritical(norm.RiskTier) {
			resp.Decision = DecisionFailClosed
		} else {
			resp.Decision = DecisionReviewRequired
		}
		resp = s.applySoftFailureGuard(norm, resp, now)
		return s.finalizeRuntimeDecision(norm, resp, now)
	}

	if len(forbiddenDynamic) > 0 {
		resp.Audit.ErrorCodes = append(resp.Audit.ErrorCodes, ErrCodeFreezeForbiddenDynamic)
		resp.ReasonCodes = append(resp.ReasonCodes, "freeze_forbidden_dynamic_field")
		resp.MatchedRuleIDs = append(resp.MatchedRuleIDs, "rule.freeze.dynamic_forbidden")
		if isHighOrCritical(norm.RiskTier) {
			resp.Decision = DecisionFailClosed
		} else {
			resp.Decision = DecisionReviewRequired
		}
		resp = s.applySoftFailureGuard(norm, resp, now)
		return s.finalizeRuntimeDecision(norm, resp, now)
	}

	if stale := evaluateFeatureStalePolicy(norm); stale.StaleDetected {
		resp.ReasonCodes = append(resp.ReasonCodes, stale.ReasonCode)
		resp.MatchedRuleIDs = append(resp.MatchedRuleIDs, "rule.feature.stale")
		s.counters["feature_stale_total"]++
		action := stale.Decision
		if stale.Allow {
			action = "allow"
		}
		s.emitEvent(eventFeatureStaleUsed, resp.DecisionID, norm.RunID, norm.StepID, map[string]interface{}{
			"feature_freshness_ms": norm.FeatureFreshnessMS,
			"feature_ttl_ms":       norm.FeatureTTLMS,
			"stale_window_ms":      stale.WindowMS,
			"risk_tier":            norm.RiskTier,
			"policy_action":        action,
		}, now)
		if !stale.Allow {
			if stale.Decision == "" {
				resp.Decision = DecisionReviewRequired
			} else {
				resp.Decision = stale.Decision
			}
			resp = s.applySoftFailureGuard(norm, resp, now)
			return s.finalizeRuntimeDecision(norm, resp, now)
		}
	}

	if driftBlocked, blockReason := evaluateFeatureDrift(norm); driftBlocked {
		resp.ReasonCodes = append(resp.ReasonCodes, blockReason)
		resp.MatchedRuleIDs = append(resp.MatchedRuleIDs, "rule.feature.drift")
		s.counters["feature_drift_block_total"]++
		if norm.RiskTier == RiskCritical {
			resp.Decision = DecisionFailClosed
		} else {
			resp.Decision = DecisionReviewRequired
		}
		resp = s.applySoftFailureGuard(norm, resp, now)
		return s.finalizeRuntimeDecision(norm, resp, now)
	}

	policyEngineAvailable := true
	if norm.PolicyEngineAvailable != nil {
		policyEngineAvailable = *norm.PolicyEngineAvailable
	}
	if !policyEngineAvailable {
		resp.Audit.ErrorCodes = append(resp.Audit.ErrorCodes, ErrCodePolicyEngineUnavailable)
		resp.ReasonCodes = append(resp.ReasonCodes, "policy_engine_unavailable")
		resp.MatchedRuleIDs = append(resp.MatchedRuleIDs, "rule.policy.unavailable")
		if isHighOrCritical(norm.RiskTier) {
			resp.Decision = DecisionFailClosed
			resp.Audit.TerminalReason = "policy_engine_unavailable_fail_closed"
		} else {
			resp.Decision = DecisionReviewRequired
			resp.Audit.TerminalReason = "policy_engine_unavailable_review_required"
		}
		resp = s.applySoftFailureGuard(norm, resp, now)
		return s.finalizeRuntimeDecision(norm, resp, now)
	}

	if !norm.ApprovalSystemAvailable {
		resp.Audit.ErrorCodes = append(resp.Audit.ErrorCodes, ErrCodeApprovalUnavailable)
		resp.MatchedRuleIDs = append(resp.MatchedRuleIDs, "rule.approval.unavailable")
		switch norm.RiskTier {
		case RiskLow:
			resp.Decision = DecisionAutoDeny
			resp.ReasonCodes = append(resp.ReasonCodes, "approval_unavailable_auto_deny")
			resp.Audit.TerminalReason = "approval_unavailable_auto_deny"
		case RiskMedium:
			resp.Decision = DecisionReviewRequired
			resp.ReasonCodes = append(resp.ReasonCodes, "approval_unavailable_review_required")
			resp.Audit.TerminalReason = "approval_unavailable_review_required"
		default:
			resp.Decision = DecisionFailClosed
			resp.ReasonCodes = append(resp.ReasonCodes, "approval_unavailable_fail_closed")
			resp.Audit.TerminalReason = "approval_unavailable_fail_closed"
		}
		resp = s.applySoftFailureGuard(norm, resp, now)
		return s.finalizeRuntimeDecision(norm, resp, now)
	}

	policy := evaluatePolicyPhase(PolicyInput{
		RiskTier:   norm.RiskTier,
		EffectType: norm.EffectType,
	}, norm.Phase, s.cfg.PolicyRules)
	resp.MatchedRuleIDs = append(resp.MatchedRuleIDs, policy.MatchedRuleIDs...)
	resp.Obligations = append(resp.Obligations, policy.Obligations...)
	if policy.EffectiveRuleID != "" {
		resp.Audit.TerminalReason = "policy_effective_rule:" + policy.EffectiveRuleID
	}
	if len(policy.Conflicts) > 0 {
		resp.ReasonCodes = append(resp.ReasonCodes, policy.Conflicts...)
	}

	if policy.Decision != "" {
		resp.Decision = policy.Decision
		resp.ReasonCodes = append(resp.ReasonCodes, "policy_decision_applied")
	} else {
		resp.Decision = DecisionAllow
		resp.ReasonCodes = append(resp.ReasonCodes, "baseline_allow")
		resp.MatchedRuleIDs = append(resp.MatchedRuleIDs, "rule.baseline.allow")
	}

	resp = s.applySoftFailureGuard(norm, resp, now)
	return s.finalizeRuntimeDecision(norm, resp, now)
}

func (s *Service) finalizeRuntimeDecision(normReq RuntimeDecisionRequest, resp RuntimeDecisionResponse, now time.Time) RuntimeDecisionResponse {
	normReq = normalizeRuntimeRequest(normReq)
	if strings.TrimSpace(resp.TraceID) == "" {
		resp.TraceID = defaultTraceID(normReq)
	}
	if strings.TrimSpace(resp.DecisionGraphID) == "" {
		resp.DecisionGraphID = defaultDecisionGraphID(normReq)
	}
	if strings.TrimSpace(resp.DecisionNodeID) == "" {
		resp.DecisionNodeID = buildDecisionNodeID(resp.DecisionID, normReq)
	}
	if len(resp.ParentDecisionNodeIDs) == 0 && len(normReq.ParentDecisionNodeIDs) > 0 {
		resp.ParentDecisionNodeIDs = append([]string(nil), normReq.ParentDecisionNodeIDs...)
	}
	resp = finalizeRuntimeResponse(resp)
	resp.DecisionHashIsFallback = false
	resp.DecisionHashRef = DecisionHashRef{
		Value: resp.DecisionHash,
		Valid: strings.TrimSpace(resp.DecisionHash) != "" && !isErrorHash(resp.DecisionHash),
		Kind:  DecisionHashKindStable,
	}
	if strings.TrimSpace(resp.DecisionHash) == "" || isErrorHash(resp.DecisionHash) {
		resp.Decision = DecisionFailClosed
		resp.DecisionSubType = "decision_hash_invalid"
		resp.Audit.ErrorCodes = append(resp.Audit.ErrorCodes, ErrCodeDecisionHashInvalid)
		resp.ReasonCodes = append(resp.ReasonCodes, "decision_hash_invalid")
		resp = finalizeRuntimeResponse(resp)
		resp.DecisionHashIsFallback = false
		resp.DecisionHashRef = DecisionHashRef{
			Value: resp.DecisionHash,
			Valid: strings.TrimSpace(resp.DecisionHash) != "" && !isErrorHash(resp.DecisionHash),
			Kind:  DecisionHashKindStable,
		}
		if strings.TrimSpace(resp.DecisionHash) == "" || isErrorHash(resp.DecisionHash) {
			resp.DecisionHash = s.nextFallbackID("dec_hash_")
			resp.DecisionHashIsFallback = true
			resp.DecisionHashRef = DecisionHashRef{
				Value: resp.DecisionHash,
				Valid: false,
				Kind:  DecisionHashKindFallback,
			}
		}
	}
	s.recordDecisionLocked("runtime", resp.DecisionID, normReq, resp, now)
	if shouldCreatePendingDecision(resp.Decision) {
		s.createPendingDecision(
			resp.DecisionID,
			normReq.RunID,
			normReq.StepID,
			normReq.RiskTier,
			now,
			pendingOwnerMeta{AttemptIndex: normReq.AttemptIndex, Phase: normReq.Phase},
		)
	}
	s.emitEvent(eventDecisionRuntimeEvaluated, resp.DecisionID, normReq.RunID, normReq.StepID, map[string]interface{}{
		"decision":                 resp.Decision,
		"reason_codes":             resp.ReasonCodes,
		"matched_rule_ids":         resp.MatchedRuleIDs,
		"error_codes":              resp.Audit.ErrorCodes,
		"pending_created":          shouldCreatePendingDecision(resp.Decision),
		"decision_hash_kind":       resp.DecisionHashRef.Kind,
		"decision_hash_valid":      resp.DecisionHashRef.Valid,
		"soft_failure_stage":       resp.SoftFailureStage,
		"decision_graph_id":        resp.DecisionGraphID,
		"decision_node_id":         resp.DecisionNodeID,
		"trace_id":                 resp.TraceID,
		"contract_scope":           resp.FeatureContractBinding.Scope,
		"contract_scope_level":     resp.FeatureContractBinding.ScopeMatchLevel,
		"contract_key_level":       resp.FeatureContractBinding.KeyMatchLevel,
		"contract_key":             resp.FeatureContractBinding.ContractKey,
		"contract_storage_key":     resp.FeatureContractBinding.StorageKey,
		"contract_version":         resp.FeatureContractBinding.Version,
		"contract_store_version":   resp.FeatureContractBinding.StoreVersion,
		"contract_hash":            resp.FeatureContractBinding.ContractHash,
		"contract_schema_version":  resp.FeatureContractBinding.SchemaVersion,
		"contract_required_fields": append([]string(nil), resp.FeatureContractBinding.RequiredFields...),
		"contract_resolution_path": append([]string(nil), resp.FeatureContractBinding.ResolutionPath...),
	}, now)
	return resp
}

func (s *Service) EvaluateScheduleAdmission(req ScheduleAdmissionRequest) ScheduleAdmissionResponse {
	now := s.cfg.Clock()
	decisionID, decisionIDOK := s.makeBestEffortID("dec_sched_", struct {
		RequestID          string             `json:"request_id"`
		RunID              string             `json:"run_id"`
		StepID             string             `json:"step_id"`
		TenantID           string             `json:"tenant_id"`
		RiskTier           string             `json:"risk_tier"`
		PriorityClass      string             `json:"priority_class"`
		IsVIP              bool               `json:"is_vip"`
		AllowPreempt       bool               `json:"allow_preempt"`
		RequestedResources map[string]float64 `json:"requested_resources"`
		QuotaRemaining     map[string]float64 `json:"quota_remaining"`
	}{
		RequestID:          req.RequestID,
		RunID:              req.RunID,
		StepID:             req.StepID,
		TenantID:           req.TenantID,
		RiskTier:           strings.ToLower(strings.TrimSpace(req.RiskTier)),
		PriorityClass:      strings.TrimSpace(req.PriorityClass),
		IsVIP:              req.IsVIP,
		AllowPreempt:       req.AllowPreempt,
		RequestedResources: cloneResourceMap(req.RequestedResources),
		QuotaRemaining:     cloneResourceMap(req.QuotaRemaining),
	})
	resp := ScheduleAdmissionResponse{
		DecisionID:  decisionID,
		ReasonCodes: make([]string, 0, 2),
		QueueClass:  "shared",
	}
	if !decisionIDOK {
		resp.Decision = DecisionDeny
		resp.ReasonCodes = append(resp.ReasonCodes, "decision_hash_invalid")
		applyScheduleHash(&resp, s.nextFallbackID("sched_hash_"), false, DecisionHashKindFallback)
		s.persistDecision("schedule_admission", decisionID, req, resp, now)
		return resp
	}

	if req.IsVIP {
		resp.QueueClass = "vip"
	}

	if quotaExceeded(req.RequestedResources, req.QuotaRemaining) {
		resp.Decision = DecisionDeny
		resp.ReasonCodes = append(resp.ReasonCodes, "quota_exceeded")
		quotaHash, ok := safeHash(resp)
		if !ok {
			resp.ReasonCodes = append(resp.ReasonCodes, "decision_hash_invalid")
			quotaHash = s.nextFallbackID("sched_hash_")
			applyScheduleHash(&resp, quotaHash, false, DecisionHashKindFallback)
		} else {
			applyScheduleHash(&resp, quotaHash, true, DecisionHashKindStable)
		}
		s.persistDecision("schedule_admission", decisionID, req, resp, now)
		s.emitEvent(eventDecisionScheduleRejected, decisionID, req.RunID, req.StepID, map[string]interface{}{
			"reason":                 "quota_exceeded",
			"decision_hash":          resp.DecisionHash,
			"decision_hash_kind":     resp.DecisionHashRef.Kind,
			"decision_hash_valid":    resp.DecisionHashRef.Valid,
			"decision_hash_fallback": resp.DecisionHashIsFallback,
		}, now)
		return resp
	}

	preemptable := req.AllowPreempt && !isHighOrCritical(strings.ToLower(req.RiskTier))
	resp.Preemptable = preemptable
	resp.Decision = DecisionAllow
	resp.ReasonCodes = append(resp.ReasonCodes, "admission_allowed")

	admissionDecisionHash, admissionHashOK := safeHash(struct {
		RequestID          string             `json:"request_id"`
		RunID              string             `json:"run_id"`
		StepID             string             `json:"step_id"`
		TenantID           string             `json:"tenant_id"`
		RiskTier           string             `json:"risk_tier"`
		QueueClass         string             `json:"queue_class"`
		Preemptable        bool               `json:"preemptable"`
		Decision           string             `json:"decision"`
		ReasonCodes        []string           `json:"reason_codes"`
		RequestedResources map[string]float64 `json:"requested_resources"`
		QuotaRemaining     map[string]float64 `json:"quota_remaining"`
	}{
		RequestID:          req.RequestID,
		RunID:              req.RunID,
		StepID:             req.StepID,
		TenantID:           req.TenantID,
		RiskTier:           strings.ToLower(strings.TrimSpace(req.RiskTier)),
		QueueClass:         resp.QueueClass,
		Preemptable:        preemptable,
		Decision:           resp.Decision,
		ReasonCodes:        append([]string(nil), resp.ReasonCodes...),
		RequestedResources: cloneResourceMap(req.RequestedResources),
		QuotaRemaining:     cloneResourceMap(req.QuotaRemaining),
	})
	if !admissionHashOK {
		resp.Decision = DecisionDeny
		resp.Preemptable = false
		resp.Ticket = nil
		resp.ReasonCodes = append(resp.ReasonCodes, "decision_hash_invalid")
		applyScheduleHash(&resp, s.nextFallbackID("sched_hash_"), false, DecisionHashKindFallback)
		s.persistDecision("schedule_admission", decisionID, req, resp, now)
		s.emitEvent(eventDecisionScheduleRejected, decisionID, req.RunID, req.StepID, map[string]interface{}{
			"reason":                 "ticket_id_hash_generation_failed",
			"decision_hash":          resp.DecisionHash,
			"decision_hash_kind":     resp.DecisionHashRef.Kind,
			"decision_hash_valid":    resp.DecisionHashRef.Valid,
			"decision_hash_fallback": resp.DecisionHashIsFallback,
		}, now)
		return resp
	}

	ticketID, ticketIDOK := s.makeBestEffortID("ticket_", struct {
		RunID        string `json:"run_id"`
		StepID       string `json:"step_id"`
		DecisionHash string `json:"decision_hash"`
	}{
		RunID: req.RunID, StepID: req.StepID, DecisionHash: admissionDecisionHash,
	})
	if !ticketIDOK {
		resp.Decision = DecisionDeny
		resp.Preemptable = false
		resp.Ticket = nil
		resp.ReasonCodes = append(resp.ReasonCodes, "ticket_id_hash_generation_failed")
		applyScheduleHash(&resp, admissionDecisionHash, true, DecisionHashKindStable)
		s.persistDecision("schedule_admission", decisionID, req, resp, now)
		return resp
	}
	ticket := &DispatchTicket{
		TicketID:         ticketID,
		RunID:            req.RunID,
		StepID:           req.StepID,
		TenantID:         req.TenantID,
		AllowedResources: cloneResourceMap(req.RequestedResources),
		PriorityClass:    req.PriorityClass,
		ExpiresAt:        now.Add(s.cfg.TicketTTL),
		DecisionHash:     admissionDecisionHash,
		Preemptable:      preemptable,
	}
	resp.Ticket = ticket
	applyScheduleHash(&resp, admissionDecisionHash, true, DecisionHashKindStable)

	s.ticketMu.Lock()
	s.tickets[ticket.TicketID] = ticket
	s.ticketMu.Unlock()

	s.persistDecision("schedule_admission", decisionID, req, resp, now)

	s.emitEvent(eventDecisionScheduleAdmitted, decisionID, req.RunID, req.StepID, map[string]interface{}{
		"ticket_id":     ticket.TicketID,
		"queue_class":   resp.QueueClass,
		"preemptable":   preemptable,
		"decision_hash": resp.DecisionHash,
		"hash_kind":     resp.DecisionHashRef.Kind,
		"hash_valid":    resp.DecisionHashRef.Valid,
	}, now)
	return resp
}

func (s *Service) ValidateExecutionReceipt(receipt ExecutionReceipt) ExecutionReceiptResult {
	s.ticketMu.RLock()
	defer s.ticketMu.RUnlock()

	ticket, ok := s.tickets[receipt.TicketID]
	if !ok {
		return ExecutionReceiptResult{Accepted: false, ReasonCodes: []string{"ticket_not_found"}}
	}
	if receipt.ReceivedAt.After(ticket.ExpiresAt) {
		return ExecutionReceiptResult{Accepted: false, ReasonCodes: []string{"ticket_expired"}}
	}
	if strings.TrimSpace(receipt.RunID) == "" || strings.TrimSpace(receipt.StepID) == "" || strings.TrimSpace(receipt.TenantID) == "" {
		return ExecutionReceiptResult{Accepted: false, ReasonCodes: []string{"receipt_binding_missing"}}
	}
	if receipt.RunID != ticket.RunID || receipt.StepID != ticket.StepID || receipt.TenantID != ticket.TenantID {
		return ExecutionReceiptResult{Accepted: false, ReasonCodes: []string{"ticket_binding_mismatch"}}
	}
	for k, used := range receipt.UsedResources {
		limit := ticket.AllowedResources[k]
		if used > limit {
			return ExecutionReceiptResult{Accepted: false, ReasonCodes: []string{"resource_exceed_ticket"}}
		}
	}
	return ExecutionReceiptResult{Accepted: true}
}

// ValidateExecutionReceiptRef enforces ticket-required execution admission on the run path.
// It validates that execution_receipt_ref resolves to a live dispatch ticket bound to run/step/tenant.
func (s *Service) ValidateExecutionReceiptRef(runID, stepID, tenantID, executionReceiptRef string) (bool, []string) {
	now := s.cfg.Clock()
	ticketID := extractTicketIDFromExecutionReceiptRef(executionReceiptRef)
	if ticketID == "" {
		return false, []string{"execution_receipt_ticket_missing"}
	}

	s.ticketMu.RLock()
	defer s.ticketMu.RUnlock()
	ticket, ok := s.tickets[ticketID]
	if !ok {
		return false, []string{"ticket_not_found"}
	}
	if now.After(ticket.ExpiresAt) {
		return false, []string{"ticket_expired"}
	}
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(stepID) == "" {
		return false, []string{"execution_binding_missing"}
	}
	if ticket.RunID != runID || ticket.StepID != stepID {
		return false, []string{"ticket_binding_mismatch"}
	}
	if strings.TrimSpace(tenantID) != "" && ticket.TenantID != tenantID {
		return false, []string{"ticket_binding_mismatch"}
	}
	return true, nil
}

// ValidateExecutionReceiptForRun is the run-path hard gate.
// It enforces both binding correctness and resource usage <= ticket limits.
func (s *Service) ValidateExecutionReceiptForRun(runID, stepID, tenantID, executionReceiptRef string, usedResources map[string]float64, receivedAt time.Time) (bool, []string) {
	ticketID := extractTicketIDFromExecutionReceiptRef(executionReceiptRef)
	if ticketID == "" {
		return false, []string{"execution_receipt_ticket_missing"}
	}
	if len(usedResources) == 0 {
		return false, []string{"execution_receipt_usage_missing"}
	}
	if receivedAt.IsZero() {
		receivedAt = s.cfg.Clock()
	}
	result := s.ValidateExecutionReceipt(ExecutionReceipt{
		TicketID:      ticketID,
		RunID:         runID,
		StepID:        stepID,
		TenantID:      tenantID,
		UsedResources: cloneResourceMap(usedResources),
		ReceivedAt:    receivedAt,
	})
	return result.Accepted, append([]string(nil), result.ReasonCodes...)
}

func (s *Service) EvaluateRelease(req ReleaseDecisionRequest) ReleaseDecisionResponse {
	now := s.cfg.Clock()
	decisionID, decisionIDOK := s.makeBestEffortID("dec_release_", struct {
		RequestID              string          `json:"request_id"`
		RiskTier               string          `json:"risk_tier"`
		FastPathRequested      bool            `json:"fast_path_requested"`
		ChangeScopeWhitelisted bool            `json:"change_scope_whitelisted"`
		Evidence               ReleaseEvidence `json:"evidence"`
	}{
		RequestID:              req.RequestID,
		RiskTier:               strings.ToLower(strings.TrimSpace(req.RiskTier)),
		FastPathRequested:      req.FastPathRequested,
		ChangeScopeWhitelisted: req.ChangeScopeWhitelisted,
		Evidence:               req.Evidence,
	})
	resp := ReleaseDecisionResponse{
		DecisionID:      decisionID,
		BlockingReasons: make([]string, 0, 4),
		ReasonCodes:     make([]string, 0, 4),
	}
	if !decisionIDOK {
		resp.FinalDecision = DecisionBlock
		resp.DecisionConfidence = 0.0
		resp.BlockingReasons = append(resp.BlockingReasons, "decision_id_hash_generation_failed")
		resp.ReasonCodes = append(resp.ReasonCodes, "decision_hash_invalid")
		applyReleaseHash(&resp, s.nextFallbackID("rel_hash_"), false, DecisionHashKindFallback)
		s.persistDecision("release", decisionID, req, resp, now)
		return resp
	}

	risk := strings.ToLower(strings.TrimSpace(req.RiskTier))
	completeness := 0.0
	if req.Evidence.EvalPass {
		completeness += 0.25
	}
	if req.Evidence.PolicyRegressionPass {
		completeness += 0.25
	}
	if req.Evidence.ReplayConsistencyPass {
		completeness += 0.25
	}
	if req.Evidence.HumanSignoff {
		completeness += 0.25
	}
	resp.EvidenceCompleteness = completeness

	if req.FastPathRequested {
		if risk != RiskLow {
			resp.ReasonCodes = append(resp.ReasonCodes, "fast_path_downgraded_non_low_risk")
		}
		if !req.ChangeScopeWhitelisted {
			resp.ReasonCodes = append(resp.ReasonCodes, "fast_path_downgraded_scope_not_whitelisted")
		}
	}

	if isHighOrCritical(risk) && !req.Evidence.HumanSignoff {
		resp.FinalDecision = DecisionBlock
		resp.BlockingReasons = append(resp.BlockingReasons, "human_signoff_missing")
		resp.DecisionConfidence = 0.1
		relHash, ok := safeHash(resp)
		if !ok {
			resp.ReasonCodes = append(resp.ReasonCodes, "decision_hash_invalid")
			relHash = s.nextFallbackID("rel_hash_")
			applyReleaseHash(&resp, relHash, false, DecisionHashKindFallback)
		} else {
			applyReleaseHash(&resp, relHash, true, DecisionHashKindStable)
		}
		s.persistDecision("release", decisionID, req, resp, now)
		s.emitEvent(eventDecisionReleaseBlocked, decisionID, "", "", map[string]interface{}{
			"reason":                 "human_signoff_missing",
			"risk":                   risk,
			"decision_hash":          resp.DecisionHash,
			"decision_hash_kind":     resp.DecisionHashRef.Kind,
			"decision_hash_valid":    resp.DecisionHashRef.Valid,
			"decision_hash_fallback": resp.DecisionHashIsFallback,
		}, now)
		return resp
	}

	if !req.Evidence.EvalPass || !req.Evidence.PolicyRegressionPass || !req.Evidence.ReplayConsistencyPass {
		resp.FinalDecision = DecisionBlock
		if !req.Evidence.EvalPass {
			resp.BlockingReasons = append(resp.BlockingReasons, "eval_failed")
		}
		if !req.Evidence.PolicyRegressionPass {
			resp.BlockingReasons = append(resp.BlockingReasons, "policy_regression_failed")
		}
		if !req.Evidence.ReplayConsistencyPass {
			resp.BlockingReasons = append(resp.BlockingReasons, "replay_consistency_failed")
		}
		resp.DecisionConfidence = 0.2
		relHash, ok := safeHash(resp)
		if !ok {
			resp.ReasonCodes = append(resp.ReasonCodes, "decision_hash_invalid")
			relHash = s.nextFallbackID("rel_hash_")
			applyReleaseHash(&resp, relHash, false, DecisionHashKindFallback)
		} else {
			applyReleaseHash(&resp, relHash, true, DecisionHashKindStable)
		}
		s.persistDecision("release", decisionID, req, resp, now)
		s.emitEvent(eventDecisionReleaseBlocked, decisionID, "", "", map[string]interface{}{
			"reason":                 "evidence_gate_failed",
			"risk":                   risk,
			"blocking_reasons":       append([]string(nil), resp.BlockingReasons...),
			"decision_hash":          resp.DecisionHash,
			"decision_hash_kind":     resp.DecisionHashRef.Kind,
			"decision_hash_valid":    resp.DecisionHashRef.Valid,
			"decision_hash_fallback": resp.DecisionHashIsFallback,
		}, now)
		return resp
	}

	resp.FinalDecision = DecisionPass
	resp.DecisionConfidence = 0.9
	resp.ReasonCodes = append(resp.ReasonCodes, "release_evidence_sufficient")
	relHash, ok := safeHash(resp)
	if !ok {
		resp.FinalDecision = DecisionBlock
		resp.DecisionConfidence = 0.0
		resp.BlockingReasons = append(resp.BlockingReasons, "decision_hash_generation_failed")
		resp.ReasonCodes = append(resp.ReasonCodes, "decision_hash_invalid")
		relHash = s.nextFallbackID("rel_hash_")
		applyReleaseHash(&resp, relHash, false, DecisionHashKindFallback)
	} else {
		applyReleaseHash(&resp, relHash, true, DecisionHashKindStable)
	}
	s.persistDecision("release", decisionID, req, resp, now)
	s.emitEvent("decision.release.evaluated", decisionID, "", "", map[string]interface{}{
		"final_decision":         resp.FinalDecision,
		"risk":                   risk,
		"decision_hash":          resp.DecisionHash,
		"decision_hash_kind":     resp.DecisionHashRef.Kind,
		"decision_hash_valid":    resp.DecisionHashRef.Valid,
		"decision_hash_fallback": resp.DecisionHashIsFallback,
		"evidence_completeness":  resp.EvidenceCompleteness,
		"decision_confidence":    resp.DecisionConfidence,
		"blocking_reasons":       append([]string(nil), resp.BlockingReasons...),
	}, now)
	return resp
}

func (s *Service) CreateApprovalCase(req ApprovalCreateRequest) ApprovalCase {
	now := s.cfg.Clock()
	decisionRequestedAt := time.Time{}
	s.runtimeMu.RLock()
	if d, ok := s.decisions[req.DecisionID]; ok && !d.CreatedAt.IsZero() {
		decisionRequestedAt = d.CreatedAt
	}
	s.runtimeMu.RUnlock()

	s.approvalMu.Lock()
	defer s.approvalMu.Unlock()
	risk := strings.ToLower(strings.TrimSpace(req.RiskTier))
	timeout := s.cfg.ApprovalHardTimeoutByRisk[risk]
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	// Approval case is a review-instance identifier (non-idempotent by design).
	// We use a monotonic sequence instead of wall-clock timestamps.
	caseSeq := atomic.AddUint64(&s.idSeq, 1)
	caseID := fmt.Sprintf(
		"apc_%s_%d",
		fallbackIDFromStrings(req.RunID, req.StepID, req.DecisionID, req.Mode),
		caseSeq,
	)
	c := ApprovalCase{
		CaseID:              caseID,
		RunID:               req.RunID,
		StepID:              req.StepID,
		DecisionID:          req.DecisionID,
		DecisionRequestedAt: decisionRequestedAt,
		RiskTier:            risk,
		Status:              "awaiting_approval",
		CreatedAt:           now,
		ApprovalHardTimeout: now.Add(timeout),
		Mode:                req.Mode,
	}
	s.approvals[c.CaseID] = &c
	s.emitEvent(eventApprovalCaseCreated, req.DecisionID, req.RunID, req.StepID, map[string]interface{}{
		"case_id": c.CaseID,
		"risk":    risk,
		"mode":    req.Mode,
	}, now)
	return c
}

func (s *Service) DecideApprovalCase(caseID string, req ApprovalDecisionRequest) ApprovalDecisionResponse {
	now := s.cfg.Clock()
	decision := strings.ToLower(strings.TrimSpace(req.Decision))
	var nextStatus string
	switch decision {
	case "approve":
		nextStatus = "approved"
	case "deny":
		nextStatus = "denied"
	default:
		return ApprovalDecisionResponse{CaseID: caseID, Status: "", ReasonCodes: []string{"invalid_approval_decision"}}
	}

	s.approvalMu.Lock()
	c, ok := s.approvals[caseID]
	if !ok {
		s.approvalMu.Unlock()
		return ApprovalDecisionResponse{CaseID: caseID, Status: "not_found", ReasonCodes: []string{"approval_case_not_found"}}
	}
	if !approvalCaseMutable(c.Status) {
		status := c.Status
		s.approvalMu.Unlock()
		return ApprovalDecisionResponse{CaseID: caseID, Status: status, ReasonCodes: []string{"approval_case_terminal_immutable"}}
	}
	c.Status = nextStatus
	c.ResolvedAt = now
	c.TerminalReason = "manual_decision"
	caseCopy := *c
	s.approvalMu.Unlock()

	pendingStatus, pendingReasons, unlockSignal := s.applyApprovalOutcomeToPending(caseCopy, now)
	runUnlockStatus := "not_required"
	unlockReasons := []string{}
	unblockedAt := time.Time{}
	if unlockSignal != nil {
		var unlockRes ApprovalRunUnlockDispatchResult
		runUnlockStatus, unlockReasons, unlockRes = s.dispatchApprovalRunUnlock(*unlockSignal, now)
		if unlockRes.BusinessActionUnblocked && !unlockRes.BusinessActionUnblockedAt.IsZero() {
			unblockedAt = unlockRes.BusinessActionUnblockedAt
		}
		unlockReasons = append(unlockReasons, unlockRes.ReasonCodes...)
	}
	guardReasons := []string{}
	if strings.EqualFold(caseCopy.Status, "approved") && runUnlockStatus != "dispatched" {
		pendingStatus, guardReasons = s.enforceApprovalUnlockFailureGuard(caseCopy, runUnlockStatus, now)
	}

	reasons := make([]string, 0, len(pendingReasons)+len(unlockReasons)+len(guardReasons))
	reasons = append(reasons, pendingReasons...)
	reasons = append(reasons, unlockReasons...)
	reasons = append(reasons, guardReasons...)
	effectiveLatencyMS := int64(0)
	if !unblockedAt.IsZero() {
		baseTS := approvalEffectiveLatencyBase(caseCopy)
		effectiveLatencyMS = unblockedAt.Sub(baseTS).Milliseconds()
	}
	s.emitEvent(eventApprovalCaseDecided, caseCopy.DecisionID, caseCopy.RunID, caseCopy.StepID, map[string]interface{}{
		"case_id":                       caseID,
		"status":                        caseCopy.Status,
		"pending_status":                pendingStatus,
		"run_unlock_status":             runUnlockStatus,
		"approval_effective_latency_ms": effectiveLatencyMS,
		"business_action_unblocked_ts":  unblockedAt,
	}, now)
	return ApprovalDecisionResponse{
		CaseID:                     caseID,
		Status:                     caseCopy.Status,
		PendingStatus:              pendingStatus,
		RunUnlockStatus:            runUnlockStatus,
		ApprovalEffectiveLatencyMS: effectiveLatencyMS,
		ReasonCodes:                reasons,
	}
}

func (s *Service) SweepApprovalHardTimeouts(now time.Time) int {
	type timedOutCase struct {
		caseData ApprovalCase
	}
	s.approvalMu.Lock()
	timedOut := make([]timedOutCase, 0, 8)
	changed := 0
	for _, c := range s.approvals {
		if c.Status != "awaiting_approval" {
			continue
		}
		if now.Before(c.ApprovalHardTimeout) {
			continue
		}
		switch c.RiskTier {
		case RiskLow:
			c.Status = "auto_deny"
		case RiskMedium:
			c.Status = "force_review_queue"
		default:
			c.Status = "fail_closed"
		}
		c.ResolvedAt = now
		c.TerminalReason = "hard_timeout"
		timedOut = append(timedOut, timedOutCase{caseData: *c})
		changed++
	}
	s.approvalMu.Unlock()

	for _, item := range timedOut {
		caseCopy := item.caseData
		pendingStatus, pendingReasons, unlockSignal := s.applyApprovalOutcomeToPending(caseCopy, now)
		runUnlockStatus := "not_required"
		unlockReasons := []string{}
		unblockedAt := time.Time{}
		if unlockSignal != nil {
			var unlockRes ApprovalRunUnlockDispatchResult
			runUnlockStatus, unlockReasons, unlockRes = s.dispatchApprovalRunUnlock(*unlockSignal, now)
			if unlockRes.BusinessActionUnblocked && !unlockRes.BusinessActionUnblockedAt.IsZero() {
				unblockedAt = unlockRes.BusinessActionUnblockedAt
			}
			unlockReasons = append(unlockReasons, unlockRes.ReasonCodes...)
		}
		guardReasons := []string{}
		if strings.EqualFold(caseCopy.Status, "approved") && runUnlockStatus != "dispatched" {
			pendingStatus, guardReasons = s.enforceApprovalUnlockFailureGuard(caseCopy, runUnlockStatus, now)
		}
		reasons := make([]string, 0, len(pendingReasons)+len(unlockReasons)+len(guardReasons))
		reasons = append(reasons, pendingReasons...)
		reasons = append(reasons, unlockReasons...)
		reasons = append(reasons, guardReasons...)
		effectiveLatencyMS := int64(0)
		if !unblockedAt.IsZero() {
			baseTS := approvalEffectiveLatencyBase(caseCopy)
			effectiveLatencyMS = unblockedAt.Sub(baseTS).Milliseconds()
		}
		s.emitEvent(eventApprovalHardTimeout, caseCopy.DecisionID, caseCopy.RunID, caseCopy.StepID, map[string]interface{}{
			"case_id":                       caseCopy.CaseID,
			"new_status":                    caseCopy.Status,
			"risk_tier":                     caseCopy.RiskTier,
			"pending_status":                pendingStatus,
			"run_unlock_status":             runUnlockStatus,
			"reason_codes":                  reasons,
			"approval_effective_latency_ms": effectiveLatencyMS,
			"business_action_unblocked_ts":  unblockedAt,
		}, now)
	}
	return changed
}

func (s *Service) ConfirmRunAdvance(req ConfirmRunAdvanceRequest) ConfirmRunAdvanceResponse {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	p, ok := s.pending[req.DecisionID]
	if !ok {
		return ConfirmRunAdvanceResponse{Status: "not_found", ReasonCodes: []string{"pending_decision_not_found"}}
	}
	if p.RunID != req.RunID || p.StepID != req.StepID {
		return ConfirmRunAdvanceResponse{Status: p.Status, ReasonCodes: []string{"pending_decision_mismatch"}}
	}
	confirmAttempt := req.AttemptIndex
	confirmPhase := strings.ToUpper(strings.TrimSpace(req.Phase))
	confirmOwner := strings.ToLower(strings.TrimSpace(req.OwnerKey))
	if confirmOwner == "" && (confirmAttempt > 0 || confirmPhase != "") {
		confirmOwner = buildPendingOwnerKey(req.RunID, req.StepID, confirmAttempt, confirmPhase)
	}
	if p.OwnerKey != "" {
		if confirmOwner == "" {
			return ConfirmRunAdvanceResponse{Status: p.Status, ReasonCodes: []string{"pending_owner_required"}}
		}
		if confirmOwner != p.OwnerKey {
			return ConfirmRunAdvanceResponse{Status: p.Status, ReasonCodes: []string{"pending_owner_mismatch"}}
		}
	}
	if p.AttemptIndex > 0 && confirmAttempt > 0 && p.AttemptIndex != confirmAttempt {
		return ConfirmRunAdvanceResponse{Status: p.Status, ReasonCodes: []string{"pending_attempt_mismatch"}}
	}
	if p.Phase != "" && confirmPhase != "" && p.Phase != confirmPhase {
		return ConfirmRunAdvanceResponse{Status: p.Status, ReasonCodes: []string{"pending_phase_mismatch"}}
	}
	if p.Status != pendingStatusPending {
		if req.Success && p.Status == pendingStatusConfirmed {
			return ConfirmRunAdvanceResponse{Status: p.Status}
		}
		return ConfirmRunAdvanceResponse{
			Status:      p.Status,
			ReasonCodes: []string{"pending_decision_terminal_state_immutable"},
		}
	}
	if req.Success {
		p.Status = pendingStatusConfirmed
		p.LastUpdatedAt = s.cfg.Clock()
		s.emitEvent(eventDecisionConfirmed, req.DecisionID, req.RunID, req.StepID, map[string]interface{}{}, p.LastUpdatedAt)
		return ConfirmRunAdvanceResponse{Status: p.Status}
	}
	p.LastUpdatedAt = s.cfg.Clock()
	s.emitEvent(eventPendingDecisionStillPending, req.DecisionID, req.RunID, req.StepID, map[string]interface{}{}, p.LastUpdatedAt)
	return ConfirmRunAdvanceResponse{Status: p.Status, ReasonCodes: []string{"run_advance_failed"}}
}

func approvalCaseMutable(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "awaiting_approval")
}

func approvalOutcomeToPendingStatus(outcome string) string {
	switch strings.ToLower(strings.TrimSpace(outcome)) {
	case "approved":
		return pendingStatusConfirmed
	case "force_review_queue":
		return pendingStatusSafeguardHold
	case "denied", "auto_deny", "fail_closed":
		return pendingStatusManuallyClosed
	default:
		return ""
	}
}

func approvalOutcomeRequiresRunUnlockSignal(outcome string) bool {
	switch strings.ToLower(strings.TrimSpace(outcome)) {
	case "approved", "denied", "auto_deny", "force_review_queue", "fail_closed":
		return true
	default:
		return false
	}
}

func approvalEffectiveLatencyBase(caseData ApprovalCase) time.Time {
	if !caseData.DecisionRequestedAt.IsZero() {
		return caseData.DecisionRequestedAt
	}
	return caseData.CreatedAt
}

func (s *Service) applyApprovalOutcomeToPending(caseData ApprovalCase, now time.Time) (pendingStatus string, reasonCodes []string, signal *ApprovalRunUnlockSignal) {
	targetStatus := approvalOutcomeToPendingStatus(caseData.Status)
	if targetStatus == "" {
		return "", []string{"approval_outcome_not_mapped"}, nil
	}

	var outSignal *ApprovalRunUnlockSignal
	s.runtimeMu.Lock()
	p, ok := s.pending[caseData.DecisionID]
	if !ok {
		s.runtimeMu.Unlock()
		return "", []string{"pending_decision_not_found"}, nil
	}
	if p.RunID != caseData.RunID || p.StepID != caseData.StepID {
		s.runtimeMu.Unlock()
		return p.Status, []string{"pending_decision_mismatch"}, nil
	}
	// Only pending decisions may be transitioned by approval outcomes.
	if p.Status != pendingStatusPending {
		status := p.Status
		s.runtimeMu.Unlock()
		return status, []string{"pending_decision_terminal_state_immutable"}, nil
	}

	p.Status = targetStatus
	p.LastUpdatedAt = now
	status := p.Status
	if status == pendingStatusConfirmed {
		s.emitEvent(eventDecisionConfirmed, caseData.DecisionID, caseData.RunID, caseData.StepID, map[string]interface{}{
			"approval_case_id": caseData.CaseID,
			"approval_outcome": caseData.Status,
		}, now)
	} else {
		s.emitEvent(eventApprovalPendingUpdated, caseData.DecisionID, caseData.RunID, caseData.StepID, map[string]interface{}{
			"approval_case_id": caseData.CaseID,
			"approval_outcome": caseData.Status,
			"pending_status":   status,
		}, now)
	}
	if approvalOutcomeRequiresRunUnlockSignal(caseData.Status) {
		outSignal = &ApprovalRunUnlockSignal{
			CaseID:        caseData.CaseID,
			RunID:         caseData.RunID,
			StepID:        caseData.StepID,
			DecisionID:    caseData.DecisionID,
			RiskTier:      caseData.RiskTier,
			Outcome:       caseData.Status,
			PendingStatus: status,
			AttemptIndex:  p.AttemptIndex,
			Phase:         p.Phase,
			OwnerKey:      p.OwnerKey,
			RequestedAt:   caseData.CreatedAt,
			ResolvedAt:    caseData.ResolvedAt,
		}
	}
	s.runtimeMu.Unlock()
	return status, nil, outSignal
}

func (s *Service) dispatchApprovalRunUnlock(signal ApprovalRunUnlockSignal, now time.Time) (string, []string, ApprovalRunUnlockDispatchResult) {
	if s.approvalRunUnlockPort == nil {
		s.emitEvent(eventApprovalRunUnlockDispatch, signal.DecisionID, signal.RunID, signal.StepID, map[string]interface{}{
			"case_id":         signal.CaseID,
			"outcome":         signal.Outcome,
			"pending_status":  signal.PendingStatus,
			"dispatch_status": "not_configured",
		}, now)
		return "not_configured", []string{"approval_run_unlock_not_configured"}, ApprovalRunUnlockDispatchResult{
			DispatchStatus: "not_configured",
		}
	}
	res, err := s.approvalRunUnlockPort.DispatchApprovalRunUnlock(signal)
	if err != nil {
		s.emitEvent(eventApprovalRunUnlockDispatch, signal.DecisionID, signal.RunID, signal.StepID, map[string]interface{}{
			"case_id":         signal.CaseID,
			"outcome":         signal.Outcome,
			"pending_status":  signal.PendingStatus,
			"dispatch_status": "dispatch_failed",
			"error":           err.Error(),
		}, now)
		return "dispatch_failed", []string{"approval_run_unlock_dispatch_failed"}, ApprovalRunUnlockDispatchResult{
			DispatchStatus: "dispatch_failed",
		}
	}
	if strings.TrimSpace(res.DispatchStatus) == "" {
		res.DispatchStatus = "dispatched"
	}
	s.emitEvent(eventApprovalRunUnlockDispatch, signal.DecisionID, signal.RunID, signal.StepID, map[string]interface{}{
		"case_id":         signal.CaseID,
		"outcome":         signal.Outcome,
		"pending_status":  signal.PendingStatus,
		"dispatch_status": res.DispatchStatus,
		"unblocked":       res.BusinessActionUnblocked,
		"unblocked_at":    res.BusinessActionUnblockedAt,
		"reason_codes":    res.ReasonCodes,
	}, now)
	return res.DispatchStatus, nil, res
}

func (s *Service) enforceApprovalUnlockFailureGuard(caseData ApprovalCase, runUnlockStatus string, now time.Time) (string, []string) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	p, ok := s.pending[caseData.DecisionID]
	if !ok {
		return "", []string{"approval_unlock_failed_pending_not_found"}
	}
	if p.RunID != caseData.RunID || p.StepID != caseData.StepID {
		return p.Status, []string{"approval_unlock_failed_pending_mismatch"}
	}
	if p.Status != pendingStatusConfirmed {
		return p.Status, []string{"approval_unlock_failed_pending_not_confirmed"}
	}
	p.Status = pendingStatusSafeguardHold
	p.LastUpdatedAt = now
	s.emitEvent(eventApprovalPendingUpdated, caseData.DecisionID, caseData.RunID, caseData.StepID, map[string]interface{}{
		"approval_case_id":  caseData.CaseID,
		"approval_outcome":  caseData.Status,
		"pending_status":    p.Status,
		"reason":            "approval_run_unlock_dispatch_failed",
		"run_unlock_status": runUnlockStatus,
	}, now)
	return p.Status, []string{"approval_unlock_failed_enter_safeguard_hold"}
}

// CanExecuteSideEffect is a decision-plane advisory check only.
// Use CanExecuteSideEffectFinal for execution-time final gating.
func (s *Service) CanExecuteSideEffect(decisionID string) bool {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	p, ok := s.pending[decisionID]
	if !ok {
		return false
	}
	if p.Status != pendingStatusConfirmed {
		return false
	}
	d, ok := s.decisions[decisionID]
	if !ok {
		return false
	}
	ref := d.DecisionHashRef
	ref.Value = d.DecisionHash
	if d.DecisionHashIsFallback {
		ref.Kind = DecisionHashKindFallback
		ref.Valid = false
	}
	if !ref.Valid || ref.Kind != DecisionHashKindStable || strings.TrimSpace(ref.Value) == "" || isErrorHash(ref.Value) {
		return false
	}
	decisionValue := strings.ToLower(strings.TrimSpace(extractStringFieldFromMap(d.Response, "decision")))
	switch decisionValue {
	case DecisionAllow, DecisionRequireApproval:
		return true
	default:
		return false
	}
}

// CanExecuteSideEffectFinal is the strict final gate used by execution paths.
// It combines decision-plane confirmation with run-state and receipt/ticket checks.
func (s *Service) CanExecuteSideEffectFinal(req SideEffectGateRequest) (bool, []string) {
	if !s.CanExecuteSideEffect(req.DecisionID) {
		return false, []string{"decision_not_confirmed_or_not_executable"}
	}

	s.runtimeMu.RLock()
	runPort := s.runStatePort
	s.runtimeMu.RUnlock()
	if runPort == nil {
		return false, []string{"run_state_port_not_configured"}
	}
	ref, ok := runPort.GetRunState(req.RunID)
	if !ok {
		return false, []string{"run_not_found"}
	}
	runState := strings.ToUpper(strings.TrimSpace(ref.State))
	if runState != "RUNNING" {
		return false, []string{"run_state_not_running"}
	}
	ok, reasons := s.ValidateExecutionReceiptForRun(
		req.RunID,
		req.StepID,
		req.TenantID,
		req.ExecutionReceiptRef,
		req.ExecutionUsedResources,
		req.ExecutionReceiptAt,
	)
	if !ok {
		return false, reasons
	}
	return true, nil
}

func (s *Service) TerminalizePendingDecision(req TerminalizePendingDecisionRequest) TerminalizePendingDecisionResponse {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()

	p, ok := s.pending[req.DecisionID]
	if !ok {
		return TerminalizePendingDecisionResponse{Status: "not_found", ReasonCodes: []string{"pending_decision_not_found"}}
	}
	targetStatus := strings.ToLower(strings.TrimSpace(req.Status))
	switch targetStatus {
	case pendingStatusSafeguardHold, pendingStatusEscalatedOncall, pendingStatusManuallyClosed:
	default:
		return TerminalizePendingDecisionResponse{Status: p.Status, ReasonCodes: []string{"invalid_terminal_status"}}
	}
	if p.Status == targetStatus {
		return TerminalizePendingDecisionResponse{Status: p.Status}
	}
	if p.Status != pendingStatusPending {
		return TerminalizePendingDecisionResponse{Status: p.Status, ReasonCodes: []string{"pending_decision_already_terminal"}}
	}

	now := s.cfg.Clock()
	p.Status = targetStatus
	p.LastUpdatedAt = now
	s.emitEvent(eventPendingDecisionTerminalized, p.DecisionID, p.RunID, p.StepID, map[string]interface{}{
		"target_status": targetStatus,
		"reason":        req.Reason,
		"actor":         req.Actor,
	}, now)
	return TerminalizePendingDecisionResponse{Status: p.Status}
}

func (s *Service) RepairPendingDecisions(now time.Time) PendingRepairResult {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()

	result := PendingRepairResult{
		ReasonCodes: make([]string, 0, 2),
	}

	for _, p := range s.pending {
		if p.Status != pendingStatusPending {
			continue
		}
		idleAge := now.Sub(p.LastUpdatedAt)
		if idleAge <= s.cfg.PendingDecisionTTL {
			continue
		}
		result.Scanned++
		p.RepairAttempts++
		p.LastUpdatedAt = now

		if p.RepairAttempts == 1 {
			result.Repaired++
			s.emitEvent(eventPendingDecisionRetryConfirm, p.DecisionID, p.RunID, p.StepID, map[string]interface{}{
				"repair_attempts": p.RepairAttempts,
				"worker_id":       s.cfg.PendingRepairWorkerID,
			}, now)
			continue
		}
		if p.RepairAttempts == 2 {
			result.Repaired++
			s.emitEvent(eventPendingDecisionCompareRunState, p.DecisionID, p.RunID, p.StepID, map[string]interface{}{
				"repair_attempts": p.RepairAttempts,
				"worker_id":       s.cfg.PendingRepairWorkerID,
			}, now)
			continue
		}
		if p.RepairAttempts == 3 {
			p.Status = pendingStatusSafeguardHold
			result.SafeguardHold++
			s.emitEvent(eventPendingDecisionSafeguardHold, p.DecisionID, p.RunID, p.StepID, map[string]interface{}{
				"repair_attempts": p.RepairAttempts,
				"worker_id":       s.cfg.PendingRepairWorkerID,
			}, now)
			continue
		}
		p.Status = pendingStatusEscalatedOncall
		result.Escalated++
		s.emitEvent(eventPendingDecisionEscalateOncall, p.DecisionID, p.RunID, p.StepID, map[string]interface{}{
			"repair_attempts": p.RepairAttempts,
			"worker_id":       s.cfg.PendingRepairWorkerID,
		}, now)
	}

	return result
}

func (s *Service) GetDecision(decisionID string) (StoredDecision, bool) {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	d, ok := s.decisions[decisionID]
	return d, ok
}

func (s *Service) GetDecisionHistory(decisionID string) ([]StoredDecision, bool) {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	h, ok := s.decisionHistory[decisionID]
	if !ok || len(h) == 0 {
		return nil, false
	}
	cp := make([]StoredDecision, len(h))
	copy(cp, h)
	return cp, true
}

func (s *Service) GetDecisionVersion(decisionID string) (int, bool) {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	d, ok := s.decisions[decisionID]
	if !ok {
		return 0, false
	}
	return d.Version, true
}

func (s *Service) GetDecisionByVersion(decisionID string, version int) (StoredDecision, bool) {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	h, ok := s.decisionHistory[decisionID]
	if !ok || len(h) == 0 {
		return StoredDecision{}, false
	}
	for _, d := range h {
		if d.Version == version {
			return d, true
		}
	}
	return StoredDecision{}, false
}

func (s *Service) IsDecisionConfirmed(decisionID, runID, stepID string) bool {
	return s.IsDecisionConfirmedWithOwner(decisionID, runID, stepID, 0, "")
}

func (s *Service) IsDecisionConfirmedWithOwner(decisionID, runID, stepID string, attemptIndex int, phase string) bool {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	p, ok := s.pending[decisionID]
	if !ok {
		return false
	}
	if p.RunID != runID || p.StepID != stepID {
		return false
	}
	if p.Status != pendingStatusConfirmed {
		return false
	}
	normalizedPhase := strings.ToUpper(strings.TrimSpace(phase))
	if p.AttemptIndex > 0 && attemptIndex > 0 && p.AttemptIndex != attemptIndex {
		return false
	}
	if p.Phase != "" && normalizedPhase != "" && p.Phase != normalizedPhase {
		return false
	}
	if p.OwnerKey != "" {
		if attemptIndex <= 0 && normalizedPhase == "" {
			return false
		}
		if buildPendingOwnerKey(runID, stepID, attemptIndex, normalizedPhase) != p.OwnerKey {
			return false
		}
	}
	return true
}

func (s *Service) GetDecisionHash(decisionID string) (string, bool) {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	d, ok := s.decisions[decisionID]
	if !ok {
		return "", false
	}
	ref := d.DecisionHashRef
	ref.Value = d.DecisionHash
	if d.DecisionHashIsFallback {
		ref.Kind = DecisionHashKindFallback
		ref.Valid = false
	}
	if strings.TrimSpace(ref.Kind) == "" {
		if d.DecisionHashIsFallback {
			ref.Kind = DecisionHashKindFallback
			ref.Valid = false
		} else if strings.TrimSpace(ref.Value) != "" && !isErrorHash(ref.Value) {
			ref.Kind = DecisionHashKindStable
			ref.Valid = true
		} else {
			ref.Kind = DecisionHashKindError
			ref.Valid = false
		}
	}
	if !ref.Valid || ref.Kind != DecisionHashKindStable {
		return "", false
	}
	if strings.TrimSpace(ref.Value) == "" || isErrorHash(ref.Value) {
		return "", false
	}
	return ref.Value, true
}

func (s *Service) GetDecisionReference(decisionID string) (DecisionReference, bool) {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	d, ok := s.decisions[decisionID]
	if !ok {
		return DecisionReference{}, false
	}
	ref := d.DecisionHashRef
	ref.Value = d.DecisionHash
	if d.DecisionHashIsFallback {
		ref.Kind = DecisionHashKindFallback
		ref.Valid = false
	}
	if strings.TrimSpace(ref.Kind) == "" {
		if d.DecisionHashIsFallback {
			ref.Kind = DecisionHashKindFallback
			ref.Valid = false
		} else if strings.TrimSpace(ref.Value) != "" && !isErrorHash(ref.Value) {
			ref.Kind = DecisionHashKindStable
			ref.Valid = true
		} else {
			ref.Kind = DecisionHashKindError
			ref.Valid = false
		}
	}
	decisionValue := extractStringFieldFromMap(d.Response, "decision")
	if decisionValue == "" {
		decisionValue = extractStringFieldFromMap(d.Response, "final_decision")
	}
	obligations := extractObligationsFromMap(d.Response)
	return DecisionReference{
		DecisionID:      d.DecisionID,
		RunID:           d.RunID,
		StepID:          d.StepID,
		AttemptIndex:    d.AttemptIndex,
		Phase:           d.Phase,
		Decision:        decisionValue,
		Obligations:     obligations,
		DecisionHashRef: ref,
	}, true
}

func (s *Service) Outbox() []Event {
	s.outboxMu.RLock()
	defer s.outboxMu.RUnlock()
	cp := make([]Event, len(s.outbox))
	copy(cp, s.outbox)
	return cp
}

func (s *Service) DrainOutbox(limit int) []Event {
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	if limit <= 0 || len(s.outbox) == 0 {
		return nil
	}
	if limit > len(s.outbox) {
		limit = len(s.outbox)
	}
	out := make([]Event, limit)
	copy(out, s.outbox[:limit])
	s.outbox = s.outbox[limit:]
	if len(s.outbox) == 0 {
		s.outbox = s.outbox[:0]
	}
	return out
}

func (s *Service) CleanupExpired(now time.Time) CleanupResult {
	result := CleanupResult{}
	cleanupNow := s.resolveCleanupNow(now)

	// Phase 1: runtime-domain state
	s.runtimeMu.Lock()
	for id, p := range s.pending {
		if p.Status == pendingStatusPending {
			continue
		}
		switch p.Status {
		case pendingStatusConfirmed, pendingStatusSafeguardHold, pendingStatusEscalatedOncall, pendingStatusManuallyClosed:
			if cleanupNow.After(p.LastUpdatedAt.Add(s.cfg.PendingRetention)) {
				delete(s.pending, id)
				result.DeletedPending++
			}
		}
	}
	for key, st := range s.softFailures {
		if st.LastUpdatedAt.IsZero() {
			delete(s.softFailures, key)
			result.DeletedSoftFails++
			continue
		}
		if cleanupNow.After(st.LastUpdatedAt.Add(s.cfg.PendingRetention)) {
			delete(s.softFailures, key)
			result.DeletedSoftFails++
		}
	}
	for id, d := range s.decisions {
		if cleanupNow.After(d.CreatedAt.Add(s.cfg.DecisionRetention)) {
			delete(s.decisions, id)
			delete(s.decisionHistory, id)
			result.DeletedDecisions++
		}
	}
	s.runtimeMu.Unlock()

	// Phase 2: ticket-domain state
	s.ticketMu.Lock()
	for id, t := range s.tickets {
		if cleanupNow.After(t.ExpiresAt.Add(s.cfg.TicketRetention)) {
			delete(s.tickets, id)
			result.DeletedTickets++
		}
	}
	s.ticketMu.Unlock()

	// Phase 3: approval-domain state
	s.approvalMu.Lock()
	for id, a := range s.approvals {
		if a.Status == "awaiting_approval" {
			continue
		}
		if cleanupNow.After(a.ApprovalHardTimeout.Add(s.cfg.ApprovalRetention)) {
			delete(s.approvals, id)
			result.DeletedApprovals++
		}
	}
	s.approvalMu.Unlock()

	// Phase 4: outbox compaction
	s.outboxMu.Lock()
	result.OutboxTrimmed = s.trimOutboxLocked(cleanupNow)
	s.outboxMu.Unlock()

	return result
}

func (s *Service) resolveCleanupNow(now time.Time) time.Time {
	if strings.ToLower(strings.TrimSpace(s.cfg.CleanupTimeSource)) != cleanupTimeSourceEventTime {
		return now
	}
	s.outboxMu.RLock()
	defer s.outboxMu.RUnlock()
	if s.maxObservedEventTS.IsZero() {
		return now
	}
	// In event-time mode, cleanup is anchored to monotonic observed event time,
	// independent from outbox drain/retention state.
	return s.maxObservedEventTS
}

func (s *Service) trimOutboxLocked(now time.Time) int {
	trimmed := 0
	if s.cfg.OutboxRetention > 0 {
		cut := 0
		for cut < len(s.outbox) {
			if now.Sub(s.outbox[cut].EventTS) <= s.cfg.OutboxRetention {
				break
			}
			cut++
		}
		if cut > 0 {
			s.outbox = s.outbox[cut:]
			trimmed += cut
		}
	}
	if overflow := len(s.outbox) - s.cfg.OutboxMaxEvents; overflow > 0 {
		s.outbox = s.outbox[overflow:]
		trimmed += overflow
	}
	if trimmed > 0 {
		atomic.AddUint64(&s.outboxTrimmedTotal, uint64(trimmed))
	}
	return trimmed
}

func (s *Service) MetricsSnapshot(now time.Time) MetricsSnapshot {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()

	rates := make(map[string]float64)
	ratios := make(map[string]float64)
	quantiles := make(map[string]float64)
	levels := make(map[string]string)
	counters := make(map[string]float64)
	for k, v := range s.counters {
		counters[k] = v
	}
	counters["outbox_trimmed_total"] = float64(atomic.LoadUint64(&s.outboxTrimmedTotal))

	totalRuntime := s.counters["runtime_eval_total"]
	if totalRuntime > 0 {
		rates["decision_dcu_exceeded_rate"] = (s.counters["decision_dcu_exceeded_total"] / totalRuntime) * 100
		rates["feature_stale_rate"] = (s.counters["feature_stale_total"] / totalRuntime) * 100
	}
	if totalFreeze := s.counters["freeze_whitelist_checks_total"]; totalFreeze > 0 {
		rates["freeze_whitelist_coverage_rate"] = (s.counters["freeze_whitelist_pass_total"] / totalFreeze) * 100
	}

	totalAgeP95Sec, totalAgeP95Ratio, idleAgeP95Sec, idleAgeP95Ratio := s.pendingDecisionAgeQuantiles(now)
	ratios["pending_decision_total_age_p95_ratio"] = totalAgeP95Ratio
	ratios["pending_decision_idle_age_p95_ratio"] = idleAgeP95Ratio
	// Backward compatibility alias used by existing gate configs; now maps to idle-age ratio.
	ratios["pending_decision_age_p95"] = idleAgeP95Ratio
	quantiles["pending_decision_total_age_p95_seconds"] = totalAgeP95Sec
	quantiles["pending_decision_idle_age_p95_seconds"] = idleAgeP95Sec

	for metric, threshold := range s.cfg.MetricMatrix {
		value, ok := rates[metric]
		if !ok {
			value, ok = ratios[metric]
		}
		if !ok {
			value, ok = quantiles[metric]
		}
		levels[metric] = evaluateEnforcement(metric, value, ok, threshold)
	}

	return MetricsSnapshot{
		Counters:          counters,
		Rates:             rates,
		Ratios:            ratios,
		Quantiles:         quantiles,
		EnforcementLevels: levels,
	}
}

func (s *Service) applySoftFailureGuard(req RuntimeDecisionRequest, resp RuntimeDecisionResponse, now time.Time) RuntimeDecisionResponse {
	stepKey := fmt.Sprintf("%s:%s:%s", req.TenantID, req.RunID, req.StepID)
	soft := resp.Decision == DecisionReviewRequired

	if !soft {
		resp.SoftFailureStage = SoftFailureStageNone
		delete(s.softFailures, stepKey)
		return resp
	}

	state := s.softFailures[stepKey]
	currentReason := firstOrEmpty(resp.ReasonCodes)
	if state.FirstReason == "" {
		state.FirstReason = currentReason
	}
	state.LastReason = currentReason

	// Prefer explicit attempt index to avoid conflating replay/evaluation count with execution retries.
	if req.AttemptIndex > 0 {
		evidenceChanged := req.EvidenceFingerprint != "" && req.EvidenceFingerprint != state.LastEvidence
		if evidenceChanged {
			state = softFailureState{
				RetryCount:       0,
				LastEvidence:     req.EvidenceFingerprint,
				FirstReason:      currentReason,
				LastReason:       currentReason,
				LastAttemptIndex: req.AttemptIndex,
				LastUpdatedAt:    now,
			}
			resp.SoftFailureStage = SoftFailureStageReset
			s.softFailures[stepKey] = state
			return resp
		}
		if req.AttemptIndex <= state.LastAttemptIndex {
			state.LastUpdatedAt = now
			resp.SoftFailureStage = SoftFailureStageRetrying
			s.softFailures[stepKey] = state
			return resp
		}
		state.LastAttemptIndex = req.AttemptIndex
		retriesUsed := req.AttemptIndex - 1
		if retriesUsed < 0 {
			retriesUsed = 0
		}
		state.RetryCount = retriesUsed
		if req.EvidenceFingerprint != "" && state.LastEvidence == "" {
			state.LastEvidence = req.EvidenceFingerprint
		}
	} else {
		lastEvidence := state.LastEvidence
		changed := req.EvidenceFingerprint != "" && req.EvidenceFingerprint != lastEvidence
		if changed {
			state.RetryCount = 0
			state.LastEvidence = req.EvidenceFingerprint
			state.LastUpdatedAt = now
			resp.SoftFailureStage = SoftFailureStageReset
			s.softFailures[stepKey] = state
			return resp
		}
		state.RetryCount++
		if req.EvidenceFingerprint != "" {
			state.LastEvidence = req.EvidenceFingerprint
		}
	}
	state.LastUpdatedAt = now
	s.softFailures[stepKey] = state

	if state.RetryCount <= s.cfg.DecisionRetryLimitPerStep {
		resp.SoftFailureStage = SoftFailureStageRetrying
		return resp
	}

	resp.Audit.ErrorCodes = append(resp.Audit.ErrorCodes, ErrCodeSoftRetryExhausted)
	resp.ReasonCodes = append(resp.ReasonCodes, "decision_retry_exhausted")
	if isHighOrCritical(req.RiskTier) {
		resp.Decision = DecisionForceReview
		resp.SoftFailureStage = SoftFailureStageEscalated
	} else {
		resp.Decision = DecisionFailed
		resp.SoftFailureStage = SoftFailureStageExhausted
	}
	s.emitEvent(eventDecisionRetryExhausted, resp.DecisionID, req.RunID, req.StepID, map[string]interface{}{
		"limit":                s.cfg.DecisionRetryLimitPerStep,
		"retry_count":          state.RetryCount,
		"attempt_index":        req.AttemptIndex,
		"soft_failure_stage":   resp.SoftFailureStage,
		"first_failure_reason": state.FirstReason,
		"last_failure_reason":  state.LastReason,
	}, now)
	return resp
}

func buildPendingOwnerKey(runID, stepID string, attempt int, phase string) string {
	return strings.ToLower(strings.TrimSpace(fmt.Sprintf("%s|%s|%d|%s", runID, stepID, attempt, strings.ToUpper(strings.TrimSpace(phase)))))
}

func (s *Service) createPendingDecision(decisionID, runID, stepID, riskTier string, now time.Time, ownerMeta ...pendingOwnerMeta) {
	meta := pendingOwnerMeta{}
	if len(ownerMeta) > 0 {
		meta = ownerMeta[0]
	}
	ownerKey := buildPendingOwnerKey(runID, stepID, meta.AttemptIndex, meta.Phase)
	if existing, ok := s.pending[decisionID]; ok {
		prevStatus := existing.Status
		if existing.OwnerKey != "" && ownerKey != "" && existing.OwnerKey != ownerKey {
			s.emitEvent(eventPendingOwnerMismatch, decisionID, runID, stepID, map[string]interface{}{
				"existing_owner_key": existing.OwnerKey,
				"incoming_owner_key": ownerKey,
				"existing_status":    prevStatus,
			}, now)
			return
		}
		switch existing.Status {
		case pendingStatusPending:
			// Preserve lifecycle fields; only refresh mutable runtime hints.
			existing.LastUpdatedAt = now
			if existing.RunID == "" {
				existing.RunID = runID
			}
			if existing.StepID == "" {
				existing.StepID = stepID
			}
			if existing.RiskTier == "" {
				existing.RiskTier = riskTier
			}
			if existing.AttemptIndex == 0 {
				existing.AttemptIndex = meta.AttemptIndex
			}
			if existing.Phase == "" {
				existing.Phase = strings.ToUpper(strings.TrimSpace(meta.Phase))
			}
			if existing.OwnerKey == "" {
				existing.OwnerKey = ownerKey
			}
		default:
			// Confirmed/terminal statuses are immutable from duplicate evaluate calls.
		}
		s.emitEvent(eventPendingDuplicateEvaluate, decisionID, existing.RunID, existing.StepID, map[string]interface{}{
			"existing_status": prevStatus,
		}, now)
		return
	}
	s.pending[decisionID] = &PendingDecision{
		DecisionID:     decisionID,
		RunID:          runID,
		StepID:         stepID,
		RiskTier:       riskTier,
		AttemptIndex:   meta.AttemptIndex,
		Phase:          strings.ToUpper(strings.TrimSpace(meta.Phase)),
		OwnerKey:       ownerKey,
		CreatedAt:      now,
		LastUpdatedAt:  now,
		RepairAttempts: 0,
		Status:         pendingStatusPending,
	}
}

func (s *Service) recordDecisionLocked(kind, decisionID string, req interface{}, resp interface{}, now time.Time) {
	reqRaw, reqMap := toCanonicalRecord(req)
	respRaw, respMap := toCanonicalRecord(resp)
	decisionHash := extractDecisionHash(resp)
	decisionHashIsFallback := extractDecisionHashIsFallback(resp)
	decisionHashRef := extractDecisionHashRef(resp)
	decisionKey := buildDecisionKey(kind, req)
	runID, stepID, attemptIndex, phase := extractDecisionBinding(req)
	semanticReqRaw := canonicalizeForDecisionSemantic(reqMap)
	semanticRespRaw := canonicalizeForDecisionSemantic(respMap)
	next := StoredDecision{
		DecisionID:             decisionID,
		DecisionType:           kind,
		DecisionKey:            decisionKey,
		DecisionHash:           decisionHash,
		DecisionHashIsFallback: decisionHashIsFallback,
		DecisionHashRef:        decisionHashRef,
		RunID:                  runID,
		StepID:                 stepID,
		AttemptIndex:           attemptIndex,
		Phase:                  phase,
		CreatedAt:              now,
		Request:                reqMap,
		Response:               respMap,
		RequestRaw:             reqRaw,
		ResponseRaw:            respRaw,
	}
	if existing, ok := s.decisions[decisionID]; ok {
		existingSemanticReq := canonicalizeForDecisionSemantic(existing.Request)
		existingSemanticResp := canonicalizeForDecisionSemantic(existing.Response)
		if existing.DecisionType == next.DecisionType &&
			existing.DecisionKey == next.DecisionKey &&
			existingSemanticReq == semanticReqRaw &&
			existingSemanticResp == semanticRespRaw {
			next.Version = existing.Version
			history := s.decisionHistory[decisionID]
			if len(history) == 0 {
				history = []StoredDecision{next}
			} else {
				history[len(history)-1] = next
			}
			history = s.capDecisionHistory(history)
			s.decisionHistory[decisionID] = history
			s.decisions[decisionID] = next
			return
		}
		next.Version = existing.Version + 1
		history := s.decisionHistory[decisionID]
		if len(history) == 0 {
			history = append(history, existing)
		}
		history = append(history, next)
		history = s.capDecisionHistory(history)
		s.decisionHistory[decisionID] = history
		s.decisions[decisionID] = next
		return
	}
	next.Version = 1
	s.decisions[decisionID] = next
	s.decisionHistory[decisionID] = s.capDecisionHistory([]StoredDecision{next})
}

func (s *Service) capDecisionHistory(history []StoredDecision) []StoredDecision {
	limit := s.cfg.DecisionMaxHistoryVersions
	if limit <= 0 || len(history) <= limit {
		return history
	}
	trimmed := make([]StoredDecision, limit)
	copy(trimmed, history[len(history)-limit:])
	return trimmed
}

func (s *Service) persistDecision(kind, decisionID string, req interface{}, resp interface{}, now time.Time) {
	s.runtimeMu.Lock()
	s.recordDecisionLocked(kind, decisionID, req, resp, now)
	s.runtimeMu.Unlock()
}

func (s *Service) emitEvent(eventType, decisionID, runID, stepID string, payload map[string]interface{}, at time.Time) {
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	seq := atomic.AddUint64(&s.eventSeq, 1)
	payloadHash, payloadHashValid := safeHash(payload)
	evidenceGrade, payloadIntegrityRequired := decisionEventEvidencePolicy(eventType)

	e := Event{
		EventID:                  fmt.Sprintf("evt_dec_seq_%d", seq),
		EventType:                eventType,
		SchemaVersion:            1,
		SourceComponent:          "decision_kernel",
		EvidenceGrade:            evidenceGrade,
		PayloadIntegrityRequired: payloadIntegrityRequired,
		DecisionID:               decisionID,
		RunID:                    runID,
		StepID:                   stepID,
		EventTS:                  at,
		PayloadRef:               fmt.Sprintf("inline://decision_outbox/%d", seq),
		PayloadHash:              payloadHash,
		PayloadHashValid:         payloadHashValid,
		Payload:                  payload,
	}
	s.outbox = append(s.outbox, e)
	if at.After(s.maxObservedEventTS) {
		s.maxObservedEventTS = at
	}
	s.trimOutboxLocked(at)
}

func decisionEventEvidencePolicy(eventType string) (grade string, payloadIntegrityRequired bool) {
	switch strings.TrimSpace(eventType) {
	case eventDecisionDCUExceeded,
		eventDecisionRetryExhausted,
		eventDecisionConfirmed,
		eventApprovalHardTimeout,
		eventApprovalCaseDecided,
		eventApprovalPendingUpdated,
		eventApprovalRunUnlockDispatch,
		eventPendingDecisionEscalateOncall,
		eventPendingOwnerMismatch:
		return "audit", true
	default:
		return "operational", false
	}
}

func (s *Service) pendingDecisionAgeQuantiles(now time.Time) (totalAgeP95Sec, totalAgeP95Ratio, idleAgeP95Sec, idleAgeP95Ratio float64) {
	if len(s.pending) == 0 {
		return 0, 0, 0, 0
	}
	totalAges := make([]float64, 0, len(s.pending))
	idleAges := make([]float64, 0, len(s.pending))
	ttl := s.cfg.PendingDecisionTTL.Seconds()
	for _, p := range s.pending {
		if p.Status == pendingStatusConfirmed {
			continue
		}
		totalAges = append(totalAges, now.Sub(p.CreatedAt).Seconds())
		idleAges = append(idleAges, now.Sub(p.LastUpdatedAt).Seconds())
	}
	if len(totalAges) == 0 || ttl <= 0 {
		return 0, 0, 0, 0
	}
	sort.Float64s(totalAges)
	sort.Float64s(idleAges)
	idx := int(0.95 * float64(len(totalAges)-1))
	totalAgeP95Sec = totalAges[idx]
	idleAgeP95Sec = idleAges[idx]
	totalAgeP95Ratio = totalAgeP95Sec / ttl
	idleAgeP95Ratio = idleAgeP95Sec / ttl
	return totalAgeP95Sec, totalAgeP95Ratio, idleAgeP95Sec, idleAgeP95Ratio
}

func (s *Service) pendingDecisionAgeP95Ratio(now time.Time) float64 {
	_, _, _, idleRatio := s.pendingDecisionAgeQuantiles(now)
	return idleRatio
}

func makeDecisionHashRef(value string, valid bool, kind string) DecisionHashRef {
	if strings.TrimSpace(kind) == "" {
		if valid {
			kind = DecisionHashKindStable
		} else {
			kind = DecisionHashKindError
		}
	}
	return DecisionHashRef{
		Value: value,
		Valid: valid,
		Kind:  kind,
	}
}

func applyScheduleHash(resp *ScheduleAdmissionResponse, value string, valid bool, kind string) {
	if resp == nil {
		return
	}
	resp.DecisionHash = value
	resp.DecisionHashIsFallback = !valid
	resp.DecisionHashRef = makeDecisionHashRef(value, valid, kind)
	if resp.Ticket != nil {
		resp.Ticket.DecisionHash = value
		resp.Ticket.DecisionHashKind = resp.DecisionHashRef.Kind
	}
}

func applyReleaseHash(resp *ReleaseDecisionResponse, value string, valid bool, kind string) {
	if resp == nil {
		return
	}
	resp.DecisionHash = value
	resp.DecisionHashIsFallback = !valid
	resp.DecisionHashRef = makeDecisionHashRef(value, valid, kind)
}

func extractTicketIDFromExecutionReceiptRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	lower := strings.ToLower(ref)
	switch {
	case strings.HasPrefix(lower, "ticket://"):
		return strings.TrimSpace(ref[len("ticket://"):])
	case strings.HasPrefix(lower, "ticket:"):
		return strings.TrimSpace(ref[len("ticket:"):])
	case strings.HasPrefix(lower, "rcpt://"):
		return strings.TrimSpace(ref[len("rcpt://"):])
	}
	if idx := strings.Index(lower, "ticket_id="); idx >= 0 {
		part := ref[idx+len("ticket_id="):]
		if cut := strings.IndexAny(part, "&?#"); cut >= 0 {
			part = part[:cut]
		}
		return strings.TrimSpace(part)
	}
	if !strings.ContainsAny(ref, "/?#") {
		return ref
	}
	return ""
}

func normalizeRuntimeRequest(req RuntimeDecisionRequest) RuntimeDecisionRequest {
	n := req
	n.RiskTier = strings.ToLower(strings.TrimSpace(req.RiskTier))
	n.EffectType = strings.ToLower(strings.TrimSpace(req.EffectType))
	n.Phase = strings.ToUpper(strings.TrimSpace(req.Phase))
	if n.Phase == "" {
		n.Phase = PhasePreTool
	}
	n.Freeze.DynamicUsed = append([]string(nil), req.Freeze.DynamicUsed...)
	sort.Strings(n.Freeze.DynamicUsed)
	n.ParentDecisionNodeIDs = append([]string(nil), req.ParentDecisionNodeIDs...)
	sort.Strings(n.ParentDecisionNodeIDs)
	if n.AttemptIndex < 0 {
		n.AttemptIndex = 0
	}
	n.FeatureVersion = strings.TrimSpace(req.FeatureVersion)
	n.FeatureSchemaVersion = strings.TrimSpace(req.FeatureSchemaVersion)
	n.FeatureEvidenceRef = strings.TrimSpace(req.FeatureEvidenceRef)
	n.FeatureProducerID = strings.TrimSpace(req.FeatureProducerID)
	n.FeatureContractScope = normalizeContractScope(req.FeatureContractScope)
	n.FeatureContractHashPin = strings.TrimSpace(req.FeatureContractHashPin)
	n.DecisionGraphID = strings.TrimSpace(req.DecisionGraphID)
	n.TraceID = strings.TrimSpace(req.TraceID)
	return n
}

func defaultTraceID(req RuntimeDecisionRequest) string {
	if strings.TrimSpace(req.TraceID) != "" {
		return strings.TrimSpace(req.TraceID)
	}
	if h, ok := safeHash(struct {
		RunID  string `json:"run_id"`
		StepID string `json:"step_id"`
		ReqID  string `json:"request_id"`
	}{
		RunID: req.RunID, StepID: req.StepID, ReqID: req.RequestID,
	}); ok {
		return "tr_" + h[:16]
	}
	return "tr_fb_" + fallbackIDFromStrings(req.RunID, req.StepID, req.RequestID)
}

func defaultDecisionGraphID(req RuntimeDecisionRequest) string {
	if strings.TrimSpace(req.DecisionGraphID) != "" {
		return strings.TrimSpace(req.DecisionGraphID)
	}
	if h, ok := safeHash(struct {
		RunID string `json:"run_id"`
		ReqID string `json:"request_id"`
	}{
		RunID: req.RunID, ReqID: req.RequestID,
	}); ok {
		return "dg_" + h[:16]
	}
	return "dg_fb_" + fallbackIDFromStrings(req.RunID, req.RequestID)
}

func buildDecisionNodeID(decisionID string, req RuntimeDecisionRequest) string {
	if h, ok := safeHash(struct {
		DecisionID string `json:"decision_id"`
		StepID     string `json:"step_id"`
		Attempt    int    `json:"attempt"`
		Phase      string `json:"phase"`
	}{
		DecisionID: decisionID, StepID: req.StepID, Attempt: req.AttemptIndex, Phase: req.Phase,
	}); ok {
		return "node_dec_" + h[:16]
	}
	return "node_dec_fb_" + fallbackIDFromStrings(decisionID, req.StepID, req.Phase)
}

func featureSignalFieldValue(req RuntimeDecisionRequest, field string) string {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "feature_version":
		return strings.TrimSpace(req.FeatureVersion)
	case "feature_schema_version":
		return strings.TrimSpace(req.FeatureSchemaVersion)
	case "feature_evidence_ref":
		return strings.TrimSpace(req.FeatureEvidenceRef)
	case "feature_producer_id":
		return strings.TrimSpace(req.FeatureProducerID)
	default:
		return ""
	}
}

func missingFeatureSignalFieldsByContract(req RuntimeDecisionRequest, required []string) []string {
	missing := make([]string, 0, len(required))
	for _, field := range required {
		field = strings.ToLower(strings.TrimSpace(field))
		if field == "" {
			continue
		}
		if strings.TrimSpace(featureSignalFieldValue(req, field)) == "" {
			missing = append(missing, field)
		}
	}
	return missing
}

func missingFeatureSignalFields(req RuntimeDecisionRequest) []string {
	return missingFeatureSignalFieldsByContract(req, []string{
		"feature_version",
		"feature_evidence_ref",
		"feature_producer_id",
	})
}

func contractKeyResolutionOrder(riskTier, phase string) []string {
	risk := strings.ToLower(strings.TrimSpace(riskTier))
	if risk == "" {
		risk = "*"
	}
	p := strings.ToUpper(strings.TrimSpace(phase))
	if p == "" {
		p = "*"
	}
	// Explicit deterministic priority:
	// 1) risk|phase
	// 2) risk|*
	// 3) *|phase
	// 4) *|*
	return []string{
		risk + "|" + p,
		risk + "|*",
		"*|" + p,
		"*|*",
	}
}

func contractScopeResolutionOrder(scope ScopeRef) []ScopeRef {
	normalized := normalizeContractScope(scope)
	// Deterministic precedence:
	// 1) project
	// 2) workspace wildcard project
	// 3) org wildcard workspace+project
	// 4) global wildcard
	//
	// Resolver always evaluates key specificity inside each scope candidate.
	candidates := []ScopeRef{
		normalized,
		{OrgID: normalized.OrgID, WorkspaceID: normalized.WorkspaceID, ProjectID: "*"},
		{OrgID: normalized.OrgID, WorkspaceID: "*", ProjectID: "*"},
		globalContractScope(),
	}
	out := make([]ScopeRef, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		n := normalizeContractScope(c)
		key := scopeKey(n)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, n)
	}
	return out
}

func (s *Service) resolveFeatureSignalContractWithBinding(scope ScopeRef, riskTier, phase string, now time.Time) (FeatureSignalContract, FeatureSignalContractBinding, bool) {
	scopeOrder := contractScopeResolutionOrder(scope)
	keyOrder := contractKeyResolutionOrder(riskTier, phase)
	// Resolution order is scope-first then key-specificity:
	// project > workspace > org > global, then exact > risk|* > *|phase > *|*.
	path := make([]string, 0, len(scopeOrder)*len(keyOrder))

	for _, sc := range scopeOrder {
		for _, key := range keyOrder {
			storageKey := featureSignalContractStorageKey(sc, key)
			path = append(path, storageKey)
			contract, ok := s.featureSignalContractsScoped[storageKey]
			if !ok || len(contract.RequiredFields) == 0 {
				continue
			}
			version := 1
			contractHash := safeHashForContract(contract)
			schemaVersion := strings.TrimSpace(contract.SchemaVersion)
			active := s.activeFeatureSignalContractVersionLocked(storageKey)
			if active.Version > 0 {
				version = active.Version
				if strings.TrimSpace(active.ContractHash) != "" {
					contractHash = active.ContractHash
				}
				if strings.TrimSpace(active.SchemaVersion) != "" {
					schemaVersion = strings.TrimSpace(active.SchemaVersion)
				}
			}
			binding := FeatureSignalContractBinding{
				Scope:            sc,
				ScopeMatchLevel:  scopeMatchLevel(sc),
				KeyMatchLevel:    keyMatchLevel(key, riskTier, phase),
				ContractKey:      key,
				StorageKey:       storageKey,
				Version:          version,
				StoreVersion:     s.storeVersion,
				ContractHash:     contractHash,
				SchemaVersion:    schemaVersion,
				ResolutionPath:   append([]string(nil), path...),
				ResolvedAt:       now,
				RequiredFields:   append([]string(nil), contract.RequiredFields...),
				MaxFreshnessMS:   contract.MaxFreshnessMS,
				MaxDriftScore:    contract.MaxDriftScore,
				TrustedProducers: append([]string(nil), contract.TrustedProducerIDs...),
			}
			return contract, binding, true
		}
	}

	defaultContract := FeatureSignalContract{
		RequiredFields: []string{"feature_version", "feature_evidence_ref", "feature_producer_id"},
	}
	binding := FeatureSignalContractBinding{
		Scope:            normalizeContractScope(scope),
		ScopeMatchLevel:  "none",
		KeyMatchLevel:    "none",
		ContractKey:      "",
		StorageKey:       "",
		Version:          0,
		StoreVersion:     s.storeVersion,
		ContractHash:     "",
		SchemaVersion:    "",
		ResolutionPath:   append([]string(nil), path...),
		ResolvedAt:       now,
		RequiredFields:   append([]string(nil), defaultContract.RequiredFields...),
		MaxFreshnessMS:   0,
		MaxDriftScore:    0,
		TrustedProducers: nil,
	}
	return defaultContract, binding, false
}

func scopeMatchLevel(scope ScopeRef) string {
	s := normalizeContractScope(scope)
	switch {
	case s.OrgID == "*" && s.WorkspaceID == "*" && s.ProjectID == "*":
		return "global"
	case s.WorkspaceID == "*" && s.ProjectID == "*":
		return "org"
	case s.ProjectID == "*":
		return "workspace"
	default:
		return "project"
	}
}

func keyMatchLevel(key, riskTier, phase string) string {
	risk := strings.ToLower(strings.TrimSpace(riskTier))
	if risk == "" {
		risk = "*"
	}
	p := strings.ToUpper(strings.TrimSpace(phase))
	if p == "" {
		p = "*"
	}
	parts := strings.SplitN(strings.TrimSpace(key), "|", 2)
	keyRisk := "*"
	keyPhase := "*"
	if len(parts) >= 1 && strings.TrimSpace(parts[0]) != "" {
		keyRisk = strings.ToLower(strings.TrimSpace(parts[0]))
	}
	if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
		keyPhase = strings.ToUpper(strings.TrimSpace(parts[1]))
	}
	normalizedKey := keyRisk + "|" + keyPhase
	switch key {
	case "":
		return "unknown"
	}
	switch normalizedKey {
	case risk + "|" + p:
		return "exact"
	case risk + "|*":
		return "risk_wildcard"
	case "*|" + p:
		return "phase_wildcard"
	case "*|*":
		return "global_wildcard"
	default:
		return "unknown"
	}
}

func (s *Service) resolveFeatureSignalContract(riskTier, phase string) FeatureSignalContract {
	contract, _, _ := s.resolveFeatureSignalContractWithBinding(globalContractScope(), riskTier, phase, s.cfg.Clock())
	return contract
}

func isFeatureStale(req RuntimeDecisionRequest) bool {
	if req.FeatureTTLMS <= 0 {
		return false
	}
	return req.FeatureFreshnessMS > req.FeatureTTLMS
}

type featureStalePolicyResult struct {
	StaleDetected bool
	Allow         bool
	Decision      string
	ReasonCode    string
	WindowMS      int
}

func evaluateFeatureStalePolicy(req RuntimeDecisionRequest) featureStalePolicyResult {
	if req.FeatureTTLMS <= 0 || req.FeatureFreshnessMS <= req.FeatureTTLMS {
		return featureStalePolicyResult{}
	}
	out := featureStalePolicyResult{
		StaleDetected: true,
	}
	switch req.RiskTier {
	case RiskLow:
		out.Allow = true
		out.ReasonCode = "feature_snapshot_stale_low_allowed"
	case RiskMedium:
		window := req.FeatureTTLMS * 2
		if window <= req.FeatureTTLMS {
			window = req.FeatureTTLMS + 1
		}
		out.WindowMS = window
		if req.FeatureFreshnessMS <= window {
			out.Allow = true
			out.ReasonCode = "feature_snapshot_stale_medium_within_window"
		} else {
			out.Decision = DecisionReviewRequired
			out.ReasonCode = "feature_snapshot_stale_medium_exceeded_window"
		}
	case RiskHigh:
		out.Decision = DecisionReviewRequired
		out.ReasonCode = "feature_snapshot_stale_high_review_required"
	case RiskCritical:
		out.Decision = DecisionFailClosed
		out.ReasonCode = "feature_snapshot_stale_critical_fail_closed"
	default:
		out.Decision = DecisionReviewRequired
		out.ReasonCode = "feature_snapshot_stale_review_required"
	}
	return out
}

type featureTrustPolicyResult struct {
	Blocked    bool
	Decision   string
	ReasonCode string
}

func evaluateFeatureTrustContract(req RuntimeDecisionRequest, contract FeatureSignalContract) featureTrustPolicyResult {
	if sv := strings.TrimSpace(contract.SchemaVersion); sv != "" {
		if strings.TrimSpace(req.FeatureSchemaVersion) == "" {
			return featureTrustPolicyResult{
				Blocked:    true,
				Decision:   decisionByRisk(req.RiskTier),
				ReasonCode: "feature_schema_version_missing",
			}
		}
		if !strings.EqualFold(strings.TrimSpace(req.FeatureSchemaVersion), sv) {
			return featureTrustPolicyResult{
				Blocked:    true,
				Decision:   decisionByRisk(req.RiskTier),
				ReasonCode: "feature_schema_version_mismatch",
			}
		}
	}
	if len(contract.TrustedProducerIDs) > 0 {
		allowed := false
		for _, p := range contract.TrustedProducerIDs {
			if strings.EqualFold(strings.TrimSpace(p), strings.TrimSpace(req.FeatureProducerID)) {
				allowed = true
				break
			}
		}
		if !allowed {
			return featureTrustPolicyResult{
				Blocked:    true,
				Decision:   decisionByRisk(req.RiskTier),
				ReasonCode: "feature_producer_not_trusted",
			}
		}
	}
	if contract.MaxFreshnessMS > 0 && req.FeatureFreshnessMS > contract.MaxFreshnessMS {
		return featureTrustPolicyResult{
			Blocked:    true,
			Decision:   decisionByRisk(req.RiskTier),
			ReasonCode: "feature_freshness_exceeds_contract",
		}
	}
	if contract.MaxDriftScore > 0 && req.FeatureDriftScore > contract.MaxDriftScore {
		return featureTrustPolicyResult{
			Blocked:    true,
			Decision:   decisionByRisk(req.RiskTier),
			ReasonCode: "feature_drift_exceeds_contract",
		}
	}
	return featureTrustPolicyResult{}
}

func decisionByRisk(riskTier string) string {
	if isHighOrCritical(riskTier) {
		return DecisionFailClosed
	}
	return DecisionReviewRequired
}

func evaluateFeatureDrift(req RuntimeDecisionRequest) (bool, string) {
	if req.FeatureDriftScore <= 0 {
		return false, ""
	}
	switch req.RiskTier {
	case RiskCritical:
		if req.FeatureDriftScore > 0.7 {
			return true, "feature_drift_fail_closed"
		}
	case RiskHigh:
		if req.FeatureDriftScore > 0.5 {
			return true, "feature_drift_review_required"
		}
	default:
		if req.FeatureDriftScore > 0.7 {
			return true, "feature_drift_review_required"
		}
	}
	return false, ""
}

func missingFrozenFields(in FrozenInput) []string {
	missing := make([]string, 0, 6)
	if strings.TrimSpace(in.ContextCandidatesSnapshotRef) == "" {
		missing = append(missing, "context_candidates_snapshot_ref")
	}
	if strings.TrimSpace(in.PolicyBundleSnapshotRef) == "" {
		missing = append(missing, "policy_bundle_snapshot_ref")
	}
	if strings.TrimSpace(in.FeatureSnapshotID) == "" {
		missing = append(missing, "feature_snapshot_id")
	}
	if strings.TrimSpace(in.ApprovalRoutingSnapshotRef) == "" {
		missing = append(missing, "approval_routing_snapshot_ref")
	}
	if strings.TrimSpace(in.QuotaSnapshotRef) == "" {
		missing = append(missing, "quota_snapshot_ref")
	}
	if strings.TrimSpace(in.SchedulerAdmissionInputSnapshotRef) == "" {
		missing = append(missing, "scheduler_admission_input_snapshot_ref")
	}
	return missing
}

func forbiddenDynamicFields(fields []string) []string {
	bad := make([]string, 0, len(fields))
	for _, f := range fields {
		if _, ok := allowedDynamicFields[f]; !ok {
			bad = append(bad, f)
		}
	}
	return bad
}

func calcDCU(in DCUInput) DCUBreakdown {
	featureCost := in.FeatureReads * 2
	ruleCost := in.RuleEvals * 1
	depCost := in.DependencyCalls * 4
	conflictCost := in.ConflictResolutions * 3
	total := featureCost + ruleCost + depCost + conflictCost
	return DCUBreakdown{
		FeatureReadsCost:       featureCost,
		RuleEvalCost:           ruleCost,
		DependencyCallCost:     depCost,
		ConflictResolutionCost: conflictCost,
		Total:                  total,
	}
}

func evaluatePolicyPhase(input PolicyInput, phase string, rules []PolicyRule) PolicyEvaluation {
	out := PolicyEvaluation{
		MatchedRuleIDs:        make([]string, 0, 2),
		Obligations:           make([]Obligation, 0, 2),
		Conflicts:             make([]string, 0, 1),
		DecisionOverrideTrace: make([]PolicyDecisionOverride, 0, 2),
		ConflictingRulePairs:  make([]string, 0, 2),
	}

	matched := make([]PolicyRule, 0, len(rules))
	for _, r := range rules {
		if r.Phase != phase {
			continue
		}
		if !matchPolicy(input, r.Match) {
			continue
		}
		matched = append(matched, r)
		out.MatchedRuleIDs = append(out.MatchedRuleIDs, r.RuleID)
		if out.Decision == "" {
			out.Decision = r.Decision
			out.EffectiveRuleID = r.RuleID
		} else {
			currentPriority := ruleDecisionPriority(out.Decision)
			newPriority := ruleDecisionPriority(r.Decision)
			if r.Decision != out.Decision {
				left, right := out.EffectiveRuleID, r.RuleID
				if left > right {
					left, right = right, left
				}
				out.Conflicts = append(out.Conflicts, "policy_decision_conflict")
				out.ConflictingRulePairs = append(out.ConflictingRulePairs, left+"|"+right)
			}
			if newPriority > currentPriority {
				out.DecisionOverrideTrace = append(out.DecisionOverrideTrace, PolicyDecisionOverride{
					FromRuleID:   out.EffectiveRuleID,
					FromDecision: out.Decision,
					ToRuleID:     r.RuleID,
					ToDecision:   r.Decision,
					Reason:       "higher_decision_priority",
				})
				out.Decision = r.Decision
				out.EffectiveRuleID = r.RuleID
			}
		}
		out.Obligations = append(out.Obligations, r.Obligations...)
	}

	sort.Strings(out.Conflicts)
	sort.Strings(out.ConflictingRulePairs)
	out.Conflicts = uniqueStrings(out.Conflicts)
	out.ConflictingRulePairs = uniqueStrings(out.ConflictingRulePairs)

	sort.Slice(out.Obligations, func(i, j int) bool {
		left := obligationOrder[out.Obligations[i].Type]
		right := obligationOrder[out.Obligations[j].Type]
		if left != right {
			return left < right
		}
		return out.Obligations[i].Strictness > out.Obligations[j].Strictness
	})

	return out
}

func BuildObligationPlan(preTool []Obligation, preResume []Obligation, approvalObligations []Obligation) ObligationPlan {
	out := ObligationPlan{
		Obligations:      make([]Obligation, 0, len(preTool)+len(preResume)+len(approvalObligations)),
		Conflicts:        make([]string, 0, 2),
		ResolutionTraces: make([]ObligationResolution, 0, len(preTool)+len(preResume)+len(approvalObligations)),
	}
	index := make(map[string]Obligation)

	for _, item := range preTool {
		key := item.Type + "|" + item.Target
		index[key] = item
		out.ResolutionTraces = append(out.ResolutionTraces, ObligationResolution{
			Key:                key,
			IncomingPhase:      item.Phase,
			IncomingStrictness: item.Strictness,
			Outcome:            "selected_initial",
		})
	}

	// PRE_RESUME cannot relax PRE_TOOL same-key obligations.
	for _, item := range preResume {
		key := item.Type + "|" + item.Target
		var prev Obligation
		var hasPrev bool
		if existing, ok := index[key]; ok {
			prev = existing
			hasPrev = true
			if item.Strictness < existing.Strictness {
				out.Conflicts = append(out.Conflicts, "obligation_phase_conflict")
				out.ResolutionTraces = append(out.ResolutionTraces, ObligationResolution{
					Key:                key,
					PreviousPhase:      existing.Phase,
					PreviousStrictness: existing.Strictness,
					IncomingPhase:      item.Phase,
					IncomingStrictness: item.Strictness,
					Outcome:            "rejected",
					Reason:             "pre_resume_cannot_relax_pre_tool",
				})
				continue
			}
		}
		index[key] = item
		out.ResolutionTraces = append(out.ResolutionTraces, ObligationResolution{
			Key:                key,
			PreviousPhase:      prev.Phase,
			PreviousStrictness: prev.Strictness,
			IncomingPhase:      item.Phase,
			IncomingStrictness: item.Strictness,
			Outcome:            "selected",
			Reason:             "pre_resume_tighten_or_equal",
		})
		if !hasPrev {
			out.ResolutionTraces[len(out.ResolutionTraces)-1].Reason = "pre_resume_new_key"
		}
	}

	// approval obligations can only tighten.
	for _, item := range approvalObligations {
		key := item.Type + "|" + item.Target
		var prev Obligation
		var hasPrev bool
		if existing, ok := index[key]; ok {
			prev = existing
			hasPrev = true
			if item.Strictness < existing.Strictness {
				out.Conflicts = append(out.Conflicts, "approval_obligation_weaken_forbidden")
				out.ResolutionTraces = append(out.ResolutionTraces, ObligationResolution{
					Key:                key,
					PreviousPhase:      existing.Phase,
					PreviousStrictness: existing.Strictness,
					IncomingPhase:      item.Phase,
					IncomingStrictness: item.Strictness,
					Outcome:            "rejected",
					Reason:             "approval_cannot_weaken_existing",
				})
				continue
			}
		}
		index[key] = item
		out.ResolutionTraces = append(out.ResolutionTraces, ObligationResolution{
			Key:                key,
			PreviousPhase:      prev.Phase,
			PreviousStrictness: prev.Strictness,
			IncomingPhase:      item.Phase,
			IncomingStrictness: item.Strictness,
			Outcome:            "selected",
			Reason:             "approval_tighten_or_equal",
		})
		if !hasPrev {
			out.ResolutionTraces[len(out.ResolutionTraces)-1].Reason = "approval_new_key"
		}
	}

	for _, item := range index {
		out.Obligations = append(out.Obligations, item)
	}
	sort.Slice(out.Obligations, func(i, j int) bool {
		oi := obligationOrder[out.Obligations[i].Type]
		oj := obligationOrder[out.Obligations[j].Type]
		if oi != oj {
			return oi < oj
		}
		if out.Obligations[i].Phase != out.Obligations[j].Phase {
			pi := phaseOrder[out.Obligations[i].Phase]
			pj := phaseOrder[out.Obligations[j].Phase]
			if pi == 0 {
				pi = 999
			}
			if pj == 0 {
				pj = 999
			}
			return pi < pj
		}
		return out.Obligations[i].Strictness > out.Obligations[j].Strictness
	})
	sort.Strings(out.Conflicts)
	return out
}

func matchPolicy(input PolicyInput, m PolicyMatch) bool {
	if len(m.RiskTiers) > 0 && !containsString(m.RiskTiers, input.RiskTier) {
		return false
	}
	if len(m.EffectTypes) > 0 && !containsString(m.EffectTypes, input.EffectType) {
		return false
	}
	return true
}

func ruleDecisionPriority(dec string) int {
	switch dec {
	case DecisionFailClosed:
		return 5
	case DecisionDeny, DecisionAutoDeny:
		return 4
	case DecisionRequireApproval:
		return 3
	case DecisionReviewRequired:
		return 2
	case DecisionAllow:
		return 1
	default:
		return 0
	}
}

func quotaExceeded(requested, quota map[string]float64) bool {
	for k, need := range requested {
		left := quota[k]
		if need > left {
			return true
		}
	}
	return false
}

func cloneResourceMap(in map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func evaluateEnforcement(metric string, value float64, hasValue bool, threshold EnforcementThreshold) string {
	if !hasValue {
		return EnforcementMissing
	}
	if threshold.Direction == "lt" {
		if value <= threshold.BlockRuntime {
			return EnforcementBlockRuntime
		}
		if value <= threshold.BlockRelease {
			return EnforcementBlockRelease
		}
		if value <= threshold.Alert {
			return EnforcementAlert
		}
		return EnforcementObserveOnly
	}
	if value >= threshold.BlockRuntime {
		return EnforcementBlockRuntime
	}
	if value >= threshold.BlockRelease {
		return EnforcementBlockRelease
	}
	if value >= threshold.Alert {
		return EnforcementAlert
	}
	return EnforcementObserveOnly
}

func finalizeRuntimeResponse(resp RuntimeDecisionResponse) RuntimeDecisionResponse {
	sort.Strings(resp.ReasonCodes)
	sort.Strings(resp.MatchedRuleIDs)
	sort.Strings(resp.Audit.ErrorCodes)
	if resp.Decision == DecisionAutoDeny && resp.DecisionSubType == "" {
		resp.DecisionSubType = "approval_unavailable"
	}
	hashInput := struct {
		Decision        string       `json:"decision"`
		DecisionSubType string       `json:"decision_sub_type"`
		ReasonCodes     []string     `json:"reason_codes"`
		MatchedRuleIDs  []string     `json:"matched_rule_ids"`
		Obligations     []Obligation `json:"obligations"`
		FeatureContract struct {
			Scope           ScopeRef  `json:"scope"`
			ScopeMatchLevel string    `json:"scope_match_level,omitempty"`
			KeyMatchLevel   string    `json:"key_match_level,omitempty"`
			ContractKey     string    `json:"contract_key"`
			StorageKey      string    `json:"storage_key"`
			Version         int       `json:"version"`
			StoreVersion    uint64    `json:"store_version,omitempty"`
			ContractHash    string    `json:"contract_hash"`
			SchemaVersion   string    `json:"schema_version,omitempty"`
			ResolvedAt      time.Time `json:"resolved_at,omitempty"`
		} `json:"feature_contract"`
		FrozenInputHash       string        `json:"frozen_input_hash"`
		TraceID               string        `json:"trace_id,omitempty"`
		DecisionGraphID       string        `json:"decision_graph_id,omitempty"`
		DecisionNodeID        string        `json:"decision_node_id,omitempty"`
		ParentDecisionNodeIDs []string      `json:"parent_decision_node_ids,omitempty"`
		Audit                 DecisionAudit `json:"audit"`
	}{
		Decision:        resp.Decision,
		DecisionSubType: resp.DecisionSubType,
		ReasonCodes:     resp.ReasonCodes,
		MatchedRuleIDs:  resp.MatchedRuleIDs,
		Obligations:     resp.Obligations,
		FeatureContract: struct {
			Scope           ScopeRef  `json:"scope"`
			ScopeMatchLevel string    `json:"scope_match_level,omitempty"`
			KeyMatchLevel   string    `json:"key_match_level,omitempty"`
			ContractKey     string    `json:"contract_key"`
			StorageKey      string    `json:"storage_key"`
			Version         int       `json:"version"`
			StoreVersion    uint64    `json:"store_version,omitempty"`
			ContractHash    string    `json:"contract_hash"`
			SchemaVersion   string    `json:"schema_version,omitempty"`
			ResolvedAt      time.Time `json:"resolved_at,omitempty"`
		}{
			Scope:           resp.FeatureContractBinding.Scope,
			ScopeMatchLevel: resp.FeatureContractBinding.ScopeMatchLevel,
			KeyMatchLevel:   resp.FeatureContractBinding.KeyMatchLevel,
			ContractKey:     resp.FeatureContractBinding.ContractKey,
			StorageKey:      resp.FeatureContractBinding.StorageKey,
			Version:         resp.FeatureContractBinding.Version,
			StoreVersion:    resp.FeatureContractBinding.StoreVersion,
			ContractHash:    resp.FeatureContractBinding.ContractHash,
			SchemaVersion:   resp.FeatureContractBinding.SchemaVersion,
		},
		FrozenInputHash:       resp.FrozenInputHash,
		TraceID:               resp.TraceID,
		DecisionGraphID:       resp.DecisionGraphID,
		DecisionNodeID:        resp.DecisionNodeID,
		ParentDecisionNodeIDs: resp.ParentDecisionNodeIDs,
		Audit:                 resp.Audit,
	}
	hash, ok := safeHash(hashInput)
	if !ok {
		resp.DecisionHash = ""
		return resp
	}
	resp.DecisionHash = hash
	return resp
}

func hashJSON(v interface{}) string {
	hash, ok := hashJSONWithStatus(v)
	if !ok || isErrorHash(hash) {
		return ""
	}
	return hash
}

func safeHash(v interface{}) (string, bool) {
	hash, ok := hashJSONWithStatus(v)
	if !ok || isErrorHash(hash) {
		return "", false
	}
	return hash, true
}

func fallbackIDFromStrings(parts ...string) string {
	normalized := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			p = "na"
		}
		p = strings.ReplaceAll(p, " ", "_")
		p = strings.ReplaceAll(p, ":", "_")
		p = strings.ReplaceAll(p, "/", "_")
		if len(p) > 16 {
			p = p[:16]
		}
		normalized = append(normalized, p)
	}
	joined := strings.Join(normalized, "_")
	if joined == "" {
		return "na"
	}
	if len(joined) > 32 {
		return joined[:32]
	}
	return joined
}

func (s *Service) nextFallbackID(prefix string) string {
	seq := atomic.AddUint64(&s.idSeq, 1)
	return fmt.Sprintf("%sseq_%d", prefix, seq)
}

// makeBestEffortID returns a stable hash ID when possible; otherwise a unique
// fallback ID to keep liveness and traceability.
func (s *Service) makeBestEffortID(prefix string, payload interface{}) (string, bool) {
	hash, ok := safeHash(payload)
	if !ok {
		return s.nextFallbackID(prefix), false
	}
	return prefix + hash[:16], true
}

func hashJSONWithStatus(v interface{}) (string, bool) {
	b, err := marshalJSON(v)
	if err != nil {
		marker := fmt.Sprintf("json_marshal_error:%T:%v", v, err)
		sum := sha256.Sum256([]byte(marker))
		return "err_" + hex.EncodeToString(sum[:]), false
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), true
}

func isErrorHash(v string) bool {
	return strings.HasPrefix(strings.TrimSpace(v), "err_")
}

func toMap(v interface{}) map[string]interface{} {
	raw, err := json.Marshal(v)
	if err != nil {
		return map[string]interface{}{
			"_error":      "marshal_failed",
			"_error_type": fmt.Sprintf("%T", v),
			"_error_msg":  err.Error(),
		}
	}
	out := make(map[string]interface{})
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return map[string]interface{}{
			"_error":   "unmarshal_failed",
			"_raw_b64": base64.StdEncoding.EncodeToString(raw),
		}
	}
	return out
}

func toCanonicalRecord(v interface{}) (json.RawMessage, map[string]interface{}) {
	raw, err := json.Marshal(v)
	if err != nil {
		errRaw := json.RawMessage(`{"_error":"marshal_failed"}`)
		return errRaw, map[string]interface{}{
			"_error":      "marshal_failed",
			"_error_type": fmt.Sprintf("%T", v),
			"_error_msg":  err.Error(),
		}
	}
	return json.RawMessage(raw), toMap(v)
}

func extractDecisionHash(resp interface{}) string {
	switch r := resp.(type) {
	case RuntimeDecisionResponse:
		if strings.TrimSpace(r.DecisionHash) != "" {
			return r.DecisionHash
		}
	case *RuntimeDecisionResponse:
		if r != nil && strings.TrimSpace(r.DecisionHash) != "" {
			return r.DecisionHash
		}
	case ScheduleAdmissionResponse:
		if strings.TrimSpace(r.DecisionHash) != "" {
			return r.DecisionHash
		}
	case *ScheduleAdmissionResponse:
		if r != nil && strings.TrimSpace(r.DecisionHash) != "" {
			return r.DecisionHash
		}
	case ReleaseDecisionResponse:
		if strings.TrimSpace(r.DecisionHash) != "" {
			return r.DecisionHash
		}
	case *ReleaseDecisionResponse:
		if r != nil && strings.TrimSpace(r.DecisionHash) != "" {
			return r.DecisionHash
		}
	}
	return ""
}

func extractDecisionHashRef(resp interface{}) DecisionHashRef {
	derive := func(ref DecisionHashRef, hash string, fallback bool) DecisionHashRef {
		if strings.TrimSpace(ref.Value) == "" {
			ref.Value = hash
		}
		if fallback {
			return makeDecisionHashRef(ref.Value, false, DecisionHashKindFallback)
		}
		if strings.TrimSpace(ref.Kind) != "" {
			if !ref.Valid {
				return makeDecisionHashRef(ref.Value, false, ref.Kind)
			}
			if strings.TrimSpace(ref.Value) == "" || isErrorHash(ref.Value) {
				return makeDecisionHashRef(ref.Value, false, DecisionHashKindError)
			}
			return makeDecisionHashRef(ref.Value, true, ref.Kind)
		}
		if strings.TrimSpace(ref.Value) == "" || isErrorHash(ref.Value) {
			return makeDecisionHashRef(ref.Value, false, DecisionHashKindError)
		}
		return makeDecisionHashRef(ref.Value, true, DecisionHashKindStable)
	}

	switch r := resp.(type) {
	case RuntimeDecisionResponse:
		return derive(r.DecisionHashRef, r.DecisionHash, r.DecisionHashIsFallback)
	case *RuntimeDecisionResponse:
		if r != nil {
			return derive(r.DecisionHashRef, r.DecisionHash, r.DecisionHashIsFallback)
		}
	case ScheduleAdmissionResponse:
		return derive(r.DecisionHashRef, r.DecisionHash, r.DecisionHashIsFallback)
	case *ScheduleAdmissionResponse:
		if r != nil {
			return derive(r.DecisionHashRef, r.DecisionHash, r.DecisionHashIsFallback)
		}
	case ReleaseDecisionResponse:
		return derive(r.DecisionHashRef, r.DecisionHash, r.DecisionHashIsFallback)
	case *ReleaseDecisionResponse:
		if r != nil {
			return derive(r.DecisionHashRef, r.DecisionHash, r.DecisionHashIsFallback)
		}
	}
	hash := extractDecisionHash(resp)
	if strings.TrimSpace(hash) == "" || isErrorHash(hash) {
		return makeDecisionHashRef(hash, false, DecisionHashKindError)
	}
	if extractDecisionHashIsFallback(resp) {
		return makeDecisionHashRef(hash, false, DecisionHashKindFallback)
	}
	return makeDecisionHashRef(hash, true, DecisionHashKindStable)
}

func extractDecisionHashIsFallback(resp interface{}) bool {
	switch r := resp.(type) {
	case RuntimeDecisionResponse:
		return r.DecisionHashIsFallback
	case *RuntimeDecisionResponse:
		return r != nil && r.DecisionHashIsFallback
	case ScheduleAdmissionResponse:
		return r.DecisionHashIsFallback
	case *ScheduleAdmissionResponse:
		return r != nil && r.DecisionHashIsFallback
	case ReleaseDecisionResponse:
		return r.DecisionHashIsFallback
	case *ReleaseDecisionResponse:
		return r != nil && r.DecisionHashIsFallback
	}
	return false
}

func buildDecisionKey(kind string, req interface{}) string {
	switch r := req.(type) {
	case RuntimeDecisionRequest:
		return fmt.Sprintf("%s|%s|%s|%d|%s", kind, r.RunID, r.StepID, r.AttemptIndex, strings.ToUpper(strings.TrimSpace(r.Phase)))
	case *RuntimeDecisionRequest:
		if r != nil {
			return fmt.Sprintf("%s|%s|%s|%d|%s", kind, r.RunID, r.StepID, r.AttemptIndex, strings.ToUpper(strings.TrimSpace(r.Phase)))
		}
	case ScheduleAdmissionRequest:
		return fmt.Sprintf("%s|%s|%s|%s", kind, r.RunID, r.StepID, strings.ToLower(strings.TrimSpace(r.TenantID)))
	case *ScheduleAdmissionRequest:
		if r != nil {
			return fmt.Sprintf("%s|%s|%s|%s", kind, r.RunID, r.StepID, strings.ToLower(strings.TrimSpace(r.TenantID)))
		}
	case ReleaseDecisionRequest:
		return fmt.Sprintf("%s|%s|%s", kind, strings.TrimSpace(r.RequestID), strings.ToLower(strings.TrimSpace(r.RiskTier)))
	case *ReleaseDecisionRequest:
		if r != nil {
			return fmt.Sprintf("%s|%s|%s", kind, strings.TrimSpace(r.RequestID), strings.ToLower(strings.TrimSpace(r.RiskTier)))
		}
	}
	if h, ok := safeHash(req); ok {
		return fmt.Sprintf("%s|%s", kind, h[:16])
	}
	return fmt.Sprintf("%s|%s", kind, fallbackIDFromStrings(kind))
}

func extractDecisionBinding(req interface{}) (runID string, stepID string, attemptIndex int, phase string) {
	switch r := req.(type) {
	case RuntimeDecisionRequest:
		return r.RunID, r.StepID, r.AttemptIndex, strings.ToUpper(strings.TrimSpace(r.Phase))
	case *RuntimeDecisionRequest:
		if r != nil {
			return r.RunID, r.StepID, r.AttemptIndex, strings.ToUpper(strings.TrimSpace(r.Phase))
		}
	case ScheduleAdmissionRequest:
		return r.RunID, r.StepID, 0, PhasePreTool
	case *ScheduleAdmissionRequest:
		if r != nil {
			return r.RunID, r.StepID, 0, PhasePreTool
		}
	}
	return "", "", 0, ""
}

func canonicalizeForDecisionSemantic(m map[string]interface{}) string {
	if len(m) == 0 {
		return "{}"
	}
	cp := make(map[string]interface{}, len(m))
	for k, v := range m {
		cp[k] = v
	}
	delete(cp, "decision_hash")
	delete(cp, "decision_hash_is_fallback")
	delete(cp, "decision_hash_ref")
	raw, err := marshalJSON(cp)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func shouldCreatePendingDecision(decision string) bool {
	switch decision {
	case DecisionAllow, DecisionRequireApproval, DecisionReviewRequired, DecisionForceReview:
		return true
	default:
		return false
	}
}

func uniqueStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, v := range in {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func isValidRisk(risk string) bool {
	_, ok := validRisk[strings.ToLower(strings.TrimSpace(risk))]
	return ok
}

func isValidEffect(effect string) bool {
	_, ok := validEffect[strings.ToLower(strings.TrimSpace(effect))]
	return ok
}

func isHighOrCritical(risk string) bool {
	r := strings.ToLower(strings.TrimSpace(risk))
	return r == RiskHigh || r == RiskCritical
}

func containsString(values []string, needle string) bool {
	for _, v := range values {
		if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(needle)) {
			return true
		}
	}
	return false
}

func extractStringFieldFromMap(m map[string]interface{}, key string) string {
	if len(m) == 0 {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case json.Number:
		return strings.TrimSpace(t.String())
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", t))
	}
}

func extractObligationsFromMap(m map[string]interface{}) []Obligation {
	if len(m) == 0 {
		return nil
	}
	raw, ok := m["obligations"]
	if !ok || raw == nil {
		return nil
	}
	list, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]Obligation, 0, len(list))
	for _, item := range list {
		im, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		ob := Obligation{
			Type:       extractStringFieldFromMap(im, "type"),
			Target:     extractStringFieldFromMap(im, "target"),
			Value:      extractStringFieldFromMap(im, "value"),
			Phase:      strings.ToUpper(extractStringFieldFromMap(im, "phase")),
			Strictness: extractIntFieldFromMap(im, "strictness"),
		}
		if ob.Type == "" || ob.Value == "" {
			continue
		}
		out = append(out, ob)
	}
	return out
}

func extractIntFieldFromMap(m map[string]interface{}, key string) int {
	if len(m) == 0 {
		return 0
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case json.Number:
		i, err := t.Int64()
		if err == nil {
			return int(i)
		}
		f, err := t.Float64()
		if err == nil {
			return int(f)
		}
		return 0
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

func firstOrEmpty(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func lastOrEmpty(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}
