package gateway

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
)

// taskResultHistoryTimeout bounds one result publication. History is downstream of
// the committed task, so the write never waits on adapter or model work.
const taskResultHistoryTimeout = 2 * time.Second

// ResultHistorySink publishes committed task results as their own history source.
// It only accepts results that already committed: the terminal state, the stable
// result identity and the game time the result occurred at.
type ResultHistorySink struct {
	Store memory.HistoryStore
}

var errResultHistoryStoreMissing = errors.New("task result history store is not configured")

func (s ResultHistorySink) Publish(ctx context.Context, owner session.AgentSessionKey, result task.Result) (memory.HistorySource, error) {
	if s.Store == nil {
		return memory.HistorySource{}, errResultHistoryStoreMissing
	}
	batch, err := TaskResultHistoryBatch(owner, result)
	if err != nil {
		return memory.HistorySource{}, err
	}
	return s.Store.AppendHistory(ctx, batch)
}

// TaskResultHistoryBatch maps one committed result onto its history source. The
// source states the confirmed outcome as a fact; it never fabricates a model turn
// and never attributes a spoken line.
func TaskResultHistoryBatch(owner session.AgentSessionKey, result task.Result) (memory.HistoryBatch, error) {
	text := taskResultOutcomeText(result)
	if text == "" || result.ID == "" || result.TaskID == "" || result.Revision == 0 || result.OccurredAt <= 0 {
		return memory.HistoryBatch{}, fmt.Errorf("%w: committed task result required", memory.ErrInvalidHistory)
	}
	gameTime, err := taskResultGameTime(result)
	if err != nil {
		return memory.HistoryBatch{}, err
	}
	return memory.HistoryBatch{
		Owner: owner, Kind: memory.HistoryKindTaskResult, Version: memory.HistoryVersion, TurnID: result.Source.TurnID,
		Event: memory.HistoryEvent{
			ID: result.Source.EventID, Type: "task_result", GameTime: gameTime,
			Facts: []memory.SourceContextFact{{Kind: "task_result", ScopeID: result.TaskID, Text: text}},
		},
		TaskResult: &memory.HistoryTaskResult{
			ResultID: result.ID, TaskID: result.TaskID, Revision: result.Revision, State: string(result.State),
			Reason: result.Reason, OccurredAt: result.OccurredAt, EvidenceRefs: slices.Clone(result.EvidenceRefs),
		},
	}, nil
}

// The confirmed outcome is decided by the authoritative terminal evidence. A
// running or paused task has no outcome to record.
func taskResultOutcomeText(result task.Result) string {
	switch result.State {
	case task.StateSucceeded:
		return "the agreed result was confirmed"
	case task.StateCancelled:
		return "the agreement was cancelled"
	case task.StateFailed:
		switch result.Reason {
		case task.EvidenceKindUnsatisfied:
			return "the agreed window ended without the result"
		case task.EvidenceKindInterrupted:
			return "the agreed action was interrupted"
		default:
			return "the agreed action failed"
		}
	default:
		return ""
	}
}

// Evidence carries the game time when the world reported one. A result whose
// source has no calendar time keeps the tick it occurred at.
func taskResultGameTime(result task.Result) (*memory.GameTimeSnapshot, error) {
	value, err := gameTimeFromRawJSON(result.Source.GameTime)
	if err != nil {
		return nil, err
	}
	if snapshot := memory.SnapshotGameTime(value); snapshot != nil {
		return snapshot, nil
	}
	return memory.TickGameTime(result.OccurredAt), nil
}

// commitTaskResult publishes the committed terminal result and then releases the
// operations the task still holds. Publication is downstream: a failure is
// diagnosed, and it never rolls back the task or blocks local control release.
func (e *streamEnvironment) commitTaskResult(ctx context.Context, binding task.Binding, record task.Record) error {
	e.publishTaskResult(ctx, record)
	return e.releaseTaskControl(ctx, binding, record)
}

func (e *streamEnvironment) publishTaskResult(ctx context.Context, record task.Record) {
	if e.taskAuthority == nil || record.Result == nil {
		return
	}
	sink := e.taskAuthority.registry.resultHistory
	if sink == nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskResultHistoryTimeout)
	defer cancel()
	started := time.Now()
	source, err := sink.Publish(writeCtx, record.Owner, *record.Result)
	if err != nil {
		log.Printf("task result history: task=%s result=%s owner=%s stage=publish error=%s", record.ID, record.Result.ID, record.Owner.DiagnosticID(), logSafeError(err))
		return
	}
	log.Printf("task result history: task=%s result=%s owner=%s stage=committed source=%s wait_ms=%d", record.ID, record.Result.ID, record.Owner.DiagnosticID(), source.ID, time.Since(started).Milliseconds())
}
