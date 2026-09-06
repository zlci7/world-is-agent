package agent_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	agentcontext "gameagent/runtime/internal/context"
	"gameagent/runtime/internal/definition"
	"gameagent/runtime/internal/llm/fake"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/tool"
	"gameagent/runtime/internal/trace"

	"google.golang.org/protobuf/types/known/structpb"
)

type recordingTraceRecorder struct {
	events []trace.Event
}

func (r *recordingTraceRecorder) Record(event trace.Event) {
	r.events = append(r.events, event)
}

func (r *recordingTraceRecorder) Close(ctx context.Context) error {
	return nil
}

type fakeEnvironment struct {
	observedWorldID   string
	observedEntityID  string
	observeCount      int
	observations      []*protocolv1alpha2.Observation
	observeErrors     []error
	submittedAction   *protocolv1alpha2.ActionRequest
	submittedActions  []*protocolv1alpha2.ActionRequest
	turnCompletions   []*protocolv1alpha2.TurnCompletion
	completeErr       error
	statusByTool      map[string]protocolv1alpha2.ActionStatus
	startStatusByTool map[string]protocolv1alpha2.ActionStatus
	waitErrorsByTool  map[string]error
	waitDelayByTool   map[string]time.Duration
	asyncActionTools  map[string]string
	cancelledActions  []string
}

type technicalActionEnvironment struct {
	mu               sync.Mutex
	submittedActions []*protocolv1alpha2.ActionRequest
	turnCompletions  []*protocolv1alpha2.TurnCompletion
	submitErrors     map[string]error
	delays           map[string]time.Duration
}

type recordingProvider struct {
	requests []model.Request
	response model.Response
}

func (p *recordingProvider) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	p.requests = append(p.requests, req)

	if p.response.Decision.ToolCalls != nil || p.response.Decision.Control.Kind != "" {
		return p.response, nil
	}

	return model.Response{
		Decision: model.ModelDecision{
			ToolCalls: []model.ToolCall{
				{
					ID:        "call_1",
					Name:      "speak",
					Arguments: map[string]any{"text": "remember this line"},
				},
			},
			Control: model.ControlDirective{Kind: model.ControlSettle},
		},
	}, nil
}

type scriptedProvider struct {
	requests  []model.Request
	responses []model.Response
	delay     time.Duration
}

func (p *scriptedProvider) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	p.requests = append(p.requests, req)

	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return model.Response{}, ctx.Err()
		}
	}

	if len(p.responses) == 0 {
		return model.Response{
			Decision: model.ModelDecision{
				Control: model.ControlDirective{Kind: model.ControlSettle},
			},
		}, nil
	}
	resp := p.responses[0]
	p.responses = p.responses[1:]
	return resp, nil
}

type failRecentStore struct {
	appended []memory.Record
}

func (s *failRecentStore) Append(ctx context.Context, record memory.Record) error {
	s.appended = append(s.appended, record)
	return nil
}

func (s *failRecentStore) Recent(ctx context.Context, key session.AgentSessionKey, limit int) ([]memory.Record, error) {
	return nil, errors.New("memory read failed")
}

type failAppendStore struct{}

func (s failAppendStore) Append(ctx context.Context, record memory.Record) error {
	return errors.New("memory append failed")
}

func (s failAppendStore) Recent(ctx context.Context, key session.AgentSessionKey, limit int) ([]memory.Record, error) {
	return nil, nil
}

type failProjector struct{}

func (failProjector) Project(input memory.ProjectInput) (memory.Record, error) {
	return memory.Record{}, memory.ErrProject
}

func (f *fakeEnvironment) Observe(ctx context.Context, worldID string, entityID string) (*protocolv1alpha2.Observation, error) {
	f.observedWorldID = worldID
	f.observedEntityID = entityID
	f.observeCount++

	if len(f.observeErrors) > 0 {
		err := f.observeErrors[0]
		f.observeErrors = f.observeErrors[1:]
		if err != nil {
			return nil, err
		}
	}

	if len(f.observations) > 0 {
		obs := f.observations[0]
		f.observations = f.observations[1:]
		return obs, nil
	}

	state, err := structpb.NewStruct(map[string]any{
		"weather": "snow",
		"time":    "afternoon",
	})
	if err != nil {
		return nil, err
	}

	return &protocolv1alpha2.Observation{
		EntityId: entityID,
		WorldId:  worldID,
		State:    state,
	}, nil
}

func TestHandleEventLoadsRecentMemoryOnLaterTurn(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &recordingProvider{}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	first := &protocolv1alpha2.GameEvent{
		EventId:        "event_1",
		EventType:      "player_interacted_with_npc",
		WorldId:        key.WorldID,
		TargetEntityId: key.EntityID,
	}
	second := &protocolv1alpha2.GameEvent{
		EventId:        "event_2",
		EventType:      "player_interacted_with_npc",
		WorldId:        key.WorldID,
		TargetEntityId: key.EntityID,
	}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, first); err != nil {
		t.Fatalf("first HandleEvent returned error: %v", err)
	}
	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, second); err != nil {
		t.Fatalf("second HandleEvent returned error: %v", err)
	}

	if len(provider.requests) != 2 {
		t.Fatalf("provider request count = %d, want 2", len(provider.requests))
	}
	secondContent := provider.requests[1].Messages[0].Content
	for _, want := range []string{
		"[Recent Memory]",
		"previous interaction",
		`tool "speak" status "ACTION_STATUS_SUCCEEDED" arguments {"text":"remember this line"}`,
		"remember this line",
	} {
		if !strings.Contains(secondContent, want) {
			t.Fatalf("second request missing %q:\n%s", want, secondContent)
		}
	}
	for _, unwanted := range []string{
		"event_1",
		"source_turn_id",
	} {
		if strings.Contains(secondContent, unwanted) {
			t.Fatalf("second request should not expose storage field %q:\n%s", unwanted, secondContent)
		}
	}

	assertTraceContains(t, recorder.events, trace.EventContextLoaded)
	assertTraceContains(t, recorder.events, trace.EventContextUpdated)
}

func TestHandleEventFailsBeforeProviderWhenContextScopeMismatches(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &recordingProvider{}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}
	event := gameEvent("event_scope_mismatch", key)
	event.TargetEntityId = "npc:Haley"

	err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, event)
	if err == nil {
		t.Fatal("HandleEvent returned nil error, want context scope mismatch")
	}
	if !strings.Contains(err.Error(), "event target_entity_id does not match session key") {
		t.Fatalf("error = %v, want event target scope mismatch", err)
	}
	if got := len(provider.requests); got != 0 {
		t.Fatalf("provider request count = %d, want 0", got)
	}
	if got := len(env.submittedActions); got != 0 {
		t.Fatalf("submitted action count = %d, want 0", got)
	}
	completion := requireSingleTurnCompletion(t, env.turnCompletions)
	if completion.Status != protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_FAILED {
		t.Fatalf("completion status = %s, want failed", completion.Status)
	}
	if completion.GetError().GetCode() != "context_build_failed" {
		t.Fatalf("completion error code = %q, want context_build_failed", completion.GetError().GetCode())
	}
}

func TestHandleEventRendersDefinitionsFromInjectedCatalogAndCanonicalTarget(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &recordingProvider{
		response: model.Response{
			Decision: model.ModelDecision{
				Control: model.ControlDirective{Kind: model.ControlSettle, Reason: "done"},
			},
		},
	}
	catalog, err := definition.NewCatalog(
		[]definition.GameDefinition{{
			SchemaVersion: definition.SchemaVersionV1Alpha1,
			GameID:        "fake-game",
			Title:         "Fake Game",
			Summary:       "A scoped fake world.",
		}},
		[]definition.AgentDefinition{{
			SchemaVersion:      definition.SchemaVersionV1Alpha1,
			GameID:             "fake-game",
			DefinitionID:       "villager/farmer",
			Identity:           "A reusable farmer archetype.",
			BehaviorGuidelines: []string{"Use the current instance descriptor."},
		}},
	)
	if err != nil {
		t.Fatalf("NewCatalog returned error: %v", err)
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig(), agent.WithDefinitionCatalog(catalog))
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "creature:alpha"}
	event := gameEvent("event_1", key)
	target := &protocolv1alpha2.EntityRef{
		EntityId:     "creature:alpha",
		EntityType:   "creature",
		DisplayName:  "Alpha",
		DefinitionId: "villager/farmer",
	}

	if err := loop.HandleEvent(context.Background(), env, conn, key, target, registry, event); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	if len(provider.requests) != 1 {
		t.Fatalf("provider request count = %d, want 1", len(provider.requests))
	}
	content := provider.requests[0].Messages[0].Content
	assertRequestContentContains(t, content,
		"[Game Definition]",
		"title: Fake Game",
		"summary: A scoped fake world.",
		"[Agent Definition]",
		"identity: A reusable farmer archetype.",
		"- Use the current instance descriptor.",
		"[Agent Descriptor]",
		"game_id: fake-game",
		"world_id: world:test",
		"entity_id: creature:alpha",
		"entity_type: creature",
		"display_name: Alpha",
		"definition_id: villager/farmer",
	)
}

func TestHandleEventRejectsMissingCanonicalTarget(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &recordingProvider{
		response: model.Response{
			Decision: model.ModelDecision{
				Control: model.ControlDirective{Kind: model.ControlSettle, Reason: "done"},
			},
		},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "creature:alpha"}

	err := loop.HandleEvent(context.Background(), env, conn, key, nil, registry, gameEvent("event_1", key))
	if err == nil {
		t.Fatal("HandleEvent returned nil error, want missing canonical target error")
	}
	if !strings.Contains(err.Error(), "canonical target") {
		t.Fatalf("error = %v, want canonical target", err)
	}
}

func TestHandleEventRejectsCanonicalTargetEntityMismatch(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &recordingProvider{
		response: model.Response{
			Decision: model.ModelDecision{
				Control: model.ControlDirective{Kind: model.ControlSettle, Reason: "done"},
			},
		},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "creature:alpha"}
	target := &protocolv1alpha2.EntityRef{
		EntityId:     "creature:beta",
		EntityType:   "creature",
		DisplayName:  "Beta",
		DefinitionId: "villager/farmer",
	}

	err := loop.HandleEvent(context.Background(), env, conn, key, target, registry, gameEvent("event_1", key))
	if err == nil {
		t.Fatal("HandleEvent returned nil error, want target mismatch error")
	}
	if !strings.Contains(err.Error(), "target entity id") {
		t.Fatalf("error = %v, want target entity id", err)
	}
}

func TestHandleEventRejectsNilEnvironmentToolCatalog(t *testing.T) {
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &recordingProvider{}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), nil, gameEvent("event_1", key))
	if err == nil {
		t.Fatal("HandleEvent returned nil error, want missing environment tool catalog error")
	}
	if !strings.Contains(err.Error(), "environment tool catalog is required") {
		t.Fatalf("HandleEvent error = %v, want missing environment tool catalog", err)
	}
	if env.observeCount != 0 {
		t.Fatalf("Observe count = %d, want 0", env.observeCount)
	}
}

func TestHandleEventCompletesSettleOnlyWithEmptyEnvironmentToolCatalog(t *testing.T) {
	catalog := mustEnvironmentToolCatalog(t, nil)
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{
		responses: []model.Response{{
			Decision: model.ModelDecision{
				Control: model.ControlDirective{Kind: model.ControlSettle},
			},
		}},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), catalog, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	if got := env.observeCount; got != 1 {
		t.Fatalf("Observe count = %d, want 1", got)
	}
	if got := len(env.submittedActions); got != 0 {
		t.Fatalf("submitted action count = %d, want 0", got)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider request count = %d, want 1", len(provider.requests))
	}
	if got := len(provider.requests[0].Tools); got != 0 {
		t.Fatalf("model-visible tool count = %d, want 0", got)
	}
	completion := requireSingleTurnCompletion(t, env.turnCompletions)
	assertTurnCompletionScope(t, completion, "event_1", key)
	if completion.Status != protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED {
		t.Fatalf("completion status = %s, want completed", completion.Status)
	}
}

