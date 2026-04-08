package decision

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	eventFeatureDefinitionCreated        = "feature.definition.created"
	eventFeatureVersionCreated           = "feature.version.created"
	eventFeatureVersionPublished         = "feature.version.published"
	eventFeatureSnapshotBuilt            = "feature.snapshot.built"
	eventFeatureRollbackCreated          = "feature.rollback.created"
	eventFeatureSignalContractPublished  = "feature.signal_contract.published"
	eventFeatureSignalContractRolledBack = "feature.signal_contract.rolled_back"
	eventFeatureSignalContractActivated  = "feature.signal_contract.activated"
	eventApprovalOrgHealthRecomputed     = "approval.org_health.recomputed"
	eventApprovalOrgHealthRemediated     = "approval.org_health.remediated"
	eventMetricsEnforcementMatrixPublish = "metrics.enforcement_matrix.published"
)

func controlErr(status int, code, msg string, reasons ...string) error {
	return &ControlError{
		StatusCode:  status,
		Code:        code,
		Message:     msg,
		ReasonCodes: append([]string(nil), reasons...),
	}
}

func normalizeScope(in ScopeRef) ScopeRef {
	return ScopeRef{
		OrgID:       strings.TrimSpace(in.OrgID),
		WorkspaceID: strings.TrimSpace(in.WorkspaceID),
		ProjectID:   strings.TrimSpace(in.ProjectID),
	}
}

func scopeKey(scope ScopeRef) string {
	s := normalizeScope(scope)
	return strings.ToLower(s.OrgID + "|" + s.WorkspaceID + "|" + s.ProjectID)
}

func sameScope(a, b ScopeRef) bool {
	return scopeKey(a) == scopeKey(b)
}

func missingScopeFields(scope ScopeRef) []string {
	s := normalizeScope(scope)
	missing := make([]string, 0, 3)
	if s.OrgID == "" {
		missing = append(missing, "org_id")
	}
	if s.WorkspaceID == "" {
		missing = append(missing, "workspace_id")
	}
	if s.ProjectID == "" {
		missing = append(missing, "project_id")
	}
	return missing
}

func normalizeFeatureID(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func normalizeVersion(v string) string {
	return strings.TrimSpace(v)
}

func normalizeRiskTier(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func globalContractScope() ScopeRef {
	return ScopeRef{OrgID: "*", WorkspaceID: "*", ProjectID: "*"}
}

func normalizeContractScope(scope ScopeRef) ScopeRef {
	s := normalizeScope(scope)
	if s.OrgID == "" && s.WorkspaceID == "" && s.ProjectID == "" {
		return globalContractScope()
	}
	if s.OrgID == "" {
		s.OrgID = "*"
	}
	if s.WorkspaceID == "" {
		s.WorkspaceID = "*"
	}
	if s.ProjectID == "" {
		s.ProjectID = "*"
	}
	return s
}

func featureSignalContractStorageKey(scope ScopeRef, contractKey string) string {
	return scopeKey(normalizeContractScope(scope)) + "::" + strings.TrimSpace(contractKey)
}

func safeHashForContract(contract FeatureSignalContract) string {
	normalized := FeatureSignalContract{
		RequiredFields:     normalizeRequiredFields(contract.RequiredFields),
		SchemaVersion:      strings.TrimSpace(contract.SchemaVersion),
		TrustedProducerIDs: normalizeTrustedProducerIDs(contract.TrustedProducerIDs),
		MaxFreshnessMS:     contract.MaxFreshnessMS,
		MaxDriftScore:      contract.MaxDriftScore,
	}
	h, ok := safeHash(normalized)
	if !ok {
		return ""
	}
	return h
}

func normalizeContractKey(riskTier, phase string) string {
	rt := normalizeRiskTier(riskTier)
	if rt == "" {
		rt = "*"
	}
	p := strings.ToUpper(strings.TrimSpace(phase))
	if p == "" {
		p = "*"
	}
	return rt + "|" + p
}

func splitContractKey(key string) (riskTier, phase string) {
	parts := strings.SplitN(strings.TrimSpace(key), "|", 2)
	if len(parts) == 0 {
		return "*", "*"
	}
	riskTier = strings.TrimSpace(parts[0])
	if riskTier == "" {
		riskTier = "*"
	}
	if len(parts) == 1 {
		return riskTier, "*"
	}
	phase = strings.TrimSpace(parts[1])
	if phase == "" {
		phase = "*"
	}
	return riskTier, phase
}

func parseFeatureSignalContractStorageKey(storageKey string) (ScopeRef, string) {
	parts := strings.SplitN(strings.TrimSpace(storageKey), "::", 2)
	if len(parts) != 2 {
		return globalContractScope(), strings.TrimSpace(storageKey)
	}
	scopePart := strings.TrimSpace(parts[0])
	scopeFields := strings.SplitN(scopePart, "|", 3)
	scope := globalContractScope()
	if len(scopeFields) == 3 {
		scope = ScopeRef{
			OrgID:       strings.TrimSpace(scopeFields[0]),
			WorkspaceID: strings.TrimSpace(scopeFields[1]),
			ProjectID:   strings.TrimSpace(scopeFields[2]),
		}
	}
	scope = normalizeContractScope(scope)
	return scope, strings.TrimSpace(parts[1])
}

func normalizeRequiredFields(fields []string) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ToLower(strings.TrimSpace(f))
		if f == "" {
			continue
		}
		out = append(out, f)
	}
	sort.Strings(out)
	return uniqueStrings(out)
}

func validateFeatureSignalContractRequiredFields(fields []string) error {
	if len(fields) == 0 {
		return controlErr(422, "FEATURE_SIGNAL_CONTRACT_FIELDS_REQUIRED", "required_fields must not be empty", "feature_signal_contract_fields_required")
	}
	allowed := map[string]struct{}{
		"feature_version":        {},
		"feature_schema_version": {},
		"feature_evidence_ref":   {},
		"feature_producer_id":    {},
	}
	invalid := make([]string, 0, 2)
	for _, f := range fields {
		if _, ok := allowed[f]; !ok {
			invalid = append(invalid, f)
		}
	}
	if len(invalid) > 0 {
		return controlErr(422, "FEATURE_SIGNAL_CONTRACT_FIELDS_INVALID", "required_fields contains unsupported entries", invalid...)
	}
	return nil
}

