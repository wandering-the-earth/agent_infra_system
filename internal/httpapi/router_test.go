package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent/infra/internal/decision"
	"agent/infra/internal/evidence"
	"agent/infra/internal/run"
)

func TestRuntimeDecisionEndpoint(t *testing.T) {
	router := NewRouterWithService(decision.NewService(decision.Config{}))
	body := decision.RuntimeDecisionRequest{
		RequestID:               "req-http-001",
		TraceID:                 "tr-http-001",
		TenantID:                "tenant-http",
		WorkflowID:              "wf-http",
		RunID:                   "run-http",
		StepID:                  "step-1",
		RiskTier:                decision.RiskHigh,
		EffectType:              decision.EffectExternalWrite,
		DecisionGraphID:         "dg-http-001",
		ApprovalSystemAvailable: true,
		PolicyEngineAvailable:   boolPtr(true),
		Phase:                   decision.PhasePreTool,
		DCUInput:                decision.DCUInput{FeatureReads: 2, RuleEvals: 3, DependencyCalls: 1, ConflictResolutions: 1},
		FeatureVersion:          "fv-http-001",
		FeatureEvidenceRef:      "evidence://feature/fv-http-001",
		FeatureProducerID:       "feature-producer-default",
		Freeze: decision.FreezeLayer{
			Frozen: decision.FrozenInput{
				ContextCandidatesSnapshotRef:       "ctx-snap",
				PolicyBundleSnapshotRef:            "policy-snap",
				FeatureSnapshotID:                  "feature-snap",
				ApprovalRoutingSnapshotRef:         "route-snap",
				QuotaSnapshotRef:                   "quota-snap",
				SchedulerAdmissionInputSnapshotRef: "sched-snap",
			},
			DynamicUsed: []string{"trace_tags"},
		},
	}
	rec := postJSON(t, router, "/v1/decision/evaluate-runtime", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status mismatch: got=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp decision.RuntimeDecisionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Decision != decision.DecisionRequireApproval {
		t.Fatalf("decision mismatch: got=%s", resp.Decision)
	}
}

func TestGetDecisionEndpoint(t *testing.T) {
	svc := decision.NewService(decision.Config{})
	router := NewRouterWithService(svc)
	body := decision.RuntimeDecisionRequest{
		RequestID:               "req-http-002",
		TraceID:                 "tr-http-002",
		TenantID:                "tenant-http",
		WorkflowID:              "wf-http",
		RunID:                   "run-http-2",
		StepID:                  "step-1",
		RiskTier:                decision.RiskLow,
		EffectType:              decision.EffectRead,
		DecisionGraphID:         "dg-http-002",
		ApprovalSystemAvailable: true,
		PolicyEngineAvailable:   boolPtr(true),
		Phase:                   decision.PhasePreTool,
		DCUInput:                decision.DCUInput{FeatureReads: 1, RuleEvals: 1, DependencyCalls: 1},
		FeatureVersion:          "fv-http-002",
		FeatureEvidenceRef:      "evidence://feature/fv-http-002",
		FeatureProducerID:       "feature-producer-default",
		Freeze: decision.FreezeLayer{
			Frozen: decision.FrozenInput{
				ContextCandidatesSnapshotRef:       "ctx-snap",
				PolicyBundleSnapshotRef:            "policy-snap",
				FeatureSnapshotID:                  "feature-snap",
				ApprovalRoutingSnapshotRef:         "route-snap",
				QuotaSnapshotRef:                   "quota-snap",
				SchedulerAdmissionInputSnapshotRef: "sched-snap",
			},
			DynamicUsed: []string{"trace_tags"},
		},
	}
	rec := postJSON(t, router, "/v1/decision/evaluate-runtime", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("runtime decision status mismatch: got=%d", rec.Code)
	}
	var runtimeResp decision.RuntimeDecisionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &runtimeResp); err != nil {
		t.Fatalf("decode runtime response: %v", err)
	}
	getReq := httptest.NewRequest(http.MethodGet, "/v1/decision/"+runtimeResp.DecisionID, nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get decision status mismatch: got=%d body=%s", getRec.Code, getRec.Body.String())
	}

	historyReq := httptest.NewRequest(http.MethodGet, "/v1/decision/"+runtimeResp.DecisionID+"/history", nil)
	historyRec := httptest.NewRecorder()
	router.ServeHTTP(historyRec, historyReq)
	if historyRec.Code != http.StatusOK {
		t.Fatalf("decision history status mismatch: got=%d body=%s", historyRec.Code, historyRec.Body.String())
	}

	versionReq := httptest.NewRequest(http.MethodGet, "/v1/decision/"+runtimeResp.DecisionID+"/versions/1", nil)
	versionRec := httptest.NewRecorder()
	router.ServeHTTP(versionRec, versionReq)
	if versionRec.Code != http.StatusOK {
		t.Fatalf("decision version status mismatch: got=%d body=%s", versionRec.Code, versionRec.Body.String())
	}
}

func TestScheduleAdmissionAndReleaseEndpoints(t *testing.T) {
	router := NewRouterWithService(decision.NewService(decision.Config{
		Clock: func() time.Time { return time.Now() },
	}))
	admission := decision.ScheduleAdmissionRequest{
		RequestID:          "adm-http-1",
		RunID:              "run-1",
		StepID:             "step-1",
		TenantID:           "tenant-a",
		RiskTier:           decision.RiskLow,
		PriorityClass:      "p1",
		RequestedResources: map[string]float64{"tokens": 10},
		QuotaRemaining:     map[string]float64{"tokens": 20},
		AllowPreempt:       true,
	}
	recA := postJSON(t, router, "/v1/decision/evaluate-schedule-admission", admission)
	if recA.Code != http.StatusOK {
		t.Fatalf("admission status mismatch: got=%d", recA.Code)
	}

	release := decision.ReleaseDecisionRequest{
		RequestID: "rel-http-1",
		RiskTier:  decision.RiskLow,
		Evidence: decision.ReleaseEvidence{
			EvalPass:              true,
			PolicyRegressionPass:  true,
			ReplayConsistencyPass: true,
			HumanSignoff:          true,
		},
	}
	recR := postJSON(t, router, "/v1/decision/evaluate-release", release)
	if recR.Code != http.StatusOK {
		t.Fatalf("release status mismatch: got=%d", recR.Code)
	}
}

