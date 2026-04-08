package run

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var marshalJSON = json.Marshal

type runInstance struct {
	view            RunView
	maxLifetime     time.Duration
	securityRisk    bool
	resourceRisk    bool
	noProgressScans int
}

type Kernel struct {
	mu sync.RWMutex

	cfg          Config
	decisionPort DecisionPort

	runs          map[string]*runInstance
	continuations map[string]*Continuation // key: continuation token
	idempotency   map[string]idempotencyEntry
	createRuns    map[string]createRunEntry // key: tenant_id|request_id
	stepIntegrity map[string][]StepIntegrity
	outbox        []KernelEvent
	eventSeq      uint64
	// runSeq is guarded by k.mu in CreateRun. Keep this invariant if lock model evolves.
	runSeq                 uint64
	outboxTrimmed          uint64
	createRunConflictTotal uint64
	createRunReuseTotal    uint64
}

type idempotencyEntry struct {
	RunID        string
	Response     AdvanceRunResponse
	CreatedAt    time.Time
	LastAccessAt time.Time
}

type createRunEntry struct {
	RunID        string
	RequestHash  string
	Response     CreateRunResponse
	CreatedAt    time.Time
	LastAccessAt time.Time
}

func NewKernel(cfg Config, decisionPort DecisionPort) *Kernel {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.ContinuationTTL <= 0 {
		cfg.ContinuationTTL = 30 * time.Minute
	}
	if cfg.ContinuationRetention <= 0 {
		cfg.ContinuationRetention = 24 * time.Hour
	}
	if cfg.IdempotencyTTL <= 0 {
		cfg.IdempotencyTTL = 24 * time.Hour
	}
	if cfg.RunRetention <= 0 {
		cfg.RunRetention = 72 * time.Hour
	}
	if cfg.CreateRequestRetention <= 0 {
		cfg.CreateRequestRetention = cfg.RunRetention
	}
	if cfg.OutboxMaxEvents <= 0 {
		cfg.OutboxMaxEvents = 10000
	}
	if cfg.OutboxRetention <= 0 {
		cfg.OutboxRetention = 24 * time.Hour
	}
	if cfg.SweepBatchLimit <= 0 {
		cfg.SweepBatchLimit = 2000
	}
	if cfg.ZombieNoProgressWindow <= 0 {
		cfg.ZombieNoProgressWindow = 10 * time.Minute
	}
	if len(cfg.RunMaxLifetime) == 0 {
		cfg.RunMaxLifetime = map[string]time.Duration{
			RiskLow:      1 * time.Hour,
			RiskMedium:   6 * time.Hour,
			RiskHigh:     24 * time.Hour,
			RiskCritical: 24 * time.Hour,
		}
	}

	return &Kernel{
		cfg:           cfg,
		decisionPort:  decisionPort,
		runs:          make(map[string]*runInstance),
		continuations: make(map[string]*Continuation),
		idempotency:   make(map[string]idempotencyEntry),
		createRuns:    make(map[string]createRunEntry),
		stepIntegrity: make(map[string][]StepIntegrity),
		outbox:        make([]KernelEvent, 0, 32),
	}
}

func (k *Kernel) CreateRun(req CreateRunRequest) (CreateRunResponse, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if strings.TrimSpace(req.RequestID) == "" || strings.TrimSpace(req.TenantID) == "" || strings.TrimSpace(req.WorkflowID) == "" || strings.TrimSpace(req.WorkflowVersion) == "" {
		return CreateRunResponse{}, kernelErr(422, "RUN_INPUT_INVALID", "request_id/tenant/workflow/version required", "input_invalid")
	}
	risk := normalizeRisk(req.RiskTier)
	if !isValidRisk(risk) {
		return CreateRunResponse{}, kernelErr(422, "RUN_INPUT_INVALID", "invalid risk_tier", "input_invalid_risk")
	}
	if strings.TrimSpace(req.Snapshot.ContextPolicySpaceHash) == "" {
		return CreateRunResponse{}, kernelErr(409, "RUN_SNAPSHOT_FREEZE_FAILED", "context policy space hash required", "snapshot_freeze_failed")
	}

	now := k.cfg.Clock()
	createFingerprintInput := struct {
		RequestID string        `json:"request_id"`
		TenantID  string        `json:"tenant_id"`
		Workflow  string        `json:"workflow"`
		Version   string        `json:"version"`
		Risk      string        `json:"risk"`
		InputRef  string        `json:"input_ref"`
		Snapshot  SnapshotInput `json:"snapshot"`
	}{
		RequestID: req.RequestID,
		TenantID:  req.TenantID,
		Workflow:  req.WorkflowID,
		Version:   req.WorkflowVersion,
		Risk:      risk,
		InputRef:  req.InputPayloadRef,
		Snapshot:  req.Snapshot,
	}
	createFingerprint, ok := hashJSONWithStatus(createFingerprintInput)
	if !ok || isErrorHash(createFingerprint) {
		return CreateRunResponse{}, kernelErr(500, "RUN_HASH_GENERATION_FAILED", "failed to hash create request", "hash_generation_failed")
	}
	createKey := req.TenantID + "|" + req.RequestID
	if existing, found := k.createRuns[createKey]; found {
		if existing.RequestHash != createFingerprint {
			atomic.AddUint64(&k.createRunConflictTotal, 1)
			k.emit("run_create_request_conflict", existing.RunID, "", map[string]interface{}{
				"tenant_id":  req.TenantID,
				"request_id": req.RequestID,
				"create_key": createKey,
				"conflict":   true,
			}, now)
			return CreateRunResponse{}, kernelErr(409, "RUN_CREATE_REQUEST_CONFLICT", "request_id reused with different payload", "create_request_conflict")
		}
		if _, runExists := k.runs[existing.RunID]; runExists {
			existing.LastAccessAt = now
			k.createRuns[createKey] = existing
			atomic.AddUint64(&k.createRunReuseTotal, 1)
			k.emit("run_create_reused", existing.RunID, "", map[string]interface{}{
				"tenant_id":  req.TenantID,
				"request_id": req.RequestID,
				"create_key": createKey,
			}, now)
			return existing.Response, nil
		}
		delete(k.createRuns, createKey)
	}
	seq := k.runSeq + 1
	k.runSeq = seq
	runID := fmt.Sprintf("run_%s_%08x", createFingerprint[:8], seq)

	snapshot := RunSnapshot{
		PolicyBundleID:         req.Snapshot.PolicyBundleID,
		ModelProfileID:         req.Snapshot.ModelProfileID,
		DependencyBundleID:     req.Snapshot.DependencyBundleID,
		SkillBundleSetHash:     req.Snapshot.SkillBundleSetHash,
		ContextPolicySpaceHash: req.Snapshot.ContextPolicySpaceHash,
		CreatedAt:              now,
	}
	snapshotHash, ok := hashJSONWithStatus(snapshot)
	if !ok || isErrorHash(snapshotHash) {
		return CreateRunResponse{}, kernelErr(500, "RUN_HASH_GENERATION_FAILED", "failed to hash run snapshot", "hash_generation_failed")
	}
	snapshot.SnapshotHash = snapshotHash

	view := RunView{
		RunID:                   runID,
		TenantID:                req.TenantID,
		WorkflowID:              req.WorkflowID,
		WorkflowVersion:         req.WorkflowVersion,
		State:                   StateRunning,
		ActiveStepID:            "",
		StateVersion:            1,
		RiskTier:                risk,
		IntegrityChainVersion:   "v1",
		LastStepHash:            "",
		RunIntegrityRoot:        "",
		RunIntegrityRootVersion: 0,
		IntegrityRootType:       "",
		TerminalReason:          "",
		TerminalSource:          "",
		CurrentStepSeqID:        0,
		CreatedAt:               now,
		UpdatedAt:               now,
		LastProgressAt:          now,
		Snapshot:                snapshot,
		RootAnchors:             RootAnchors{},
	}
	instance := &runInstance{
		view:        view,
		maxLifetime: k.maxLifetimeForRisk(risk),
	}
	k.runs[runID] = instance
	k.emit("run.created", runID, "", map[string]interface{}{
		"snapshot_hash": snapshot.SnapshotHash,
		"risk_tier":     risk,
	}, now)

	resp := CreateRunResponse{
		RunID:                  runID,
		SnapshotHash:           snapshot.SnapshotHash,
		ContextPolicySpaceHash: snapshot.ContextPolicySpaceHash,
		InitialState:           StateRunning,
		CreatedAt:              now,
	}
	k.createRuns[createKey] = createRunEntry{
		RunID:        runID,
		RequestHash:  createFingerprint,
		Response:     resp,
		CreatedAt:    now,
		LastAccessAt: now,
	}
	return resp, nil
}

