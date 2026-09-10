package agent

import (
	"context"
	"log"
	"sync"
	"time"

	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/session"
)

type historyMaintenanceProgress struct {
	rebuildAfter int64
	pruneAfter   int64
	running      bool
}

type historyMaintenanceState struct {
	mu     sync.Mutex
	owners map[session.AgentSessionKey]historyMaintenanceProgress
}

func (s *historyMaintenanceState) begin(key session.AgentSessionKey) (historyMaintenanceProgress, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	progress := s.owners[key]
	if progress.running {
		return progress, false
	}
	if s.owners == nil {
		s.owners = make(map[session.AgentSessionKey]historyMaintenanceProgress)
	}
	progress.running = true
	s.owners[key] = progress
	return progress, true
}

func (s *historyMaintenanceState) finish(key session.AgentSessionKey, progress historyMaintenanceProgress) {
	s.mu.Lock()
	defer s.mu.Unlock()
	progress.running = false
	s.owners[key] = progress
}

// MaintainHistory runs synchronously on the caller's entity lane, including all
// transaction cleanup. Rebuild and prune share one deadline and resource budget.
func (l *Loop) MaintainHistory(ctx context.Context, key session.AgentSessionKey, currentTime *memory.GameTimeSnapshot) {
	store, ok := l.historyStore.(memory.HistoryMaintenanceStore)
	if !ok || !l.config.MemoryEnabledValue() || ctx.Err() != nil {
		return
	}
	if _, err := session.Resolve(key.GameID, key.WorldID, key.EntityID); err != nil {
		log.Printf("history maintenance invalid owner: %v", err)
		return
	}
	progress, ok := l.maintenanceState.begin(key)
	if !ok {
		return
	}
	defer func() { l.maintenanceState.finish(key, progress) }()
	limits := l.config.HistoryMaintenance.WithDefaults()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(limits.TimeoutMS)*time.Millisecond)
	defer cancel()
	started := time.Now()
	result, err := store.RebuildHistoryIndex(ctx, memory.HistoryRebuildRequest{Owner: key, AfterSequence: progress.rebuildAfter, Limits: limits})
	valid := validHistoryMaintenanceResult(result, progress.rebuildAfter, limits)
	logHistoryMaintenance(key, "rebuild", started, result, valid, err)
	if err != nil || !valid {
		return
	}
	if result.More {
		progress.rebuildAfter = result.NextAfter
	} else {
		progress.rebuildAfter = 0
	}
	limits.Sources -= max(result.Processed, result.ReadSources)
	limits.Bytes -= max(result.Bytes, result.ReadBytes)
	if l.config.RetentionDays == 0 || ctx.Err() != nil || limits.Sources <= 0 || limits.Bytes <= 0 {
		return
	}
	if memory.SharedGameTimeBasis(currentTime) == memory.GameTimeUnknown {
		log.Printf("history maintenance owner=%s stage=prune diagnostics=[prune_time_unproven]", key.DiagnosticID())
		return
	}
	result, err = store.PruneHistory(ctx, memory.HistoryPruneRequest{
		Owner: key, AfterSequence: progress.pruneAfter, Now: time.Now(), RetentionDays: l.config.RetentionDays,
		CurrentTime: currentTime, KeepRecentTokens: l.config.Compaction.KeepRecentTokens, Limits: limits,
	})
	valid = validHistoryMaintenanceResult(result, progress.pruneAfter, limits)
	logHistoryMaintenance(key, "prune", started, result, valid, err)
	if err != nil || !valid {
		return
	}
	if result.More {
		progress.pruneAfter = result.NextAfter
	} else {
		progress.pruneAfter = 0
	}
}

func validHistoryMaintenanceResult(result memory.HistoryMaintenanceResult, after int64, limits memory.HistoryMaintenanceLimits) bool {
	if result.Processed < 0 || result.Bytes < 0 || result.ReadSources < 0 || result.ReadBytes < 0 ||
		max(result.Processed, result.ReadSources) > limits.Sources || max(result.Bytes, result.ReadBytes) > limits.Bytes ||
		len(result.SourceIDs) > result.Processed || result.NextAfter < 0 {
		return false
	}
	if result.Processed > 0 {
		return result.NextAfter > after
	}
	return !result.More && (result.NextAfter == 0 || result.NextAfter == after)
}

func logHistoryMaintenance(key session.AgentSessionKey, stage string, started time.Time, result memory.HistoryMaintenanceResult, valid bool, err error) {
	log.Printf("history maintenance owner=%s stage=%s processed=%d bytes=%d read_sources=%d read_bytes=%d next_after=%d more=%t valid=%t diagnostics=%q elapsed_ms=%d error=%v",
		key.DiagnosticID(), stage, result.Processed, result.Bytes, result.ReadSources, result.ReadBytes, result.NextAfter, result.More, valid, result.Diagnostics, time.Since(started).Milliseconds(), err)
}
