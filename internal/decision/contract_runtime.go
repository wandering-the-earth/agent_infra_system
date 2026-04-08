package decision

import (
	"context"
	"fmt"
	"time"
)

func nextStoreRevision(current uint64) uint64 {
	if current == 0 {
		return 1
	}
	return current + 1
}

func (s *Service) buildContractStoreSnapshotLocked(now time.Time) FeatureSignalContractStoreSnapshot {
	return cloneFeatureSignalContractStoreSnapshot(FeatureSignalContractStoreSnapshot{
		Revision:    s.storeVersion,
		GeneratedAt: now,
		Active:      s.featureSignalContractsScoped,
		Meta:        s.featureSignalContractMeta,
		History:     s.featureSignalContractHistory,
		Rollbacks:   s.featureSignalContractRollbacks,
		Scheduled:   s.featureSignalContractScheduled,
	})
}

func (s *Service) applyContractStoreSnapshotLocked(snapshot FeatureSignalContractStoreSnapshot) {
	s.featureSignalContractsScoped = make(map[string]FeatureSignalContract, len(snapshot.Active))
	for k, v := range snapshot.Active {
		s.featureSignalContractsScoped[k] = FeatureSignalContract{
			RequiredFields:     append([]string(nil), v.RequiredFields...),
			SchemaVersion:      v.SchemaVersion,
			TrustedProducerIDs: append([]string(nil), v.TrustedProducerIDs...),
			MaxFreshnessMS:     v.MaxFreshnessMS,
			MaxDriftScore:      v.MaxDriftScore,
		}
	}
	s.featureSignalContractMeta = make(map[string]time.Time, len(snapshot.Meta))
	for k, v := range snapshot.Meta {
		s.featureSignalContractMeta[k] = v
	}
	s.featureSignalContractHistory = make(map[string][]FeatureSignalContractVersion, len(snapshot.History))
	for k, v := range snapshot.History {
		cp := make([]FeatureSignalContractVersion, len(v))
		for i := range v {
			cp[i] = cloneFeatureSignalContractVersion(v[i])
		}
		s.featureSignalContractHistory[k] = cp
	}
	s.featureSignalContractRollbacks = make(map[string][]FeatureSignalContractRollbackRecord, len(snapshot.Rollbacks))
	for k, v := range snapshot.Rollbacks {
		cp := make([]FeatureSignalContractRollbackRecord, len(v))
		for i := range v {
			cp[i] = cloneFeatureSignalContractRollbackRecord(v[i])
		}
		s.featureSignalContractRollbacks[k] = cp
	}
	s.featureSignalContractScheduled = make(map[string][]FeatureSignalContractVersion, len(snapshot.Scheduled))
	for k, v := range snapshot.Scheduled {
		cp := make([]FeatureSignalContractVersion, len(v))
		for i := range v {
			cp[i] = cloneFeatureSignalContractVersion(v[i])
		}
		s.featureSignalContractScheduled[k] = cp
	}
	if snapshot.Revision > 0 {
		s.storeVersion = snapshot.Revision
	}
}

func (s *Service) publishContractSnapshotLocked(now time.Time) error {
	snap := s.buildContractStoreSnapshotLocked(now)
	if snap.Revision == 0 {
		snap.Revision = nextStoreRevision(s.storeVersion)
	}
	return s.commitContractSnapshotLocked(snap)
}

func (s *Service) commitContractSnapshotLocked(snapshot FeatureSignalContractStoreSnapshot) error {
	if snapshot.Revision == 0 {
		snapshot.Revision = nextStoreRevision(s.storeVersion)
	}
	if s.contractStore == nil {
		s.applyContractStoreSnapshotLocked(snapshot)
		return nil
	}
	committed, err := s.contractStore.PutSnapshot(context.Background(), snapshot)
	if err != nil {
		return err
	}
	if committed.Revision == 0 {
		committed.Revision = snapshot.Revision
	}
	s.applyContractStoreSnapshotLocked(committed)
	return nil
}

func (s *Service) syncContractCacheFromStoreLocked(ctx context.Context) error {
	if s.contractStore == nil {
		return nil
	}
	snap, err := s.contractStore.GetSnapshot(ctx)
	if err != nil {
		return err
	}
	if snap.Revision == 0 {
		return nil
	}
	s.applyContractStoreSnapshotLocked(snap)
	return nil
}

func (s *Service) StartFeatureSignalContractCacheWatcher(ctx context.Context) error {
	if s.contractStore == nil {
		return fmt.Errorf("feature signal contract store not configured")
	}
	s.runtimeMu.Lock()
	if s.contractCacheWatcherCancel != nil {
		s.runtimeMu.Unlock()
		return fmt.Errorf("feature signal contract cache watcher already started")
	}
	watchCtx, cancel := context.WithCancel(ctx)
	s.contractCacheWatcherCancel = cancel
	s.runtimeMu.Unlock()

	ch, err := s.contractStore.Watch(watchCtx)
	if err != nil {
		s.runtimeMu.Lock()
		s.contractCacheWatcherCancel = nil
		s.runtimeMu.Unlock()
		cancel()
		return err
	}

	go func() {
		defer func() {
			s.runtimeMu.Lock()
			s.contractCacheWatcherCancel = nil
			s.runtimeMu.Unlock()
		}()
		for {
			select {
			case <-watchCtx.Done():
				return
			case snap, ok := <-ch:
				if !ok {
					return
				}
				s.runtimeMu.Lock()
				if snap.Revision > s.storeVersion {
					s.applyContractStoreSnapshotLocked(snap)
				}
				s.runtimeMu.Unlock()
			}
		}
	}()
	return nil
}

func (s *Service) StopFeatureSignalContractCacheWatcher() {
	s.runtimeMu.Lock()
	cancel := s.contractCacheWatcherCancel
	s.contractCacheWatcherCancel = nil
	s.runtimeMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Service) StartFeatureSignalContractActivationWorker(ctx context.Context, interval time.Duration, workerID string) error {
	if interval <= 0 {
		interval = 1 * time.Second
	}
	if workerID == "" {
		workerID = "contract-activation-worker"
	}
	s.runtimeMu.Lock()
	if s.contractActivationWorkerCancel != nil {
		s.runtimeMu.Unlock()
		return fmt.Errorf("feature signal contract activation worker already started")
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.contractActivationWorkerCancel = cancel
	s.runtimeMu.Unlock()

	go func() {
		defer func() {
			s.runtimeMu.Lock()
			s.contractActivationWorkerCancel = nil
			s.runtimeMu.Unlock()
		}()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				s.RunFeatureSignalContractSchedulerWithWorker(s.cfg.Clock(), workerID)
			}
		}
	}()
	return nil
}

func (s *Service) StopFeatureSignalContractActivationWorker() {
	s.runtimeMu.Lock()
	cancel := s.contractActivationWorkerCancel
	s.contractActivationWorkerCancel = nil
	s.runtimeMu.Unlock()
	if cancel != nil {
		cancel()
	}
}