func TestApprovalEndpoints(t *testing.T) {
	router := NewRouterWithService(decision.NewService(decision.Config{}))
	create := decision.ApprovalCreateRequest{
		RunID:      "run-approval-1",
		StepID:     "step-1",
		DecisionID: "dec-1",
		RiskTier:   decision.RiskHigh,
		Mode:       "two_man_rule",
	}
	rec := postJSON(t, router, "/v1/approval/cases", create)
	if rec.Code != http.StatusOK {
		t.Fatalf("create approval case status mismatch: got=%d", rec.Code)
	}
	var c decision.ApprovalCase
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatalf("decode approval case: %v", err)
	}

	rec2 := postJSON(t, router, "/v1/approval/cases/"+c.CaseID+"/decision", decision.ApprovalDecisionRequest{
		Decision: "approve",
	})
	if rec2.Code != http.StatusOK {
		t.Fatalf("approval decision status mismatch: got=%d", rec2.Code)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	router := NewRouterWithService(decision.NewService(decision.Config{}))
	req := httptest.NewRequest(http.MethodGet, "/v1/metrics/decision", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status mismatch: got=%d", rec.Code)
	}
}

func TestOutboxEndpoint(t *testing.T) {
	router := NewRouterWithService(decision.NewService(decision.Config{}))
	body := decision.RuntimeDecisionRequest{
		RequestID:               "req-outbox-1",
		TraceID:                 "tr-outbox-1",
		TenantID:                "tenant-outbox",
		WorkflowID:              "wf-outbox",
		RunID:                   "run-outbox",
		StepID:                  "step-1",
		RiskTier:                decision.RiskLow,
		EffectType:              decision.EffectRead,
		DecisionGraphID:         "dg-outbox-1",
		ApprovalSystemAvailable: true,
		PolicyEngineAvailable:   boolPtr(true),
		Phase:                   decision.PhasePreTool,
		DCUInput:                decision.DCUInput{FeatureReads: 1, RuleEvals: 1, DependencyCalls: 1},
		FeatureVersion:          "fv-outbox-1",
		FeatureEvidenceRef:      "evidence://feature/fv-outbox-1",
		FeatureProducerID:       "feature-producer-default",
		Freeze: decision.FreezeLayer{
			Frozen: decision.FrozenInput{
				ContextCandidatesSnapshotRef:       "ctx-snap",
				PolicyBundleSnapshotRef:            "policy-snap",
				FeatureSnapshotID:                  "feature-snap",
				ApprovalRoutingSnapshotRef:         "route-snap",
				QuotaSnapshotRef:                   "quota-snap",
				SchedulerAdmissionInputSnapshotRef: "sched-snap",
			},
			DynamicUsed: []string{"trace_tags"},
		},
	}
	rec := postJSON(t, router, "/v1/decision/evaluate-runtime", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("runtime status mismatch: got=%d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/decision/outbox", nil)
	outRec := httptest.NewRecorder()
	router.ServeHTTP(outRec, req)
	if outRec.Code != http.StatusOK {
		t.Fatalf("outbox status mismatch: got=%d", outRec.Code)
	}
}

func TestFeatureSignalContractSchedulerEndpoint(t *testing.T) {
	svc := decision.NewKernel(decision.Config{
		Clock: func() time.Time { return time.Date(2026, 4, 8, 2, 0, 0, 0, time.UTC) },
	})
	router := NewRouterWithService(svc)
	scope := decision.ScopeRef{OrgID: "org-http", WorkspaceID: "ws-http", ProjectID: "proj-http"}
	recPub := postJSON(t, router, "/v1/features/signal-contracts", decision.FeatureSignalContractPublishRequest{
		RiskTier:       decision.RiskLow,
		Phase:          decision.PhasePreTool,
		RequiredFields: []string{"feature_version"},
		Reason:         "seed",
		Scope:          scope,
		ActivateAt:     time.Date(2026, 4, 8, 2, 5, 0, 0, time.UTC),
	})
	if recPub.Code != http.StatusOK {
		t.Fatalf("publish status mismatch: got=%d body=%s", recPub.Code, recPub.Body.String())
	}

	recRun := postJSON(t, router, "/v1/features/signal-contracts/scheduler/run", map[string]interface{}{
		"at": "2026-04-08T02:06:00Z",
	})
	if recRun.Code != http.StatusOK {
		t.Fatalf("scheduler run status mismatch: got=%d body=%s", recRun.Code, recRun.Body.String())
	}
}

func TestNewRouterAndHealthzAndHash(t *testing.T) {
	router := NewRouter()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status mismatch: %d", rec.Code)
	}
	if hashMap(map[string]string{"a": "b"}) == "" {
		t.Fatalf("hashMap should not be empty")
	}
}

func TestContextResolveAndMethodErrors(t *testing.T) {
	router := NewRouterWithService(decision.NewService(decision.Config{}))
	body := decision.RuntimeDecisionRequest{
		RequestID:          "ctx-1",
		DecisionGraphID:    "dg-ctx-1",
		FeatureVersion:     "fv-ctx-1",
		FeatureEvidenceRef: "evidence://feature/fv-ctx-1",
		Freeze: decision.FreezeLayer{
			Frozen: decision.FrozenInput{
				ContextCandidatesSnapshotRef:       "c",
				PolicyBundleSnapshotRef:            "p",
				FeatureSnapshotID:                  "f",
				ApprovalRoutingSnapshotRef:         "a",
				QuotaSnapshotRef:                   "q",
				SchedulerAdmissionInputSnapshotRef: "s",
			},
		},
	}
	rec := postJSON(t, router, "/v1/context/resolve", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("context resolve status mismatch: %d", rec.Code)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode context response: %v", err)
	}
	for _, forbidden := range []string{"allow", "deny", "risk", "approval", "effect"} {
		if _, exists := out[forbidden]; exists {
			t.Fatalf("context response must not expose decision field: %s", forbidden)
		}
	}

	// method not allowed
	req := httptest.NewRequest(http.MethodGet, "/v1/decision/evaluate-runtime", nil)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got=%d", rec2.Code)
	}
}