func TestHandleEventReturnsToolNotRegisteredWithEmptyEnvironmentToolCatalog(t *testing.T) {
	catalog := mustEnvironmentToolCatalog(t, nil)
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{
		responses: []model.Response{
			{
				Decision: model.ModelDecision{
					ToolCalls: []model.ToolCall{{
						ID:        "call_1",
						Name:      "speak",
						Arguments: map[string]any{"text": "hello"},
					}},
					Control: model.ControlDirective{Kind: model.ControlContinue},
				},
			},
			{
				Decision: model.ModelDecision{
					Control: model.ControlDirective{Kind: model.ControlSettle},
				},
			},
		},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), catalog, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	if got := len(env.submittedActions); got != 0 {
		t.Fatalf("submitted action count = %d, want 0", got)
	}
	if got := len(provider.requests); got != 2 {
		t.Fatalf("provider request count = %d, want retry step", got)
	}
	for index, req := range provider.requests {
		if got := len(req.Tools); got != 0 {
			t.Fatalf("request %d tool count = %d, want 0", index, got)
		}
	}
	if !requestMessagesContain(provider.requests[1].Messages, "tool_not_registered") {
		t.Fatalf("second request missing tool_not_registered result: %+v", provider.requests[1].Messages)
	}
	completion := requireSingleTurnCompletion(t, env.turnCompletions)
	assertTurnCompletionScope(t, completion, "event_1", key)
	if completion.Status != protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED {
		t.Fatalf("completion status = %s, want completed", completion.Status)
	}
}

func TestHandleEventUsesTurnToolViewForModelRequestAndScheduler(t *testing.T) {
	catalog := mustEnvironmentToolCatalog(t, []*protocolv1alpha2.Capability{
		{
			Name:            "emote",
			Description:     "Display an emote.",
			InputSchemaJson: `{"type":"object","properties":{"emote":{"type":"string","enum":["happy","sad"]}},"required":["emote"],"additionalProperties":false}`,
			ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_SYNC,
		},
	})
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &recordingProvider{
		response: model.Response{
			Decision: model.ModelDecision{
				ToolCalls: []model.ToolCall{{
					ID:        "call_1",
					Name:      "emote",
					Arguments: map[string]any{"emote": "happy"},
				}},
				Control: model.ControlDirective{Kind: model.ControlSettle},
			},
		},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), catalog, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	if len(provider.requests) != 1 {
		t.Fatalf("provider request count = %d, want 1", len(provider.requests))
	}
	if got, want := provider.requests[0].Tools[0].Name, "emote"; got != want {
		t.Fatalf("request tool = %q, want %q", got, want)
	}
	if env.submittedAction == nil {
		t.Fatal("expected emote action to be submitted")
	}
	if env.submittedAction.Capability != "emote" {
		t.Fatalf("submitted capability = %q, want emote", env.submittedAction.Capability)
	}
}

func TestHandleEventAppliesToolAdmissionToModelRequestAndScheduler(t *testing.T) {
	catalog := mustEnvironmentToolCatalog(t, []*protocolv1alpha2.Capability{
		{
			Name:            "alpha",
			Description:     "First admitted tool.",
			InputSchemaJson: `{"type":"object"}`,
			ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_SYNC,
		},
		{
			Name:            "zeta",
			Description:     "Dropped after the count limit.",
			InputSchemaJson: `{"type":"object"}`,
			ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_SYNC,
		},
	})
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{
		responses: []model.Response{
			{
				Decision: model.ModelDecision{
					ToolCalls: []model.ToolCall{{
						ID:        "call_1",
						Name:      "zeta",
						Arguments: map[string]any{},
					}},
					Control: model.ControlDirective{Kind: model.ControlContinue},
				},
			},
			{
				Decision: model.ModelDecision{
					Control: model.ControlDirective{Kind: model.ControlSettle},
				},
			},
		},
	}
	config := agent.DefaultConfig()
	config.MaxToolCount = 1
	config.MaxSteps = 2
	loop := agent.NewLoop(provider, recorder, config)
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), catalog, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	if got := len(provider.requests); got != 2 {
		t.Fatalf("provider request count = %d, want retry after dropped tool failure", got)
	}
	for index, req := range provider.requests {
		if got, want := len(req.Tools), 1; got != want {
			t.Fatalf("request %d tool count = %d, want %d", index, got, want)
		}
		if got, want := req.Tools[0].Name, "alpha"; got != want {
			t.Fatalf("request %d tool = %q, want %q", index, got, want)
		}
	}
	if !requestMessagesContain(provider.requests[1].Messages, "tool_not_registered") {
		t.Fatalf("second request missing tool_not_registered result: %+v", provider.requests[1].Messages)
	}
	assertTraceContains(t, recorder.events, trace.EventContextRequestBuilt)
}

func TestHandleEventEmitsBoundedToolAdmissionTraceSummary(t *testing.T) {
	capabilities := make([]*protocolv1alpha2.Capability, 0, 25)
	for i := 0; i < 25; i++ {
		capabilities = append(capabilities, &protocolv1alpha2.Capability{
			Name:            fmt.Sprintf("tool_%02d", i),
			Description:     "short",
			InputSchemaJson: `{"type":"object"}`,
			ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_SYNC,
		})
	}
	catalog := mustEnvironmentToolCatalog(t, capabilities)
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &recordingProvider{response: model.Response{
		Decision: model.ModelDecision{
			Control: model.ControlDirective{Kind: model.ControlSettle},
		},
	}}
	config := agent.DefaultConfig()
	config.MaxToolCount = 1
	loop := agent.NewLoop(provider, recorder, config)
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), catalog, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	started := traceEventsByName(recorder.events, trace.EventTurnStarted)
	if len(started) != 1 {
		t.Fatalf("turn_started event count = %d, want 1", len(started))
	}
	assertTraceToolAdmissionSummary(t, started[0].Fields, 24)

	built := traceEventsByName(recorder.events, trace.EventContextRequestBuilt)
	if len(built) != 1 {
		t.Fatalf("context_request_built event count = %d, want 1", len(built))
	}
	assertTraceToolAdmissionSummary(t, built[0].Fields, 24)
}

func TestHandleEventFailsBeforeProviderWhenRequestHardLimitExceeded(t *testing.T) {
	catalog := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &recordingProvider{}
	config := agent.DefaultConfig()
	config.MaxSystemTokens = 1
	loop := agent.NewLoop(provider, recorder, config)
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), catalog, gameEvent("event_1", key))
	if !errors.Is(err, agentcontext.ErrBudgetExceeded) {
		t.Fatalf("HandleEvent error = %v, want ErrBudgetExceeded", err)
	}
	if got := len(provider.requests); got != 0 {
		t.Fatalf("provider request count = %d, want 0", got)
	}
	if got := len(env.submittedActions); got != 0 {
		t.Fatalf("submitted action count = %d, want 0", got)
	}
	completion := requireSingleTurnCompletion(t, env.turnCompletions)
	if completion.GetError().GetCode() != agentcontext.ReasonRequiredContextOverBudget {
		t.Fatalf("completion error code = %q, want %q", completion.GetError().GetCode(), agentcontext.ReasonRequiredContextOverBudget)
	}
	assertTraceContains(t, recorder.events, trace.EventContextRequestBuildFailed)
	assertContextBuildFailedReasons(t, recorder.events, []string{
		agentcontext.ReasonRequiredContextOverBudget,
		agentcontext.ReasonRequiredSectionOverBudget,
	})
	failed := traceEventsByName(recorder.events, trace.EventContextRequestBuildFailed)
	if len(failed) != 1 {
		t.Fatalf("context_request_build_failed event count = %d, want 1", len(failed))
	}
	totalTokens, ok := failed[0].Fields["request_total_estimated_tokens"].(int)
	if !ok || totalTokens == 0 {
		t.Fatalf("request_total_estimated_tokens = %#v, want non-zero int", failed[0].Fields["request_total_estimated_tokens"])
	}
	assertTraceNotContains(t, recorder.events, trace.EventModelRequestStarted)
}

func TestHandleEventKeepsSeparateInstanceScopeForSharedDefinition(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &recordingProvider{
		response: model.Response{
			Decision: model.ModelDecision{
				Control: model.ControlDirective{Kind: model.ControlSettle, Reason: "done"},
			},
		},
	}
	catalog, err := definition.NewCatalog(
		[]definition.GameDefinition{{
			SchemaVersion: definition.SchemaVersionV1Alpha1,
			GameID:        "fake-game",
			Title:         "Fake Game",
		}},
		[]definition.AgentDefinition{{
			SchemaVersion: definition.SchemaVersionV1Alpha1,
			GameID:        "fake-game",
			DefinitionID:  "villager/farmer",
			Identity:      "A reusable farmer archetype.",
		}},
	)
	if err != nil {
		t.Fatalf("NewCatalog returned error: %v", err)
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig(), agent.WithDefinitionCatalog(catalog))
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	alphaKey := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:alpha", EntityID: "creature:alpha"}
	betaKey := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:beta", EntityID: "creature:beta"}
	alphaTarget := &protocolv1alpha2.EntityRef{
		EntityId:     alphaKey.EntityID,
		EntityType:   "creature",
		DisplayName:  "Alpha",
		DefinitionId: "villager/farmer",
	}
	betaTarget := &protocolv1alpha2.EntityRef{
		EntityId:     betaKey.EntityID,
		EntityType:   "creature",
		DisplayName:  "Beta",
		DefinitionId: "villager/farmer",
	}

	if err := loop.HandleEvent(context.Background(), env, conn, alphaKey, alphaTarget, registry, gameEvent("event_alpha", alphaKey)); err != nil {
		t.Fatalf("alpha HandleEvent returned error: %v", err)
	}
	if err := loop.HandleEvent(context.Background(), env, conn, betaKey, betaTarget, registry, gameEvent("event_beta", betaKey)); err != nil {
		t.Fatalf("beta HandleEvent returned error: %v", err)
	}

	if len(provider.requests) != 2 {
		t.Fatalf("provider request count = %d, want 2", len(provider.requests))
	}
	alphaContent := provider.requests[0].Messages[0].Content
	betaContent := provider.requests[1].Messages[0].Content
	assertRequestContentContains(t, alphaContent,
		"identity: A reusable farmer archetype.",
		"world_id: world:alpha",
		"entity_id: creature:alpha",
		"display_name: Alpha",
		"definition_id: villager/farmer",
	)
	assertRequestContentContains(t, betaContent,
		"identity: A reusable farmer archetype.",
		"world_id: world:beta",
		"entity_id: creature:beta",
		"display_name: Beta",
		"definition_id: villager/farmer",
	)
	for _, unwanted := range []string{"world:beta", "creature:beta", "display_name: Beta"} {
		if strings.Contains(alphaContent, unwanted) {
			t.Fatalf("alpha request should not contain beta scope %q:\n%s", unwanted, alphaContent)
		}
	}
	for _, unwanted := range []string{"world:alpha", "creature:alpha", "display_name: Alpha"} {
		if strings.Contains(betaContent, unwanted) {
			t.Fatalf("beta request should not contain alpha scope %q:\n%s", unwanted, betaContent)
		}
	}
}

