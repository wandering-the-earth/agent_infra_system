package decision

import (
	"context"
	"sync"
	"time"
)

// FeatureSignalContractStoreSnapshot is the authoritative replicated state for
// contract control-plane data. Local kernel cache should only be updated from this snapshot.
type FeatureSignalContractStoreSnapshot struct {
	Revision    uint64                                           `json:"revision"`
	GeneratedAt time.Time                                        `json:"generated_at"`
	Active      map[string]FeatureSignalContract                 `json:"active"`
	Meta        map[string]time.Time                             `json:"meta"`
	History     map[string][]FeatureSignalContractVersion        `json:"history"`
	Rollbacks   map[string][]FeatureSignalContractRollbackRecord `json:"rollbacks"`
	Scheduled   map[string][]FeatureSignalContractVersion        `json:"scheduled"`
}

// FeatureSignalContractStore is the authoritative store + watch interface.
// Read/runtime paths must stay side-effect free and consume cache state only.
type FeatureSignalContractStore interface {
	GetSnapshot(ctx context.Context) (FeatureSignalContractStoreSnapshot, error)
	PutSnapshot(ctx context.Context, snapshot FeatureSignalContractStoreSnapshot) (FeatureSignalContractStoreSnapshot, error)
	Watch(ctx context.Context) (<-chan FeatureSignalContractStoreSnapshot, error)
}

type InMemoryFeatureSignalContractStore struct {
	mu          sync.RWMutex
	snapshot    FeatureSignalContractStoreSnapshot
	watchers    map[int]chan FeatureSignalContractStoreSnapshot
	nextWatchID int
	watchBuffer int
}

func NewInMemoryFeatureSignalContractStore(watchBuffer int, seed FeatureSignalContractStoreSnapshot) *InMemoryFeatureSignalContractStore {
	if watchBuffer <= 0 {
		watchBuffer = 16
	}
	s := &InMemoryFeatureSignalContractStore{
		watchers:    make(map[int]chan FeatureSignalContractStoreSnapshot),
		watchBuffer: watchBuffer,
	}
	if seed.Revision > 0 {
		s.snapshot = cloneFeatureSignalContractStoreSnapshot(seed)
	}
	return s
}

func (s *InMemoryFeatureSignalContractStore) GetSnapshot(ctx context.Context) (FeatureSignalContractStoreSnapshot, error) {
	select {
	case <-ctx.Done():
		return FeatureSignalContractStoreSnapshot{}, ctx.Err()
	default:
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneFeatureSignalContractStoreSnapshot(s.snapshot), nil
}

func (s *InMemoryFeatureSignalContractStore) PutSnapshot(ctx context.Context, snapshot FeatureSignalContractStoreSnapshot) (FeatureSignalContractStoreSnapshot, error) {
	select {
	case <-ctx.Done():
		return FeatureSignalContractStoreSnapshot{}, ctx.Err()
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneFeatureSignalContractStoreSnapshot(snapshot)
	if next.Revision <= s.snapshot.Revision {
		next.Revision = s.snapshot.Revision + 1
	}
	if next.GeneratedAt.IsZero() {
		next.GeneratedAt = time.Now().UTC()
	}
	s.snapshot = next

	emit := cloneFeatureSignalContractStoreSnapshot(s.snapshot)
	for _, ch := range s.watchers {
		select {
		case ch <- emit:
		default:
			// Keep latest snapshot semantics: drop stale queued item, then retry once.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- emit:
			default:
			}
		}
	}
	return cloneFeatureSignalContractStoreSnapshot(s.snapshot), nil
}

func (s *InMemoryFeatureSignalContractStore) Watch(ctx context.Context) (<-chan FeatureSignalContractStoreSnapshot, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	ch := make(chan FeatureSignalContractStoreSnapshot, s.watchBuffer)

	s.mu.Lock()
	id := s.nextWatchID
	s.nextWatchID++
	s.watchers[id] = ch
	initial := cloneFeatureSignalContractStoreSnapshot(s.snapshot)
	s.mu.Unlock()

	if initial.Revision > 0 {
		ch <- initial
	}

	go func() {
		<-ctx.Done()
		s.mu.Lock()
		delete(s.watchers, id)
		s.mu.Unlock()
		close(ch)
	}()
	return ch, nil
}

func cloneFeatureSignalContractStoreSnapshot(in FeatureSignalContractStoreSnapshot) FeatureSignalContractStoreSnapshot {
	out := FeatureSignalContractStoreSnapshot{
		Revision:    in.Revision,
		GeneratedAt: in.GeneratedAt,
		Active:      make(map[string]FeatureSignalContract, len(in.Active)),
		Meta:        make(map[string]time.Time, len(in.Meta)),
		History:     make(map[string][]FeatureSignalContractVersion, len(in.History)),
		Rollbacks:   make(map[string][]FeatureSignalContractRollbackRecord, len(in.Rollbacks)),
		Scheduled:   make(map[string][]FeatureSignalContractVersion, len(in.Scheduled)),
	}
	for k, v := range in.Active {
		out.Active[k] = FeatureSignalContract{
			RequiredFields:     append([]string(nil), v.RequiredFields...),
			SchemaVersion:      v.SchemaVersion,
			TrustedProducerIDs: append([]string(nil), v.TrustedProducerIDs...),
			MaxFreshnessMS:     v.MaxFreshnessMS,
			MaxDriftScore:      v.MaxDriftScore,
		}
	}
	for k, v := range in.Meta {
		out.Meta[k] = v
	}
	for k, items := range in.History {
		cp := make([]FeatureSignalContractVersion, len(items))
		for i := range items {
			cp[i] = cloneFeatureSignalContractVersion(items[i])
		}
		out.History[k] = cp
	}
	for k, items := range in.Rollbacks {
		cp := make([]FeatureSignalContractRollbackRecord, len(items))
		for i := range items {
			cp[i] = cloneFeatureSignalContractRollbackRecord(items[i])
		}
		out.Rollbacks[k] = cp
	}
	for k, items := range in.Scheduled {
		cp := make([]FeatureSignalContractVersion, len(items))
		for i := range items {
			cp[i] = cloneFeatureSignalContractVersion(items[i])
		}
		out.Scheduled[k] = cp
	}
	return out
}

func cloneFeatureSignalContractVersion(v FeatureSignalContractVersion) FeatureSignalContractVersion {
	v.RequiredFields = append([]string(nil), v.RequiredFields...)
	v.TrustedProducerIDs = append([]string(nil), v.TrustedProducerIDs...)
	return v
}

func cloneFeatureSignalContractRollbackRecord(v FeatureSignalContractRollbackRecord) FeatureSignalContractRollbackRecord {
	v.PreviousRequiredFields = append([]string(nil), v.PreviousRequiredFields...)
	v.TargetRequiredFields = append([]string(nil), v.TargetRequiredFields...)
	return v
}