func TestInvalidJSONAndPathBranches(t *testing.T) {
	router := NewRouterWithService(decision.NewService(decision.Config{}))

	bad := httptest.NewRequest(http.MethodPost, "/v1/decision/evaluate-release", bytes.NewBufferString("{"))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, bad)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid json should return 400, got=%d", rec.Code)
	}

	badPath := httptest.NewRequest(http.MethodPost, "/v1/approval/cases/only", bytes.NewBufferString(`{"decision":"approve"}`))
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, badPath)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("invalid path should return 400, got=%d", rec2.Code)
	}

	getBad := httptest.NewRequest(http.MethodGet, "/v1/decision/a/b", nil)
	rec3 := httptest.NewRecorder()
	router.ServeHTTP(rec3, getBad)
	if rec3.Code != http.StatusBadRequest {
		t.Fatalf("invalid decision path should return 400, got=%d", rec3.Code)
	}
}

func TestConfirmRunAdvanceAndRepairEndpoints(t *testing.T) {
	svc := decision.NewService(decision.Config{})
	router := NewRouterWithService(svc)
	body := decision.RuntimeDecisionRequest{
		RequestID:               "req-confirm-1",
		TraceID:                 "tr-confirm-1",
		TenantID:                "tenant",
		WorkflowID:              "wf",
		RunID:                   "run",
		StepID:                  "step",
		RiskTier:                decision.RiskLow,
		EffectType:              decision.EffectRead,
		DecisionGraphID:         "dg-confirm-1",
		ApprovalSystemAvailable: true,
		PolicyEngineAvailable:   boolPtr(true),
		Phase:                   decision.PhasePreTool,
		DCUInput:                decision.DCUInput{FeatureReads: 1, RuleEvals: 1, DependencyCalls: 1},
		FeatureVersion:          "fv-confirm-1",
		FeatureEvidenceRef:      "evidence://feature/fv-confirm-1",
		FeatureProducerID:       "feature-producer-default",
		Freeze: decision.FreezeLayer{
			Frozen: decision.FrozenInput{
				ContextCandidatesSnapshotRef:       "ctx",
				PolicyBundleSnapshotRef:            "pb",
				FeatureSnapshotID:                  "fs",
				ApprovalRoutingSnapshotRef:         "ar",
				QuotaSnapshotRef:                   "qs",
				SchedulerAdmissionInputSnapshotRef: "ss",
			},
		},
	}
	rec := postJSON(t, router, "/v1/decision/evaluate-runtime", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("runtime status mismatch: %d", rec.Code)
	}
	var evalResp decision.RuntimeDecisionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &evalResp); err != nil {
		t.Fatalf("decode runtime response: %v", err)
	}

	confirm := postJSON(t, router, "/v1/decision/confirm-run-advance", decision.ConfirmRunAdvanceRequest{
		DecisionID:   evalResp.DecisionID,
		RunID:        "run",
		StepID:       "step",
		AttemptIndex: 0,
		Phase:        decision.PhasePreTool,
		Success:      true,
	})
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm status mismatch: %d", confirm.Code)
	}

	repair := postJSON(t, router, "/v1/decision/repair-pending", map[string]string{})
	if repair.Code != http.StatusOK {
		t.Fatalf("repair status mismatch: %d", repair.Code)
	}

	terminalize := postJSON(t, router, "/v1/decision/terminalize-pending", decision.TerminalizePendingDecisionRequest{
		DecisionID: evalResp.DecisionID,
		Status:     "safeguard_hold",
		Reason:     "manual",
		Actor:      "oncall",
	})
	if terminalize.Code != http.StatusOK {
		t.Fatalf("terminalize status mismatch: %d", terminalize.Code)
	}
}

func TestEndpointMethodAndInvalidJSONBranches(t *testing.T) {
	router := NewRouterWithService(decision.NewService(decision.Config{}))

	methodCases := []struct {
		path string
	}{
		{"/v1/context/resolve"},
		{"/v1/decision/evaluate-schedule-admission"},
		{"/v1/decision/evaluate-release"},
		{"/v1/decision/confirm-run-advance"},
		{"/v1/decision/repair-pending"},
		{"/v1/decision/terminalize-pending"},
		{"/v1/approval/cases"},
		{"/v1/runs"},
		{"/v1/runs/sweep-ttl"},
		{"/v1/runs/sweep-zombie"},
		{"/v1/runs/maintenance"},
	}
	for _, tc := range methodCases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405 for %s, got=%d", tc.path, rec.Code)
		}
	}

	badJSONCases := []string{
		"/v1/context/resolve",
		"/v1/decision/evaluate-runtime",
		"/v1/decision/evaluate-schedule-admission",
		"/v1/decision/evaluate-release",
		"/v1/decision/confirm-run-advance",
		"/v1/decision/terminalize-pending",
		"/v1/approval/cases",
		"/v1/approval/cases/case-x/decision",
		"/v1/runs",
		"/v1/runs/run-1/advance",
		"/v1/runs/run-1/park",
		"/v1/runs/run-1/resume",
		"/v1/runs/run-1/abort",
	}
	for _, path := range badJSONCases {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString("{"))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for bad json path=%s got=%d", path, rec.Code)
		}
	}

	// metrics/outbox method not allowed branches
	reqM := httptest.NewRequest(http.MethodPost, "/v1/metrics/decision", nil)
	recM := httptest.NewRecorder()
	router.ServeHTTP(recM, reqM)
	if recM.Code != http.StatusMethodNotAllowed {
		t.Fatalf("metrics POST expected 405, got=%d", recM.Code)
	}

	reqO := httptest.NewRequest(http.MethodPost, "/v1/decision/outbox", nil)
	recO := httptest.NewRecorder()
	router.ServeHTTP(recO, reqO)
	if recO.Code != http.StatusMethodNotAllowed {
		t.Fatalf("outbox POST expected 405, got=%d", recO.Code)
	}

	reqRunGetInvalid := httptest.NewRequest(http.MethodGet, "/v1/runs/", nil)
	recRunGetInvalid := httptest.NewRecorder()
	router.ServeHTTP(recRunGetInvalid, reqRunGetInvalid)
	if recRunGetInvalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid run path expected 400, got=%d", recRunGetInvalid.Code)
	}
}