func TestHandleEventKeepsMemoryScopeSeparateForSharedDefinition(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{
		responses: []model.Response{
			{
				Decision: model.ModelDecision{
					ToolCalls: []model.ToolCall{{
						ID:        "call_alpha_1",
						Name:      "speak",
						Arguments: map[string]any{"text": "alpha-only-memory"},
					}},
					Control: model.ControlDirective{Kind: model.ControlSettle},
				},
			},
			{
				Decision: model.ModelDecision{
					Control: model.ControlDirective{Kind: model.ControlSettle},
				},
			},
			{
				Decision: model.ModelDecision{
					Control: model.ControlDirective{Kind: model.ControlSettle},
				},
			},
		},
	}
	catalog, err := definition.NewCatalog(
		[]definition.GameDefinition{{
			SchemaVersion: definition.SchemaVersionV1Alpha1,
			GameID:        "fake-game",
			Title:         "Fake Game",
		}},
		[]definition.AgentDefinition{{
			SchemaVersion: definition.SchemaVersionV1Alpha1,
			GameID:        "fake-game",
			DefinitionID:  "villager/farmer",
			Identity:      "A reusable farmer archetype.",
		}},
	)
	if err != nil {
		t.Fatalf("NewCatalog returned error: %v", err)
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig(), agent.WithDefinitionCatalog(catalog))
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	alphaKey := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:shared", EntityID: "creature:alpha"}
	betaKey := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:shared", EntityID: "creature:beta"}
	alphaTarget := &protocolv1alpha2.EntityRef{
		EntityId:     alphaKey.EntityID,
		EntityType:   "creature",
		DisplayName:  "Alpha",
		DefinitionId: "villager/farmer",
	}
	betaTarget := &protocolv1alpha2.EntityRef{
		EntityId:     betaKey.EntityID,
		EntityType:   "creature",
		DisplayName:  "Beta",
		DefinitionId: "villager/farmer",
	}

	if err := loop.HandleEvent(context.Background(), env, conn, alphaKey, alphaTarget, registry, gameEvent("event_alpha_1", alphaKey)); err != nil {
		t.Fatalf("first alpha HandleEvent returned error: %v", err)
	}
	if err := loop.HandleEvent(context.Background(), env, conn, betaKey, betaTarget, registry, gameEvent("event_beta_1", betaKey)); err != nil {
		t.Fatalf("beta HandleEvent returned error: %v", err)
	}
	if err := loop.HandleEvent(context.Background(), env, conn, alphaKey, alphaTarget, registry, gameEvent("event_alpha_2", alphaKey)); err != nil {
		t.Fatalf("second alpha HandleEvent returned error: %v", err)
	}

	if len(provider.requests) != 3 {
		t.Fatalf("provider request count = %d, want 3", len(provider.requests))
	}
	betaContent := provider.requests[1].Messages[0].Content
	if strings.Contains(betaContent, "alpha-only-memory") {
		t.Fatalf("beta request should not contain alpha memory:\n%s", betaContent)
	}
	secondAlphaContent := provider.requests[2].Messages[0].Content
	if !strings.Contains(secondAlphaContent, "alpha-only-memory") {
		t.Fatalf("second alpha request missing alpha memory:\n%s", secondAlphaContent)
	}
}

func TestHandleEventRendersDifferentBundledStardewDefinitions(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &recordingProvider{
		response: model.Response{
			Decision: model.ModelDecision{
				Control: model.ControlDirective{Kind: model.ControlSettle, Reason: "done"},
			},
		},
	}
	catalog, err := definition.LoadCatalogFromDir(filepath.Join("..", "..", "config", "games"))
	if err != nil {
		t.Fatalf("LoadCatalogFromDir returned error: %v", err)
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig(), agent.WithDefinitionCatalog(catalog))
	conn := agent.ConnectionContext{GameID: "stardew-valley", SessionID: "session:test"}
	abigailKey := session.AgentSessionKey{GameID: conn.GameID, WorldID: "farm:one", EntityID: "npc:Abigail"}
	linusKey := session.AgentSessionKey{GameID: conn.GameID, WorldID: "farm:one", EntityID: "npc:Linus"}
	abigailTarget := &protocolv1alpha2.EntityRef{
		EntityId:     abigailKey.EntityID,
		EntityType:   "npc",
		DisplayName:  "Abigail",
		DefinitionId: "npc:Abigail",
	}
	linusTarget := &protocolv1alpha2.EntityRef{
		EntityId:     linusKey.EntityID,
		EntityType:   "npc",
		DisplayName:  "Linus",
		DefinitionId: "npc:Linus",
	}

	if err := loop.HandleEvent(context.Background(), env, conn, abigailKey, abigailTarget, registry, gameEvent("event_abigail", abigailKey)); err != nil {
		t.Fatalf("Abigail HandleEvent returned error: %v", err)
	}
	if err := loop.HandleEvent(context.Background(), env, conn, linusKey, linusTarget, registry, gameEvent("event_linus", linusKey)); err != nil {
		t.Fatalf("Linus HandleEvent returned error: %v", err)
	}

	if len(provider.requests) != 2 {
		t.Fatalf("provider request count = %d, want 2", len(provider.requests))
	}
	abigailContent := provider.requests[0].Messages[0].Content
	linusContent := provider.requests[1].Messages[0].Content
	assertRequestContentContains(t, abigailContent,
		"title: Stardew Valley",
		"identity: Abigail is a young Pelican Town resident with a taste for adventure, music, games, and the unusual.",
		"entity_id: npc:Abigail",
		"display_name: Abigail",
		"definition_id: npc:Abigail",
	)
	assertRequestContentContains(t, linusContent,
		"title: Stardew Valley",
		"identity: Linus is a self-reliant mountain dweller who lives close to nature and values his chosen independence.",
		"entity_id: npc:Linus",
		"display_name: Linus",
		"definition_id: npc:Linus",
	)
	for _, unwanted := range []string{"identity: Linus is", "entity_id: npc:Linus", "display_name: Linus"} {
		if strings.Contains(abigailContent, unwanted) {
			t.Fatalf("Abigail request should not contain Linus projection %q:\n%s", unwanted, abigailContent)
		}
	}
	for _, unwanted := range []string{"identity: Abigail is", "entity_id: npc:Abigail", "display_name: Abigail"} {
		if strings.Contains(linusContent, unwanted) {
			t.Fatalf("Linus request should not contain Abigail projection %q:\n%s", unwanted, linusContent)
		}
	}
}

func TestHandleEventDefaultStoreRetainsAtLeastRecentMemoryLimit(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &recordingProvider{}
	config := agent.DefaultConfig()
	config.RecentMemoryLimit = 25
	config.MaxRecentMemoryTokens = 65536
	config.MaxRecentMemoryTokens = 65536
	config.MaxRequestTokens = 262144
	config.MaxUserMessageTokens = 262144
	loop := agent.NewLoop(provider, recorder, config)
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	for i := 1; i <= 26; i++ {
		if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent(fmt.Sprintf("event_%02d", i), key)); err != nil {
			t.Fatalf("HandleEvent(%d) returned error: %v", i, err)
		}
	}

	if len(provider.requests) != 26 {
		t.Fatalf("provider request count = %d, want 26", len(provider.requests))
	}
	lastContent := provider.requests[25].Messages[0].Content
	if got := strings.Count(lastContent, "remember this line"); got != 25 {
		t.Fatalf("default memory store should retain recent_memory_limit records; rendered memory count = %d, want 25:\n%s", got, lastContent)
	}
}

func TestHandleEventSkipsMemoryWhenDisabled(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &recordingProvider{}
	config := agent.DefaultConfig()
	config.MemoryEnabled = boolPtr(false)
	loop := agent.NewLoop(provider, recorder, config)
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("first HandleEvent returned error: %v", err)
	}
	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_2", key)); err != nil {
		t.Fatalf("second HandleEvent returned error: %v", err)
	}

	if len(provider.requests) != 2 {
		t.Fatalf("provider request count = %d, want 2", len(provider.requests))
	}
	secondContent := provider.requests[1].Messages[0].Content
	if strings.Contains(secondContent, "remember this line") {
		t.Fatalf("second request contains memory while memory disabled:\n%s", secondContent)
	}
	assertTraceContains(t, recorder.events, trace.EventContextLoaded)
	assertTraceNotContains(t, recorder.events, trace.EventContextUpdated)
}

func TestWithMemoryStoreNilDoesNotDisableDefaultMemoryStore(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &recordingProvider{}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig(), agent.WithMemoryStore(nil))
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("first HandleEvent returned error: %v", err)
	}
	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_2", key)); err != nil {
		t.Fatalf("second HandleEvent returned error: %v", err)
	}

	if len(provider.requests) != 2 {
		t.Fatalf("provider request count = %d, want 2", len(provider.requests))
	}
	secondContent := provider.requests[1].Messages[0].Content
	if !strings.Contains(secondContent, `tool "speak" status "ACTION_STATUS_SUCCEEDED" arguments {"text":"remember this line"}`) {
		t.Fatalf("nil memory store option should keep default store; second request:\n%s", secondContent)
	}
}

func TestHandleEventFailOpenWhenMemoryLoadFails(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &recordingProvider{}
	store := &failRecentStore{}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig(), agent.WithMemoryStore(store))
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	if len(provider.requests) != 1 {
		t.Fatalf("provider request count = %d, want 1", len(provider.requests))
	}
	if len(store.appended) != 1 {
		t.Fatalf("append count = %d, want 1", len(store.appended))
	}
	assertTraceContains(t, recorder.events, trace.EventContextLoadFailed)
	assertTraceContains(t, recorder.events, trace.EventContextUpdated)
	assertTraceContains(t, recorder.events, trace.EventTurnCompleted)
}

func TestHandleEventCompletesWhenMemoryAppendFails(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &recordingProvider{}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig(), agent.WithMemoryStore(failAppendStore{}))
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	assertTraceContains(t, recorder.events, trace.EventContextUpdateFailed)
	assertTraceContains(t, recorder.events, trace.EventTurnCompleted)
}

func TestHandleEventCompletesWhenMemoryProjectionFails(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &recordingProvider{}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig(), agent.WithMemoryProjector(failProjector{}))
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	assertTraceContains(t, recorder.events, trace.EventContextUpdateFailed)
	assertTraceContains(t, recorder.events, trace.EventTurnCompleted)
}

func assertTraceContains(t *testing.T, events []trace.Event, want trace.EventName) {
	t.Helper()

	for _, event := range events {
		if event.Event == want {
			return
		}
	}
	t.Fatalf("trace missing event %q; got %+v", want, events)
}

func assertTraceNotContains(t *testing.T, events []trace.Event, unwanted trace.EventName) {
	t.Helper()

	for _, event := range events {
		if event.Event == unwanted {
			t.Fatalf("trace unexpectedly contains event %q; got %+v", unwanted, events)
		}
	}
}

func assertContextBuildFailedReasons(t *testing.T, events []trace.Event, want []string) {
	t.Helper()

	for _, event := range events {
		if event.Event != trace.EventContextRequestBuildFailed {
			continue
		}
		got, ok := event.Fields["reason_codes"].([]string)
		if !ok {
			t.Fatalf("reason_codes = %#v, want []string", event.Fields["reason_codes"])
		}
		for _, reason := range want {
			if !stringSliceContains(got, reason) {
				t.Fatalf("reason_codes = %v, want %q", got, reason)
			}
		}
		return
	}
	t.Fatalf("trace missing event %q; got %+v", trace.EventContextRequestBuildFailed, events)
}

