package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
)

func committedTaskResult(state task.State, reason string) task.Result {
	return task.Result{
		ID: "result-1", TaskID: "task-1", Revision: 4, State: state, Reason: reason, OccurredAt: 520,
		EvidenceRefs: []string{"fact-1"},
		Source:       task.SourceRef{Kind: task.SourceKindEnvironment, EventID: "event-1", TurnID: "turn-1", CallID: "fact-1"},
	}
}

func TestTaskResultHistoryMapsCommittedResults(t *testing.T) {
	owner := session.AgentSessionKey{GameID: "sim", WorldID: "world", EntityID: "actor"}
	tests := []struct {
		name     string
		result   task.Result
		wantFact string
		wantTick int64
	}{
		{name: "satisfied", result: committedTaskResult(task.StateSucceeded, task.EvidenceKindSatisfied), wantFact: "the agreed result was confirmed", wantTick: 520},
		{name: "expired", result: committedTaskResult(task.StateFailed, task.EvidenceKindUnsatisfied), wantFact: "the agreed window ended without the result", wantTick: 520},
		{name: "interrupted", result: committedTaskResult(task.StateFailed, task.EvidenceKindInterrupted), wantFact: "the agreed action was interrupted", wantTick: 520},
		{name: "failed", result: committedTaskResult(task.StateFailed, "route_unavailable"), wantFact: "the agreed action failed", wantTick: 520},
		{name: "cancelled", result: committedTaskResult(task.StateCancelled, "player withdrew"), wantFact: "the agreement was cancelled", wantTick: 520},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			batch, err := TaskResultHistoryBatch(owner, tt.result)
			if err != nil {
				t.Fatal(err)
			}
			if batch.Kind != memory.HistoryKindTaskResult || batch.Version != memory.HistoryVersion || batch.TurnID != "turn-1" {
				t.Fatalf("batch identity = %+v", batch)
			}
			if batch.Event.ID != "event-1" || batch.Event.Type != "task_result" {
				t.Fatalf("source event = %+v, want the committed source event", batch.Event)
			}
			if batch.Event.GameTime == nil || batch.Event.GameTime.Tick != tt.wantTick {
				t.Fatalf("game time = %+v, want tick %d", batch.Event.GameTime, tt.wantTick)
			}
			if len(batch.Event.Facts) != 1 {
				t.Fatalf("facts = %+v", batch.Event.Facts)
			}
			fact := batch.Event.Facts[0]
			if fact.Kind != "task_result" || fact.ScopeID != "task-1" || fact.Text != tt.wantFact || fact.ActorEntityID != "" {
				t.Fatalf("fact = %+v, want the confirmed outcome under the task scope", fact)
			}
			got := batch.TaskResult
			if got == nil || got.ResultID != "result-1" || got.TaskID != "task-1" || got.Revision != 4 || got.State != string(tt.result.State) || got.Reason != tt.result.Reason || got.OccurredAt != 520 {
				t.Fatalf("result payload = %+v", got)
			}
			if len(got.EvidenceRefs) != 1 || got.EvidenceRefs[0] != "fact-1" {
				t.Fatalf("evidence refs = %+v", got.EvidenceRefs)
			}
		})
	}
}

func TestTaskResultHistoryUsesTheReportedGameTime(t *testing.T) {
	owner := session.AgentSessionKey{GameID: "sim", WorldID: "world", EntityID: "actor"}
	result := committedTaskResult(task.StateSucceeded, task.EvidenceKindSatisfied)
	result.Source.GameTime = json.RawMessage(`{"year":"1","season":"2","day":"3","hour":"14","minute":"30","tick":"900"}`)
	batch, err := TaskResultHistoryBatch(owner, result)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Event.GameTime == nil || batch.Event.GameTime.Hour != 14 || batch.Event.GameTime.Day != 3 || batch.Event.GameTime.Tick != 900 {
		t.Fatalf("game time = %+v, want the reported calendar time", batch.Event.GameTime)
	}
}