func TestRunEndpointsHappyPath(t *testing.T) {
	decisionSvc := decision.NewService(decision.Config{})
	runSvc := run.NewKernel(run.Config{}, decisionRunPort{svc: decisionSvc})
	router := NewRouterWithServices(decisionSvc, runSvc)

	create := run.CreateRunRequest{
		RequestID:       "req-create-r1",
		TenantID:        "tenant-r1",
		WorkflowID:      "wf-r1",
		WorkflowVersion: "v1",
		RiskTier:        "low",
		InputPayloadRef: "obj://input",
		Snapshot: run.SnapshotInput{
			PolicyBundleID:         "pb-1",
			ModelProfileID:         "mp-1",
			DependencyBundleID:     "db-1",
			SkillBundleSetHash:     "skills",
			ContextPolicySpaceHash: "cps",
		},
	}
	createRec := postJSON(t, router, "/v1/runs", create)
	if createRec.Code != http.StatusOK {
		t.Fatalf("run create failed: code=%d body=%s", createRec.Code, createRec.Body.String())
	}
	var created run.CreateRunResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create run response: %v", err)
	}

	// Create decision and confirm, then advance run.
	eval := decision.RuntimeDecisionRequest{
		RequestID:               "req-run-1",
		TraceID:                 "tr-run-1",
		TenantID:                "tenant-r1",
		WorkflowID:              "wf-r1",
		RunID:                   created.RunID,
		StepID:                  "step-1",
		RiskTier:                decision.RiskLow,
		EffectType:              decision.EffectRead,
		DecisionGraphID:         "dg-run-1",
		ApprovalSystemAvailable: true,
		PolicyEngineAvailable:   boolPtr(true),
		Phase:                   decision.PhasePreTool,
		DCUInput:                decision.DCUInput{FeatureReads: 1, RuleEvals: 1, DependencyCalls: 1},
		FeatureVersion:          "fv-run-1",
		FeatureEvidenceRef:      "evidence://feature/fv-run-1",
		FeatureProducerID:       "feature-producer-default",
		Freeze: decision.FreezeLayer{
			Frozen: decision.FrozenInput{
				ContextCandidatesSnapshotRef:       "ctx",
				PolicyBundleSnapshotRef:            "pb",
				FeatureSnapshotID:                  "fs",
				ApprovalRoutingSnapshotRef:         "ar",
				QuotaSnapshotRef:                   "qr",
				SchedulerAdmissionInputSnapshotRef: "sr",
			},
		},
	}
	evalRec := postJSON(t, router, "/v1/decision/evaluate-runtime", eval)
	if evalRec.Code != http.StatusOK {
		t.Fatalf("decision evaluate failed: %d %s", evalRec.Code, evalRec.Body.String())
	}
	var evalResp decision.RuntimeDecisionResponse
	_ = json.Unmarshal(evalRec.Body.Bytes(), &evalResp)
	confirmRec := postJSON(t, router, "/v1/decision/confirm-run-advance", decision.ConfirmRunAdvanceRequest{
		DecisionID:   evalResp.DecisionID,
		RunID:        created.RunID,
		StepID:       "step-1",
		AttemptIndex: 0,
		Phase:        decision.PhasePreTool,
		Success:      true,
	})
	if confirmRec.Code != http.StatusOK {
		t.Fatalf("decision confirm failed: %d %s", confirmRec.Code, confirmRec.Body.String())
	}
	admitRec := postJSON(t, router, "/v1/decision/evaluate-schedule-admission", decision.ScheduleAdmissionRequest{
		RequestID:          "req-admit-run-1",
		RunID:              created.RunID,
		StepID:             "step-1",
		TenantID:           "tenant-r1",
		RiskTier:           decision.RiskLow,
		PriorityClass:      "normal",
		RequestedResources: map[string]float64{"tokens": 10},
		QuotaRemaining:     map[string]float64{"tokens": 100},
	})
	if admitRec.Code != http.StatusOK {
		t.Fatalf("schedule admission failed: %d %s", admitRec.Code, admitRec.Body.String())
	}
	var admitResp decision.ScheduleAdmissionResponse
	_ = json.Unmarshal(admitRec.Body.Bytes(), &admitResp)
	if admitResp.Ticket == nil || admitResp.Ticket.TicketID == "" {
		t.Fatalf("schedule admission did not return ticket: %+v", admitResp)
	}

	advance := run.AdvanceRunRequest{
		ExpectedStateVersion: 1,
		StepID:               "step-1",
		StepVersion:          "1",
		IdempotencyKey:       "idem-1",
		DecisionRef: run.DecisionRef{
			DecisionID:   evalResp.DecisionID,
			DecisionHash: evalResp.DecisionHash,
		},
		ExecutionReceiptRef:    "ticket://" + admitResp.Ticket.TicketID,
		ExecutionUsedResources: map[string]float64{"tokens": 5},
		IncomingStepSeqID:      1,
		InputHash:              "in-h1",
		OutputHash:             "out-h1",
		NextState:              run.StateRunning,
	}
	advRec := postJSON(t, router, "/v1/runs/"+created.RunID+"/advance", advance)
	if advRec.Code != http.StatusOK {
		t.Fatalf("advance failed: %d %s", advRec.Code, advRec.Body.String())
	}

	park := run.ParkRunRequest{
		ExpectedStateVersion: 2,
		StepID:               "step-park",
		IncomingStepSeqID:    2,
		TokenType:            "park",
		ParkReason:           "wait",
	}
	parkRec := postJSON(t, router, "/v1/runs/"+created.RunID+"/park", park)
	if parkRec.Code != http.StatusOK {
		t.Fatalf("park failed: %d %s", parkRec.Code, parkRec.Body.String())
	}
	var parked run.ParkRunResponse
	_ = json.Unmarshal(parkRec.Body.Bytes(), &parked)

	resume := run.ResumeRunRequest{
		ContinuationToken:    parked.ContinuationToken,
		ResumeReason:         "callback",
		ReceiptRef:           "rcpt://resume",
		IncomingStepSeqID:    3,
		ExpectedSnapshotHash: created.SnapshotHash,
	}
	resRec := postJSON(t, router, "/v1/runs/"+created.RunID+"/resume", resume)
	if resRec.Code != http.StatusOK {
		t.Fatalf("resume failed: %d %s", resRec.Code, resRec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/runs/"+created.RunID, nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get run failed: %d %s", getRec.Code, getRec.Body.String())
	}

	sweepTTL := postJSON(t, router, "/v1/runs/sweep-ttl", map[string]string{})
	if sweepTTL.Code != http.StatusOK {
		t.Fatalf("sweep ttl failed: %d", sweepTTL.Code)
	}
	sweepZombie := postJSON(t, router, "/v1/runs/sweep-zombie", map[string]string{})
	if sweepZombie.Code != http.StatusOK {
		t.Fatalf("sweep zombie failed: %d", sweepZombie.Code)
	}
	maintenance := postJSON(t, router, "/v1/runs/maintenance", map[string]string{})
	if maintenance.Code != http.StatusOK {
		t.Fatalf("sweep maintenance failed: %d", maintenance.Code)
	}

	abortRec := postJSON(t, router, "/v1/runs/"+created.RunID+"/abort", run.AbortRunRequest{Reason: "manual"})
	if abortRec.Code != http.StatusOK {
		t.Fatalf("abort failed: %d %s", abortRec.Code, abortRec.Body.String())
	}
}

