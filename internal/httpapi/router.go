package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"agent/infra/internal/decision"
	"agent/infra/internal/evidence"
	"agent/infra/internal/run"
)

type Router struct {
	decisionSvc *decision.Kernel
	runSvc      *run.Kernel
	evidenceSvc *evidence.Kernel
	mux         *http.ServeMux
	syncMu      sync.Mutex
	seenEventID map[string]struct{}
	seenQueue   []string
	seenCap     int
}

type contextResolveResponse struct {
	CompiledContextRef string `json:"compiled_context_ref"`
	FrozenInputHash    string `json:"frozen_input_hash"`
	SelectionRationale string `json:"selection_rationale"`
}

func NewRouter() http.Handler {
	decisionSvc := decision.NewKernel(decision.Config{})
	runSvc := run.NewKernel(run.Config{}, decisionRunPort{svc: decisionSvc})
	decisionSvc.SetApprovalRunUnlockPort(decisionApprovalRunUnlockPort{runSvc: runSvc})
	decisionSvc.SetRunStatePort(decisionRunStatePort{runSvc: runSvc})
	evidenceSvc := evidence.NewKernel(evidence.Config{})
	return NewRouterWithKernels(decisionSvc, runSvc, evidenceSvc)
}

func NewRouterWithService(svc *decision.Kernel) http.Handler {
	runSvc := run.NewKernel(run.Config{}, decisionRunPort{svc: svc})
	svc.SetApprovalRunUnlockPort(decisionApprovalRunUnlockPort{runSvc: runSvc})
	svc.SetRunStatePort(decisionRunStatePort{runSvc: runSvc})
	evidenceSvc := evidence.NewKernel(evidence.Config{})
	return NewRouterWithKernels(svc, runSvc, evidenceSvc)
}

func NewRouterWithServices(decisionSvc *decision.Kernel, runSvc *run.Kernel) http.Handler {
	evidenceSvc := evidence.NewKernel(evidence.Config{})
	return NewRouterWithKernels(decisionSvc, runSvc, evidenceSvc)
}

func NewRouterWithKernels(decisionSvc *decision.Kernel, runSvc *run.Kernel, evidenceSvc *evidence.Kernel) http.Handler {
	if decisionSvc != nil && runSvc != nil {
		decisionSvc.SetApprovalRunUnlockPort(decisionApprovalRunUnlockPort{runSvc: runSvc})
		decisionSvc.SetRunStatePort(decisionRunStatePort{runSvc: runSvc})
	}
	r := &Router{
		decisionSvc: decisionSvc,
		runSvc:      runSvc,
		evidenceSvc: evidenceSvc,
		mux:         http.NewServeMux(),
		seenEventID: make(map[string]struct{}),
		seenQueue:   make([]string, 0, 256),
		seenCap:     20000,
	}
	r.routes()
	return r.mux
}

type decisionRunPort struct {
	svc *decision.Kernel
}

type decisionApprovalRunUnlockPort struct {
	runSvc *run.Kernel
}

type decisionRunStatePort struct {
	runSvc *run.Kernel
}

func (p decisionRunPort) IsDecisionConfirmed(decisionID, runID, stepID string) bool {
	if p.svc == nil {
		return false
	}
	return p.svc.IsDecisionConfirmed(decisionID, runID, stepID)
}

func (p decisionRunPort) IsDecisionConfirmedWithOwner(decisionID, runID, stepID string, attemptIndex int, phase string) bool {
	if p.svc == nil {
		return false
	}
	return p.svc.IsDecisionConfirmedWithOwner(decisionID, runID, stepID, attemptIndex, phase)
}

func (p decisionRunPort) GetDecisionReference(decisionID string) (run.DecisionReference, bool) {
	if p.svc == nil {
		return run.DecisionReference{}, false
	}
	ref, ok := p.svc.GetDecisionReference(decisionID)
	if !ok {
		return run.DecisionReference{}, false
	}
	obligations := make([]run.DecisionObligation, 0, len(ref.Obligations))
	for _, ob := range ref.Obligations {
		obligations = append(obligations, run.DecisionObligation{
			Type:       ob.Type,
			Target:     ob.Target,
			Value:      ob.Value,
			Phase:      ob.Phase,
			Strictness: ob.Strictness,
		})
	}
	return run.DecisionReference{
		DecisionID:   ref.DecisionID,
		RunID:        ref.RunID,
		StepID:       ref.StepID,
		AttemptIndex: ref.AttemptIndex,
		Phase:        ref.Phase,
		Decision:     ref.Decision,
		Obligations:  obligations,
		DecisionHash: ref.DecisionHashRef.Value,
		HashValid:    ref.DecisionHashRef.Valid,
		HashKind:     ref.DecisionHashRef.Kind,
	}, true
}

