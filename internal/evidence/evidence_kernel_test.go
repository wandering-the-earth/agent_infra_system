package evidence

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIngestEventBuildsSummaryGraphLedger(t *testing.T) {
	base := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	k := NewKernel(Config{
		Clock: func() time.Time { return base },
	})

	resp, err := k.IngestEvent(IngestEventRequest{
		EventID:                  "evt-1",
		EventType:                "decision.runtime.evaluated",
		SchemaVersion:            1,
		SourceComponent:          "decision_kernel",
		RunID:                    "run-1",
		StepID:                   "step-1",
		DecisionID:               "dec-1",
		TenantID:                 "tenant-a",
		WorkflowID:               "wf-a",
		RiskTier:                 RiskHigh,
		EvidenceTier:             Tier0,
		EvidenceGrade:            GradeAudit,
		PayloadIntegrityRequired: true,
		Payload: map[string]interface{}{
			"decision":            "require_approval",
			"decision_confidence": 0.92,
			"rationale_ref":       "obj://rationale/1",
		},
		Usage: &UsageInput{
			ResourceType: "tokens",
			UsageAmount:  128,
			Unit:         "token",
			CostAmount:   0.01,
		},
	})
	if err != nil {
		t.Fatalf("ingest decision event: %v", err)
	}
	if !resp.Accepted || resp.Dropped {
		t.Fatalf("event should be accepted: %+v", resp)
	}

	resp2, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-2",
		EventType:       "run.advanced",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-1",
		StepID:          "step-2",
		RiskTier:        RiskHigh,
		EvidenceTier:    Tier1,
		EvidenceGrade:   GradeOperational,
		Payload: map[string]interface{}{
			"state": "running",
		},
	})
	if err != nil {
		t.Fatalf("ingest run event: %v", err)
	}
	if !resp2.Accepted {
		t.Fatalf("run event should be accepted")
	}

	summary, ok := k.GetRunEvidence("run-1")
	if !ok {
		t.Fatalf("expected summary")
	}
	if summary.TotalEvents != 2 {
		t.Fatalf("summary total events mismatch: %d", summary.TotalEvents)
	}
	if summary.BySource["decision_kernel"] != 1 || summary.BySource["run_kernel"] != 1 {
		t.Fatalf("source counts mismatch: %+v", summary.BySource)
	}
	if !summary.HighRiskGraphComplete {
		t.Fatalf("high risk graph should be complete")
	}

	graph, ok := k.GetDecisionGraph("run-1")
	if !ok || len(graph.Nodes) < 2 || len(graph.Edges) < 1 {
		t.Fatalf("graph incomplete: nodes=%d edges=%d", len(graph.Nodes), len(graph.Edges))
	}

	log, ok := k.GetDecisionLog("dec-1")
	if !ok || log.DecisionValue == "" {
		t.Fatalf("decision log missing: %+v ok=%v", log, ok)
	}

	ledger, ok := k.GetLedger("run-1")
	if !ok || len(ledger) != 1 {
		t.Fatalf("ledger mismatch: len=%d ok=%v", len(ledger), ok)
	}
}

func TestIngestEventValidationAndSourceRegistry(t *testing.T) {
	k := NewKernel(Config{})
	if _, err := k.IngestEvent(IngestEventRequest{}); err == nil {
		t.Fatalf("expected validation error")
	}

	if _, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-x",
		EventType:       "x",
		SchemaVersion:   1,
		SourceComponent: "unknown_component",
		RunID:           "run-x",
	}); err == nil {
		t.Fatalf("expected unregistered source error")
	}

	if err := k.RegisterSource(SourceRegistration{
		SourceComponent: "unknown_component",
		MinSchema:       2,
		DefaultTier:     Tier2,
	}); err != nil {
		t.Fatalf("register source: %v", err)
	}
	if _, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-y",
		EventType:       "x",
		SchemaVersion:   1,
		SourceComponent: "unknown_component",
		RunID:           "run-y",
	}); err == nil {
		t.Fatalf("expected schema reject with min_schema=2")
	}
}

func TestIngestEventDedupAndMetrics(t *testing.T) {
	k := NewKernel(Config{})
	req := IngestEventRequest{
		EventID:         "evt-dedup-1",
		EventType:       "run.created",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-dedup",
		Payload:         map[string]interface{}{"a": 1},
	}
	first, err := k.IngestEvent(req)
	if err != nil || !first.Accepted || first.Deduped {
		t.Fatalf("first ingest mismatch: resp=%+v err=%v", first, err)
	}
	second, err := k.IngestEvent(req)
	if err != nil || !second.Accepted || !second.Deduped {
		t.Fatalf("second ingest should dedupe: resp=%+v err=%v", second, err)
	}

	metrics := k.MetricsSnapshot()
	if metrics.Counters["ingest_dedup_total"] < 1 {
		t.Fatalf("expected dedup counter, got=%+v", metrics.Counters)
	}
}

func TestBudgetDropAndDegradePolicies(t *testing.T) {
	base := time.Date(2026, 4, 7, 11, 0, 0, 0, time.UTC)
	k := NewKernel(Config{
		Clock:                  func() time.Time { return base },
		MaxEventsPerRun:        1,
		MaxEvidenceBytesPerRun: 512,
	})

	_, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-budget-1",
		EventType:       "run.created",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-budget",
		EvidenceTier:    Tier1,
		Payload:         map[string]interface{}{"ok": true},
	})
	if err != nil {
		t.Fatalf("seed ingest failed: %v", err)
	}

	drop, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-budget-2",
		EventType:       "run.advanced",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-budget",
		EvidenceTier:    Tier3,
		Payload:         map[string]interface{}{"x": "drop"},
	})
	if err != nil {
		t.Fatalf("drop ingest error: %v", err)
	}
	if !drop.Dropped {
		t.Fatalf("tier3 event should be dropped under budget pressure: %+v", drop)
	}

	degrade, err := k.IngestEvent(IngestEventRequest{
		EventID:                  "evt-budget-3",
		EventType:                "decision.runtime.evaluated",
		SchemaVersion:            1,
		SourceComponent:          "decision_kernel",
		RunID:                    "run-budget",
		EvidenceTier:             Tier0,
		PayloadIntegrityRequired: true,
		Payload:                  map[string]interface{}{"x": "degrade"},
	})
	if err != nil {
		t.Fatalf("degrade ingest error: %v", err)
	}
	if !degrade.Degraded || degrade.Dropped {
		t.Fatalf("tier0 event should degrade not drop: %+v", degrade)
	}
}

func TestBackpressureAndSampling(t *testing.T) {
	k := NewKernel(Config{
		HighLoadThreshold:     0.8,
		Tier2HighLoadDropRate: 1.0,
		Tier3HighLoadDropRate: 1.0,
	})
	k.SetBackpressureLevel(BackpressureLevel1)
	resp, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-bp-1",
		EventType:       "run.advanced",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-bp",
		RiskTier:        RiskLow,
		EvidenceTier:    Tier2,
		SystemLoad:      0.95,
		Payload:         map[string]interface{}{"x": "drop"},
	})
	if err != nil {
		t.Fatalf("ingest under backpressure: %v", err)
	}
	if !resp.Dropped {
		t.Fatalf("expected dropped under backpressure/sampling: %+v", resp)
	}

	k.SetBackpressureLevel(BackpressureLevel3)
	resp2, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-bp-2",
		EventType:       "decision.runtime.evaluated",
		SchemaVersion:   1,
		SourceComponent: "decision_kernel",
		RunID:           "run-bp",
		RiskTier:        RiskCritical,
		EvidenceTier:    Tier0,
		Payload:         map[string]interface{}{"x": "keep"},
	})
	if err != nil {
		t.Fatalf("critical tier0 ingest under level3: %v", err)
	}
	if resp2.Dropped {
		t.Fatalf("critical tier0 must not drop under level3")
	}
}

func TestRootCausePackModes(t *testing.T) {
	k := NewKernel(Config{})
	if _, err := k.BuildRootCausePack("", RootCausePackModeMinimal); err == nil {
		t.Fatalf("expected run_id validation error")
	}
	if _, err := k.BuildRootCausePack("run-miss", RootCausePackMode("x")); err == nil {
		t.Fatalf("expected mode validation error")
	}
	if _, err := k.BuildRootCausePack("run-miss", RootCausePackModeMinimal); err == nil {
		t.Fatalf("expected run not found error")
	}

	_, _ = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-rc-1",
		EventType:       "run.created",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-rc",
	})
	_, _ = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-rc-2",
		EventType:       "irreversible_progress_violation_event",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-rc",
	})
	_, _ = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-rc-3",
		EventType:       "decision.runtime.evaluated",
		SchemaVersion:   1,
		SourceComponent: "decision_kernel",
		RunID:           "run-rc",
		DecisionID:      "dec-rc-1",
		Usage: &UsageInput{
			ResourceType: "tokens",
			UsageAmount:  1,
			Unit:         "token",
		},
	})

	minPack, err := k.BuildRootCausePack("run-rc", RootCausePackModeMinimal)
	if err != nil {
		t.Fatalf("build minimal pack: %v", err)
	}
	if minPack.RunID != "run-rc" || len(minPack.CriticalPath) == 0 {
		t.Fatalf("minimal pack mismatch: %+v", minPack)
	}

	fullPack, err := k.BuildRootCausePack("run-rc", RootCausePackModeFull)
	if err != nil {
		t.Fatalf("build full pack: %v", err)
	}
	if fullPack.DecisionGraph == nil || len(fullPack.Ledger) == 0 {
		t.Fatalf("full pack should include graph and ledger")
	}
}

func TestSweepRetentionAndOutboxTrim(t *testing.T) {
	base := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)
	now := base
	k := NewKernel(Config{
		Clock:           func() time.Time { return now },
		EventRetention:  1 * time.Minute,
		OutboxRetention: 1 * time.Minute,
		OutboxMaxEvents: 2,
	})

	_, _ = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-old",
		EventType:       "run.created",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-sweep",
		EventTS:         base.Add(-5 * time.Minute),
		EvidenceTier:    Tier3,
		Payload:         map[string]interface{}{"x": "old"},
	})
	_, _ = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-new",
		EventType:       "run.advanced",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-sweep",
		EventTS:         base,
	})

	now = base.Add(5 * time.Minute)
	sweep := k.SweepRetention(now)
	if sweep.DeletedEvents == 0 && sweep.WarmArchived == 0 && sweep.ColdArchived == 0 {
		t.Fatalf("expected old events to be deleted or archived")
	}
	if _, ok := k.GetRunEvidence("run-sweep"); ok {
		// run-sweep may still exist if newer event remained; in this case ensure outbox works.
	}

	events := k.Outbox()
	if len(events) > 2 {
		t.Fatalf("outbox should be trimmed to <=2, got=%d", len(events))
	}
	drained := k.DrainOutbox(1)
	if len(drained) > 1 {
		t.Fatalf("drain limit mismatch")
	}
}

