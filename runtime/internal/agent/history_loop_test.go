package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/trace"
)

func requestHistorySources(t *testing.T, req model.Request) []memory.HistoryBatch {
	t.Helper()
	for _, message := range req.Messages {
		const marker = "[History]\n"
		index := strings.Index(message.Content, marker)
		if index < 0 {
			continue
		}
		var history struct {
			Sources []struct {
				SourceID string              `json:"source_id"`
				Content  memory.HistoryBatch `json:"content"`
			} `json:"sources"`
		}
		if err := json.NewDecoder(strings.NewReader(message.Content[index+len(marker):])).Decode(&history); err != nil {
			t.Fatal(err)
		}
		batches := make([]memory.HistoryBatch, len(history.Sources))
		for i, source := range history.Sources {
			if source.SourceID == "" {
				t.Fatal("history provenance missing")
			}
			batches[i] = source.Content
		}
		return batches
	}
	t.Fatal("final model request has no History section")
	return nil
}

func assertRequestHasCompletedHistory(t *testing.T, req model.Request, eventID, text string) {
	t.Helper()
	sources := requestHistorySources(t, req)
	if len(sources) != 1 {
		t.Fatalf("history count=%d", len(sources))
	}
	batch := sources[0]
	if batch.Event.ID != eventID || batch.Terminal.Status != "completed" || len(batch.Steps) != 1 || len(batch.Steps[0].Executions) != 1 {
		t.Fatalf("wrong terminal source: %+v", batch)
	}
	execution := batch.Steps[0].Executions[0]
	if execution.Call.Arguments["text"] != text || !execution.Started || execution.ActionID == "" || execution.ActionResult.GetStatus() != protocol.ActionStatus_ACTION_STATUS_SUCCEEDED {
		t.Fatalf("actual action source missing: %+v", execution)
	}
}

func TestHistoryLifecycleSavesEveryCreatedTerminalTurn(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		observeErr   error
		decision     model.ModelDecision
	}{
		{name: "completed", status: "completed", decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}},
		{name: "observe failed", status: "failed", observeErr: errors.New("observe unavailable")},
		{name: "invalid decision", status: "failed", decision: model.ModelDecision{Control: model.ControlDirective{Kind: "invalid"}}},
		{name: "cancelled", status: "cancelled", observeErr: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultLoopConfig(t)
			key := session.AgentSessionKey{GameID: "history-game", WorldID: "history-world", EntityID: "npc"}
			event := gameEvent("input", key)
			event.ContextFacts = []*protocol.ContextFact{{Kind: "dialogue", ActorEntityId: "player", Text: "Do not repeat code 7319."}}
			env := &fakeEnvironment{observeErrors: []error{tc.observeErr}}
			provider := &recordingProvider{response: model.Response{Decision: tc.decision}}
			loop := agent.NewLoop(provider, trace.NoopRecorder{}, cfg)
			_ = loop.HandleEvent(context.Background(), env, agent.ConnectionContext{GameID: key.GameID}, key, entityTarget(key), newSpeakRegistry(), event)
			store := memory.NewSQLiteHistoryStore(memory.SQLiteStoreOptions{Root: cfg.MemoryStore.Root}, cfg.History)
			snapshot, err := store.BeginHistorySnapshot(context.Background(), key)
			if err != nil {
				t.Fatal(err)
			}
			defer store.ReleaseHistorySnapshot(snapshot)
			page, err := store.ReadHistorySnapshot(context.Background(), snapshot, 0, memory.HistoryReadLimits{Records: 10, Bytes: 1 << 20})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Sources) != 1 {
				t.Fatalf("terminal history count = %d, want 1", len(page.Sources))
			}
			batch := page.Sources[0].Batch
			if batch.Terminal.Status != tc.status || batch.Event.Facts[0].Text != event.ContextFacts[0].Text {
				t.Fatalf("wrong terminal history: %+v", batch)
			}
			if tc.observeErr != nil && len(batch.Observations) != 0 {
				t.Fatal("failed Observe fabricated an observation")
			}
		})
	}
}

