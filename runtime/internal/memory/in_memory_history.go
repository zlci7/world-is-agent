package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"gameagent/runtime/internal/idgen"
	"gameagent/runtime/internal/session"
)

type historyWorldKey struct {
	game  string
	world string
}

type InMemoryHistoryStore struct {
	mu           sync.Mutex
	limits       HistoryLimits
	sources      map[string]HistorySource
	historyOrder map[session.AgentSessionKey][]string
	sequences    map[historyWorldKey]int64
	summaries    map[string]SummaryCheckpoint
	summaryOrder map[session.AgentSessionKey][]string
	revisions    map[historyWorldKey]int64
	leases       map[string]HistorySnapshot
}

var (
	_ HistoryStore = (*InMemoryHistoryStore)(nil)
	_ SummaryStore = (*InMemoryHistoryStore)(nil)
)

func NewInMemoryHistoryStore(limits HistoryLimits) *InMemoryHistoryStore {
	return &InMemoryHistoryStore{
		limits: limits.WithDefaults(), sources: make(map[string]HistorySource),
		historyOrder: make(map[session.AgentSessionKey][]string), sequences: make(map[historyWorldKey]int64),
		summaries: make(map[string]SummaryCheckpoint), summaryOrder: make(map[session.AgentSessionKey][]string),
		revisions: make(map[historyWorldKey]int64), leases: make(map[string]HistorySnapshot),
	}
}

func (s *InMemoryHistoryStore) validate(ctx context.Context, owner session.AgentSessionKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := session.Resolve(owner.GameID, owner.WorldID, owner.EntityID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidHistory, err)
	}
	return s.limits.Validate()
}

func (s *InMemoryHistoryStore) AppendHistory(ctx context.Context, batch HistoryBatch) (HistorySource, error) {
	if err := s.validate(ctx, batch.Owner); err != nil {
		return HistorySource{}, err
	}
	data, key, fingerprint, err := CanonicalHistoryBatch(batch, s.limits.MaxBatchBytes)
	if err != nil {
		return HistorySource{}, err
	}
	if batch.Kind != HistoryKindTerminal && batch.Kind != HistoryKindTaskResult {
		return HistorySource{}, fmt.Errorf("%w: terminal history is required", ErrInvalidHistory)
	}
	var stored HistoryBatch
	if err := json.Unmarshal(data, &stored); err != nil {
		return HistorySource{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return HistorySource{}, err
	}
	id := "history_" + sha256LowerHex(key)
	if previous, ok := s.sources[id]; ok {
		if previous.Fingerprint != fingerprint {
			return HistorySource{}, ErrHistoryConflict
		}
		return cloneHistorySource(previous)
	}
	world := historyWorldKey{game: stored.Owner.GameID, world: stored.Owner.WorldID}
	s.sequences[world]++
	source := HistorySource{ID: id, Sequence: s.sequences[world], Owner: stored.Owner, BatchKey: key, Fingerprint: fingerprint, CreatedAt: time.Now().UTC(), Times: cloneHistoryTimes(HistoryTimes(stored)), Batch: &stored, Bytes: len(data), Availability: HistoryAvailable}
	s.sources[id] = source
	s.historyOrder[stored.Owner] = append(s.historyOrder[stored.Owner], id)
	return cloneHistorySource(source)
}

func (s *InMemoryHistoryStore) BeginHistorySnapshot(ctx context.Context, owner session.AgentSessionKey) (HistorySnapshot, error) {
	if err := s.validate(ctx, owner); err != nil {
		return HistorySnapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return HistorySnapshot{}, err
	}
	snapshot := HistorySnapshot{Owner: owner, LeaseID: idgen.New("history_snapshot")}
	if ids := s.historyOrder[owner]; len(ids) > 0 {
		snapshot.Watermark = s.sources[ids[len(ids)-1]].Sequence
	}
	if ids := s.summaryOrder[owner]; len(ids) > 0 {
		snapshot.SummaryRevision = s.summaries[ids[len(ids)-1]].Revision
	}
	s.leases[snapshot.LeaseID] = snapshot
	return snapshot, nil
}

func (s *InMemoryHistoryStore) ReleaseHistorySnapshot(snapshot HistorySnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.validSnapshotLocked(snapshot) {
		delete(s.leases, snapshot.LeaseID)
	}
}

func (s *InMemoryHistoryStore) validSnapshotLocked(snapshot HistorySnapshot) bool {
	saved, ok := s.leases[snapshot.LeaseID]
	return ok && saved == snapshot
}

func (s *InMemoryHistoryStore) ReadHistorySnapshot(ctx context.Context, snapshot HistorySnapshot, before int64, limits HistoryReadLimits) (HistoryPage, error) {
	if err := s.validate(ctx, snapshot.Owner); err != nil {
		return HistoryPage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.validSnapshotLocked(snapshot) {
		return HistoryPage{}, fmt.Errorf("%w: inactive snapshot", ErrInvalidHistory)
	}
	if limits.Records <= 0 {
		limits.Records = s.limits.PageRecords
	}
	if limits.Bytes <= 0 {
		limits.Bytes = s.limits.PageBytes
	}
	limits.Records = min(limits.Records, s.limits.PageRecords)
	limits.Bytes = min(limits.Bytes, s.limits.PageBytes)
	page := HistoryPage{}
	ids := s.historyOrder[snapshot.Owner]
	end := sort.Search(len(ids), func(i int) bool {
		sequence := s.sources[ids[i]].Sequence
		return sequence > snapshot.Watermark || (before != 0 && sequence >= before)
	})
	for i := end - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return HistoryPage{}, err
		}
		source := s.sources[ids[i]]
		if page.Scanned == limits.Records || (source.Availability == HistoryAvailable && source.Bytes <= limits.Bytes && page.Bytes+source.Bytes > limits.Bytes) {
			page.More = true
			break
		}
		page.Scanned++
		page.NextBefore = source.Sequence
		if source.Availability == HistoryPruned {
			page.Diagnostics = append(page.Diagnostics, "source_pruned")
			continue
		}
		if source.Bytes > limits.Bytes {
			page.Diagnostics = append(page.Diagnostics, "history_source_exceeds_page_bytes")
			continue
		}
		cloned, err := cloneHistorySource(source)
		if err != nil {
			return HistoryPage{}, err
		}
		page.Sources = append(page.Sources, cloned)
		page.Bytes += source.Bytes
	}
	return page, nil
}

