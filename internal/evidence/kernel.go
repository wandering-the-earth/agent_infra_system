package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type runEvidenceStats struct {
	Events int
	Bytes  int
}

type storedRunData struct {
	Events      []CanonicalEvent
	Stats       runEvidenceStats
	BySource    map[string]int
	ByTier      map[string]int
	ByGrade     map[string]int
	LastEventTS time.Time
	NeedsGraph  bool
}

type runIntegrityState struct {
	LastHash string
	Count    int
}

type Kernel struct {
	mu sync.RWMutex

	cfg Config

	registeredSources  map[string]SourceRegistration
	eventsByID         map[string]CanonicalEvent
	runs               map[string]*storedRunData
	decisionLogs       map[string]DecisionLog
	decisionLogHistory map[string][]DecisionLog
	graphNodesByRun    map[string][]DecisionGraphNode
	graphEdgesByRun    map[string][]DecisionGraphEdge
	lastNodeByRun      map[string]string
	ledgerByRun        map[string][]LedgerEntry
	warmArchiveByRun   map[string][]CanonicalEvent
	coldArchiveByRun   map[string][]CanonicalEvent
	outbox             []KernelEvent
	integrityByRun     map[string]runIntegrityState
	globalIntegrity    runIntegrityState
	globalAnchors      []IntegrityAnchor
	tombstonedRefs     map[string]time.Time

	backpressureLevel  int32
	sourceBackpressure map[string]int
	tenantBackpressure map[string]int
	maxObservedEventTS time.Time
	counters           map[string]float64
	eventSeq           uint64
	idSeq              uint64
}

// Service is an alias kept for naming parity with other kernels.
type Service = Kernel

func NewService(cfg Config) *Service {
	return NewKernel(cfg)
}

func NewKernel(cfg Config) *Kernel {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.MaxEventsPerRun <= 0 {
		cfg.MaxEventsPerRun = 2000
	}
	if cfg.MaxEvidenceBytesPerRun <= 0 {
		cfg.MaxEvidenceBytesPerRun = 8 * 1024 * 1024
	}
	if cfg.EventRetention <= 0 {
		cfg.EventRetention = 24 * time.Hour
	}
	if cfg.WarmRetention <= 0 {
		cfg.WarmRetention = 30 * 24 * time.Hour
	}
	if cfg.ColdRetention <= 0 {
		cfg.ColdRetention = 180 * 24 * time.Hour
	}
	if cfg.WarmRetention < cfg.EventRetention {
		cfg.WarmRetention = cfg.EventRetention
	}
	if cfg.ColdRetention < cfg.WarmRetention {
		cfg.ColdRetention = cfg.WarmRetention
	}
	if cfg.OutboxMaxEvents <= 0 {
		cfg.OutboxMaxEvents = 10000
	}
	if cfg.OutboxRetention <= 0 {
		cfg.OutboxRetention = 24 * time.Hour
	}
	if cfg.HighLoadThreshold <= 0 {
		cfg.HighLoadThreshold = 0.8
	}
	if cfg.Tier2HighLoadDropRate <= 0 {
		cfg.Tier2HighLoadDropRate = 0.5
	}
	if cfg.Tier3HighLoadDropRate <= 0 {
		cfg.Tier3HighLoadDropRate = 0.9
	}
	if cfg.DecisionLogHistoryMax <= 0 {
		cfg.DecisionLogHistoryMax = 128
	}
	if cfg.GlobalAnchorHistoryMax <= 0 {
		cfg.GlobalAnchorHistoryMax = 128
	}

	k := &Kernel{
		cfg:                cfg,
		registeredSources:  defaultSourceRegistrations(),
		eventsByID:         make(map[string]CanonicalEvent),
		runs:               make(map[string]*storedRunData),
		decisionLogs:       make(map[string]DecisionLog),
		decisionLogHistory: make(map[string][]DecisionLog),
		graphNodesByRun:    make(map[string][]DecisionGraphNode),
		graphEdgesByRun:    make(map[string][]DecisionGraphEdge),
		lastNodeByRun:      make(map[string]string),
		ledgerByRun:        make(map[string][]LedgerEntry),
		warmArchiveByRun:   make(map[string][]CanonicalEvent),
		coldArchiveByRun:   make(map[string][]CanonicalEvent),
		outbox:             make([]KernelEvent, 0, 16),
		integrityByRun:     make(map[string]runIntegrityState),
		globalAnchors:      make([]IntegrityAnchor, 0, 16),
		tombstonedRefs:     make(map[string]time.Time),
		sourceBackpressure: make(map[string]int),
		tenantBackpressure: make(map[string]int),
		counters:           make(map[string]float64),
	}
	return k
}

func defaultSourceRegistrations() map[string]SourceRegistration {
	return map[string]SourceRegistration{
		"run_kernel":      {SourceComponent: "run_kernel", MinSchema: 1, DefaultTier: Tier1},
		"decision_kernel": {SourceComponent: "decision_kernel", MinSchema: 1, DefaultTier: Tier1},
		"adapter":         {SourceComponent: "adapter", MinSchema: 1, DefaultTier: Tier1},
	}
}