func assertTraceToolAdmissionSummary(t *testing.T, fields trace.Fields, droppedCount int) {
	t.Helper()

	names, ok := fields["dropped_tool_names"].([]string)
	if !ok {
		t.Fatalf("dropped_tool_names = %#v, want []string", fields["dropped_tool_names"])
	}
	if len(names) != tool.MaxToolAdmissionDiagnosticNames {
		t.Fatalf("dropped_tool_names length = %d, want %d", len(names), tool.MaxToolAdmissionDiagnosticNames)
	}
	truncated, ok := fields["dropped_tool_names_truncated_count"].(int)
	if !ok {
		t.Fatalf("dropped_tool_names_truncated_count = %#v, want int", fields["dropped_tool_names_truncated_count"])
	}
	if truncated != droppedCount-tool.MaxToolAdmissionDiagnosticNames {
		t.Fatalf("dropped_tool_names_truncated_count = %d, want %d", truncated, droppedCount-tool.MaxToolAdmissionDiagnosticNames)
	}
	reasons, ok := fields["dropped_tool_reason_counts"].(map[string]int)
	if !ok {
		t.Fatalf("dropped_tool_reason_counts = %#v, want map[string]int", fields["dropped_tool_reason_counts"])
	}
	if reasons[tool.ToolDropReasonCountExceeded] != droppedCount {
		t.Fatalf("dropped_tool_reason_counts[%s] = %d, want %d", tool.ToolDropReasonCountExceeded, reasons[tool.ToolDropReasonCountExceeded], droppedCount)
	}
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func assertTraceContainsInOrder(t *testing.T, events []trace.Event, want []trace.EventName) {
	t.Helper()

	next := 0
	for _, event := range events {
		if next < len(want) && event.Event == want[next] {
			next++
		}
	}
	if next != len(want) {
		t.Fatalf("trace did not contain ordered events %v; got %+v", want, events)
	}
}

func traceEventsByName(events []trace.Event, name trace.EventName) []trace.Event {
	out := make([]trace.Event, 0)
	for _, event := range events {
		if event.Event == name {
			out = append(out, event)
		}
	}
	return out
}

func requireSingleTurnCompletion(t *testing.T, completions []*protocolv1alpha2.TurnCompletion) *protocolv1alpha2.TurnCompletion {
	t.Helper()

	if len(completions) != 1 {
		t.Fatalf("turn completion count = %d, want 1", len(completions))
	}
	completion := completions[0]
	if completion == nil {
		t.Fatal("turn completion is nil")
	}
	if completion.TurnId == "" {
		t.Fatal("turn completion turn_id is empty")
	}
	return completion
}

func assertTurnCompletionScope(t *testing.T, completion *protocolv1alpha2.TurnCompletion, eventID string, key session.AgentSessionKey) {
	t.Helper()

	if completion.EventId != eventID {
		t.Fatalf("completion event_id = %q, want %q", completion.EventId, eventID)
	}
	if completion.WorldId != key.WorldID {
		t.Fatalf("completion world_id = %q, want %q", completion.WorldId, key.WorldID)
	}
	if completion.EntityId != key.EntityID {
		t.Fatalf("completion entity_id = %q, want %q", completion.EntityId, key.EntityID)
	}
}

func traceEventCount(events []trace.Event, name trace.EventName) int {
	count := 0
	for _, event := range events {
		if event.Event == name {
			count++
		}
	}
	return count
}

func indexOfTrace(events []trace.Event, name trace.EventName) int {
	for i, event := range events {
		if event.Event == name {
			return i
		}
	}
	return -1
}

func requestMessagesContain(messages []model.Message, needle string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, needle) {
			return true
		}
	}
	return false
}

func assertRequestContentContains(t *testing.T, content string, values ...string) {
	t.Helper()

	for _, want := range values {
		if !strings.Contains(content, want) {
			t.Fatalf("request content missing %q:\n%s", want, content)
		}
	}
}

func newSpeakRegistry() *tool.EnvironmentToolCatalog {
	return environmentCatalogFromCapabilities([]*protocolv1alpha2.Capability{
		{
			Name:            "speak",
			Description:     "Make the NPC speak.",
			InputSchemaJson: `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`,
			ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_SYNC,
		},
	})
}

func mustEnvironmentToolCatalog(t *testing.T, capabilities []*protocolv1alpha2.Capability) *tool.EnvironmentToolCatalog {
	t.Helper()

	catalog, diagnostics, err := tool.BuildEnvironmentToolCatalog(&protocolv1alpha2.CapabilityList{
		Capabilities: capabilities,
	})
	if err != nil {
		t.Fatalf("BuildEnvironmentToolCatalog returned error: %v", err)
	}
	if diagnostics.CatalogToolCount != len(catalog.Available()) {
		t.Fatalf("CatalogToolCount = %d, Available count = %d", diagnostics.CatalogToolCount, len(catalog.Available()))
	}
	return catalog
}

func newParallelSpeakRegistry() *tool.EnvironmentToolCatalog {
	return environmentCatalogFromCapabilities([]*protocolv1alpha2.Capability{
		{
			Name:            "speak",
			Description:     "Make the NPC speak.",
			InputSchemaJson: `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`,
			ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_SYNC,
			ConcurrencyMode: protocolv1alpha2.CapabilityConcurrencyMode_CAPABILITY_CONCURRENCY_MODE_PARALLEL_SAFE,
		},
	})
}

func newSpeakEmoteRegistry() *tool.EnvironmentToolCatalog {
	return environmentCatalogFromCapabilities([]*protocolv1alpha2.Capability{
		{
			Name:            "speak",
			Description:     "Make the NPC speak.",
			InputSchemaJson: `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`,
			ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_SYNC,
		},
		{
			Name:            "emote",
			Description:     "Make the NPC emote.",
			InputSchemaJson: `{"type":"object","properties":{"emote":{"type":"string"}},"required":["emote"]}`,
			ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_SYNC,
		},
	})
}

func newSpeakAskPlayerRegistry() *tool.EnvironmentToolCatalog {
	return environmentCatalogFromCapabilities([]*protocolv1alpha2.Capability{
		{
			Name:            "speak",
			Description:     "Make the NPC speak.",
			InputSchemaJson: `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`,
			ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_SYNC,
		},
		{
			Name:            "ask_player",
			Description:     "Show a player-facing prompt and wait for a later player response event.",
			InputSchemaJson: `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`,
			ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_SYNC,
			Extensions:      loopToolPolicyExtensions(true, true),
		},
	})
}

func newMoveToRegistry() *tool.EnvironmentToolCatalog {
	return newMoveToRegistryWithPolicy(tool.ToolPolicy{})
}

func newMoveToRegistryWithPolicy(policy tool.ToolPolicy) *tool.EnvironmentToolCatalog {
	capability := &protocolv1alpha2.Capability{
		Name:            "move_to",
		Description:     "Move the NPC to a target location and tile.",
		InputSchemaJson: `{"type":"object","properties":{"label":{"type":"string"}},"required":["label"],"additionalProperties":false}`,
		ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_ASYNC,
		ConcurrencyMode: protocolv1alpha2.CapabilityConcurrencyMode_CAPABILITY_CONCURRENCY_MODE_SEQUENTIAL,
	}
	if policy.ExclusivePerStep || policy.SettleAfterSuccess {
		capability.Extensions = loopToolPolicyExtensions(policy.ExclusivePerStep, policy.SettleAfterSuccess)
	}
	return environmentCatalogFromCapabilities([]*protocolv1alpha2.Capability{capability})
}

func loopToolPolicyExtensions(exclusivePerStep bool, settleAfterSuccess bool) *structpb.Struct {
	extensions, err := structpb.NewStruct(map[string]any{
		"gameagent": map[string]any{
			"tool_policy": map[string]any{
				"exclusive_per_step":   exclusivePerStep,
				"settle_after_success": settleAfterSuccess,
			},
		},
	})
	if err != nil {
		panic(err)
	}
	return extensions
}

func newParallelSenseRegistry() *tool.EnvironmentToolCatalog {
	return newSenseRegistry(protocolv1alpha2.CapabilityConcurrencyMode_CAPABILITY_CONCURRENCY_MODE_PARALLEL_SAFE)
}

func newSequentialSenseRegistry() *tool.EnvironmentToolCatalog {
	return newSenseRegistry(protocolv1alpha2.CapabilityConcurrencyMode_CAPABILITY_CONCURRENCY_MODE_SEQUENTIAL)
}

func newSenseRegistry(concurrencyMode protocolv1alpha2.CapabilityConcurrencyMode) *tool.EnvironmentToolCatalog {
	return environmentCatalogFromCapabilities([]*protocolv1alpha2.Capability{
		{
			Name:            "sense",
			Description:     "Read local environment state.",
			InputSchemaJson: `{"type":"object","properties":{"label":{"type":"string"}}}`,
			ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_SYNC,
			ConcurrencyMode: concurrencyMode,
		},
	})
}

func environmentCatalogFromCapabilities(capabilities []*protocolv1alpha2.Capability) *tool.EnvironmentToolCatalog {
	catalog, _, err := tool.BuildEnvironmentToolCatalog(&protocolv1alpha2.CapabilityList{
		Capabilities: capabilities,
	})
	if err != nil {
		panic(err)
	}
	return catalog
}

func gameEvent(eventID string, key session.AgentSessionKey) *protocolv1alpha2.GameEvent {
	return &protocolv1alpha2.GameEvent{
		EventId:        eventID,
		EventType:      "player_interacted_with_npc",
		WorldId:        key.WorldID,
		TargetEntityId: key.EntityID,
		Entities: []*protocolv1alpha2.EntityRef{
			{EntityId: key.EntityID, DefinitionId: key.EntityID},
		},
	}
}

func entityTarget(key session.AgentSessionKey) *protocolv1alpha2.EntityRef {
	return &protocolv1alpha2.EntityRef{
		EntityId:     key.EntityID,
		DefinitionId: key.EntityID,
	}
}

func playerUtteranceEvent(eventID string, key session.AgentSessionKey, sequence uint64, text string) *protocolv1alpha2.GameEvent {
	event := gameEvent(eventID, key)
	event.EventType = "player_said_to_npc"
	event.Sequence = sequence
	event.ContextFacts = []*protocolv1alpha2.ContextFact{{
		Kind:           "utterance",
		ActorEntityId:  "player:local",
		TargetEntityId: key.EntityID,
		ScopeId:        "conv_1",
		Text:           text,
	}}
	return event
}

func observationWithState(worldID string, entityID string, marker string) *protocolv1alpha2.Observation {
	state, err := structpb.NewStruct(map[string]any{
		"marker": marker,
	})
	if err != nil {
		panic(err)
	}
	return &protocolv1alpha2.Observation{
		EntityId: entityID,
		WorldId:  worldID,
		State:    state,
	}
}

func (f *fakeEnvironment) SubmitAction(ctx context.Context, req *protocolv1alpha2.ActionRequest) (*protocolv1alpha2.ActionResult, error) {
	f.submittedAction = req
	f.submittedActions = append(f.submittedActions, req)

	status := protocolv1alpha2.ActionStatus_ACTION_STATUS_SUCCEEDED
	if configured := f.statusByTool[req.Capability]; configured != protocolv1alpha2.ActionStatus_ACTION_STATUS_UNSPECIFIED {
		status = configured
	}

	return &protocolv1alpha2.ActionResult{
		ActionId: req.ActionId,
		Status:   status,
		Error: &protocolv1alpha2.Error{
			Code:    "adapter_" + strings.ToLower(strings.TrimPrefix(status.String(), "ACTION_STATUS_")),
			Message: "adapter returned " + status.String(),
		},
	}, nil
}

func (f *fakeEnvironment) StartAction(ctx context.Context, req *protocolv1alpha2.ActionRequest) (agent.ActionStart, error) {
	f.submittedAction = req
	f.submittedActions = append(f.submittedActions, req)
	if f.asyncActionTools == nil {
		f.asyncActionTools = make(map[string]string)
	}
	f.asyncActionTools[req.GetActionId()] = req.GetCapability()

	status := protocolv1alpha2.ActionStatus_ACTION_STATUS_ACCEPTED
	if configured := f.startStatusByTool[req.GetCapability()]; configured != protocolv1alpha2.ActionStatus_ACTION_STATUS_UNSPECIFIED {
		status = configured
	}

	return agent.ActionStart{Update: &protocolv1alpha2.ActionStatusUpdate{
		ActionId: req.GetActionId(),
		Status:   status,
	}}, nil
}

func (f *fakeEnvironment) WaitActionResult(ctx context.Context, actionID string) (*protocolv1alpha2.ActionResult, error) {
	toolName := f.asyncActionTools[actionID]
	if delay := f.waitDelayByTool[toolName]; delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			f.CancelAction(actionID, "async_action_timeout")
			return nil, ctx.Err()
		}
	}
	if err := f.waitErrorsByTool[toolName]; err != nil {
		return nil, err
	}

	status := protocolv1alpha2.ActionStatus_ACTION_STATUS_SUCCEEDED
	if configured := f.statusByTool[toolName]; configured != protocolv1alpha2.ActionStatus_ACTION_STATUS_UNSPECIFIED {
		status = configured
	}

	return &protocolv1alpha2.ActionResult{
		ActionId: actionID,
		Status:   status,
		Error: &protocolv1alpha2.Error{
			Code:    "adapter_" + strings.ToLower(strings.TrimPrefix(status.String(), "ACTION_STATUS_")),
			Message: "adapter returned " + status.String(),
		},
	}, nil
}

func (f *fakeEnvironment) CancelAction(actionID string, reason string) {
	f.cancelledActions = append(f.cancelledActions, actionID+":"+reason)
}