func TestReplayPackManifestAndDecisionLogHistoryAndGlobalIntegrity(t *testing.T) {
	k := NewKernel(Config{})
	_, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-manifest-1",
		EventType:       "decision.runtime.evaluated",
		SchemaVersion:   1,
		SourceComponent: "decision_kernel",
		RunID:           "run-manifest",
		StepID:          "step-1",
		DecisionID:      "dec-manifest-1",
		Payload: map[string]interface{}{
			"decision":                   "allow",
			"snapshot_ref":               "snap://run/1",
			"policy_bundle_snapshot_ref": "pol://bundle/1",
			"feature_snapshot_id":        "fs://1",
			"execution_receipt_ref":      "rcpt://1",
		},
	})
	if err != nil {
		t.Fatalf("ingest manifest event #1: %v", err)
	}
	_, err = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-manifest-2",
		EventType:       "decision.runtime.evaluated",
		SchemaVersion:   1,
		SourceComponent: "decision_kernel",
		RunID:           "run-manifest",
		StepID:          "step-2",
		DecisionID:      "dec-manifest-1",
		Payload: map[string]interface{}{
			"decision":      "require_approval",
			"snapshot_hash": "snap_hash_2",
			"receipt_ref":   "rcpt://2",
		},
	})
	if err != nil {
		t.Fatalf("ingest manifest event #2: %v", err)
	}

	pack, err := k.BuildReplayPack("run-manifest", ReplayPackModeFull)
	if err != nil {
		t.Fatalf("build replay pack: %v", err)
	}
	if len(pack.SnapshotRefs) == 0 || len(pack.PolicyBundleRefs) == 0 || len(pack.FeatureSnapshotRefs) == 0 || len(pack.AdapterReceiptRefs) == 0 || len(pack.DecisionRefs) == 0 {
		t.Fatalf("expected replay manifest refs to be populated: %+v", pack)
	}
	if strings.TrimSpace(pack.GlobalIntegrityRootHash) == "" {
		t.Fatalf("global integrity root hash should be populated")
	}

	h, ok := k.GetDecisionLogHistory("dec-manifest-1", 10)
	if !ok || len(h) < 2 {
		t.Fatalf("expected decision log history >=2, ok=%v len=%d", ok, len(h))
	}

	global := k.VerifyGlobalIntegrity()
	if !global.Verified || !global.GlobalVerified {
		t.Fatalf("global integrity should verify: %+v", global)
	}
}

func TestArchiveRotationAndGlobalIntegrityBranches(t *testing.T) {
	base := time.Date(2026, 4, 7, 13, 0, 0, 0, time.UTC)
	now := base
	k := NewKernel(Config{
		Clock:          func() time.Time { return now },
		EventRetention: 1 * time.Minute,
		WarmRetention:  2 * time.Minute,
		ColdRetention:  4 * time.Minute,
	})

	_, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-arch-hot",
		EventType:       "run.created",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-arch",
		EventTS:         base.Add(-90 * time.Second), // warm archive
	})
	if err != nil {
		t.Fatalf("ingest warm candidate: %v", err)
	}
	_, err = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-arch-cold",
		EventType:       "run.advanced",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-arch",
		EventTS:         base.Add(-3 * time.Minute), // cold archive
	})
	if err != nil {
		t.Fatalf("ingest cold candidate: %v", err)
	}
	_, err = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-arch-drop",
		EventType:       "run.failed",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-arch",
		EventTS:         base.Add(-10 * time.Minute), // deleted
	})
	if err != nil {
		t.Fatalf("ingest delete candidate: %v", err)
	}

	sweep := k.SweepRetention(now)
	if sweep.WarmArchived == 0 || sweep.ColdArchived == 0 || sweep.DeletedEvents == 0 {
		t.Fatalf("expected warm/cold/delete coverage in sweep: %+v", sweep)
	}

	// Age archives further to exercise rotateArchivesLocked warm->cold and cold->delete.
	now = now.Add(10 * time.Minute)
	sweep2 := k.SweepRetention(now)
	if sweep2.DeletedEvents == 0 {
		t.Fatalf("expected aged archives to be deleted after cold retention")
	}

	if _, ok := k.GetDecisionLogHistory("missing-decision", 10); ok {
		t.Fatalf("missing decision history should not exist")
	}

	// Global integrity failure branch by tampering indexed event integrity hash.
	k2 := NewKernel(Config{})
	_, err = k2.IngestEvent(IngestEventRequest{
		EventID:         "evt-global-1",
		EventType:       "run.created",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-global",
	})
	if err != nil {
		t.Fatalf("ingest global event: %v", err)
	}
	k2.mu.Lock()
	for id, e := range k2.eventsByID {
		e.IntegrityHash = "tampered"
		k2.eventsByID[id] = e
		break
	}
	k2.mu.Unlock()
	global := k2.VerifyGlobalIntegrity()
	if global.Verified || global.GlobalVerified {
		t.Fatalf("tampered global integrity should fail: %+v", global)
	}

	// mismatch-count branch in global verifier
	k2.mu.Lock()
	k2.globalIntegrity.Count++
	k2.mu.Unlock()
	global2 := k2.VerifyGlobalIntegrity()
	if global2.Verified || global2.GlobalVerified {
		t.Fatalf("global integrity count mismatch should fail: %+v", global2)
	}
}

func TestEvidenceHelperBranchCoverage_Additional(t *testing.T) {
	k := NewKernel(Config{})

	// DeleteByDSAR validation + no-op branch
	if _, err := k.DeleteByDSAR(DSARDeleteRequest{RequestID: "dsar-x"}); err == nil {
		t.Fatalf("expected tenant required for DSAR delete")
	}
	noop, err := k.DeleteByDSAR(DSARDeleteRequest{RequestID: "dsar-y", TenantID: "tenant-none", PayloadRefPrefix: "obj://none"})
	if err != nil {
		t.Fatalf("unexpected dsar noop error: %v", err)
	}
	if noop.DeletedEvents != 0 && noop.RedactedEvents != 0 {
		t.Fatalf("expected noop DSAR result, got=%+v", noop)
	}

	// Aggregate ledger branches: invalid group_by + grouped queries + anomaly/no-anomaly.
	if _, err := k.AggregateLedger(LedgerAggregateRequest{GroupBy: "bad"}); err == nil {
		t.Fatalf("expected invalid group_by error")
	}
	now := time.Now()
	_, _ = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-ledger-a",
		EventType:       "decision.runtime.evaluated",
		SchemaVersion:   1,
		SourceComponent: "decision_kernel",
		RunID:           "run-ledger-a",
		TenantID:        "tenant-a",
		WorkflowID:      "wf-a",
		Usage: &UsageInput{
			ResourceType: "tokens",
			UsageAmount:  1,
			Unit:         "token",
			CostAmount:   1,
		},
	})
	_, _ = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-ledger-b",
		EventType:       "decision.runtime.evaluated",
		SchemaVersion:   1,
		SourceComponent: "decision_kernel",
		RunID:           "run-ledger-b",
		TenantID:        "tenant-a",
		WorkflowID:      "wf-a",
		Usage: &UsageInput{
			ResourceType: "tokens",
			UsageAmount:  20,
			Unit:         "token",
			CostAmount:   400,
		},
	})
	aggRun, err := k.AggregateLedger(LedgerAggregateRequest{GroupBy: "run", DetectAnomaly: true, WindowStart: now.Add(-time.Hour), WindowEnd: now.Add(time.Hour)})
	if err != nil || len(aggRun.Rows) == 0 {
		t.Fatalf("aggregate by run failed: rows=%d err=%v", len(aggRun.Rows), err)
	}
	aggTenant, err := k.AggregateLedger(LedgerAggregateRequest{GroupBy: "tenant"})
	if err != nil || len(aggTenant.Rows) == 0 {
		t.Fatalf("aggregate by tenant failed: rows=%d err=%v", len(aggTenant.Rows), err)
	}
	aggWorkflow, err := k.AggregateLedger(LedgerAggregateRequest{GroupBy: "workflow"})
	if err != nil || len(aggWorkflow.Rows) == 0 {
		t.Fatalf("aggregate by workflow failed: rows=%d err=%v", len(aggWorkflow.Rows), err)
	}

	// direct helper branches
	if bad := findFirstBadEvent([]CanonicalEvent{{EventID: "e1", EventType: "run.created"}, {EventID: "e2", EventType: "run.error"}}); bad == nil || bad.EventID != "e2" {
		t.Fatalf("findFirstBadEvent fallback branch mismatch")
	}
	if bad := findFirstBadEvent([]CanonicalEvent{{EventID: "e3", EventType: "run.advanced", Payload: map[string]interface{}{"decision": "fail_closed"}}}); bad == nil || bad.EventID != "e3" {
		t.Fatalf("findFirstBadEvent strong branch mismatch")
	}
	if isStrongFailureEvent(CanonicalEvent{EventType: "run.ok", Payload: map[string]interface{}{"decision": "allow"}}) {
		t.Fatalf("non-failure event should not be strong failure")
	}
	score, reason := detectLedgerAnomaly(LedgerAggregateRow{EntryCount: 1, AvgUsage: 10, MaxUsage: 10, TotalUsage: 10, TotalCost: 1})
	if score != 0 || reason != "" {
		t.Fatalf("non-anomalous ledger row should score 0, got score=%.2f reason=%s", score, reason)
	}
}