func (p decisionRunPort) ValidateExecutionReceiptForRun(runID, stepID, tenantID, executionReceiptRef string, usedResources map[string]float64, receivedAt time.Time) (bool, []string) {
	if p.svc == nil {
		return false, []string{"decision_kernel_unavailable"}
	}
	return p.svc.ValidateExecutionReceiptForRun(runID, stepID, tenantID, executionReceiptRef, usedResources, receivedAt)
}

func (p decisionRunStatePort) GetRunState(runID string) (decision.RunStateReference, bool) {
	if p.runSvc == nil {
		return decision.RunStateReference{}, false
	}
	view, err := p.runSvc.GetRun(runID)
	if err != nil {
		return decision.RunStateReference{}, false
	}
	return decision.RunStateReference{
		RunID:        view.RunID,
		State:        view.State,
		StateVersion: view.StateVersion,
		UpdatedAt:    view.UpdatedAt,
	}, true
}

func (p decisionApprovalRunUnlockPort) DispatchApprovalRunUnlock(signal decision.ApprovalRunUnlockSignal) (decision.ApprovalRunUnlockDispatchResult, error) {
	if p.runSvc == nil {
		return decision.ApprovalRunUnlockDispatchResult{DispatchStatus: "dispatch_failed"}, errors.New("run kernel unavailable")
	}
	outcome := strings.ToLower(strings.TrimSpace(signal.Outcome))
	if outcome != "approved" {
		// Non-approved outcomes are terminalized at Decision side and do not require resume.
		unblockedAt := signal.ResolvedAt
		if unblockedAt.IsZero() {
			unblockedAt = time.Now().UTC()
		}
		return decision.ApprovalRunUnlockDispatchResult{
			DispatchStatus:            "not_required",
			BusinessActionUnblocked:   true,
			BusinessActionUnblockedAt: unblockedAt,
		}, nil
	}
	cont, ok := p.runSvc.FindActiveContinuation(signal.RunID, signal.StepID, run.TokenTypeApproval)
	if !ok {
		return decision.ApprovalRunUnlockDispatchResult{DispatchStatus: "dispatch_failed"}, fmt.Errorf("approval continuation not found for run=%s step=%s", signal.RunID, signal.StepID)
	}
	view, err := p.runSvc.GetRun(signal.RunID)
	if err != nil {
		return decision.ApprovalRunUnlockDispatchResult{DispatchStatus: "dispatch_failed"}, err
	}
	_, err = p.runSvc.ResumeRun(signal.RunID, run.ResumeRunRequest{
		ContinuationToken:    cont.ContinuationID,
		ResumeReason:         "approval_approved",
		ReceiptRef:           "decision://approval_unlock/" + signal.CaseID,
		IncomingStepSeqID:    view.CurrentStepSeqID + 1,
		ExpectedSnapshotHash: view.Snapshot.SnapshotHash,
	})
	if err != nil {
		return decision.ApprovalRunUnlockDispatchResult{DispatchStatus: "dispatch_failed"}, err
	}
	updated, getErr := p.runSvc.GetRun(signal.RunID)
	if getErr != nil {
		return decision.ApprovalRunUnlockDispatchResult{DispatchStatus: "dispatch_failed"}, getErr
	}
	unblockedAt := updated.UpdatedAt
	if unblockedAt.IsZero() {
		unblockedAt = time.Now().UTC()
	}
	return decision.ApprovalRunUnlockDispatchResult{
		DispatchStatus:            "dispatched",
		BusinessActionUnblocked:   true,
		BusinessActionUnblockedAt: unblockedAt,
	}, nil
}