func (k *Kernel) AdvanceRun(runID string, req AdvanceRunRequest) (AdvanceRunResponse, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	r, err := k.getRunLocked(runID)
	if err != nil {
		return AdvanceRunResponse{}, err
	}
	now := k.cfg.Clock()

	if isTerminalState(r.view.State) {
		return AdvanceRunResponse{}, kernelErr(409, "RUN_TERMINAL_STATE", "run is terminal", "terminal_state")
	}
	if strings.EqualFold(r.view.State, StateParked) || strings.EqualFold(r.view.State, StateAwaitingApproval) {
		return AdvanceRunResponse{}, kernelErr(409, "RUN_STATE_CONFLICT", "run must resume before advance", "state_conflict")
	}
	if strings.TrimSpace(req.StepID) == "" || strings.TrimSpace(req.StepVersion) == "" || strings.TrimSpace(req.IdempotencyKey) == "" {
		return AdvanceRunResponse{}, kernelErr(422, "RUN_INPUT_INVALID", "step_id/step_version/idempotency_key required", "input_invalid")
	}

	idemKey := fmt.Sprintf("%s|%s|%s|%s", runID, req.StepID, req.StepVersion, req.IdempotencyKey)
	if cached, ok := k.idempotency[idemKey]; ok {
		cached.LastAccessAt = now
		k.idempotency[idemKey] = cached
		return cached.Response, nil
	}
	if req.ExpectedStateVersion != r.view.StateVersion {
		return AdvanceRunResponse{}, kernelErr(409, "RUN_STATE_VERSION_CONFLICT", "expected_state_version mismatch", "state_version_conflict")
	}

	if req.IncomingStepSeqID <= r.view.CurrentStepSeqID {
		r.view.State = StateSafeguardHold
		r.view.UpdatedAt = now
		k.anchorRootLocked(r, RootTypePartial, r.view.LastStepHash)
		k.emitProgressMonotonicityViolation(runID, req.StepID, req.IncomingStepSeqID, r.view.CurrentStepSeqID, now)
		return AdvanceRunResponse{}, kernelErr(409, "RUN_PROGRESS_MONOTONICITY_VIOLATION", "incoming_step_seq_id must be strictly increasing", "progress_monotonicity_violation")
	}

	if strings.TrimSpace(req.DecisionRef.DecisionID) == "" || strings.TrimSpace(req.DecisionRef.DecisionHash) == "" {
		return AdvanceRunResponse{}, kernelErr(422, "RUN_INPUT_INVALID", "decision_ref required", "decision_ref_missing")
	}
	if k.decisionPort == nil {
		r.view.State = StateSafeguardHold
		r.view.UpdatedAt = now
		k.anchorRootLocked(r, RootTypePartial, r.view.LastStepHash)
		k.emit("run_pending_decision_mismatch", runID, req.StepID, map[string]interface{}{
			"decision_id": req.DecisionRef.DecisionID,
		}, now)
		return AdvanceRunResponse{}, kernelErr(409, "RUN_PENDING_DECISION_MISMATCH", "decision reference missing", "pending_decision_mismatch")
	}
	decisionRef, ok := k.decisionPort.GetDecisionReference(req.DecisionRef.DecisionID)
	if !ok {
		r.view.State = StateSafeguardHold
		r.view.UpdatedAt = now
		k.anchorRootLocked(r, RootTypePartial, r.view.LastStepHash)
		k.emit("run_pending_decision_mismatch", runID, req.StepID, map[string]interface{}{
			"decision_id": req.DecisionRef.DecisionID,
		}, now)
		return AdvanceRunResponse{}, kernelErr(409, "RUN_PENDING_DECISION_MISMATCH", "decision reference missing", "pending_decision_mismatch")
	}
	if !k.decisionPort.IsDecisionConfirmedWithOwner(req.DecisionRef.DecisionID, runID, req.StepID, decisionRef.AttemptIndex, decisionRef.Phase) {
		r.view.State = StateSafeguardHold
		r.view.UpdatedAt = now
		k.anchorRootLocked(r, RootTypePartial, r.view.LastStepHash)
		k.emit("run_pending_decision_mismatch", runID, req.StepID, map[string]interface{}{
			"decision_id":   req.DecisionRef.DecisionID,
			"attempt_index": decisionRef.AttemptIndex,
			"phase":         decisionRef.Phase,
		}, now)
		return AdvanceRunResponse{}, kernelErr(409, "RUN_PENDING_DECISION_MISMATCH", "decision is not confirmed for run/step", "pending_decision_mismatch")
	}
	if !decisionRef.HashValid || strings.ToLower(strings.TrimSpace(decisionRef.HashKind)) != "stable" {
		r.view.State = StateSafeguardHold
		r.view.UpdatedAt = now
		k.anchorRootLocked(r, RootTypePartial, r.view.LastStepHash)
		k.emit("run_decision_hash_invalid", runID, req.StepID, map[string]interface{}{
			"decision_id": req.DecisionRef.DecisionID,
			"hash_kind":   decisionRef.HashKind,
			"hash_valid":  decisionRef.HashValid,
		}, now)
		return AdvanceRunResponse{}, kernelErr(409, "RUN_DECISION_HASH_INVALID", "decision hash is not stable/valid", "decision_hash_invalid")
	}
	if strings.TrimSpace(decisionRef.RunID) != "" && decisionRef.RunID != runID {
		r.view.State = StateSafeguardHold
		r.view.UpdatedAt = now
		k.anchorRootLocked(r, RootTypePartial, r.view.LastStepHash)
		k.emit("run_decision_binding_mismatch", runID, req.StepID, map[string]interface{}{
			"decision_id":      req.DecisionRef.DecisionID,
			"decision_run_id":  decisionRef.RunID,
			"incoming_run_id":  runID,
			"decision_step_id": decisionRef.StepID,
			"incoming_step_id": req.StepID,
		}, now)
		return AdvanceRunResponse{}, kernelErr(409, "RUN_DECISION_BINDING_MISMATCH", "decision binding mismatch", "decision_binding_mismatch")
	}
	if strings.TrimSpace(decisionRef.StepID) != "" && decisionRef.StepID != req.StepID {
		r.view.State = StateSafeguardHold
		r.view.UpdatedAt = now
		k.anchorRootLocked(r, RootTypePartial, r.view.LastStepHash)
		k.emit("run_decision_binding_mismatch", runID, req.StepID, map[string]interface{}{
			"decision_id":      req.DecisionRef.DecisionID,
			"decision_run_id":  decisionRef.RunID,
			"incoming_run_id":  runID,
			"decision_step_id": decisionRef.StepID,
			"incoming_step_id": req.StepID,
		}, now)
		return AdvanceRunResponse{}, kernelErr(409, "RUN_DECISION_BINDING_MISMATCH", "decision binding mismatch", "decision_binding_mismatch")
	}
	if req.DecisionRef.DecisionHashKind != "" && strings.ToLower(strings.TrimSpace(req.DecisionRef.DecisionHashKind)) != strings.ToLower(strings.TrimSpace(decisionRef.HashKind)) {
		r.view.State = StateSafeguardHold
		r.view.UpdatedAt = now
		k.anchorRootLocked(r, RootTypePartial, r.view.LastStepHash)
		k.emit("run_decision_hash_kind_mismatch", runID, req.StepID, map[string]interface{}{
			"decision_id":   req.DecisionRef.DecisionID,
			"expected_kind": req.DecisionRef.DecisionHashKind,
			"actual_kind":   decisionRef.HashKind,
		}, now)
		return AdvanceRunResponse{}, kernelErr(409, "RUN_DECISION_HASH_KIND_MISMATCH", "decision hash kind mismatch", "decision_hash_kind_mismatch")
	}
	if decisionRef.DecisionHash != req.DecisionRef.DecisionHash {
		r.view.State = StateSafeguardHold
		r.view.UpdatedAt = now
		k.anchorRootLocked(r, RootTypePartial, r.view.LastStepHash)
		k.emit("run_decision_hash_mismatch", runID, req.StepID, map[string]interface{}{
			"decision_id": req.DecisionRef.DecisionID,
		}, now)
		return AdvanceRunResponse{}, kernelErr(409, "RUN_DECISION_HASH_MISMATCH", "decision hash mismatch", "decision_hash_mismatch")
	}
	if !isExecutableDecision(decisionRef.Decision) {
		r.view.State = StateSafeguardHold
		r.view.UpdatedAt = now
		k.anchorRootLocked(r, RootTypePartial, r.view.LastStepHash)
		k.emit("run_decision_not_executable", runID, req.StepID, map[string]interface{}{
			"decision_id":    req.DecisionRef.DecisionID,
			"decision_value": decisionRef.Decision,
		}, now)
		return AdvanceRunResponse{}, kernelErr(409, "RUN_DECISION_NOT_EXECUTABLE", "decision is not executable for run advance", "decision_not_executable")
	}
	if ok, reasons := k.decisionPort.ValidateExecutionReceiptForRun(
		runID,
		req.StepID,
		r.view.TenantID,
		req.ExecutionReceiptRef,
		req.ExecutionUsedResources,
		req.ExecutionReceiptAt,
	); !ok {
		r.view.State = StateSafeguardHold
		r.view.UpdatedAt = now
		k.anchorRootLocked(r, RootTypePartial, r.view.LastStepHash)
		k.emit("run_execution_receipt_rejected", runID, req.StepID, map[string]interface{}{
			"decision_id":              req.DecisionRef.DecisionID,
			"execution_receipt_ref":    req.ExecutionReceiptRef,
			"execution_used_resources": req.ExecutionUsedResources,
			"ticket_validation_pass":   false,
			"reason_codes":             reasons,
		}, now)
		if len(reasons) == 0 {
			reasons = []string{"execution_receipt_ticket_invalid"}
		}
		return AdvanceRunResponse{}, kernelErr(409, "RUN_EXECUTION_RECEIPT_REJECTED", "execution receipt/ticket validation failed", reasons...)
	}

	if err := k.enforceDecisionObligationsLocked(r, req, decisionRef, now); err != nil {
		r.view.State = StateSafeguardHold
		r.view.UpdatedAt = now
		k.anchorRootLocked(r, RootTypePartial, r.view.LastStepHash)
		return AdvanceRunResponse{}, err
	}

	nextState := normalizeState(req.NextState)
	if nextState == "" {
		nextState = StateRunning
	}
	if !isValidState(nextState) {
		return AdvanceRunResponse{}, kernelErr(422, "RUN_INPUT_INVALID", "invalid next_state", "invalid_next_state")
	}
	if !isAllowedTransition(r.view.State, nextState) {
		return AdvanceRunResponse{}, kernelErr(409, "RUN_STATE_TRANSITION_FORBIDDEN", "transition not allowed", "state_transition_forbidden")
	}

	if strings.TrimSpace(req.InputHash) == "" || strings.TrimSpace(req.OutputHash) == "" {
		return AdvanceRunResponse{}, kernelErr(422, "RUN_INPUT_INVALID", "input_hash/output_hash required", "hash_input_missing")
	}

	prev := r.view.LastStepHash
	stepHash, ok := hashJSONWithStatus(struct {
		PreviousStepHash string `json:"previous_step_hash"`
		DecisionHash     string `json:"decision_hash"`
		InputHash        string `json:"input_hash"`
		OutputHash       string `json:"output_hash"`
	}{
		PreviousStepHash: prev,
		DecisionHash:     req.DecisionRef.DecisionHash,
		InputHash:        req.InputHash,
		OutputHash:       req.OutputHash,
	})
	if !ok || isErrorHash(stepHash) {
		r.view.State = StateSafeguardHold
		r.view.UpdatedAt = now
		k.anchorRootLocked(r, RootTypePartial, r.view.LastStepHash)
		k.emit("run_hash_generation_failed", runID, req.StepID, map[string]interface{}{
			"target": "step_hash",
		}, now)
		return AdvanceRunResponse{}, kernelErr(500, "RUN_HASH_GENERATION_FAILED", "failed to hash step integrity", "hash_generation_failed")
	}

	integrity := StepIntegrity{
		RunID:                 runID,
		StepID:                req.StepID,
		StepSeqID:             req.IncomingStepSeqID,
		StepType:              StepTypeNormal,
		SyntheticType:         "",
		IntegrityChainVersion: r.view.IntegrityChainVersion,
		PreviousStepHash:      prev,
		DecisionHash:          req.DecisionRef.DecisionHash,
		InputHash:             req.InputHash,
		OutputHash:            req.OutputHash,
		StepHash:              stepHash,
		CreatedAt:             now,
	}
	k.stepIntegrity[runID] = append(k.stepIntegrity[runID], integrity)

	r.view.State = nextState
	r.view.ActiveStepID = req.StepID
	r.view.StateVersion++
	r.view.CurrentStepSeqID = req.IncomingStepSeqID
	r.view.LastStepHash = stepHash
	r.view.UpdatedAt = now
	r.view.LastProgressAt = now
	r.noProgressScans = 0

	r.view.TerminalReason = ""
	r.view.TerminalSource = ""
	k.anchorByStateLocked(r, nextState, stepHash)
	if isTerminalState(nextState) {
		r.view.TerminalReason = "advance_terminal_state"
		r.view.TerminalSource = "advance"
	}
	k.emit("run.advanced", runID, req.StepID, map[string]interface{}{
		"new_state":          nextState,
		"state_version":      r.view.StateVersion,
		"step_seq_id":        req.IncomingStepSeqID,
		"step_hash":          stepHash,
		"decision_id":        req.DecisionRef.DecisionID,
		"decision_hash":      req.DecisionRef.DecisionHash,
		"decision_hash_kind": decisionRef.HashKind,
	}, now)

	resp := AdvanceRunResponse{
		NewState:                nextState,
		NextAction:              nextActionByState(nextState),
		StateVersion:            r.view.StateVersion,
		StepHash:                stepHash,
		RunIntegrityRoot:        r.view.RunIntegrityRoot,
		RunIntegrityRootVersion: r.view.RunIntegrityRootVersion,
		IntegrityRootType:       r.view.IntegrityRootType,
		CurrentStepSeqID:        r.view.CurrentStepSeqID,
	}
	k.idempotency[idemKey] = idempotencyEntry{
		RunID:        runID,
		Response:     resp,
		CreatedAt:    now,
		LastAccessAt: now,
	}
	if isTerminalState(nextState) {
		k.cleanupRunScopedCachesLocked(runID)
	}
	return resp, nil
}

