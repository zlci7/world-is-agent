package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"gameagent/runtime/internal/memory"
)

type maintenanceTestStore struct {
	*memory.InMemoryHistoryStore
	rebuilds  []memory.HistoryRebuildRequest
	prunes    []memory.HistoryPruneRequest
	deadlines []time.Time
	rebuild   func(context.Context, memory.HistoryRebuildRequest) (memory.HistoryMaintenanceResult, error)
	prune     func(context.Context, memory.HistoryPruneRequest) (memory.HistoryMaintenanceResult, error)
}

func (s *maintenanceTestStore) RebuildHistoryIndex(ctx context.Context, req memory.HistoryRebuildRequest) (memory.HistoryMaintenanceResult, error) {
	s.rebuilds = append(s.rebuilds, req)
	deadline, _ := ctx.Deadline()
	s.deadlines = append(s.deadlines, deadline)
	if s.rebuild != nil {
		return s.rebuild(ctx, req)
	}
	return memory.HistoryMaintenanceResult{}, nil
}
func (s *maintenanceTestStore) PruneHistory(ctx context.Context, req memory.HistoryPruneRequest) (memory.HistoryMaintenanceResult, error) {
	s.prunes = append(s.prunes, req)
	deadline, _ := ctx.Deadline()
	s.deadlines = append(s.deadlines, deadline)
	if s.prune != nil {
		return s.prune(ctx, req)
	}
	return memory.HistoryMaintenanceResult{}, nil
}

func TestMaintainHistoryDefaultRetentionBackfillsWithoutPruneOrModel(t *testing.T) {
	store := &maintenanceTestStore{InMemoryHistoryStore: memory.NewInMemoryHistoryStore(memory.HistoryLimits{})}
	store.rebuild = func(_ context.Context, req memory.HistoryRebuildRequest) (memory.HistoryMaintenanceResult, error) {
		if req.AfterSequence == 7 {
			return memory.HistoryMaintenanceResult{NextAfter: 7}, nil
		}
		return memory.HistoryMaintenanceResult{Processed: 1, Bytes: 100, NextAfter: 7, More: true}, nil
	}
	l := &Loop{config: DefaultConfig(), historyStore: store}
	l.MaintainHistory(context.Background(), preparationOwner, nil)
	l.MaintainHistory(context.Background(), preparationOwner, nil)
	if len(store.rebuilds) != 2 || len(store.prunes) != 0 || store.rebuilds[1].AfterSequence != 7 {
		t.Fatalf("maintenance: rebuild=%+v prune=%+v", store.rebuilds, store.prunes)
	}
	if store.rebuilds[0].Limits != memory.DefaultHistoryMaintenanceLimits() || store.rebuilds[0].Force {
		t.Fatalf("unbounded/forced backfill: %+v", store.rebuilds[0])
	}
}

func TestMaintainHistoryWrapsCompletedBackfillCursor(t *testing.T) {
	store := &maintenanceTestStore{InMemoryHistoryStore: memory.NewInMemoryHistoryStore(memory.HistoryLimits{})}
	store.rebuild = func(_ context.Context, req memory.HistoryRebuildRequest) (memory.HistoryMaintenanceResult, error) {
		if len(store.rebuilds) == 1 {
			return memory.HistoryMaintenanceResult{Processed: 1, NextAfter: 7, More: true}, nil
		}
		return memory.HistoryMaintenanceResult{NextAfter: req.AfterSequence}, nil
	}
	l := &Loop{config: DefaultConfig(), historyStore: store}
	for i := 0; i < 3; i++ {
		l.MaintainHistory(context.Background(), preparationOwner, nil)
	}
	if store.rebuilds[1].AfterSequence != 7 || store.rebuilds[2].AfterSequence != 0 {
		t.Fatalf("completed backfill prevents repairing earlier missing indexes: %+v", store.rebuilds)
	}
}