func (s *Service) ListFeatureSignalContracts() []FeatureSignalContractView {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	out := make([]FeatureSignalContractView, 0, len(s.featureSignalContractsScoped))
	for storageKey, contract := range s.featureSignalContractsScoped {
		scope, key := parseFeatureSignalContractStorageKey(storageKey)
		rt, phase := splitContractKey(key)
		active := s.activeFeatureSignalContractVersionLocked(storageKey)
		version := active.Version
		if version <= 0 {
			version = 1
		}
		contractHash := active.ContractHash
		if strings.TrimSpace(contractHash) == "" {
			contractHash = safeHashForContract(contract)
		}
		activationAt := active.ActivationAt
		if activationAt.IsZero() {
			activationAt = s.featureSignalContractMeta[storageKey]
		}
		out = append(out, FeatureSignalContractView{
			ContractKey:        key,
			StorageKey:         storageKey,
			RiskTier:           rt,
			Phase:              phase,
			RequiredFields:     append([]string(nil), contract.RequiredFields...),
			SchemaVersion:      contract.SchemaVersion,
			TrustedProducerIDs: append([]string(nil), contract.TrustedProducerIDs...),
			MaxFreshnessMS:     contract.MaxFreshnessMS,
			MaxDriftScore:      contract.MaxDriftScore,
			Scope:              scope,
			Version:            version,
			ContractHash:       contractHash,
			Status:             "active",
			ActivationAt:       activationAt,
			UpdatedAt:          s.featureSignalContractMeta[storageKey],
		})
	}
	for storageKey, scheduled := range s.featureSignalContractScheduled {
		scope, key := parseFeatureSignalContractStorageKey(storageKey)
		rt, phase := splitContractKey(key)
		for _, item := range scheduled {
			out = append(out, FeatureSignalContractView{
				ContractKey:        key,
				StorageKey:         storageKey,
				RiskTier:           rt,
				Phase:              phase,
				RequiredFields:     append([]string(nil), item.RequiredFields...),
				SchemaVersion:      item.SchemaVersion,
				TrustedProducerIDs: append([]string(nil), item.TrustedProducerIDs...),
				MaxFreshnessMS:     item.MaxFreshnessMS,
				MaxDriftScore:      item.MaxDriftScore,
				Scope:              scope,
				Version:            item.Version,
				ContractHash:       item.ContractHash,
				Status:             "scheduled",
				ActivationAt:       item.ActivationAt,
				UpdatedAt:          item.CreatedAt,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if scopeKey(out[i].Scope) != scopeKey(out[j].Scope) {
			return scopeKey(out[i].Scope) < scopeKey(out[j].Scope)
		}
		if out[i].StorageKey != out[j].StorageKey {
			return out[i].StorageKey < out[j].StorageKey
		}
		if out[i].Version != out[j].Version {
			return out[i].Version < out[j].Version
		}
		return out[i].ContractKey < out[j].ContractKey
	})
	return out
}

func (s *Service) GetFeatureSignalContract(riskTier, phase string) (FeatureSignalContractView, bool) {
	return s.GetFeatureSignalContractForScope(riskTier, phase, globalContractScope())
}

func (s *Service) GetFeatureSignalContractForScope(riskTier, phase string, scope ScopeRef) (FeatureSignalContractView, bool) {
	key := normalizeContractKey(riskTier, phase)
	scope = normalizeContractScope(scope)
	storageKey := featureSignalContractStorageKey(scope, key)
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	contract, ok := s.featureSignalContractsScoped[storageKey]
	rt, p := splitContractKey(key)
	if !ok {
		if scheduled := s.featureSignalContractScheduled[storageKey]; len(scheduled) > 0 {
			sched := append([]FeatureSignalContractVersion(nil), scheduled...)
			sort.Slice(sched, func(i, j int) bool {
				if sched[i].ActivationAt.Equal(sched[j].ActivationAt) {
					return sched[i].Version < sched[j].Version
				}
				return sched[i].ActivationAt.Before(sched[j].ActivationAt)
			})
			item := sched[len(sched)-1]
			return FeatureSignalContractView{
				ContractKey:        key,
				StorageKey:         storageKey,
				RiskTier:           rt,
				Phase:              p,
				RequiredFields:     append([]string(nil), item.RequiredFields...),
				SchemaVersion:      item.SchemaVersion,
				TrustedProducerIDs: append([]string(nil), item.TrustedProducerIDs...),
				MaxFreshnessMS:     item.MaxFreshnessMS,
				MaxDriftScore:      item.MaxDriftScore,
				Scope:              scope,
				Version:            item.Version,
				ContractHash:       item.ContractHash,
				Status:             "scheduled",
				ActivationAt:       item.ActivationAt,
				UpdatedAt:          item.CreatedAt,
			}, true
		}
		return FeatureSignalContractView{}, false
	}
	active := s.activeFeatureSignalContractVersionLocked(storageKey)
	version := active.Version
	if version <= 0 {
		version = 1
	}
	contractHash := active.ContractHash
	if strings.TrimSpace(contractHash) == "" {
		contractHash = safeHashForContract(contract)
	}
	activationAt := active.ActivationAt
	if activationAt.IsZero() {
		activationAt = s.featureSignalContractMeta[storageKey]
	}
	return FeatureSignalContractView{
		ContractKey:        key,
		StorageKey:         storageKey,
		RiskTier:           rt,
		Phase:              p,
		RequiredFields:     append([]string(nil), contract.RequiredFields...),
		SchemaVersion:      contract.SchemaVersion,
		TrustedProducerIDs: append([]string(nil), contract.TrustedProducerIDs...),
		MaxFreshnessMS:     contract.MaxFreshnessMS,
		MaxDriftScore:      contract.MaxDriftScore,
		Scope:              scope,
		Version:            version,
		ContractHash:       contractHash,
		Status:             "active",
		ActivationAt:       activationAt,
		UpdatedAt:          s.featureSignalContractMeta[storageKey],
	}, true
}

func (s *Service) PublishFeatureSignalContract(req FeatureSignalContractPublishRequest) (FeatureSignalContractView, error) {
	riskTier := normalizeRiskTier(req.RiskTier)
	if riskTier == "" {
		return FeatureSignalContractView{}, controlErr(422, "FEATURE_SIGNAL_CONTRACT_RISK_REQUIRED", "risk_tier required", "feature_signal_contract_risk_required")
	}
	switch riskTier {
	case RiskLow, RiskMedium, RiskHigh, RiskCritical, "*":
	default:
		return FeatureSignalContractView{}, controlErr(422, "FEATURE_SIGNAL_CONTRACT_RISK_INVALID", "invalid risk_tier", "feature_signal_contract_risk_invalid")
	}
	phase := strings.ToUpper(strings.TrimSpace(req.Phase))
	if phase != "" {
		switch phase {
		case PhasePreContext, PhasePreTool, PhasePreResume, PhasePreRelease, "*":
		default:
			return FeatureSignalContractView{}, controlErr(422, "FEATURE_SIGNAL_CONTRACT_PHASE_INVALID", "invalid phase", "feature_signal_contract_phase_invalid")
		}
	}
	required := normalizeRequiredFields(req.RequiredFields)
	if err := validateFeatureSignalContractRequiredFields(required); err != nil {
		return FeatureSignalContractView{}, err
	}
	schemaVersion := strings.TrimSpace(req.SchemaVersion)
	trustedProducers := normalizeTrustedProducerIDs(req.TrustedProducerIDs)
	if req.MaxFreshnessMS < 0 {
		return FeatureSignalContractView{}, controlErr(422, "FEATURE_SIGNAL_CONTRACT_FRESHNESS_INVALID", "max_freshness_ms must be >= 0", "feature_signal_contract_freshness_invalid")
	}
	if req.MaxDriftScore < 0 {
		return FeatureSignalContractView{}, controlErr(422, "FEATURE_SIGNAL_CONTRACT_DRIFT_INVALID", "max_drift_score must be >= 0", "feature_signal_contract_drift_invalid")
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		return FeatureSignalContractView{}, controlErr(422, "FEATURE_SIGNAL_CONTRACT_REASON_REQUIRED", "reason required", "feature_signal_contract_reason_required")
	}
	actor := strings.TrimSpace(req.Actor)
	scope := normalizeContractScope(req.Scope)
	now := s.cfg.Clock()
	key := normalizeContractKey(riskTier, phase)
	storageKey := featureSignalContractStorageKey(scope, key)
	rt, p := splitContractKey(key)
	contractHash := safeHashForContract(FeatureSignalContract{
		RequiredFields:     append([]string(nil), required...),
		SchemaVersion:      schemaVersion,
		TrustedProducerIDs: append([]string(nil), trustedProducers...),
		MaxFreshnessMS:     req.MaxFreshnessMS,
		MaxDriftScore:      req.MaxDriftScore,
	})
	activationAt := req.ActivateAt
	if activationAt.IsZero() {
		activationAt = now
	}
	compatibility := "compatible"
	nextVersion := 1
	s.runtimeMu.RLock()
	current, hasCurrent := s.featureSignalContractsScoped[storageKey]
	history := s.featureSignalContractHistory[storageKey]
	s.runtimeMu.RUnlock()
	if hasCurrent {
		if removed := removedRequiredFields(current.RequiredFields, required); len(removed) > 0 {
			if !req.AllowBreakingChange {
				return FeatureSignalContractView{}, controlErr(409, "FEATURE_SIGNAL_CONTRACT_BREAKING_CHANGE", "breaking contract change requires allow_breaking_change=true", removed...)
			}
			compatibility = "breaking_allowed"
		}
		if isSchemaBreakingChange(current.SchemaVersion, schemaVersion) {
			if !req.AllowBreakingChange {
				return FeatureSignalContractView{}, controlErr(409, "FEATURE_SIGNAL_CONTRACT_SCHEMA_BREAKING_CHANGE", "schema version breaking change requires allow_breaking_change=true", "feature_signal_contract_schema_breaking_change")
			}
			compatibility = "breaking_allowed"
		}
	}
	if len(history) > 0 {
		nextVersion = history[len(history)-1].Version + 1
	}
	view := FeatureSignalContractView{
		ContractKey:        key,
		StorageKey:         storageKey,
		RiskTier:           rt,
		Phase:              p,
		RequiredFields:     append([]string(nil), required...),
		SchemaVersion:      schemaVersion,
		TrustedProducerIDs: append([]string(nil), trustedProducers...),
		MaxFreshnessMS:     req.MaxFreshnessMS,
		MaxDriftScore:      req.MaxDriftScore,
		Scope:              scope,
		Version:            nextVersion,
		ContractHash:       contractHash,
		Status:             contractPublishStatus(activationAt, now),
		ActivationAt:       activationAt,
		UpdatedAt:          now,
	}
	if req.DryRun {
		return view, nil
	}

	s.runtimeMu.Lock()
	candidate := s.buildContractStoreSnapshotLocked(now)
	currentCandidate, ok := candidate.Active[storageKey]
	if ok {
		if removed := removedRequiredFields(currentCandidate.RequiredFields, required); len(removed) > 0 {
			if !req.AllowBreakingChange {
				s.runtimeMu.Unlock()
				return FeatureSignalContractView{}, controlErr(409, "FEATURE_SIGNAL_CONTRACT_BREAKING_CHANGE", "breaking contract change requires allow_breaking_change=true", removed...)
			}
			compatibility = "breaking_allowed"
		}
		if isSchemaBreakingChange(currentCandidate.SchemaVersion, schemaVersion) {
			if !req.AllowBreakingChange {
				s.runtimeMu.Unlock()
				return FeatureSignalContractView{}, controlErr(409, "FEATURE_SIGNAL_CONTRACT_SCHEMA_BREAKING_CHANGE", "schema version breaking change requires allow_breaking_change=true", "feature_signal_contract_schema_breaking_change")
			}
			compatibility = "breaking_allowed"
		}
	}
	nextVersion = 1
	if hist := candidate.History[storageKey]; len(hist) > 0 {
		nextVersion = hist[len(hist)-1].Version + 1
	}
	view.Version = nextVersion
	view.Status = contractPublishStatus(activationAt, now)
	view.ActivationAt = activationAt
	view.UpdatedAt = now
	versionRecord := FeatureSignalContractVersion{
		Version:            nextVersion,
		ContractKey:        key,
		StorageKey:         storageKey,
		RiskTier:           rt,
		Phase:              p,
		RequiredFields:     append([]string(nil), required...),
		SchemaVersion:      schemaVersion,
		TrustedProducerIDs: append([]string(nil), trustedProducers...),
		MaxFreshnessMS:     req.MaxFreshnessMS,
		MaxDriftScore:      req.MaxDriftScore,
		Scope:              scope,
		ContractHash:       contractHash,
		Status:             contractPublishStatus(activationAt, now),
		ActivationAt:       activationAt,
		Reason:             reason,
		Actor:              actor,
		CreatedAt:          now,
	}
	if activationAt.After(now) {
		candidate.Scheduled[storageKey] = append(candidate.Scheduled[storageKey], versionRecord)
	} else {
		candidate.Active[storageKey] = FeatureSignalContract{
			RequiredFields:     append([]string(nil), required...),
			SchemaVersion:      schemaVersion,
			TrustedProducerIDs: append([]string(nil), trustedProducers...),
			MaxFreshnessMS:     req.MaxFreshnessMS,
			MaxDriftScore:      req.MaxDriftScore,
		}
		candidate.Meta[storageKey] = now
	}
	candidate.History[storageKey] = append(candidate.History[storageKey], versionRecord)
	if !activationAt.After(now) {
		setFeatureSignalContractActiveVersion(candidate.History, storageKey, nextVersion, now)
	}
	candidate.Revision = nextStoreRevision(s.storeVersion)
	if err := s.commitContractSnapshotLocked(candidate); err != nil {
		s.runtimeMu.Unlock()
		return FeatureSignalContractView{}, controlErr(503, "FEATURE_SIGNAL_CONTRACT_STORE_SYNC_FAILED", "failed to sync feature signal contract authoritative store", "feature_signal_contract_store_sync_failed")
	}
	s.runtimeMu.Unlock()
	s.emitEvent(eventFeatureSignalContractPublished, "", "", "", map[string]interface{}{
		"contract_key":         key,
		"storage_key":          storageKey,
		"scope":                scope,
		"risk_tier":            rt,
		"phase":                p,
		"required_fields":      required,
		"schema_version":       schemaVersion,
		"trusted_producer_ids": append([]string(nil), trustedProducers...),
		"max_freshness_ms":     req.MaxFreshnessMS,
		"max_drift_score":      req.MaxDriftScore,
		"version":              nextVersion,
		"contract_hash":        contractHash,
		"compatibility":        compatibility,
		"status":               contractPublishStatus(activationAt, now),
		"activation_at":        activationAt,
		"reason":               reason,
		"actor":                actor,
	}, now)
	return view, nil
}

func (s *Service) ValidateFeatureSignalContract(req FeatureSignalContractPublishRequest) FeatureSignalContractValidateResponse {
	riskTier := normalizeRiskTier(req.RiskTier)
	phase := strings.ToUpper(strings.TrimSpace(req.Phase))
	required := normalizeRequiredFields(req.RequiredFields)
	resp := FeatureSignalContractValidateResponse{
		Valid:              true,
		ContractKey:        normalizeContractKey(riskTier, phase),
		NormalizedRiskTier: riskTier,
		NormalizedPhase:    phase,
		NormalizedFields:   append([]string(nil), required...),
	}
	if riskTier == "" {
		resp.Valid = false
		resp.ReasonCodes = append(resp.ReasonCodes, "feature_signal_contract_risk_required")
	}
	if phase != "" {
		switch phase {
		case PhasePreContext, PhasePreTool, PhasePreResume, PhasePreRelease, "*":
		default:
			resp.Valid = false
			resp.ReasonCodes = append(resp.ReasonCodes, "feature_signal_contract_phase_invalid")
		}
	}
	if err := validateFeatureSignalContractRequiredFields(required); err != nil {
		resp.Valid = false
		resp.ReasonCodes = append(resp.ReasonCodes, "feature_signal_contract_fields_invalid")
	}
	if strings.TrimSpace(req.Reason) == "" {
		resp.Valid = false
		resp.ReasonCodes = append(resp.ReasonCodes, "feature_signal_contract_reason_required")
	}
	if req.MaxFreshnessMS < 0 {
		resp.Valid = false
		resp.ReasonCodes = append(resp.ReasonCodes, "feature_signal_contract_freshness_invalid")
	}
	if req.MaxDriftScore < 0 {
		resp.Valid = false
		resp.ReasonCodes = append(resp.ReasonCodes, "feature_signal_contract_drift_invalid")
	}
	return resp
}

func (s *Service) GetFeatureSignalContractHistory(riskTier, phase string, limit int) ([]FeatureSignalContractVersion, bool) {
	return s.GetFeatureSignalContractHistoryForScope(riskTier, phase, globalContractScope(), limit)
}

func (s *Service) GetFeatureSignalContractHistoryForScope(riskTier, phase string, scope ScopeRef, limit int) ([]FeatureSignalContractVersion, bool) {
	key := normalizeContractKey(riskTier, phase)
	scope = normalizeContractScope(scope)
	storageKey := featureSignalContractStorageKey(scope, key)
	if limit <= 0 {
		limit = 20
	}
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	h, ok := s.featureSignalContractHistory[storageKey]
	if !ok || len(h) == 0 {
		return nil, false
	}
	start := len(h) - limit
	if start < 0 {
		start = 0
	}
	out := make([]FeatureSignalContractVersion, len(h[start:]))
	copy(out, h[start:])
	return out, true
}

func (s *Service) RollbackFeatureSignalContract(req FeatureSignalContractRollbackRequest) (FeatureSignalContractRollbackRecord, error) {
	riskTier := normalizeRiskTier(req.RiskTier)
	if riskTier == "" {
		return FeatureSignalContractRollbackRecord{}, controlErr(422, "FEATURE_SIGNAL_CONTRACT_RISK_REQUIRED", "risk_tier required", "feature_signal_contract_risk_required")
	}
	phase := strings.ToUpper(strings.TrimSpace(req.Phase))
	if phase != "" {
		switch phase {
		case PhasePreContext, PhasePreTool, PhasePreResume, PhasePreRelease, "*":
		default:
			return FeatureSignalContractRollbackRecord{}, controlErr(422, "FEATURE_SIGNAL_CONTRACT_PHASE_INVALID", "invalid phase", "feature_signal_contract_phase_invalid")
		}
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		return FeatureSignalContractRollbackRecord{}, controlErr(422, "FEATURE_SIGNAL_CONTRACT_REASON_REQUIRED", "reason required", "feature_signal_contract_reason_required")
	}
	key := normalizeContractKey(riskTier, phase)
	scope := normalizeContractScope(req.Scope)
	storageKey := featureSignalContractStorageKey(scope, key)
	actor := strings.TrimSpace(req.Actor)
	now := s.cfg.Clock()

	s.runtimeMu.Lock()
	candidate := s.buildContractStoreSnapshotLocked(now)
	history := candidate.History[storageKey]
	if len(history) == 0 {
		s.runtimeMu.Unlock()
		return FeatureSignalContractRollbackRecord{}, controlErr(404, "FEATURE_SIGNAL_CONTRACT_NOT_FOUND", "feature signal contract history not found", "feature_signal_contract_not_found")
	}
	current := history[len(history)-1]
	target := current
	if req.TargetVersion > 0 {
		found := false
		for _, v := range history {
			if v.Version == req.TargetVersion {
				target = v
				found = true
				break
			}
		}
		if !found {
			s.runtimeMu.Unlock()
			return FeatureSignalContractRollbackRecord{}, controlErr(404, "FEATURE_SIGNAL_CONTRACT_TARGET_VERSION_NOT_FOUND", "target feature signal contract version not found", "feature_signal_contract_target_version_not_found")
		}
	} else {
		if len(history) < 2 {
			s.runtimeMu.Unlock()
			return FeatureSignalContractRollbackRecord{}, controlErr(409, "FEATURE_SIGNAL_CONTRACT_NO_PREVIOUS_VERSION", "no previous feature signal contract version available for rollback", "feature_signal_contract_no_previous_version")
		}
		target = history[len(history)-2]
	}

	nextVersion := history[len(history)-1].Version + 1
	candidate.Active[storageKey] = FeatureSignalContract{
		RequiredFields:     append([]string(nil), target.RequiredFields...),
		SchemaVersion:      target.SchemaVersion,
		TrustedProducerIDs: append([]string(nil), target.TrustedProducerIDs...),
		MaxFreshnessMS:     target.MaxFreshnessMS,
		MaxDriftScore:      target.MaxDriftScore,
	}
	candidate.Meta[storageKey] = now
	rt, p := splitContractKey(key)
	applied := FeatureSignalContractVersion{
		Version:            nextVersion,
		ContractKey:        key,
		StorageKey:         storageKey,
		RiskTier:           rt,
		Phase:              p,
		RequiredFields:     append([]string(nil), target.RequiredFields...),
		SchemaVersion:      target.SchemaVersion,
		TrustedProducerIDs: append([]string(nil), target.TrustedProducerIDs...),
		MaxFreshnessMS:     target.MaxFreshnessMS,
		MaxDriftScore:      target.MaxDriftScore,
		Scope:              scope,
		ContractHash: safeHashForContract(FeatureSignalContract{
			RequiredFields:     append([]string(nil), target.RequiredFields...),
			SchemaVersion:      target.SchemaVersion,
			TrustedProducerIDs: append([]string(nil), target.TrustedProducerIDs...),
			MaxFreshnessMS:     target.MaxFreshnessMS,
			MaxDriftScore:      target.MaxDriftScore,
		}),
		Status:       "active",
		ActivationAt: now,
		Reason:       "rollback:" + reason,
		Actor:        actor,
		CreatedAt:    now,
	}
	candidate.History[storageKey] = append(candidate.History[storageKey], applied)
	setFeatureSignalContractActiveVersion(candidate.History, storageKey, nextVersion, now)
	record := FeatureSignalContractRollbackRecord{
		RollbackID:             s.nextFallbackID("fscrb_"),
		ContractKey:            key,
		StorageKey:             storageKey,
		Scope:                  scope,
		PreviousVersion:        current.Version,
		TargetVersion:          target.Version,
		PreviousRequiredFields: append([]string(nil), current.RequiredFields...),
		TargetRequiredFields:   append([]string(nil), target.RequiredFields...),
		PreviousContractHash:   current.ContractHash,
		TargetContractHash:     target.ContractHash,
		Reason:                 reason,
		Actor:                  actor,
		CreatedAt:              now,
	}
	candidate.Rollbacks[storageKey] = append(candidate.Rollbacks[storageKey], record)
	candidate.Revision = nextStoreRevision(s.storeVersion)
	if err := s.commitContractSnapshotLocked(candidate); err != nil {
		s.runtimeMu.Unlock()
		return FeatureSignalContractRollbackRecord{}, controlErr(503, "FEATURE_SIGNAL_CONTRACT_STORE_SYNC_FAILED", "failed to sync feature signal contract authoritative store", "feature_signal_contract_store_sync_failed")
	}
	s.runtimeMu.Unlock()
	s.emitEvent(eventFeatureSignalContractRolledBack, "", "", "", map[string]interface{}{
		"contract_key":         key,
		"storage_key":          storageKey,
		"scope":                scope,
		"previous_version":     current.Version,
		"target_version":       target.Version,
		"applied_version":      nextVersion,
		"schema_version":       target.SchemaVersion,
		"trusted_producer_ids": append([]string(nil), target.TrustedProducerIDs...),
		"max_freshness_ms":     target.MaxFreshnessMS,
		"max_drift_score":      target.MaxDriftScore,
		"reason":               reason,
		"actor":                actor,
		"rollback_id":          record.RollbackID,
	}, now)
	return record, nil
}

func (s *Service) ListFeatureSignalContractRollbacks(riskTier, phase string, limit int) ([]FeatureSignalContractRollbackRecord, bool) {
	return s.ListFeatureSignalContractRollbacksForScope(riskTier, phase, globalContractScope(), limit)
}

func (s *Service) ListFeatureSignalContractRollbacksForScope(riskTier, phase string, scope ScopeRef, limit int) ([]FeatureSignalContractRollbackRecord, bool) {
	key := normalizeContractKey(riskTier, phase)
	scope = normalizeContractScope(scope)
	storageKey := featureSignalContractStorageKey(scope, key)
	if limit <= 0 {
		limit = 20
	}
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	src := s.featureSignalContractRollbacks[storageKey]
	if len(src) == 0 {
		return nil, false
	}
	start := len(src) - limit
	if start < 0 {
		start = 0
	}
	out := make([]FeatureSignalContractRollbackRecord, len(src[start:]))
	copy(out, src[start:])
	return out, true
}

func contractPublishStatus(activationAt, now time.Time) string {
	if activationAt.After(now) {
		return "scheduled"
	}
	return "active"
}

func removedRequiredFields(previous, next []string) []string {
	prevSet := make(map[string]struct{}, len(previous))
	for _, f := range normalizeRequiredFields(previous) {
		prevSet[f] = struct{}{}
	}
	nextSet := make(map[string]struct{}, len(next))
	for _, f := range normalizeRequiredFields(next) {
		nextSet[f] = struct{}{}
	}
	removed := make([]string, 0, len(prevSet))
	for f := range prevSet {
		if _, ok := nextSet[f]; !ok {
			removed = append(removed, f)
		}
	}
	sort.Strings(removed)
	return removed
}

func normalizeTrustedProducerIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func isSchemaBreakingChange(previous, next string) bool {
	prevMajor, prevOK := schemaMajor(previous)
	nextMajor, nextOK := schemaMajor(next)
	if !prevOK || !nextOK {
		return false
	}
	return prevMajor != nextMajor
}

func schemaMajor(v string) (int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	v = strings.TrimPrefix(strings.ToLower(v), "v")
	parts := strings.SplitN(v, ".", 2)
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, false
	}
	if n < 0 {
		return 0, false
	}
	return n, true
}

func buildFeatureSignalContractFromVersion(item FeatureSignalContractVersion) FeatureSignalContract {
	return FeatureSignalContract{
		RequiredFields:     append([]string(nil), item.RequiredFields...),
		SchemaVersion:      item.SchemaVersion,
		TrustedProducerIDs: append([]string(nil), item.TrustedProducerIDs...),
		MaxFreshnessMS:     item.MaxFreshnessMS,
		MaxDriftScore:      item.MaxDriftScore,
	}
}

func setFeatureSignalContractActiveVersion(
	history map[string][]FeatureSignalContractVersion,
	storageKey string,
	activeVersion int,
	activatedAt time.Time,
) {
	h := history[storageKey]
	if len(h) == 0 {
		return
	}
	for i := range h {
		if h[i].Version == activeVersion {
			h[i].Status = "active"
			h[i].ActivationAt = activatedAt
			continue
		}
		if strings.EqualFold(strings.TrimSpace(h[i].Status), "active") {
			h[i].Status = "superseded"
		}
	}
	history[storageKey] = h
}

func applyDueFeatureSignalContractsSnapshot(snapshot *FeatureSignalContractStoreSnapshot, now time.Time) []FeatureSignalContractVersion {
	if snapshot == nil {
		return nil
	}
	activated := make([]FeatureSignalContractVersion, 0, 4)
	for storageKey, scheduled := range snapshot.Scheduled {
		if len(scheduled) == 0 {
			delete(snapshot.Scheduled, storageKey)
			continue
		}
		sort.Slice(scheduled, func(i, j int) bool {
			if scheduled[i].ActivationAt.Equal(scheduled[j].ActivationAt) {
				return scheduled[i].Version < scheduled[j].Version
			}
			return scheduled[i].ActivationAt.Before(scheduled[j].ActivationAt)
		})
		remaining := scheduled[:0]
		for _, item := range scheduled {
			if item.ActivationAt.After(now) {
				remaining = append(remaining, item)
				continue
			}
			snapshot.Active[storageKey] = buildFeatureSignalContractFromVersion(item)
			snapshot.Meta[storageKey] = now
			setFeatureSignalContractActiveVersion(snapshot.History, storageKey, item.Version, now)
			activated = append(activated, item)
		}
		if len(remaining) == 0 {
			delete(snapshot.Scheduled, storageKey)
			continue
		}
		cp := make([]FeatureSignalContractVersion, len(remaining))
		copy(cp, remaining)
		snapshot.Scheduled[storageKey] = cp
	}
	return activated
}

func (s *Service) bumpContractStoreVersionLocked() {
	s.storeVersion = nextStoreRevision(s.storeVersion)
}

func (s *Service) setFeatureSignalContractActiveVersionLocked(storageKey string, activeVersion int, activatedAt time.Time) {
	setFeatureSignalContractActiveVersion(s.featureSignalContractHistory, storageKey, activeVersion, activatedAt)
}

func (s *Service) activeFeatureSignalContractVersionLocked(storageKey string) FeatureSignalContractVersion {
	h := s.featureSignalContractHistory[storageKey]
	for i := len(h) - 1; i >= 0; i-- {
		if strings.EqualFold(strings.TrimSpace(h[i].Status), "active") || strings.TrimSpace(h[i].Status) == "" {
			return h[i]
		}
	}
	return FeatureSignalContractVersion{}
}

type FeatureSignalContractSchedulerResult struct {
	Activated   int       `json:"activated"`
	Pending     int       `json:"pending"`
	RanAt       time.Time `json:"ran_at"`
	ErrorStatus string    `json:"error_status,omitempty"`
	ReasonCodes []string  `json:"reason_codes,omitempty"`
}

func (s *Service) RunFeatureSignalContractScheduler(now time.Time) FeatureSignalContractSchedulerResult {
	return s.RunFeatureSignalContractSchedulerWithWorker(now, "manual")
}

func (s *Service) RunFeatureSignalContractSchedulerWithWorker(now time.Time, workerID string) FeatureSignalContractSchedulerResult {
	if now.IsZero() {
		now = s.cfg.Clock()
	}
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		workerID = "manual"
	}
	s.runtimeMu.Lock()
	pendingBefore := 0
	for _, items := range s.featureSignalContractScheduled {
		pendingBefore += len(items)
	}
	candidate := s.buildContractStoreSnapshotLocked(now)
	activatedItems := applyDueFeatureSignalContractsSnapshot(&candidate, now)
	if len(activatedItems) == 0 {
		s.runtimeMu.Unlock()
		return FeatureSignalContractSchedulerResult{
			Activated: 0,
			Pending:   pendingBefore,
			RanAt:     now,
		}
	}
	candidate.Revision = nextStoreRevision(s.storeVersion)
	if err := s.commitContractSnapshotLocked(candidate); err != nil {
		s.runtimeMu.Unlock()
		s.emitEvent(eventFeatureSignalContractSchedulerCommitFailed, "", "", "", map[string]interface{}{
			"activation_worker_id":   workerID,
			"activation_now":         now,
			"activated_attempted":    len(activatedItems),
			"pending_before_attempt": pendingBefore,
			"error":                  err.Error(),
		}, now)
		return FeatureSignalContractSchedulerResult{
			Activated:   0,
			Pending:     pendingBefore,
			RanAt:       now,
			ErrorStatus: "store_sync_failed",
			ReasonCodes: []string{
				"feature_signal_contract_store_sync_failed",
			},
		}
	}
	pending := 0
	for _, items := range s.featureSignalContractScheduled {
		pending += len(items)
	}
	s.runtimeMu.Unlock()
	for _, item := range activatedItems {
		s.emitEvent(eventFeatureSignalContractActivated, "", "", "", map[string]interface{}{
			"contract_key":         item.ContractKey,
			"storage_key":          item.StorageKey,
			"scope":                item.Scope,
			"risk_tier":            item.RiskTier,
			"phase":                item.Phase,
			"version":              item.Version,
			"previous_status":      "scheduled",
			"next_status":          "active",
			"schema_version":       item.SchemaVersion,
			"trusted_producer_ids": append([]string(nil), item.TrustedProducerIDs...),
			"max_freshness_ms":     item.MaxFreshnessMS,
			"max_drift_score":      item.MaxDriftScore,
			"status":               "active",
			"activation_at":        item.ActivationAt,
			"activation_now":       now,
			"activation_worker_id": workerID,
		}, now)
	}
	return FeatureSignalContractSchedulerResult{
		Activated: len(activatedItems),
		Pending:   pending,
		RanAt:     now,
	}
}