func (k *Kernel) ParkRun(runID string, req ParkRunRequest) (ParkRunResponse, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	r, err := k.getRunLocked(runID)
	if err != nil {
		return ParkRunResponse{}, err
	}
	now := k.cfg.Clock()

	if isTerminalState(r.view.State) {
		return ParkRunResponse{}, kernelErr(409, "RUN_TERMINAL_STATE", "run is terminal", "terminal_state")
	}
	if req.ExpectedStateVersion != r.view.StateVersion {
		return ParkRunResponse{}, kernelErr(409, "RUN_STATE_VERSION_CONFLICT", "expected_state_version mismatch", "state_version_conflict")
	}
	if req.IncomingStepSeqID <= r.view.CurrentStepSeqID {
		k.emitProgressMonotonicityViolation(runID, req.StepID, req.IncomingStepSeqID, r.view.CurrentStepSeqID, now)
		return ParkRunResponse{}, kernelErr(409, "RUN_PROGRESS_MONOTONICITY_VIOLATION", "incoming_step_seq_id must be strictly increasing", "progress_monotonicity_violation")
	}

	tokenType := strings.ToLower(strings.TrimSpace(req.TokenType))
	if tokenType == "" {
		tokenType = TokenTypePark
	}
	if !isValidTokenType(tokenType) {
		return ParkRunResponse{}, kernelErr(422, "RUN_INPUT_INVALID", "invalid token_type", "invalid_token_type")
	}
	if strings.TrimSpace(req.StepID) == "" {
		return ParkRunResponse{}, kernelErr(422, "RUN_INPUT_INVALID", "step_id required", "step_id_missing")
	}
	newState := StateParked
	if tokenType == TokenTypeApproval {
		newState = StateAwaitingApproval
	}
	if !isAllowedTransition(r.view.State, newState) {
		return ParkRunResponse{}, kernelErr(409, "RUN_STATE_TRANSITION_FORBIDDEN", "transition not allowed", "state_transition_forbidden")
	}

	ttl := k.cfg.ContinuationTTL
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	expiresAt := now.Add(ttl)
	tokenHashInput, ok := hashJSONWithStatus(struct {
		RunID     string `json:"run_id"`
		StepID    string `json:"step_id"`
		StepSeqID int64  `json:"step_seq_id"`
		TokenType string `json:"token_type"`
		RequestTS int64  `json:"request_ts"`
	}{
		RunID: runID, StepID: req.StepID, StepSeqID: req.IncomingStepSeqID, TokenType: tokenType, RequestTS: now.UnixNano(),
	})
	if !ok || isErrorHash(tokenHashInput) {
		return ParkRunResponse{}, kernelErr(500, "RUN_HASH_GENERATION_FAILED", "failed to hash continuation token", "hash_generation_failed")
	}
	token := "ct_" + tokenHashInput[:20]
	tokenHash, ok := hashJSONWithStatus(token)
	if !ok || isErrorHash(tokenHash) {
		return ParkRunResponse{}, kernelErr(500, "RUN_HASH_GENERATION_FAILED", "failed to hash continuation token ref", "hash_generation_failed")
	}

	continuation := &Continuation{
		ContinuationID: token,
		RunID:          runID,
		StepID:         req.StepID,
		StepSeqID:      req.IncomingStepSeqID,
		TokenType:      tokenType,
		TokenHash:      tokenHash,
		ExpiresAt:      expiresAt,
		Status:         "active",
		CreatedAt:      now,
	}

	stepHash, err := k.appendSyntheticStepLocked(r, req.StepID, req.IncomingStepSeqID, "park", req.ParkReason, "parked:"+tokenType, now)
	if err != nil {
		return ParkRunResponse{}, err
	}
	k.continuations[token] = continuation
	r.view.State = newState
	r.view.ActiveStepID = req.StepID
	r.view.StateVersion++
	r.view.CurrentStepSeqID = req.IncomingStepSeqID
	r.view.LastStepHash = stepHash
	r.view.UpdatedAt = now
	r.view.LastProgressAt = now
	r.view.TerminalReason = ""
	r.view.TerminalSource = ""
	r.noProgressScans = 0
	k.anchorRootLocked(r, RootTypePartial, stepHash)

	parkEventType := "run.parked"
	if newState == StateAwaitingApproval {
		parkEventType = "run.awaiting_approval"
	}
	k.emit(parkEventType, runID, req.StepID, map[string]interface{}{
		"continuation_id": token,
		"token_type":      tokenType,
		"expires_at":      expiresAt,
		"step_seq_id":     req.IncomingStepSeqID,
		"new_state":       newState,
	}, now)

	return ParkRunResponse{
		RunID:                runID,
		NewState:             newState,
		StateVersion:         r.view.StateVersion,
		ContinuationToken:    token,
		ContinuationType:     tokenType,
		ExpiresAt:            expiresAt,
		PartialIntegrityRoot: r.view.RootAnchors.PartialRoot,
	}, nil
}