func TestMaintainHistorySharesBudgetAndDeadlineAndWrapsPruneCursor(t *testing.T) {
	store := &maintenanceTestStore{InMemoryHistoryStore: memory.NewInMemoryHistoryStore(memory.HistoryLimits{})}
	store.rebuild = func(_ context.Context, req memory.HistoryRebuildRequest) (memory.HistoryMaintenanceResult, error) {
		return memory.HistoryMaintenanceResult{Processed: 2, Bytes: 1000, NextAfter: req.AfterSequence + 2}, nil
	}
	store.prune = func(_ context.Context, req memory.HistoryPruneRequest) (memory.HistoryMaintenanceResult, error) {
		return memory.HistoryMaintenanceResult{Processed: 1, Bytes: 100, NextAfter: req.AfterSequence + 1, More: len(store.prunes) == 1}, nil
	}
	cfg := DefaultConfig()
	cfg.RetentionDays = 7
	l := &Loop{config: cfg, historyStore: store}
	current := preparationTime(9)
	for i := 0; i < 3; i++ {
		l.MaintainHistory(context.Background(), preparationOwner, current)
	}
	if len(store.prunes) != 3 {
		t.Fatalf("prune calls: %+v", store.prunes)
	}
	for i, req := range store.prunes {
		if req.Owner != preparationOwner || req.CurrentTime == nil || *req.CurrentTime != *current || req.RetentionDays != 7 || req.Now.IsZero() || req.KeepRecentTokens != cfg.Compaction.KeepRecentTokens || req.Limits.Sources != 14 || req.Limits.Bytes != (8<<20)-1000 {
			t.Fatalf("prune budget/scope: %+v", req)
		}
		if store.deadlines[i*2].IsZero() || !store.deadlines[i*2].Equal(store.deadlines[i*2+1]) {
			t.Fatalf("maintenance deadline renewed: %v", store.deadlines)
		}
	}
	if store.prunes[0].AfterSequence != 0 || store.prunes[1].AfterSequence != 1 || store.prunes[2].AfterSequence != 0 {
		t.Fatalf("prune cursor does not rescan aged sources: %+v", store.prunes)
	}
}

func TestMaintainHistoryCancellationExhaustionAndUnprovenTimeKeepPruneIdle(t *testing.T) {
	for _, mode := range []string{"canceled", "timeout", "sources", "bytes", "unknown time", "read failure", "invalid result"} {
		t.Run(mode, func(t *testing.T) {
			store := &maintenanceTestStore{InMemoryHistoryStore: memory.NewInMemoryHistoryStore(memory.HistoryLimits{})}
			cfg := DefaultConfig()
			cfg.RetentionDays = 1
			cfg.HistoryMaintenance.TimeoutMS = 20
			cfg.HistoryMaintenance.LockTimeoutMS = 5
			store.rebuild = func(ctx context.Context, req memory.HistoryRebuildRequest) (memory.HistoryMaintenanceResult, error) {
				switch mode {
				case "timeout":
					<-ctx.Done()
					return memory.HistoryMaintenanceResult{}, ctx.Err()
				case "sources":
					return memory.HistoryMaintenanceResult{Processed: req.Limits.Sources, NextAfter: 16}, nil
				case "bytes":
					return memory.HistoryMaintenanceResult{Processed: 1, Bytes: req.Limits.Bytes, NextAfter: 1}, nil
				case "read failure":
					return memory.HistoryMaintenanceResult{NextAfter: 77}, errors.New("read failed")
				case "invalid result":
					return memory.HistoryMaintenanceResult{Processed: 17, NextAfter: 77}, nil
				}
				return memory.HistoryMaintenanceResult{}, nil
			}
			l := &Loop{config: cfg, historyStore: store}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			current := preparationTime(9)
			if mode == "unknown time" {
				current = nil
			}
			l.MaintainHistory(ctx, preparationOwner, current)
			if len(store.prunes) != 0 {
				t.Fatalf("prune ran after %s", mode)
			}
			if mode == "canceled" && len(store.rebuilds) != 0 {
				t.Fatal("canceled task opened store")
			}
			if mode == "read failure" || mode == "invalid result" {
				l.MaintainHistory(ctx, preparationOwner, current)
				if store.rebuilds[1].AfterSequence != 0 {
					t.Fatal("invalid result advanced cursor")
				}
			}
		})
	}
}