func (k *Kernel) RegisterSource(reg SourceRegistration) error {
	reg.SourceComponent = normalizeSource(reg.SourceComponent)
	reg.DefaultTier = normalizeTier(reg.DefaultTier)
	if reg.SourceComponent == "" {
		return evidenceErr(422, "SOURCE_COMPONENT_REQUIRED", "source_component required", "source_component_required")
	}
	if reg.MinSchema <= 0 {
		return evidenceErr(422, "SOURCE_SCHEMA_INVALID", "min_schema must be >= 1", "source_schema_invalid")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.registeredSources[reg.SourceComponent] = reg
	return nil
}

func (k *Kernel) SetBackpressureLevel(level int) {
	if level < BackpressureLevel0 {
		level = BackpressureLevel0
	}
	if level > BackpressureLevel3 {
		level = BackpressureLevel3
	}
	atomic.StoreInt32(&k.backpressureLevel, int32(level))
}

func (k *Kernel) SetSourceBackpressureLevel(source string, level int) {
	source = normalizeSource(source)
	if source == "" {
		return
	}
	if level < BackpressureLevel0 {
		level = BackpressureLevel0
	}
	if level > BackpressureLevel3 {
		level = BackpressureLevel3
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.sourceBackpressure[source] = level
}

func (k *Kernel) SetTenantBackpressureLevel(tenantID string, level int) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return
	}
	if level < BackpressureLevel0 {
		level = BackpressureLevel0
	}
	if level > BackpressureLevel3 {
		level = BackpressureLevel3
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.tenantBackpressure[tenantID] = level
}

func (k *Kernel) IngestEvent(req IngestEventRequest) (IngestEventResponse, error) {
	now := k.cfg.Clock()
	norm := normalizeIngestRequest(req, now)
	if err := validateIngestRequest(norm); err != nil {
		return IngestEventResponse{}, err
	}

	k.mu.Lock()
	defer k.mu.Unlock()
	k.counters["ingest_attempt_total"]++

	if _, ok := k.registeredSources[norm.SourceComponent]; !ok {
		k.counters["ingest_source_reject_total"]++
		return IngestEventResponse{}, evidenceErr(422, "UNREGISTERED_SOURCE_COMPONENT", "source_component not registered", "unregistered_source_component")
	}
	reg := k.registeredSources[norm.SourceComponent]
	if norm.SchemaVersion < reg.MinSchema {
		k.counters["ingest_schema_reject_total"]++
		return IngestEventResponse{}, evidenceErr(422, "SCHEMA_VERSION_REJECTED", "schema version below minimum", "schema_version_rejected")
	}

	if existing, ok := k.eventsByID[norm.EventID]; ok {
		k.counters["ingest_dedup_total"]++
		return IngestEventResponse{
			Accepted:    true,
			Deduped:     true,
			Dropped:     false,
			Degraded:    existing.Degraded,
			EventID:     norm.EventID,
			RunID:       norm.RunID,
			ReasonCodes: []string{"event_id_deduped"},
			IngestedAt:  now,
		}, nil
	}

	e := canonicalize(norm, reg)
	e.EstimatedBytes = estimateEventBytes(e)
	action, reasons := k.decideIngestAction(e, norm.SystemLoad)
	switch action {
	case "drop":
		k.counters["ingest_dropped_total"]++
		k.emitLocked("evidence_tier_drop_event", e.RunID, map[string]interface{}{
			"event_id":      e.EventID,
			"event_type":    e.EventType,
			"evidence_tier": e.EvidenceTier,
			"risk_tier":     e.RiskTier,
			"reason_codes":  reasons,
		}, now)
		return IngestEventResponse{
			Accepted:    true,
			Deduped:     false,
			Dropped:     true,
			Degraded:    false,
			EventID:     e.EventID,
			RunID:       e.RunID,
			ReasonCodes: reasons,
			IngestedAt:  now,
		}, nil
	case "degrade":
		e.Degraded = true
		e.Payload = map[string]interface{}{"summary": "degraded_due_to_budget_or_backpressure"}
		e.PayloadHash, e.PayloadHashValid = safeHash(e.Payload)
		if e.PayloadIntegrityRequired && !e.PayloadHashValid {
			reasons = append(reasons, "payload_integrity_required_but_hash_invalid")
		}
		k.counters["ingest_degraded_total"]++
		k.emitLocked("evidence_degrade_event", e.RunID, map[string]interface{}{
			"event_id":      e.EventID,
			"event_type":    e.EventType,
			"evidence_tier": e.EvidenceTier,
			"risk_tier":     e.RiskTier,
			"reason_codes":  reasons,
		}, now)
	}

	if e.PayloadIntegrityRequired && !e.PayloadHashValid {
		k.counters["ingest_payload_integrity_invalid_total"]++
	}

	k.appendIntegrityLocked(&e)
	k.eventsByID[e.EventID] = e
	if e.EventTS.After(k.maxObservedEventTS) {
		k.maxObservedEventTS = e.EventTS
	}
	runData := k.ensureRunDataLocked(e.RunID)
	runData.Events = append(runData.Events, e)
	runData.Stats.Events++
	runData.Stats.Bytes += e.EstimatedBytes
	runData.BySource[e.SourceComponent]++
	runData.ByTier[e.EvidenceTier]++
	runData.ByGrade[e.EvidenceGrade]++
	runData.NeedsGraph = runData.NeedsGraph || isHighRisk(e.RiskTier)
	if e.EventTS.After(runData.LastEventTS) {
		runData.LastEventTS = e.EventTS
	}

	k.updateDecisionLogLocked(e, now)
	k.updateDecisionGraphLocked(e, now)
	k.updateLedgerLocked(e, e.Usage, now)
	k.counters["ingest_accepted_total"]++
	k.counters["ingest_tier_"+e.EvidenceTier+"_total"]++
	k.counters["ingest_source_"+e.SourceComponent+"_total"]++
	if trimmed := k.trimOutboxLocked(now); trimmed > 0 {
		k.counters["outbox_trimmed_total"] += float64(trimmed)
	}

	return IngestEventResponse{
		Accepted:    true,
		Deduped:     false,
		Dropped:     false,
		Degraded:    e.Degraded,
		EventID:     e.EventID,
		RunID:       e.RunID,
		ReasonCodes: reasons,
		IngestedAt:  now,
	}, nil
}

func (k *Kernel) GetRunEvidence(runID string) (RunEvidenceSummary, bool) {
	runID = strings.TrimSpace(runID)
	k.mu.RLock()
	defer k.mu.RUnlock()
	rd, ok := k.runs[runID]
	if !ok {
		return RunEvidenceSummary{}, false
	}
	graphOK := k.isHighRiskGraphCompleteLocked(runID)
	return RunEvidenceSummary{
		RunID:                 runID,
		TotalEvents:           rd.Stats.Events,
		TotalBytes:            rd.Stats.Bytes,
		BySource:              cloneIntMap(rd.BySource),
		ByTier:                cloneIntMap(rd.ByTier),
		ByGrade:               cloneIntMap(rd.ByGrade),
		LastEventTS:           rd.LastEventTS,
		HighRiskGraphComplete: !rd.NeedsGraph || graphOK,
	}, true
}

func (k *Kernel) GetDecisionGraph(runID string) (DecisionGraph, bool) {
	runID = strings.TrimSpace(runID)
	k.mu.RLock()
	defer k.mu.RUnlock()
	nodes, ok := k.graphNodesByRun[runID]
	if !ok {
		return DecisionGraph{}, false
	}
	edges := k.graphEdgesByRun[runID]
	outNodes := make([]DecisionGraphNode, len(nodes))
	outEdges := make([]DecisionGraphEdge, len(edges))
	copy(outNodes, nodes)
	copy(outEdges, edges)
	return DecisionGraph{RunID: runID, Nodes: outNodes, Edges: outEdges}, true
}

func (k *Kernel) GetDecisionLog(decisionID string) (DecisionLog, bool) {
	decisionID = strings.TrimSpace(decisionID)
	k.mu.RLock()
	defer k.mu.RUnlock()
	log, ok := k.decisionLogs[decisionID]
	return log, ok
}

func (k *Kernel) GetDecisionLogHistory(decisionID string, limit int) ([]DecisionLog, bool) {
	decisionID = strings.TrimSpace(decisionID)
	k.mu.RLock()
	defer k.mu.RUnlock()
	h, ok := k.decisionLogHistory[decisionID]
	if !ok || len(h) == 0 {
		return nil, false
	}
	if limit <= 0 {
		limit = len(h)
	}
	start := len(h) - limit
	if start < 0 {
		start = 0
	}
	out := make([]DecisionLog, len(h[start:]))
	copy(out, h[start:])
	return out, true
}

func (k *Kernel) GetLedger(runID string) ([]LedgerEntry, bool) {
	runID = strings.TrimSpace(runID)
	k.mu.RLock()
	defer k.mu.RUnlock()
	items, ok := k.ledgerByRun[runID]
	if !ok {
		return nil, false
	}
	out := make([]LedgerEntry, len(items))
	copy(out, items)
	return out, true
}

func (k *Kernel) GetArchiveSummary(runID string) ArchiveSummary {
	runID = strings.TrimSpace(runID)
	k.mu.RLock()
	defer k.mu.RUnlock()
	out := ArchiveSummary{RunID: runID}
	if runID != "" {
		if rd, ok := k.runs[runID]; ok {
			out.HotEventCount = len(rd.Events)
		}
		out.WarmEventCount = len(k.warmArchiveByRun[runID])
		out.ColdEventCount = len(k.coldArchiveByRun[runID])
		return out
	}
	for _, rd := range k.runs {
		out.HotEventCount += len(rd.Events)
	}
	for _, items := range k.warmArchiveByRun {
		out.WarmEventCount += len(items)
	}
	for _, items := range k.coldArchiveByRun {
		out.ColdEventCount += len(items)
	}
	return out
}

func (k *Kernel) ExportArchive(runID, tier string, limit int) (ArchiveExport, error) {
	runID = strings.TrimSpace(runID)
	tier = strings.ToLower(strings.TrimSpace(tier))
	if runID == "" {
		return ArchiveExport{}, evidenceErr(422, "RUN_ID_REQUIRED", "run_id required", "run_id_required")
	}
	if tier != "warm" && tier != "cold" {
		return ArchiveExport{}, evidenceErr(422, "ARCHIVE_TIER_INVALID", "tier must be warm|cold", "archive_tier_invalid")
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	var src []CanonicalEvent
	switch tier {
	case "warm":
		src = k.warmArchiveByRun[runID]
	case "cold":
		src = k.coldArchiveByRun[runID]
	}
	if len(src) == 0 {
		return ArchiveExport{
			RunID:       runID,
			Tier:        tier,
			EventCount:  0,
			Events:      []CanonicalEvent{},
			GeneratedAt: k.cfg.Clock(),
			Contract: ExportContract{
				IntegrityKind:           "export_digest",
				ContainsTombstoned:      false,
				ContainsRedacted:        false,
				ReplayEligible:          true,
				AuditArtifactEligible:   false,
				ExternalAnchorRequired:  k.cfg.RequireExternalAnchorForAudit,
				ExternalAnchorSatisfied: false,
				ContractVersion:         1,
			},
		}, nil
	}
	if limit <= 0 || limit > len(src) {
		limit = len(src)
	}
	events := make([]CanonicalEvent, limit)
	copy(events, src[len(src)-limit:])
	sortEventsByTS(events)
	containsTombstoned := false
	containsRedacted := false
	replayEligible := true
	auditEligible := true
	anchorRequired := k.cfg.RequireExternalAnchorForAudit
	anchorSatisfied := false
	anchorID := ""
	anchorKind := ""
	for _, e := range events {
		if e.PayloadTombstoned {
			containsTombstoned = true
		}
		if strings.TrimSpace(e.RedactionReason) != "" || e.PayloadTombstoned {
			containsRedacted = true
		}
		if !e.PayloadHashValid || e.PayloadTombstoned {
			replayEligible = false
		}
		if e.EvidenceGrade != GradeAudit {
			auditEligible = false
		}
	}
	if anchorRequired {
		if anchor, ok := k.latestExternalAnchorLocked(); ok {
			anchorSatisfied = true
			anchorID = anchor.AnchorID
			anchorKind = anchor.AnchorKind
		}
	}
	if auditEligible && anchorRequired && !anchorSatisfied {
		auditEligible = false
	}
	integrityHash := ""
	if h, ok := safeHash(events); ok {
		integrityHash = h
	}
	chainRootHash := buildArchiveChainRoot(events)
	return ArchiveExport{
		RunID:         runID,
		Tier:          tier,
		EventCount:    len(events),
		Events:        events,
		GeneratedAt:   k.cfg.Clock(),
		IntegrityHash: integrityHash,
		ChainRootHash: chainRootHash,
		Contract: ExportContract{
			IntegrityKind:           "chain_digest",
			ContainsTombstoned:      containsTombstoned,
			ContainsRedacted:        containsRedacted,
			ReplayEligible:          replayEligible,
			AuditArtifactEligible:   auditEligible,
			ExternalAnchorRequired:  anchorRequired,
			ExternalAnchorSatisfied: anchorSatisfied,
			ExternalAnchorID:        anchorID,
			ExternalAnchorKind:      anchorKind,
			ContractVersion:         1,
		},
	}, nil
}

func (k *Kernel) CreateGlobalIntegrityAnchor(reason string) IntegrityAnchor {
	now := k.cfg.Clock()
	k.mu.Lock()
	anchor := IntegrityAnchor{
		AnchorID:   k.nextID("anc_"),
		RootHash:   k.globalIntegrity.LastHash,
		EventCount: k.globalIntegrity.Count,
		Reason:     strings.TrimSpace(reason),
		CreatedAt:  now,
		AnchorKind: "kernel_internal",
	}
	k.globalAnchors = append(k.globalAnchors, anchor)
	if max := k.cfg.GlobalAnchorHistoryMax; max > 0 && len(k.globalAnchors) > max {
		k.globalAnchors = append([]IntegrityAnchor(nil), k.globalAnchors[len(k.globalAnchors)-max:]...)
	}
	attestor := k.cfg.ExternalAnchorAttestor
	k.emitLocked("evidence_global_integrity_anchor_created", "_global", map[string]interface{}{
		"anchor_id":   anchor.AnchorID,
		"root_hash":   anchor.RootHash,
		"event_count": anchor.EventCount,
		"reason":      anchor.Reason,
	}, now)
	k.mu.Unlock()

	if attestor != nil {
		attReq := ExternalAnchorRequest{
			AnchorID:   anchor.AnchorID,
			RootHash:   anchor.RootHash,
			EventCount: anchor.EventCount,
			Reason:     anchor.Reason,
			CreatedAt:  anchor.CreatedAt,
		}
		att, err := attestor(attReq)
		k.mu.Lock()
		defer k.mu.Unlock()
		for i := len(k.globalAnchors) - 1; i >= 0; i-- {
			if k.globalAnchors[i].AnchorID != anchor.AnchorID {
				continue
			}
			if err != nil {
				k.globalAnchors[i].AnchorKind = "external_attest_failed"
				k.globalAnchors[i].Attestation = &ExternalAttestation{
					Provider:     strings.TrimSpace(att.Provider),
					AttestedAt:   now,
					Status:       "failed",
					ErrorCode:    "external_attestor_error",
					ErrorMessage: err.Error(),
				}
				k.emitLocked("evidence_global_integrity_anchor_external_failed", "_global", map[string]interface{}{
					"anchor_id":     anchor.AnchorID,
					"root_hash":     anchor.RootHash,
					"event_count":   anchor.EventCount,
					"error_code":    "external_attestor_error",
					"error_message": err.Error(),
					"provider":      strings.TrimSpace(att.Provider),
				}, now)
			} else {
				if strings.TrimSpace(att.Status) == "" {
					att.Status = "attested"
				}
				if att.AttestedAt.IsZero() {
					att.AttestedAt = now
				}
				k.globalAnchors[i].AnchorKind = "external_attested"
				k.globalAnchors[i].Attestation = &att
				k.emitLocked("evidence_global_integrity_anchor_external_attested", "_global", map[string]interface{}{
					"anchor_id":       anchor.AnchorID,
					"root_hash":       anchor.RootHash,
					"event_count":     anchor.EventCount,
					"provider":        att.Provider,
					"attestation_ref": att.AttestationRef,
					"worm_ref":        att.WormRef,
					"attested_at":     att.AttestedAt,
				}, now)
			}
			return k.globalAnchors[i]
		}
		return anchor
	}
	return anchor
}

func (k *Kernel) ListGlobalIntegrityAnchors(limit int) []IntegrityAnchor {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if len(k.globalAnchors) == 0 {
		return []IntegrityAnchor{}
	}
	if limit <= 0 || limit > len(k.globalAnchors) {
		limit = len(k.globalAnchors)
	}
	start := len(k.globalAnchors) - limit
	if start < 0 {
		start = 0
	}
	out := make([]IntegrityAnchor, len(k.globalAnchors[start:]))
	copy(out, k.globalAnchors[start:])
	return out
}

func (k *Kernel) BuildRootCausePack(runID string, mode RootCausePackMode) (RootCausePack, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return RootCausePack{}, evidenceErr(422, "RUN_ID_REQUIRED", "run_id required", "run_id_required")
	}
	if mode == "" {
		mode = RootCausePackModeMinimal
	}
	if mode != RootCausePackModeMinimal && mode != RootCausePackModeFull {
		return RootCausePack{}, evidenceErr(422, "ROOT_CAUSE_MODE_INVALID", "mode must be minimal|full", "root_cause_mode_invalid")
	}

	k.mu.RLock()
	defer k.mu.RUnlock()
	rd, ok := k.runs[runID]
	if !ok {
		return RootCausePack{}, evidenceErr(404, "RUN_NOT_FOUND", "run not found", "run_not_found")
	}

	events := append([]CanonicalEvent(nil), rd.Events...)
	sortEventsByTS(events)

	firstBadEvent := findFirstBadEvent(events)
	firstBadNode := ""
	if firstBadEvent != nil {
		if n, found := k.findNodeByEventIDLocked(runID, firstBadEvent.EventID); found {
			firstBadNode = n.NodeID
		} else {
			firstBadNode = "evt:" + firstBadEvent.EventID
		}
	}

	criticalPath := k.traceCriticalPathLocked(runID, firstBadNode)
	if len(criticalPath) == 0 {
		nodes := k.graphNodesByRun[runID]
		start := 0
		if len(nodes) > 8 {
			start = len(nodes) - 8
		}
		for _, n := range nodes[start:] {
			criticalPath = append(criticalPath, n.NodeID)
		}
	}

	key := k.buildKeyEvidencesLocked(runID, events, criticalPath, 8)

	timeline := make([]TimelineItem, 0, len(events))
	for _, e := range events {
		timeline = append(timeline, TimelineItem{EventID: e.EventID, EventType: e.EventType, EventTS: e.EventTS})
	}

	pack := RootCausePack{
		RunID:        runID,
		Mode:         mode,
		FirstBadNode: firstBadNode,
		CriticalPath: criticalPath,
		KeyEvidences: key,
		Timeline:     timeline,
		GeneratedAt:  k.cfg.Clock(),
	}
	if mode == RootCausePackModeFull {
		g, _ := k.GetDecisionGraph(runID)
		l, _ := k.GetLedger(runID)
		pack.DecisionGraph = &g
		pack.Ledger = l
	}
	return pack, nil
}

func (k *Kernel) BuildReplayPack(runID string, mode ReplayPackMode) (ReplayPack, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return ReplayPack{}, evidenceErr(422, "RUN_ID_REQUIRED", "run_id required", "run_id_required")
	}
	if mode == "" {
		mode = ReplayPackModeMinimal
	}
	if mode != ReplayPackModeMinimal && mode != ReplayPackModeFull {
		return ReplayPack{}, evidenceErr(422, "REPLAY_MODE_INVALID", "mode must be minimal|full", "replay_mode_invalid")
	}

	k.mu.RLock()
	defer k.mu.RUnlock()
	rd, ok := k.runs[runID]
	if !ok {
		return ReplayPack{}, evidenceErr(404, "RUN_NOT_FOUND", "run not found", "run_not_found")
	}

	events := append([]CanonicalEvent(nil), rd.Events...)
	sortEventsByTS(events)

	outEvents := make([]ReplayEvent, 0, len(events))
	reasons := make([]string, 0, 4)
	snapshotRefs := make([]string, 0, 8)
	policyBundleRefs := make([]string, 0, 8)
	featureSnapshotRefs := make([]string, 0, 8)
	adapterReceiptRefs := make([]string, 0, 8)
	decisionRefs := make([]string, 0, 8)
	for _, e := range events {
		pe := ReplayEvent{
			EventID:           e.EventID,
			EventType:         e.EventType,
			SchemaVersion:     e.SchemaVersion,
			SourceComponent:   e.SourceComponent,
			EventTS:           e.EventTS,
			RunID:             e.RunID,
			StepID:            e.StepID,
			DecisionID:        e.DecisionID,
			PayloadRef:        e.PayloadRef,
			PayloadHash:       e.PayloadHash,
			PayloadHashValid:  e.PayloadHashValid,
			PayloadTombstoned: e.PayloadTombstoned,
			Payload:           cloneMap(e.Payload),
		}
		if e.PayloadTombstoned || isPayloadTombstoned(k.tombstonedRefs, e.PayloadRef) {
			pe.Redacted = true
			pe.RedactionReason = e.RedactionReason
			if strings.TrimSpace(pe.RedactionReason) == "" {
				pe.RedactionReason = "dsar_tombstoned"
			}
			pe.Payload = map[string]interface{}{"tombstone": "deleted_by_dsar"}
			reasons = appendUniqueString(reasons, "dsar_tombstone_applied")
		}
		if mode == ReplayPackModeMinimal {
			pe.Payload = nil
		}
		outEvents = append(outEvents, pe)
		for _, key := range []string{"snapshot_ref", "snapshot_hash", "snapshot_refs"} {
			snapshotRefs = appendUniqueStrings(snapshotRefs, extractStringSliceOrScalar(e.Payload, key))
		}
		for _, key := range []string{"policy_bundle_snapshot_ref", "policy_bundle_id", "policy_bundle_refs"} {
			policyBundleRefs = appendUniqueStrings(policyBundleRefs, extractStringSliceOrScalar(e.Payload, key))
		}
		for _, key := range []string{"feature_snapshot_id", "feature_snapshot_ref", "feature_snapshot_refs"} {
			featureSnapshotRefs = appendUniqueStrings(featureSnapshotRefs, extractStringSliceOrScalar(e.Payload, key))
		}
		for _, key := range []string{"execution_receipt_ref", "receipt_ref", "adapter_receipt_refs"} {
			adapterReceiptRefs = appendUniqueStrings(adapterReceiptRefs, extractStringSliceOrScalar(e.Payload, key))
		}
		if strings.TrimSpace(e.DecisionID) != "" {
			decisionRefs = appendUniqueString(decisionRefs, e.DecisionID)
		}
		decisionRefs = appendUniqueStrings(decisionRefs, extractStringSliceOrScalar(e.Payload, "decision_refs"))
	}

	pack := ReplayPack{
		RunID:                   runID,
		Mode:                    mode,
		ManifestVersion:         1,
		EventCount:              len(outEvents),
		Events:                  outEvents,
		SnapshotRefs:            snapshotRefs,
		PolicyBundleRefs:        policyBundleRefs,
		FeatureSnapshotRefs:     featureSnapshotRefs,
		AdapterReceiptRefs:      adapterReceiptRefs,
		DecisionRefs:            decisionRefs,
		GeneratedAt:             k.cfg.Clock(),
		ReasonCodes:             reasons,
		IntegrityRootHash:       k.integrityByRun[runID].LastHash,
		GlobalIntegrityRootHash: k.globalIntegrity.LastHash,
	}
	if mode == ReplayPackModeFull {
		g, _ := k.GetDecisionGraph(runID)
		l, _ := k.GetLedger(runID)
		pack.DecisionGraph = &g
		pack.Ledger = l
	}
	return pack, nil
}