func (k *Kernel) ResumeRun(runID string, req ResumeRunRequest) (ResumeRunResponse, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	r, err := k.getRunLocked(runID)
	if err != nil {
		return ResumeRunResponse{}, err
	}
	now := k.cfg.Clock()

	if strings.TrimSpace(req.ContinuationToken) == "" {
		return ResumeRunResponse{}, kernelErr(422, "RUN_INPUT_INVALID", "continuation_token required", "continuation_token_missing")
	}
	c, ok := k.continuations[req.ContinuationToken]
	if !ok {
		return ResumeRunResponse{}, kernelErr(410, "RUN_CONTINUATION_EXPIRED_OR_MISSING", "continuation not found", "continuation_missing")
	}
	if c.RunID != runID {
		return ResumeRunResponse{}, kernelErr(409, "RUN_CONTINUATION_RUN_MISMATCH", "continuation run mismatch", "continuation_run_mismatch")
	}
	if c.Status == "consumed" {
		return ResumeRunResponse{}, kernelErr(409, "RUN_CONTINUATION_CONSUMED", "continuation already consumed", "continuation_consumed")
	}
	if now.After(c.ExpiresAt) {
		c.Status = "expired"
		return ResumeRunResponse{}, kernelErr(410, "RUN_CONTINUATION_EXPIRED", "continuation expired", "continuation_expired")
	}
	if strings.TrimSpace(req.ExpectedSnapshotHash) != "" && req.ExpectedSnapshotHash != r.view.Snapshot.SnapshotHash {
		return ResumeRunResponse{}, kernelErr(412, "RUN_SNAPSHOT_MISMATCH", "snapshot mismatch on resume", "snapshot_mismatch")
	}
	if req.IncomingStepSeqID <= r.view.CurrentStepSeqID {
		k.emitProgressMonotonicityViolation(runID, c.StepID, req.IncomingStepSeqID, r.view.CurrentStepSeqID, now)
		return ResumeRunResponse{}, kernelErr(409, "RUN_PROGRESS_MONOTONICITY_VIOLATION", "incoming_step_seq_id must be strictly increasing", "progress_monotonicity_violation")
	}
	if c.StepSeqID != r.view.CurrentStepSeqID {
		return ResumeRunResponse{}, kernelErr(409, "RUN_CONTINUATION_SEQ_MISMATCH", "continuation step_seq mismatch", "continuation_seq_mismatch")
	}
	if r.view.State != StateParked && r.view.State != StateAwaitingApproval {
		return ResumeRunResponse{}, kernelErr(409, "RUN_STATE_CONFLICT", "run is not resumable state", "state_conflict")
	}
	if !tokenTypeMatchesRunState(c.TokenType, r.view.State) {
		return ResumeRunResponse{}, kernelErr(409, "RUN_CONTINUATION_TYPE_STATE_MISMATCH", "continuation token type does not match run state", "continuation_type_state_mismatch")
	}

	stepHash, err := k.appendSyntheticStepLocked(r, c.StepID, req.IncomingStepSeqID, "resume", req.ResumeReason, req.ReceiptRef, now)
	if err != nil {
		return ResumeRunResponse{}, err
	}

	r.view.State = StateRunning
	r.view.ActiveStepID = c.StepID
	r.view.StateVersion++
	r.view.CurrentStepSeqID = req.IncomingStepSeqID
	r.view.UpdatedAt = now
	r.view.LastProgressAt = now
	r.view.LastStepHash = stepHash
	r.view.TerminalReason = ""
	r.view.TerminalSource = ""
	r.noProgressScans = 0
	k.anchorRootLocked(r, RootTypePartial, stepHash)

	c.Status = "consumed"
	c.ConsumedAt = &now

	k.emit("run.resumed", runID, c.StepID, map[string]interface{}{
		"continuation_id": c.ContinuationID,
		"resume_reason":   req.ResumeReason,
		"step_seq_id":     req.IncomingStepSeqID,
	}, now)

	return ResumeRunResponse{
		ResumeStatus: "resumed",
		NewState:     r.view.State,
		StateVersion: r.view.StateVersion,
	}, nil
}