func TestEvidenceHelperBranchCoverage(t *testing.T) {
	if NewService(Config{}) == nil {
		t.Fatalf("new service should not be nil")
	}
	if got := (&KernelError{Message: "x"}).Error(); got != "x" {
		t.Fatalf("kernel error string mismatch: %s", got)
	}

	if shouldDropByRate("a", 0) {
		t.Fatalf("rate=0 should never drop")
	}
	_ = shouldDropByRate("mid", 0.5)
	if !shouldDropByRate("a", 1) {
		t.Fatalf("rate=1 should always drop")
	}

	if fallbackHashID("event-x", "run-x", "event.type") == "" {
		t.Fatalf("fallback hash id should not be empty")
	}

	events := []CanonicalEvent{
		{EventID: "2", EventTS: time.Date(2026, 4, 7, 14, 0, 2, 0, time.UTC)},
		{EventID: "1", EventTS: time.Date(2026, 4, 7, 14, 0, 1, 0, time.UTC)},
	}
	sortEventsByTS(events)
	if events[0].EventID != "1" {
		t.Fatalf("sort events by ts failed")
	}
	eventsTie := []CanonicalEvent{
		{EventID: "b", EventTS: time.Date(2026, 4, 7, 14, 0, 1, 0, time.UTC)},
		{EventID: "a", EventTS: time.Date(2026, 4, 7, 14, 0, 1, 0, time.UTC)},
	}
	sortEventsByTS(eventsTie)
	if eventsTie[0].EventID != "a" {
		t.Fatalf("sort events tie branch failed")
	}

	if inferNodeType("decision_kernel", "approval.case.created") != "approval" {
		t.Fatalf("infer node type approval mismatch")
	}
	if inferNodeType("decision_kernel", "context.resolve") != "context" {
		t.Fatalf("infer node type context mismatch")
	}
	if inferNodeType("decision_kernel", "policy.eval") != "policy" {
		t.Fatalf("infer node type policy mismatch")
	}
	if inferNodeType("decision_kernel", "schedule.admit") != "schedule" {
		t.Fatalf("infer node type schedule mismatch")
	}
	if inferNodeType("decision_kernel", "release.check") != "release" {
		t.Fatalf("infer node type release mismatch")
	}
	if inferNodeType("decision_kernel", "decision.runtime.evaluated") != "decision" {
		t.Fatalf("infer node type decision mismatch")
	}
	if inferNodeType("run_kernel", "run.tool.called") != "tool" {
		t.Fatalf("infer node type tool mismatch")
	}
	if inferNodeType("adapter", "x") != "adapter" {
		t.Fatalf("infer node type fallback mismatch")
	}
	if inferEdgeType("approval.case.created") != "influenced_by" {
		t.Fatalf("infer edge type approval mismatch")
	}
	if inferEdgeType("decision.blocked") != "blocks" {
		t.Fatalf("infer edge type blocked mismatch")
	}
	if inferEdgeType("context.derived") != "influenced_by" {
		t.Fatalf("infer edge type derived mismatch")
	}
	if inferEdgeType("decision.override") != "overrides" {
		t.Fatalf("infer edge type override mismatch")
	}
	if inferEdgeType("run.advanced") != "depends_on" {
		t.Fatalf("infer edge type temporal mismatch")
	}

	if extractString(map[string]interface{}{"x": "v"}, "x") != "v" {
		t.Fatalf("extract string mismatch")
	}
	if extractString(map[string]interface{}{"x": 1}, "x") != "1" {
		t.Fatalf("extract string non-string should be formatted as string")
	}
	if extractFloat(map[string]interface{}{"x": int64(7)}, "x") != 7 {
		t.Fatalf("extract float int64 mismatch")
	}
	if extractFloat(map[string]interface{}{"x": float32(3.2)}, "x") != float64(float32(3.2)) {
		t.Fatalf("extract float float32 mismatch")
	}
	if extractFloat(map[string]interface{}{"x": int(4)}, "x") != 4 {
		t.Fatalf("extract float int mismatch")
	}
	if extractFloat(map[string]interface{}{"x": "bad"}, "x") != 0 {
		t.Fatalf("extract float non-number should be 0")
	}
	if got := extractStringSlice(map[string]interface{}{"x": []string{"a", " ", "b"}}, "x"); len(got) != 2 {
		t.Fatalf("extract string slice []string mismatch: %+v", got)
	}
	if got := extractStringSlice(map[string]interface{}{"x": []interface{}{"a", 1, " "}}, "x"); len(got) != 2 {
		t.Fatalf("extract string slice []interface{} mismatch: %+v", got)
	}
	if got := extractStringSlice(map[string]interface{}{"x": 123}, "x"); got != nil {
		t.Fatalf("extract string slice default branch should be nil")
	}
	if boolToFloat(false) != 0 || boolToFloat(true) != 1 {
		t.Fatalf("bool to float mismatch")
	}

	if hash, ok := safeHash(map[string]interface{}{"bad": make(chan int)}); ok || hash != "" {
		t.Fatalf("safe hash should fail for unsupported payload")
	}

	k := NewKernel(Config{})
	k.SetSourceBackpressureLevel("", BackpressureLevel1)
	k.SetTenantBackpressureLevel("", BackpressureLevel1)
	k.SetBackpressureLevel(-1)
	if got := atomic.LoadInt32(&k.backpressureLevel); got != BackpressureLevel0 {
		t.Fatalf("backpressure level clamp low mismatch: %d", got)
	}
	k.SetBackpressureLevel(99)
	if got := atomic.LoadInt32(&k.backpressureLevel); got != BackpressureLevel3 {
		t.Fatalf("backpressure level clamp high mismatch: %d", got)
	}
	k.SetSourceBackpressureLevel("run_kernel", 99)
	k.SetTenantBackpressureLevel("tenant-a", -1)

	if err := k.RegisterSource(SourceRegistration{}); err == nil {
		t.Fatalf("register source with empty component should fail")
	}
	if err := k.RegisterSource(SourceRegistration{SourceComponent: "x", MinSchema: 0}); err == nil {
		t.Fatalf("register source with invalid schema should fail")
	}

	if err := validateIngestRequest(IngestEventRequest{}); err == nil {
		t.Fatalf("validate ingest should fail on empty input")
	}
	if err := validateIngestRequest(IngestEventRequest{EventID: "e"}); err == nil {
		t.Fatalf("validate ingest should fail on missing event_type")
	}
	if err := validateIngestRequest(IngestEventRequest{EventID: "e", EventType: "t"}); err == nil {
		t.Fatalf("validate ingest should fail on missing source_component")
	}
	if err := validateIngestRequest(IngestEventRequest{EventID: "e", EventType: "t", SourceComponent: "s"}); err == nil {
		t.Fatalf("validate ingest should fail on missing run_id")
	}
	if err := validateIngestRequest(IngestEventRequest{EventID: "e", EventType: "t", SourceComponent: "s", RunID: "r"}); err != nil {
		t.Fatalf("validate ingest should pass on required fields: %v", err)
	}

	k2 := NewKernel(Config{
		OutboxMaxEvents: 1,
		OutboxRetention: 1 * time.Second,
	})
	now := time.Date(2026, 4, 7, 15, 0, 0, 0, time.UTC)
	k2.outbox = append(k2.outbox, KernelEvent{EventID: "old", EventTS: now.Add(-10 * time.Second)})
	k2.outbox = append(k2.outbox, KernelEvent{EventID: "new", EventTS: now})
	if trimmed := k2.trimOutboxLocked(now); trimmed == 0 {
		t.Fatalf("trim outbox should trim old/overflow events")
	}

	k2.graphNodesByRun["run-a"] = []DecisionGraphNode{{NodeID: "n1", NodeRef: "e1"}}
	if _, ok := k2.findNodeByEventIDLocked("run-a", "missing"); ok {
		t.Fatalf("find node missing branch failed")
	}
	k2.upsertGraphNodeLocked("run-a", DecisionGraphNode{NodeID: "n1", NodeRef: "e2"})
	if got := k2.graphNodesByRun["run-a"][0].NodeRef; got != "e2" {
		t.Fatalf("upsert existing node should refresh node ref, got=%s", got)
	}
	k2.upsertGraphEdgeLocked("run-a", DecisionGraphEdge{FromNodeID: "n1", ToNodeID: "n2", EdgeType: "temporal"})
	k2.upsertGraphEdgeLocked("run-a", DecisionGraphEdge{FromNodeID: "n1", ToNodeID: "n2", EdgeType: "temporal"})
	if got := len(k2.graphEdgesByRun["run-a"]); got != 1 {
		t.Fatalf("upsert duplicate edge should dedupe, got=%d", got)
	}
	if findEventTypeByID([]CanonicalEvent{{EventID: "e1", EventType: "t1"}}, "missing") != "" {
		t.Fatalf("findEventTypeByID missing branch should be empty")
	}
	if findPayloadRefByID([]CanonicalEvent{{EventID: "e1", PayloadRef: "p1"}}, "missing") != "" {
		t.Fatalf("findPayloadRefByID missing branch should be empty")
	}
	if ratio(1, 0) != 0 {
		t.Fatalf("ratio divide-by-zero branch should be 0")
	}
	if path := k2.traceCriticalPathLocked("run-a", "missing-node"); path != nil {
		t.Fatalf("traceCriticalPath missing node should be nil")
	}
}