func (k *Kernel) VerifyIntegrity(runID string) (IntegrityVerifyResult, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return IntegrityVerifyResult{}, evidenceErr(422, "RUN_ID_REQUIRED", "run_id required", "run_id_required")
	}

	k.mu.RLock()
	defer k.mu.RUnlock()
	rd, ok := k.runs[runID]
	if !ok {
		return IntegrityVerifyResult{}, evidenceErr(404, "RUN_NOT_FOUND", "run not found", "run_not_found")
	}
	events := append([]CanonicalEvent(nil), rd.Events...)
	sortEventsByTS(events)
	prev := ""
	for _, e := range events {
		sum := sha256.Sum256([]byte(strings.Join([]string{
			prev,
			e.EventID,
			e.EventType,
			e.CanonicalHash,
			e.PayloadHash,
			e.EventTS.UTC().Format(time.RFC3339Nano),
		}, "|")))
		expect := hex.EncodeToString(sum[:])
		if e.IntegrityHash != expect || e.IntegrityPrevHash != prev {
			return IntegrityVerifyResult{
				RunID:                   runID,
				Verified:                false,
				EventCount:              len(events),
				IntegrityRootHash:       k.integrityByRun[runID].LastHash,
				GlobalIntegrityRootHash: k.globalIntegrity.LastHash,
				GlobalVerified:          k.verifyGlobalIntegrityLocked(),
				FailureEventID:          e.EventID,
				FailureReason:           "integrity_chain_mismatch",
				VerifiedAt:              k.cfg.Clock(),
			}, nil
		}
		prev = e.IntegrityHash
	}
	return IntegrityVerifyResult{
		RunID:                   runID,
		Verified:                true,
		EventCount:              len(events),
		IntegrityRootHash:       k.integrityByRun[runID].LastHash,
		GlobalIntegrityRootHash: k.globalIntegrity.LastHash,
		GlobalVerified:          k.verifyGlobalIntegrityLocked(),
		VerifiedAt:              k.cfg.Clock(),
	}, nil
}

