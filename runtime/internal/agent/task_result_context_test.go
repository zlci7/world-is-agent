package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/trace"
)

// commitContextTask drives one task to a confirmed terminal result through the real
// evidence and reconcile path.
func commitContextTask(t *testing.T, env *taskContextEnvironment, key session.AgentSessionKey, suffix, kind string, occurredAt int64) task.Record {
	t.Helper()
	ctx := context.Background()
	record := seedContextTask(t, env, key, suffix)
	evidence := task.Evidence{
		FactID: "fact-" + suffix, TaskID: record.ID, Binding: env.world.head.Binding, StartRevision: record.Revision,
		OccurredAt: occurredAt, Kind: kind,
		Source: task.SourceRef{Kind: task.SourceKindEnvironment, EventID: "event-" + suffix, TurnID: "turn-" + suffix, CallID: "call-" + suffix},
	}
	added, err := env.svc.AdmitEvidence(ctx, env.world.head.Binding, evidence)
	if err != nil || !added {
		t.Fatalf("admit %s evidence = %v (%v)", suffix, added, err)
	}
	reconciled, err := env.svc.Reconcile(ctx, task.ExecutionContext{
		Owner: key, Binding: env.world.head.Binding, Clock: env.world.head.Clock, TaskID: record.ID, ExpectedRevision: record.Revision,
		Source: task.SourceRef{Kind: task.SourceKindInternal, EventID: "reconcile", TurnID: "reconcile", CallID: "reconcile-" + suffix},
	})
	if err != nil || reconciled.Task.Result == nil {
		t.Fatalf("reconcile %s = %+v (%v)", suffix, reconciled.Task, err)
	}
	return reconciled.Task
}

func TestOrdinaryDialogueRequestCarriesCommittedResults(t *testing.T) {
	env, key, event, catalog := taskContextFixture(t)
	missed := commitContextTask(t, env, key, "missed", task.EvidenceKindUnsatisfied, 90)
	failed := commitContextTask(t, env, key, "failed", task.EvidenceKindInterrupted, 85)
	current := seedContextTask(t, env, key, "current")

	provider := &taskContextModel{run: func(req model.Request, _ int) model.ModelDecision {
		return model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}
	}}
	config := contextConfig(t)
	loop := NewLoop(provider, nil, config, WithHistoryStore(memory.NewInMemoryHistoryStore(config.History)))
	if err := loop.HandleEvent(context.Background(), env, ConnectionContext{}, key, &protocol.EntityRef{EntityId: key.EntityID}, catalog, event); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) == 0 {
		t.Fatal("turn produced no model request")
	}
	content := provider.requests[0].Messages[0].Content
	for _, want := range []string{
		`"task_id": "` + current.ID + `"`, `"state": "waiting"`,
		`"recent_results"`, `"result_id": "` + missed.Result.ID + `"`, `"reason": "unsatisfied"`,
		`"result_id": "` + failed.Result.ID + `"`, `"reason": "interrupted"`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("final request missing %q:\n%s", want, content)
		}
	}
	// Committed results are facts, not execution state: the authoritative task block
	// leads, the live task keeps its own state, and the results name their own tasks.
	if strings.Index(content, `"task_id": "`+current.ID+`"`) > strings.Index(content, `"recent_results"`) {
		t.Fatalf("results preceded the live task identity:\n%s", content)
	}
	for _, terminal := range []task.Record{missed, failed} {
		if strings.Contains(content, `"task_id": "`+terminal.Result.TaskID+`"`) == false {
			t.Fatalf("result %s lost its own task identity:\n%s", terminal.Result.ID, content)
		}
	}
	record, err := env.svc.Read(context.Background(), key, current.ID)
	if err != nil || record.State != task.StateWaiting || record.Result != nil {
		t.Fatalf("results changed the live task: %+v (%v)", record, err)
	}
}

type recordingTrace struct {
	mu     sync.Mutex
	events []trace.Event
}

func (r *recordingTrace) Record(event trace.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}
func (r *recordingTrace) Close(context.Context) error { return nil }

func (r *recordingTrace) fieldsFor(name trace.EventName) []trace.Fields {
	r.mu.Lock()
	defer r.mu.Unlock()
	var found []trace.Fields
	for _, event := range r.events {
		if event.Event == name {
			found = append(found, event.Fields)
		}
	}
	return found
}

func TestTaskContextTraceNamesTaskResultAndCheckpoint(t *testing.T) {
	env, key, event, catalog := taskContextFixture(t)
	env.world.head.CheckpointID = "checkpoint-7"
	missed := commitContextTask(t, env, key, "missed", task.EvidenceKindUnsatisfied, 90)
	current := seedContextTask(t, env, key, "current")

	provider := &taskContextModel{run: func(model.Request, int) model.ModelDecision {
		return model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}
	}}
	recorder := &recordingTrace{}
	config := contextConfig(t)
	loop := NewLoop(provider, recorder, config, WithHistoryStore(memory.NewInMemoryHistoryStore(config.History)))
	if err := loop.HandleEvent(context.Background(), env, ConnectionContext{}, key, &protocol.EntityRef{EntityId: key.EntityID}, catalog, event); err != nil {
		t.Fatal(err)
	}
	fields := recorder.fieldsFor(trace.EventTaskContextPrepared)
	if len(fields) != 1 {
		t.Fatalf("task context trace events = %d, want one per prepared step", len(fields))
	}
	got := fields[0]
	if got["task_id"] != current.ID || got["task_state"] != string(task.StateWaiting) {
		t.Fatalf("task trace = %+v", got)
	}
	if got["checkpoint_id"] != "checkpoint-7" {
		t.Fatalf("checkpoint trace = %+v", got)
	}
	if got["result_id"] != missed.Result.ID || got["result_state"] != string(task.StateFailed) {
		t.Fatalf("result trace = %+v", got)
	}
	for name := range got {
		switch name {
		case "step_index", "task_id", "task_state", "checkpoint_id", "result_id", "result_state":
		default:
			t.Fatalf("unasserted trace field %q in %+v", name, got)
		}
	}

	sizes := recorder.fieldsFor(trace.EventContextRequestBuilt)
	if len(sizes) == 0 || sizes[0]["task_context_estimated_tokens"] == nil {
		t.Fatalf("task budget report missing: %+v", sizes)
	}
}