func TestTaskResultHistoryRejectsUncommittedResults(t *testing.T) {
	owner := session.AgentSessionKey{GameID: "sim", WorldID: "world", EntityID: "actor"}
	tests := []struct {
		name   string
		want   error
		mutate func(*task.Result)
	}{
		{name: "running", want: memory.ErrInvalidHistory, mutate: func(r *task.Result) { r.State, r.Reason = task.StateRunning, "" }},
		{name: "waiting", want: memory.ErrInvalidHistory, mutate: func(r *task.Result) { r.State, r.Reason = task.StateWaiting, "" }},
		{name: "paused", want: memory.ErrInvalidHistory, mutate: func(r *task.Result) { r.State, r.Reason = task.StatePaused, "checkpoint_missing" }},
		{name: "unknown state", want: memory.ErrInvalidHistory, mutate: func(r *task.Result) { r.State = "archived" }},
		{name: "missing result id", want: memory.ErrInvalidHistory, mutate: func(r *task.Result) { r.ID = "" }},
		{name: "missing task id", want: memory.ErrInvalidHistory, mutate: func(r *task.Result) { r.TaskID = "" }},
		{name: "zero revision", want: memory.ErrInvalidHistory, mutate: func(r *task.Result) { r.Revision = 0 }},
		{name: "missing occurrence", want: memory.ErrInvalidHistory, mutate: func(r *task.Result) { r.OccurredAt = 0 }},
		{name: "unreadable game time", want: task.ErrInvalidTaskSpec, mutate: func(r *task.Result) { r.Source.GameTime = json.RawMessage(`{"tick":`) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := committedTaskResult(task.StateSucceeded, task.EvidenceKindSatisfied)
			tt.mutate(&result)
			if _, err := TaskResultHistoryBatch(owner, result); !errors.Is(err, tt.want) {
				t.Fatalf("TaskResultHistoryBatch() = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestTaskResultHistoryOptionWiresTheSinkInAnyOrder(t *testing.T) {
	history := memory.NewInMemoryHistoryStore(memory.HistoryLimits{})
	for _, options := range [][]ServerOption{
		{WithTaskService(task.NewService(nil)), WithTaskResultHistory(history)},
		{WithTaskResultHistory(history), WithTaskService(task.NewService(nil))},
	} {
		server := NewServer(nil, options...)
		if server.WorldRegistry() == nil || server.WorldRegistry().resultHistory == nil || server.WorldRegistry().resultHistory.Store != memory.HistoryStore(history) {
			t.Fatal("task result history sink not wired to the world registry")
		}
	}
	if server := NewServer(nil, WithTaskResultHistory(nil)); server.WorldRegistry() != nil {
		t.Fatal("empty history store created a registry")
	}
}

func TestCommittedTaskResultReachesHistoryWithoutAModel(t *testing.T) {
	f := newTaskWireFixture(t, false)
	history := memory.NewInMemoryHistoryStore(memory.HistoryLimits{})
	f.server.worlds.resultHistory = &ResultHistorySink{Store: history}
	record, op := seedWireOperation(t, f)

	f.event("receipt-event", []*protocol.TaskEvidence{wireReceipt(f, record, op, "satisfied")}, nil)
	if f.next().GetEventAck().GetStatus() != protocol.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatal("evidence rejected")
	}
	control := f.next().GetTaskControl()
	if control == nil || control.TaskId != record.ID {
		t.Fatalf("terminal result did not release its operation: %v", control)
	}
	// The Runtime waits for the adapter to confirm a release. Answering here is
	// what the neighbouring suites do; a peer that never answers makes the Runtime
	// spend its whole confirmation budget, which is slow and turns the release this
	// test requests later into a race against that same budget.
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_TaskControlResult{TaskControlResult: &protocol.TaskControlResult{
		Scope:       control.Scope,
		TaskId:      control.TaskId,
		OperationId: control.OperationId,
		RequestId:   control.RequestId,
		Status:      "released",
	}}})
	terminal := f.awaitRecord(record.ID, func(r task.Record) bool { return r.Result != nil })
	if f.model.calls.Load() != 0 {
		t.Fatalf("model calls = %d, want a result source without cognition", f.model.calls.Load())
	}

	sources := taskResultSources(t, history, f.key)
	if len(sources) != 1 {
		t.Fatalf("history sources = %d, want one result source", len(sources))
	}
	stored := sources[0].Batch
	if stored == nil || stored.TaskResult == nil || stored.TaskResult.ResultID != terminal.Result.ID {
		t.Fatalf("stored result = %+v", stored)
	}
	if stored.Event.Facts[0].Text != "the agreed result was confirmed" || stored.Event.Facts[0].ScopeID != record.ID {
		t.Fatalf("stored fact = %+v", stored.Event.Facts)
	}

	entry, _ := f.server.worlds.Current(f.head.Binding.World)
	if err := entry.Environment.(*streamEnvironment).reconcileTaskIDs(f.ctx, f.key, []string{record.ID}); err != nil {
		t.Fatal(err)
	}
	if again := taskResultSources(t, history, f.key); len(again) != 1 || again[0].ID != sources[0].ID {
		t.Fatalf("repeat commit produced %d sources", len(again))
	}
}

func TestTaskResultHistoryFailureKeepsTheCommittedTask(t *testing.T) {
	f := newTaskWireFixture(t, false)
	f.server.worlds.resultHistory = &ResultHistorySink{Store: failingHistoryStore{err: errors.New("history unavailable")}}
	record, op := seedWireOperation(t, f)
	f.event("receipt-event", []*protocol.TaskEvidence{wireReceipt(f, record, op, "satisfied")}, nil)
	if f.next().GetEventAck().GetStatus() != protocol.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatal("evidence rejected")
	}
	if control := f.next().GetTaskControl(); control == nil || control.TaskId != record.ID {
		t.Fatalf("history failure blocked local release: %v", control)
	}
	terminal := f.awaitRecord(record.ID, func(r task.Record) bool { return r.Result != nil })
	if terminal.State != task.StateSucceeded {
		t.Fatalf("history failure changed the task: %+v", terminal)
	}
}

type failingHistoryStore struct {
	memory.HistoryStore
	err error
}

func (s failingHistoryStore) AppendHistory(context.Context, memory.HistoryBatch) (memory.HistorySource, error) {
	return memory.HistorySource{}, s.err
}

func taskResultSources(t *testing.T, store memory.HistoryStore, owner session.AgentSessionKey) []memory.HistorySource {
	t.Helper()
	snapshot, err := store.BeginHistorySnapshot(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseHistorySnapshot(snapshot)
	page, err := store.ReadHistorySnapshot(context.Background(), snapshot, 0, memory.HistoryReadLimits{})
	if err != nil {
		t.Fatal(err)
	}
	return page.Sources
}