func (k *Kernel) VerifyGlobalIntegrity() IntegrityVerifyResult {
	k.mu.RLock()
	defer k.mu.RUnlock()
	verified := k.verifyGlobalIntegrityLocked()
	return IntegrityVerifyResult{
		RunID:                   "_global",
		Verified:                verified,
		GlobalVerified:          verified,
		GlobalIntegrityRootHash: k.globalIntegrity.LastHash,
		EventCount:              len(k.eventsByID),
		IntegrityRootHash:       "",
		VerifiedAt:              k.cfg.Clock(),
	}
}

func (k *Kernel) verifyGlobalIntegrityLocked() bool {
	if k.globalIntegrity.Count == 0 {
		return true
	}
	all := make([]CanonicalEvent, 0, len(k.eventsByID))
	for _, e := range k.eventsByID {
		all = append(all, e)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].EventTS.Equal(all[j].EventTS) {
			return all[i].EventID < all[j].EventID
		}
		return all[i].EventTS.Before(all[j].EventTS)
	})
	prev := ""
	count := 0
	for _, e := range all {
		sum := sha256.Sum256([]byte(strings.Join([]string{
			prev,
			e.RunID,
			e.EventID,
			e.IntegrityHash,
			e.EventTS.UTC().Format(time.RFC3339Nano),
		}, "|")))
		prev = hex.EncodeToString(sum[:])
		count++
	}
	if count != k.globalIntegrity.Count {
		return false
	}
	return prev == k.globalIntegrity.LastHash
}

func (k *Kernel) DeleteByDSAR(req DSARDeleteRequest) (DSARDeleteResponse, error) {
	now := k.cfg.Clock()
	req.RequestID = strings.TrimSpace(req.RequestID)
	req.TenantID = strings.TrimSpace(req.TenantID)
	req.RunID = strings.TrimSpace(req.RunID)
	req.PayloadRefPrefix = strings.TrimSpace(req.PayloadRefPrefix)
	if req.RequestID == "" {
		req.RequestID = k.nextID("dsar_")
	}
	if req.TenantID == "" {
		return DSARDeleteResponse{}, evidenceErr(422, "TENANT_ID_REQUIRED", "tenant_id required", "tenant_id_required")
	}

	k.mu.Lock()
	defer k.mu.Unlock()

	deletedEvents := 0
	redactedEvents := 0
	tombstones := 0

	filterRun := req.RunID != ""
	for runID, rd := range k.runs {
		if filterRun && runID != req.RunID {
			continue
		}
		kept := rd.Events[:0]
		for _, e := range rd.Events {
			if e.TenantID != req.TenantID {
				kept = append(kept, e)
				continue
			}
			if req.PayloadRefPrefix != "" && !strings.HasPrefix(e.PayloadRef, req.PayloadRefPrefix) {
				kept = append(kept, e)
				continue
			}
			// Hard-delete only for low-signal operational events; redact others.
			if e.EvidenceTier == Tier3 && e.EvidenceGrade == GradeOperational {
				deletedEvents++
				continue
			}
			e.Payload = map[string]interface{}{"tombstone": "deleted_by_dsar"}
			e.PayloadTombstoned = true
			e.RedactionReason = "dsar_tombstoned"
			e.PayloadHash, e.PayloadHashValid = safeHash(e.Payload)
			redactedEvents++
			if strings.TrimSpace(e.PayloadRef) != "" {
				k.tombstonedRefs[e.PayloadRef] = now
				tombstones++
			}
			kept = append(kept, e)
		}
		rd.Events = kept
	}

	k.rebuildDerivedIndexesLocked()
	k.emitLocked("evidence_dsar_delete_event", req.RunID, map[string]interface{}{
		"request_id":      req.RequestID,
		"tenant_id":       req.TenantID,
		"run_id":          req.RunID,
		"deleted_events":  deletedEvents,
		"redacted_events": redactedEvents,
		"tombstones":      tombstones,
	}, now)

	return DSARDeleteResponse{
		RequestID:      req.RequestID,
		TenantID:       req.TenantID,
		RunID:          req.RunID,
		DeletedEvents:  deletedEvents,
		RedactedEvents: redactedEvents,
		Tombstones:     tombstones,
		CompletedAt:    now,
		ReasonCodes:    []string{"dsar_completed"},
	}, nil
}