func (f *fakeEnvironment) SendTurnCompletion(ctx context.Context, completion *protocolv1alpha2.TurnCompletion) error {
	f.turnCompletions = append(f.turnCompletions, completion)
	return f.completeErr
}

func (e *technicalActionEnvironment) Observe(ctx context.Context, worldID string, entityID string) (*protocolv1alpha2.Observation, error) {
	return &protocolv1alpha2.Observation{WorldId: worldID, EntityId: entityID}, nil
}

func (e *technicalActionEnvironment) SubmitAction(ctx context.Context, req *protocolv1alpha2.ActionRequest) (*protocolv1alpha2.ActionResult, error) {
	label := actionRequestLabel(req)
	e.mu.Lock()
	e.submittedActions = append(e.submittedActions, req)
	e.mu.Unlock()

	if delay := e.delays[label]; delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := e.submitErrors[label]; err != nil {
		return nil, err
	}
	return &protocolv1alpha2.ActionResult{
		ActionId: req.GetActionId(),
		Status:   protocolv1alpha2.ActionStatus_ACTION_STATUS_SUCCEEDED,
	}, nil
}

func (e *technicalActionEnvironment) StartAction(ctx context.Context, req *protocolv1alpha2.ActionRequest) (agent.ActionStart, error) {
	return agent.ActionStart{}, errors.New("StartAction is not implemented for sync loop tests")
}

func (e *technicalActionEnvironment) WaitActionResult(ctx context.Context, actionID string) (*protocolv1alpha2.ActionResult, error) {
	return nil, errors.New("WaitActionResult is not implemented for sync loop tests")
}

func (e *technicalActionEnvironment) CancelAction(actionID string, reason string) {}

func (e *technicalActionEnvironment) SendTurnCompletion(ctx context.Context, completion *protocolv1alpha2.TurnCompletion) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.turnCompletions = append(e.turnCompletions, completion)
	return nil
}

func actionRequestLabel(req *protocolv1alpha2.ActionRequest) string {
	if req.GetArguments() == nil {
		return req.GetCapability()
	}
	value := req.GetArguments().GetFields()["label"]
	if value == nil {
		value = req.GetArguments().GetFields()["text"]
	}
	if value == nil {
		return req.GetCapability()
	}
	label := strings.TrimSpace(value.GetStringValue())
	if label == "" {
		return req.GetCapability()
	}
	return label
}

func TestHandleEventRunsOneTurnNPCInteraction(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	loop := agent.NewLoop(fake.NewProvider(), recorder, agent.DefaultConfig())

	event := &protocolv1alpha2.GameEvent{
		EventId:        "event_1",
		EventType:      "player_interacted_with_npc",
		WorldId:        "world:test",
		TargetEntityId: "npc:Robin",
		Entities: []*protocolv1alpha2.EntityRef{
			{
				EntityId:     "player:local",
				EntityType:   "player",
				DefinitionId: "player:local",
			},
			{
				EntityId:     "npc:Linus",
				EntityType:   "npc",
				DefinitionId: "npc:Linus",
			},
			{
				EntityId:     "npc:Robin",
				EntityType:   "npc",
				DefinitionId: "npc:Robin",
			},
		},
	}

	conn := agent.ConnectionContext{
		GameID:    "fake-game",
		SessionID: "session:test",
	}

	key := session.AgentSessionKey{
		GameID:   conn.GameID,
		WorldID:  event.WorldId,
		EntityID: event.TargetEntityId,
	}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, event); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	if env.observedWorldID != "world:test" {
		t.Fatalf("observed world id = %q, want %q", env.observedWorldID, "world:test")
	}

	if env.observedEntityID != "npc:Robin" {
		t.Fatalf("observed entity id = %q, want %q", env.observedEntityID, "npc:Robin")
	}

	if env.submittedAction == nil {
		t.Fatal("expected action to be submitted")
	}

	if env.submittedAction.WorldId != "world:test" {
		t.Fatalf("submitted world id = %q, want %q", env.submittedAction.WorldId, "world:test")
	}

	if env.submittedAction.EntityId != "npc:Robin" {
		t.Fatalf("submitted entity id = %q, want %q", env.submittedAction.EntityId, "npc:Robin")
	}

	if env.submittedAction.Capability != "speak" {
		t.Fatalf("submitted capability = %q, want %q", env.submittedAction.Capability, "speak")
	}

	if env.submittedAction.ActionId == "" {
		t.Fatal("expected submitted action to have an action id")
	}

	textValue := env.submittedAction.Arguments.Fields["text"]
	if textValue == nil || textValue.GetStringValue() == "" {
		t.Fatal("expected submitted speak action to include non-empty text")
	}

	wantTimeline := []trace.EventName{
		trace.EventTurnStarted,
		trace.EventObservationRequested,
		trace.EventObservationReceived,
		trace.EventContextLoaded,
		trace.EventModelRequestStarted,
		trace.EventModelResponseReceived,
		trace.EventToolCallSelected,
		trace.EventActionSubmitStarted,
		trace.EventActionResultReceived,
		trace.EventContextUpdated,
		trace.EventTurnCompleted,
	}
	assertTraceContainsInOrder(t, recorder.events, wantTimeline)

	for i, got := range recorder.events {
		if got.Seq != uint32(i+1) {
			t.Fatalf("trace event[%d] seq = %d, want %d", i, got.Seq, i+1)
		}
		if got.TraceID != got.TurnID {
			t.Fatalf("trace event[%d] trace_id = %q, want turn_id %q", i, got.TraceID, got.TurnID)
		}
		if got.GameID != conn.GameID || got.SessionID != conn.SessionID || got.WorldID != event.WorldId || got.EventID != event.EventId || got.EventType != event.EventType || got.EntityID != "npc:Robin" {
			t.Fatalf("trace event[%d] context mismatch: %+v", i, got)
		}
	}

	terminal := recorder.events[len(recorder.events)-1]
	if terminal.ActionID == "" {
		t.Fatal("expected terminal event to include action id")
	}
	if terminal.Tool != "speak" {
		t.Fatalf("terminal tool = %q, want %q", terminal.Tool, "speak")
	}
}

func TestHandleEventExecutesSingleToolCallFromModelDecision(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &recordingProvider{
		response: model.Response{
			Decision: model.ModelDecision{
				ToolCalls: []model.ToolCall{{
					ID:        "call_1",
					Name:      "speak",
					Arguments: map[string]any{"text": "from decision"},
				}},
				Control: model.ControlDirective{Kind: model.ControlSettle},
			},
		},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	if env.submittedAction == nil {
		t.Fatal("expected action to be submitted")
	}
	if text := env.submittedAction.Arguments.Fields["text"].GetStringValue(); text != "from decision" {
		t.Fatalf("submitted text = %q, want from decision", text)
	}
	assertTraceContains(t, recorder.events, trace.EventToolCallSelected)
	assertTraceContains(t, recorder.events, trace.EventTurnCompleted)
}

func TestHandleEventCompletesOnSettleOnlyDecision(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &recordingProvider{
		response: model.Response{
			Decision: model.ModelDecision{
				Control: model.ControlDirective{Kind: model.ControlSettle, Reason: "idle"},
			},
		},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	if env.submittedAction != nil {
		t.Fatalf("settle-only decision submitted action: %+v", env.submittedAction)
	}
	assertTraceContains(t, recorder.events, trace.EventModelResponseReceived)
	assertTraceNotContains(t, recorder.events, trace.EventActionSubmitStarted)
	assertTraceContains(t, recorder.events, trace.EventTurnCompleted)
}

func TestHandleEventSendsTurnCompletionOnSettle(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{responses: []model.Response{
		{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}},
	}}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	completion := requireSingleTurnCompletion(t, env.turnCompletions)
	if completion.Status != protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED {
		t.Fatalf("completion status = %s, want completed", completion.Status)
	}
	assertTurnCompletionScope(t, completion, "event_1", key)
	if completion.Error != nil {
		t.Fatalf("completion error = %+v, want nil", completion.Error)
	}
	assertTraceContainsInOrder(t, recorder.events, []trace.EventName{
		trace.EventTurnCompletionSent,
		trace.EventTurnCompleted,
	})
}

func TestHandleEventSendsTurnCompletionOnFailure(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{responses: []model.Response{
		{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlContinue}}},
	}}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key))
	if err == nil {
		t.Fatal("HandleEvent returned nil error, want invalid model response")
	}

	completion := requireSingleTurnCompletion(t, env.turnCompletions)
	if completion.Status != protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_FAILED {
		t.Fatalf("completion status = %s, want failed", completion.Status)
	}
	assertTurnCompletionScope(t, completion, "event_1", key)
	if completion.Error == nil || completion.Error.Code != "invalid_model_response" {
		t.Fatalf("completion error = %+v, want invalid_model_response", completion.Error)
	}
	assertTraceContainsInOrder(t, recorder.events, []trace.EventName{
		trace.EventTurnCompletionSent,
		trace.EventTurnFailed,
	})
}

func TestTurnCompletionSendFailureDoesNotChangeCompletedTurnStatus(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{completeErr: errors.New("stream closed")}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{responses: []model.Response{
		{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}},
	}}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	completion := requireSingleTurnCompletion(t, env.turnCompletions)
	if completion.Status != protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED {
		t.Fatalf("completion status = %s, want completed", completion.Status)
	}
	assertTraceContains(t, recorder.events, trace.EventTurnCompletionSendFailed)
	assertTraceContains(t, recorder.events, trace.EventTurnCompleted)
	assertTraceNotContains(t, recorder.events, trace.EventTurnFailed)
}

func TestHandleEventActionRequestCarriesSourceCorrelation(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{responses: []model.Response{
		{
			Decision: model.ModelDecision{
				ToolCalls: []model.ToolCall{{ID: "call_1", Name: "speak", Arguments: map[string]any{"text": "hello"}}},
				Control:   model.ControlDirective{Kind: model.ControlSettle},
			},
		},
	}}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	if env.submittedAction == nil {
		t.Fatal("expected submitted action")
	}
	completion := requireSingleTurnCompletion(t, env.turnCompletions)
	if env.submittedAction.SourceEventId != "event_1" {
		t.Fatalf("SourceEventId = %q, want event_1", env.submittedAction.SourceEventId)
	}
	if env.submittedAction.SourceTurnId == "" {
		t.Fatal("SourceTurnId is empty")
	}
	if env.submittedAction.SourceTurnId != completion.TurnId {
		t.Fatalf("SourceTurnId = %q, want completion turn_id %q", env.submittedAction.SourceTurnId, completion.TurnId)
	}
}

func TestHandleEventWritesContextFactMemoryOnSettleOnlyDecision(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	store := &failRecentStore{}
	provider := &recordingProvider{
		response: model.Response{
			Decision: model.ModelDecision{
				Control: model.ControlDirective{Kind: model.ControlSettle, Reason: "heard player"},
			},
		},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig(), agent.WithMemoryStore(store))
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, playerUtteranceEvent("event_1", key, 43, "Let's go fishing."))
	if err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}
	if env.submittedAction != nil {
		t.Fatalf("settle-only decision submitted action: %+v", env.submittedAction)
	}
	if got := len(store.appended); got != 1 {
		t.Fatalf("appended memory count = %d, want 1", got)
	}
	record := store.appended[0]
	if got := len(record.SourceContextFacts); got != 1 {
		t.Fatalf("context fact count = %d, want 1", got)
	}
	if got := record.SourceContextFacts[0].Text; got != "Let's go fishing." {
		t.Fatalf("context fact text = %q, want player utterance", got)
	}
	if got := len(record.Outcomes); got != 0 {
		t.Fatalf("outcome count = %d, want 0", got)
	}
	assertTraceContains(t, recorder.events, trace.EventContextUpdated)
	assertTraceContains(t, recorder.events, trace.EventTurnCompleted)
}