func (r *Router) routes() {
	r.mux.HandleFunc("/healthz", r.healthz)
	r.mux.HandleFunc("/v1/context/resolve", r.contextResolve)
	r.mux.HandleFunc("/v1/decision/evaluate-runtime", r.evaluateRuntime)
	r.mux.HandleFunc("/v1/decision/evaluate-schedule-admission", r.evaluateScheduleAdmission)
	r.mux.HandleFunc("/v1/decision/evaluate-release", r.evaluateRelease)
	r.mux.HandleFunc("/v1/decision/confirm-run-advance", r.confirmRunAdvance)
	r.mux.HandleFunc("/v1/decision/repair-pending", r.repairPending)
	r.mux.HandleFunc("/v1/decision/terminalize-pending", r.terminalizePending)
	r.mux.HandleFunc("/v1/approval/cases", r.createApprovalCase)
	r.mux.HandleFunc("/v1/approval/cases/", r.decideApprovalCase)
	r.mux.HandleFunc("/v1/decision/", r.getDecision)
	r.mux.HandleFunc("/v1/decision/outbox", r.outbox)
	r.mux.HandleFunc("/v1/metrics/decision", r.metrics)
	r.mux.HandleFunc("/v1/features/definitions", r.createFeatureDefinition)
	r.mux.HandleFunc("/v1/features/signal-contracts", r.featureSignalContracts)
	r.mux.HandleFunc("/v1/features/signal-contracts/scheduler/run", r.runFeatureSignalContractScheduler)
	r.mux.HandleFunc("/v1/features/signal-contracts/validate", r.validateFeatureSignalContract)
	r.mux.HandleFunc("/v1/features/signal-contracts/history", r.featureSignalContractHistory)
	r.mux.HandleFunc("/v1/features/signal-contracts/rollback", r.rollbackFeatureSignalContract)
	r.mux.HandleFunc("/v1/features/snapshots/build", r.buildFeatureSnapshot)
	r.mux.HandleFunc("/v1/features/snapshots/validate-freshness", r.validateFeatureSnapshotFreshness)
	r.mux.HandleFunc("/v1/features/snapshots/", r.featureSnapshotAction)
	r.mux.HandleFunc("/v1/features/", r.featureAction)
	r.mux.HandleFunc("/v1/approval/org-health", r.getApprovalOrgHealth)
	r.mux.HandleFunc("/v1/approval/org-health/recompute", r.recomputeApprovalOrgHealth)
	r.mux.HandleFunc("/v1/approval/org-health/remediation", r.remediateApprovalOrgHealth)
	r.mux.HandleFunc("/v1/approval/org-health/reports", r.getApprovalOrgHealthReports)
	r.mux.HandleFunc("/v1/metrics/enforcement-matrix", r.getEnforcementMatrix)
	r.mux.HandleFunc("/v1/metrics/enforcement-matrix/validate", r.validateEnforcementMatrix)
	r.mux.HandleFunc("/v1/metrics/enforcement-matrix/publish", r.publishEnforcementMatrix)
	r.mux.HandleFunc("/v1/evidence/events/ingest", r.ingestEvidenceEvent)
	r.mux.HandleFunc("/v1/evidence/sync-kernel-outboxes", r.syncEvidenceKernelOutboxes)
	r.mux.HandleFunc("/v1/evidence/dsar/delete", r.deleteEvidenceByDSAR)
	r.mux.HandleFunc("/v1/evidence/integrity-verify", r.verifyEvidenceIntegrity)
	r.mux.HandleFunc("/v1/evidence/integrity-anchor", r.evidenceIntegrityAnchor)
	r.mux.HandleFunc("/v1/evidence/archive/summary", r.getEvidenceArchiveSummary)
	r.mux.HandleFunc("/v1/evidence/archive/export", r.exportEvidenceArchive)
	r.mux.HandleFunc("/v1/evidence/decision-logs/", r.getEvidenceDecisionLogHistory)
	r.mux.HandleFunc("/v1/evidence/runs/", r.getRunEvidenceSummary)
	r.mux.HandleFunc("/v1/ledger/runs/", r.getRunLedger)
	r.mux.HandleFunc("/v1/ledger/aggregate", r.aggregateLedger)

	r.mux.HandleFunc("/v1/runs", r.createRun)
	r.mux.HandleFunc("/v1/runs/sweep-ttl", r.sweepRunTTL)
	r.mux.HandleFunc("/v1/runs/sweep-zombie", r.sweepRunZombie)
	r.mux.HandleFunc("/v1/runs/maintenance", r.sweepRunMaintenance)
	r.mux.HandleFunc("/v1/runs/", r.runAction)
}

func (r *Router) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (r *Router) contextResolve(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input decision.RuntimeDecisionRequest
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, contextResolveResponse{
		CompiledContextRef: "ctx_compiled_" + input.RequestID,
		FrozenInputHash:    hashMap(input.Freeze.Frozen),
		SelectionRationale: "minimal_context_profile",
	})
}