func (k *Kernel) AggregateLedger(req LedgerAggregateRequest) (LedgerAggregateResponse, error) {
	now := k.cfg.Clock()
	groupBy := strings.TrimSpace(req.GroupBy)
	if groupBy == "" {
		groupBy = "resource_type"
	}
	if groupBy != "run" && groupBy != "tenant" && groupBy != "workflow" && groupBy != "resource_type" {
		return LedgerAggregateResponse{}, evidenceErr(422, "LEDGER_GROUP_BY_INVALID", "group_by must be run|tenant|workflow|resource_type", "ledger_group_by_invalid")
	}

	k.mu.RLock()
	defer k.mu.RUnlock()

	rows := map[string]*LedgerAggregateRow{}
	for runID, entries := range k.ledgerByRun {
		if req.RunID != "" && runID != strings.TrimSpace(req.RunID) {
			continue
		}
		for _, e := range entries {
			if req.TenantID != "" && e.TenantID != strings.TrimSpace(req.TenantID) {
				continue
			}
			if req.WorkflowID != "" && e.WorkflowID != strings.TrimSpace(req.WorkflowID) {
				continue
			}
			if req.ResourceType != "" && e.ResourceType != strings.TrimSpace(req.ResourceType) {
				continue
			}
			if !req.WindowStart.IsZero() && e.CreatedAt.Before(req.WindowStart) {
				continue
			}
			if !req.WindowEnd.IsZero() && e.CreatedAt.After(req.WindowEnd) {
				continue
			}
			key := ledgerGroupKey(e, groupBy)
			row, ok := rows[key]
			if !ok {
				row = &LedgerAggregateRow{GroupKey: key}
				rows[key] = row
			}
			row.EntryCount++
			row.TotalUsage += e.UsageAmount
			row.TotalCost += e.CostAmount
			if e.UsageAmount > row.MaxUsage {
				row.MaxUsage = e.UsageAmount
			}
		}
	}

	out := make([]LedgerAggregateRow, 0, len(rows))
	for _, row := range rows {
		if row.EntryCount > 0 {
			row.AvgUsage = row.TotalUsage / float64(row.EntryCount)
			row.AvgCost = row.TotalCost / float64(row.EntryCount)
			if req.DetectAnomaly {
				row.AnomalyScore, row.AnomalyReason = detectLedgerAnomaly(*row)
			}
		}
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GroupKey < out[j].GroupKey })
	return LedgerAggregateResponse{
		Rows:        out,
		GeneratedAt: now,
	}, nil
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
	if limit <= 0 || limit > len(k.outbox) {
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

func (k *Kernel) SweepRetention(now time.Time) SweepResult {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := SweepResult{}
	cutoff := now.Add(-k.cfg.EventRetention)
	k.rotateArchivesLocked(now, &out)
	for runID, rd := range k.runs {
		kept := rd.Events[:0]
		for _, e := range rd.Events {
			if e.EventTS.Before(cutoff) {
				k.archiveEventByAgeLocked(runID, e, now, &out)
				continue
			}
			kept = append(kept, e)
		}
		rd.Events = kept
		rd.Stats.Events = len(kept)
		rd.Stats.Bytes = 0
		rd.BySource = map[string]int{}
		rd.ByTier = map[string]int{}
		rd.ByGrade = map[string]int{}
		rd.LastEventTS = time.Time{}
		for _, e := range kept {
			rd.Stats.Bytes += e.EstimatedBytes
			rd.BySource[e.SourceComponent]++
			rd.ByTier[e.EvidenceTier]++
			rd.ByGrade[e.EvidenceGrade]++
			if e.EventTS.After(rd.LastEventTS) {
				rd.LastEventTS = e.EventTS
			}
		}
		if len(kept) == 0 {
			delete(k.runs, runID)
			delete(k.graphNodesByRun, runID)
			delete(k.graphEdgesByRun, runID)
			delete(k.lastNodeByRun, runID)
			delete(k.ledgerByRun, runID)
		}
	}
	k.rebuildDerivedIndexesLocked()
	out.OutboxTrimmed = k.trimOutboxLocked(now)
	return out
}

func (k *Kernel) archiveEventByAgeLocked(runID string, e CanonicalEvent, now time.Time, out *SweepResult) {
	if out == nil {
		return
	}
	age := now.Sub(e.EventTS)
	switch {
	case age <= k.cfg.WarmRetention:
		k.warmArchiveByRun[runID] = append(k.warmArchiveByRun[runID], e)
		out.WarmArchived++
	case age <= k.cfg.ColdRetention:
		k.coldArchiveByRun[runID] = append(k.coldArchiveByRun[runID], e)
		out.ColdArchived++
	default:
		out.DeletedEvents++
	}
}

func (k *Kernel) rotateArchivesLocked(now time.Time, out *SweepResult) {
	for runID, warm := range k.warmArchiveByRun {
		keepWarm := warm[:0]
		for _, e := range warm {
			age := now.Sub(e.EventTS)
			if age <= k.cfg.WarmRetention {
				keepWarm = append(keepWarm, e)
				continue
			}
			if age <= k.cfg.ColdRetention {
				k.coldArchiveByRun[runID] = append(k.coldArchiveByRun[runID], e)
				if out != nil {
					out.ColdArchived++
				}
				continue
			}
			if out != nil {
				out.DeletedEvents++
			}
		}
		if len(keepWarm) == 0 {
			delete(k.warmArchiveByRun, runID)
		} else {
			k.warmArchiveByRun[runID] = keepWarm
		}
	}
	for runID, cold := range k.coldArchiveByRun {
		keepCold := cold[:0]
		for _, e := range cold {
			if now.Sub(e.EventTS) <= k.cfg.ColdRetention {
				keepCold = append(keepCold, e)
				continue
			}
			if out != nil {
				out.DeletedEvents++
			}
		}
		if len(keepCold) == 0 {
			delete(k.coldArchiveByRun, runID)
		} else {
			k.coldArchiveByRun[runID] = keepCold
		}
	}
}

func (k *Kernel) MetricsSnapshot() MetricsSnapshot {
	k.mu.RLock()
	defer k.mu.RUnlock()
	counters := cloneFloatMap(k.counters)
	rates := map[string]float64{}
	gauges := map[string]float64{}

	totalAttempts := counters["ingest_attempt_total"]
	accepted := counters["ingest_accepted_total"]
	dropped := counters["ingest_dropped_total"]
	degraded := counters["ingest_degraded_total"]
	deduped := counters["ingest_dedup_total"]
	payloadIntegrityInvalid := counters["ingest_payload_integrity_invalid_total"]

	rates["ingest_accept_rate"] = ratio(accepted, totalAttempts)
	rates["ingest_drop_rate"] = ratio(dropped, totalAttempts)
	rates["ingest_degrade_rate"] = ratio(degraded, totalAttempts)
	rates["ingest_dedup_rate"] = ratio(deduped, totalAttempts)
	rates["payload_integrity_invalid_rate"] = ratio(payloadIntegrityInvalid, accepted)

	highRiskRuns := 0.0
	highRiskGraphComplete := 0.0
	for runID, rd := range k.runs {
		if !rd.NeedsGraph {
			continue
		}
		highRiskRuns++
		if k.isHighRiskGraphCompleteLocked(runID) {
			highRiskGraphComplete++
		}
	}
	rates["high_risk_graph_complete_rate"] = ratio(highRiskGraphComplete, highRiskRuns)

	gauges["outbox_depth"] = float64(len(k.outbox))
	gauges["runs_active"] = float64(len(k.runs))
	gauges["events_indexed"] = float64(len(k.eventsByID))
	gauges["decision_logs"] = float64(len(k.decisionLogs))
	gauges["decision_log_history_keys"] = float64(len(k.decisionLogHistory))
	gauges["warm_archive_runs"] = float64(len(k.warmArchiveByRun))
	gauges["cold_archive_runs"] = float64(len(k.coldArchiveByRun))
	gauges["global_integrity_count"] = float64(k.globalIntegrity.Count)
	gauges["global_integrity_anchor_count"] = float64(len(k.globalAnchors))

	return MetricsSnapshot{Counters: counters, Rates: rates, Gauges: gauges}
}

func (k *Kernel) ensureRunDataLocked(runID string) *storedRunData {
	rd, ok := k.runs[runID]
	if ok {
		return rd
	}
	rd = &storedRunData{
		Events:      make([]CanonicalEvent, 0, 16),
		BySource:    make(map[string]int),
		ByTier:      make(map[string]int),
		ByGrade:     make(map[string]int),
		LastEventTS: time.Time{},
	}
	k.runs[runID] = rd
	return rd
}

func (k *Kernel) appendIntegrityLocked(e *CanonicalEvent) {
	if e == nil {
		return
	}
	state := k.integrityByRun[e.RunID]
	prev := state.LastHash
	seq := state.Count + 1
	sum := sha256.Sum256([]byte(strings.Join([]string{
		prev,
		e.EventID,
		e.EventType,
		e.CanonicalHash,
		e.PayloadHash,
		e.EventTS.UTC().Format(time.RFC3339Nano),
	}, "|")))
	cur := hex.EncodeToString(sum[:])
	e.IntegritySeq = seq
	e.IntegrityPrevHash = prev
	e.IntegrityHash = cur
	k.integrityByRun[e.RunID] = runIntegrityState{
		LastHash: cur,
		Count:    seq,
	}
	globalPrev := k.globalIntegrity.LastHash
	globalSum := sha256.Sum256([]byte(strings.Join([]string{
		globalPrev,
		e.RunID,
		e.EventID,
		e.IntegrityHash,
		e.EventTS.UTC().Format(time.RFC3339Nano),
	}, "|")))
	k.globalIntegrity = runIntegrityState{
		LastHash: hex.EncodeToString(globalSum[:]),
		Count:    k.globalIntegrity.Count + 1,
	}
}

func (k *Kernel) rebuildDerivedIndexesLocked() {
	newEventsByID := make(map[string]CanonicalEvent, len(k.eventsByID))
	k.decisionLogs = make(map[string]DecisionLog)
	k.decisionLogHistory = make(map[string][]DecisionLog)
	k.graphNodesByRun = make(map[string][]DecisionGraphNode)
	k.graphEdgesByRun = make(map[string][]DecisionGraphEdge)
	k.lastNodeByRun = make(map[string]string)
	k.ledgerByRun = make(map[string][]LedgerEntry)
	k.integrityByRun = make(map[string]runIntegrityState)
	k.globalIntegrity = runIntegrityState{}

	for runID, rd := range k.runs {
		sortEventsByTS(rd.Events)
		rd.Stats.Events = len(rd.Events)
		rd.Stats.Bytes = 0
		rd.BySource = map[string]int{}
		rd.ByTier = map[string]int{}
		rd.ByGrade = map[string]int{}
		rd.NeedsGraph = false
		rd.LastEventTS = time.Time{}
		for i := range rd.Events {
			e := rd.Events[i]
			if _, exists := newEventsByID[e.EventID]; exists {
				k.counters["event_id_collision_total"]++
			}
			k.appendIntegrityLocked(&e)
			rd.Events[i] = e
			newEventsByID[e.EventID] = e
			rd.Stats.Bytes += e.EstimatedBytes
			rd.BySource[e.SourceComponent]++
			rd.ByTier[e.EvidenceTier]++
			rd.ByGrade[e.EvidenceGrade]++
			rd.NeedsGraph = rd.NeedsGraph || isHighRisk(e.RiskTier)
			if e.EventTS.After(rd.LastEventTS) {
				rd.LastEventTS = e.EventTS
			}
			k.updateDecisionLogLocked(e, e.EventTS)
			k.updateDecisionGraphLocked(e, e.EventTS)
			k.updateLedgerLocked(e, e.Usage, e.EventTS)
		}
		// Ensure maps exist for runs that survived but currently have no graph nodes.
		if _, ok := k.graphNodesByRun[runID]; !ok {
			k.graphNodesByRun[runID] = []DecisionGraphNode{}
			k.graphEdgesByRun[runID] = []DecisionGraphEdge{}
		}
	}
	k.eventsByID = newEventsByID
}

func (k *Kernel) decideIngestAction(e CanonicalEvent, systemLoad float64) (string, []string) {
	reasons := make([]string, 0, 3)
	rd := k.ensureRunDataLocked(e.RunID)
	if k.cfg.MaxEventsPerRun > 0 && rd.Stats.Events+1 > k.cfg.MaxEventsPerRun {
		reasons = append(reasons, "max_events_per_run_exceeded")
	}
	if k.cfg.MaxEvidenceBytesPerRun > 0 && rd.Stats.Bytes+e.EstimatedBytes > k.cfg.MaxEvidenceBytesPerRun {
		reasons = append(reasons, "max_evidence_bytes_per_run_exceeded")
	}
	if len(reasons) > 0 {
		if isTierDroppable(e.EvidenceTier) {
			k.emitLocked("evidence_write_budget_exceeded_event", e.RunID, map[string]interface{}{
				"event_id":      e.EventID,
				"event_type":    e.EventType,
				"evidence_tier": e.EvidenceTier,
				"reason_codes":  reasons,
			}, k.cfg.Clock())
			return "drop", reasons
		}
		return "degrade", reasons
	}

	level := k.effectiveBackpressureLevelLocked(e.SourceComponent, e.TenantID)
	switch level {
	case BackpressureLevel1:
		if isTierDroppable(e.EvidenceTier) && !isHighRisk(e.RiskTier) {
			return "drop", []string{"backpressure_level1_drop_non_critical"}
		}
	case BackpressureLevel2:
		if e.EvidenceTier != Tier0 && !isHighRisk(e.RiskTier) {
			return "drop", []string{"backpressure_level2_drop_non_critical"}
		}
	case BackpressureLevel3:
		if !isHighRisk(e.RiskTier) && e.EvidenceTier != Tier0 {
			return "drop", []string{"backpressure_level3_drop_non_critical"}
		}
	}

	if systemLoad >= k.cfg.HighLoadThreshold && !isHighRisk(e.RiskTier) {
		if e.EvidenceTier == Tier2 && shouldDropByRate(e.EventID, k.cfg.Tier2HighLoadDropRate) {
			return "drop", []string{"sampling_drop_tier2_high_load"}
		}
		if e.EvidenceTier == Tier3 && shouldDropByRate(e.EventID, k.cfg.Tier3HighLoadDropRate) {
			return "drop", []string{"sampling_drop_tier3_high_load"}
		}
	}

	return "accept", nil
}

func (k *Kernel) effectiveBackpressureLevelLocked(source, tenantID string) int {
	level := int(atomic.LoadInt32(&k.backpressureLevel))
	if scoped, ok := k.sourceBackpressure[normalizeSource(source)]; ok && scoped > level {
		level = scoped
	}
	if scoped, ok := k.tenantBackpressure[strings.TrimSpace(tenantID)]; ok && scoped > level {
		level = scoped
	}
	return level
}

func (k *Kernel) updateDecisionLogLocked(e CanonicalEvent, now time.Time) {
	if strings.TrimSpace(e.DecisionID) == "" {
		return
	}
	history := k.decisionLogHistory[e.DecisionID]
	version := len(history) + 1
	decisionType := e.EventType
	decisionValue := extractString(e.Payload, "decision")
	if decisionValue == "" {
		decisionValue = extractString(e.Payload, "final_decision")
	}
	if decisionValue == "" {
		decisionValue = "unknown"
	}
	log := DecisionLog{
		DecisionID:                e.DecisionID,
		Version:                   version,
		SourceEventID:             e.EventID,
		SourceSchemaVersion:       e.SchemaVersion,
		SourceComponent:           e.SourceComponent,
		SourceEventTS:             e.EventTS,
		DerivedFromEventID:        e.EventID,
		RunID:                     e.RunID,
		DecisionType:              decisionType,
		DecisionValue:             decisionValue,
		DecisionConfidence:        extractFloat(e.Payload, "decision_confidence"),
		PayloadIntegrityScore:     boolToFloat(e.PayloadHashValid),
		EvidenceCompletenessScore: estimateEvidenceCompleteness(e, decisionValue),
		RationaleRef:              extractString(e.Payload, "rationale_ref"),
		CreatedAt:                 now,
	}
	if len(history) > 0 {
		history[len(history)-1].SupersededByVersion = version
	}
	k.decisionLogs[e.DecisionID] = log
	h := append(history, log)
	if max := k.cfg.DecisionLogHistoryMax; max > 0 && len(h) > max {
		h = append([]DecisionLog(nil), h[len(h)-max:]...)
	}
	k.decisionLogHistory[e.DecisionID] = h
}

func (k *Kernel) latestExternalAnchorLocked() (IntegrityAnchor, bool) {
	for i := len(k.globalAnchors) - 1; i >= 0; i-- {
		a := k.globalAnchors[i]
		if a.Attestation == nil {
			continue
		}
		if strings.TrimSpace(a.AnchorKind) != "external_attested" {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(a.Attestation.Status), "attested") {
			continue
		}
		return a, true
	}
	return IntegrityAnchor{}, false
}

func (k *Kernel) updateDecisionGraphLocked(e CanonicalEvent, now time.Time) {
	if strings.TrimSpace(e.RunID) == "" {
		return
	}
	nodeID := stableNodeID(e)
	if payloadNodeID := extractString(e.Payload, "decision_node_id"); payloadNodeID != "" {
		nodeID = payloadNodeID
	}
	nodeType := inferNodeType(e.SourceComponent, e.EventType)
	node := DecisionGraphNode{
		NodeID:    nodeID,
		RunID:     e.RunID,
		NodeType:  nodeType,
		NodeRef:   e.EventID,
		CreatedAt: now,
	}
	k.upsertGraphNodeLocked(e.RunID, node)

	parentIDs := extractStringSlice(e.Payload, "parent_decision_node_ids")
	if len(parentIDs) > 0 {
		for _, parent := range parentIDs {
			k.upsertGraphEdgeLocked(e.RunID, DecisionGraphEdge{
				EdgeID:     stableEdgeID(e.RunID, parent, node.NodeID, "causal", e.EventID),
				RunID:      e.RunID,
				FromNodeID: parent,
				ToNodeID:   node.NodeID,
				EdgeType:   "causal",
				CreatedAt:  now,
			})
		}
	} else {
		prev := strings.TrimSpace(k.lastNodeByRun[e.RunID])
		if prev != "" && prev != node.NodeID {
			edgeType := inferEdgeType(e.EventType)
			k.upsertGraphEdgeLocked(e.RunID, DecisionGraphEdge{
				EdgeID:     stableEdgeID(e.RunID, prev, node.NodeID, edgeType, e.EventID),
				RunID:      e.RunID,
				FromNodeID: prev,
				ToNodeID:   node.NodeID,
				EdgeType:   edgeType,
				CreatedAt:  now,
			})
		}
	}
	k.lastNodeByRun[e.RunID] = node.NodeID
}

func (k *Kernel) updateLedgerLocked(e CanonicalEvent, usage *UsageInput, now time.Time) {
	if usage == nil {
		return
	}
	resourceType := strings.TrimSpace(usage.ResourceType)
	unit := strings.TrimSpace(usage.Unit)
	if resourceType == "" || unit == "" {
		return
	}
	entry := LedgerEntry{
		LedgerID:     k.nextID("led_"),
		RunID:        e.RunID,
		StepID:       e.StepID,
		TenantID:     e.TenantID,
		ProjectID:    e.ProjectID,
		WorkflowID:   e.WorkflowID,
		ResourceType: resourceType,
		UsageAmount:  usage.UsageAmount,
		Unit:         unit,
		CostAmount:   usage.CostAmount,
		CreatedAt:    now,
	}
	k.ledgerByRun[e.RunID] = append(k.ledgerByRun[e.RunID], entry)
}

func (k *Kernel) emitLocked(eventType, runID string, payload map[string]interface{}, now time.Time) {
	seq := atomic.AddUint64(&k.eventSeq, 1)
	hash, ok := safeHash(payload)
	ev := KernelEvent{
		EventID:          fmt.Sprintf("evt_evd_seq_%d", seq),
		EventType:        eventType,
		SchemaVersion:    1,
		SourceComponent:  "evidence_kernel",
		EventTS:          now,
		RunID:            runID,
		PayloadRef:       fmt.Sprintf("inline://evidence_outbox/%d", seq),
		PayloadHash:      hash,
		PayloadHashValid: ok,
		Payload:          payload,
	}
	k.outbox = append(k.outbox, ev)
	if now.After(k.maxObservedEventTS) {
		k.maxObservedEventTS = now
	}
	if trimmed := k.trimOutboxLocked(now); trimmed > 0 {
		k.counters["outbox_trimmed_total"] += float64(trimmed)
	}
}

func (k *Kernel) trimOutboxLocked(now time.Time) int {
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
	return trimmed
}

func normalizeIngestRequest(req IngestEventRequest, now time.Time) IngestEventRequest {
	req.EventID = strings.TrimSpace(req.EventID)
	req.EventType = strings.TrimSpace(req.EventType)
	req.SourceComponent = normalizeSource(req.SourceComponent)
	req.RunID = strings.TrimSpace(req.RunID)
	req.StepID = strings.TrimSpace(req.StepID)
	req.DecisionID = strings.TrimSpace(req.DecisionID)
	req.TenantID = strings.TrimSpace(req.TenantID)
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.WorkflowID = strings.TrimSpace(req.WorkflowID)
	req.RiskTier = normalizeRisk(req.RiskTier)
	req.EvidenceTier = normalizeTier(req.EvidenceTier)
	req.EvidenceGrade = normalizeGrade(req.EvidenceGrade)
	req.PayloadRef = strings.TrimSpace(req.PayloadRef)
	req.PayloadHash = strings.TrimSpace(req.PayloadHash)
	if req.EventTS.IsZero() {
		req.EventTS = now
	}
	if req.SchemaVersion <= 0 {
		req.SchemaVersion = 1
	}
	if req.SystemLoad < 0 {
		req.SystemLoad = 0
	}
	if req.SystemLoad > 1 {
		req.SystemLoad = 1
	}
	return req
}

func (k *Kernel) isHighRiskGraphCompleteLocked(runID string) bool {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return false
	}
	rd, ok := k.runs[runID]
	if !ok || !rd.NeedsGraph {
		return true
	}
	nodes := k.graphNodesByRun[runID]
	edges := k.graphEdgesByRun[runID]
	if len(nodes) == 0 || len(edges) == 0 {
		return false
	}

	nodeByID := make(map[string]DecisionGraphNode, len(nodes))
	for _, n := range nodes {
		nodeByID[n.NodeID] = n
	}
	edgeSet := make(map[string]struct{}, len(edges))
	incoming := make(map[string][]string, len(edges))
	for _, e := range edges {
		edgeSet[e.FromNodeID+"->"+e.ToNodeID] = struct{}{}
		incoming[e.ToNodeID] = append(incoming[e.ToNodeID], e.FromNodeID)
	}

	finalNode, ok := selectFinalDecisionNode(nodes, edges)
	if !ok {
		return false
	}
	finalNodeID := finalNode.NodeID

	// Validate explicit parent edges from payload declarations.
	for _, e := range rd.Events {
		parents := extractStringSlice(e.Payload, "parent_decision_node_ids")
		if len(parents) == 0 {
			continue
		}
		child := extractString(e.Payload, "decision_node_id")
		if child == "" {
			child = stableNodeID(e)
		}
		for _, p := range parents {
			if _, ok := edgeSet[p+"->"+child]; !ok {
				return false
			}
		}
	}

	// Ancestor closure from final decision node.
	visited := map[string]bool{finalNodeID: true}
	stack := []string{finalNodeID}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, parent := range incoming[cur] {
			if parent == "" || visited[parent] {
				continue
			}
			visited[parent] = true
			stack = append(stack, parent)
		}
	}

	hasTypeInClosure := func(nodeType string) bool {
		for nodeID := range visited {
			if n, ok := nodeByID[nodeID]; ok && n.NodeType == nodeType {
				return true
			}
		}
		return false
	}

	// Required key node families that appear in this run must causally reach final decision.
	requiredTypes := []string{"context", "policy", "approval", "schedule", "release", "tool"}
	for _, nodeType := range requiredTypes {
		seenType := false
		for _, n := range nodes {
			if n.NodeType == nodeType {
				seenType = true
				break
			}
		}
		if !seenType {
			continue
		}
		if !hasTypeInClosure(nodeType) {
			return false
		}
	}
	return true
}