func TestHandleEventRunsBatchToolCallsThenSettle(t *testing.T) {
	registry := newSpeakEmoteRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{
		responses: []model.Response{{
			Decision: model.ModelDecision{
				ToolCalls: []model.ToolCall{
					{ID: "call_1", Name: "speak", Arguments: map[string]any{"text": "one"}},
					{ID: "call_2", Name: "emote", Arguments: map[string]any{"emote": "happy"}},
				},
				Control: model.ControlDirective{Kind: model.ControlSettle},
			},
		}},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}
	if got := len(env.submittedActions); got != 2 {
		t.Fatalf("submitted action count = %d, want 2", got)
	}
	if env.submittedActions[0].Capability != "speak" || env.submittedActions[1].Capability != "emote" {
		t.Fatalf("submitted capabilities = %s, %s; want speak, emote", env.submittedActions[0].Capability, env.submittedActions[1].Capability)
	}
	if got := len(provider.requests); got != 1 {
		t.Fatalf("provider request count = %d, want 1 because settle completed same step", got)
	}
	assertTraceContains(t, recorder.events, trace.EventTurnCompleted)
}

func TestHandleEventRejectsExclusivePolicyToolMixedWithOtherToolCalls(t *testing.T) {
	registry := newSpeakAskPlayerRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{
		responses: []model.Response{
			{
				Decision: model.ModelDecision{
					ToolCalls: []model.ToolCall{
						{ID: "call_1", Name: "ask_player", Arguments: map[string]any{"text": "Choose one."}},
						{ID: "call_2", Name: "speak", Arguments: map[string]any{"text": "This would overwrite the menu."}},
					},
					Control: model.ControlDirective{Kind: model.ControlContinue},
				},
			},
			{
				Decision: model.ModelDecision{
					ToolCalls: []model.ToolCall{
						{ID: "call_3", Name: "ask_player", Arguments: map[string]any{"text": "Choose one."}},
					},
					Control: model.ControlDirective{Kind: model.ControlSettle},
				},
			},
		},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}
	if got := len(env.submittedActions); got != 1 {
		t.Fatalf("submitted action count = %d, want only retried exclusive policy tool", got)
	}
	if env.submittedActions[0].Capability != "ask_player" {
		t.Fatalf("submitted capability = %q, want ask_player", env.submittedActions[0].Capability)
	}
	if got := len(provider.requests); got != 2 {
		t.Fatalf("provider request count = %d, want retry after invalid exclusive policy batch", got)
	}
	if !requestMessagesContain(provider.requests[1].Messages, "exclusive_tool_must_be_only_tool_call") {
		t.Fatalf("retry request should explain exclusive policy batch validation; messages=%+v", provider.requests[1].Messages)
	}
	if requestMessagesContain(provider.requests[1].Messages, "present_dialogue_must_be_only_tool_call") {
		t.Fatalf("retry request should not expose old present_dialogue-specific code; messages=%+v", provider.requests[1].Messages)
	}
	assertTraceContains(t, recorder.events, trace.EventToolBatchFailed)
	assertTraceContains(t, recorder.events, trace.EventTurnCompleted)
}

func TestHandleEventSettlesAfterSuccessfulPolicyToolWithoutNextModelRequest(t *testing.T) {
	registry := newSpeakAskPlayerRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{
		responses: []model.Response{
			{
				Decision: model.ModelDecision{
					ToolCalls: []model.ToolCall{
						{ID: "call_1", Name: "ask_player", Arguments: map[string]any{"text": "Choose one."}},
					},
					Control: model.ControlDirective{Kind: model.ControlContinue},
				},
			},
			{
				Decision: model.ModelDecision{
					ToolCalls: []model.ToolCall{
						{ID: "call_2", Name: "speak", Arguments: map[string]any{"text": "Too early."}},
					},
					Control: model.ControlDirective{Kind: model.ControlSettle},
				},
			},
		},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}
	if got := len(env.submittedActions); got != 1 {
		t.Fatalf("submitted action count = %d, want only policy-settled action", got)
	}
	if env.submittedActions[0].Capability != "ask_player" {
		t.Fatalf("submitted capability = %q, want ask_player", env.submittedActions[0].Capability)
	}
	if got := len(provider.requests); got != 1 {
		t.Fatalf("provider request count = %d, want policy-settled action to complete the turn", got)
	}
	assertTraceContains(t, recorder.events, trace.EventTurnCompleted)
}

func TestHandleEventSuspendsResumesAndReobservesAfterAsyncAction(t *testing.T) {
	registry := newMoveToRegistry()
	env := &fakeEnvironment{observations: []*protocolv1alpha2.Observation{
		observationWithState("world:test", "npc:Abigail", "before_move"),
		observationWithState("world:test", "npc:Abigail", "after_move"),
	}}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{
		responses: []model.Response{
			{
				Decision: model.ModelDecision{
					ToolCalls: []model.ToolCall{{ID: "call_move", Name: "move_to", Arguments: map[string]any{"label": "town_square"}}},
					Control:   model.ControlDirective{Kind: model.ControlContinue},
				},
			},
			{
				Decision: model.ModelDecision{
					Control: model.ControlDirective{Kind: model.ControlSettle},
				},
			},
		},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	if got := env.observeCount; got != 2 {
		t.Fatalf("observe count = %d, want initial observe plus async resume re-observe", got)
	}
	if got := len(provider.requests); got != 2 {
		t.Fatalf("provider request count = %d, want resumed settle step", got)
	}
	if !requestMessagesContain(provider.requests[1].Messages, "action_succeeded") {
		t.Fatalf("resumed request missing async terminal ToolResult transcript: %+v", provider.requests[1].Messages)
	}
	if !requestMessagesContain(provider.requests[1].Messages, "after_move") {
		t.Fatalf("resumed request missing re-observed state: %+v", provider.requests[1].Messages)
	}
	if got := len(env.submittedActions); got != 1 {
		t.Fatalf("submitted action count = %d, want 1", got)
	}
	if env.submittedActions[0].SourceEventId != "event_1" {
		t.Fatalf("SourceEventId = %q, want event_1", env.submittedActions[0].SourceEventId)
	}
	if env.submittedActions[0].SourceTurnId == "" {
		t.Fatal("SourceTurnId is empty")
	}
	assertTraceContainsInOrder(t, recorder.events, []trace.EventName{
		trace.EventActionStatusUpdateReceived,
		trace.EventTurnSuspended,
		trace.EventActionResultReceived,
		trace.EventTurnResumed,
		trace.EventObservationRequested,
		trace.EventObservationReceived,
		trace.EventTurnCompleted,
	})
}

func TestHandleEventAsyncSuccessContinuesToResumeStepBeforeSettling(t *testing.T) {
	for _, tc := range []struct {
		name     string
		registry *tool.EnvironmentToolCatalog
		control  model.ControlKind
	}{
		{
			name:     "same_step_control_settle",
			registry: newMoveToRegistry(),
			control:  model.ControlSettle,
		},
		{
			name:     "settle_after_success_policy",
			registry: newMoveToRegistryWithPolicy(tool.ToolPolicy{SettleAfterSuccess: true}),
			control:  model.ControlContinue,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := &fakeEnvironment{observations: []*protocolv1alpha2.Observation{
				observationWithState("world:test", "npc:Abigail", "before_move"),
				observationWithState("world:test", "npc:Abigail", "after_move"),
			}}
			recorder := &recordingTraceRecorder{}
			provider := &scriptedProvider{
				responses: []model.Response{
					{
						Decision: model.ModelDecision{
							ToolCalls: []model.ToolCall{{ID: "call_move", Name: "move_to", Arguments: map[string]any{"label": "town_square"}}},
							Control:   model.ControlDirective{Kind: tc.control},
						},
					},
					{
						Decision: model.ModelDecision{
							Control: model.ControlDirective{Kind: model.ControlSettle},
						},
					},
				},
			}
			loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
			conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
			key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

			if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), tc.registry, gameEvent("event_1", key)); err != nil {
				t.Fatalf("HandleEvent returned error: %v", err)
			}

			if got := env.observeCount; got != 2 {
				t.Fatalf("observe count = %d, want initial observe plus async resume re-observe", got)
			}
			if got := len(provider.requests); got != 2 {
				t.Fatalf("provider request count = %d, want resumed settle step", got)
			}
			if !requestMessagesContain(provider.requests[1].Messages, "action_succeeded") {
				t.Fatalf("resumed request missing async ToolResult transcript: %+v", provider.requests[1].Messages)
			}
			assertTraceContainsInOrder(t, recorder.events, []trace.EventName{
				trace.EventTurnSuspended,
				trace.EventTurnResumed,
				trace.EventTurnCompleted,
			})
		})
	}
}

func TestHandleEventRejectsSecondAsyncActionPerTurn(t *testing.T) {
	registry := newMoveToRegistry()
	env := &fakeEnvironment{observations: []*protocolv1alpha2.Observation{
		observationWithState("world:test", "npc:Abigail", "before_first_move"),
		observationWithState("world:test", "npc:Abigail", "after_first_move"),
	}}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{
		responses: []model.Response{
			{
				Decision: model.ModelDecision{
					ToolCalls: []model.ToolCall{{ID: "call_move_1", Name: "move_to", Arguments: map[string]any{"label": "first"}}},
					Control:   model.ControlDirective{Kind: model.ControlContinue},
				},
			},
			{
				Decision: model.ModelDecision{
					ToolCalls: []model.ToolCall{{ID: "call_move_2", Name: "move_to", Arguments: map[string]any{"label": "second"}}},
					Control:   model.ControlDirective{Kind: model.ControlContinue},
				},
			},
			{
				Decision: model.ModelDecision{
					Control: model.ControlDirective{Kind: model.ControlSettle},
				},
			},
		},
	}
	config := agent.DefaultConfig()
	config.MaxAsyncActionsPerTurn = 1
	loop := agent.NewLoop(provider, recorder, config)
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	if got := len(env.submittedActions); got != 1 {
		t.Fatalf("submitted action count = %d, want only first async action started", got)
	}
	if got := len(provider.requests); got != 3 {
		t.Fatalf("provider request count = %d, want retry after async limit feedback", got)
	}
	if !requestMessagesContain(provider.requests[2].Messages, "async_action_limit_exceeded") {
		t.Fatalf("third request missing async limit feedback: %+v", provider.requests[2].Messages)
	}
	assertTraceContains(t, recorder.events, trace.EventToolBatchFailed)
	assertTraceContains(t, recorder.events, trace.EventTurnCompleted)
}

func TestHandleEventFailsTurnWhenReobserveFailsAfterAsyncSuccess(t *testing.T) {
	registry := newMoveToRegistry()
	env := &fakeEnvironment{
		observeErrors: []error{nil, errors.New("adapter observe closed")},
	}
	recorder := &recordingTraceRecorder{}
	store := &failRecentStore{}
	provider := &scriptedProvider{
		responses: []model.Response{
			{
				Decision: model.ModelDecision{
					ToolCalls: []model.ToolCall{{ID: "call_move", Name: "move_to", Arguments: map[string]any{"label": "town_square"}}},
					Control:   model.ControlDirective{Kind: model.ControlContinue},
				},
			},
			{
				Decision: model.ModelDecision{
					Control: model.ControlDirective{Kind: model.ControlSettle},
				},
			},
		},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig(), agent.WithMemoryStore(store))
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key))
	if err == nil || !strings.Contains(err.Error(), "adapter observe closed") {
		t.Fatalf("HandleEvent error = %v, want re-observe failure", err)
	}

	if got := env.observeCount; got != 2 {
		t.Fatalf("observe count = %d, want initial observe and failed re-observe", got)
	}
	if got := len(provider.requests); got != 1 {
		t.Fatalf("provider request count = %d, want no stale-observation continuation", got)
	}
	if got := len(store.appended); got != 1 {
		t.Fatalf("appended memory count = %d, want successful async outcome recorded", got)
	}
	if got := len(store.appended[0].Outcomes); got != 1 {
		t.Fatalf("memory outcome count = %d, want 1", got)
	}
	completion := requireSingleTurnCompletion(t, env.turnCompletions)
	if completion.Status != protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_FAILED {
		t.Fatalf("completion status = %s, want failed", completion.Status)
	}
	assertTraceContainsInOrder(t, recorder.events, []trace.EventName{
		trace.EventTurnSuspended,
		trace.EventTurnResumed,
		trace.EventObservationRequested,
		trace.EventTurnFailed,
	})
}