func (k *Kernel) AbortRun(runID string, req AbortRunRequest) (RunView, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	r, err := k.getRunLocked(runID)
	if err != nil {
		return RunView{}, err
	}
	now := k.cfg.Clock()
	if isTerminalState(r.view.State) {
		return r.view, nil
	}

	nextSeq := r.view.CurrentStepSeqID + 1
	stepHash, appendErr := k.appendSyntheticStepLocked(r, r.view.ActiveStepID, nextSeq, "abort", req.Reason, "aborted", now)
	if appendErr != nil {
		return RunView{}, appendErr
	}
	r.view.State = StateAborted
	r.view.StateVersion++
	r.view.CurrentStepSeqID = nextSeq
	r.view.LastStepHash = stepHash
	r.view.UpdatedAt = now
	r.view.LastProgressAt = now
	r.view.TerminalReason = strings.TrimSpace(req.Reason)
	if r.view.TerminalReason == "" {
		r.view.TerminalReason = "manual_abort"
	}
	r.view.TerminalSource = "abort_api"
	k.anchorRootLocked(r, RootTypeAborted, stepHash)
	k.cleanupRunScopedCachesLocked(runID)
	k.emit("run.aborted", runID, r.view.ActiveStepID, map[string]interface{}{
		"reason": req.Reason,
	}, now)
	return r.view, nil
}

func (k *Kernel) GetRun(runID string) (RunView, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	r, err := k.getRunLocked(runID)
	if err != nil {
		return RunView{}, err
	}
	return r.view, nil
}

func (k *Kernel) FindActiveContinuation(runID, stepID, tokenType string) (Continuation, bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	runID = strings.TrimSpace(runID)
	stepID = strings.TrimSpace(stepID)
	tokenType = strings.ToLower(strings.TrimSpace(tokenType))
	if runID == "" {
		return Continuation{}, false
	}
	now := k.cfg.Clock()
	var chosen *Continuation
	for _, c := range k.continuations {
		if c == nil {
			continue
		}
		if c.RunID != runID {
			continue
		}
		if stepID != "" && c.StepID != stepID {
			continue
		}
		if tokenType != "" && c.TokenType != tokenType {
			continue
		}
		if c.Status != "active" {
			continue
		}
		if now.After(c.ExpiresAt) {
			continue
		}
		if chosen == nil ||
			c.StepSeqID > chosen.StepSeqID ||
			(c.StepSeqID == chosen.StepSeqID && c.CreatedAt.After(chosen.CreatedAt)) {
			cp := *c
			chosen = &cp
		}
	}
	if chosen == nil {
		return Continuation{}, false
	}
	return *chosen, true
}

func (k *Kernel) Outbox() []KernelEvent {
	k.mu.RLock()
	defer k.mu.RUnlock()
	cp := make([]KernelEvent, len(k.outbox))
	copy(cp, k.outbox)
	return cp
}

func (k *Kernel) DrainOutbox(limit int) []KernelEvent {
	k.mu.Lock()
	defer k.mu.Unlock()
	if limit <= 0 || len(k.outbox) == 0 {
		return nil
	}
	if limit > len(k.outbox) {
		limit = len(k.outbox)
	}
	out := make([]KernelEvent, limit)
	copy(out, k.outbox[:limit])
	k.outbox = k.outbox[limit:]
	if len(k.outbox) == 0 {
		k.outbox = k.outbox[:0]
	}
	return out
}

func (k *Kernel) StepIntegrities(runID string) []StepIntegrity {
	k.mu.RLock()
	defer k.mu.RUnlock()
	values := k.stepIntegrity[runID]
	cp := make([]StepIntegrity, len(values))
	copy(cp, values)
	return cp
}

func (k *Kernel) FlagRunRisk(runID string, securityRisk bool, resourceRisk bool) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	r, err := k.getRunLocked(runID)
	if err != nil {
		return err
	}
	r.securityRisk = securityRisk
	r.resourceRisk = resourceRisk
	return nil
}

func (k *Kernel) SweepTTL(now time.Time) SweepResult {
	k.mu.Lock()
	defer k.mu.Unlock()

	result := SweepResult{}
	for _, r := range sortedRuns(k.runs) {
		if result.Scanned >= k.cfg.SweepBatchLimit {
			break
		}
		result.Scanned++
		if isTerminalState(r.view.State) {
			continue
		}
		if r.maxLifetime <= 0 {
			continue
		}
		if now.Sub(r.view.CreatedAt) <= r.maxLifetime {
			continue
		}
		if now.Sub(r.view.LastProgressAt) <= k.cfg.ZombieNoProgressWindow && !r.securityRisk && !r.resourceRisk {
			continue
		}

		nextSeq := r.view.CurrentStepSeqID + 1
		if r.securityRisk || r.resourceRisk {
			stepHash, err := k.appendSyntheticStepLocked(r, r.view.ActiveStepID, nextSeq, "ttl_force_abort", "ttl_breach_risk", "", now)
			if err != nil {
				result.SkippedIntegrityErrors++
				continue
			}
			r.view.State = StateForceAbort
			r.view.StateVersion++
			r.view.CurrentStepSeqID = nextSeq
			r.view.LastStepHash = stepHash
			r.view.UpdatedAt = now
			r.view.TerminalReason = "ttl_breach_risk"
			r.view.TerminalSource = "ttl_sweeper"
			k.anchorRootLocked(r, RootTypeAborted, stepHash)
			k.cleanupRunScopedCachesLocked(r.view.RunID)
			result.ForceAbortCount++
			k.emit("run_ttl_force_abort", r.view.RunID, r.view.ActiveStepID, map[string]interface{}{
				"risk_tier": r.view.RiskTier,
			}, now)
			continue
		}

		stepHash, err := k.appendSyntheticStepLocked(r, r.view.ActiveStepID, nextSeq, "ttl_force_review", "ttl_breach", "", now)
		if err != nil {
			result.SkippedIntegrityErrors++
			continue
		}
		r.view.State = StateForceReviewRequired
		r.view.StateVersion++
		r.view.CurrentStepSeqID = nextSeq
		r.view.LastStepHash = stepHash
		r.view.UpdatedAt = now
		r.view.TerminalReason = "ttl_breach"
		r.view.TerminalSource = "ttl_sweeper"
		k.anchorRootLocked(r, RootTypeReviewHold, stepHash)
		result.ForceReviewRequiredCount++
		k.emit("run_ttl_force_review_required", r.view.RunID, r.view.ActiveStepID, map[string]interface{}{
			"risk_tier": r.view.RiskTier,
		}, now)
	}
	k.applyMaintenanceLocked(now, &result)
	return result
}

func (k *Kernel) SweepZombie(now time.Time) SweepResult {
	k.mu.Lock()
	defer k.mu.Unlock()

	result := SweepResult{}
	for _, r := range sortedRuns(k.runs) {
		if result.Scanned >= k.cfg.SweepBatchLimit {
			break
		}
		result.Scanned++
		if isTerminalState(r.view.State) {
			continue
		}
		if now.Sub(r.view.LastProgressAt) <= k.cfg.ZombieNoProgressWindow {
			r.noProgressScans = 0
			continue
		}
		r.noProgressScans++
		if r.noProgressScans < 2 {
			continue
		}

		nextSeq := r.view.CurrentStepSeqID + 1
		var nextState string
		var rootType string
		if r.securityRisk || r.resourceRisk {
			nextState = StateForceAbort
			rootType = RootTypeAborted
			result.ForceAbortCount++
		} else {
			nextState = StateForceReviewRequired
			rootType = RootTypeReviewHold
			result.ForceReviewRequiredCount++
		}

		stepHash, err := k.appendSyntheticStepLocked(r, r.view.ActiveStepID, nextSeq, "zombie_sweep", "no_progress", "", now)
		if err != nil {
			result.SkippedIntegrityErrors++
			continue
		}
		r.view.State = nextState
		r.view.StateVersion++
		r.view.CurrentStepSeqID = nextSeq
		r.view.LastStepHash = stepHash
		r.view.UpdatedAt = now
		r.view.TerminalReason = "no_progress"
		r.view.TerminalSource = "zombie_sweeper"
		k.anchorRootLocked(r, rootType, stepHash)
		if nextState == StateForceAbort {
			k.cleanupRunScopedCachesLocked(r.view.RunID)
		}
		result.ZombieTerminatedCount++
		k.emit("run_zombie_terminated", r.view.RunID, r.view.ActiveStepID, map[string]interface{}{
			"new_state": nextState,
		}, now)
	}
	k.applyMaintenanceLocked(now, &result)
	return result
}