func (r *Router) evaluateRuntime(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input decision.RuntimeDecisionRequest
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	out := r.decisionSvc.EvaluateRuntime(input)
	r.syncEvidenceFromKernelOutboxes()
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) evaluateScheduleAdmission(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input decision.ScheduleAdmissionRequest
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	out := r.decisionSvc.EvaluateScheduleAdmission(input)
	r.syncEvidenceFromKernelOutboxes()
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) evaluateRelease(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input decision.ReleaseDecisionRequest
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	out := r.decisionSvc.EvaluateRelease(input)
	r.syncEvidenceFromKernelOutboxes()
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) confirmRunAdvance(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input decision.ConfirmRunAdvanceRequest
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	out := r.decisionSvc.ConfirmRunAdvance(input)
	r.syncEvidenceFromKernelOutboxes()
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) repairPending(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	now := time.Now()
	out := r.decisionSvc.RepairPendingDecisions(now)
	r.syncEvidenceFromKernelOutboxes()
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) terminalizePending(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input decision.TerminalizePendingDecisionRequest
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	out := r.decisionSvc.TerminalizePendingDecision(input)
	r.syncEvidenceFromKernelOutboxes()
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) createApprovalCase(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input decision.ApprovalCreateRequest
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	out := r.decisionSvc.CreateApprovalCase(input)
	r.syncEvidenceFromKernelOutboxes()
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) decideApprovalCase(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// /v1/approval/cases/{case_id}/decision
	path := strings.TrimPrefix(req.URL.Path, "/v1/approval/cases/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[1] != "decision" || parts[0] == "" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	var input decision.ApprovalDecisionRequest
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	out := r.decisionSvc.DecideApprovalCase(parts[0], input)
	r.syncEvidenceFromKernelOutboxes()
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) getDecision(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.Trim(strings.TrimPrefix(req.URL.Path, "/v1/decision/"), "/")
	if path == "" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	parts := strings.Split(path, "/")
	switch {
	case len(parts) == 1 && parts[0] != "":
		data, ok := r.decisionSvc.GetDecision(parts[0])
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, data)
		return
	case len(parts) == 2 && parts[0] != "" && parts[1] == "history":
		history, ok := r.decisionSvc.GetDecisionHistory(parts[0])
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"decision_id": parts[0],
			"history":     history,
		})
		return
	case len(parts) == 2 && parts[0] != "" && parts[1] == "reference":
		ref, ok := r.decisionSvc.GetDecisionReference(parts[0])
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, ref)
		return
	case len(parts) == 3 && parts[0] != "" && parts[1] == "versions":
		version, err := strconv.Atoi(parts[2])
		if err != nil || version <= 0 {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		d, ok := r.decisionSvc.GetDecisionByVersion(parts[0], version)
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, d)
		return
	default:
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
}

func (r *Router) metrics(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, r.decisionSvc.MetricsSnapshot(time.Now()))
}

func (r *Router) outbox(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"events": r.decisionSvc.Outbox(),
	})
}

func (r *Router) createFeatureDefinition(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodPost:
		var input decision.FeatureDefinitionCreateRequest
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		out, err := r.decisionSvc.CreateFeatureDefinition(input)
		if err != nil {
			writeDecisionError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodGet:
		out, err := r.decisionSvc.ListFeatureDefinitions(scopeFromQuery(req))
		if err != nil {
			writeDecisionError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"definitions": out})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (r *Router) featureSignalContracts(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		risk := strings.TrimSpace(req.URL.Query().Get("risk_tier"))
		phase := strings.TrimSpace(req.URL.Query().Get("phase"))
		scope := scopeFromQuery(req)
		hasScope := strings.TrimSpace(scope.OrgID) != "" || strings.TrimSpace(scope.WorkspaceID) != "" || strings.TrimSpace(scope.ProjectID) != ""
		if risk != "" || phase != "" {
			out, ok := r.decisionSvc.GetFeatureSignalContractForScope(risk, phase, scope)
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			writeJSON(w, http.StatusOK, out)
			return
		}
		contracts := r.decisionSvc.ListFeatureSignalContracts()
		if hasScope {
			filtered := make([]decision.FeatureSignalContractView, 0, len(contracts))
			norm := decision.ScopeRef{
				OrgID:       strings.TrimSpace(scope.OrgID),
				WorkspaceID: strings.TrimSpace(scope.WorkspaceID),
				ProjectID:   strings.TrimSpace(scope.ProjectID),
			}
			for _, c := range contracts {
				if strings.TrimSpace(norm.OrgID) != "" && !strings.EqualFold(strings.TrimSpace(c.Scope.OrgID), strings.TrimSpace(norm.OrgID)) {
					continue
				}
				if strings.TrimSpace(norm.WorkspaceID) != "" && !strings.EqualFold(strings.TrimSpace(c.Scope.WorkspaceID), strings.TrimSpace(norm.WorkspaceID)) {
					continue
				}
				if strings.TrimSpace(norm.ProjectID) != "" && !strings.EqualFold(strings.TrimSpace(c.Scope.ProjectID), strings.TrimSpace(norm.ProjectID)) {
					continue
				}
				filtered = append(filtered, c)
			}
			contracts = filtered
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"contracts": contracts,
		})
	case http.MethodPost:
		var input decision.FeatureSignalContractPublishRequest
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		out, err := r.decisionSvc.PublishFeatureSignalContract(input)
		if err != nil {
			writeDecisionError(w, err)
			return
		}
		r.syncEvidenceFromKernelOutboxes()
		writeJSON(w, http.StatusOK, out)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (r *Router) runFeatureSignalContractScheduler(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		At time.Time `json:"at,omitempty"`
	}
	if req.Body != nil {
		_ = json.NewDecoder(req.Body).Decode(&input)
	}
	out := r.decisionSvc.RunFeatureSignalContractScheduler(input.At)
	r.syncEvidenceFromKernelOutboxes()
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) validateFeatureSignalContract(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input decision.FeatureSignalContractPublishRequest
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, r.decisionSvc.ValidateFeatureSignalContract(input))
}