func selectFinalDecisionNode(nodes []DecisionGraphNode, edges []DecisionGraphEdge) (DecisionGraphNode, bool) {
	if len(nodes) == 0 {
		return DecisionGraphNode{}, false
	}
	isDecisionLike := func(nodeType string) bool {
		switch nodeType {
		case "decision", "release", "schedule", "approval":
			return true
		default:
			return false
		}
	}
	decisionLike := make(map[string]DecisionGraphNode, len(nodes))
	for _, n := range nodes {
		if isDecisionLike(n.NodeType) {
			decisionLike[n.NodeID] = n
		}
	}
	if len(decisionLike) == 0 {
		return DecisionGraphNode{}, false
	}
	outToDecisionLike := make(map[string]int, len(decisionLike))
	for _, e := range edges {
		if _, fromOK := decisionLike[e.FromNodeID]; !fromOK {
			continue
		}
		if _, toOK := decisionLike[e.ToNodeID]; !toOK {
			continue
		}
		outToDecisionLike[e.FromNodeID]++
	}
	candidates := make([]DecisionGraphNode, 0, len(decisionLike))
	for id, n := range decisionLike {
		if outToDecisionLike[id] == 0 {
			candidates = append(candidates, n)
		}
	}
	if len(candidates) != 1 {
		return DecisionGraphNode{}, false
	}
	return candidates[0], true
}