func TestEvidenceAdditionalBranchesForCoverage(t *testing.T) {
	base := time.Date(2026, 4, 7, 16, 0, 0, 0, time.UTC)
	now := base
	k := NewKernel(Config{
		Clock:                 func() time.Time { return now },
		HighLoadThreshold:     0.1,
		Tier2HighLoadDropRate: 1.0,
		Tier3HighLoadDropRate: 1.0,
	})

	// Not found branches.
	if _, ok := k.GetDecisionGraph("missing-run"); ok {
		t.Fatalf("expected missing run graph to return false")
	}
	if _, ok := k.GetLedger("missing-run"); ok {
		t.Fatalf("expected missing run ledger to return false")
	}

	// Build a run with >8 nodes and >5 events to cover truncation branches.
	for i := 0; i < 9; i++ {
		_, err := k.IngestEvent(IngestEventRequest{
			EventID:         "evt-many-" + string(rune('a'+i)),
			EventType:       "run.advanced",
			SchemaVersion:   1,
			SourceComponent: "run_kernel",
			RunID:           "run-many",
			EventTS:         now.Add(time.Duration(i) * time.Second),
		})
		if err != nil {
			t.Fatalf("seed many events: %v", err)
		}
	}
	_, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-many-fail",
		EventType:       "run.error",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-many",
		EventTS:         now.Add(20 * time.Second),
	})
	if err != nil {
		t.Fatalf("seed failure event: %v", err)
	}
	packDefaultMode, err := k.BuildRootCausePack("run-many", "")
	if err != nil {
		t.Fatalf("build default mode pack: %v", err)
	}
	if packDefaultMode.Mode != RootCausePackModeMinimal {
		t.Fatalf("default mode should become minimal: %+v", packDefaultMode.Mode)
	}
	if len(packDefaultMode.CriticalPath) == 0 || len(packDefaultMode.KeyEvidences) == 0 {
		t.Fatalf("expected non-empty pack slices")
	}

	// Force fallback first_bad_node path by removing graph nodes for existing run.
	k.mu.Lock()
	delete(k.graphNodesByRun, "run-many")
	k.mu.Unlock()
	packFallbackNode, err := k.BuildRootCausePack("run-many", RootCausePackModeMinimal)
	if err != nil {
		t.Fatalf("build fallback-node pack: %v", err)
	}
	if packFallbackNode.FirstBadNode == "" {
		t.Fatalf("expected fallback first_bad_node")
	}

	// Payload integrity invalid path on accepted event.
	_, err = k.IngestEvent(IngestEventRequest{
		EventID:                  "evt-integrity-invalid",
		EventType:                "decision.runtime.evaluated",
		SchemaVersion:            1,
		SourceComponent:          "decision_kernel",
		RunID:                    "run-integrity",
		PayloadIntegrityRequired: true,
		Payload: map[string]interface{}{
			"bad": make(chan int),
		},
	})
	if err != nil {
		t.Fatalf("ingest payload-integrity-invalid event: %v", err)
	}

	// Backpressure level2/level3 drop branches.
	k.SetBackpressureLevel(BackpressureLevel2)
	respL2, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-l2",
		EventType:       "run.advanced",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-bp2",
		RiskTier:        RiskLow,
		EvidenceTier:    Tier1,
	})
	if err != nil || !respL2.Dropped {
		t.Fatalf("expected level2 drop, resp=%+v err=%v", respL2, err)
	}

	k.SetBackpressureLevel(BackpressureLevel3)
	respL3, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-l3",
		EventType:       "run.advanced",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-bp3",
		RiskTier:        RiskLow,
		EvidenceTier:    Tier1,
	})
	if err != nil || !respL3.Dropped {
		t.Fatalf("expected level3 drop, resp=%+v err=%v", respL3, err)
	}

	// Sampling tier2/tier3 high-load branches.
	k.SetBackpressureLevel(BackpressureLevel0)
	respS2, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-s2",
		EventType:       "run.advanced",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-s2",
		RiskTier:        RiskLow,
		EvidenceTier:    Tier2,
		SystemLoad:      0.99,
	})
	if err != nil || !respS2.Dropped {
		t.Fatalf("expected sampling drop tier2, resp=%+v err=%v", respS2, err)
	}
	respS3, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-s3",
		EventType:       "run.advanced",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-s3",
		RiskTier:        RiskLow,
		EvidenceTier:    Tier3,
		SystemLoad:      0.99,
	})
	if err != nil || !respS3.Dropped {
		t.Fatalf("expected sampling drop tier3, resp=%+v err=%v", respS3, err)
	}

	// Byte budget rejection branch.
	kBytes := NewKernel(Config{
		MaxEventsPerRun:        100,
		MaxEvidenceBytesPerRun: 100,
	})
	respBytes, err := kBytes.IngestEvent(IngestEventRequest{
		EventID:         "evt-bytes-1",
		EventType:       "run.advanced",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-bytes",
		RiskTier:        RiskLow,
		EvidenceTier:    Tier3,
		Payload: map[string]interface{}{
			"big": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		},
	})
	if err != nil || !respBytes.Dropped {
		t.Fatalf("expected byte-budget drop, resp=%+v err=%v", respBytes, err)
	}

	// Sweep with mixed old/new events to hit kept append and recalc branches.
	now2 := base
	kSweep := NewKernel(Config{
		Clock:          func() time.Time { return now2 },
		EventRetention: 2 * time.Minute,
	})
	_, _ = kSweep.IngestEvent(IngestEventRequest{
		EventID:         "evt-sweep-old",
		EventType:       "run.created",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-sweep-mixed",
		EventTS:         now2.Add(-10 * time.Minute),
	})
	_, _ = kSweep.IngestEvent(IngestEventRequest{
		EventID:         "evt-sweep-new",
		EventType:       "run.advanced",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-sweep-mixed",
		EventTS:         now2,
	})
	sweepMixed := kSweep.SweepRetention(now2)
	if sweepMixed.DeletedEvents == 0 && sweepMixed.WarmArchived == 0 && sweepMixed.ColdArchived == 0 {
		t.Fatalf("expected at least one deleted or archived old event")
	}
	summaryMixed, ok := kSweep.GetRunEvidence("run-sweep-mixed")
	if !ok || summaryMixed.TotalEvents != 1 {
		t.Fatalf("expected one retained event after mixed sweep, summary=%+v ok=%v", summaryMixed, ok)
	}

	// Explicit helper branches not hit by runtime path.
	norm := normalizeIngestRequest(IngestEventRequest{
		EventID:         " e ",
		EventType:       " t ",
		SourceComponent: " RUN_KERNEL ",
		RunID:           " r ",
		SystemLoad:      -1,
	}, now)
	if norm.SystemLoad != 0 {
		t.Fatalf("normalize load lower clamp failed: %v", norm.SystemLoad)
	}
	norm2 := normalizeIngestRequest(IngestEventRequest{
		EventID:         "e2",
		EventType:       "t2",
		SourceComponent: "run_kernel",
		RunID:           "r2",
		SystemLoad:      2,
	}, now)
	if norm2.SystemLoad != 1 {
		t.Fatalf("normalize load upper clamp failed: %v", norm2.SystemLoad)
	}
	can := canonicalize(IngestEventRequest{
		EventID:         "evt-can",
		EventType:       "run.advanced",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-can",
	}, SourceRegistration{SourceComponent: "run_kernel", MinSchema: 1, DefaultTier: "not-valid"})
	if can.EvidenceTier != Tier1 {
		t.Fatalf("invalid default tier should fallback to tier1, got=%s", can.EvidenceTier)
	}

	if got := extractString(map[string]interface{}{"x": 1}, "missing"); got != "" {
		t.Fatalf("extractString missing key should be empty, got=%q", got)
	}
	if got := extractFloat(map[string]interface{}{"x": 1}, "missing"); got != 0 {
		t.Fatalf("extractFloat missing key should be 0, got=%v", got)
	}

	// Direct internal branch coverage for private helpers.
	k.mu.Lock()
	k.updateDecisionGraphLocked(CanonicalEvent{RunID: "", EventID: "no-run"}, now)
	k.updateLedgerLocked(CanonicalEvent{RunID: "run-ledger"}, &UsageInput{ResourceType: "token", Unit: ""}, now)
	k.mu.Unlock()

	// Overflow-only trim branch.
	kOverflow := NewKernel(Config{
		OutboxRetention: 0,
		OutboxMaxEvents: 1,
	})
	kOverflow.outbox = append(kOverflow.outbox, KernelEvent{EventID: "o1", EventTS: now})
	kOverflow.outbox = append(kOverflow.outbox, KernelEvent{EventID: "o2", EventTS: now})
	if trimmed := kOverflow.trimOutboxLocked(now); trimmed == 0 {
		t.Fatalf("expected overflow trim branch")
	}
}

func TestDecisionLogHistoryVersioningAndArchiveAPIs(t *testing.T) {
	now := time.Date(2026, 4, 7, 21, 0, 0, 0, time.UTC)
	clock := now
	k := NewKernel(Config{
		Clock:          func() time.Time { return clock },
		EventRetention: 1 * time.Minute,
		WarmRetention:  2 * time.Minute,
		ColdRetention:  4 * time.Minute,
	})

	_, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-ver-1",
		EventType:       "decision.runtime.evaluated",
		SchemaVersion:   1,
		SourceComponent: "decision_kernel",
		RunID:           "run-ver",
		DecisionID:      "dec-ver",
		Payload:         map[string]interface{}{"decision": "allow"},
		EventTS:         now.Add(-3 * time.Minute),
	})
	if err != nil {
		t.Fatalf("ingest #1: %v", err)
	}
	_, err = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-ver-2",
		EventType:       "decision.runtime.evaluated",
		SchemaVersion:   2,
		SourceComponent: "decision_kernel",
		RunID:           "run-ver",
		DecisionID:      "dec-ver",
		Payload:         map[string]interface{}{"decision": "require_approval"},
		EventTS:         now.Add(-90 * time.Second),
	})
	if err != nil {
		t.Fatalf("ingest #2: %v", err)
	}

	h, ok := k.GetDecisionLogHistory("dec-ver", 10)
	if !ok || len(h) < 2 {
		t.Fatalf("history should include versions, ok=%v len=%d", ok, len(h))
	}
	if h[len(h)-1].Version <= h[0].Version {
		t.Fatalf("expected monotonic decision log versions: %+v", h)
	}
	if h[len(h)-1].SourceEventID == "" || h[len(h)-1].SourceSchemaVersion == 0 {
		t.Fatalf("expected source metadata on decision log: %+v", h[len(h)-1])
	}
	if h[len(h)-1].SourceComponent == "" || h[len(h)-1].SourceEventTS.IsZero() {
		t.Fatalf("expected source component/time metadata on decision log: %+v", h[len(h)-1])
	}

	sweep := k.SweepRetention(now)
	if sweep.WarmArchived == 0 && sweep.ColdArchived == 0 {
		t.Fatalf("expected events to move into archive tiers, got=%+v", sweep)
	}
	summary := k.GetArchiveSummary("run-ver")
	if summary.WarmEventCount+summary.ColdEventCount == 0 {
		t.Fatalf("archive summary should expose archived events: %+v", summary)
	}
	if _, err := k.ExportArchive("", "warm", 10); err == nil {
		t.Fatalf("export archive without run_id should fail")
	}
	if _, err := k.ExportArchive("run-ver", "bad", 10); err == nil {
		t.Fatalf("export archive with invalid tier should fail")
	}
	arch, err := k.ExportArchive("run-ver", "warm", 10)
	if err != nil {
		t.Fatalf("export warm archive: %v", err)
	}
	if arch.EventCount > 0 && strings.TrimSpace(arch.IntegrityHash) == "" {
		t.Fatalf("archive export should include integrity hash when events exist")
	}
}