func (s *Service) CreateFeatureDefinition(req FeatureDefinitionCreateRequest) (FeatureDefinition, error) {
	featureID := normalizeFeatureID(req.FeatureID)
	name := strings.TrimSpace(req.Name)
	owner := strings.TrimSpace(req.Owner)
	scope := normalizeScope(req.Scope)
	if featureID == "" || name == "" || owner == "" {
		return FeatureDefinition{}, controlErr(422, "FEATURE_INPUT_INVALID", "feature_id/name/owner required", "feature_input_invalid")
	}
	if missing := missingScopeFields(scope); len(missing) > 0 {
		return FeatureDefinition{}, controlErr(422, "FEATURE_SCOPE_INVALID", "org/workspace/project scope required", missing...)
	}

	now := s.cfg.Clock()
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if _, exists := s.featureDefinitions[featureID]; exists {
		return FeatureDefinition{}, controlErr(409, "FEATURE_ALREADY_EXISTS", "feature definition already exists", "feature_already_exists")
	}

	def := FeatureDefinition{
		FeatureID:   featureID,
		Name:        name,
		Description: strings.TrimSpace(req.Description),
		Owner:       owner,
		Scope:       scope,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	s.featureDefinitions[featureID] = def
	s.emitEvent(eventFeatureDefinitionCreated, "", "", "", map[string]interface{}{
		"feature_id": featureID,
		"scope":      scope,
		"owner":      owner,
	}, now)
	return def, nil
}

func (s *Service) ListFeatureDefinitions(scope ScopeRef) ([]FeatureDefinition, error) {
	scope = normalizeScope(scope)
	if missing := missingScopeFields(scope); len(missing) > 0 {
		return nil, controlErr(422, "FEATURE_SCOPE_INVALID", "org/workspace/project scope required", missing...)
	}
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	out := make([]FeatureDefinition, 0, len(s.featureDefinitions))
	for _, def := range s.featureDefinitions {
		if sameScope(def.Scope, scope) {
			out = append(out, def)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FeatureID != out[j].FeatureID {
			return out[i].FeatureID < out[j].FeatureID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (s *Service) CreateFeatureVersion(featureID string, req FeatureVersionCreateRequest) (FeatureVersion, error) {
	featureID = normalizeFeatureID(featureID)
	version := normalizeVersion(req.Version)
	producerID := strings.TrimSpace(req.ProducerID)
	schemaVersion := strings.TrimSpace(req.SchemaVersion)
	evidenceRef := strings.TrimSpace(req.EvidenceRef)
	scope := normalizeScope(req.Scope)

	if featureID == "" || version == "" || producerID == "" || schemaVersion == "" || evidenceRef == "" {
		return FeatureVersion{}, controlErr(422, "FEATURE_VERSION_INPUT_INVALID", "feature/version/producer/schema/evidence required", "feature_version_input_invalid")
	}
	if missing := missingScopeFields(scope); len(missing) > 0 {
		return FeatureVersion{}, controlErr(422, "FEATURE_SCOPE_INVALID", "org/workspace/project scope required", missing...)
	}

	now := s.cfg.Clock()
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	def, ok := s.featureDefinitions[featureID]
	if !ok {
		return FeatureVersion{}, controlErr(404, "FEATURE_NOT_FOUND", "feature definition not found", "feature_not_found")
	}
	if !sameScope(scope, def.Scope) {
		return FeatureVersion{}, controlErr(409, "FEATURE_SCOPE_MISMATCH", "feature scope mismatch", "feature_scope_mismatch")
	}
	if _, ok := s.featureVersions[featureID]; !ok {
		s.featureVersions[featureID] = make(map[string]FeatureVersion)
	}
	if _, exists := s.featureVersions[featureID][version]; exists {
		return FeatureVersion{}, controlErr(409, "FEATURE_VERSION_EXISTS", "feature version already exists", "feature_version_exists")
	}

	upstream := make([]string, 0, len(req.UpstreamFeatureIDs))
	for _, u := range req.UpstreamFeatureIDs {
		u = normalizeFeatureID(u)
		if u != "" {
			upstream = append(upstream, u)
		}
	}
	sort.Strings(upstream)
	upstream = uniqueStrings(upstream)

	fv := FeatureVersion{
		FeatureID:          featureID,
		Version:            version,
		ProducerID:         producerID,
		SchemaVersion:      schemaVersion,
		EvidenceRef:        evidenceRef,
		UpstreamFeatureIDs: upstream,
		DerivationType:     strings.TrimSpace(req.DerivationType),
		CriticalPath:       req.CriticalPath,
		DriftScore:         req.DriftScore,
		Scope:              scope,
		CreatedAt:          now,
	}
	s.featureVersions[featureID][version] = fv
	if strings.TrimSpace(def.ActiveVersion) == "" {
		def.ActiveVersion = version
		def.UpdatedAt = now
		s.featureDefinitions[featureID] = def
	}
	s.emitEvent(eventFeatureVersionCreated, "", "", "", map[string]interface{}{
		"feature_id":    featureID,
		"version":       version,
		"producer_id":   producerID,
		"critical_path": req.CriticalPath,
		"scope":         scope,
	}, now)
	return fv, nil
}

func (s *Service) ListFeatureVersions(featureID string, scope ScopeRef) ([]FeatureVersion, error) {
	featureID = normalizeFeatureID(featureID)
	scope = normalizeScope(scope)
	if featureID == "" {
		return nil, controlErr(422, "FEATURE_ID_REQUIRED", "feature_id required", "feature_id_required")
	}
	if missing := missingScopeFields(scope); len(missing) > 0 {
		return nil, controlErr(422, "FEATURE_SCOPE_INVALID", "org/workspace/project scope required", missing...)
	}

	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	def, ok := s.featureDefinitions[featureID]
	if !ok {
		return nil, controlErr(404, "FEATURE_NOT_FOUND", "feature definition not found", "feature_not_found")
	}
	if !sameScope(def.Scope, scope) {
		return nil, controlErr(403, "FEATURE_SCOPE_FORBIDDEN", "scope not allowed for feature", "feature_scope_forbidden")
	}
	versions := s.featureVersions[featureID]
	out := make([]FeatureVersion, 0, len(versions))
	for _, v := range versions {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].Version < out[j].Version
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (s *Service) PublishFeatureVersion(featureID string, req FeatureVersionPublishRequest) (FeatureVersionPublishResponse, error) {
	featureID = normalizeFeatureID(featureID)
	targetVersion := normalizeVersion(req.TargetVersion)
	scope := normalizeScope(req.Scope)
	reason := strings.TrimSpace(req.Reason)
	if featureID == "" || targetVersion == "" || reason == "" {
		return FeatureVersionPublishResponse{}, controlErr(422, "FEATURE_PUBLISH_INPUT_INVALID", "feature_id/target_version/reason required", "feature_publish_input_invalid")
	}
	if missing := missingScopeFields(scope); len(missing) > 0 {
		return FeatureVersionPublishResponse{}, controlErr(422, "FEATURE_SCOPE_INVALID", "org/workspace/project scope required", missing...)
	}

	now := s.cfg.Clock()
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()

	def, ok := s.featureDefinitions[featureID]
	if !ok {
		return FeatureVersionPublishResponse{}, controlErr(404, "FEATURE_NOT_FOUND", "feature definition not found", "feature_not_found")
	}
	if !sameScope(scope, def.Scope) {
		return FeatureVersionPublishResponse{}, controlErr(403, "FEATURE_SCOPE_FORBIDDEN", "scope not allowed for feature", "feature_scope_forbidden")
	}
	versions := s.featureVersions[featureID]
	if _, ok := versions[targetVersion]; !ok {
		return FeatureVersionPublishResponse{}, controlErr(404, "FEATURE_VERSION_NOT_FOUND", "target feature version not found", "feature_version_not_found")
	}

	prev := def.ActiveVersion
	def.ActiveVersion = targetVersion
	def.UpdatedAt = now
	s.featureDefinitions[featureID] = def
	s.emitEvent(eventFeatureVersionPublished, "", "", "", map[string]interface{}{
		"feature_id":       featureID,
		"previous_version": prev,
		"target_version":   targetVersion,
		"scope":            scope,
		"reason":           reason,
	}, now)
	return FeatureVersionPublishResponse{
		FeatureID:       featureID,
		PreviousVersion: prev,
		TargetVersion:   targetVersion,
		Published:       true,
		UpdatedAt:       now,
	}, nil
}

func (s *Service) BuildFeatureSnapshot(req FeatureSnapshotBuildRequest) (FeatureSnapshot, error) {
	featureID := normalizeFeatureID(req.FeatureID)
	version := normalizeVersion(req.Version)
	producerID := strings.TrimSpace(req.ProducerID)
	evidenceRef := strings.TrimSpace(req.EvidenceRef)
	scope := normalizeScope(req.Scope)
	if featureID == "" || version == "" || producerID == "" || evidenceRef == "" {
		return FeatureSnapshot{}, controlErr(422, "FEATURE_SNAPSHOT_INPUT_INVALID", "feature/version/producer/evidence required", "feature_snapshot_input_invalid")
	}
	if missing := missingScopeFields(scope); len(missing) > 0 {
		return FeatureSnapshot{}, controlErr(422, "FEATURE_SCOPE_INVALID", "org/workspace/project scope required", missing...)
	}
	if req.TTLMS <= 0 {
		return FeatureSnapshot{}, controlErr(422, "FEATURE_SNAPSHOT_TTL_INVALID", "ttl_ms must be > 0", "feature_snapshot_ttl_invalid")
	}

	now := s.cfg.Clock()
	freshnessTS := req.FreshnessTS
	if freshnessTS.IsZero() {
		freshnessTS = now
	}

	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()

	versions, ok := s.featureVersions[featureID]
	if !ok {
		return FeatureSnapshot{}, controlErr(404, "FEATURE_NOT_FOUND", "feature definition/version not found", "feature_not_found")
	}
	ver, ok := versions[version]
	if !ok {
		return FeatureSnapshot{}, controlErr(404, "FEATURE_VERSION_NOT_FOUND", "feature version not found", "feature_version_not_found")
	}
	if !sameScope(scope, ver.Scope) {
		return FeatureSnapshot{}, controlErr(409, "FEATURE_SCOPE_MISMATCH", "feature scope mismatch", "feature_scope_mismatch")
	}
	if producerID != ver.ProducerID {
		return FeatureSnapshot{}, controlErr(409, "FEATURE_PRODUCER_MISMATCH", "producer_id mismatch with registered feature version", "feature_producer_mismatch")
	}

	snapshotInput := struct {
		FeatureID   string            `json:"feature_id"`
		Version     string            `json:"version"`
		ProducerID  string            `json:"producer_id"`
		EvidenceRef string            `json:"evidence_ref"`
		FreshnessTS int64             `json:"freshness_ts"`
		TTLMS       int               `json:"ttl_ms"`
		Scope       ScopeRef          `json:"scope"`
		Metadata    map[string]string `json:"metadata,omitempty"`
	}{
		FeatureID:   featureID,
		Version:     version,
		ProducerID:  producerID,
		EvidenceRef: evidenceRef,
		FreshnessTS: freshnessTS.UnixMilli(),
		TTLMS:       req.TTLMS,
		Scope:       scope,
		Metadata:    req.Metadata,
	}
	snapshotID, _ := s.makeBestEffortID("fs_", snapshotInput)

	signAlgo := strings.TrimSpace(req.SignatureAlgo)
	if signAlgo == "" {
		signAlgo = "sha256"
	}
	signatureInput := struct {
		SnapshotID  string   `json:"snapshot_id"`
		FeatureID   string   `json:"feature_id"`
		Version     string   `json:"version"`
		ProducerID  string   `json:"producer_id"`
		EvidenceRef string   `json:"evidence_ref"`
		Scope       ScopeRef `json:"scope"`
	}{
		SnapshotID:  snapshotID,
		FeatureID:   featureID,
		Version:     version,
		ProducerID:  producerID,
		EvidenceRef: evidenceRef,
		Scope:       scope,
	}
	signature, ok := safeHash(signatureInput)
	if !ok {
		signature = s.nextFallbackID("sig_")
	}

	snap := FeatureSnapshot{
		SnapshotID:     snapshotID,
		FeatureID:      featureID,
		Version:        version,
		ProducerID:     producerID,
		EvidenceRef:    evidenceRef,
		FreshnessTS:    freshnessTS,
		TTLMS:          req.TTLMS,
		Metadata:       req.Metadata,
		Scope:          scope,
		FeatureVersion: version,
		Signature:      signature,
		SignatureAlgo:  signAlgo,
		CreatedAt:      now,
	}
	s.featureSnapshots[snapshotID] = snap
	s.emitEvent(eventFeatureSnapshotBuilt, "", "", "", map[string]interface{}{
		"snapshot_id": snapshotID,
		"feature_id":  featureID,
		"version":     version,
		"scope":       scope,
		"signature":   signature,
	}, now)
	return snap, nil
}

func (s *Service) GetFeatureSnapshot(snapshotID string) (FeatureSnapshot, bool) {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	snap, ok := s.featureSnapshots[strings.TrimSpace(snapshotID)]
	return snap, ok
}

func (s *Service) ValidateFeatureSnapshotFreshness(req FeatureSnapshotFreshnessValidateRequest) (FeatureSnapshotFreshnessValidateResponse, error) {
	snapshotID := strings.TrimSpace(req.SnapshotID)
	if snapshotID == "" {
		return FeatureSnapshotFreshnessValidateResponse{}, controlErr(422, "FEATURE_SNAPSHOT_ID_REQUIRED", "snapshot_id required", "feature_snapshot_id_required")
	}
	scope := normalizeScope(req.Scope)
	if missing := missingScopeFields(scope); len(missing) > 0 {
		return FeatureSnapshotFreshnessValidateResponse{}, controlErr(422, "FEATURE_SCOPE_INVALID", "org/workspace/project scope required", missing...)
	}
	at := req.At
	if at.IsZero() {
		at = s.cfg.Clock()
	}

	s.runtimeMu.RLock()
	snap, ok := s.featureSnapshots[snapshotID]
	s.runtimeMu.RUnlock()
	if !ok {
		return FeatureSnapshotFreshnessValidateResponse{}, controlErr(404, "FEATURE_SNAPSHOT_NOT_FOUND", "feature snapshot not found", "feature_snapshot_not_found")
	}
	if !sameScope(scope, snap.Scope) {
		return FeatureSnapshotFreshnessValidateResponse{}, controlErr(403, "FEATURE_SCOPE_FORBIDDEN", "scope not allowed for snapshot", "feature_scope_forbidden")
	}

	ageMS := at.Sub(snap.FreshnessTS).Milliseconds()
	if ageMS < 0 {
		ageMS = 0
	}
	fresh := ageMS <= int64(snap.TTLMS)
	resp := FeatureSnapshotFreshnessValidateResponse{
		SnapshotID: snapshotID,
		Fresh:      fresh,
		Stale:      !fresh,
		AgeMS:      ageMS,
		TTLMS:      snap.TTLMS,
	}
	if fresh {
		resp.ReasonCode = "feature_snapshot_fresh"
		resp.RequiredAction = "allow"
		return resp, nil
	}

	switch normalizeRiskTier(req.RiskTier) {
	case RiskCritical:
		resp.ReasonCode = "feature_snapshot_stale_critical"
		resp.RequiredAction = "fail_closed_feature_rollback_candidate"
	case RiskHigh:
		resp.ReasonCode = "feature_snapshot_stale_high"
		resp.RequiredAction = "review_required_hold_feature_version"
	default:
		resp.ReasonCode = "feature_snapshot_stale"
		resp.RequiredAction = "review_required_feature_watch"
	}
	return resp, nil
}

func (s *Service) GetFeatureSnapshotEvidence(snapshotID string) (FeatureSnapshotEvidence, bool) {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	snap, ok := s.featureSnapshots[strings.TrimSpace(snapshotID)]
	if !ok {
		return FeatureSnapshotEvidence{}, false
	}
	return FeatureSnapshotEvidence{
		SnapshotID:     snap.SnapshotID,
		FeatureID:      snap.FeatureID,
		FeatureVersion: snap.FeatureVersion,
		ProducerID:     snap.ProducerID,
		EvidenceRef:    snap.EvidenceRef,
		Signature:      snap.Signature,
		Scope:          snap.Scope,
		CreatedAt:      snap.CreatedAt,
	}, true
}

func (s *Service) GetFeatureDriftReport(featureID string, scope ScopeRef) (FeatureDriftReport, error) {
	featureID = normalizeFeatureID(featureID)
	scope = normalizeScope(scope)
	if featureID == "" {
		return FeatureDriftReport{}, controlErr(422, "FEATURE_ID_REQUIRED", "feature_id required", "feature_id_required")
	}
	if missing := missingScopeFields(scope); len(missing) > 0 {
		return FeatureDriftReport{}, controlErr(422, "FEATURE_SCOPE_INVALID", "org/workspace/project scope required", missing...)
	}

	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()

	def, ok := s.featureDefinitions[featureID]
	if !ok {
		return FeatureDriftReport{}, controlErr(404, "FEATURE_NOT_FOUND", "feature definition not found", "feature_not_found")
	}
	if !sameScope(scope, def.Scope) {
		return FeatureDriftReport{}, controlErr(403, "FEATURE_SCOPE_FORBIDDEN", "scope not allowed for feature", "feature_scope_forbidden")
	}

	versionsMap := s.featureVersions[featureID]
	items := make([]FeatureVersionSummary, 0, len(versionsMap))
	for _, v := range versionsMap {
		items = append(items, FeatureVersionSummary{
			Version:       v.Version,
			ProducerID:    v.ProducerID,
			DriftScore:    v.DriftScore,
			CriticalPath:  v.CriticalPath,
			UpstreamCount: len(v.UpstreamFeatureIDs),
			CreatedAt:     v.CreatedAt,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })

	latestDrift := 0.0
	trend := "stable"
	if len(items) > 0 {
		latestDrift = items[len(items)-1].DriftScore
		if len(items) > 1 {
			first := items[0].DriftScore
			last := items[len(items)-1].DriftScore
			switch {
			case last > first:
				trend = "increasing"
			case last < first:
				trend = "decreasing"
			default:
				trend = "stable"
			}
		}
	}

	return FeatureDriftReport{
		FeatureID:        featureID,
		Scope:            scope,
		LatestDriftScore: latestDrift,
		Trend:            trend,
		CurrentVersion:   def.ActiveVersion,
		Versions:         items,
		GeneratedAt:      s.cfg.Clock(),
	}, nil
}

func (s *Service) RollbackFeature(featureID string, req FeatureRollbackRequest) (FeatureRollbackRecord, error) {
	featureID = normalizeFeatureID(featureID)
	targetVersion := normalizeVersion(req.TargetVersion)
	scope := normalizeScope(req.Scope)
	reason := strings.TrimSpace(req.Reason)
	if featureID == "" || targetVersion == "" || reason == "" {
		return FeatureRollbackRecord{}, controlErr(422, "FEATURE_ROLLBACK_INPUT_INVALID", "feature_id/target_version/reason required", "feature_rollback_input_invalid")
	}
	if missing := missingScopeFields(scope); len(missing) > 0 {
		return FeatureRollbackRecord{}, controlErr(422, "FEATURE_SCOPE_INVALID", "org/workspace/project scope required", missing...)
	}

	now := s.cfg.Clock()
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()

	def, ok := s.featureDefinitions[featureID]
	if !ok {
		return FeatureRollbackRecord{}, controlErr(404, "FEATURE_NOT_FOUND", "feature definition not found", "feature_not_found")
	}
	if !sameScope(scope, def.Scope) {
		return FeatureRollbackRecord{}, controlErr(403, "FEATURE_SCOPE_FORBIDDEN", "scope not allowed for feature", "feature_scope_forbidden")
	}
	versions := s.featureVersions[featureID]
	if _, ok := versions[targetVersion]; !ok {
		return FeatureRollbackRecord{}, controlErr(404, "FEATURE_VERSION_NOT_FOUND", "target feature version not found", "feature_version_not_found")
	}

	record := FeatureRollbackRecord{
		RollbackID:      s.nextFallbackID("rbk_"),
		FeatureID:       featureID,
		PreviousVersion: def.ActiveVersion,
		TargetVersion:   targetVersion,
		Reason:          reason,
		Scope:           scope,
		CreatedAt:       now,
	}
	def.ActiveVersion = targetVersion
	def.UpdatedAt = now
	s.featureDefinitions[featureID] = def
	s.featureRollbacks[featureID] = append(s.featureRollbacks[featureID], record)
	s.emitEvent(eventFeatureRollbackCreated, "", "", "", map[string]interface{}{
		"feature_id":       featureID,
		"previous_version": record.PreviousVersion,
		"target_version":   targetVersion,
		"rollback_id":      record.RollbackID,
		"scope":            scope,
	}, now)
	return record, nil
}

func (s *Service) GetFeatureRollbackHistory(featureID string, scope ScopeRef, limit int) ([]FeatureRollbackRecord, error) {
	featureID = normalizeFeatureID(featureID)
	scope = normalizeScope(scope)
	if featureID == "" {
		return nil, controlErr(422, "FEATURE_ID_REQUIRED", "feature_id required", "feature_id_required")
	}
	if missing := missingScopeFields(scope); len(missing) > 0 {
		return nil, controlErr(422, "FEATURE_SCOPE_INVALID", "org/workspace/project scope required", missing...)
	}
	if limit <= 0 {
		limit = 20
	}
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	def, ok := s.featureDefinitions[featureID]
	if !ok {
		return nil, controlErr(404, "FEATURE_NOT_FOUND", "feature definition not found", "feature_not_found")
	}
	if !sameScope(def.Scope, scope) {
		return nil, controlErr(403, "FEATURE_SCOPE_FORBIDDEN", "scope not allowed for feature", "feature_scope_forbidden")
	}
	src := s.featureRollbacks[featureID]
	if len(src) == 0 {
		return []FeatureRollbackRecord{}, nil
	}
	start := len(src) - limit
	if start < 0 {
		start = 0
	}
	out := make([]FeatureRollbackRecord, len(src[start:]))
	copy(out, src[start:])
	return out, nil
}

func (s *Service) GetFeatureDependencyGraph(featureID string, scope ScopeRef) (FeatureDependencyGraph, error) {
	featureID = normalizeFeatureID(featureID)
	scope = normalizeScope(scope)
	if featureID == "" {
		return FeatureDependencyGraph{}, controlErr(422, "FEATURE_ID_REQUIRED", "feature_id required", "feature_id_required")
	}
	if missing := missingScopeFields(scope); len(missing) > 0 {
		return FeatureDependencyGraph{}, controlErr(422, "FEATURE_SCOPE_INVALID", "org/workspace/project scope required", missing...)
	}

	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	def, ok := s.featureDefinitions[featureID]
	if !ok {
		return FeatureDependencyGraph{}, controlErr(404, "FEATURE_NOT_FOUND", "feature definition not found", "feature_not_found")
	}
	if !sameScope(scope, def.Scope) {
		return FeatureDependencyGraph{}, controlErr(403, "FEATURE_SCOPE_FORBIDDEN", "scope not allowed for feature", "feature_scope_forbidden")
	}

	nodesSet := map[string]struct{}{featureID: {}}
	edges := make([]FeatureDependencyEdge, 0)
	edgeSeen := make(map[string]struct{})
	for _, v := range s.featureVersions[featureID] {
		from := featureID + "@" + v.Version
		nodesSet[from] = struct{}{}
		for _, up := range v.UpstreamFeatureIDs {
			nodesSet[up] = struct{}{}
			key := from + "->" + up
			if _, ok := edgeSeen[key]; ok {
				continue
			}
			edgeSeen[key] = struct{}{}
			edges = append(edges, FeatureDependencyEdge{From: from, To: up, DerivationType: v.DerivationType})
		}
	}

	nodes := make([]string, 0, len(nodesSet))
	for n := range nodesSet {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})

	return FeatureDependencyGraph{
		FeatureID:   featureID,
		Scope:       scope,
		Current:     def.ActiveVersion,
		Nodes:       nodes,
		Edges:       edges,
		GeneratedAt: s.cfg.Clock(),
	}, nil
}

func defaultApprovalOrgHealth(scope ScopeRef, now time.Time) ApprovalOrgHealth {
	return ApprovalOrgHealth{
		Scope:                   scope,
		ActiveApproverRatio:     1.0,
		DelegateFreshness:       1.0,
		OverrideDependenceRate:  0.0,
		StaleApproverGroupRatio: 0.0,
		RouteToNoActionCases:    0,
		Status:                  "healthy",
		UpdatedAt:               now,
	}
}

func approvalOrgHealthStatus(h ApprovalOrgHealth) string {
	if h.ActiveApproverRatio < 0.7 || h.DelegateFreshness < 0.6 || h.OverrideDependenceRate > 0.25 || h.StaleApproverGroupRatio > 0.25 || h.RouteToNoActionCases > 50 {
		return "critical"
	}
	if h.ActiveApproverRatio < 0.9 || h.DelegateFreshness < 0.85 || h.OverrideDependenceRate > 0.1 || h.StaleApproverGroupRatio > 0.1 || h.RouteToNoActionCases > 10 {
		return "degraded"
	}
	return "healthy"
}

func approvalRecommendations(h ApprovalOrgHealth) []string {
	out := make([]string, 0, 5)
	if h.ActiveApproverRatio < 0.9 {
		out = append(out, "rebalance_approvers")
	}
	if h.DelegateFreshness < 0.85 {
		out = append(out, "refresh_delegates")
	}
	if h.OverrideDependenceRate > 0.1 {
		out = append(out, "reduce_override_dependency")
	}
	if h.StaleApproverGroupRatio > 0.1 {
		out = append(out, "cleanup_stale_approver_groups")
	}
	if h.RouteToNoActionCases > 10 {
		out = append(out, "replay_approval_routes")
	}
	if len(out) == 0 {
		out = append(out, "no_action_required")
	}
	return out
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}

func (s *Service) GetApprovalOrgHealth(scope ScopeRef) (ApprovalOrgHealth, error) {
	scope = normalizeScope(scope)
	if missing := missingScopeFields(scope); len(missing) > 0 {
		return ApprovalOrgHealth{}, controlErr(422, "APPROVAL_SCOPE_INVALID", "org/workspace/project scope required", missing...)
	}
	now := s.cfg.Clock()
	key := scopeKey(scope)

	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if h, ok := s.approvalOrgHealth[key]; ok {
		return h, nil
	}
	h := defaultApprovalOrgHealth(scope, now)
	s.approvalOrgHealth[key] = h
	return h, nil
}

func (s *Service) RecomputeApprovalOrgHealth(req ApprovalOrgHealthRecomputeRequest) (ApprovalOrgHealthRecomputeResponse, error) {
	scope := normalizeScope(req.Scope)
	if missing := missingScopeFields(scope); len(missing) > 0 {
		return ApprovalOrgHealthRecomputeResponse{}, controlErr(422, "APPROVAL_SCOPE_INVALID", "org/workspace/project scope required", missing...)
	}
	if strings.TrimSpace(req.Reason) == "" {
		return ApprovalOrgHealthRecomputeResponse{}, controlErr(422, "APPROVAL_RECOMPUTE_REASON_REQUIRED", "reason required", "approval_recompute_reason_required")
	}
	if req.RouteToNoActionCases < 0 {
		return ApprovalOrgHealthRecomputeResponse{}, controlErr(422, "APPROVAL_RECOMPUTE_INPUT_INVALID", "route_to_no_action_cases must be >= 0", "approval_recompute_input_invalid")
	}

	now := s.cfg.Clock()
	key := scopeKey(scope)

	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	state, ok := s.approvalOrgHealth[key]
	if !ok {
		state = defaultApprovalOrgHealth(scope, now)
	}

	state.ActiveApproverRatio = clamp01(req.ActiveApproverRatio)
	state.DelegateFreshness = clamp01(req.DelegateFreshness)
	state.OverrideDependenceRate = clamp01(req.OverrideDependenceRate)
	state.StaleApproverGroupRatio = clamp01(req.StaleApproverGroupRatio)
	state.RouteToNoActionCases = req.RouteToNoActionCases
	state.Status = approvalOrgHealthStatus(state)
	state.UpdatedAt = now
	s.approvalOrgHealth[key] = state

	report := ApprovalOrgHealthReport{
		ReportID:        s.nextFallbackID("aphr_"),
		Scope:           scope,
		Status:          state.Status,
		Recommendations: approvalRecommendations(state),
		GeneratedAt:     now,
	}
	s.approvalOrgReports[key] = append(s.approvalOrgReports[key], report)
	s.emitEvent(eventApprovalOrgHealthRecomputed, "", "", "", map[string]interface{}{
		"scope":     scope,
		"status":    state.Status,
		"reason":    strings.TrimSpace(req.Reason),
		"actor":     strings.TrimSpace(req.Actor),
		"report_id": report.ReportID,
		"health":    state,
	}, now)

	return ApprovalOrgHealthRecomputeResponse{
		Accepted:  true,
		ReportID:  report.ReportID,
		Status:    state.Status,
		UpdatedAt: now,
	}, nil
}

func (s *Service) RemediateApprovalOrgHealth(req ApprovalOrgHealthRemediationRequest) (ApprovalOrgHealthRemediationResponse, error) {
	scope := normalizeScope(req.Scope)
	if missing := missingScopeFields(scope); len(missing) > 0 {
		return ApprovalOrgHealthRemediationResponse{}, controlErr(422, "APPROVAL_SCOPE_INVALID", "org/workspace/project scope required", missing...)
	}
	if len(req.Actions) == 0 {
		return ApprovalOrgHealthRemediationResponse{}, controlErr(422, "APPROVAL_REMEDIATION_ACTIONS_REQUIRED", "actions required", "approval_remediation_actions_required")
	}
	if strings.TrimSpace(req.Reason) == "" {
		return ApprovalOrgHealthRemediationResponse{}, controlErr(422, "APPROVAL_REMEDIATION_REASON_REQUIRED", "reason required", "approval_remediation_reason_required")
	}

	now := s.cfg.Clock()
	key := scopeKey(scope)

	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	state, ok := s.approvalOrgHealth[key]
	if !ok {
		state = defaultApprovalOrgHealth(scope, now)
	}

	applied := make([]string, 0, len(req.Actions))
	for _, action := range req.Actions {
		a := strings.ToLower(strings.TrimSpace(action))
		if a == "" {
			continue
		}
		switch a {
		case "refresh_delegates":
			applied = append(applied, a)
			state.DelegateFreshness = 1.0
		case "rebalance_approvers":
			applied = append(applied, a)
			state.ActiveApproverRatio = 1.0
		case "reduce_override", "reduce_override_dependency":
			applied = append(applied, a)
			state.OverrideDependenceRate = state.OverrideDependenceRate * 0.5
		case "cleanup_stale_groups", "cleanup_stale_approver_groups":
			applied = append(applied, a)
			state.StaleApproverGroupRatio = 0.0
		case "route_replay", "replay_approval_routes":
			applied = append(applied, a)
			state.RouteToNoActionCases = 0
		}
	}
	if len(applied) == 0 {
		return ApprovalOrgHealthRemediationResponse{}, controlErr(422, "APPROVAL_REMEDIATION_ACTIONS_INVALID", "no valid remediation actions", "approval_remediation_actions_invalid")
	}
	state.Status = approvalOrgHealthStatus(state)
	state.UpdatedAt = now
	s.approvalOrgHealth[key] = state

	report := ApprovalOrgHealthReport{
		ReportID:        s.nextFallbackID("aphr_"),
		Scope:           scope,
		Status:          state.Status,
		Recommendations: approvalRecommendations(state),
		GeneratedAt:     now,
	}
	s.approvalOrgReports[key] = append(s.approvalOrgReports[key], report)
	s.emitEvent(eventApprovalOrgHealthRemediated, "", "", "", map[string]interface{}{
		"scope":     scope,
		"status":    state.Status,
		"applied":   applied,
		"reason":    req.Reason,
		"report_id": report.ReportID,
	}, now)

	return ApprovalOrgHealthRemediationResponse{
		Accepted:       true,
		ReportID:       report.ReportID,
		Status:         state.Status,
		AppliedActions: applied,
		UpdatedAt:      now,
	}, nil
}

func (s *Service) GetApprovalOrgHealthReports(scope ScopeRef, limit int) ([]ApprovalOrgHealthReport, error) {
	scope = normalizeScope(scope)
	if missing := missingScopeFields(scope); len(missing) > 0 {
		return nil, controlErr(422, "APPROVAL_SCOPE_INVALID", "org/workspace/project scope required", missing...)
	}
	key := scopeKey(scope)
	if limit <= 0 {
		limit = 20
	}

	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	src := s.approvalOrgReports[key]
	if len(src) == 0 {
		return []ApprovalOrgHealthReport{}, nil
	}
	start := len(src) - limit
	if start < 0 {
		start = 0
	}
	out := make([]ApprovalOrgHealthReport, len(src[start:]))
	copy(out, src[start:])
	return out, nil
}

func copyEnforcementMatrix(in map[string]EnforcementThreshold) map[string]EnforcementThreshold {
	out := make(map[string]EnforcementThreshold, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (s *Service) GetEnforcementMatrix() EnforcementMatrixView {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return EnforcementMatrixView{
		Version:   s.matrixVersion,
		UpdatedAt: s.matrixUpdatedAt,
		Matrix:    copyEnforcementMatrix(s.cfg.MetricMatrix),
	}
}

func validateThreshold(t EnforcementThreshold) []string {
	errs := make([]string, 0, 2)
	switch strings.ToLower(strings.TrimSpace(t.Direction)) {
	case "gt":
		if !(t.ObserveOnly <= t.Alert && t.Alert <= t.BlockRelease && t.BlockRelease <= t.BlockRuntime) {
			errs = append(errs, "gt_threshold_order_invalid")
		}
	case "lt":
		if !(t.ObserveOnly >= t.Alert && t.Alert >= t.BlockRelease && t.BlockRelease >= t.BlockRuntime) {
			errs = append(errs, "lt_threshold_order_invalid")
		}
	default:
		errs = append(errs, "direction_invalid")
	}
	return errs
}

func (s *Service) ValidateEnforcementMatrix(req EnforcementMatrixValidateRequest) EnforcementMatrixValidateResponse {
	errs := make([]string, 0, 4)
	if len(req.Matrix) == 0 {
		errs = append(errs, "matrix_empty")
	}
	for metric, threshold := range req.Matrix {
		name := strings.TrimSpace(metric)
		if name == "" {
			errs = append(errs, "metric_name_empty")
			continue
		}
		for _, e := range validateThreshold(threshold) {
			errs = append(errs, fmt.Sprintf("%s:%s", name, e))
		}
	}
	sort.Strings(errs)
	return EnforcementMatrixValidateResponse{
		Valid:  len(errs) == 0,
		Errors: errs,
	}
}

func (s *Service) PublishEnforcementMatrix(req EnforcementMatrixPublishRequest) (EnforcementMatrixPublishResponse, error) {
	check := s.ValidateEnforcementMatrix(EnforcementMatrixValidateRequest{Matrix: req.Matrix})
	if !check.Valid {
		return EnforcementMatrixPublishResponse{}, controlErr(422, "ENFORCEMENT_MATRIX_INVALID", "enforcement matrix validation failed", check.Errors...)
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		return EnforcementMatrixPublishResponse{}, controlErr(422, "ENFORCEMENT_MATRIX_REASON_REQUIRED", "publish reason required", "enforcement_matrix_reason_required")
	}

	now := s.cfg.Clock()
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.cfg.MetricMatrix = copyEnforcementMatrix(req.Matrix)
	s.matrixVersion++
	s.matrixUpdatedAt = now
	s.emitEvent(eventMetricsEnforcementMatrixPublish, "", "", "", map[string]interface{}{
		"version": s.matrixVersion,
		"reason":  reason,
		"size":    len(req.Matrix),
	}, now)
	return EnforcementMatrixPublishResponse{
		Version:   s.matrixVersion,
		UpdatedAt: now,
		Reason:    reason,
	}, nil
}