func TestMaintainHistorySerializesSameOwnerAcrossCallers(t *testing.T) {
	store := &maintenanceTestStore{InMemoryHistoryStore: memory.NewInMemoryHistoryStore(memory.HistoryLimits{})}
	started, release := make(chan struct{}), make(chan struct{})
	store.rebuild = func(context.Context, memory.HistoryRebuildRequest) (memory.HistoryMaintenanceResult, error) {
		close(started)
		<-release
		return memory.HistoryMaintenanceResult{Processed: 1, NextAfter: 1}, nil
	}
	l := &Loop{config: DefaultConfig(), historyStore: store}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); l.MaintainHistory(context.Background(), preparationOwner, nil) }()
	<-started
	l.MaintainHistory(context.Background(), preparationOwner, nil)
	close(release)
	wg.Wait()
	if len(store.rebuilds) != 1 {
		t.Fatalf("overlapping same-owner maintenance: %d", len(store.rebuilds))
	}
}

func TestMaintainHistoryAccountsForAllReadsBeforePrune(t *testing.T) {
	for _, usage := range []memory.HistoryMaintenanceResult{
		{Processed: 1, Bytes: 100, ReadSources: 4, ReadBytes: 4096, NextAfter: 1},
		{Processed: 3, Bytes: 4096, ReadSources: 1, ReadBytes: 100, NextAfter: 1},
	} {
		store := &maintenanceTestStore{InMemoryHistoryStore: memory.NewInMemoryHistoryStore(memory.HistoryLimits{})}
		store.rebuild = func(context.Context, memory.HistoryRebuildRequest) (memory.HistoryMaintenanceResult, error) {
			return usage, nil
		}
		cfg := DefaultConfig()
		cfg.RetentionDays = 7
		l := &Loop{config: cfg, historyStore: store}
		l.MaintainHistory(context.Background(), preparationOwner, preparationTime(9))
		if len(store.prunes) != 1 {
			t.Fatalf("prune calls=%d", len(store.prunes))
		}
		limits := store.prunes[0].Limits
		if limits.Sources != 16-max(usage.Processed, usage.ReadSources) || limits.Bytes != (8<<20)-max(usage.Bytes, usage.ReadBytes) {
			t.Fatalf("read usage not subtracted: usage=%+v remaining=%+v", usage, limits)
		}
	}
}

func TestMaintainHistoryReadExhaustionStopsPrune(t *testing.T) {
	for _, usage := range []memory.HistoryMaintenanceResult{
		{ReadSources: 16}, {ReadBytes: 8 << 20}, {ReadSources: 17}, {ReadBytes: (8 << 20) + 1}, {ReadSources: -1}, {ReadBytes: -1},
	} {
		store := &maintenanceTestStore{InMemoryHistoryStore: memory.NewInMemoryHistoryStore(memory.HistoryLimits{})}
		store.rebuild = func(context.Context, memory.HistoryRebuildRequest) (memory.HistoryMaintenanceResult, error) {
			return usage, nil
		}
		cfg := DefaultConfig()
		cfg.RetentionDays = 7
		l := &Loop{config: cfg, historyStore: store}
		l.MaintainHistory(context.Background(), preparationOwner, preparationTime(9))
		if len(store.prunes) != 0 {
			t.Fatalf("prune ran despite read exhaustion/invalid usage: %+v", usage)
		}
	}
}

func TestMaintainHistoryInvalidPruneReadUsagePreservesCursor(t *testing.T) {
	store := &maintenanceTestStore{InMemoryHistoryStore: memory.NewInMemoryHistoryStore(memory.HistoryLimits{})}
	store.prune = func(context.Context, memory.HistoryPruneRequest) (memory.HistoryMaintenanceResult, error) {
		return memory.HistoryMaintenanceResult{Processed: 1, NextAfter: 77, More: true, ReadBytes: (8 << 20) + 1}, nil
	}
	cfg := DefaultConfig()
	cfg.RetentionDays = 7
	l := &Loop{config: cfg, historyStore: store}
	for i := 0; i < 2; i++ {
		l.MaintainHistory(context.Background(), preparationOwner, preparationTime(9))
	}
	if len(store.prunes) != 2 || store.prunes[1].AfterSequence != 0 {
		t.Fatalf("invalid prune result advanced cursor: %+v", store.prunes)
	}
}
