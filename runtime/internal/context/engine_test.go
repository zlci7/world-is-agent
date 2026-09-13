package context_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	agentcontext "gameagent/runtime/internal/context"
	"gameagent/runtime/internal/definition"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/tokenestimate"
	"gameagent/runtime/internal/tool"
)

func TestEngineBuildCreatesContextProjectionFromValidatedInput(t *testing.T) {
	engine := agentcontext.NewEngine(agentcontext.EngineConfig{})
	input := validEngineInput(t)
	input.Observation.State = mustStruct(t, map[string]any{"weather": "rain"})

	result, err := engine.Build(input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	projection := result.Projection

	if projection.SessionKey != input.SessionKey {
		t.Fatalf("SessionKey = %+v, want %+v", projection.SessionKey, input.SessionKey)
	}
	if projection.CanonicalTarget.GetEntityId() != "npc:Abigail" {
		t.Fatalf("CanonicalTarget entity_id = %q, want npc:Abigail", projection.CanonicalTarget.GetEntityId())
	}
	if projection.AgentDescriptor.DefinitionID != "npc/abigail" {
		t.Fatalf("AgentDescriptor.DefinitionID = %q, want npc/abigail", projection.AgentDescriptor.DefinitionID)
	}
	if projection.GameDefinition == nil || projection.GameDefinition.GameID != "stardew-valley" {
		t.Fatalf("GameDefinition = %+v, want stardew-valley definition", projection.GameDefinition)
	}
	if projection.AgentDefinition == nil || projection.AgentDefinition.DefinitionID != "npc/abigail" {
		t.Fatalf("AgentDefinition = %+v, want npc/abigail definition", projection.AgentDefinition)
	}
	if projection.RuntimePolicy != "runtime policy" {
		t.Fatalf("RuntimePolicy = %q, want runtime policy", projection.RuntimePolicy)
	}
	if projection.CurrentEvent.EventID != "event-1" {
		t.Fatalf("CurrentEvent.EventID = %q, want event-1", projection.CurrentEvent.EventID)
	}
	if projection.CurrentObservation.EntityID != "npc:Abigail" || projection.CurrentObservation.WorldID != "world-a" {
		t.Fatalf("CurrentObservation scope = %+v, want world-a/npc:Abigail", projection.CurrentObservation)
	}
	if got := projection.CurrentObservation.State["weather"]; got != "rain" {
		t.Fatalf("CurrentObservation.State[weather] = %#v, want rain", got)
	}
	if got := len(projection.Tools); got != 1 {
		t.Fatalf("Tools length = %d, want 1", got)
	}
	if projection.Tools[0].Name != "speak" {
		t.Fatalf("Tools[0].Name = %q, want speak", projection.Tools[0].Name)
	}
}

func TestEngineBuildReturnsReportWithEffectiveBudget(t *testing.T) {
	engine := agentcontext.NewEngine(agentcontext.BudgetConfig{
		MaxRecentMemoryTokens:     123,
		MaxRequestTokens:          4096,
		MaxSystemTokens:           512,
		MaxUserMessageTokens:      2048,
		MaxToolCount:              3,
		MaxToolDescriptionTokens:  64,
		MaxToolSchemaTokens:       128,
		MaxTotalToolSchemaTokens:  256,
		MaxToolResultOutputTokens: 300,
	})

	result, err := engine.Build(validEngineInput(t))
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if result.Projection.SessionKey.EntityID != "npc:Abigail" {
		t.Fatalf("Projection.SessionKey.EntityID = %q, want npc:Abigail", result.Projection.SessionKey.EntityID)
	}

	budget := result.Report.EffectiveBudget
	if budget.MaxRecentMemoryTokens != 123 {
		t.Fatalf("MaxRecentMemoryTokens = %d, want configured token budget 123", budget.MaxRecentMemoryTokens)
	}
	if budget.MaxRequestTokens != 4096 ||
		budget.MaxSystemTokens != 512 ||
		budget.MaxUserMessageTokens != 2048 ||
		budget.MaxToolCount != 3 ||
		budget.MaxToolDescriptionTokens != 64 ||
		budget.MaxToolSchemaTokens != 128 ||
		budget.MaxTotalToolSchemaTokens != 256 ||
		budget.MaxToolResultOutputTokens != 300 {
		t.Fatalf("EffectiveBudget did not preserve configured limits: %+v", budget)
	}
	if !result.Report.Sections.Has("current_event") {
		t.Fatalf("report sections = %+v, want current_event section", result.Report.Sections)
	}
}

func TestEngineBuildProjectsObservationProtocolFields(t *testing.T) {
	engine := agentcontext.NewEngine(agentcontext.EngineConfig{})
	input := validEngineInput(t)
	input.Observation.Revision = 7
	input.Observation.NearbyEntities = []*protocolv1alpha2.EntityRef{
		{
			EntityId:     " player:local ",
			EntityType:   " player ",
			DisplayName:  " Local Player ",
			DefinitionId: " player/local ",
		},
		nil,
		{
			EntityId:     "npc:Robin",
			EntityType:   "npc",
			DisplayName:  "Robin",
			DefinitionId: "npc/robin",
		},
	}
	input.Observation.Extensions = mustStruct(t, map[string]any{
		"adapter_revision": "stardew-0.1",
		"source":           "fixture",
	})

	result, err := engine.Build(input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	projection := result.Projection

	observation := projection.CurrentObservation
	if observation.Revision != 7 {
		t.Fatalf("CurrentObservation.Revision = %d, want 7", observation.Revision)
	}
	if got, want := len(observation.NearbyEntities), 2; got != want {
		t.Fatalf("CurrentObservation.NearbyEntities length = %d, want %d", got, want)
	}
	first := observation.NearbyEntities[0]
	if first.GetEntityId() != "player:local" ||
		first.GetEntityType() != "player" ||
		first.GetDisplayName() != "Local Player" ||
		first.GetDefinitionId() != "player/local" {
		t.Fatalf("first nearby entity = %+v, want trimmed player ref", first)
	}
	if got, want := observation.Extensions, map[string]any{"adapter_revision": "stardew-0.1", "source": "fixture"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("CurrentObservation.Extensions = %#v, want %#v", got, want)
	}

	input.Observation.NearbyEntities[0].EntityId = "mutated"
	if observation.NearbyEntities[0].GetEntityId() != "player:local" {
		t.Fatalf("CurrentObservation.NearbyEntities shares source refs: %+v", observation.NearbyEntities[0])
	}
}

func TestEngineBuildTrimsEventScopeIdentityForValidationAndProjection(t *testing.T) {
	engine := agentcontext.NewEngine(agentcontext.EngineConfig{})
	input := validEngineInput(t)
	input.Event.WorldId = " world-a "
	input.Event.TargetEntityId = "\tnpc:Abigail\n"

	result, err := engine.Build(input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	projection := result.Projection
	if projection.CurrentEvent.WorldID != "world-a" {
		t.Fatalf("CurrentEvent.WorldID = %q, want trimmed world-a", projection.CurrentEvent.WorldID)
	}
	if projection.CurrentEvent.TargetEntityID != "npc:Abigail" {
		t.Fatalf("CurrentEvent.TargetEntityID = %q, want trimmed npc:Abigail", projection.CurrentEvent.TargetEntityID)
	}
}

func TestEngineBuildKeepsEventScopeIdentityCaseSensitive(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*agentcontext.BuildInput)
		wantErr string
	}{
		{
			name: "world case mismatch",
			mutate: func(input *agentcontext.BuildInput) {
				input.Event.WorldId = "World-A"
			},
			wantErr: "event world_id does not match session key",
		},
		{
			name: "target case mismatch",
			mutate: func(input *agentcontext.BuildInput) {
				input.Event.TargetEntityId = "NPC:Abigail"
			},
			wantErr: "event target_entity_id does not match session key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := validEngineInput(t)
			tt.mutate(&input)

			_, err := agentcontext.NewEngine(agentcontext.EngineConfig{}).Build(input)
			assertInvalidInputError(t, err, tt.wantErr)
		})
	}
}

func TestEngineBuildProjectsAuthorityInstruction(t *testing.T) {
	result, err := agentcontext.NewEngine(agentcontext.EngineConfig{}).Build(validEngineInput(t))
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	projection := result.Projection
	assertStringContainsAll(
		t,
		projection.Instruction,
		"Current Observation is the current truth.",
		"Recent Memory is historical context.",
		"Use only tools in the current View.",
	)
}