func TestFeatureControlPlaneEndpoints(t *testing.T) {
	router := NewRouterWithService(decision.NewKernel(decision.Config{}))
	scope := decision.ScopeRef{
		OrgID:       "org-http",
		WorkspaceID: "ws-http",
		ProjectID:   "proj-http",
	}

	defRec := postJSON(t, router, "/v1/features/definitions", decision.FeatureDefinitionCreateRequest{
		FeatureID:   "risk_score",
		Name:        "Risk Score",
		Description: "risk score",
		Owner:       "team-risk",
		Scope:       scope,
	})
	if defRec.Code != http.StatusOK {
		t.Fatalf("create feature definition failed: code=%d body=%s", defRec.Code, defRec.Body.String())
	}

	verRec := postJSON(t, router, "/v1/features/risk_score/versions", decision.FeatureVersionCreateRequest{
		Version:       "1.0.0",
		ProducerID:    "producer-risk",
		SchemaVersion: "schema-v1",
		EvidenceRef:   "evidence://feature/risk_score/v1",
		Scope:         scope,
		DriftScore:    0.1,
	})
	if verRec.Code != http.StatusOK {
		t.Fatalf("create feature version failed: code=%d body=%s", verRec.Code, verRec.Body.String())
	}

	buildRec := postJSON(t, router, "/v1/features/snapshots/build", decision.FeatureSnapshotBuildRequest{
		FeatureID:   "risk_score",
		Version:     "1.0.0",
		ProducerID:  "producer-risk",
		EvidenceRef: "evidence://feature/risk_score/v1",
		TTLMS:       5000,
		Scope:       scope,
	})
	if buildRec.Code != http.StatusOK {
		t.Fatalf("build snapshot failed: code=%d body=%s", buildRec.Code, buildRec.Body.String())
	}
	var snap decision.FeatureSnapshot
	if err := json.Unmarshal(buildRec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snap.SnapshotID == "" {
		t.Fatalf("snapshot id must not be empty")
	}

	getSnapReq := httptest.NewRequest(http.MethodGet, "/v1/features/snapshots/"+snap.SnapshotID, nil)
	getSnapRec := httptest.NewRecorder()
	router.ServeHTTP(getSnapRec, getSnapReq)
	if getSnapRec.Code != http.StatusOK {
		t.Fatalf("get snapshot failed: code=%d body=%s", getSnapRec.Code, getSnapRec.Body.String())
	}

	validateRec := postJSON(t, router, "/v1/features/snapshots/validate-freshness", decision.FeatureSnapshotFreshnessValidateRequest{
		SnapshotID: snap.SnapshotID,
		RiskTier:   decision.RiskLow,
		Scope:      scope,
	})
	if validateRec.Code != http.StatusOK {
		t.Fatalf("validate freshness failed: code=%d body=%s", validateRec.Code, validateRec.Body.String())
	}

	getEvidenceReq := httptest.NewRequest(http.MethodGet, "/v1/features/snapshots/"+snap.SnapshotID+"/evidence", nil)
	getEvidenceRec := httptest.NewRecorder()
	router.ServeHTTP(getEvidenceRec, getEvidenceReq)
	if getEvidenceRec.Code != http.StatusOK {
		t.Fatalf("get snapshot evidence failed: code=%d body=%s", getEvidenceRec.Code, getEvidenceRec.Body.String())
	}

	scopeQuery := "?org_id=org-http&workspace_id=ws-http&project_id=proj-http"
	getDriftReq := httptest.NewRequest(http.MethodGet, "/v1/features/risk_score/drift-report"+scopeQuery, nil)
	getDriftRec := httptest.NewRecorder()
	router.ServeHTTP(getDriftRec, getDriftReq)
	if getDriftRec.Code != http.StatusOK {
		t.Fatalf("get drift report failed: code=%d body=%s", getDriftRec.Code, getDriftRec.Body.String())
	}

	getDepReq := httptest.NewRequest(http.MethodGet, "/v1/features/risk_score/dependency-graph"+scopeQuery, nil)
	getDepRec := httptest.NewRecorder()
	router.ServeHTTP(getDepRec, getDepReq)
	if getDepRec.Code != http.StatusOK {
		t.Fatalf("get dependency graph failed: code=%d body=%s", getDepRec.Code, getDepRec.Body.String())
	}

	rollbackRec := postJSON(t, router, "/v1/features/risk_score/rollback", decision.FeatureRollbackRequest{
		TargetVersion: "1.0.0",
		Reason:        "manual-rollback",
		Scope:         scope,
	})
	if rollbackRec.Code != http.StatusOK {
		t.Fatalf("rollback feature failed: code=%d body=%s", rollbackRec.Code, rollbackRec.Body.String())
	}
}

func TestApprovalOrgHealthAndEnforcementEndpoints(t *testing.T) {
	router := NewRouterWithService(decision.NewKernel(decision.Config{}))
	scopeQuery := "?org_id=org-http&workspace_id=ws-http&project_id=proj-http"

	getHealthReq := httptest.NewRequest(http.MethodGet, "/v1/approval/org-health"+scopeQuery, nil)
	getHealthRec := httptest.NewRecorder()
	router.ServeHTTP(getHealthRec, getHealthReq)
	if getHealthRec.Code != http.StatusOK {
		t.Fatalf("get approval org health failed: code=%d body=%s", getHealthRec.Code, getHealthRec.Body.String())
	}

	remediationRec := postJSON(t, router, "/v1/approval/org-health/remediation", decision.ApprovalOrgHealthRemediationRequest{
		Scope: decision.ScopeRef{
			OrgID:       "org-http",
			WorkspaceID: "ws-http",
			ProjectID:   "proj-http",
		},
		Actions: []string{"refresh_delegates", "replay_approval_routes"},
		Reason:  "nightly-repair",
		Actor:   "ops",
	})
	if remediationRec.Code != http.StatusOK {
		t.Fatalf("approval remediation failed: code=%d body=%s", remediationRec.Code, remediationRec.Body.String())
	}

	reportsReq := httptest.NewRequest(http.MethodGet, "/v1/approval/org-health/reports"+scopeQuery+"&limit=5", nil)
	reportsRec := httptest.NewRecorder()
	router.ServeHTTP(reportsRec, reportsReq)
	if reportsRec.Code != http.StatusOK {
		t.Fatalf("get approval reports failed: code=%d body=%s", reportsRec.Code, reportsRec.Body.String())
	}

	getMatrixReq := httptest.NewRequest(http.MethodGet, "/v1/metrics/enforcement-matrix", nil)
	getMatrixRec := httptest.NewRecorder()
	router.ServeHTTP(getMatrixRec, getMatrixReq)
	if getMatrixRec.Code != http.StatusOK {
		t.Fatalf("get enforcement matrix failed: code=%d body=%s", getMatrixRec.Code, getMatrixRec.Body.String())
	}

	validateRec := postJSON(t, router, "/v1/metrics/enforcement-matrix/validate", decision.EnforcementMatrixValidateRequest{
		Matrix: map[string]decision.EnforcementThreshold{
			"bad": {
				ObserveOnly:  1,
				Alert:        2,
				BlockRelease: 3,
				BlockRuntime: 4,
				Direction:    "lt",
			},
		},
	})
	if validateRec.Code != http.StatusOK {
		t.Fatalf("validate enforcement matrix failed: code=%d body=%s", validateRec.Code, validateRec.Body.String())
	}

	publishRec := postJSON(t, router, "/v1/metrics/enforcement-matrix/publish", decision.EnforcementMatrixPublishRequest{
		Matrix: map[string]decision.EnforcementThreshold{
			"feature_stale_rate": {
				ObserveOnly:  1,
				Alert:        2,
				BlockRelease: 3,
				BlockRuntime: 4,
				Direction:    "gt",
			},
		},
		Reason: "tighten-stale-guard",
	})
	if publishRec.Code != http.StatusOK {
		t.Fatalf("publish enforcement matrix failed: code=%d body=%s", publishRec.Code, publishRec.Body.String())
	}
}

func TestControlPlaneErrorMappingAndMethodBranches(t *testing.T) {
	router := NewRouterWithService(decision.NewKernel(decision.Config{}))

	// scope missing should return control error (422), not generic 500.
	req := httptest.NewRequest(http.MethodGet, "/v1/approval/org-health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for missing scope, got=%d body=%s", rec.Code, rec.Body.String())
	}

	// method branch
	reqMethod := httptest.NewRequest(http.MethodGet, "/v1/metrics/enforcement-matrix/publish", nil)
	recMethod := httptest.NewRecorder()
	router.ServeHTTP(recMethod, reqMethod)
	if recMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for publish GET, got=%d", recMethod.Code)
	}

	// invalid limit query
	reqLimit := httptest.NewRequest(http.MethodGet, "/v1/approval/org-health/reports?org_id=o&workspace_id=w&project_id=p&limit=bad", nil)
	recLimit := httptest.NewRecorder()
	router.ServeHTTP(recLimit, reqLimit)
	if recLimit.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid limit, got=%d", recLimit.Code)
	}
}