func (k *Kernel) MaintenanceSweep(now time.Time) SweepResult {
	k.mu.Lock()
	defer k.mu.Unlock()
	result := SweepResult{}
	k.applyMaintenanceLocked(now, &result)
	return result
}

func (k *Kernel) getRunLocked(runID string) (*runInstance, error) {
	r, ok := k.runs[runID]
	if !ok {
		return nil, kernelErr(404, "RUN_NOT_FOUND", "run not found", "run_not_found")
	}
	return r, nil
}

func (k *Kernel) appendSyntheticStepLocked(r *runInstance, stepID string, seq int64, syntheticType, inputHash, outputHash string, now time.Time) (string, error) {
	prev := r.view.LastStepHash
	stepHash, ok := hashJSONWithStatus(struct {
		PreviousStepHash string `json:"previous_step_hash"`
		SyntheticType    string `json:"synthetic_type"`
		InputHash        string `json:"input_hash"`
		OutputHash       string `json:"output_hash"`
	}{
		PreviousStepHash: prev,
		SyntheticType:    syntheticType,
		InputHash:        inputHash,
		OutputHash:       outputHash,
	})
	if !ok || isErrorHash(stepHash) {
		k.emit("run_hash_generation_failed", r.view.RunID, stepID, map[string]interface{}{
			"target": "synthetic_step_hash",
		}, now)
		return "", kernelErr(500, "RUN_HASH_GENERATION_FAILED", "failed to hash synthetic step", "hash_generation_failed")
	}
	k.stepIntegrity[r.view.RunID] = append(k.stepIntegrity[r.view.RunID], StepIntegrity{
		RunID:                 r.view.RunID,
		StepID:                stepID,
		StepSeqID:             seq,
		StepType:              StepTypeSynthetic,
		SyntheticType:         strings.ToLower(strings.TrimSpace(syntheticType)),
		IntegrityChainVersion: r.view.IntegrityChainVersion,
		PreviousStepHash:      prev,
		DecisionHash:          "",
		InputHash:             inputHash,
		OutputHash:            outputHash,
		StepHash:              stepHash,
		CreatedAt:             now,
	})
	return stepHash, nil
}

func (k *Kernel) maxLifetimeForRisk(risk string) time.Duration {
	if v, ok := k.cfg.RunMaxLifetime[normalizeRisk(risk)]; ok {
		return v
	}
	return k.cfg.RunMaxLifetime[RiskMedium]
}

func (k *Kernel) anchorByStateLocked(r *runInstance, state string, stepHash string) {
	switch state {
	case StateRunning:
		k.anchorRootLocked(r, RootTypePartial, stepHash)
	case StateCompleted:
		k.anchorRootLocked(r, RootTypeCompleted, stepHash)
	case StateParked, StateAwaitingApproval:
		k.anchorRootLocked(r, RootTypePartial, stepHash)
	case StateReconciling:
		k.anchorRootLocked(r, RootTypeReconciled, stepHash)
	case StateForceReviewRequired:
		k.anchorRootLocked(r, RootTypeReviewHold, stepHash)
	case StateForceAbort, StateAborted:
		k.anchorRootLocked(r, RootTypeAborted, stepHash)
	}
}

// anchorRootLocked is the single authoritative mutation point for run integrity
// root view fields. Any root update must flow through this function so these
// four fields stay consistent in one place:
//   - RootAnchors
//   - RunIntegrityRoot
//   - RunIntegrityRootVersion
//   - IntegrityRootType
func (k *Kernel) anchorRootLocked(r *runInstance, rootType string, root string) {
	root = strings.TrimSpace(root)
	if root == "" {
		return
	}
	// Versioning semantics: bump only when root hash value changes.
	// Root type transitions with the same hash update type/anchors but keep version.
	if root != r.view.RunIntegrityRoot {
		r.view.RunIntegrityRoot = root
		r.view.RunIntegrityRootVersion++
	}
	r.view.IntegrityRootType = rootType
	switch rootType {
	case RootTypeCompleted:
		r.view.RootAnchors.CompletedRoot = root
	case RootTypePartial:
		r.view.RootAnchors.PartialRoot = root
	case RootTypeReconciled:
		r.view.RootAnchors.ReconciledRoot = root
	case RootTypeReviewHold:
		r.view.RootAnchors.ReviewHoldRoot = root
	case RootTypeAborted:
		r.view.RootAnchors.AbortedRoot = root
	}
}

func (k *Kernel) emit(eventType string, runID string, stepID string, payload map[string]interface{}, now time.Time) {
	seq := atomic.AddUint64(&k.eventSeq, 1)
	payloadHash, payloadHashValid := hashJSONWithStatus(payload)
	if !payloadHashValid || isErrorHash(payloadHash) {
		payloadHash = ""
		payloadHashValid = false
	}
	evidenceGrade, payloadIntegrityRequired := runEventEvidencePolicy(eventType)
	e := KernelEvent{
		EventID:                  fmt.Sprintf("evt_run_seq_%d", seq),
		EventType:                eventType,
		SchemaVersion:            1,
		SourceComponent:          "run_kernel",
		EvidenceGrade:            evidenceGrade,
		PayloadIntegrityRequired: payloadIntegrityRequired,
		RunID:                    runID,
		StepID:                   stepID,
		EventTS:                  now,
		PayloadRef:               fmt.Sprintf("inline://run_outbox/%d", seq),
		PayloadHash:              payloadHash,
		PayloadHashValid:         payloadHashValid,
		Payload:                  payload,
	}
	k.outbox = append(k.outbox, e)
	k.cleanupOutboxLocked(now)
}

func runEventEvidencePolicy(eventType string) (grade string, payloadIntegrityRequired bool) {
	switch strings.TrimSpace(eventType) {
	case "irreversible_progress_violation_event",
		"run_decision_hash_invalid",
		"run_decision_binding_mismatch",
		"run_decision_hash_kind_mismatch",
		"run_decision_hash_mismatch",
		"run_decision_not_executable",
		"run_execution_receipt_rejected",
		"run_hash_generation_failed",
		"run_obligation_limit_violation",
		"run_obligation_template_missing",
		"run_obligation_unsupported",
		"run_obligation_phase_mismatch",
		"run_obligation_deny_execution",
		"run_obligation_audit_event":
		return "audit", true
	default:
		return "operational", false
	}
}

func (k *Kernel) emitProgressMonotonicityViolation(runID, stepID string, incoming, current int64, now time.Time) {
	payload := map[string]interface{}{
		"incoming_step_seq_id": incoming,
		"current_step_seq_id":  current,
		"severity":             "P1",
	}
	// Backward compatibility for existing consumers.
	k.emit("progress_monotonicity_violation", runID, stepID, payload, now)
	// Canonical event name required by the design doc gate.
	k.emit("irreversible_progress_violation_event", runID, stepID, payload, now)
}

func kernelErr(status int, code string, msg string, reasons ...string) error {
	return &KernelError{
		StatusCode:  status,
		Code:        code,
		Message:     msg,
		ReasonCodes: append([]string(nil), reasons...),
	}
}