func TestHistoryLifecycleTimeoutPreservesInput(t *testing.T) {
	cfg := defaultLoopConfig(t)
	cfg.TurnTimeout = 20 * time.Millisecond
	key := session.AgentSessionKey{GameID: "history-game", WorldID: "timeout-world", EntityID: "npc"}
	provider := &scriptedProvider{delay: time.Second}
	loop := agent.NewLoop(provider, trace.NoopRecorder{}, cfg)
	err := loop.HandleEvent(context.Background(), &fakeEnvironment{}, agent.ConnectionContext{GameID: key.GameID}, key, entityTarget(key), newSpeakRegistry(), gameEvent("timed-input", key))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	store := memory.NewSQLiteHistoryStore(memory.SQLiteStoreOptions{Root: cfg.MemoryStore.Root}, cfg.History)
	snapshot, err := store.BeginHistorySnapshot(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseHistorySnapshot(snapshot)
	page, err := store.ReadHistorySnapshot(context.Background(), snapshot, 0, memory.HistoryReadLimits{Records: 10, Bytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sources) != 1 || page.Sources[0].Batch.Terminal.Status != "cancelled" {
		t.Fatalf("timed-out turn missing: %+v", page)
	}
}

type faultHistoryStore struct {
	memory.HistoryStore
	append     func(context.Context, memory.HistoryBatch) (memory.HistorySource, error)
	beginError error
}

func (s *faultHistoryStore) AppendHistory(ctx context.Context, batch memory.HistoryBatch) (memory.HistorySource, error) {
	return s.append(ctx, batch)
}
func (s *faultHistoryStore) BeginHistorySnapshot(ctx context.Context, key session.AgentSessionKey) (memory.HistorySnapshot, error) {
	if s.beginError != nil {
		return memory.HistorySnapshot{}, s.beginError
	}
	return s.HistoryStore.BeginHistorySnapshot(ctx, key)
}

func TestHistoryFailuresPreserveActionAndTurnCompletion(t *testing.T) {
	cfg := defaultLoopConfig(t)
	key := session.AgentSessionKey{GameID: "history-game", WorldID: "fault-world", EntityID: "npc"}
	calls := 0
	store := &faultHistoryStore{HistoryStore: memory.NewInMemoryHistoryStore(cfg.History), beginError: errors.New("read failed")}
	store.append = func(ctx context.Context, batch memory.HistoryBatch) (memory.HistorySource, error) {
		calls++
		if len(batch.Steps) != 1 || len(batch.Steps[0].Executions) != 1 || batch.Steps[0].Executions[0].ActionResult.GetStatus() != protocol.ActionStatus_ACTION_STATUS_SUCCEEDED {
			t.Error("successful action missing from write attempt")
		}
		return memory.HistorySource{}, errors.New("write failed")
	}
	provider := &recordingProvider{}
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	loop := agent.NewLoop(provider, recorder, cfg, agent.WithHistoryStore(store))
	if err := loop.HandleEvent(context.Background(), env, agent.ConnectionContext{GameID: key.GameID}, key, entityTarget(key), newSpeakRegistry(), gameEvent("input", key)); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(env.submittedActions) != 1 || len(env.turnCompletions) != 1 || env.turnCompletions[0].Status != protocol.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED {
		t.Fatalf("history failure changed terminal semantics: writes=%d env=%+v", calls, env)
	}
	assertTraceContains(t, recorder.events, trace.EventContextUpdateFailed)
	if recorder.events[len(recorder.events)-1].Event != trace.EventTurnCompleted {
		t.Fatal("terminal trace is not last")
	}
}

func TestHistoryCancellationWaitsForBoundedTerminalAppend(t *testing.T) {
	cfg := defaultLoopConfig(t)
	cfg.History.WriteTimeoutMS = 500
	key := session.AgentSessionKey{GameID: "history-game", WorldID: "cancel-world", EntityID: "npc"}
	started := make(chan struct {
		err       error
		remaining time.Duration
		status    string
	}, 1)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	store := &faultHistoryStore{HistoryStore: memory.NewInMemoryHistoryStore(cfg.History)}
	store.append = func(ctx context.Context, batch memory.HistoryBatch) (memory.HistorySource, error) {
		deadline, _ := ctx.Deadline()
		started <- struct {
			err       error
			remaining time.Duration
			status    string
		}{ctx.Err(), time.Until(deadline), batch.Terminal.Status}
		<-release
		return store.HistoryStore.AppendHistory(ctx, batch)
	}
	loop := agent.NewLoop(&scriptedProvider{}, trace.NoopRecorder{}, cfg, agent.WithHistoryStore(store))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		done <- loop.HandleEvent(ctx, &fakeEnvironment{observeErrors: []error{context.Canceled}}, agent.ConnectionContext{GameID: key.GameID}, key, entityTarget(key), newSpeakRegistry(), gameEvent("cancelled-input", key))
	}()
	select {
	case state := <-started:
		if state.err != nil || state.remaining <= 0 || state.remaining > 500*time.Millisecond || state.status != "cancelled" {
			t.Fatalf("invalid independent write context: %+v", state)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal append did not start")
	}
	select {
	case err := <-done:
		t.Fatalf("lane task returned before transaction completion: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	released = true
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("turn did not return after append")
	}
	snapshot, err := store.BeginHistorySnapshot(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseHistorySnapshot(snapshot)
	page, err := store.ReadHistorySnapshot(context.Background(), snapshot, 0, memory.HistoryReadLimits{Records: 1, Bytes: 1 << 20})
	if err != nil || len(page.Sources) != 1 {
		t.Fatalf("next turn cannot read completed terminal append: %+v %v", page, err)
	}
}

func TestHistoryPreparationUsesContextCurrentTime(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		event, observe, stored int64
		want                   bool
	}{
		{"event authoritative", 100, 90, 95, true},
		{"event hides future", 90, 100, 95, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultLoopConfig(t)
			store := memory.NewInMemoryHistoryStore(cfg.History)
			key := session.AgentSessionKey{GameID: "clock-test", WorldID: "world", EntityID: "npc"}
			_, err := store.AppendHistory(context.Background(), memory.HistoryBatch{Owner: key, Kind: memory.HistoryKindTerminal, Version: memory.HistoryVersion, TurnID: "earlier", Event: memory.HistoryEvent{ID: "earlier", GameTime: memory.SnapshotGameTime(&protocol.GameTime{Tick: &tc.stored}), Facts: []memory.SourceContextFact{{Kind: "fact", Text: "unique-time-visible-history"}}}, Terminal: memory.HistoryTerminal{Status: "completed"}})
			if err != nil {
				t.Fatal(err)
			}
			provider := &scriptedProvider{}
			loop := agent.NewLoop(provider, trace.NoopRecorder{}, cfg, agent.WithHistoryStore(store))
			event := gameEvent("now", key)
			event.GameTime = &protocol.GameTime{Tick: &tc.event}
			env := &fakeEnvironment{observations: []*protocol.Observation{{WorldId: key.WorldID, EntityId: key.EntityID, GameTime: &protocol.GameTime{Tick: &tc.observe}}}}
			if err := loop.HandleEvent(context.Background(), env, agent.ConnectionContext{GameID: key.GameID}, key, entityTarget(key), environmentCatalogFromCapabilities(nil), event); err != nil {
				t.Fatal(err)
			}
			visible := strings.Contains(provider.requests[0].Messages[0].Content, "unique-time-visible-history")
			if visible != tc.want {
				t.Fatalf("history visible=%t want=%t", visible, tc.want)
			}
		})
	}
}

func TestHistorySkipsPreparationWhenRequiredContextCannotFit(t *testing.T) {
	cfg := defaultLoopConfig(t)
	cfg.MaxSystemTokens = 1
	key := session.AgentSessionKey{GameID: "budget-test", WorldID: "world", EntityID: "npc"}
	reads, writes := 0, 0
	store := &countingHistoryStore{HistoryStore: memory.NewInMemoryHistoryStore(cfg.History), reads: &reads, writes: &writes}
	provider := &scriptedProvider{}
	loop := agent.NewLoop(provider, trace.NoopRecorder{}, cfg, agent.WithHistoryStore(store))
	if err := loop.HandleEvent(context.Background(), &fakeEnvironment{}, agent.ConnectionContext{GameID: key.GameID}, key, entityTarget(key), environmentCatalogFromCapabilities(nil), gameEvent("oversized", key)); err == nil {
		t.Fatal("required-context failure missing")
	}
	if reads != 0 || writes != 1 || len(provider.requests) != 0 {
		t.Fatalf("failed context did unnecessary work: reads=%d writes=%d decisions=%d", reads, writes, len(provider.requests))
	}
}

type countingHistoryStore struct {
	memory.HistoryStore
	reads, writes *int
}

func (s *countingHistoryStore) BeginHistorySnapshot(ctx context.Context, key session.AgentSessionKey) (memory.HistorySnapshot, error) {
	*s.reads++
	return s.HistoryStore.BeginHistorySnapshot(ctx, key)
}
func (s *countingHistoryStore) AppendHistory(ctx context.Context, batch memory.HistoryBatch) (memory.HistorySource, error) {
	*s.writes++
	return s.HistoryStore.AppendHistory(ctx, batch)
}

type cancellingHistoryTrace struct{ cancel context.CancelFunc }

func (r cancellingHistoryTrace) Record(event trace.Event) {
	if event.Event == trace.EventContextRequestBuilt {
		r.cancel()
	}
}
func (cancellingHistoryTrace) Close(context.Context) error { return nil }

func TestHistoryParentCancelledAfterBuildDoesNotCallProvider(t *testing.T) {
	cfg := defaultLoopConfig(t)
	key := session.AgentSessionKey{GameID: "cancel-test", WorldID: "world", EntityID: "npc"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &recordingProvider{}
	loop := agent.NewLoop(provider, cancellingHistoryTrace{cancel}, cfg, agent.WithHistoryStore(memory.NewInMemoryHistoryStore(cfg.History)))
	_ = loop.HandleEvent(ctx, &fakeEnvironment{}, agent.ConnectionContext{GameID: key.GameID}, key, entityTarget(key), newSpeakRegistry(), gameEvent("cancel-on-build", key))
	if len(provider.requests) != 0 {
		t.Fatal("provider called after parent cancellation")
	}
}

type historyErrorProvider struct{}

func (historyErrorProvider) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, errors.New("provider unavailable")
}

func TestHistoryPersistsKnownSourcesAcrossTerminalPaths(t *testing.T) {
	for _, kind := range []string{"provider_failure", "call_budget", "step_budget", "parallel_partial_failure", "async_reobserve_failure", "rejected_then_settled"} {
		t.Run(kind, func(t *testing.T) {
			cfg := defaultLoopConfig(t)
			key := session.AgentSessionKey{GameID: "terminal-paths", WorldID: kind, EntityID: "npc"}
			store := memory.NewInMemoryHistoryStore(cfg.History)
			var env agent.Environment = &fakeEnvironment{}
			registry := newSpeakRegistry()
			decision := model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlContinue}, ToolCalls: []model.ToolCall{{ID: "first", Name: "speak", Arguments: map[string]any{"text": "success"}}}}
			var provider model.Provider
			status, stage, steps := "failed", "", 1
			switch kind {
			case "provider_failure":
				provider = historyErrorProvider{}
				stage = "model"
				steps = 0
			case "call_budget":
				cfg.MaxToolCallsPerStep = 1
				stage = "model"
				decision.ToolCalls = append(decision.ToolCalls, model.ToolCall{ID: "second", Name: "speak", Arguments: map[string]any{"text": "extra"}})
			case "step_budget":
				cfg.MaxSteps = 1
				stage = "step"
			case "parallel_partial_failure":
				stage = "action"
				registry = newParallelSpeakRegistry()
				env = &technicalActionEnvironment{delays: map[string]time.Duration{"fatal": 10 * time.Millisecond}, submitErrors: map[string]error{"fatal": errors.New("transport closed")}}
				decision.ToolCalls = append(decision.ToolCalls, model.ToolCall{ID: "second", Name: "speak", Arguments: map[string]any{"text": "fatal"}})
			case "async_reobserve_failure":
				stage = "observation"
				registry = newMoveToRegistry()
				env = &fakeEnvironment{observeErrors: []error{nil, errors.New("reobserve unavailable")}}
				decision.ToolCalls = []model.ToolCall{{ID: "move", Name: "move_to", Arguments: map[string]any{"label": "town_square"}}}
			case "rejected_then_settled":
				status = "completed"
				steps = 2
				env = &fakeEnvironment{statusByTool: map[string]protocol.ActionStatus{"speak": protocol.ActionStatus_ACTION_STATUS_REJECTED}}
			}
			if provider == nil {
				provider = &scriptedProvider{responses: []model.Response{{Decision: decision}}}
			}
			loop := agent.NewLoop(provider, trace.NoopRecorder{}, cfg, agent.WithHistoryStore(store))
			event := gameEvent("input", key)
			event.ContextFacts = []*protocol.ContextFact{{Kind: "dialogue", Text: "Do not reveal 7319."}}
			err := loop.HandleEvent(context.Background(), env, agent.ConnectionContext{GameID: key.GameID}, key, entityTarget(key), registry, event)
			if (err == nil) != (status == "completed") {
				t.Fatalf("turn result=%v want status=%s", err, status)
			}
			snapshot, err := store.BeginHistorySnapshot(context.Background(), key)
			if err != nil {
				t.Fatal(err)
			}
			defer store.ReleaseHistorySnapshot(snapshot)
			page, err := store.ReadHistorySnapshot(context.Background(), snapshot, 0, memory.HistoryReadLimits{Records: 8, Bytes: 1 << 20})
			if err != nil || len(page.Sources) != 1 {
				t.Fatalf("history=%+v err=%v", page, err)
			}
			batch := page.Sources[0].Batch
			if batch.Terminal.Status != status || batch.Terminal.Stage != stage || len(batch.Steps) != steps || batch.Event.Facts[0].Text != event.ContextFacts[0].Text || len(batch.Observations) != 1 {
				t.Fatalf("terminal source mismatch: %+v", batch)
			}
			if steps == 0 {
				return
			}
			executions := batch.Steps[0].Executions
			if kind == "call_budget" {
				if len(executions) != 0 || len(batch.Steps[0].Decision.ToolCalls) != 2 {
					t.Fatal("unexecuted intent lost or fabricated receipt")
				}
				return
			}
			if len(executions) == 0 || executions[0].ActionID == "" || !executions[0].Started {
				t.Fatalf("actual action provenance missing: %+v", executions)
			}
			if kind == "rejected_then_settled" {
				if executions[0].ActionResult.GetStatus() != protocol.ActionStatus_ACTION_STATUS_REJECTED {
					t.Fatal("actual rejected receipt lost")
				}
				return
			}
			if executions[0].ActionResult.GetStatus() != protocol.ActionStatus_ACTION_STATUS_SUCCEEDED {
				t.Fatal("successful actual receipt lost")
			}
			if kind == "parallel_partial_failure" && (len(executions) != 2 || executions[1].RuntimeError == "" || executions[1].ActionResult != nil) {
				t.Fatalf("technical failure confused with game receipt: %+v", executions)
			}
		})
	}
}