func TestEvidenceSyncAndRunEvidenceEndpoints(t *testing.T) {
	router := NewRouter()

	runCreate := run.CreateRunRequest{
		RequestID:       "req-evd-run-1",
		TenantID:        "tenant-evd",
		WorkflowID:      "wf-evd",
		WorkflowVersion: "v1",
		RiskTier:        run.RiskLow,
		InputPayloadRef: "obj://input/evd-1",
		Snapshot: run.SnapshotInput{
			PolicyBundleID:         "pb-1",
			ModelProfileID:         "mp-1",
			DependencyBundleID:     "db-1",
			SkillBundleSetHash:     "sb-1",
			ContextPolicySpaceHash: "cp-1",
		},
	}
	createRec := postJSON(t, router, "/v1/runs", runCreate)
	if createRec.Code != http.StatusOK {
		t.Fatalf("run create failed: code=%d body=%s", createRec.Code, createRec.Body.String())
	}
	var created run.CreateRunResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create run response: %v", err)
	}

	decReq := decision.RuntimeDecisionRequest{
		RequestID:               "req-evd-dec-1",
		TraceID:                 "tr-evd-dec-1",
		TenantID:                "tenant-evd",
		WorkflowID:              "wf-evd",
		RunID:                   created.RunID,
		StepID:                  "step-1",
		RiskTier:                decision.RiskHigh,
		EffectType:              decision.EffectExternalWrite,
		DecisionGraphID:         "dg-evd-1",
		ApprovalSystemAvailable: true,
		PolicyEngineAvailable:   boolPtr(true),
		Phase:                   decision.PhasePreTool,
		DCUInput:                decision.DCUInput{FeatureReads: 2, RuleEvals: 2, DependencyCalls: 1},
		FeatureVersion:          "fv-1",
		FeatureEvidenceRef:      "evidence://fv/1",
		FeatureProducerID:       "producer-1",
		Freeze: decision.FreezeLayer{
			Frozen: decision.FrozenInput{
				ContextCandidatesSnapshotRef:       "ctx-1",
				PolicyBundleSnapshotRef:            "policy-1",
				FeatureSnapshotID:                  "fs-1",
				ApprovalRoutingSnapshotRef:         "ar-1",
				QuotaSnapshotRef:                   "q-1",
				SchedulerAdmissionInputSnapshotRef: "sa-1",
			},
			DynamicUsed: []string{"trace_tags"},
		},
	}
	decRec := postJSON(t, router, "/v1/decision/evaluate-runtime", decReq)
	if decRec.Code != http.StatusOK {
		t.Fatalf("runtime decision failed: code=%d body=%s", decRec.Code, decRec.Body.String())
	}

	syncRec := postJSON(t, router, "/v1/evidence/sync-kernel-outboxes", map[string]string{})
	if syncRec.Code != http.StatusOK {
		t.Fatalf("evidence sync failed: code=%d body=%s", syncRec.Code, syncRec.Body.String())
	}

	getEvidenceReq := httptest.NewRequest(http.MethodGet, "/v1/evidence/runs/"+created.RunID, nil)
	getEvidenceRec := httptest.NewRecorder()
	router.ServeHTTP(getEvidenceRec, getEvidenceReq)
	if getEvidenceRec.Code != http.StatusOK {
		t.Fatalf("run evidence query failed: code=%d body=%s", getEvidenceRec.Code, getEvidenceRec.Body.String())
	}

	graphReq := httptest.NewRequest(http.MethodGet, "/v1/runs/"+created.RunID+"/decision-graph", nil)
	graphRec := httptest.NewRecorder()
	router.ServeHTTP(graphRec, graphReq)
	if graphRec.Code != http.StatusOK {
		t.Fatalf("decision graph query failed: code=%d body=%s", graphRec.Code, graphRec.Body.String())
	}

	rootCauseReq := httptest.NewRequest(http.MethodGet, "/v1/runs/"+created.RunID+"/root-cause-pack?mode=minimal", nil)
	rootCauseRec := httptest.NewRecorder()
	router.ServeHTTP(rootCauseRec, rootCauseReq)
	if rootCauseRec.Code != http.StatusOK {
		t.Fatalf("root cause pack query failed: code=%d body=%s", rootCauseRec.Code, rootCauseRec.Body.String())
	}
}