func (k *Kernel) enforceDecisionObligationsLocked(r *runInstance, req AdvanceRunRequest, ref DecisionReference, now time.Time) error {
	if len(ref.Obligations) == 0 {
		return nil
	}
	if r.view.AttachedTags == nil {
		r.view.AttachedTags = make(map[string]string)
	}
	phase := strings.ToUpper(strings.TrimSpace(ref.Phase))
	if phase == "" {
		phase = "PRE_TOOL"
	}

	for idx, ob := range ref.Obligations {
		obType := strings.ToLower(strings.TrimSpace(ob.Type))
		if obType == "" {
			continue
		}
		obPhase := strings.ToUpper(strings.TrimSpace(ob.Phase))
		if obPhase == "" {
			obPhase = phase
		}
		if !isObligationPhaseAllowed(phase, obPhase) {
			k.emit("run_obligation_phase_mismatch", r.view.RunID, req.StepID, map[string]interface{}{
				"decision_id":      ref.DecisionID,
				"obligation_type":  obType,
				"obligation_phase": obPhase,
				"run_phase":        phase,
				"index":            idx,
			}, now)
			return kernelErr(409, "RUN_OBLIGATION_PHASE_MISMATCH", "obligation phase not allowed for advance", "obligation_phase_mismatch")
		}

		switch obType {
		case "deny_execution":
			k.emit("run_obligation_deny_execution", r.view.RunID, req.StepID, map[string]interface{}{
				"decision_id":       ref.DecisionID,
				"obligation_type":   obType,
				"obligation_target": ob.Target,
				"obligation_value":  ob.Value,
				"index":             idx,
			}, now)
			return kernelErr(409, "RUN_OBLIGATION_DENY_EXECUTION", "deny_execution obligation blocks run advance", "obligation_deny_execution")
		case "limit_param":
			if err := enforceLimitParamObligation(req, ob); err != nil {
				k.emit("run_obligation_limit_violation", r.view.RunID, req.StepID, map[string]interface{}{
					"decision_id":       ref.DecisionID,
					"obligation_type":   obType,
					"obligation_target": ob.Target,
					"obligation_value":  ob.Value,
					"index":             idx,
					"error":             err.Error(),
				}, now)
				return kernelErr(409, "RUN_OBLIGATION_LIMIT_PARAM_VIOLATION", "limit_param obligation violated", "obligation_limit_param_violation")
			}
		case "attach_tag":
			key := strings.TrimSpace(ob.Target)
			if key == "" {
				key = fmt.Sprintf("obligation_tag_%d", idx)
			}
			r.view.AttachedTags[key] = strings.TrimSpace(ob.Value)
			k.emit("run_obligation_tag_attached", r.view.RunID, req.StepID, map[string]interface{}{
				"decision_id": ref.DecisionID,
				"tag":         key,
				"value":       ob.Value,
				"index":       idx,
			}, now)
		case "require_template":
			templateID := strings.TrimSpace(ob.Value)
			if templateID == "" {
				templateID = strings.TrimSpace(ob.Target)
			}
			if templateID != "" && !receiptRefHasTemplateEvidence(req.ExecutionReceiptRef, templateID) {
				k.emit("run_obligation_template_missing", r.view.RunID, req.StepID, map[string]interface{}{
					"decision_id":          ref.DecisionID,
					"template_id":          templateID,
					"receipt_ref":          req.ExecutionReceiptRef,
					"resolved_templates":   sortedTemplateEvidenceIDs(req.ExecutionReceiptRef),
					"proof_match_required": true,
					"index":                idx,
				}, now)
				return kernelErr(409, "RUN_OBLIGATION_TEMPLATE_REQUIRED", "required template evidence missing in execution_receipt_ref", "obligation_template_required")
			}
		case "emit_audit":
			k.emit("run_obligation_audit_event", r.view.RunID, req.StepID, map[string]interface{}{
				"decision_id":       ref.DecisionID,
				"obligation_type":   obType,
				"obligation_target": ob.Target,
				"obligation_value":  ob.Value,
				"index":             idx,
			}, now)
		default:
			k.emit("run_obligation_unsupported", r.view.RunID, req.StepID, map[string]interface{}{
				"decision_id":     ref.DecisionID,
				"obligation_type": obType,
				"index":           idx,
			}, now)
			return kernelErr(409, "RUN_OBLIGATION_UNSUPPORTED_TYPE", "unsupported obligation type", "obligation_unsupported")
		}
	}
	return nil
}

func enforceLimitParamObligation(req AdvanceRunRequest, ob DecisionObligation) error {
	target := strings.ToLower(strings.TrimSpace(ob.Target))
	value := strings.TrimSpace(ob.Value)
	n, ok := parseLimitValue(value)
	if !ok {
		return fmt.Errorf("invalid limit value")
	}
	switch target {
	case "max_input_hash_len":
		if len(req.InputHash) > n {
			return fmt.Errorf("input_hash exceeds limit")
		}
	case "max_output_hash_len":
		if len(req.OutputHash) > n {
			return fmt.Errorf("output_hash exceeds limit")
		}
	case "max_step_seq_id":
		if req.IncomingStepSeqID > int64(n) {
			return fmt.Errorf("step_seq_id exceeds limit")
		}
	default:
		return fmt.Errorf("unsupported limit target")
	}
	return nil
}