func (s *InMemoryHistoryStore) ReadHistorySource(ctx context.Context, owner session.AgentSessionKey, id string) (HistorySource, error) {
	if err := s.validate(ctx, owner); err != nil {
		return HistorySource{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return HistorySource{}, err
	}
	source, ok := s.sources[id]
	if !ok || source.Owner != owner {
		return HistorySource{}, ErrHistoryNotFound
	}
	return cloneHistorySource(source)
}

func (s *InMemoryHistoryStore) ReadSummary(ctx context.Context, snapshot HistorySnapshot, currentTime *GameTimeSnapshot, limits SummaryReadLimits) (SummaryRead, error) {
	if err := s.validate(ctx, snapshot.Owner); err != nil {
		return SummaryRead{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.limits.ReadTimeoutMS)*time.Millisecond)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.validSnapshotLocked(snapshot) {
		return SummaryRead{}, fmt.Errorf("%w: inactive snapshot", ErrInvalidHistory)
	}
	return readSummaryWithAccess(ctx, snapshot, currentTime, boundedSummaryReadLimits(s.limits, limits), s.summaryAccessLocked(snapshot))
}

func (s *InMemoryHistoryStore) CommitSummary(ctx context.Context, request SummaryCommit) (SummaryCheckpoint, error) {
	if err := s.validate(ctx, request.Snapshot.Owner); err != nil {
		return SummaryCheckpoint{}, err
	}
	if err := validateSummaryCommit(request, s.limits); err != nil {
		return SummaryCheckpoint{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.limits.WriteTimeoutMS)*time.Millisecond)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.validSnapshotLocked(request.Snapshot) {
		return SummaryCheckpoint{}, fmt.Errorf("%w: inactive snapshot", ErrInvalidHistory)
	}
	owner := request.Snapshot.Owner
	var revision int64
	if ids := s.summaryOrder[owner]; len(ids) > 0 {
		revision = s.summaries[ids[len(ids)-1]].Revision
	}
	if revision != request.ExpectedRevision || revision != request.Snapshot.SummaryRevision {
		return SummaryCheckpoint{}, ErrSummaryConflict
	}
	checkpoint, err := prepareSummaryCheckpoint(ctx, request, s.limits, s.summaryAccessLocked(request.Snapshot))
	if err != nil {
		return SummaryCheckpoint{}, err
	}
	if err := ctx.Err(); err != nil {
		return SummaryCheckpoint{}, err
	}
	world := historyWorldKey{game: owner.GameID, world: owner.WorldID}
	s.revisions[world]++
	checkpoint.ID = idgen.New("summary")
	checkpoint.Revision = s.revisions[world]
	checkpoint.CreatedAt = time.Now().UTC()
	s.summaries[checkpoint.ID] = checkpoint
	s.summaryOrder[owner] = append(s.summaryOrder[owner], checkpoint.ID)
	return cloneSummaryCheckpoint(checkpoint), nil
}

func (s *InMemoryHistoryStore) summaryAccessLocked(snapshot HistorySnapshot) summaryAccess {
	return summaryAccess{
		checkpoint: func(id string, before int64, budget *summaryScanBudget) (*SummaryCheckpoint, error) {
			var checkpoint SummaryCheckpoint
			if id != "" {
				checkpoint = s.summaries[id]
			} else {
				ids := s.summaryOrder[snapshot.Owner]
				end := sort.Search(len(ids), func(i int) bool {
					revision := s.summaries[ids[i]].Revision
					return revision > snapshot.SummaryRevision || (before != 0 && revision >= before)
				})
				if end > 0 {
					checkpoint = s.summaries[ids[end-1]]
				}
			}
			if checkpoint.ID == "" || checkpoint.Owner != snapshot.Owner || checkpoint.Revision > snapshot.SummaryRevision {
				return nil, nil
			}
			checkpoint.Sources = nil
			checkpoint.Times = nil
			if err := budget.takeBytes(summaryHeaderBytes(checkpoint)); err != nil {
				return &checkpoint, err
			}
			return &checkpoint, decodeSummaryCoverageTimes(&checkpoint)
		},
		references: func(id string, budget *summaryScanBudget) ([]HistorySourceRef, error) {
			checkpoint := s.summaries[id]
			if checkpoint.Owner != snapshot.Owner {
				return nil, ErrSummaryConflict
			}
			if len(checkpoint.Sources) > budget.sources {
				return nil, fmt.Errorf("%w: summary coverage scan", ErrHistoryCapacity)
			}
			size := 0
			for _, ref := range checkpoint.Sources {
				size += summaryReferenceBytes(ref)
			}
			if err := budget.takeBytes(size); err != nil {
				return nil, err
			}
			budget.sources -= len(checkpoint.Sources)
			return append([]HistorySourceRef(nil), checkpoint.Sources...), nil
		},
		sources: func(ctx context.Context, ids []string, budget *summaryScanBudget) ([]HistorySource, error) {
			heads := make([]HistorySource, 0, len(ids))
			for _, id := range ids {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				source, ok := s.sources[id]
				if !ok || source.Owner != snapshot.Owner {
					return nil, ErrHistoryNotFound
				}
				times, err := json.Marshal(source.Times)
				if err != nil {
					return nil, err
				}
				if err := budget.takeBytes(summarySourceHeadBytes(source, len(times))); err != nil {
					return nil, err
				}
				heads = append(heads, HistorySource{
					ID: source.ID, Sequence: source.Sequence, Owner: source.Owner,
					Fingerprint: source.Fingerprint, Availability: source.Availability,
					Times: cloneHistoryTimes(source.Times),
				})
			}
			return heads, nil
		},
	}
}

func cloneHistorySource(source HistorySource) (HistorySource, error) {
	source.Times = cloneHistoryTimes(source.Times)
	if source.Batch != nil {
		batch, err := CloneHistoryBatch(*source.Batch)
		if err != nil {
			return HistorySource{}, err
		}
		source.Batch = &batch
	}
	return source, nil
}