func TestEvidenceIngestAndLedgerEndpoint(t *testing.T) {
	router := NewRouter()
	ingest := evidence.IngestEventRequest{
		EventID:         "evt-manual-1",
		EventType:       "run.advanced",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-ledger-1",
		StepID:          "step-1",
		TenantID:        "tenant-ledger",
		WorkflowID:      "wf-ledger",
		EvidenceTier:    evidence.Tier1,
		EvidenceGrade:   evidence.GradeOperational,
		Payload: map[string]interface{}{
			"resource_type": "tokens",
			"usage_amount":  256.0,
			"unit":          "token",
			"cost_amount":   0.12,
		},
		Usage: &evidence.UsageInput{
			ResourceType: "tokens",
			UsageAmount:  256,
			Unit:         "token",
			CostAmount:   0.12,
		},
	}
	ingestRec := postJSON(t, router, "/v1/evidence/events/ingest", ingest)
	if ingestRec.Code != http.StatusOK {
		t.Fatalf("evidence ingest failed: code=%d body=%s", ingestRec.Code, ingestRec.Body.String())
	}

	ledgerReq := httptest.NewRequest(http.MethodGet, "/v1/ledger/runs/run-ledger-1", nil)
	ledgerRec := httptest.NewRecorder()
	router.ServeHTTP(ledgerRec, ledgerReq)
	if ledgerRec.Code != http.StatusOK {
		t.Fatalf("ledger query failed: code=%d body=%s", ledgerRec.Code, ledgerRec.Body.String())
	}
}

func TestEvidenceReplayIntegrityDSARAndLedgerAggregateEndpoints(t *testing.T) {
	router := NewRouter()
	ingest := evidence.IngestEventRequest{
		EventID:         "evt-replay-1",
		EventType:       "decision.runtime.evaluated",
		SchemaVersion:   1,
		SourceComponent: "decision_kernel",
		RunID:           "run-replay-1",
		StepID:          "step-1",
		DecisionID:      "dec-replay-1",
		TenantID:        "tenant-replay",
		WorkflowID:      "wf-replay",
		RiskTier:        evidence.RiskHigh,
		EvidenceTier:    evidence.Tier1,
		EvidenceGrade:   evidence.GradeAudit,
		PayloadRef:      "obj://pii/replay-1",
		Payload: map[string]interface{}{
			"decision":                 "allow",
			"decision_node_id":         "node-replay-1",
			"parent_decision_node_ids": []interface{}{"node-root-1"},
		},
		Usage: &evidence.UsageInput{
			ResourceType: "tokens",
			UsageAmount:  100,
			Unit:         "token",
			CostAmount:   0.2,
		},
	}
	if rec := postJSON(t, router, "/v1/evidence/events/ingest", ingest); rec.Code != http.StatusOK {
		t.Fatalf("evidence ingest failed: code=%d body=%s", rec.Code, rec.Body.String())
	}

	replayReq := httptest.NewRequest(http.MethodGet, "/v1/runs/run-replay-1/replay-pack?mode=full", nil)
	replayRec := httptest.NewRecorder()
	router.ServeHTTP(replayRec, replayReq)
	if replayRec.Code != http.StatusOK {
		t.Fatalf("replay pack failed: code=%d body=%s", replayRec.Code, replayRec.Body.String())
	}
	var replayOut map[string]interface{}
	if err := json.Unmarshal(replayRec.Body.Bytes(), &replayOut); err != nil {
		t.Fatalf("decode replay pack: %v", err)
	}
	if replayOut["run_id"] != "run-replay-1" {
		t.Fatalf("unexpected replay run_id: %+v", replayOut)
	}

	verifyReq := httptest.NewRequest(http.MethodGet, "/v1/runs/run-replay-1/integrity-verify", nil)
	verifyRec := httptest.NewRecorder()
	router.ServeHTTP(verifyRec, verifyReq)
	if verifyRec.Code != http.StatusOK {
		t.Fatalf("integrity verify failed: code=%d body=%s", verifyRec.Code, verifyRec.Body.String())
	}
	var verifyOut map[string]interface{}
	if err := json.Unmarshal(verifyRec.Body.Bytes(), &verifyOut); err != nil {
		t.Fatalf("decode integrity verify: %v", err)
	}
	if v, ok := verifyOut["verified"].(bool); !ok || !v {
		t.Fatalf("expected verified=true, out=%+v", verifyOut)
	}

	aggRec := postJSON(t, router, "/v1/ledger/aggregate", evidence.LedgerAggregateRequest{
		RunID:         "run-replay-1",
		GroupBy:       "resource_type",
		DetectAnomaly: true,
	})
	if aggRec.Code != http.StatusOK {
		t.Fatalf("ledger aggregate failed: code=%d body=%s", aggRec.Code, aggRec.Body.String())
	}

	badAggRec := postJSON(t, router, "/v1/ledger/aggregate", evidence.LedgerAggregateRequest{
		RunID:   "run-replay-1",
		GroupBy: "bad-group",
	})
	if badAggRec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for bad group_by, got=%d body=%s", badAggRec.Code, badAggRec.Body.String())
	}

	dsarRec := postJSON(t, router, "/v1/evidence/dsar/delete", evidence.DSARDeleteRequest{
		RequestID: "dsar-replay-1",
		TenantID:  "tenant-replay",
		RunID:     "run-replay-1",
	})
	if dsarRec.Code != http.StatusOK {
		t.Fatalf("dsar delete failed: code=%d body=%s", dsarRec.Code, dsarRec.Body.String())
	}

	replayAfterReq := httptest.NewRequest(http.MethodGet, "/v1/runs/run-replay-1/replay-pack?mode=full", nil)
	replayAfterRec := httptest.NewRecorder()
	router.ServeHTTP(replayAfterRec, replayAfterReq)
	if replayAfterRec.Code != http.StatusOK {
		t.Fatalf("replay pack after dsar failed: code=%d body=%s", replayAfterRec.Code, replayAfterRec.Body.String())
	}
	if !strings.Contains(replayAfterRec.Body.String(), "tombstone") {
		t.Fatalf("expected tombstone marker after dsar, body=%s", replayAfterRec.Body.String())
	}
}