func parseLimitValue(v string) (int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if strings.Contains(v, ":") {
		parts := strings.Split(v, ":")
		v = strings.TrimSpace(parts[len(parts)-1])
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func receiptRefHasTemplateEvidence(receiptRef, templateID string) bool {
	templateID = strings.TrimSpace(templateID)
	if templateID == "" {
		return true
	}
	_, ok := extractTemplateEvidenceIDs(receiptRef)[templateID]
	return ok
}

func sortedTemplateEvidenceIDs(receiptRef string) []string {
	ids := extractTemplateEvidenceIDs(receiptRef)
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func extractTemplateEvidenceIDs(receiptRef string) map[string]struct{} {
	out := map[string]struct{}{}
	ref := strings.TrimSpace(receiptRef)
	if ref == "" {
		return out
	}

	parsed, err := url.Parse(ref)
	if err != nil {
		return out
	}

	if strings.EqualFold(strings.TrimSpace(parsed.Scheme), "template") {
		addTemplateEvidenceCandidate(out, parsed.Host)
		addTemplateEvidenceCandidate(out, parsed.Path)
	}
	addTemplateEvidenceFromPath(out, parsed.Path)

	q := parsed.Query()
	for _, key := range []string{"template", "template_id", "templateId"} {
		for _, v := range q[key] {
			addTemplateEvidenceCandidate(out, v)
		}
	}
	return out
}

func addTemplateEvidenceFromPath(out map[string]struct{}, path string) {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i := 0; i < len(segs)-1; i++ {
		seg := strings.ToLower(strings.TrimSpace(segs[i]))
		if seg == "template" || seg == "templates" {
			addTemplateEvidenceCandidate(out, segs[i+1])
		}
	}
}

func addTemplateEvidenceCandidate(out map[string]struct{}, v string) {
	candidate := strings.Trim(v, "/")
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return
	}
	if strings.Contains(candidate, "/") {
		parts := strings.Split(candidate, "/")
		candidate = strings.TrimSpace(parts[len(parts)-1])
	}
	if candidate == "" {
		return
	}
	out[candidate] = struct{}{}
}

func isExecutableDecision(decision string) bool {
	d := strings.ToLower(strings.TrimSpace(decision))
	if d == "" {
		return true
	}
	switch d {
	case "allow", "require_approval":
		return true
	default:
		return false
	}
}

func isObligationPhaseAllowed(runPhase, obligationPhase string) bool {
	rp := strings.ToUpper(strings.TrimSpace(runPhase))
	op := strings.ToUpper(strings.TrimSpace(obligationPhase))
	switch rp {
	case "PRE_CONTEXT":
		return op == "PRE_CONTEXT"
	case "PRE_TOOL":
		return op == "PRE_CONTEXT" || op == "PRE_TOOL"
	default:
		return false
	}
}

func normalizeRisk(risk string) string {
	return strings.ToLower(strings.TrimSpace(risk))
}

func normalizeState(state string) string {
	s := strings.ToUpper(strings.TrimSpace(state))
	if s == "" {
		return ""
	}
	return s
}

func isValidRisk(risk string) bool {
	switch normalizeRisk(risk) {
	case RiskLow, RiskMedium, RiskHigh, RiskCritical:
		return true
	default:
		return false
	}
}

func isValidTokenType(tokenType string) bool {
	switch strings.ToLower(strings.TrimSpace(tokenType)) {
	case TokenTypePark, TokenTypeApproval, TokenTypeCallback:
		return true
	default:
		return false
	}
}

func tokenTypeMatchesRunState(tokenType, runState string) bool {
	t := strings.ToLower(strings.TrimSpace(tokenType))
	s := normalizeState(runState)
	switch t {
	case TokenTypeApproval:
		return s == StateAwaitingApproval
	case TokenTypePark, TokenTypeCallback:
		return s == StateParked
	default:
		return false
	}
}

func isValidState(state string) bool {
	switch normalizeState(state) {
	case StateRunning, StateParked, StateAwaitingApproval, StateReconciling, StateCompleted, StateFailed, StateSafeguardHold, StateForceReviewRequired, StateForceAbort, StateAborted:
		return true
	default:
		return false
	}
}

func isTerminalState(state string) bool {
	switch normalizeState(state) {
	case StateCompleted, StateFailed, StateForceAbort, StateAborted:
		return true
	default:
		return false
	}
}

func isAllowedTransition(from string, to string) bool {
	f := normalizeState(from)
	t := normalizeState(to)

	if f == t {
		return true
	}
	switch f {
	case StateRunning:
		switch t {
		case StateRunning, StateParked, StateAwaitingApproval, StateReconciling, StateCompleted, StateFailed, StateSafeguardHold, StateForceReviewRequired, StateForceAbort, StateAborted:
			return true
		}
	case StateReconciling:
		switch t {
		case StateRunning, StateCompleted, StateFailed, StateForceReviewRequired, StateForceAbort:
			return true
		}
	case StateParked:
		return t == StateRunning || t == StateForceReviewRequired || t == StateForceAbort
	case StateAwaitingApproval:
		return t == StateRunning || t == StateForceReviewRequired || t == StateForceAbort
	case StateSafeguardHold:
		return t == StateForceReviewRequired || t == StateForceAbort || t == StateAborted
	}
	return false
}

func nextActionByState(state string) string {
	switch normalizeState(state) {
	case StateRunning:
		return "continue"
	case StateParked:
		return "wait_resume"
	case StateAwaitingApproval:
		return "wait_approval"
	case StateReconciling:
		return "reconcile"
	case StateCompleted:
		return "done"
	case StateForceReviewRequired:
		return "manual_review"
	case StateForceAbort, StateAborted:
		return "stop"
	default:
		return "halt"
	}
}

func sortedRuns(in map[string]*runInstance) []*runInstance {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*runInstance, 0, len(keys))
	for _, k := range keys {
		out = append(out, in[k])
	}
	return out
}

func hashJSON(v interface{}) string {
	hash, ok := hashJSONWithStatus(v)
	if !ok || isErrorHash(hash) {
		return ""
	}
	return hash
}

func hashJSONWithStatus(v interface{}) (string, bool) {
	raw, err := marshalJSON(v)
	if err != nil {
		marker := fmt.Sprintf("json_marshal_error:%T:%v", v, err)
		sum := sha256.Sum256([]byte(marker))
		return "err_" + hex.EncodeToString(sum[:]), false
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), true
}

func isErrorHash(v string) bool {
	return strings.HasPrefix(strings.TrimSpace(v), "err_")
}

func (k *Kernel) cleanupRunScopedCachesLocked(runID string) {
	for key, entry := range k.idempotency {
		if entry.RunID == runID {
			delete(k.idempotency, key)
		}
	}
	for token, c := range k.continuations {
		if c.RunID == runID {
			delete(k.continuations, token)
		}
	}
	k.cleanupCreateRunsForRunIDLocked(runID)
}

func (k *Kernel) applyMaintenanceLocked(now time.Time, result *SweepResult) {
	deletedCont := k.cleanupContinuationsLocked(now)
	deletedIdem := k.cleanupIdempotencyLocked(now)
	deletedRuns := k.cleanupTerminalRunsLocked(now)
	deletedCreate := k.cleanupCreateRunsLocked(now)
	trimmedOutbox := k.cleanupOutboxLocked(now)
	if result != nil {
		result.DeletedContinuations += deletedCont
		result.DeletedIdempotency += deletedIdem
		result.DeletedRuns += deletedRuns
		result.DeletedCreateRequests += deletedCreate
		result.OutboxTrimmed += trimmedOutbox
	}
}

func (k *Kernel) cleanupContinuationsLocked(now time.Time) int {
	deleted := 0
	for token, c := range k.continuations {
		if c == nil {
			delete(k.continuations, token)
			deleted++
			continue
		}
		if c.Status == "active" && now.After(c.ExpiresAt) {
			c.Status = "expired"
		}
		switch c.Status {
		case "consumed":
			if c.ConsumedAt != nil && now.After(c.ConsumedAt.Add(k.cfg.ContinuationRetention)) {
				delete(k.continuations, token)
				deleted++
			}
		case "expired":
			if now.After(c.ExpiresAt.Add(k.cfg.ContinuationRetention)) {
				delete(k.continuations, token)
				deleted++
			}
		case "active":
			if now.After(c.ExpiresAt.Add(k.cfg.ContinuationRetention)) {
				delete(k.continuations, token)
				deleted++
			}
		}
	}
	return deleted
}

func (k *Kernel) cleanupIdempotencyLocked(now time.Time) int {
	deleted := 0
	for key, entry := range k.idempotency {
		anchor := entry.LastAccessAt
		if anchor.IsZero() {
			anchor = entry.CreatedAt
		}
		if k.cfg.IdempotencyTTL > 0 && now.After(anchor.Add(k.cfg.IdempotencyTTL)) {
			delete(k.idempotency, key)
			deleted++
		}
	}
	return deleted
}

func (k *Kernel) cleanupTerminalRunsLocked(now time.Time) (deletedRuns int) {
	if k.cfg.RunRetention <= 0 {
		return 0
	}
	for runID, r := range k.runs {
		if !isTerminalState(r.view.State) {
			continue
		}
		if !now.After(r.view.UpdatedAt.Add(k.cfg.RunRetention)) {
			continue
		}
		delete(k.runs, runID)
		delete(k.stepIntegrity, runID)
		k.cleanupRunScopedCachesLocked(runID)
		deletedRuns++
	}
	return deletedRuns
}

func (k *Kernel) cleanupCreateRunsLocked(now time.Time) int {
	deleted := 0
	for key, entry := range k.createRuns {
		anchor := entry.LastAccessAt
		if anchor.IsZero() {
			anchor = entry.CreatedAt
		}
		if k.cfg.CreateRequestRetention > 0 && now.After(anchor.Add(k.cfg.CreateRequestRetention)) {
			delete(k.createRuns, key)
			deleted++
		}
	}
	return deleted
}

func (k *Kernel) cleanupCreateRunsForRunIDLocked(runID string) int {
	deleted := 0
	for key, entry := range k.createRuns {
		if entry.RunID == runID {
			delete(k.createRuns, key)
			deleted++
		}
	}
	return deleted
}

func (k *Kernel) cleanupOutboxLocked(now time.Time) int {
	trimmed := 0
	if k.cfg.OutboxRetention > 0 {
		cut := 0
		for cut < len(k.outbox) {
			if now.Sub(k.outbox[cut].EventTS) <= k.cfg.OutboxRetention {
				break
			}
			cut++
		}
		if cut > 0 {
			k.outbox = k.outbox[cut:]
			trimmed += cut
		}
	}
	if overflow := len(k.outbox) - k.cfg.OutboxMaxEvents; overflow > 0 {
		k.outbox = k.outbox[overflow:]
		trimmed += overflow
	}
	if trimmed > 0 {
		atomic.AddUint64(&k.outboxTrimmed, uint64(trimmed))
	}
	return trimmed
}