func TestEngineBuildAllowsDefinitionFallback(t *testing.T) {
	engine := agentcontext.NewEngine(agentcontext.EngineConfig{})
	input := validEngineInput(t)
	input.GameDefinition = nil
	input.AgentDefinition = nil

	result, err := engine.Build(input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	projection := result.Projection
	if projection.GameDefinition != nil {
		t.Fatalf("GameDefinition = %+v, want nil fallback", projection.GameDefinition)
	}
	if projection.AgentDefinition != nil {
		t.Fatalf("AgentDefinition = %+v, want nil fallback", projection.AgentDefinition)
	}
	if projection.AgentDescriptor.DefinitionID != "npc/abigail" {
		t.Fatalf("AgentDescriptor.DefinitionID = %q, want original definition id", projection.AgentDescriptor.DefinitionID)
	}
}

func TestEngineBuildRejectsMissingRequiredInputs(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*agentcontext.BuildInput)
		wantErr string
	}{
		{
			name: "missing event",
			mutate: func(input *agentcontext.BuildInput) {
				input.Event = nil
			},
			wantErr: "event is required",
		},
		{
			name: "missing observation",
			mutate: func(input *agentcontext.BuildInput) {
				input.Observation = nil
			},
			wantErr: "observation is required",
		},
		{
			name: "missing canonical target",
			mutate: func(input *agentcontext.BuildInput) {
				input.CanonicalTarget = nil
			},
			wantErr: "canonical target is required",
		},
		{
			name: "missing runtime policy",
			mutate: func(input *agentcontext.BuildInput) {
				input.RuntimePolicy = ""
			},
			wantErr: "runtime policy is required",
		},
		{
			name: "missing session game",
			mutate: func(input *agentcontext.BuildInput) {
				input.SessionKey.GameID = ""
			},
			wantErr: "session key game_id is required",
		},
		{
			name: "missing session world",
			mutate: func(input *agentcontext.BuildInput) {
				input.SessionKey.WorldID = ""
			},
			wantErr: "session key world_id is required",
		},
		{
			name: "missing session entity",
			mutate: func(input *agentcontext.BuildInput) {
				input.SessionKey.EntityID = ""
			},
			wantErr: "session key entity_id is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := agentcontext.NewEngine(agentcontext.EngineConfig{})
			input := validEngineInput(t)
			tt.mutate(&input)

			_, err := engine.Build(input)
			assertInvalidInputError(t, err, tt.wantErr)
		})
	}
}

func TestEngineBuildRejectsScopeMismatches(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*agentcontext.BuildInput)
		wantErr string
	}{
		{
			name: "canonical target entity mismatch",
			mutate: func(input *agentcontext.BuildInput) {
				input.CanonicalTarget.EntityId = "npc:Haley"
			},
			wantErr: "canonical target entity_id does not match session key",
		},
		{
			name: "descriptor session mismatch",
			mutate: func(input *agentcontext.BuildInput) {
				input.AgentDescriptor.SessionKey.EntityID = "npc:Haley"
			},
			wantErr: "agent descriptor session key does not match session key",
		},
		{
			name: "descriptor definition mismatch",
			mutate: func(input *agentcontext.BuildInput) {
				input.AgentDescriptor.DefinitionID = "npc/haley"
			},
			wantErr: "agent descriptor definition_id does not match canonical target",
		},
		{
			name: "event world mismatch",
			mutate: func(input *agentcontext.BuildInput) {
				input.Event.WorldId = "world-b"
			},
			wantErr: "event world_id does not match session key",
		},
		{
			name: "event target mismatch",
			mutate: func(input *agentcontext.BuildInput) {
				input.Event.TargetEntityId = "npc:Haley"
			},
			wantErr: "event target_entity_id does not match session key",
		},
		{
			name: "observation world mismatch",
			mutate: func(input *agentcontext.BuildInput) {
				input.Observation.WorldId = "world-b"
			},
			wantErr: "observation world_id does not match session key",
		},
		{
			name: "observation entity mismatch",
			mutate: func(input *agentcontext.BuildInput) {
				input.Observation.EntityId = "npc:Haley"
			},
			wantErr: "observation entity_id does not match session key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := agentcontext.NewEngine(agentcontext.EngineConfig{})
			input := validEngineInput(t)
			tt.mutate(&input)

			_, err := engine.Build(input)
			assertInvalidInputError(t, err, tt.wantErr)
		})
	}
}

func TestEngineBuildRejectsDefinitionBindingMismatches(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*agentcontext.BuildInput)
		wantErr string
	}{
		{
			name: "game definition game mismatch",
			mutate: func(input *agentcontext.BuildInput) {
				input.GameDefinition.GameID = "other-game"
			},
			wantErr: "game definition game_id does not match session key",
		},
		{
			name: "agent definition game mismatch",
			mutate: func(input *agentcontext.BuildInput) {
				input.AgentDefinition.GameID = "other-game"
			},
			wantErr: "agent definition game_id does not match session key",
		},
		{
			name: "agent definition descriptor mismatch",
			mutate: func(input *agentcontext.BuildInput) {
				input.AgentDefinition.DefinitionID = "npc/haley"
			},
			wantErr: "agent definition definition_id does not match agent descriptor",
		},
		{
			name: "empty target definition cannot have agent definition",
			mutate: func(input *agentcontext.BuildInput) {
				input.CanonicalTarget.DefinitionId = ""
				input.AgentDescriptor.DefinitionID = ""
			},
			wantErr: "agent definition must be nil when canonical target definition_id is empty",
		},
		{
			name: "empty target definition cannot have descriptor definition",
			mutate: func(input *agentcontext.BuildInput) {
				input.CanonicalTarget.DefinitionId = ""
				input.AgentDefinition = nil
			},
			wantErr: "agent descriptor definition_id does not match canonical target",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := agentcontext.NewEngine(agentcontext.EngineConfig{})
			input := validEngineInput(t)
			tt.mutate(&input)

			_, err := engine.Build(input)
			assertInvalidInputError(t, err, tt.wantErr)
		})
	}
}