func TestHandleEventAsyncTerminalFailureFeedsNextStep(t *testing.T) {
	registry := newMoveToRegistry()
	env := &fakeEnvironment{
		statusByTool: map[string]protocolv1alpha2.ActionStatus{
			"move_to": protocolv1alpha2.ActionStatus_ACTION_STATUS_REJECTED,
		},
		observations: []*protocolv1alpha2.Observation{
			observationWithState("world:test", "npc:Abigail", "before_move"),
			observationWithState("world:test", "npc:Abigail", "after_rejected_move"),
		},
	}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{
		responses: []model.Response{
			{
				Decision: model.ModelDecision{
					ToolCalls: []model.ToolCall{{ID: "call_move", Name: "move_to", Arguments: map[string]any{"label": "blocked"}}},
					Control:   model.ControlDirective{Kind: model.ControlSettle},
				},
			},
			{
				Decision: model.ModelDecision{
					Control: model.ControlDirective{Kind: model.ControlSettle},
				},
			},
		},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	if got := env.observeCount; got != 2 {
		t.Fatalf("observe count = %d, want re-observe after async terminal failure", got)
	}
	if got := len(provider.requests); got != 2 {
		t.Fatalf("provider request count = %d, want retry step", got)
	}
	if !requestMessagesContain(provider.requests[1].Messages, "adapter_rejected") {
		t.Fatalf("retry request missing rejected async ToolResult: %+v", provider.requests[1].Messages)
	}
	assertTraceContains(t, recorder.events, trace.EventTurnCompleted)
}

func TestHandleEventRunsMultipleStepsUntilSettle(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{
		responses: []model.Response{
			{
				Decision: model.ModelDecision{
					ToolCalls: []model.ToolCall{{ID: "call_1", Name: "speak", Arguments: map[string]any{"text": "first step"}}},
					Control:   model.ControlDirective{Kind: model.ControlContinue},
				},
			},
			{
				Decision: model.ModelDecision{
					Control: model.ControlDirective{Kind: model.ControlSettle},
				},
			},
		},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	if got := len(provider.requests); got != 2 {
		t.Fatalf("provider request count = %d, want 2", got)
	}
	if got := len(provider.requests[1].Messages); got != 3 {
		t.Fatalf("second request message count = %d, want user context plus tool transcript", got)
	}
	if provider.requests[1].Messages[1].Role != model.RoleAssistant || provider.requests[1].Messages[2].Role != model.RoleTool {
		t.Fatalf("second request transcript roles = %+v", provider.requests[1].Messages)
	}
	if !strings.Contains(provider.requests[1].Messages[1].Content, "first step") {
		t.Fatalf("second request missing prior tool call transcript:\n%s", provider.requests[1].Messages[1].Content)
	}
	if !strings.Contains(provider.requests[1].Messages[2].Content, "action_succeeded") {
		t.Fatalf("second request missing prior tool result transcript:\n%s", provider.requests[1].Messages[2].Content)
	}
	assertTraceContains(t, recorder.events, trace.EventTurnCompleted)
}

func TestHandleEventRejectsToolCallIDReusedAcrossSteps(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{
		responses: []model.Response{
			{
				Decision: model.ModelDecision{
					ToolCalls: []model.ToolCall{{ID: "call_1", Name: "speak", Arguments: map[string]any{"text": "first step"}}},
					Control:   model.ControlDirective{Kind: model.ControlContinue},
				},
			},
			{
				Decision: model.ModelDecision{
					ToolCalls: []model.ToolCall{
						{ID: "call_1", Name: "speak", Arguments: map[string]any{"text": "duplicate"}},
						{ID: "call_2", Name: "speak", Arguments: map[string]any{"text": "skipped with invalid batch"}},
					},
					Control: model.ControlDirective{Kind: model.ControlContinue},
				},
			},
			{
				Decision: model.ModelDecision{
					Control: model.ControlDirective{Kind: model.ControlSettle},
				},
			},
		},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	if got := len(env.submittedActions); got != 1 {
		t.Fatalf("submitted action count = %d, want only the first step action", got)
	}
	if got := len(provider.requests); got != 3 {
		t.Fatalf("provider request count = %d, want duplicate feedback followed by settle", got)
	}
	finalTranscript := provider.requests[2].Messages
	if !requestMessagesContain(finalTranscript, "duplicate_tool_call_id") {
		t.Fatalf("third request missing duplicate tool call feedback: %+v", finalTranscript)
	}
	if !requestMessagesContain(finalTranscript, "batch_validation_failed") {
		t.Fatalf("third request missing skipped sibling feedback: %+v", finalTranscript)
	}
	assertTraceContains(t, recorder.events, trace.EventToolBatchFailed)
	assertTraceContains(t, recorder.events, trace.EventTurnCompleted)
}

func TestHandleEventFailsWhenMaxStepsExceeded(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{
		responses: []model.Response{
			{Decision: model.ModelDecision{ToolCalls: []model.ToolCall{{ID: "call_1", Name: "speak", Arguments: map[string]any{"text": "one"}}}, Control: model.ControlDirective{Kind: model.ControlContinue}}},
			{Decision: model.ModelDecision{ToolCalls: []model.ToolCall{{ID: "call_2", Name: "speak", Arguments: map[string]any{"text": "two"}}}, Control: model.ControlDirective{Kind: model.ControlContinue}}},
		},
	}
	config := agent.DefaultConfig()
	config.MaxSteps = 2
	loop := agent.NewLoop(provider, recorder, config)
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key))
	if err == nil || !strings.Contains(err.Error(), "max steps exceeded") {
		t.Fatalf("HandleEvent error = %v, want max steps exceeded", err)
	}
	if got := len(env.submittedActions); got != 2 {
		t.Fatalf("submitted action count = %d, want 2 before max steps terminal", got)
	}
	assertTraceContains(t, recorder.events, trace.EventTurnFailed)
}

func TestHandleEventFailsWhenMaxToolCallsPerStepExceeded(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{
		responses: []model.Response{{
			Decision: model.ModelDecision{
				ToolCalls: []model.ToolCall{
					{ID: "call_1", Name: "speak", Arguments: map[string]any{"text": "one"}},
					{ID: "call_2", Name: "speak", Arguments: map[string]any{"text": "two"}},
				},
				Control: model.ControlDirective{Kind: model.ControlContinue},
			},
		}},
	}
	config := agent.DefaultConfig()
	config.MaxToolCallsPerStep = 1
	loop := agent.NewLoop(provider, recorder, config)
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key))
	if err == nil || !strings.Contains(err.Error(), "max tool calls per step exceeded") {
		t.Fatalf("HandleEvent error = %v, want max tool calls per step exceeded", err)
	}
	if got := len(env.submittedActions); got != 0 {
		t.Fatalf("submitted action count = %d, want 0", got)
	}
	assertTraceContains(t, recorder.events, trace.EventTurnFailed)
}

func TestHandleEventFailsWhenMaxToolCallsPerTurnExceeded(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{
		responses: []model.Response{
			{Decision: model.ModelDecision{ToolCalls: []model.ToolCall{{ID: "call_1", Name: "speak", Arguments: map[string]any{"text": "one"}}}, Control: model.ControlDirective{Kind: model.ControlContinue}}},
			{Decision: model.ModelDecision{ToolCalls: []model.ToolCall{
				{ID: "call_2", Name: "speak", Arguments: map[string]any{"text": "two"}},
				{ID: "call_3", Name: "speak", Arguments: map[string]any{"text": "three"}},
			}, Control: model.ControlDirective{Kind: model.ControlContinue}}},
		},
	}
	config := agent.DefaultConfig()
	config.MaxToolCallsPerTurn = 2
	loop := agent.NewLoop(provider, recorder, config)
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key))
	if err == nil || !strings.Contains(err.Error(), "max tool calls per turn exceeded") {
		t.Fatalf("HandleEvent error = %v, want max tool calls per turn exceeded", err)
	}
	if got := len(env.submittedActions); got != 1 {
		t.Fatalf("submitted action count = %d, want only first step executed", got)
	}
	assertTraceContains(t, recorder.events, trace.EventTurnFailed)
}

func TestHandleEventTurnTimeoutCanPreemptBudgetsWithDelayedProvider(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{delay: 200 * time.Millisecond}
	config := agent.DefaultConfig()
	config.TurnTimeout = 30 * time.Millisecond
	config.LLMTimeout = time.Second
	loop := agent.NewLoop(provider, recorder, config)
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("HandleEvent error = %v, want context deadline exceeded", err)
	}
	assertTraceContains(t, recorder.events, trace.EventTurnFailed)
}

func TestFailedMultiStepTurnDoesNotAppendMemory(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	store := &failRecentStore{}
	provider := &scriptedProvider{
		responses: []model.Response{
			{Decision: model.ModelDecision{ToolCalls: []model.ToolCall{{ID: "call_1", Name: "speak", Arguments: map[string]any{"text": "side effect happened"}}}, Control: model.ControlDirective{Kind: model.ControlContinue}}},
		},
	}
	config := agent.DefaultConfig()
	config.MaxSteps = 1
	loop := agent.NewLoop(provider, recorder, config, agent.WithMemoryStore(store))
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key))
	if err == nil || !strings.Contains(err.Error(), "max steps exceeded") {
		t.Fatalf("HandleEvent error = %v, want max steps exceeded", err)
	}
	if got := len(store.appended); got != 0 {
		t.Fatalf("appended memory count = %d, want 0 for failed turn", got)
	}
}

func TestFailedTurnWithContextFactDoesNotAppendMemory(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	store := &failRecentStore{}
	provider := &scriptedProvider{
		responses: []model.Response{{
			Decision: model.ModelDecision{
				Control: model.ControlDirective{Kind: model.ControlContinue},
			},
		}},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig(), agent.WithMemoryStore(store))
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, playerUtteranceEvent("event_1", key, 43, "Will this be remembered?"))
	if err == nil || !strings.Contains(err.Error(), "continue control requires tool calls") {
		t.Fatalf("HandleEvent error = %v, want invalid continue without tool calls", err)
	}
	if got := len(store.appended); got != 0 {
		t.Fatalf("appended memory count = %d, want 0 for failed turn with context fact", got)
	}
}

func TestActionTechnicalFailureRecordsCompletedParallelSiblingMemory(t *testing.T) {
	registry := newParallelSpeakRegistry()
	env := &technicalActionEnvironment{
		delays: map[string]time.Duration{
			"fatal": 10 * time.Millisecond,
		},
		submitErrors: map[string]error{
			"fatal": errors.New("adapter transport closed"),
		},
	}
	recorder := &recordingTraceRecorder{}
	store := &failRecentStore{}
	provider := &scriptedProvider{
		responses: []model.Response{{
			Decision: model.ModelDecision{
				ToolCalls: []model.ToolCall{
					{ID: "call_success", Name: "speak", Arguments: map[string]any{"text": "success"}},
					{ID: "call_fatal", Name: "speak", Arguments: map[string]any{"text": "fatal"}},
				},
				Control: model.ControlDirective{Kind: model.ControlContinue},
			},
		}},
	}
	config := agent.DefaultConfig()
	config.ActionTimeout = time.Second
	loop := agent.NewLoop(provider, recorder, config, agent.WithMemoryStore(store))
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, playerUtteranceEvent("event_1", key, 43, "Remember my request."))
	if err == nil || !strings.Contains(err.Error(), "adapter transport closed") {
		t.Fatalf("HandleEvent error = %v, want adapter transport closed", err)
	}
	if got := len(store.appended); got != 1 {
		t.Fatalf("appended memory count = %d, want 1", got)
	}
	record := store.appended[0]
	if got := len(record.Outcomes); got != 1 {
		t.Fatalf("memory outcome count = %d, want 1", got)
	}
	if record.Outcomes[0].ToolName != "speak" {
		t.Fatalf("memory outcome tool = %q, want speak", record.Outcomes[0].ToolName)
	}
	if got := record.Outcomes[0].ToolArguments["text"]; got != "success" {
		t.Fatalf("memory outcome text = %v, want success", got)
	}
	if got := len(record.SourceContextFacts); got != 0 {
		t.Fatalf("context fact count = %d, want 0 for technical failure prior outcome record", got)
	}
	assertTraceContains(t, recorder.events, trace.EventTurnFailed)
}