func TestGlobalIntegrityAnchorLifecycle(t *testing.T) {
	k := NewKernel(Config{GlobalAnchorHistoryMax: 2})
	_, _ = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-anc-1",
		EventType:       "run.created",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-anc",
	})
	a1 := k.CreateGlobalIntegrityAnchor("checkpoint-1")
	a2 := k.CreateGlobalIntegrityAnchor("checkpoint-2")
	a3 := k.CreateGlobalIntegrityAnchor("checkpoint-3")
	if a1.AnchorID == "" || a2.AnchorID == "" || a3.AnchorID == "" {
		t.Fatalf("anchor ids should not be empty")
	}
	anchors := k.ListGlobalIntegrityAnchors(10)
	if len(anchors) != 2 {
		t.Fatalf("anchor history should be capped to 2, got=%d", len(anchors))
	}
	if anchors[0].AnchorID != a2.AnchorID || anchors[1].AnchorID != a3.AnchorID {
		t.Fatalf("expected last two anchors, got=%+v", anchors)
	}
}

func TestGlobalIntegrityExternalAttestorBranches(t *testing.T) {
	kSuccess := NewKernel(Config{
		ExternalAnchorAttestor: func(req ExternalAnchorRequest) (ExternalAttestation, error) {
			return ExternalAttestation{
				Provider:       "mock-attestor",
				AttestationRef: "att://ok/" + req.AnchorID,
				Signature:      "sig-ok",
				WormRef:        "worm://ok/" + req.AnchorID,
				AttestedAt:     time.Now().UTC(),
				Status:         "attested",
			}, nil
		},
	})
	_, _ = kSuccess.IngestEvent(IngestEventRequest{
		EventID:         "evt-ext-attestor-1",
		EventType:       "run.created",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-ext-attestor",
	})
	anchor := kSuccess.CreateGlobalIntegrityAnchor("external-success")
	if anchor.AnchorKind != "external_attested" || anchor.Attestation == nil || anchor.Attestation.Status != "attested" {
		t.Fatalf("expected external attested anchor, got=%+v", anchor)
	}

	kFail := NewKernel(Config{
		ExternalAnchorAttestor: func(req ExternalAnchorRequest) (ExternalAttestation, error) {
			return ExternalAttestation{Provider: "mock-attestor"}, errors.New("downstream unavailable")
		},
	})
	_, _ = kFail.IngestEvent(IngestEventRequest{
		EventID:         "evt-ext-attestor-2",
		EventType:       "run.created",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-ext-attestor-fail",
	})
	anchorFail := kFail.CreateGlobalIntegrityAnchor("external-fail")
	if anchorFail.AnchorKind != "external_attest_failed" || anchorFail.Attestation == nil || anchorFail.Attestation.Status != "failed" {
		t.Fatalf("expected external failed anchor, got=%+v", anchorFail)
	}
}

func TestArchiveExportContractSemantics(t *testing.T) {
	now := time.Date(2026, 4, 7, 23, 0, 0, 0, time.UTC)
	k := NewKernel(Config{
		Clock:          func() time.Time { return now },
		EventRetention: 1 * time.Minute,
		WarmRetention:  5 * time.Minute,
		ColdRetention:  30 * time.Minute,
	})
	_, _ = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-arc-contract-1",
		EventType:       "decision.runtime.evaluated",
		SchemaVersion:   1,
		SourceComponent: "decision_kernel",
		RunID:           "run-arc-contract",
		EvidenceTier:    Tier1,
		EvidenceGrade:   GradeAudit,
		EventTS:         now.Add(-3 * time.Minute),
		PayloadRef:      "obj://payload/1",
		Payload:         map[string]interface{}{"decision": "allow"},
	})
	_, _ = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-arc-contract-2",
		EventType:       "run.advanced",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-arc-contract",
		EvidenceTier:    Tier2,
		EvidenceGrade:   GradeOperational,
		EventTS:         now.Add(-3 * time.Minute),
		PayloadRef:      "obj://payload/2",
		Payload:         map[string]interface{}{"state": "running"},
	})
	_ = k.SweepRetention(now)
	// Force tombstone/redacted signal in archive.
	k.mu.Lock()
	if len(k.warmArchiveByRun["run-arc-contract"]) > 0 {
		k.warmArchiveByRun["run-arc-contract"][0].PayloadTombstoned = true
		k.warmArchiveByRun["run-arc-contract"][0].RedactionReason = "dsar_tombstoned"
	}
	k.mu.Unlock()
	exported, err := k.ExportArchive("run-arc-contract", "warm", 100)
	if err != nil {
		t.Fatalf("export archive: %v", err)
	}
	if exported.Contract.ContractVersion != 1 || exported.Contract.IntegrityKind == "" {
		t.Fatalf("archive contract missing required fields: %+v", exported.Contract)
	}
	if strings.TrimSpace(exported.ChainRootHash) == "" {
		t.Fatalf("expected chain root hash for archive export")
	}
	if !exported.Contract.ContainsTombstoned || !exported.Contract.ContainsRedacted {
		t.Fatalf("expected tombstone/redaction flags to be set: %+v", exported.Contract)
	}
}

func TestArchiveExportContractRequiresExternalAnchorForAudit(t *testing.T) {
	now := time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC)
	attestor := func(req ExternalAnchorRequest) (ExternalAttestation, error) {
		return ExternalAttestation{
			Provider:       "mock-attestor",
			AttestationRef: "att://ok/" + req.AnchorID,
			Signature:      "sig-ok",
			WormRef:        "worm://ok/" + req.AnchorID,
			AttestedAt:     now,
			Status:         "attested",
		}, nil
	}
	k := NewKernel(Config{
		Clock:                         func() time.Time { return now },
		EventRetention:                1 * time.Minute,
		WarmRetention:                 5 * time.Minute,
		ColdRetention:                 30 * time.Minute,
		RequireExternalAnchorForAudit: true,
		ExternalAnchorAttestor:        attestor,
	})
	_, _ = k.IngestEvent(IngestEventRequest{
		EventID:                  "evt-arc-anchor-1",
		EventType:                "decision.runtime.evaluated",
		SchemaVersion:            1,
		SourceComponent:          "decision_kernel",
		RunID:                    "run-arc-anchor",
		EvidenceTier:             Tier1,
		EvidenceGrade:            GradeAudit,
		PayloadIntegrityRequired: true,
		EventTS:                  now.Add(-3 * time.Minute),
		PayloadRef:               "obj://payload/audit-1",
		Payload:                  map[string]interface{}{"decision": "allow"},
	})
	_ = k.SweepRetention(now)

	noAnchor, err := k.ExportArchive("run-arc-anchor", "warm", 100)
	if err != nil {
		t.Fatalf("export archive without anchor: %v", err)
	}
	if noAnchor.Contract.AuditArtifactEligible {
		t.Fatalf("audit artifact should be ineligible before external anchor attestation")
	}
	if !noAnchor.Contract.ExternalAnchorRequired || noAnchor.Contract.ExternalAnchorSatisfied {
		t.Fatalf("unexpected external anchor contract status before attestation: %+v", noAnchor.Contract)
	}

	anchor := k.CreateGlobalIntegrityAnchor("audit-attest")
	if anchor.AnchorKind != "external_attested" {
		t.Fatalf("expected external attested anchor, got=%+v", anchor)
	}

	withAnchor, err := k.ExportArchive("run-arc-anchor", "warm", 100)
	if err != nil {
		t.Fatalf("export archive with anchor: %v", err)
	}
	if !withAnchor.Contract.AuditArtifactEligible || !withAnchor.Contract.ExternalAnchorSatisfied {
		t.Fatalf("audit artifact should be eligible after external anchor attestation: %+v", withAnchor.Contract)
	}
	if withAnchor.Contract.ExternalAnchorID == "" || withAnchor.Contract.ExternalAnchorKind == "" {
		t.Fatalf("missing external anchor binding details: %+v", withAnchor.Contract)
	}
}

func TestDecisionLogSupersededVersionSemantics(t *testing.T) {
	k := NewKernel(Config{})
	_, _ = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-dec-sup-1",
		EventType:       "decision.runtime.evaluated",
		SchemaVersion:   1,
		SourceComponent: "decision_kernel",
		RunID:           "run-dec-sup",
		DecisionID:      "dec-sup",
		Payload:         map[string]interface{}{"decision": "allow"},
	})
	_, _ = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-dec-sup-2",
		EventType:       "decision.runtime.evaluated",
		SchemaVersion:   1,
		SourceComponent: "decision_kernel",
		RunID:           "run-dec-sup",
		DecisionID:      "dec-sup",
		Payload:         map[string]interface{}{"decision": "review_required"},
	})
	h, ok := k.GetDecisionLogHistory("dec-sup", 10)
	if !ok || len(h) < 2 {
		t.Fatalf("expected decision log history with superseded semantics")
	}
	if h[0].SupersededByVersion != h[1].Version {
		t.Fatalf("expected first version to be superseded by second: v1=%d superseded_by=%d v2=%d", h[0].Version, h[0].SupersededByVersion, h[1].Version)
	}
	if h[1].DerivedFromEventID == "" {
		t.Fatalf("expected derived_from_event_id to be populated")
	}
}