func TestDecisionReferenceEndpoint(t *testing.T) {
	router := NewRouter()
	req := decision.RuntimeDecisionRequest{
		RequestID:               "req-ref-1",
		TraceID:                 "tr-ref-1",
		TenantID:                "tenant-ref",
		WorkflowID:              "wf-ref",
		RunID:                   "run-ref-1",
		StepID:                  "step-1",
		RiskTier:                decision.RiskLow,
		EffectType:              decision.EffectRead,
		DecisionGraphID:         "dg-ref-1",
		ApprovalSystemAvailable: true,
		PolicyEngineAvailable:   boolPtr(true),
		Phase:                   decision.PhasePreTool,
		Freeze: decision.FreezeLayer{
			Frozen: decision.FrozenInput{
				ContextCandidatesSnapshotRef:       "ctx-ref",
				PolicyBundleSnapshotRef:            "pol-ref",
				FeatureSnapshotID:                  "fs-ref",
				ApprovalRoutingSnapshotRef:         "ar-ref",
				QuotaSnapshotRef:                   "q-ref",
				SchedulerAdmissionInputSnapshotRef: "sa-ref",
			},
		},
		FeatureVersion:     "fv-ref",
		FeatureEvidenceRef: "evidence://fv-ref",
		FeatureProducerID:  "producer-ref",
	}
	rec := postJSON(t, router, "/v1/decision/evaluate-runtime", req)
	if rec.Code != http.StatusOK {
		t.Fatalf("runtime eval failed: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out decision.RuntimeDecisionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode runtime response: %v", err)
	}
	refReq := httptest.NewRequest(http.MethodGet, "/v1/decision/"+out.DecisionID+"/reference", nil)
	refRec := httptest.NewRecorder()
	router.ServeHTTP(refRec, refReq)
	if refRec.Code != http.StatusOK {
		t.Fatalf("decision reference failed: code=%d body=%s", refRec.Code, refRec.Body.String())
	}
}

func postJSON(t *testing.T, router http.Handler, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func boolPtr(v bool) *bool { return &v }

func TestDecisionRunPortNilService(t *testing.T) {
	p := decisionRunPort{}
	if p.IsDecisionConfirmed("d1", "r1", "s1") {
		t.Fatalf("nil service must not confirm decisions")
	}
	if ref, ok := p.GetDecisionReference("d1"); ok || ref.DecisionHash != "" {
		t.Fatalf("nil service reference lookup must fail: ok=%v ref=%+v", ok, ref)
	}
}

func TestRunAndDecisionNotFoundBranches(t *testing.T) {
	router := NewRouterWithService(decision.NewService(decision.Config{}))

	getDecisionReq := httptest.NewRequest(http.MethodGet, "/v1/decision/not-exist", nil)
	getDecisionRec := httptest.NewRecorder()
	router.ServeHTTP(getDecisionRec, getDecisionReq)
	if getDecisionRec.Code != http.StatusNotFound {
		t.Fatalf("expected decision 404, got=%d", getDecisionRec.Code)
	}

	getRunReq := httptest.NewRequest(http.MethodGet, "/v1/runs/not-exist", nil)
	getRunRec := httptest.NewRecorder()
	router.ServeHTTP(getRunRec, getRunReq)
	if getRunRec.Code != http.StatusNotFound {
		t.Fatalf("expected run get 404, got=%d body=%s", getRunRec.Code, getRunRec.Body.String())
	}

	advance := run.AdvanceRunRequest{
		ExpectedStateVersion: 1,
		StepID:               "step-1",
		StepVersion:          "v1",
		IdempotencyKey:       "idem-1",
		DecisionRef: run.DecisionRef{
			DecisionID:   "d1",
			DecisionHash: "h1",
		},
		IncomingStepSeqID: 1,
		InputHash:         "in-h",
		OutputHash:        "out-h",
	}
	advanceRec := postJSON(t, router, "/v1/runs/not-exist/advance", advance)
	if advanceRec.Code != http.StatusNotFound {
		t.Fatalf("expected run advance 404, got=%d body=%s", advanceRec.Code, advanceRec.Body.String())
	}

	invalidActionReq := httptest.NewRequest(http.MethodPost, "/v1/runs/run-1/unknown", bytes.NewBufferString(`{}`))
	invalidActionRec := httptest.NewRecorder()
	router.ServeHTTP(invalidActionRec, invalidActionReq)
	if invalidActionRec.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid action 400, got=%d", invalidActionRec.Code)
	}
}

func TestWriteKernelErrorFallback(t *testing.T) {
	rec := httptest.NewRecorder()
	writeKernelError(rec, errors.New("non-kernel-error"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got=%d", rec.Code)
	}
}

func TestWriteDecisionErrorFallback(t *testing.T) {
	rec := httptest.NewRecorder()
	writeDecisionError(rec, errors.New("non-decision-error"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got=%d", rec.Code)
	}
}

func TestWriteEvidenceErrorFallback(t *testing.T) {
	rec := httptest.NewRecorder()
	writeEvidenceError(rec, errors.New("non-evidence-error"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got=%d", rec.Code)
	}
}