func TestActionTechnicalFailureRecordsCompletedSequentialMemory(t *testing.T) {
	registry := newSpeakRegistry()
	env := &technicalActionEnvironment{
		submitErrors: map[string]error{
			"fatal": errors.New("adapter transport closed"),
		},
	}
	recorder := &recordingTraceRecorder{}
	store := &failRecentStore{}
	provider := &scriptedProvider{
		responses: []model.Response{{
			Decision: model.ModelDecision{
				ToolCalls: []model.ToolCall{
					{ID: "call_success", Name: "speak", Arguments: map[string]any{"text": "success"}},
					{ID: "call_fatal", Name: "speak", Arguments: map[string]any{"text": "fatal"}},
				},
				Control: model.ControlDirective{Kind: model.ControlContinue},
			},
		}},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig(), agent.WithMemoryStore(store))
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key))
	if err == nil || !strings.Contains(err.Error(), "adapter transport closed") {
		t.Fatalf("HandleEvent error = %v, want adapter transport closed", err)
	}
	if got := len(store.appended); got != 1 {
		t.Fatalf("appended memory count = %d, want 1", got)
	}
	record := store.appended[0]
	if got := len(record.Outcomes); got != 1 {
		t.Fatalf("memory outcome count = %d, want 1", got)
	}
	if record.Outcomes[0].ToolName != "speak" {
		t.Fatalf("memory outcome tool = %q, want speak", record.Outcomes[0].ToolName)
	}
	if got := record.Outcomes[0].ToolArguments["text"]; got != "success" {
		t.Fatalf("memory outcome text = %v, want success", got)
	}
	assertTraceContains(t, recorder.events, trace.EventTurnFailed)
}

func TestCompletedTurnAfterRejectedActionWritesOnlySuccessfulOutcomes(t *testing.T) {
	registry := newSpeakEmoteRegistry()
	env := &fakeEnvironment{
		statusByTool: map[string]protocolv1alpha2.ActionStatus{
			"emote": protocolv1alpha2.ActionStatus_ACTION_STATUS_REJECTED,
		},
	}
	recorder := &recordingTraceRecorder{}
	store := &failRecentStore{}
	provider := &scriptedProvider{
		responses: []model.Response{
			{Decision: model.ModelDecision{
				ToolCalls: []model.ToolCall{
					{ID: "call_1", Name: "speak", Arguments: map[string]any{"text": "kept"}},
					{ID: "call_2", Name: "emote", Arguments: map[string]any{"emote": "happy"}},
				},
				Control: model.ControlDirective{Kind: model.ControlSettle},
			}},
			{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}},
		},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig(), agent.WithMemoryStore(store))
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}
	if got := len(store.appended); got != 1 {
		t.Fatalf("appended memory count = %d, want 1", got)
	}
	record := store.appended[0]
	if got := len(record.Outcomes); got != 1 {
		t.Fatalf("memory outcome count = %d, want 1 successful outcome", got)
	}
	if record.Outcomes[0].ToolName != "speak" {
		t.Fatalf("memory outcomes = %+v, want only speak", record.Outcomes)
	}
	if got := record.Outcomes[0].ToolArguments["text"]; got != "kept" {
		t.Fatalf("memory speak text = %v, want kept", got)
	}
}

func TestHandleEventRetriesAfterInvalidToolCallBatchWithinStepBudget(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{
		responses: []model.Response{
			{Decision: model.ModelDecision{
				ToolCalls: []model.ToolCall{{ID: "call_1", Name: "missing", Arguments: map[string]any{}}},
				Control:   model.ControlDirective{Kind: model.ControlContinue},
			}},
			{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}},
		},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}
	if got := len(env.submittedActions); got != 0 {
		t.Fatalf("submitted action count = %d, want 0 for invalid batch", got)
	}
	if got := len(provider.requests); got != 2 {
		t.Fatalf("provider request count = %d, want retry step", got)
	}
	assertTraceContains(t, recorder.events, trace.EventTurnCompleted)
}

func TestHandleEventRetriesAfterActionResultTerminalFailure(t *testing.T) {
	for _, status := range []protocolv1alpha2.ActionStatus{
		protocolv1alpha2.ActionStatus_ACTION_STATUS_REJECTED,
		protocolv1alpha2.ActionStatus_ACTION_STATUS_FAILED,
		protocolv1alpha2.ActionStatus_ACTION_STATUS_CANCELLED,
		protocolv1alpha2.ActionStatus_ACTION_STATUS_INTERRUPTED,
	} {
		t.Run(status.String(), func(t *testing.T) {
			registry := newSpeakRegistry()
			env := &fakeEnvironment{statusByTool: map[string]protocolv1alpha2.ActionStatus{"speak": status}}
			recorder := &recordingTraceRecorder{}
			provider := &scriptedProvider{
				responses: []model.Response{
					{Decision: model.ModelDecision{
						ToolCalls: []model.ToolCall{{ID: "call_1", Name: "speak", Arguments: map[string]any{"text": "try"}}},
						Control:   model.ControlDirective{Kind: model.ControlContinue},
					}},
					{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}},
				},
			}
			loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
			conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
			key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

			if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
				t.Fatalf("HandleEvent returned error: %v", err)
			}
			if got := len(provider.requests); got != 2 {
				t.Fatalf("provider request count = %d, want retry step", got)
			}
			assertTraceContains(t, recorder.events, trace.EventTurnCompleted)
		})
	}
}

func TestHandleEventDoesNotSettleAfterFailedBatchEvenWhenControlSettleRequested(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{statusByTool: map[string]protocolv1alpha2.ActionStatus{
		"speak": protocolv1alpha2.ActionStatus_ACTION_STATUS_FAILED,
	}}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{
		responses: []model.Response{
			{Decision: model.ModelDecision{
				ToolCalls: []model.ToolCall{{ID: "call_1", Name: "speak", Arguments: map[string]any{"text": "fail"}}},
				Control:   model.ControlDirective{Kind: model.ControlSettle},
			}},
			{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}},
		},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}
	if got := len(provider.requests); got != 2 {
		t.Fatalf("provider request count = %d, want second step despite first settle", got)
	}
	assertTraceContains(t, recorder.events, trace.EventTurnCompleted)
}

func TestHandleEventFailsWhenFailureLoopExhaustsMaxSteps(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{
		responses: []model.Response{
			{Decision: model.ModelDecision{
				ToolCalls: []model.ToolCall{{ID: "call_1", Name: "missing", Arguments: map[string]any{}}},
				Control:   model.ControlDirective{Kind: model.ControlSettle},
			}},
		},
	}
	config := agent.DefaultConfig()
	config.MaxSteps = 1
	loop := agent.NewLoop(provider, recorder, config)
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key))
	if err == nil || !strings.Contains(err.Error(), "max steps exceeded") {
		t.Fatalf("HandleEvent error = %v, want max steps exceeded", err)
	}
	if got := len(env.submittedActions); got != 0 {
		t.Fatalf("submitted action count = %d, want 0", got)
	}
	assertTraceContains(t, recorder.events, trace.EventTurnFailed)
}

func TestMultiStepTraceEventsShareTurnIDAndIncreaseStepIndex(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{
		responses: []model.Response{
			{Decision: model.ModelDecision{ToolCalls: []model.ToolCall{{ID: "call_1", Name: "speak", Arguments: map[string]any{"text": "first"}}}, Control: model.ControlDirective{Kind: model.ControlContinue}}},
			{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}},
		},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	stepEvents := traceEventsByName(recorder.events, trace.EventAgentStepStarted)
	if got := len(stepEvents); got != 2 {
		t.Fatalf("step started count = %d, want 2; events=%+v", got, recorder.events)
	}
	if stepEvents[0].TurnID == "" || stepEvents[0].TurnID != stepEvents[1].TurnID {
		t.Fatalf("step turn ids = %q, %q; want same non-empty", stepEvents[0].TurnID, stepEvents[1].TurnID)
	}
	if stepEvents[0].Fields["step_index"] != 1 || stepEvents[1].Fields["step_index"] != 2 {
		t.Fatalf("step indices = %+v, %+v; want 1 then 2", stepEvents[0].Fields, stepEvents[1].Fields)
	}
	if got := len(provider.requests); got != 2 {
		t.Fatalf("provider request count = %d, want 2", got)
	}
	for index, req := range provider.requests {
		if len(req.Tools) != 1 || req.Tools[0].Name != "speak" {
			t.Fatalf("request %d tools = %+v, want speak from the same turn view", index, req.Tools)
		}
	}
}

func TestToolBatchTraceFieldsIncludeCallCountAndConcurrency(t *testing.T) {
	registry := newSpeakEmoteRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{
		responses: []model.Response{{
			Decision: model.ModelDecision{
				ToolCalls: []model.ToolCall{
					{ID: "call_1", Name: "speak", Arguments: map[string]any{"text": "one"}},
					{ID: "call_2", Name: "emote", Arguments: map[string]any{"emote": "happy"}},
				},
				Control: model.ControlDirective{Kind: model.ControlSettle},
			},
		}},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	batchEvents := traceEventsByName(recorder.events, trace.EventToolBatchStarted)
	if got := len(batchEvents); got != 1 {
		t.Fatalf("tool batch started count = %d, want 1; events=%+v", got, recorder.events)
	}
	fields := batchEvents[0].Fields
	if fields["tool_call_count"] != 2 {
		t.Fatalf("tool_call_count = %#v, want 2", fields["tool_call_count"])
	}
	if !strings.Contains(fmt.Sprint(fields["concurrency_modes"]), "sequential") {
		t.Fatalf("concurrency_modes = %#v, want sequential", fields["concurrency_modes"])
	}
}

func TestMultiStepTerminalEventIsUniqueAndLast(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{
		responses: []model.Response{
			{Decision: model.ModelDecision{ToolCalls: []model.ToolCall{{ID: "call_1", Name: "speak", Arguments: map[string]any{"text": "first"}}}, Control: model.ControlDirective{Kind: model.ControlContinue}}},
			{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}},
		},
	}
	loop := agent.NewLoop(provider, recorder, agent.DefaultConfig())
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	if err := loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key)); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	terminalCount := traceEventCount(recorder.events, trace.EventTurnCompleted) + traceEventCount(recorder.events, trace.EventTurnFailed)
	if terminalCount != 1 {
		t.Fatalf("terminal event count = %d, want 1; events=%+v", terminalCount, recorder.events)
	}
	if recorder.events[len(recorder.events)-1].Event != trace.EventTurnCompleted {
		t.Fatalf("last event = %q, want turn_completed; events=%+v", recorder.events[len(recorder.events)-1].Event, recorder.events)
	}
	if indexOfTrace(recorder.events, trace.EventTurnSettled) >= len(recorder.events)-1 {
		t.Fatalf("turn_settled should be non-terminal before turn_completed; events=%+v", recorder.events)
	}
}

func TestMaxStepsTraceFailureReason(t *testing.T) {
	registry := newSpeakRegistry()
	env := &fakeEnvironment{}
	recorder := &recordingTraceRecorder{}
	provider := &scriptedProvider{
		responses: []model.Response{
			{Decision: model.ModelDecision{ToolCalls: []model.ToolCall{{ID: "call_1", Name: "speak", Arguments: map[string]any{"text": "one"}}}, Control: model.ControlDirective{Kind: model.ControlContinue}}},
		},
	}
	config := agent.DefaultConfig()
	config.MaxSteps = 1
	loop := agent.NewLoop(provider, recorder, config)
	conn := agent.ConnectionContext{GameID: "fake-game", SessionID: "session:test"}
	key := session.AgentSessionKey{GameID: conn.GameID, WorldID: "world:test", EntityID: "npc:Abigail"}

	_ = loop.HandleEvent(context.Background(), env, conn, key, entityTarget(key), registry, gameEvent("event_1", key))

	terminal := recorder.events[len(recorder.events)-1]
	if terminal.Event != trace.EventTurnFailed || terminal.Reason != "max_steps_exceeded" {
		t.Fatalf("terminal = %+v, want turn_failed reason max_steps_exceeded", terminal)
	}
}