func TestEngineBuildProjectsCurrentEventAndContextFactsSeparately(t *testing.T) {
	engine := agentcontext.NewEngine(agentcontext.EngineConfig{})
	input := validEngineInput(t)
	input.Event.Sequence = 42
	input.Event.GameTime = &protocolv1alpha2.GameTime{
		Year:   ptrInt32(1),
		Season: ptrInt32(2),
		Day:    ptrInt32(3),
		Hour:   ptrInt32(6),
		Minute: ptrInt32(20),
		Tick:   ptrInt64(9001),
	}
	input.Event.Payload = mustStruct(t, map[string]any{
		"dialogue_id": "intro",
		"nested": map[string]any{
			"mood": "curious",
		},
	})
	input.Event.ContextFacts = []*protocolv1alpha2.ContextFact{
		{
			Kind:           "utterance",
			ActorEntityId:  "player:local",
			TargetEntityId: "npc:Abigail",
			ScopeId:        "conversation:1",
			Text:           "  Want to explore the mines?  ",
			Label:          "  player invite  ",
			Attributes: mustStruct(t, map[string]any{
				"tone": "friendly",
			}),
		},
		{
			Kind:  "scene",
			Label: "rainy day",
		},
	}

	result, err := engine.Build(input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	projection := result.Projection

	event := projection.CurrentEvent
	if event.EventID != "event-1" || event.EventType != "player_interacted_with_npc" {
		t.Fatalf("CurrentEvent identity = %+v, want event-1/player_interacted_with_npc", event)
	}
	if event.WorldID != "world-a" || event.TargetEntityID != "npc:Abigail" || event.Sequence != 42 {
		t.Fatalf("CurrentEvent scope = %+v, want world-a/npc:Abigail/42", event)
	}
	if event.GameTime == nil || event.GameTime.GetTick() != 9001 {
		t.Fatalf("CurrentEvent.GameTime = %+v, want copied game time", event.GameTime)
	}
	if event.CanonicalTarget.GetDefinitionId() != "npc/abigail" {
		t.Fatalf("CurrentEvent.CanonicalTarget = %+v, want canonical target", event.CanonicalTarget)
	}
	if got := event.Payload["dialogue_id"]; got != "intro" {
		t.Fatalf("CurrentEvent.Payload[dialogue_id] = %#v, want intro", got)
	}
	eventJSON := mustMarshalJSONBytes(t, event)
	if strings.Contains(string(eventJSON), "context_facts") || strings.Contains(string(eventJSON), "ContextFacts") {
		t.Fatalf("CurrentEvent unexpectedly contains context facts: %s", eventJSON)
	}

	facts := projection.CurrentEventContextFacts
	if got, want := len(facts), 2; got != want {
		t.Fatalf("CurrentEventContextFacts length = %d, want %d", got, want)
	}
	if facts[0].Kind != "utterance" ||
		facts[0].ActorEntityID != "player:local" ||
		facts[0].TargetEntityID != "npc:Abigail" ||
		facts[0].ScopeID != "conversation:1" ||
		facts[0].Text != "Want to explore the mines?" ||
		facts[0].Label != "player invite" {
		t.Fatalf("first fact projection = %+v, want trimmed utterance fact", facts[0])
	}
	if got, want := facts[0].Attributes, map[string]any{"tone": "friendly"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first fact attributes = %#v, want %#v", got, want)
	}
	if facts[1].Kind != "scene" || facts[1].Label != "rainy day" {
		t.Fatalf("second fact projection = %+v, want scene/rainy day", facts[1])
	}
}

func TestEngineBuildDoesNotInferContextFactsFromPayloadOrObservationState(t *testing.T) {
	engine := agentcontext.NewEngine(agentcontext.EngineConfig{})
	input := validEngineInput(t)
	input.Event.Payload = mustStruct(t, map[string]any{
		"context_facts": []any{
			map[string]any{"kind": "utterance", "text": "payload should not become a fact"},
		},
	})
	input.Observation.State = mustStruct(t, map[string]any{
		"context_facts": []any{
			map[string]any{"kind": "scene", "label": "state should not become a fact"},
		},
	})

	result, err := engine.Build(input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	projection := result.Projection
	if got := len(projection.CurrentEventContextFacts); got != 0 {
		t.Fatalf("CurrentEventContextFacts length = %d, want 0", got)
	}
	if got := projection.CurrentEvent.Payload["context_facts"]; got == nil {
		t.Fatal("CurrentEvent payload lost context_facts key; want payload preserved as generic JSON")
	}
}

func TestEngineBuildProjectsRecentMemorySelection(t *testing.T) {
	engine := agentcontext.NewEngine(agentcontext.EngineConfig{MaxRecentMemoryTokens: 4096})
	input := validEngineInput(t)
	input.Event.GameTime = &protocolv1alpha2.GameTime{
		Year:   ptrInt32(1),
		Season: ptrInt32(1),
		Day:    ptrInt32(2),
		Hour:   ptrInt32(6),
		Minute: ptrInt32(20),
	}
	gameTime := &memory.GameTimeSnapshot{Year: 1, Season: 1, Day: 2, Hour: 6, Minute: 20}
	input.RecentMemories = []memory.Record{
		{
			MemoryID:            "memory-seq-2",
			GameTime:            gameTime,
			SourceEventSequence: 2,
			Outcomes: []memory.TurnOutcome{{
				ToolName: "present_dialogue",
				ToolArguments: map[string]any{
					"text":            "I brought snacks.",
					"allow_free_text": true,
				},
				ActionStatus: "ACTION_STATUS_SUCCEEDED",
			}},
		},
		{
			MemoryID:            "memory-seq-1",
			GameTime:            gameTime,
			SourceEventSequence: 1,
			SourceContextFacts: []memory.SourceContextFact{{
				Kind:          "utterance",
				ActorEntityID: "player:local",
				Text:          "Meet me at the mine.",
			}},
		},
		{
			MemoryID: "memory-future",
			GameTime: &memory.GameTimeSnapshot{Year: 1, Season: 1, Day: 2, Hour: 8, Minute: 0},
			SourceContextFacts: []memory.SourceContextFact{{
				Kind:          "utterance",
				ActorEntityID: "player:local",
				Text:          "This future fact must be filtered.",
			}},
		},
	}

	result, err := engine.Build(input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	projection := result.Projection

	if got, want := len(projection.RecentMemory), 2; got != want {
		t.Fatalf("RecentMemory length = %d, want %d", got, want)
	}
	if projection.RecentMemory[0].MemoryID != "memory-seq-1" || projection.RecentMemory[1].MemoryID != "memory-seq-2" {
		t.Fatalf("RecentMemory order = %+v, want sequence order memory-seq-1 then memory-seq-2", projection.RecentMemory)
	}
	if got, want := projection.RecentMemory[0].TimeRelation, "today 06:20"; got != want {
		t.Fatalf("first TimeRelation = %q, want %q", got, want)
	}
	if got, want := projection.RecentMemory[0].Summaries, []string{`player:local said "Meet me at the mine."`}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first summaries = %#v, want %#v", got, want)
	}
	wantOutcome := `tool "present_dialogue" status "ACTION_STATUS_SUCCEEDED" arguments {"allow_free_text":true,"text":"I brought snacks."}`
	if got, want := projection.RecentMemory[1].Summaries, []string{wantOutcome}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second summaries = %#v, want %#v", got, want)
	}
}

func TestEngineBuildAppliesRecentMemorySoftLimit(t *testing.T) {
	input := validEngineInput(t)
	input.RecentMemories = []memory.Record{
		{
			MemoryID:  "old",
			CreatedAt: time.Unix(100, 0),
			SourceContextFacts: []memory.SourceContextFact{{
				Kind:          "utterance",
				ActorEntityID: "player:local",
				Text:          "older memory should be trimmed by the soft limit",
			}},
		},
		{
			MemoryID:  "new",
			CreatedAt: time.Unix(200, 0),
			SourceContextFacts: []memory.SourceContextFact{{
				Kind:          "utterance",
				ActorEntityID: "player:local",
				Text:          "newest memory is retained",
			}},
		},
	}
	latestOnlyInput := input
	latestOnlyInput.RecentMemories = input.RecentMemories[1:]
	latestOnly, err := agentcontext.NewEngine(agentcontext.EngineConfig{}).Build(latestOnlyInput)
	if err != nil {
		t.Fatalf("latest-only Build returned error: %v", err)
	}
	engine := agentcontext.NewEngine(agentcontext.EngineConfig{
		MaxRecentMemoryTokens: reportSectionProjectionEstimatedTokens(t, latestOnly.Report, "recent_memory"),
	})

	result, err := engine.Build(input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	projection := result.Projection

	if got, want := len(projection.RecentMemory), 1; got != want {
		t.Fatalf("RecentMemory length = %d, want %d", got, want)
	}
	if projection.RecentMemory[0].MemoryID != "new" {
		t.Fatalf("retained memory = %q, want newest memory", projection.RecentMemory[0].MemoryID)
	}
}

func TestEngineBuildProjectsBoundedRecentMemoryToolArguments(t *testing.T) {
	engine := agentcontext.NewEngine(agentcontext.EngineConfig{
		MaxRecentMemoryTokens:         4096,
		MaxToolResultOutputTokens:     300,
		MaxToolResultOutputDepth:      2,
		MaxToolResultOutputFields:     2,
		MaxToolResultOutputArrayItems: 2,
	})
	input := validEngineInput(t)
	input.RecentMemories = []memory.Record{{
		Outcomes: []memory.TurnOutcome{{
			ToolName: "inspect",
			ToolArguments: map[string]any{
				"a": []any{"one", "two", "three"},
				"b": map[string]any{"nested": map[string]any{"leaf": "too deep"}},
				"c": "extra field",
			},
			ActionStatus: "ACTION_STATUS_SUCCEEDED",
		}},
	}}

	result, err := engine.Build(input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	projection := result.Projection

	summary := projection.RecentMemory[0].Summaries[0]
	assertStringContainsAll(t, summary, `tool "inspect"`, `status "ACTION_STATUS_SUCCEEDED"`, `"a":["one","two","_truncated_items:1"]`, "_truncated")
	for _, unwanted := range []string{"three", "extra field", "too deep"} {
		if strings.Contains(summary, unwanted) {
			t.Fatalf("bounded memory arguments leaked %q:\n%s", unwanted, summary)
		}
	}
}

func TestEngineBuildProjectsCurrentTurnTranscriptWithBounds(t *testing.T) {
	engine := agentcontext.NewEngine(agentcontext.EngineConfig{
		MaxToolResultOutputTokens:     300,
		MaxToolResultOutputDepth:      2,
		MaxToolResultOutputFields:     2,
		MaxToolResultOutputArrayItems: 2,
	})
	input := validEngineInput(t)
	input.Transcript = []model.Message{
		{
			Role: model.RoleAssistant,
			ToolCalls: []model.ToolCall{{
				ID:   "call_1",
				Name: "inspect",
				Arguments: map[string]any{
					"a": []any{"one", "two", "three"},
					"b": map[string]any{"nested": map[string]any{"leaf": "too deep"}},
					"c": "extra field",
				},
			}},
		},
		{
			Role: model.RoleTool,
			ToolResults: []model.ToolResult{{
				ToolCallID: "call_1",
				Name:       "inspect",
				Status:     "rejected",
				Code:       "adapter_rejected",
				Message:    "adapter rejected request\nstack trace line\n{\"raw\":\"json\",\"action_id\":\"runtime-action-123\"}" + strings.Repeat("x", 180),
				Output: map[string]any{
					"a": []any{"one", "two", "three"},
					"b": map[string]any{"nested": map[string]any{"leaf": "too deep"}},
					"c": "extra field",
				},
			}},
		},
	}

	result, err := engine.Build(input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	projection := result.Projection

	if got, want := len(projection.CurrentTurnTranscript), 2; got != want {
		t.Fatalf("CurrentTurnTranscript length = %d, want %d", got, want)
	}
	callArgs := projection.CurrentTurnTranscript[0].ToolCalls[0].Arguments
	if _, ok := callArgs["c"]; ok {
		t.Fatalf("projected tool call arguments leaked extra field: %#v", callArgs)
	}
	if got := callArgs["a"]; !reflect.DeepEqual(got, []any{"one", "two", "_truncated_items:1"}) {
		t.Fatalf("projected tool call arguments[a] = %#v", got)
	}
	toolResult := projection.CurrentTurnTranscript[1].ToolResults[0]
	if strings.Contains(toolResult.Message, "stack trace") || strings.Contains(toolResult.Message, "runtime-action-123") || strings.Contains(toolResult.Message, `{"raw"`) {
		t.Fatalf("projected tool result message leaked raw diagnostic: %q", toolResult.Message)
	}
	if len(toolResult.Message) > 120 {
		t.Fatalf("projected tool result message length = %d, want <= 120", len(toolResult.Message))
	}
	if _, ok := toolResult.Output["c"]; ok {
		t.Fatalf("projected tool result output leaked extra field: %#v", toolResult.Output)
	}
	if got := toolResult.Output["a"]; !reflect.DeepEqual(got, []any{"one", "two", "_truncated_items:1"}) {
		t.Fatalf("projected tool result output[a] = %#v", got)
	}
}

func TestEngineBuildReportsMemoryBudgetWithoutDroppingRequiredProjection(t *testing.T) {
	input := validEngineInput(t)
	input.RecentMemories = []memory.Record{
		{
			MemoryID:  "old",
			CreatedAt: time.Unix(100, 0),
			SourceContextFacts: []memory.SourceContextFact{{
				Kind:          "utterance",
				ActorEntityID: "player:local",
				Text:          "older memory should be trimmed before required current context",
			}},
		},
		{
			MemoryID:  "new",
			CreatedAt: time.Unix(200, 0),
			SourceContextFacts: []memory.SourceContextFact{{
				Kind:          "utterance",
				ActorEntityID: "player:local",
				Text:          "hi",
			}},
		},
	}
	latestOnlyInput := input
	latestOnlyInput.RecentMemories = input.RecentMemories[1:]
	latestOnly, err := agentcontext.NewEngine(agentcontext.BudgetConfig{}).Build(latestOnlyInput)
	if err != nil {
		t.Fatalf("latest-only Build returned error: %v", err)
	}
	engine := agentcontext.NewEngine(agentcontext.BudgetConfig{
		MaxRecentMemoryTokens: reportSectionProjectionEstimatedTokens(t, latestOnly.Report, "recent_memory"),
	})

	result, err := engine.Build(input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	projection := result.Projection

	if projection.AgentDefinition == nil || projection.AgentDefinition.Identity == "" {
		t.Fatalf("AgentDefinition was dropped: %+v", projection.AgentDefinition)
	}
	if projection.CurrentEvent.EventID != "event-1" {
		t.Fatalf("CurrentEvent.EventID = %q, want event-1", projection.CurrentEvent.EventID)
	}
	if projection.CurrentObservation.EntityID != "npc:Abigail" {
		t.Fatalf("CurrentObservation.EntityID = %q, want npc:Abigail", projection.CurrentObservation.EntityID)
	}
	if got, want := len(projection.RecentMemory), 1; got != want {
		t.Fatalf("RecentMemory length = %d, want newest memory only", got)
	}
	if projection.RecentMemory[0].MemoryID != "new" {
		t.Fatalf("retained memory = %q, want new", projection.RecentMemory[0].MemoryID)
	}
	if result.Report.RecentMemory.DroppedCount != 1 {
		t.Fatalf("RecentMemory.DroppedCount = %d, want 1", result.Report.RecentMemory.DroppedCount)
	}
	memorySection := reportSection(t, result.Report, "recent_memory")
	if !memorySection.Cropped || memorySection.Reason != agentcontext.ReasonMemoryBudgetExceeded {
		t.Fatalf("recent_memory section = %+v, want cropped with memory budget reason", memorySection)
	}
	if !reportHasReason(result.Report, agentcontext.ReasonMemoryBudgetExceeded) {
		t.Fatalf("ReasonCodes = %v, want memory budget reason", result.Report.ReasonCodes)
	}
}

func TestEngineBuildDropsNewestMemoryWhenItExceedsMemoryBudget(t *testing.T) {
	engine := agentcontext.NewEngine(agentcontext.BudgetConfig{MaxRecentMemoryTokens: 80})
	input := validEngineInput(t)
	input.RecentMemories = []memory.Record{{
		MemoryID: "oversized-newest",
		SourceContextFacts: []memory.SourceContextFact{{
			Kind:          "utterance",
			ActorEntityID: "player:local",
			Text:          strings.Repeat("memory-secret", 40),
		}},
	}}

	result, err := engine.Build(input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(result.Projection.RecentMemory) != 0 {
		t.Fatalf("RecentMemory = %+v, want oversized optional memory dropped", result.Projection.RecentMemory)
	}
	if result.Report.RecentMemory.RetainedCount != 0 || result.Report.RecentMemory.DroppedCount != 1 {
		t.Fatalf("RecentMemory report = %+v, want one dropped memory", result.Report.RecentMemory)
	}
	if !reportHasReason(result.Report, agentcontext.ReasonMemoryBudgetExceeded) {
		t.Fatalf("ReasonCodes = %v, want memory budget reason", result.Report.ReasonCodes)
	}
}

func TestEngineBuildAppliesRecentMemoryBudgetUsingProjectionEstimatedTokens(t *testing.T) {
	input := validEngineInput(t)
	input.RecentMemories = []memory.Record{{
		MemoryID: "new",
		SourceContextFacts: []memory.SourceContextFact{{
			Kind:          "utterance",
			ActorEntityID: "player:local",
			Text:          "hi",
		}},
	}}

	baseline, err := agentcontext.NewEngine(agentcontext.BudgetConfig{}).Build(input)
	if err != nil {
		t.Fatalf("baseline Build returned error: %v", err)
	}
	proxyTokens := reportSectionProjectionEstimatedTokens(t, baseline.Report, "recent_memory")
	if proxyTokens <= 1 {
		t.Fatalf("recent_memory proxy tokens = %d, want > 1", proxyTokens)
	}

	result, err := agentcontext.NewEngine(agentcontext.BudgetConfig{
		MaxRecentMemoryTokens: proxyTokens - 1,
	}).Build(input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(result.Projection.RecentMemory) != 0 {
		t.Fatalf("RecentMemory = %+v, want dropped when proxy tokens exceed budget", result.Projection.RecentMemory)
	}
	if result.Report.RecentMemory.RetainedCount != 0 || result.Report.RecentMemory.DroppedCount != 1 {
		t.Fatalf("RecentMemory report = %+v, want one dropped memory", result.Report.RecentMemory)
	}
}

func TestEngineBuildAppliesSharedDefinitionBudget(t *testing.T) {
	engine := agentcontext.NewEngine(agentcontext.BudgetConfig{MaxDefinitionTokens: 260})
	input := validEngineInput(t)
	input.AgentDefinition.Identity = "agent-core"
	input.AgentDefinition.Personality = []string{
		"kept-agent-trait",
		strings.Repeat("agent-secret", 40),
	}
	input.AgentDefinition.SpeechStyle = []string{strings.Repeat("agent-speech-secret", 40)}
	input.AgentDefinition.Preferences = []string{strings.Repeat("agent-preference-secret", 40)}
	input.AgentDefinition.BehaviorGuidelines = []string{strings.Repeat("agent-guideline-secret", 40)}
	input.GameDefinition.Summary = strings.Repeat("game-secret", 40)
	input.GameDefinition.WorldRules = []string{strings.Repeat("game-rule-secret", 40)}
	input.RecentMemories = []memory.Record{{
		MemoryID: "memory-1",
		Outcomes: []memory.TurnOutcome{{
			ToolName:      "speak",
			ActionStatus:  "succeeded",
			ToolArguments: map[string]any{"text": strings.Repeat("memory-secret", 40)},
		}},
	}}

	result, err := engine.Build(input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	definitionJSON := string(mustMarshalProjectionJSON(t, map[string]any{
		"agent_definition": result.Projection.AgentDefinition,
		"game_definition":  result.Projection.GameDefinition,
	}))
	assertStringContainsAll(t, definitionJSON, "agent-core")
	for _, leaked := range []string{
		"agent-secret",
		"agent-speech-secret",
		"agent-preference-secret",
		"agent-guideline-secret",
		"game-secret",
		"game-rule-secret",
	} {
		if strings.Contains(definitionJSON, leaked) {
			t.Fatalf("definition budget leaked %q:\n%s", leaked, definitionJSON)
		}
	}
	if result.Projection.AgentDefinition == nil || result.Projection.GameDefinition == nil {
		t.Fatalf("definitions should remain present after budget: agent=%+v game=%+v", result.Projection.AgentDefinition, result.Projection.GameDefinition)
	}
	if !reportHasReason(result.Report, agentcontext.ReasonDefinitionBudgetExceeded) {
		t.Fatalf("ReasonCodes = %v, want definition budget reason", result.Report.ReasonCodes)
	}
}

func TestEngineBuildGloballyTrimsOptionalContextToFitRequestBudget(t *testing.T) {
	input := validEngineInput(t)
	input.AgentDefinition.Identity = "agent-core"
	input.Event.Payload = mustStruct(t, map[string]any{"dialogue_id": "intro"})
	input.Observation.State = mustStruct(t, map[string]any{"weather": "rain"})
	input.RecentMemories = []memory.Record{{
		MemoryID: "oversized-memory",
		SourceContextFacts: []memory.SourceContextFact{{
			Kind:          "utterance",
			ActorEntityID: "player:local",
			Text:          strings.Repeat("memory-secret", 120),
		}},
	}}
	input.Transcript = []model.Message{
		{
			Role: model.RoleAssistant,
			ToolCalls: []model.ToolCall{{
				ID:        "call_old",
				Name:      "inspect",
				Arguments: map[string]any{"query": strings.Repeat("old-transcript-secret", 80)},
			}},
		},
		{
			Role: model.RoleTool,
			ToolResults: []model.ToolResult{{
				ToolCallID: "call_old",
				Name:       "inspect",
				Status:     "succeeded",
				Code:       "action_succeeded",
				Output:     map[string]any{"result": strings.Repeat("old-output-secret", 80)},
			}},
		},
		{
			Role: model.RoleAssistant,
			ToolCalls: []model.ToolCall{{
				ID:        "call_latest",
				Name:      "inspect",
				Arguments: map[string]any{"query": "latest"},
			}},
		},
		{
			Role: model.RoleTool,
			ToolResults: []model.ToolResult{{
				ToolCallID: "call_latest",
				Name:       "inspect",
				Status:     "succeeded",
				Code:       "action_succeeded",
				Output:     map[string]any{"result": "ok"},
			}},
		},
	}

	baseline, err := agentcontext.NewEngine(agentcontext.BudgetConfig{
		MaxRequestTokens:          262144,
		MaxUserMessageTokens:      262144,
		MaxDefinitionTokens:       262144,
		MaxObservationTokens:      262144,
		MaxEventTokens:            262144,
		MaxContextFactsTokens:     262144,
		MaxRecentMemoryTokens:     262144,
		MaxTranscriptTokens:       262144,
		MaxToolResultOutputTokens: 262144,
	}).Build(input)
	if err != nil {
		t.Fatalf("baseline Build returned error: %v", err)
	}
	latestWithoutMemory := baseline.Projection
	latestWithoutMemory.RecentMemory = nil
	latestWithoutMemory.CurrentTurnTranscript = baseline.Projection.CurrentTurnTranscript[2:]
	fitSize := measureProjectionForTest(t, latestWithoutMemory)

	engine := agentcontext.NewEngine(agentcontext.BudgetConfig{
		MaxRequestTokens:          fitSize.TotalEstimatedTokens,
		MaxUserMessageTokens:      262144,
		MaxDefinitionTokens:       262144,
		MaxObservationTokens:      262144,
		MaxEventTokens:            262144,
		MaxContextFactsTokens:     262144,
		MaxRecentMemoryTokens:     262144,
		MaxTranscriptTokens:       262144,
		MaxToolResultOutputTokens: 262144,
	})
	result, err := engine.Build(input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	req, err := agentcontext.NewRenderer().Render(result.Projection)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	size := estimateRequestTokensForTest(t, req)
	if agentcontext.RequestEstimatedTokensExceedBudget(size, result.Report.EffectiveBudget) {
		t.Fatalf("rendered request still exceeds budget: size=%+v budget=%+v\n%s", size, result.Report.EffectiveBudget, req.Messages[0].Content)
	}
	content := renderRequestText(req)
	assertStringContainsAll(t, content, "agent-core", "call_latest")
	for _, leaked := range []string{"memory-secret", "old-transcript-secret", "old-output-secret"} {
		if strings.Contains(content, leaked) {
			t.Fatalf("globally trimmed request leaked %q:\n%s", leaked, content)
		}
	}
	if len(result.Projection.RecentMemory) != 0 {
		t.Fatalf("RecentMemory = %+v, want dropped under global pressure", result.Projection.RecentMemory)
	}
	if got, want := len(result.Projection.CurrentTurnTranscript), 2; got != want {
		t.Fatalf("CurrentTurnTranscript length = %d, want latest causal group", got)
	}
	if !reportHasReason(result.Report, agentcontext.ReasonMemoryBudgetExceeded) ||
		!reportHasReason(result.Report, agentcontext.ReasonTranscriptBudgetExceeded) {
		t.Fatalf("ReasonCodes = %v, want memory and transcript budget reasons", result.Report.ReasonCodes)
	}
}

func TestEngineBuildTrimsOlderTranscriptBeforeRecentMemoryUnderGlobalBudget(t *testing.T) {
	input := validEngineInput(t)
	input.RecentMemories = []memory.Record{{
		MemoryID: "memory-keep",
		SourceContextFacts: []memory.SourceContextFact{{
			Kind:          "utterance",
			ActorEntityID: "player:local",
			Text:          strings.Repeat("memory-keep", 40),
		}},
	}}
	input.Transcript = []model.Message{
		{
			Role: model.RoleAssistant,
			ToolCalls: []model.ToolCall{{
				ID:        "call_old",
				Name:      "inspect",
				Arguments: map[string]any{"query": strings.Repeat("old-transcript", 40)},
			}},
		},
		{
			Role: model.RoleTool,
			ToolResults: []model.ToolResult{{
				ToolCallID: "call_old",
				Name:       "inspect",
				Status:     "succeeded",
				Code:       "action_succeeded",
				Output:     map[string]any{"result": strings.Repeat("old-output", 40)},
			}},
		},
		{
			Role: model.RoleAssistant,
			ToolCalls: []model.ToolCall{{
				ID:        "call_latest",
				Name:      "inspect",
				Arguments: map[string]any{"query": "latest"},
			}},
		},
		{
			Role: model.RoleTool,
			ToolResults: []model.ToolResult{{
				ToolCallID: "call_latest",
				Name:       "inspect",
				Status:     "succeeded",
				Code:       "action_succeeded",
				Output:     map[string]any{"result": "ok"},
			}},
		},
	}

	baseline, err := agentcontext.NewEngine(agentcontext.BudgetConfig{
		MaxRequestTokens:          262144,
		MaxUserMessageTokens:      262144,
		MaxRecentMemoryTokens:     262144,
		MaxTranscriptTokens:       262144,
		MaxToolResultOutputTokens: 262144,
	}).Build(input)
	if err != nil {
		t.Fatalf("baseline Build returned error: %v", err)
	}
	fullSize := measureProjectionForTest(t, baseline.Projection)

	withoutOldTranscript := baseline.Projection
	withoutOldTranscript.CurrentTurnTranscript = baseline.Projection.CurrentTurnTranscript[2:]
	afterTranscriptTrim := measureProjectionForTest(t, withoutOldTranscript)

	withoutMemory := baseline.Projection
	withoutMemory.RecentMemory = nil
	afterMemoryDrop := measureProjectionForTest(t, withoutMemory)

	budget := maxInt(afterTranscriptTrim.TotalEstimatedTokens, afterMemoryDrop.TotalEstimatedTokens)
	if budget >= fullSize.TotalEstimatedTokens {
		t.Fatalf("test setup did not create budget pressure: full=%+v after_transcript=%+v after_memory=%+v", fullSize, afterTranscriptTrim, afterMemoryDrop)
	}

	result, err := agentcontext.NewEngine(agentcontext.BudgetConfig{
		MaxRequestTokens:          budget,
		MaxUserMessageTokens:      262144,
		MaxRecentMemoryTokens:     262144,
		MaxTranscriptTokens:       262144,
		MaxToolResultOutputTokens: 262144,
	}).Build(input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if got, want := len(result.Projection.RecentMemory), 1; got != want {
		t.Fatalf("RecentMemory length = %d, want %d", got, want)
	}
	if result.Projection.RecentMemory[0].MemoryID != "memory-keep" {
		t.Fatalf("retained memory = %q, want memory-keep", result.Projection.RecentMemory[0].MemoryID)
	}
	if got, want := len(result.Projection.CurrentTurnTranscript), 2; got != want {
		t.Fatalf("CurrentTurnTranscript length = %d, want latest causal group", got)
	}
	if result.Projection.CurrentTurnTranscript[0].ToolCalls[0].ID != "call_latest" {
		t.Fatalf("retained transcript = %+v, want latest group", result.Projection.CurrentTurnTranscript)
	}
	if !reportHasReason(result.Report, agentcontext.ReasonTranscriptBudgetExceeded) {
		t.Fatalf("ReasonCodes = %v, want transcript budget reason", result.Report.ReasonCodes)
	}
	if reportHasReason(result.Report, agentcontext.ReasonMemoryBudgetExceeded) {
		t.Fatalf("ReasonCodes = %v, want no memory budget reason", result.Report.ReasonCodes)
	}
}

func TestEngineBuildDropsOnlyOldestTranscriptGroupWhenThatFitsGlobalBudget(t *testing.T) {
	input := validEngineInput(t)
	input.RecentMemories = []memory.Record{{
		MemoryID: "memory-keep",
		SourceContextFacts: []memory.SourceContextFact{{
			Kind:          "utterance",
			ActorEntityID: "player:local",
			Text:          "keep this memory",
		}},
	}}
	input.Transcript = []model.Message{
		transcriptCallMessage("call_oldest", strings.Repeat("oldest-transcript", 35)),
		transcriptResultMessage("call_oldest", strings.Repeat("oldest-output", 35)),
		transcriptCallMessage("call_middle", strings.Repeat("middle-transcript", 20)),
		transcriptResultMessage("call_middle", strings.Repeat("middle-output", 20)),
		transcriptCallMessage("call_latest", "latest"),
		transcriptResultMessage("call_latest", "ok"),
	}

	baseline, err := agentcontext.NewEngine(agentcontext.BudgetConfig{
		MaxRequestTokens:          262144,
		MaxUserMessageTokens:      262144,
		MaxRecentMemoryTokens:     262144,
		MaxTranscriptTokens:       262144,
		MaxToolResultOutputTokens: 262144,
	}).Build(input)
	if err != nil {
		t.Fatalf("baseline Build returned error: %v", err)
	}
	fullSize := measureProjectionForTest(t, baseline.Projection)

	withoutOldestGroup := baseline.Projection
	withoutOldestGroup.CurrentTurnTranscript = baseline.Projection.CurrentTurnTranscript[2:]
	afterOneTranscriptGroupDrop := measureProjectionForTest(t, withoutOldestGroup)
	if afterOneTranscriptGroupDrop.TotalEstimatedTokens >= fullSize.TotalEstimatedTokens {
		t.Fatalf("test setup did not create transcript pressure: full=%+v after_one_drop=%+v", fullSize, afterOneTranscriptGroupDrop)
	}

	result, err := agentcontext.NewEngine(agentcontext.BudgetConfig{
		MaxRequestTokens:          afterOneTranscriptGroupDrop.TotalEstimatedTokens,
		MaxUserMessageTokens:      262144,
		MaxRecentMemoryTokens:     262144,
		MaxTranscriptTokens:       262144,
		MaxToolResultOutputTokens: 262144,
	}).Build(input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if got, want := len(result.Projection.RecentMemory), 1; got != want {
		t.Fatalf("RecentMemory length = %d, want %d", got, want)
	}
	if got, want := len(result.Projection.CurrentTurnTranscript), 4; got != want {
		t.Fatalf("CurrentTurnTranscript length = %d, want middle and latest groups", got)
	}
	retainedIDs := []string{
		result.Projection.CurrentTurnTranscript[0].ToolCalls[0].ID,
		result.Projection.CurrentTurnTranscript[2].ToolCalls[0].ID,
	}
	if want := []string{"call_middle", "call_latest"}; !reflect.DeepEqual(retainedIDs, want) {
		t.Fatalf("retained transcript IDs = %v, want %v", retainedIDs, want)
	}
	if result.Report.Transcript.DroppedCount != 2 || result.Report.Transcript.RetainedCount != 4 {
		t.Fatalf("Transcript report = %+v, want one dropped group and two retained groups", result.Report.Transcript)
	}
	if !reportHasReason(result.Report, agentcontext.ReasonTranscriptBudgetExceeded) {
		t.Fatalf("ReasonCodes = %v, want transcript budget reason", result.Report.ReasonCodes)
	}
	if reportHasReason(result.Report, agentcontext.ReasonMemoryBudgetExceeded) {
		t.Fatalf("ReasonCodes = %v, want memory untouched", result.Report.ReasonCodes)
	}
}

func TestEngineBuildFailsWhenDefinitionRequiredMinimumExceedsBudget(t *testing.T) {
	engine := agentcontext.NewEngine(agentcontext.BudgetConfig{MaxDefinitionTokens: 1})
	input := validEngineInput(t)

	result, err := engine.Build(input)
	if !errors.Is(err, agentcontext.ErrBudgetExceeded) {
		t.Fatalf("Build error = %v, want ErrBudgetExceeded", err)
	}
	if !reportHasReason(result.Report, agentcontext.ReasonDefinitionBudgetExceeded) ||
		!reportHasReason(result.Report, agentcontext.ReasonRequiredContextOverBudget) {
		t.Fatalf("ReasonCodes = %v, want definition and required context budget reasons", result.Report.ReasonCodes)
	}
	if reportHasReason(result.Report, agentcontext.ReasonRequiredSectionOverBudget) {
		t.Fatalf("ReasonCodes = %v, want no required section reason for definition budget", result.Report.ReasonCodes)
	}
}

func TestEngineBuildCropsStructuredSectionsWithoutInvalidJSON(t *testing.T) {
	engine := agentcontext.NewEngine(agentcontext.BudgetConfig{
		MaxEventTokens:            180,
		MaxObservationTokens:      80,
		MaxContextFactsTokens:     120,
		MaxToolResultOutputTokens: 256,
	})
	input := validEngineInput(t)
	input.Event.Payload = mustStruct(t, map[string]any{
		"dialogue_id": "intro",
		"large":       strings.Repeat("event-secret", 40),
	})
	input.Event.ContextFacts = []*protocolv1alpha2.ContextFact{{
		Kind:           "utterance",
		ActorEntityId:  "player:local",
		TargetEntityId: "npc:Abigail",
		ScopeId:        "conversation:1",
		Label:          "greeting",
		Text:           strings.Repeat("fact-secret", 40),
		Attributes: mustStruct(t, map[string]any{
			"large": strings.Repeat("attribute-secret", 40),
		}),
	}}
	input.Observation.State = mustStruct(t, map[string]any{
		"weather": "sunny",
		"large":   strings.Repeat("observation-secret", 40),
	})

	result, err := engine.Build(input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	mustMarshalProjectionJSON(t, result.Projection.CurrentEvent.Payload)
	mustMarshalProjectionJSON(t, result.Projection.CurrentObservation.State)
	mustMarshalProjectionJSON(t, result.Projection.CurrentEventContextFacts)

	eventJSON := string(mustMarshalProjectionJSON(t, result.Projection.CurrentEvent.Payload))
	observationJSON := string(mustMarshalProjectionJSON(t, result.Projection.CurrentObservation.State))
	factsJSON := string(mustMarshalProjectionJSON(t, result.Projection.CurrentEventContextFacts))
	for _, leaked := range []string{"event-secret", "observation-secret", "fact-secret", "attribute-secret"} {
		if strings.Contains(eventJSON+observationJSON+factsJSON, leaked) {
			t.Fatalf("cropped projection leaked %q:\nevent=%s\nobservation=%s\nfacts=%s", leaked, eventJSON, observationJSON, factsJSON)
		}
	}

	fact := result.Projection.CurrentEventContextFacts[0]
	if fact.Kind != "utterance" ||
		fact.ActorEntityID != "player:local" ||
		fact.TargetEntityID != "npc:Abigail" ||
		fact.ScopeID != "conversation:1" ||
		fact.Label != "greeting" {
		t.Fatalf("fact identity = %+v, want identity fields preserved", fact)
	}
	if fact.Text != "_truncated" {
		t.Fatalf("fact text = %q, want short truncation marker", fact.Text)
	}
}

func TestEngineBuildDropsContextFactAttributesBeforeShortText(t *testing.T) {
	input := validEngineInput(t)
	input.Event.ContextFacts = []*protocolv1alpha2.ContextFact{{
		Kind:           "utterance",
		ActorEntityId:  "player:local",
		TargetEntityId: "npc:Abigail",
		ScopeId:        "conversation:1",
		Label:          "birthday",
		Text:           "生日快乐",
		Attributes: mustStruct(t, map[string]any{
			"large": strings.Repeat("attribute-secret", 80),
		}),
	}}
	expectedFacts := []agentcontext.ContextFactProjection{{
		Kind:           "utterance",
		ActorEntityID:  "player:local",
		TargetEntityID: "npc:Abigail",
		ScopeID:        "conversation:1",
		Label:          "birthday",
		Text:           "生日快乐",
	}}
	limit, err := tokenestimate.EstimateStableJSON(expectedFacts)
	if err != nil {
		t.Fatalf("EstimateStableJSON(expected facts) error = %v", err)
	}

	result, err := agentcontext.NewEngine(agentcontext.BudgetConfig{
		MaxContextFactsTokens: limit,
	}).Build(input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	fact := result.Projection.CurrentEventContextFacts[0]
	if fact.Text != "生日快乐" {
		t.Fatalf("fact text = %q, want short text preserved after attributes are dropped", fact.Text)
	}
	if len(fact.Attributes) != 0 {
		t.Fatalf("fact attributes = %+v, want dropped", fact.Attributes)
	}
	section := reportSection(t, result.Report, "current_event_context_facts")
	if !section.Cropped || section.Reason != agentcontext.ReasonContextFactsBudgetExceeded {
		t.Fatalf("context fact section = %+v, want cropped with context facts reason", section)
	}
}

func TestEngineBuildFailsWhenContextFactIdentityAndMarkerExceedSectionBudget(t *testing.T) {
	input := validEngineInput(t)
	input.Event.ContextFacts = []*protocolv1alpha2.ContextFact{{
		Kind:           "utterance",
		ActorEntityId:  "player:local",
		TargetEntityId: "npc:Abigail",
		ScopeId:        "conversation:1",
		Label:          "greeting",
		Text:           strings.Repeat("fact-secret", 40),
	}}
	markerProjection := agentcontext.ContextProjection{
		CurrentEventContextFacts: []agentcontext.ContextFactProjection{{
			Kind:           "utterance",
			ActorEntityID:  "player:local",
			TargetEntityID: "npc:Abigail",
			ScopeID:        "conversation:1",
			Label:          "greeting",
			Text:           "_truncated",
		}},
	}
	requiredMarkerTokens, err := tokenestimate.EstimateStableJSON(markerProjection.CurrentEventContextFacts)
	if err != nil {
		t.Fatalf("EstimateStableJSON returned error: %v", err)
	}
	requiredMarkerTokens--
	result, err := agentcontext.NewEngine(agentcontext.BudgetConfig{
		MaxContextFactsTokens: requiredMarkerTokens,
	}).Build(input)
	if !errors.Is(err, agentcontext.ErrBudgetExceeded) {
		t.Fatalf("Build error = %v, want ErrBudgetExceeded", err)
	}
	if !reportHasReason(result.Report, agentcontext.ReasonContextFactsBudgetExceeded) ||
		!reportHasReason(result.Report, agentcontext.ReasonRequiredContextOverBudget) ||
		!reportHasReason(result.Report, agentcontext.ReasonRequiredSectionOverBudget) {
		t.Fatalf("ReasonCodes = %v, want context facts required-section budget reasons", result.Report.ReasonCodes)
	}
}

func TestEngineBuildDropsStructuredTruncationMarkerBeforeFailingRequiredMinimum(t *testing.T) {
	baselineInput := validEngineInput(t)
	baselineInput.Event.Payload = nil
	baseline, err := agentcontext.NewEngine(agentcontext.BudgetConfig{}).Build(baselineInput)
	if err != nil {
		t.Fatalf("baseline Build returned error: %v", err)
	}
	eventShellTokens := reportSectionProjectionEstimatedTokens(t, baseline.Report, "current_event")

	engine := agentcontext.NewEngine(agentcontext.BudgetConfig{MaxEventTokens: eventShellTokens})
	input := validEngineInput(t)
	input.Event.Payload = mustStruct(t, map[string]any{
		"large": strings.Repeat("event-secret", 40),
	})

	result, err := engine.Build(input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(result.Projection.CurrentEvent.Payload) != 0 {
		t.Fatalf("CurrentEvent.Payload = %+v, want dropped optional payload", result.Projection.CurrentEvent.Payload)
	}
	if got := reportSectionProjectionEstimatedTokens(t, result.Report, "current_event"); got > eventShellTokens {
		t.Fatalf("current_event proxy tokens = %d, want <= %d", got, eventShellTokens)
	}
	if !reportHasReason(result.Report, agentcontext.ReasonEventBudgetExceeded) {
		t.Fatalf("ReasonCodes = %v, want event budget reason", result.Report.ReasonCodes)
	}
	if reportHasReason(result.Report, agentcontext.ReasonRequiredSectionOverBudget) ||
		reportHasReason(result.Report, agentcontext.ReasonRequiredContextOverBudget) {
		t.Fatalf("ReasonCodes = %v, want no required over-budget reasons", result.Report.ReasonCodes)
	}
}

func TestEngineBuildFailsWhenRequiredEventShellExceedsSectionBudget(t *testing.T) {
	engine := agentcontext.NewEngine(agentcontext.BudgetConfig{MaxEventTokens: 1})
	input := validEngineInput(t)
	input.Event.Payload = mustStruct(t, map[string]any{
		"large": strings.Repeat("event-secret", 40),
	})

	result, err := engine.Build(input)
	if !errors.Is(err, agentcontext.ErrBudgetExceeded) {
		t.Fatalf("Build error = %v, want ErrBudgetExceeded", err)
	}
	if !reportHasReason(result.Report, agentcontext.ReasonEventBudgetExceeded) ||
		!reportHasReason(result.Report, agentcontext.ReasonRequiredContextOverBudget) ||
		!reportHasReason(result.Report, agentcontext.ReasonRequiredSectionOverBudget) {
		t.Fatalf("ReasonCodes = %v, want event and required section budget reasons", result.Report.ReasonCodes)
	}
}

func TestEngineBuildPreservesLatestTranscriptCausalGroupWithinBudget(t *testing.T) {
	engine := agentcontext.NewEngine(agentcontext.BudgetConfig{MaxTranscriptTokens: 512})
	input := validEngineInput(t)
	input.Transcript = []model.Message{
		{
			Role: model.RoleAssistant,
			ToolCalls: []model.ToolCall{{
				ID:        "call_old",
				Name:      "inspect",
				Arguments: map[string]any{"query": strings.Repeat("old", 80)},
			}},
		},
		{
			Role: model.RoleTool,
			ToolResults: []model.ToolResult{{
				ToolCallID: "call_old",
				Name:       "inspect",
				Status:     "succeeded",
				Code:       "action_succeeded",
				Output:     map[string]any{"result": strings.Repeat("old-output", 40)},
			}},
		},
		{
			Role: model.RoleAssistant,
			ToolCalls: []model.ToolCall{{
				ID:        "call_new",
				Name:      "present_dialogue",
				Arguments: map[string]any{"text": "Hi."},
			}},
		},
		{
			Role: model.RoleTool,
			ToolResults: []model.ToolResult{{
				ToolCallID: "call_new",
				Name:       "present_dialogue",
				Status:     "rejected",
				Code:       "adapter_rejected",
				Message:    "player is busy",
			}},
		},
	}

	result, err := engine.Build(input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	transcript := result.Projection.CurrentTurnTranscript
	if got, want := len(transcript), 2; got != want {
		t.Fatalf("CurrentTurnTranscript length = %d, want latest causal group only", got)
	}
	if transcript[0].ToolCalls[0].ID != "call_new" || transcript[1].ToolResults[0].ToolCallID != "call_new" {
		t.Fatalf("retained transcript = %+v, want call_new group", transcript)
	}
	if result.Report.Transcript.DroppedCount != 2 {
		t.Fatalf("Transcript.DroppedCount = %d, want two old messages dropped", result.Report.Transcript.DroppedCount)
	}
	transcriptSection := reportSection(t, result.Report, "current_turn_transcript")
	if !transcriptSection.Cropped || transcriptSection.Reason != agentcontext.ReasonTranscriptBudgetExceeded {
		t.Fatalf("current_turn_transcript section = %+v, want cropped with transcript budget reason", transcriptSection)
	}
	if !reportHasReason(result.Report, agentcontext.ReasonTranscriptBudgetExceeded) {
		t.Fatalf("ReasonCodes = %v, want transcript budget reason", result.Report.ReasonCodes)
	}
}

func TestEngineBuildFailsWhenLatestTranscriptCausalGroupExceedsBudget(t *testing.T) {
	engine := agentcontext.NewEngine(agentcontext.BudgetConfig{
		MaxTranscriptTokens:       64,
		MaxToolResultOutputTokens: 4096,
	})
	input := validEngineInput(t)
	input.Transcript = []model.Message{
		{
			Role: model.RoleAssistant,
			ToolCalls: []model.ToolCall{{
				ID:        "call_latest",
				Name:      "inspect",
				Arguments: map[string]any{"query": strings.Repeat("latest-query", 20)},
			}},
		},
		{
			Role: model.RoleTool,
			ToolResults: []model.ToolResult{{
				ToolCallID: "call_latest",
				Name:       "inspect",
				Status:     "succeeded",
				Code:       "action_succeeded",
				Output:     map[string]any{"result": strings.Repeat("latest-output", 20)},
			}},
		},
	}

	result, err := engine.Build(input)
	if !errors.Is(err, agentcontext.ErrBudgetExceeded) {
		t.Fatalf("Build error = %v, want ErrBudgetExceeded", err)
	}
	if result.Report.Transcript.RetainedCount != 0 || result.Report.Transcript.DroppedCount != 2 {
		t.Fatalf("Transcript report = %+v, want no retained messages and two dropped messages", result.Report.Transcript)
	}
	if !reportHasReason(result.Report, agentcontext.ReasonTranscriptBudgetExceeded) ||
		!reportHasReason(result.Report, agentcontext.ReasonRequiredContextOverBudget) {
		t.Fatalf("ReasonCodes = %v, want transcript and required context budget reasons", result.Report.ReasonCodes)
	}
	if reportHasReason(result.Report, agentcontext.ReasonRequiredSectionOverBudget) {
		t.Fatalf("ReasonCodes = %v, want no required section reason for transcript budget", result.Report.ReasonCodes)
	}
}

func TestEngineBuildRejectsIncompleteTranscriptToolCallGroup(t *testing.T) {
	input := validEngineInput(t)
	input.Transcript = []model.Message{{
		Role: model.RoleAssistant,
		ToolCalls: []model.ToolCall{{
			ID:        "call_pending",
			Name:      "inspect",
			Arguments: map[string]any{"query": "where am I"},
		}},
	}}

	_, err := agentcontext.NewEngine(agentcontext.BudgetConfig{}).Build(input)
	assertInvalidInputError(t, err, "transcript tool call has no corresponding result")
}

func TestEngineBuildRejectsTranscriptDuplicateResultIDs(t *testing.T) {
	input := validEngineInput(t)
	input.Transcript = []model.Message{
		{
			Role: model.RoleAssistant,
			ToolCalls: []model.ToolCall{
				{ID: "call_a", Name: "inspect", Arguments: map[string]any{"query": "a"}},
				{ID: "call_b", Name: "inspect", Arguments: map[string]any{"query": "b"}},
			},
		},
		{
			Role: model.RoleTool,
			ToolResults: []model.ToolResult{
				{ToolCallID: "call_a", Name: "inspect", Status: "succeeded"},
				{ToolCallID: "call_a", Name: "inspect", Status: "succeeded"},
			},
		},
	}

	_, err := agentcontext.NewEngine(agentcontext.BudgetConfig{}).Build(input)
	assertInvalidInputError(t, err, "transcript tool call has no corresponding result")
}

func TestEngineBuildRejectsTranscriptDuplicateCallIDs(t *testing.T) {
	input := validEngineInput(t)
	input.Transcript = []model.Message{
		{
			Role: model.RoleAssistant,
			ToolCalls: []model.ToolCall{
				{ID: "call_a", Name: "inspect", Arguments: map[string]any{"query": "a"}},
				{ID: "call_a", Name: "inspect", Arguments: map[string]any{"query": "again"}},
			},
		},
		{
			Role: model.RoleTool,
			ToolResults: []model.ToolResult{
				{ToolCallID: "call_a", Name: "inspect", Status: "succeeded"},
				{ToolCallID: "call_a", Name: "inspect", Status: "rejected"},
			},
		},
	}

	_, err := agentcontext.NewEngine(agentcontext.BudgetConfig{}).Build(input)
	assertInvalidInputError(t, err, "transcript tool call has no corresponding result")
}

func TestEngineBuildAcceptsTranscriptResultsMatchedByIDOutOfOrder(t *testing.T) {
	input := validEngineInput(t)
	input.Transcript = []model.Message{
		{
			Role: model.RoleAssistant,
			ToolCalls: []model.ToolCall{
				{ID: "call_a", Name: "inspect", Arguments: map[string]any{"query": "a"}},
				{ID: "call_b", Name: "inspect", Arguments: map[string]any{"query": "b"}},
			},
		},
		{
			Role: model.RoleTool,
			ToolResults: []model.ToolResult{
				{ToolCallID: "call_b", Name: "inspect", Status: "succeeded"},
				{ToolCallID: "call_a", Name: "inspect", Status: "succeeded"},
			},
		},
	}

	result, err := agentcontext.NewEngine(agentcontext.BudgetConfig{}).Build(input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if got, want := len(result.Projection.CurrentTurnTranscript), 2; got != want {
		t.Fatalf("CurrentTurnTranscript length = %d, want %d", got, want)
	}
}

func TestEstimateRequestTokensIsDeterministicForFixedRequest(t *testing.T) {
	req := model.Request{
		System: "policy",
		Messages: []model.Message{
			{
				Role:    model.RoleUser,
				Content: "rendered context",
			},
			{
				Role: model.RoleAssistant,
				ToolCalls: []model.ToolCall{{
					ID:        "call_1",
					Name:      "inspect",
					Arguments: map[string]any{"b": 2, "a": 1},
				}},
			},
		},
		Tools: []model.ToolDefinition{{
			Name:        "inspect",
			Description: "Inspect state.",
			InputSchema: `{"type":"object","properties":{"query":{"type":"string"}}}`,
		}},
		Controls: []model.ControlDefinition{{
			Kind:        model.ControlSettle,
			Description: "settle",
		}},
	}

	first := estimateRequestTokensForTest(t, req)
	second := estimateRequestTokensForTest(t, req)
	if first != second {
		t.Fatalf("EstimateRequestTokens was not deterministic:\nfirst=%+v\nsecond=%+v", first, second)
	}
	if first.UserMessageEstimatedTokens != 4 {
		t.Fatalf("UserMessageEstimatedTokens = %d, want rendered user content estimated tokens", first.UserMessageEstimatedTokens)
	}
	if first.TotalEstimatedTokens <= 0 || !agentcontext.RequestEstimatedTokensExceedBudget(first, agentcontext.BudgetConfig{MaxRequestTokens: first.TotalEstimatedTokens - 1}.WithDefaults()) {
		t.Fatalf("request sizing did not enforce total budget: %+v", first)
	}
}

func TestEstimateRequestTokensCountsMessageContentWithoutJSONEscaping(t *testing.T) {
	content := strings.Repeat("\"\\\n", 20)
	req := model.Request{
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: content,
		}},
	}

	size := estimateRequestTokensForTest(t, req)
	contentTokens := tokenestimate.EstimateText(content)
	structureTokens, err := tokenestimate.EstimateStableJSON([]map[string]any{{"role": model.RoleUser}})
	if err != nil {
		t.Fatalf("EstimateStableJSON(structure) error = %v", err)
	}
	if size.UserMessageEstimatedTokens != contentTokens {
		t.Fatalf("UserMessageEstimatedTokens = %d, want content estimate %d", size.UserMessageEstimatedTokens, contentTokens)
	}
	if size.MessagesEstimatedTokens != contentTokens+structureTokens {
		t.Fatalf("MessagesEstimatedTokens = %d, want content %d + structure %d", size.MessagesEstimatedTokens, contentTokens, structureTokens)
	}
}

func TestEstimateRequestTokensDoesNotDoubleCountRenderedToolTranscript(t *testing.T) {
	content := `[{"id":"call_1","name":"inspect","arguments":{"topic":"生日快乐"}}]`
	plain := model.Request{
		Messages: []model.Message{{
			Role:    model.RoleAssistant,
			Content: content,
		}},
	}
	withAuxiliaryFields := model.Request{
		Messages: []model.Message{{
			Role:    model.RoleAssistant,
			Content: content,
			ToolCalls: []model.ToolCall{{
				ID:        "call_1",
				Name:      "inspect",
				Arguments: map[string]any{"topic": "生日快乐"},
			}},
		}},
	}

	plainSize := estimateRequestTokensForTest(t, plain)
	auxiliarySize := estimateRequestTokensForTest(t, withAuxiliaryFields)
	if auxiliarySize.MessagesEstimatedTokens != plainSize.MessagesEstimatedTokens {
		t.Fatalf("MessagesEstimatedTokens = %d with auxiliary transcript fields, want %d", auxiliarySize.MessagesEstimatedTokens, plainSize.MessagesEstimatedTokens)
	}
	if auxiliarySize.TotalEstimatedTokens != plainSize.TotalEstimatedTokens {
		t.Fatalf("TotalEstimatedTokens = %d with auxiliary transcript fields, want %d", auxiliarySize.TotalEstimatedTokens, plainSize.TotalEstimatedTokens)
	}
}

func TestEstimateRequestTokensNormalizesToolInputSchemas(t *testing.T) {
	compactSchema := `{"description":"你好","type":"object"}`
	prettySchema := `{
  "type": "object",
  "description": "\u4f60\u597d"
}`
	compact := model.Request{
		Tools: []model.ToolDefinition{{
			Name:        "inspect",
			Description: "Inspect.",
			InputSchema: compactSchema,
		}},
	}
	pretty := model.Request{
		Tools: []model.ToolDefinition{{
			Name:        "inspect",
			Description: "Inspect.",
			InputSchema: prettySchema,
		}},
	}

	compactSize := estimateRequestTokensForTest(t, compact)
	prettySize := estimateRequestTokensForTest(t, pretty)
	if prettySize.ToolsEstimatedTokens != compactSize.ToolsEstimatedTokens {
		t.Fatalf("ToolsEstimatedTokens = %d for pretty schema, want compact estimate %d", prettySize.ToolsEstimatedTokens, compactSize.ToolsEstimatedTokens)
	}
	if prettySize.TotalEstimatedTokens != compactSize.TotalEstimatedTokens {
		t.Fatalf("TotalEstimatedTokens = %d for pretty schema, want compact estimate %d", prettySize.TotalEstimatedTokens, compactSize.TotalEstimatedTokens)
	}
}

func TestEstimateRequestTokensRejectsToolSchemaTrailingContent(t *testing.T) {
	_, err := agentcontext.EstimateRequestTokens(model.Request{
		Tools: []model.ToolDefinition{{
			Name:        "inspect",
			Description: "Inspect.",
			InputSchema: `{"type":"object"} trailing`,
		}},
	})
	if err == nil {
		t.Fatal("EstimateRequestTokens succeeded for schema with trailing content, want error")
	}
}

func validEngineInput(t *testing.T) agentcontext.BuildInput {
	t.Helper()

	key := session.AgentSessionKey{GameID: "stardew-valley", WorldID: "world-a", EntityID: "npc:Abigail"}
	target := &protocolv1alpha2.EntityRef{
		EntityId:     key.EntityID,
		EntityType:   "npc",
		DisplayName:  "Abigail",
		DefinitionId: "npc/abigail",
	}

	return agentcontext.BuildInput{
		SessionKey:      key,
		CanonicalTarget: target,
		AgentDescriptor: definition.NewAgentInstanceDescriptor(key, target),
		GameDefinition: &definition.GameDefinition{
			SchemaVersion: definition.SchemaVersionV1Alpha1,
			GameID:        key.GameID,
			Title:         "Stardew Valley",
		},
		AgentDefinition: &definition.AgentDefinition{
			SchemaVersion: definition.SchemaVersionV1Alpha1,
			GameID:        key.GameID,
			DefinitionID:  "npc/abigail",
			Identity:      "A purple-haired villager.",
		},
		RuntimePolicy: "runtime policy",
		Event: &protocolv1alpha2.GameEvent{
			EventId:        "event-1",
			EventType:      "player_interacted_with_npc",
			WorldId:        key.WorldID,
			TargetEntityId: key.EntityID,
		},
		Observation: &protocolv1alpha2.Observation{
			WorldId:  key.WorldID,
			EntityId: key.EntityID,
		},
		TurnToolView: singleToolView(t, "speak"),
	}
}

func singleToolView(t *testing.T, name string) tool.TurnToolView {
	t.Helper()

	catalog, _, err := tool.BuildEnvironmentToolCatalog(&protocolv1alpha2.CapabilityList{
		Capabilities: []*protocolv1alpha2.Capability{{
			Name:            name,
			Description:     "Run " + name,
			InputSchemaJson: `{"type":"object"}`,
		}},
	})
	if err != nil {
		t.Fatalf("BuildEnvironmentToolCatalog returned error: %v", err)
	}
	return catalog.Snapshot()
}

func assertInvalidInputError(t *testing.T, err error, want string) {
	t.Helper()

	if !errors.Is(err, agentcontext.ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want message containing %q", err, want)
	}
}

func assertStringContainsAll(t *testing.T, content string, values ...string) {
	t.Helper()

	for _, want := range values {
		if !strings.Contains(content, want) {
			t.Fatalf("content missing %q:\n%s", want, content)
		}
	}
}

func renderRequestText(req model.Request) string {
	parts := []string{req.System}
	for _, message := range req.Messages {
		parts = append(parts, message.Content)
	}
	return strings.Join(parts, "\n")
}

func measureProjectionForTest(t *testing.T, projection agentcontext.ContextProjection) agentcontext.RequestTokenSummary {
	t.Helper()

	req, err := agentcontext.NewRenderer().Render(projection)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	return estimateRequestTokensForTest(t, req)
}

func estimateRequestTokensForTest(t *testing.T, req model.Request) agentcontext.RequestTokenSummary {
	t.Helper()

	size, err := agentcontext.EstimateRequestTokens(req)
	if err != nil {
		t.Fatalf("EstimateRequestTokens returned error: %v", err)
	}
	return size
}

func transcriptCallMessage(callID string, query string) model.Message {
	return model.Message{
		Role: model.RoleAssistant,
		ToolCalls: []model.ToolCall{{
			ID:        callID,
			Name:      "inspect",
			Arguments: map[string]any{"query": query},
		}},
	}
}

func transcriptResultMessage(callID string, result string) model.Message {
	return model.Message{
		Role: model.RoleTool,
		ToolResults: []model.ToolResult{{
			ToolCallID: callID,
			Name:       "inspect",
			Status:     "succeeded",
			Code:       "action_succeeded",
			Output:     map[string]any{"result": result},
		}},
	}
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func reportHasReason(report agentcontext.ContextBuildReport, reason string) bool {
	for _, code := range report.ReasonCodes {
		if code == reason {
			return true
		}
	}
	return false
}

func reportSectionProjectionEstimatedTokens(t *testing.T, report agentcontext.ContextBuildReport, name string) int {
	t.Helper()

	return reportSection(t, report, name).ProjectionEstimatedTokens
}

func reportSection(t *testing.T, report agentcontext.ContextBuildReport, name string) agentcontext.SectionReport {
	t.Helper()

	for _, section := range report.Sections {
		if section.Name == name {
			return section
		}
	}
	t.Fatalf("section %q not found in %+v", name, report.Sections)
	return agentcontext.SectionReport{}
}

func mustMarshalProjectionJSON(t *testing.T, value any) []byte {
	t.Helper()

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal projection JSON: %v", err)
	}
	return data
}

func ptrInt64(value int64) *int64 {
	return &value
}