func validateIngestRequest(req IngestEventRequest) error {
	if req.EventID == "" {
		return evidenceErr(422, "EVENT_ID_REQUIRED", "event_id required", "event_id_required")
	}
	if req.EventType == "" {
		return evidenceErr(422, "EVENT_TYPE_REQUIRED", "event_type required", "event_type_required")
	}
	if req.SourceComponent == "" {
		return evidenceErr(422, "SOURCE_COMPONENT_REQUIRED", "source_component required", "source_component_required")
	}
	if req.RunID == "" {
		return evidenceErr(422, "RUN_ID_REQUIRED", "run_id required", "run_id_required")
	}
	return nil
}

func canonicalize(req IngestEventRequest, reg SourceRegistration) CanonicalEvent {
	tier := req.EvidenceTier
	if tier == "" {
		tier = normalizeTier(reg.DefaultTier)
		if tier == "" {
			tier = Tier1
		}
	}
	grade := req.EvidenceGrade
	if grade == "" {
		if req.PayloadIntegrityRequired {
			grade = GradeAudit
		} else {
			grade = GradeOperational
		}
	}
	payloadHash := req.PayloadHash
	payloadHashValid := req.PayloadHashValid && payloadHash != ""
	if !payloadHashValid {
		payloadHash, payloadHashValid = safeHash(req.Payload)
	}
	c := CanonicalEvent{
		EventID:                  req.EventID,
		EventType:                req.EventType,
		SchemaVersion:            req.SchemaVersion,
		SourceComponent:          req.SourceComponent,
		RunID:                    req.RunID,
		StepID:                   req.StepID,
		DecisionID:               req.DecisionID,
		TenantID:                 req.TenantID,
		ProjectID:                req.ProjectID,
		WorkflowID:               req.WorkflowID,
		RiskTier:                 req.RiskTier,
		EvidenceTier:             tier,
		EvidenceGrade:            grade,
		PayloadIntegrityRequired: req.PayloadIntegrityRequired,
		EventTS:                  req.EventTS,
		PayloadRef:               req.PayloadRef,
		PayloadHash:              payloadHash,
		PayloadHashValid:         payloadHashValid,
		Payload:                  cloneMap(req.Payload),
		Usage:                    cloneUsage(req.Usage),
	}
	canonicalInput := map[string]interface{}{
		"event_id":         c.EventID,
		"event_type":       c.EventType,
		"run_id":           c.RunID,
		"source_component": c.SourceComponent,
		"event_ts":         c.EventTS.UTC().Format(time.RFC3339Nano),
		"payload_hash":     c.PayloadHash,
		"decision_id":      c.DecisionID,
	}
	c.CanonicalHash, _ = safeHash(canonicalInput)
	if c.CanonicalHash == "" {
		c.CanonicalHash = fallbackHashID(c.EventID, c.RunID, c.EventType)
	}
	return c
}

func estimateEventBytes(e CanonicalEvent) int {
	size := len(e.EventID) + len(e.EventType) + len(e.RunID) + len(e.SourceComponent) + len(e.PayloadRef) + len(e.PayloadHash) + len(e.DecisionID)
	raw, err := json.Marshal(e.Payload)
	if err == nil {
		size += len(raw)
	}
	if size < 128 {
		size = 128
	}
	return size
}

func shouldDropByRate(eventID string, rate float64) bool {
	if rate <= 0 {
		return false
	}
	if rate >= 1 {
		return true
	}
	sum := sha256.Sum256([]byte(eventID))
	v := int(sum[0])
	threshold := int(rate * 255.0)
	return v <= threshold
}

func inferNodeType(source, eventType string) string {
	ev := strings.ToLower(strings.TrimSpace(eventType))
	switch {
	case strings.Contains(ev, "context"):
		return "context"
	case strings.Contains(ev, "policy"):
		return "policy"
	case strings.Contains(ev, "approval"):
		return "approval"
	case strings.Contains(ev, "schedule"):
		return "schedule"
	case strings.Contains(ev, "release"):
		return "release"
	case strings.Contains(ev, "tool"):
		return "tool"
	}
	if source == "decision_kernel" {
		return "decision"
	}
	if source == "run_kernel" {
		return "run"
	}
	return source
}

func isFailureLikeEvent(eventType string) bool {
	ev := strings.ToLower(strings.TrimSpace(eventType))
	for _, token := range []string{"fail", "error", "block", "violation", "deny", "abort", "critical"} {
		if strings.Contains(ev, token) {
			return true
		}
	}
	return false
}

func (k *Kernel) findNodeByEventIDLocked(runID, eventID string) (DecisionGraphNode, bool) {
	for _, n := range k.graphNodesByRun[runID] {
		if n.NodeRef == eventID {
			return n, true
		}
	}
	return DecisionGraphNode{}, false
}

func (k *Kernel) upsertGraphNodeLocked(runID string, node DecisionGraphNode) {
	nodes := k.graphNodesByRun[runID]
	for i := range nodes {
		if nodes[i].NodeID == node.NodeID {
			// Refresh node ref when new evidence links to the same stable node id.
			if strings.TrimSpace(node.NodeRef) != "" {
				nodes[i].NodeRef = node.NodeRef
			}
			k.graphNodesByRun[runID] = nodes
			return
		}
	}
	k.graphNodesByRun[runID] = append(nodes, node)
}

func (k *Kernel) upsertGraphEdgeLocked(runID string, edge DecisionGraphEdge) {
	edges := k.graphEdgesByRun[runID]
	for i := range edges {
		if edges[i].FromNodeID == edge.FromNodeID &&
			edges[i].ToNodeID == edge.ToNodeID &&
			edges[i].EdgeType == edge.EdgeType {
			return
		}
	}
	k.graphEdgesByRun[runID] = append(edges, edge)
}

func stableNodeID(e CanonicalEvent) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		e.RunID,
		e.StepID,
		e.DecisionID,
		e.EventID,
		e.EventType,
	}, "|")))
	return "dgn_" + hex.EncodeToString(sum[:10])
}

func stableEdgeID(runID, fromNodeID, toNodeID, edgeType, eventID string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		runID,
		fromNodeID,
		toNodeID,
		edgeType,
		eventID,
	}, "|")))
	return "dge_" + hex.EncodeToString(sum[:10])
}