func TestArchiveSummaryExportAndRotateCoverage(t *testing.T) {
	base := time.Date(2026, 4, 7, 22, 0, 0, 0, time.UTC)
	now := base
	k := NewKernel(Config{
		Clock:          func() time.Time { return now },
		EventRetention: 1 * time.Minute,
		WarmRetention:  5 * time.Minute,
		ColdRetention:  10 * time.Minute,
	})

	// Empty archive branches.
	if out := k.GetArchiveSummary("run-none"); out.HotEventCount != 0 || out.WarmEventCount != 0 || out.ColdEventCount != 0 {
		t.Fatalf("unexpected non-empty summary for missing run: %+v", out)
	}
	if anchors := k.ListGlobalIntegrityAnchors(0); len(anchors) != 0 {
		t.Fatalf("empty anchor list expected")
	}

	_, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-arc-1",
		EventType:       "run.created",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-arc-a",
		EventTS:         base.Add(-2 * time.Minute), // warm
	})
	if err != nil {
		t.Fatalf("ingest arc-1: %v", err)
	}
	_, err = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-arc-2",
		EventType:       "run.created",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-arc-b",
		EventTS:         base.Add(-20 * time.Minute), // delete
	})
	if err != nil {
		t.Fatalf("ingest arc-2: %v", err)
	}
	k.SweepRetention(now)

	global := k.GetArchiveSummary("")
	if global.WarmEventCount == 0 && global.ColdEventCount == 0 {
		t.Fatalf("expected non-empty global archive summary: %+v", global)
	}
	perRun := k.GetArchiveSummary("run-arc-a")
	if perRun.WarmEventCount+perRun.ColdEventCount == 0 {
		t.Fatalf("expected archived events for run-arc-a: %+v", perRun)
	}

	// Export no-data branch.
	emptyExport, err := k.ExportArchive("run-none", "warm", 10)
	if err != nil {
		t.Fatalf("empty export should still succeed: %v", err)
	}
	if emptyExport.EventCount != 0 {
		t.Fatalf("expected empty export for run-none, got=%d", emptyExport.EventCount)
	}
	// Export with default limit branch.
	warmExport, err := k.ExportArchive("run-arc-a", "warm", 0)
	if err != nil {
		t.Fatalf("warm export: %v", err)
	}
	if warmExport.EventCount > 0 && strings.TrimSpace(warmExport.IntegrityHash) == "" {
		t.Fatalf("warm export should include integrity hash")
	}

	// Force rotateArchivesLocked branches for keep/move/delete on warm+cold.
	k.mu.Lock()
	k.warmArchiveByRun["run-rotate"] = []CanonicalEvent{
		{EventID: "w-keep", EventTS: now.Add(-2 * time.Minute)},
		{EventID: "w-move", EventTS: now.Add(-8 * time.Minute)},
	}
	k.coldArchiveByRun["run-rotate"] = []CanonicalEvent{
		{EventID: "c-keep", EventTS: now.Add(-9 * time.Minute)},
		{EventID: "c-drop", EventTS: now.Add(-20 * time.Minute)},
	}
	k.mu.Unlock()
	sweep := k.SweepRetention(now)
	if sweep.ColdArchived == 0 || sweep.DeletedEvents == 0 {
		t.Fatalf("expected cold archive move and delete during rotate: %+v", sweep)
	}
}

func TestSweepRetentionRebuildsDecisionGraphAndLedgerConsistency(t *testing.T) {
	base := time.Date(2026, 4, 7, 17, 0, 0, 0, time.UTC)
	k := NewKernel(Config{
		Clock:          func() time.Time { return base },
		EventRetention: 2 * time.Minute,
	})

	// Old decision event with decision log reference should be swept.
	_, _ = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-old-decision",
		EventType:       "decision.runtime.evaluated",
		SchemaVersion:   1,
		SourceComponent: "decision_kernel",
		RunID:           "run-ret",
		DecisionID:      "dec-old",
		EventTS:         base.Add(-10 * time.Minute),
		Payload: map[string]interface{}{
			"decision":      "allow",
			"rationale_ref": "obj://old",
		},
		Usage: &UsageInput{ResourceType: "tokens", UsageAmount: 10, Unit: "token"},
	})
	// New event survives.
	_, _ = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-new-decision",
		EventType:       "decision.runtime.evaluated",
		SchemaVersion:   1,
		SourceComponent: "decision_kernel",
		RunID:           "run-ret",
		DecisionID:      "dec-new",
		EventTS:         base,
		Payload: map[string]interface{}{
			"decision":      "allow",
			"rationale_ref": "obj://new",
		},
		Usage: &UsageInput{ResourceType: "tokens", UsageAmount: 5, Unit: "token"},
	})

	k.SweepRetention(base)

	if _, ok := k.GetDecisionLog("dec-old"); ok {
		t.Fatalf("old decision log should be removed by sweep rebuild")
	}
	if _, ok := k.GetDecisionLog("dec-new"); !ok {
		t.Fatalf("new decision log should remain")
	}
	if _, ok := k.eventsByID["evt-old-decision"]; ok {
		t.Fatalf("old event index should be compacted from eventsByID")
	}
	if _, ok := k.eventsByID["evt-new-decision"]; !ok {
		t.Fatalf("new event index should remain in eventsByID")
	}
	ledger, ok := k.GetLedger("run-ret")
	if !ok || len(ledger) != 1 {
		t.Fatalf("ledger should be rebuilt from surviving events only: ok=%v len=%d", ok, len(ledger))
	}
	graph, ok := k.GetDecisionGraph("run-ret")
	if !ok || len(graph.Nodes) == 0 {
		t.Fatalf("graph should be rebuilt from surviving events")
	}
}

func TestGraphBuilderUsesStableRefsAndParentEdges(t *testing.T) {
	k := NewKernel(Config{})
	_, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-parent",
		EventType:       "decision.runtime.evaluated",
		SchemaVersion:   1,
		SourceComponent: "decision_kernel",
		RunID:           "run-graph",
		DecisionID:      "dec-parent",
		Payload: map[string]interface{}{
			"decision_node_id": "node-parent",
			"decision":         "allow",
		},
	})
	if err != nil {
		t.Fatalf("ingest parent: %v", err)
	}
	_, err = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-child",
		EventType:       "approval.case.created",
		SchemaVersion:   1,
		SourceComponent: "decision_kernel",
		RunID:           "run-graph",
		DecisionID:      "dec-child",
		Payload: map[string]interface{}{
			"decision_node_id":         "node-child",
			"parent_decision_node_ids": []interface{}{"node-parent"},
			"decision":                 "require_approval",
		},
	})
	if err != nil {
		t.Fatalf("ingest child: %v", err)
	}
	graph, ok := k.GetDecisionGraph("run-graph")
	if !ok {
		t.Fatalf("graph missing")
	}
	foundParent := false
	foundChild := false
	foundCausal := false
	for _, n := range graph.Nodes {
		if n.NodeID == "node-parent" {
			foundParent = true
		}
		if n.NodeID == "node-child" {
			foundChild = true
		}
	}
	for _, e := range graph.Edges {
		if e.FromNodeID == "node-parent" && e.ToNodeID == "node-child" && e.EdgeType == "causal" {
			foundCausal = true
		}
	}
	if !foundParent || !foundChild || !foundCausal {
		t.Fatalf("expected stable nodes and causal edge, graph=%+v", graph)
	}
}

func TestCanonicalFallbackHashIsStableAndMetricsAreRich(t *testing.T) {
	k := NewKernel(Config{})
	// Stable fallback should not depend on wall-clock time.
	a := fallbackHashID("evt-x", "run-x", "run.created")
	b := fallbackHashID("evt-x", "run-x", "run.created")
	if a != b {
		t.Fatalf("fallback hash should be stable: a=%s b=%s", a, b)
	}

	_, _ = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-metrics-1",
		EventType:       "run.created",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-metrics",
		RiskTier:        RiskHigh,
		EvidenceTier:    Tier1,
	})
	s := k.MetricsSnapshot()
	if s.Rates == nil || s.Gauges == nil {
		t.Fatalf("metrics snapshot should include rates and gauges")
	}
	if _, ok := s.Rates["ingest_accept_rate"]; !ok {
		t.Fatalf("missing ingest_accept_rate")
	}
	if _, ok := s.Rates["high_risk_graph_complete_rate"]; !ok {
		t.Fatalf("missing high_risk_graph_complete_rate")
	}
	if _, ok := s.Gauges["outbox_depth"]; !ok {
		t.Fatalf("missing outbox_depth gauge")
	}
}

func TestScopedBackpressureSourceAndTenant(t *testing.T) {
	k := NewKernel(Config{})
	k.SetBackpressureLevel(BackpressureLevel0)
	k.SetSourceBackpressureLevel("run_kernel", BackpressureLevel2)
	k.SetTenantBackpressureLevel("tenant-hot", BackpressureLevel3)

	respBySource, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-scope-source",
		EventType:       "run.advanced",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-scope-1",
		TenantID:        "tenant-cold",
		RiskTier:        RiskLow,
		EvidenceTier:    Tier1,
	})
	if err != nil {
		t.Fatalf("ingest source-scoped bp: %v", err)
	}
	if !respBySource.Dropped {
		t.Fatalf("expected source-scoped backpressure drop")
	}

	respByTenant, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-scope-tenant",
		EventType:       "run.advanced",
		SchemaVersion:   1,
		SourceComponent: "adapter",
		RunID:           "run-scope-2",
		TenantID:        "tenant-hot",
		RiskTier:        RiskLow,
		EvidenceTier:    Tier1,
	})
	if err != nil {
		t.Fatalf("ingest tenant-scoped bp: %v", err)
	}
	if !respByTenant.Dropped {
		t.Fatalf("expected tenant-scoped backpressure drop")
	}
}