func (r *Router) featureSignalContractHistory(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	risk := strings.TrimSpace(req.URL.Query().Get("risk_tier"))
	phase := strings.TrimSpace(req.URL.Query().Get("phase"))
	if risk == "" {
		http.Error(w, "invalid query: risk_tier required", http.StatusBadRequest)
		return
	}
	limit := 20
	if raw := strings.TrimSpace(req.URL.Query().Get("limit")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			http.Error(w, "invalid query: limit", http.StatusBadRequest)
			return
		}
		limit = v
	}
	scope := scopeFromQuery(req)
	history, ok := r.decisionSvc.GetFeatureSignalContractHistoryForScope(risk, phase, scope, limit)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	rollbacks, _ := r.decisionSvc.ListFeatureSignalContractRollbacksForScope(risk, phase, scope, limit)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"history":   history,
		"rollbacks": rollbacks,
	})
}

func (r *Router) rollbackFeatureSignalContract(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input decision.FeatureSignalContractRollbackRequest
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	out, err := r.decisionSvc.RollbackFeatureSignalContract(input)
	if err != nil {
		writeDecisionError(w, err)
		return
	}
	r.syncEvidenceFromKernelOutboxes()
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) buildFeatureSnapshot(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input decision.FeatureSnapshotBuildRequest
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	out, err := r.decisionSvc.BuildFeatureSnapshot(input)
	if err != nil {
		writeDecisionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) validateFeatureSnapshotFreshness(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input decision.FeatureSnapshotFreshnessValidateRequest
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	out, err := r.decisionSvc.ValidateFeatureSnapshotFreshness(input)
	if err != nil {
		writeDecisionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) featureSnapshotAction(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(req.URL.Path, "/v1/features/snapshots/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	snapshotID := parts[0]
	switch {
	case len(parts) == 1:
		out, ok := r.decisionSvc.GetFeatureSnapshot(snapshotID)
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	case len(parts) == 2 && parts[1] == "evidence":
		out, ok := r.decisionSvc.GetFeatureSnapshotEvidence(snapshotID)
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	default:
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
}

func (r *Router) featureAction(w http.ResponseWriter, req *http.Request) {
	path := strings.TrimPrefix(req.URL.Path, "/v1/features/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	featureID := parts[0]
	action := parts[1]
	switch action {
	case "versions":
		switch req.Method {
		case http.MethodPost:
			var input decision.FeatureVersionCreateRequest
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			out, err := r.decisionSvc.CreateFeatureVersion(featureID, input)
			if err != nil {
				writeDecisionError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, out)
		case http.MethodGet:
			out, err := r.decisionSvc.ListFeatureVersions(featureID, scopeFromQuery(req))
			if err != nil {
				writeDecisionError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"versions": out})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "drift-report":
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		out, err := r.decisionSvc.GetFeatureDriftReport(featureID, scopeFromQuery(req))
		if err != nil {
			writeDecisionError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	case "rollback":
		switch req.Method {
		case http.MethodPost:
			var input decision.FeatureRollbackRequest
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			out, err := r.decisionSvc.RollbackFeature(featureID, input)
			if err != nil {
				writeDecisionError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, out)
		case http.MethodGet:
			limit := 20
			if raw := strings.TrimSpace(req.URL.Query().Get("limit")); raw != "" {
				v, err := strconv.Atoi(raw)
				if err != nil || v <= 0 {
					http.Error(w, "invalid query: limit", http.StatusBadRequest)
					return
				}
				limit = v
			}
			out, err := r.decisionSvc.GetFeatureRollbackHistory(featureID, scopeFromQuery(req), limit)
			if err != nil {
				writeDecisionError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"rollbacks": out})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "publish":
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var input decision.FeatureVersionPublishRequest
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		out, err := r.decisionSvc.PublishFeatureVersion(featureID, input)
		if err != nil {
			writeDecisionError(w, err)
			return
		}
		r.syncEvidenceFromKernelOutboxes()
		writeJSON(w, http.StatusOK, out)
	case "dependency-graph":
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		out, err := r.decisionSvc.GetFeatureDependencyGraph(featureID, scopeFromQuery(req))
		if err != nil {
			writeDecisionError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	default:
		http.Error(w, "invalid path", http.StatusBadRequest)
	}
}

func (r *Router) getApprovalOrgHealth(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	out, err := r.decisionSvc.GetApprovalOrgHealth(scopeFromQuery(req))
	if err != nil {
		writeDecisionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) remediateApprovalOrgHealth(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input decision.ApprovalOrgHealthRemediationRequest
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	out, err := r.decisionSvc.RemediateApprovalOrgHealth(input)
	if err != nil {
		writeDecisionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) recomputeApprovalOrgHealth(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input decision.ApprovalOrgHealthRecomputeRequest
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	out, err := r.decisionSvc.RecomputeApprovalOrgHealth(input)
	if err != nil {
		writeDecisionError(w, err)
		return
	}
	r.syncEvidenceFromKernelOutboxes()
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) getApprovalOrgHealthReports(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 20
	if raw := strings.TrimSpace(req.URL.Query().Get("limit")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			http.Error(w, "invalid query: limit", http.StatusBadRequest)
			return
		}
		limit = v
	}
	out, err := r.decisionSvc.GetApprovalOrgHealthReports(scopeFromQuery(req), limit)
	if err != nil {
		writeDecisionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"reports": out})
}

func (r *Router) getEnforcementMatrix(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, r.decisionSvc.GetEnforcementMatrix())
}

func (r *Router) validateEnforcementMatrix(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input decision.EnforcementMatrixValidateRequest
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, r.decisionSvc.ValidateEnforcementMatrix(input))
}

func (r *Router) publishEnforcementMatrix(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input decision.EnforcementMatrixPublishRequest
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	out, err := r.decisionSvc.PublishEnforcementMatrix(input)
	if err != nil {
		writeDecisionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) createRun(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input run.CreateRunRequest
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	resp, err := r.runSvc.CreateRun(input)
	if err != nil {
		writeKernelError(w, err)
		return
	}
	r.syncEvidenceFromKernelOutboxes()
	writeJSON(w, http.StatusOK, resp)
}

func (r *Router) runAction(w http.ResponseWriter, req *http.Request) {
	path := strings.TrimPrefix(req.URL.Path, "/v1/runs/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	runID := parts[0]

	if len(parts) == 1 {
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		view, err := r.runSvc.GetRun(runID)
		if err != nil {
			writeKernelError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view)
		return
	}

	action := parts[1]
	switch action {
	case "decision-graph":
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.syncEvidenceFromKernelOutboxes()
		graph, ok := r.evidenceSvc.GetDecisionGraph(runID)
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, graph)
	case "root-cause-pack":
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		mode := evidence.RootCausePackMode(strings.TrimSpace(req.URL.Query().Get("mode")))
		if mode == "" {
			mode = evidence.RootCausePackModeMinimal
		}
		r.syncEvidenceFromKernelOutboxes()
		pack, err := r.evidenceSvc.BuildRootCausePack(runID, mode)
		if err != nil {
			writeEvidenceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, pack)
	case "replay-pack":
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		mode := evidence.ReplayPackMode(strings.TrimSpace(req.URL.Query().Get("mode")))
		if mode == "" {
			mode = evidence.ReplayPackModeMinimal
		}
		r.syncEvidenceFromKernelOutboxes()
		pack, err := r.evidenceSvc.BuildReplayPack(runID, mode)
		if err != nil {
			writeEvidenceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, pack)
	case "integrity-verify":
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.syncEvidenceFromKernelOutboxes()
		out, err := r.evidenceSvc.VerifyIntegrity(runID)
		if err != nil {
			writeEvidenceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	case "advance":
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var input run.AdvanceRunRequest
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		resp, err := r.runSvc.AdvanceRun(runID, input)
		if err != nil {
			writeKernelError(w, err)
			return
		}
		r.syncEvidenceFromKernelOutboxes()
		writeJSON(w, http.StatusOK, resp)
	case "park":
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var input run.ParkRunRequest
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		resp, err := r.runSvc.ParkRun(runID, input)
		if err != nil {
			writeKernelError(w, err)
			return
		}
		r.syncEvidenceFromKernelOutboxes()
		writeJSON(w, http.StatusOK, resp)
	case "resume":
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var input run.ResumeRunRequest
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		resp, err := r.runSvc.ResumeRun(runID, input)
		if err != nil {
			writeKernelError(w, err)
			return
		}
		r.syncEvidenceFromKernelOutboxes()
		writeJSON(w, http.StatusOK, resp)
	case "abort":
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var input run.AbortRunRequest
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		resp, err := r.runSvc.AbortRun(runID, input)
		if err != nil {
			writeKernelError(w, err)
			return
		}
		r.syncEvidenceFromKernelOutboxes()
		writeJSON(w, http.StatusOK, resp)
	default:
		http.Error(w, "invalid path", http.StatusBadRequest)
	}
}

func (r *Router) sweepRunTTL(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	out := r.runSvc.SweepTTL(time.Now())
	r.syncEvidenceFromKernelOutboxes()
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) sweepRunZombie(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	out := r.runSvc.SweepZombie(time.Now())
	r.syncEvidenceFromKernelOutboxes()
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) sweepRunMaintenance(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	out := r.runSvc.MaintenanceSweep(time.Now())
	r.syncEvidenceFromKernelOutboxes()
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) syncEvidenceFromKernelOutboxes() map[string]int {
	result := map[string]int{
		"seen_decision_events": 0,
		"seen_run_events":      0,
		"ingested":             0,
		"dropped":              0,
		"errors":               0,
	}
	for _, e := range r.decisionSvc.Outbox() {
		if !r.markEventSeen(e.EventID) {
			continue
		}
		result["seen_decision_events"]++
		runID := strings.TrimSpace(e.RunID)
		if runID == "" {
			runID = syntheticRunID("decision_kernel", e.DecisionID, e.EventID)
		}
		req := evidence.IngestEventRequest{
			EventID:                  e.EventID,
			EventType:                e.EventType,
			SchemaVersion:            e.SchemaVersion,
			SourceComponent:          e.SourceComponent,
			RunID:                    runID,
			StepID:                   e.StepID,
			DecisionID:               e.DecisionID,
			RiskTier:                 stringFromPayload(e.Payload, "risk_tier"),
			EvidenceTier:             tierFromGrade(e.EvidenceGrade),
			EvidenceGrade:            e.EvidenceGrade,
			PayloadIntegrityRequired: e.PayloadIntegrityRequired,
			EventTS:                  e.EventTS,
			PayloadRef:               e.PayloadRef,
			PayloadHash:              e.PayloadHash,
			PayloadHashValid:         e.PayloadHashValid,
			Payload:                  e.Payload,
			Usage:                    usageFromPayload(e.Payload),
		}
		resp, err := r.evidenceSvc.IngestEvent(req)
		if err != nil {
			result["errors"]++
			continue
		}
		if resp.Dropped {
			result["dropped"]++
		} else {
			result["ingested"]++
		}
	}
	for _, e := range r.runSvc.Outbox() {
		if !r.markEventSeen(e.EventID) {
			continue
		}
		result["seen_run_events"]++
		req := evidence.IngestEventRequest{
			EventID:                  e.EventID,
			EventType:                e.EventType,
			SchemaVersion:            e.SchemaVersion,
			SourceComponent:          e.SourceComponent,
			RunID:                    e.RunID,
			StepID:                   e.StepID,
			RiskTier:                 stringFromPayload(e.Payload, "risk_tier"),
			EvidenceTier:             tierFromGrade(e.EvidenceGrade),
			EvidenceGrade:            e.EvidenceGrade,
			PayloadIntegrityRequired: e.PayloadIntegrityRequired,
			EventTS:                  e.EventTS,
			PayloadRef:               e.PayloadRef,
			PayloadHash:              e.PayloadHash,
			PayloadHashValid:         e.PayloadHashValid,
			Payload:                  e.Payload,
			Usage:                    usageFromPayload(e.Payload),
		}
		resp, err := r.evidenceSvc.IngestEvent(req)
		if err != nil {
			result["errors"]++
			continue
		}
		if resp.Dropped {
			result["dropped"]++
		} else {
			result["ingested"]++
		}
	}
	return result
}

func (r *Router) markEventSeen(eventID string) bool {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return false
	}
	r.syncMu.Lock()
	defer r.syncMu.Unlock()
	if _, ok := r.seenEventID[eventID]; ok {
		return false
	}
	r.seenEventID[eventID] = struct{}{}
	r.seenQueue = append(r.seenQueue, eventID)
	for len(r.seenQueue) > r.seenCap {
		old := r.seenQueue[0]
		r.seenQueue = r.seenQueue[1:]
		delete(r.seenEventID, old)
	}
	return true
}

func syntheticRunID(source, primary, fallback string) string {
	primary = strings.TrimSpace(primary)
	if primary == "" {
		primary = strings.TrimSpace(fallback)
	}
	if primary == "" {
		primary = "na"
	}
	return source + "::" + primary
}

func tierFromGrade(grade string) string {
	switch strings.ToLower(strings.TrimSpace(grade)) {
	case "audit":
		return evidence.Tier0
	default:
		return evidence.Tier1
	}
}

func usageFromPayload(payload map[string]interface{}) *evidence.UsageInput {
	if payload == nil {
		return nil
	}
	resourceType := strings.TrimSpace(stringFromPayload(payload, "resource_type"))
	unit := strings.TrimSpace(stringFromPayload(payload, "unit"))
	if resourceType == "" || unit == "" {
		return nil
	}
	return &evidence.UsageInput{
		ResourceType: resourceType,
		UsageAmount:  floatFromPayload(payload, "usage_amount"),
		Unit:         unit,
		CostAmount:   floatFromPayload(payload, "cost_amount"),
	}
}

func stringFromPayload(payload map[string]interface{}, key string) string {
	if payload == nil {
		return ""
	}
	v, ok := payload[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	default:
		return ""
	}
}

func floatFromPayload(payload map[string]interface{}, key string) float64 {
	if payload == nil {
		return 0
	}
	v, ok := payload[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}

func (r *Router) ingestEvidenceEvent(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input evidence.IngestEventRequest
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	out, err := r.evidenceSvc.IngestEvent(input)
	if err != nil {
		writeEvidenceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) syncEvidenceKernelOutboxes(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, r.syncEvidenceFromKernelOutboxes())
}

func (r *Router) deleteEvidenceByDSAR(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input evidence.DSARDeleteRequest
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	out, err := r.evidenceSvc.DeleteByDSAR(input)
	if err != nil {
		writeEvidenceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) getRunEvidenceSummary(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(req.URL.Path, "/v1/evidence/runs/")
	runID := strings.Trim(path, "/")
	if runID == "" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	r.syncEvidenceFromKernelOutboxes()
	out, ok := r.evidenceSvc.GetRunEvidence(runID)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) getRunLedger(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(req.URL.Path, "/v1/ledger/runs/")
	runID := strings.Trim(path, "/")
	if runID == "" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	r.syncEvidenceFromKernelOutboxes()
	items, ok := r.evidenceSvc.GetLedger(runID)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"run_id": runID,
		"items":  items,
	})
}

func (r *Router) aggregateLedger(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input evidence.LedgerAggregateRequest
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	r.syncEvidenceFromKernelOutboxes()
	out, err := r.evidenceSvc.AggregateLedger(input)
	if err != nil {
		writeEvidenceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) verifyEvidenceIntegrity(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.syncEvidenceFromKernelOutboxes()
	writeJSON(w, http.StatusOK, r.evidenceSvc.VerifyGlobalIntegrity())
}

func (r *Router) evidenceIntegrityAnchor(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		limit := 20
		if raw := strings.TrimSpace(req.URL.Query().Get("limit")); raw != "" {
			v, err := strconv.Atoi(raw)
			if err != nil || v <= 0 {
				http.Error(w, "invalid query: limit", http.StatusBadRequest)
				return
			}
			limit = v
		}
		r.syncEvidenceFromKernelOutboxes()
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"anchors": r.evidenceSvc.ListGlobalIntegrityAnchors(limit),
		})
	case http.MethodPost:
		r.syncEvidenceFromKernelOutboxes()
		var input struct {
			Reason string `json:"reason"`
		}
		if req.Body != nil {
			_ = json.NewDecoder(req.Body).Decode(&input)
		}
		writeJSON(w, http.StatusOK, r.evidenceSvc.CreateGlobalIntegrityAnchor(strings.TrimSpace(input.Reason)))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (r *Router) getEvidenceArchiveSummary(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.syncEvidenceFromKernelOutboxes()
	runID := strings.TrimSpace(req.URL.Query().Get("run_id"))
	writeJSON(w, http.StatusOK, r.evidenceSvc.GetArchiveSummary(runID))
}

func (r *Router) exportEvidenceArchive(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	runID := strings.TrimSpace(req.URL.Query().Get("run_id"))
	tier := strings.TrimSpace(req.URL.Query().Get("tier"))
	limit := 0
	if raw := strings.TrimSpace(req.URL.Query().Get("limit")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			http.Error(w, "invalid query: limit", http.StatusBadRequest)
			return
		}
		limit = v
	}
	r.syncEvidenceFromKernelOutboxes()
	out, err := r.evidenceSvc.ExportArchive(runID, tier, limit)
	if err != nil {
		writeEvidenceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) getEvidenceDecisionLogHistory(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(req.URL.Path, "/v1/evidence/decision-logs/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "history" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	limit := 20
	if raw := strings.TrimSpace(req.URL.Query().Get("limit")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			http.Error(w, "invalid query: limit", http.StatusBadRequest)
			return
		}
		limit = v
	}
	r.syncEvidenceFromKernelOutboxes()
	h, ok := r.evidenceSvc.GetDecisionLogHistory(parts[0], limit)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"decision_id": parts[0],
		"history":     h,
	})
}

func writeKernelError(w http.ResponseWriter, err error) {
	var ke *run.KernelError
	if errors.As(err, &ke) {
		writeJSON(w, ke.StatusCode, ke)
		return
	}
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func writeEvidenceError(w http.ResponseWriter, err error) {
	var ee *evidence.KernelError
	if errors.As(err, &ee) {
		writeJSON(w, ee.StatusCode, ee)
		return
	}
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func writeDecisionError(w http.ResponseWriter, err error) {
	var ce *decision.ControlError
	if errors.As(err, &ce) {
		writeJSON(w, ce.StatusCode, ce)
		return
	}
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func scopeFromQuery(req *http.Request) decision.ScopeRef {
	q := req.URL.Query()
	return decision.ScopeRef{
		OrgID:       strings.TrimSpace(q.Get("org_id")),
		WorkspaceID: strings.TrimSpace(q.Get("workspace_id")),
		ProjectID:   strings.TrimSpace(q.Get("project_id")),
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func hashMap(v interface{}) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