func inferEdgeType(eventType string) string {
	ev := strings.ToLower(strings.TrimSpace(eventType))
	switch {
	case strings.Contains(ev, "override"), strings.Contains(ev, "supersede"), strings.Contains(ev, "rollback"):
		return "overrides"
	case strings.Contains(ev, "block"), strings.Contains(ev, "deny"), strings.Contains(ev, "fail"), strings.Contains(ev, "violation"):
		return "blocks"
	case strings.Contains(ev, "approval"), strings.Contains(ev, "derived"), strings.Contains(ev, "compiled"), strings.Contains(ev, "injected"), strings.Contains(ev, "influence"):
		return "influenced_by"
	default:
		return "depends_on"
	}
}

func findFirstBadEvent(events []CanonicalEvent) *CanonicalEvent {
	for i := range events {
		if isStrongFailureEvent(events[i]) {
			return &events[i]
		}
	}
	for i := range events {
		if isFailureLikeEvent(events[i].EventType) {
			return &events[i]
		}
	}
	return nil
}

func isStrongFailureEvent(e CanonicalEvent) bool {
	ev := strings.ToLower(strings.TrimSpace(e.EventType))
	if strings.Contains(ev, "irreversible_progress_violation") ||
		strings.Contains(ev, "force_abort") ||
		strings.Contains(ev, "fail_closed") ||
		strings.Contains(ev, "critical") {
		return true
	}
	decision := strings.ToLower(extractString(e.Payload, "decision"))
	switch decision {
	case "fail_closed", "deny", "force_abort", "force_review_required":
		return true
	}
	return false
}

func (k *Kernel) traceCriticalPathLocked(runID, leafNodeID string) []string {
	if strings.TrimSpace(leafNodeID) == "" {
		return nil
	}
	nodes := k.graphNodesByRun[runID]
	if len(nodes) == 0 {
		return nil
	}
	nodeExists := false
	for _, n := range nodes {
		if n.NodeID == leafNodeID {
			nodeExists = true
			break
		}
	}
	if !nodeExists {
		return nil
	}
	incoming := make(map[string][]DecisionGraphEdge)
	for _, e := range k.graphEdgesByRun[runID] {
		incoming[e.ToNodeID] = append(incoming[e.ToNodeID], e)
	}
	visited := map[string]bool{}
	path := []string{leafNodeID}
	cur := leafNodeID
	for {
		edges := incoming[cur]
		if len(edges) == 0 {
			break
		}
		parent := edges[0].FromNodeID
		if parent == "" || visited[parent] {
			break
		}
		visited[parent] = true
		path = append(path, parent)
		cur = parent
		if len(path) >= 32 {
			break
		}
	}
	// Reverse to root -> leaf.
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

func (k *Kernel) buildKeyEvidencesLocked(runID string, events []CanonicalEvent, criticalPath []string, max int) []KeyEvidence {
	if max <= 0 {
		max = 8
	}
	nodeByID := map[string]DecisionGraphNode{}
	for _, n := range k.graphNodesByRun[runID] {
		nodeByID[n.NodeID] = n
	}
	seen := map[string]bool{}
	out := make([]KeyEvidence, 0, max)
	for _, nodeID := range criticalPath {
		n, ok := nodeByID[nodeID]
		if !ok || strings.TrimSpace(n.NodeRef) == "" || seen[n.NodeRef] {
			continue
		}
		seen[n.NodeRef] = true
		out = append(out, KeyEvidence{EventID: n.NodeRef, EventType: findEventTypeByID(events, n.NodeRef), PayloadRef: findPayloadRefByID(events, n.NodeRef)})
		if len(out) >= max {
			return out
		}
	}
	for i := len(events) - 1; i >= 0 && len(out) < max; i-- {
		e := events[i]
		if seen[e.EventID] {
			continue
		}
		seen[e.EventID] = true
		out = append(out, KeyEvidence{EventID: e.EventID, EventType: e.EventType, PayloadRef: e.PayloadRef})
	}
	return out
}

func findEventTypeByID(events []CanonicalEvent, eventID string) string {
	for i := range events {
		if events[i].EventID == eventID {
			return events[i].EventType
		}
	}
	return ""
}

func findPayloadRefByID(events []CanonicalEvent, eventID string) string {
	for i := range events {
		if events[i].EventID == eventID {
			return events[i].PayloadRef
		}
	}
	return ""
}

func normalizeSource(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func normalizeRisk(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case RiskLow, RiskMedium, RiskHigh, RiskCritical:
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return RiskMedium
	}
}

func normalizeTier(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case Tier0, Tier1, Tier2, Tier3:
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return ""
	}
}

func normalizeGrade(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case GradeAudit, GradeOperational:
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return ""
	}
}

func isTierDroppable(tier string) bool {
	return tier == Tier2 || tier == Tier3
}

func isHighRisk(risk string) bool {
	return risk == RiskHigh || risk == RiskCritical
}

func extractString(m map[string]interface{}, key string) string {
	if len(m) == 0 {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(fmt.Sprintf("%v", v))
}

func extractStringSliceOrScalar(m map[string]interface{}, key string) []string {
	out := extractStringSlice(m, key)
	if len(out) > 0 {
		return out
	}
	v := extractString(m, key)
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return []string{v}
}

func extractStringSlice(m map[string]interface{}, key string) []string {
	if len(m) == 0 {
		return nil
	}
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	switch t := v.(type) {
	case []string:
		out := make([]string, 0, len(t))
		for _, s := range t {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s := strings.TrimSpace(fmt.Sprintf("%v", item))
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func extractFloat(m map[string]interface{}, key string) float64 {
	if len(m) == 0 {
		return 0
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int:
		return float64(t)
	case int64:
		return float64(t)
	default:
		return 0
	}
}

func boolToFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

func ratio(num, den float64) float64 {
	if den <= 0 {
		return 0
	}
	return (num / den) * 100.0
}

func buildArchiveChainRoot(events []CanonicalEvent) string {
	if len(events) == 0 {
		return ""
	}
	prev := ""
	for _, e := range events {
		link := strings.TrimSpace(e.IntegrityHash)
		if link == "" {
			link = strings.TrimSpace(e.CanonicalHash)
		}
		if link == "" {
			link = strings.TrimSpace(e.EventID)
		}
		sum := sha256.Sum256([]byte(strings.Join([]string{
			prev,
			e.RunID,
			e.EventID,
			link,
			e.EventTS.UTC().Format(time.RFC3339Nano),
		}, "|")))
		prev = hex.EncodeToString(sum[:])
	}
	return prev
}

func estimateEvidenceCompleteness(e CanonicalEvent, decisionValue string) float64 {
	score := 0.0
	if e.PayloadHashValid {
		score += 0.4
	}
	if strings.TrimSpace(decisionValue) != "" && strings.TrimSpace(decisionValue) != "unknown" {
		score += 0.2
	}
	if strings.TrimSpace(extractString(e.Payload, "rationale_ref")) != "" || strings.TrimSpace(e.PayloadRef) != "" {
		score += 0.3
	}
	if extractFloat(e.Payload, "decision_confidence") > 0 {
		score += 0.1
	}
	if score > 1 {
		return 1
	}
	return score
}

func cloneMap(in map[string]interface{}) map[string]interface{} {
	if in == nil {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneUsage(in *UsageInput) *UsageInput {
	if in == nil {
		return nil
	}
	cp := *in
	return &cp
}

func cloneIntMap(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneFloatMap(in map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func appendUniqueString(in []string, v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return in
	}
	for _, x := range in {
		if x == v {
			return in
		}
	}
	return append(in, v)
}

func appendUniqueStrings(in []string, values []string) []string {
	for _, v := range values {
		in = appendUniqueString(in, v)
	}
	return in
}

func isPayloadTombstoned(tombstoned map[string]time.Time, payloadRef string) bool {
	payloadRef = strings.TrimSpace(payloadRef)
	if payloadRef == "" {
		return false
	}
	_, ok := tombstoned[payloadRef]
	return ok
}

func ledgerGroupKey(e LedgerEntry, groupBy string) string {
	switch groupBy {
	case "run":
		return e.RunID
	case "tenant":
		return e.TenantID
	case "workflow":
		return e.WorkflowID
	default:
		return e.ResourceType
	}
}

func detectLedgerAnomaly(row LedgerAggregateRow) (float64, string) {
	if row.EntryCount == 0 {
		return 0, ""
	}
	usageBurst := 0.0
	if row.AvgUsage > 0 {
		usageBurst = row.MaxUsage / row.AvgUsage
	}
	costDensity := 0.0
	if row.TotalUsage > 0 {
		costDensity = row.TotalCost / row.TotalUsage
	}
	score := 0.0
	reason := ""
	if usageBurst > 5 {
		score += 0.6
		reason = appendReason(reason, "usage_burst")
	}
	if costDensity > 10 {
		score += 0.4
		reason = appendReason(reason, "high_cost_density")
	}
	if score > 1 {
		score = 1
	}
	return score, reason
}

func appendReason(existing, next string) string {
	if existing == "" {
		return next
	}
	if next == "" {
		return existing
	}
	return existing + "|" + next
}

func (k *Kernel) nextID(prefix string) string {
	seq := atomic.AddUint64(&k.idSeq, 1)
	return fmt.Sprintf("%s%d", prefix, seq)
}

func safeHash(v interface{}) (string, bool) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), true
}

func fallbackHashID(eventID, runID, eventType string) string {
	sum := sha256.Sum256([]byte(eventID + "|" + runID + "|" + eventType))
	return "fallback_" + hex.EncodeToString(sum[:8])
}

func evidenceErr(status int, code, msg string, reasons ...string) error {
	return &KernelError{
		StatusCode:  status,
		Code:        code,
		Message:     msg,
		ReasonCodes: append([]string(nil), reasons...),
	}
}

func sortEventsByTS(in []CanonicalEvent) {
	sort.Slice(in, func(i, j int) bool {
		if in[i].EventTS.Equal(in[j].EventTS) {
			return in[i].EventID < in[j].EventID
		}
		return in[i].EventTS.Before(in[j].EventTS)
	})
}