func TestReplayIntegrityDSARAndLedgerAggregate(t *testing.T) {
	base := time.Date(2026, 4, 7, 18, 0, 0, 0, time.UTC)
	k := NewKernel(Config{
		Clock: func() time.Time { return base },
	})

	_, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-ri-1",
		EventType:       "decision.runtime.evaluated",
		SchemaVersion:   1,
		SourceComponent: "decision_kernel",
		RunID:           "run-ri",
		StepID:          "step-1",
		DecisionID:      "dec-ri-1",
		TenantID:        "tenant-ri",
		WorkflowID:      "wf-ri",
		EvidenceTier:    Tier1,
		EvidenceGrade:   GradeAudit,
		PayloadRef:      "obj://pii/ri-1",
		Payload: map[string]interface{}{
			"decision": "allow",
		},
		Usage: &UsageInput{
			ResourceType: "tokens",
			UsageAmount:  50,
			Unit:         "token",
			CostAmount:   0.1,
		},
	})
	if err != nil {
		t.Fatalf("seed replay event: %v", err)
	}

	replayMin, err := k.BuildReplayPack("run-ri", ReplayPackModeMinimal)
	if err != nil {
		t.Fatalf("build replay minimal: %v", err)
	}
	if replayMin.EventCount == 0 || len(replayMin.Events) == 0 {
		t.Fatalf("replay minimal should contain events: %+v", replayMin)
	}
	if replayMin.Events[0].Payload != nil {
		t.Fatalf("minimal replay should omit payload")
	}

	replayFull, err := k.BuildReplayPack("run-ri", ReplayPackModeFull)
	if err != nil {
		t.Fatalf("build replay full: %v", err)
	}
	if replayFull.DecisionGraph == nil || len(replayFull.Ledger) == 0 {
		t.Fatalf("full replay should include graph and ledger")
	}
	if replayFull.IntegrityRootHash == "" {
		t.Fatalf("replay full should include integrity root hash")
	}

	verify, err := k.VerifyIntegrity("run-ri")
	if err != nil {
		t.Fatalf("verify integrity: %v", err)
	}
	if !verify.Verified {
		t.Fatalf("integrity should verify: %+v", verify)
	}

	agg, err := k.AggregateLedger(LedgerAggregateRequest{
		RunID:         "run-ri",
		GroupBy:       "resource_type",
		DetectAnomaly: true,
	})
	if err != nil {
		t.Fatalf("aggregate ledger: %v", err)
	}
	if len(agg.Rows) == 0 || agg.Rows[0].GroupKey == "" {
		t.Fatalf("aggregate rows missing: %+v", agg)
	}
	if _, err := k.AggregateLedger(LedgerAggregateRequest{GroupBy: "bad"}); err == nil {
		t.Fatalf("expected invalid group_by error")
	}

	dsar, err := k.DeleteByDSAR(DSARDeleteRequest{
		RequestID: "dsar-ri-1",
		TenantID:  "tenant-ri",
		RunID:     "run-ri",
	})
	if err != nil {
		t.Fatalf("dsar delete: %v", err)
	}
	if dsar.DeletedEvents+dsar.RedactedEvents == 0 {
		t.Fatalf("dsar should affect at least one event: %+v", dsar)
	}
	replayAfter, err := k.BuildReplayPack("run-ri", ReplayPackModeFull)
	if err != nil {
		t.Fatalf("replay after dsar: %v", err)
	}
	if len(replayAfter.Events) == 0 {
		t.Fatalf("replay after dsar should contain event")
	}
	// Tombstone redaction marker should be visible for downstream exports.
	if !replayAfter.Events[0].Redacted && replayAfter.Events[0].PayloadRef != "" {
		// allow hard delete path, but if event remains it must be redacted.
		t.Fatalf("remaining event should be redacted after dsar: %+v", replayAfter.Events[0])
	}
}

func TestEvidenceExtendedErrorAndBranchCoverage(t *testing.T) {
	k := NewKernel(Config{})

	// ReplayPack validation branches.
	if _, err := k.BuildReplayPack("", ReplayPackModeMinimal); err == nil {
		t.Fatalf("expected run_id required for replay pack")
	}
	if _, err := k.BuildReplayPack("missing-run", ReplayPackMode("bad")); err == nil {
		t.Fatalf("expected invalid replay mode")
	}
	if _, err := k.BuildReplayPack("missing-run", ReplayPackModeMinimal); err == nil {
		t.Fatalf("expected replay run not found")
	}

	// VerifyIntegrity validation branches.
	if _, err := k.VerifyIntegrity(""); err == nil {
		t.Fatalf("expected verify run_id required")
	}
	if _, err := k.VerifyIntegrity("missing-run"); err == nil {
		t.Fatalf("expected verify run not found")
	}

	// Seed events for integrity mismatch and DSAR hard delete branch.
	_, _ = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-ext-1",
		EventType:       "run.created",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-ext",
		StepID:          "step-1",
		TenantID:        "tenant-ext",
		WorkflowID:      "wf-ext",
		EvidenceTier:    Tier3,
		EvidenceGrade:   GradeOperational,
		PayloadRef:      "obj://ext/1",
		Payload:         map[string]interface{}{"x": 1},
		Usage:           &UsageInput{ResourceType: "cpu", UsageAmount: 1, Unit: "sec", CostAmount: 100},
	})
	_, _ = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-ext-2",
		EventType:       "run.advanced",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-ext",
		StepID:          "step-2",
		TenantID:        "tenant-ext",
		WorkflowID:      "wf-ext",
		EvidenceTier:    Tier1,
		EvidenceGrade:   GradeAudit,
		PayloadRef:      "obj://ext/2",
		Payload:         map[string]interface{}{"x": 2},
		Usage:           &UsageInput{ResourceType: "cpu", UsageAmount: 10, Unit: "sec", CostAmount: 200},
	})

	// Tamper one event to trigger integrity mismatch branch.
	k.mu.Lock()
	for i := range k.runs["run-ext"].Events {
		if k.runs["run-ext"].Events[i].EventID == "evt-ext-2" {
			k.runs["run-ext"].Events[i].IntegrityHash = "tampered"
			break
		}
	}
	k.mu.Unlock()
	verify, err := k.VerifyIntegrity("run-ext")
	if err != nil {
		t.Fatalf("verify integrity tampered run: %v", err)
	}
	if verify.Verified {
		t.Fatalf("expected verify failure for tampered chain: %+v", verify)
	}

	// DSAR validation and delete branches.
	if _, err := k.DeleteByDSAR(DSARDeleteRequest{}); err == nil {
		t.Fatalf("expected tenant required for dsar")
	}
	resp, err := k.DeleteByDSAR(DSARDeleteRequest{
		RequestID:        "dsar-ext-1",
		TenantID:         "tenant-ext",
		RunID:            "run-ext",
		PayloadRefPrefix: "obj://ext/",
	})
	if err != nil {
		t.Fatalf("dsar ext delete: %v", err)
	}
	if resp.DeletedEvents == 0 && resp.RedactedEvents == 0 {
		t.Fatalf("expected dsar to affect events: %+v", resp)
	}

	// Replay full should include reason codes when tombstone applied.
	replay, err := k.BuildReplayPack("run-ext", ReplayPackModeFull)
	if err != nil {
		t.Fatalf("replay full ext: %v", err)
	}
	_ = replay

	// Aggregate with all group-by branches and anomaly path.
	_, err = k.AggregateLedger(LedgerAggregateRequest{GroupBy: "run", DetectAnomaly: true})
	if err != nil {
		t.Fatalf("aggregate run group: %v", err)
	}
	_, err = k.AggregateLedger(LedgerAggregateRequest{GroupBy: "tenant", DetectAnomaly: true})
	if err != nil {
		t.Fatalf("aggregate tenant group: %v", err)
	}
	_, err = k.AggregateLedger(LedgerAggregateRequest{GroupBy: "workflow", DetectAnomaly: true})
	if err != nil {
		t.Fatalf("aggregate workflow group: %v", err)
	}
	outRes, err := k.AggregateLedger(LedgerAggregateRequest{GroupBy: "resource_type", DetectAnomaly: true})
	if err != nil {
		t.Fatalf("aggregate resource group: %v", err)
	}
	if len(outRes.Rows) == 0 {
		t.Fatalf("expected aggregate rows")
	}

	// Direct helper branches.
	if got := appendUniqueString([]string{"a"}, "a"); len(got) != 1 {
		t.Fatalf("appendUnique duplicate branch failed")
	}
	if got := appendUniqueString([]string{"a"}, ""); len(got) != 1 {
		t.Fatalf("appendUnique empty branch failed")
	}
	if !isPayloadTombstoned(map[string]time.Time{"obj://x": time.Now()}, "obj://x") {
		t.Fatalf("isPayloadTombstoned positive branch failed")
	}
	if isPayloadTombstoned(map[string]time.Time{}, "") {
		t.Fatalf("isPayloadTombstoned empty branch failed")
	}
	if appendReason("a", "b") != "a|b" {
		t.Fatalf("appendReason join branch failed")
	}
	if appendReason("", "b") != "b" {
		t.Fatalf("appendReason empty-existing branch failed")
	}
	if appendReason("a", "") != "a" {
		t.Fatalf("appendReason empty-next branch failed")
	}
	if score, reason := detectLedgerAnomaly(LedgerAggregateRow{EntryCount: 1, AvgUsage: 1, MaxUsage: 10, TotalUsage: 1, TotalCost: 20}); score == 0 || reason == "" {
		t.Fatalf("detectLedgerAnomaly burst+cost branch failed: score=%v reason=%s", score, reason)
	}
}

func TestHighRiskGraphCompletenessStrictRequiredTypesAndParentEdges(t *testing.T) {
	now := time.Date(2026, 4, 8, 11, 0, 0, 0, time.UTC)
	k := NewKernel(Config{
		Clock: func() time.Time { return now },
	})

	// run-fail: tool appears after final decision node; tool is not in decision ancestor closure.
	if _, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-strict-fail-1",
		EventType:       "decision.runtime.evaluated",
		SchemaVersion:   1,
		SourceComponent: "decision_kernel",
		RunID:           "run-strict-fail",
		StepID:          "s1",
		DecisionID:      "d1",
		RiskTier:        RiskHigh,
		EvidenceTier:    Tier0,
		EvidenceGrade:   GradeAudit,
		Payload: map[string]interface{}{
			"decision": "allow",
		},
	}); err != nil {
		t.Fatalf("ingest strict fail decision event: %v", err)
	}
	if _, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-strict-fail-2",
		EventType:       "tool.dispatch",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-strict-fail",
		StepID:          "s2",
		RiskTier:        RiskHigh,
		EvidenceTier:    Tier1,
		EvidenceGrade:   GradeOperational,
		Payload: map[string]interface{}{
			"tool": "x",
		},
	}); err != nil {
		t.Fatalf("ingest strict fail tool event: %v", err)
	}
	summaryFail, ok := k.GetRunEvidence("run-strict-fail")
	if !ok {
		t.Fatalf("expected summary for run-strict-fail")
	}
	if summaryFail.HighRiskGraphComplete {
		t.Fatalf("high-risk graph should be incomplete when tool node is outside final decision ancestry")
	}

	// run-pass: tool appears before decision and is causally linked to final decision.
	if _, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-strict-pass-1",
		EventType:       "tool.dispatch",
		SchemaVersion:   1,
		SourceComponent: "run_kernel",
		RunID:           "run-strict-pass",
		StepID:          "s1",
		RiskTier:        RiskHigh,
		EvidenceTier:    Tier1,
		EvidenceGrade:   GradeOperational,
		Payload: map[string]interface{}{
			"tool": "x",
		},
	}); err != nil {
		t.Fatalf("ingest strict pass tool event: %v", err)
	}
	if _, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-strict-pass-2",
		EventType:       "decision.runtime.evaluated",
		SchemaVersion:   1,
		SourceComponent: "decision_kernel",
		RunID:           "run-strict-pass",
		StepID:          "s2",
		DecisionID:      "d2",
		RiskTier:        RiskHigh,
		EvidenceTier:    Tier0,
		EvidenceGrade:   GradeAudit,
		Payload: map[string]interface{}{
			"decision": "allow",
		},
	}); err != nil {
		t.Fatalf("ingest strict pass decision event: %v", err)
	}
	summaryPass, ok := k.GetRunEvidence("run-strict-pass")
	if !ok {
		t.Fatalf("expected summary for run-strict-pass")
	}
	if !summaryPass.HighRiskGraphComplete {
		t.Fatalf("high-risk graph should be complete when tool node reaches final decision ancestry")
	}

	// run-parent-missing: explicit parent declaration exists but graph edge is missing.
	if _, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-parent-1",
		EventType:       "context.compiled",
		SchemaVersion:   1,
		SourceComponent: "decision_kernel",
		RunID:           "run-parent-missing",
		StepID:          "s1",
		RiskTier:        RiskHigh,
		EvidenceTier:    Tier0,
		EvidenceGrade:   GradeAudit,
		Payload: map[string]interface{}{
			"decision_node_id": "node_ctx",
		},
	}); err != nil {
		t.Fatalf("ingest parent context event: %v", err)
	}
	if _, err := k.IngestEvent(IngestEventRequest{
		EventID:         "evt-parent-2",
		EventType:       "decision.runtime.evaluated",
		SchemaVersion:   1,
		SourceComponent: "decision_kernel",
		RunID:           "run-parent-missing",
		StepID:          "s2",
		DecisionID:      "d-parent",
		RiskTier:        RiskHigh,
		EvidenceTier:    Tier0,
		EvidenceGrade:   GradeAudit,
		Payload: map[string]interface{}{
			"decision":                 "allow",
			"decision_node_id":         "node_dec",
			"parent_decision_node_ids": []interface{}{"node_ctx"},
		},
	}); err != nil {
		t.Fatalf("ingest parent decision event: %v", err)
	}
	k.mu.Lock()
	edges := k.graphEdgesByRun["run-parent-missing"]
	filtered := edges[:0]
	for _, e := range edges {
		if e.FromNodeID == "node_ctx" && e.ToNodeID == "node_dec" {
			continue
		}
		filtered = append(filtered, e)
	}
	k.graphEdgesByRun["run-parent-missing"] = filtered
	k.mu.Unlock()
	summaryParent, ok := k.GetRunEvidence("run-parent-missing")
	if !ok {
		t.Fatalf("expected summary for run-parent-missing")
	}
	if summaryParent.HighRiskGraphComplete {
		t.Fatalf("high-risk graph should be incomplete when explicit parent edge is missing")
	}
}

func TestEvidenceHelperCoverageForAnchorsSelectorsAndArchiveRoot(t *testing.T) {
	now := time.Date(2026, 4, 8, 11, 30, 0, 0, time.UTC)
	k := NewKernel(Config{
		Clock: func() time.Time { return now },
	})

	k.mu.Lock()
	if _, ok := k.latestExternalAnchorLocked(); ok {
		k.mu.Unlock()
		t.Fatalf("empty anchors must not produce external anchor")
	}
	k.globalAnchors = append(k.globalAnchors,
		IntegrityAnchor{AnchorID: "a1", AnchorKind: "kernel_internal"},
		IntegrityAnchor{AnchorID: "a1b", AnchorKind: "kernel_internal", Attestation: &ExternalAttestation{Status: "attested"}},
		IntegrityAnchor{AnchorID: "a2", AnchorKind: "external_attested", Attestation: nil},
		IntegrityAnchor{AnchorID: "a2b", AnchorKind: "external_attested", Attestation: &ExternalAttestation{Status: "failed"}},
		IntegrityAnchor{AnchorID: "a3", AnchorKind: "external_attested", Attestation: &ExternalAttestation{Status: "attested", AttestedAt: now}},
	)
	latest, ok := k.latestExternalAnchorLocked()
	k.mu.Unlock()
	if !ok || latest.AnchorID != "a3" {
		t.Fatalf("latest external attested anchor mismatch: ok=%v latest=%+v", ok, latest)
	}

	if _, ok := selectFinalDecisionNode(nil, nil); ok {
		t.Fatalf("empty node set must not select final decision node")
	}
	if _, ok := selectFinalDecisionNode([]DecisionGraphNode{
		{NodeID: "n1", NodeType: "context"},
	}, nil); ok {
		t.Fatalf("non-decision-like node set must not select final decision node")
	}
	if _, ok := selectFinalDecisionNode([]DecisionGraphNode{
		{NodeID: "d1", NodeType: "decision"},
		{NodeID: "d2", NodeType: "release"},
	}, nil); ok {
		t.Fatalf("multiple decision-like sinks must be rejected")
	}
	if n, ok := selectFinalDecisionNode([]DecisionGraphNode{
		{NodeID: "d1", NodeType: "decision"},
		{NodeID: "d2", NodeType: "release"},
	}, []DecisionGraphEdge{
		{FromNodeID: "d1", ToNodeID: "d2", EdgeType: "temporal"},
	}); !ok || n.NodeID != "d2" {
		t.Fatalf("expected unique final decision-like sink, got=%+v ok=%v", n, ok)
	}

	if root := buildArchiveChainRoot(nil); root != "" {
		t.Fatalf("empty archive root should be empty, got=%s", root)
	}
	root := buildArchiveChainRoot([]CanonicalEvent{
		{RunID: "run-arc", EventID: "e1", EventTS: now},
		{RunID: "run-arc", EventID: "e2", CanonicalHash: "c2", EventTS: now.Add(1 * time.Second)},
		{RunID: "run-arc", EventID: "e3", IntegrityHash: "i3", EventTS: now.Add(2 * time.Second)},
	})
	if strings.TrimSpace(root) == "" {
		t.Fatalf("archive chain root should be generated for non-empty events")
	}
}

func TestEvidenceMiscCoverageForArchiveAndBackpressure(t *testing.T) {
	now := time.Date(2026, 4, 8, 11, 40, 0, 0, time.UTC)
	k := NewKernel(Config{
		Clock: func() time.Time { return now },
	})
	k.SetSourceBackpressureLevel("decision_kernel", 1)
	k.SetSourceBackpressureLevel("decision_kernel", 99)
	k.SetSourceBackpressureLevel("", 2)
	k.SetTenantBackpressureLevel("tenant-a", 2)
	k.SetTenantBackpressureLevel("tenant-a", -1)
	k.SetTenantBackpressureLevel("", 2)

	k.mu.RLock()
	srcLevel := k.sourceBackpressure["decision_kernel"]
	tenantLevel := k.tenantBackpressure["tenant-a"]
	k.mu.RUnlock()
	if srcLevel != BackpressureLevel3 {
		t.Fatalf("source backpressure should clamp to level3, got=%d", srcLevel)
	}
	if tenantLevel != BackpressureLevel0 {
		t.Fatalf("tenant backpressure should clamp to level0, got=%d", tenantLevel)
	}
	k.SetSourceBackpressureLevel("decision_kernel", BackpressureLevel0)

	summary := k.GetArchiveSummary("")
	if summary.HotEventCount != 0 || summary.WarmEventCount != 0 || summary.ColdEventCount != 0 {
		t.Fatalf("empty archive summary should be zeroed: %+v", summary)
	}

	_, _ = k.IngestEvent(IngestEventRequest{
		EventID:         "evt-arc-cov-1",
		EventType:       "decision.runtime.evaluated",
		SchemaVersion:   1,
		SourceComponent: "decision_kernel",
		RunID:           "run-arc-cov",
		StepID:          "s1",
		DecisionID:      "d-arc-cov",
		RiskTier:        RiskLow,
		EvidenceTier:    Tier1,
		EvidenceGrade:   GradeOperational,
	})
	k.mu.Lock()
	k.warmArchiveByRun["run-arc-cov"] = []CanonicalEvent{
		{EventID: "evt-arc-warm", RunID: "run-arc-cov", EventType: "warm", EventTS: now},
	}
	k.coldArchiveByRun["run-arc-cov"] = []CanonicalEvent{
		{EventID: "evt-arc-cold", RunID: "run-arc-cov", EventType: "cold", EventTS: now},
	}
	k.mu.Unlock()
	sum2 := k.GetArchiveSummary("")
	if sum2.HotEventCount == 0 {
		t.Fatalf("archive summary should count hot events after ingest")
	}
	if sum2.WarmEventCount == 0 || sum2.ColdEventCount == 0 {
		t.Fatalf("archive summary should count warm/cold events: %+v", sum2)
	}
}

func TestEmitLockedAppliesOutboxTrimAndCounters(t *testing.T) {
	now := time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC)
	k := NewKernel(Config{
		Clock:           func() time.Time { return now },
		OutboxMaxEvents: 1,
		OutboxRetention: time.Minute,
	})
	k.mu.Lock()
	k.outbox = append(k.outbox, KernelEvent{
		EventID:       "evt-old",
		EventType:     "old.event",
		SchemaVersion: 1,
		EventTS:       now.Add(-2 * time.Hour),
		RunID:         "_global",
	})
	k.mu.Unlock()

	_ = k.CreateGlobalIntegrityAnchor("trim-test-1")
	_ = k.CreateGlobalIntegrityAnchor("trim-test-2")

	k.mu.RLock()
	defer k.mu.RUnlock()
	if len(k.outbox) == 0 {
		t.Fatalf("expected non-empty outbox after anchor emission")
	}
	if len(k.outbox) > 1 {
		t.Fatalf("outbox max events should be enforced, got=%d", len(k.outbox))
	}
	if k.counters["outbox_trimmed_total"] == 0 {
		t.Fatalf("outbox_trimmed_total should increase when emit path trims")
	}
}
