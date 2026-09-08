package context_test

import (
	"encoding/json"
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

	"google.golang.org/protobuf/types/known/structpb"
)

func TestEngineUsesInjectedDescriptorInsteadOfScanningEventEntities(t *testing.T) {
	key := session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "creature:alpha"}

	agentCtx, err := buildProjection(t, agentcontext.BuildInput{
		SessionKey: key,
		AgentDescriptor: definition.AgentInstanceDescriptor{
			SessionKey:   key,
			EntityType:   "creature",
			DisplayName:  "Alpha",
			DefinitionID: "villager/farmer",
		},
		Event: &protocolv1alpha2.GameEvent{
			EventId:        "event-1",
			WorldId:        "world-a",
			TargetEntityId: "creature:alpha",
			Entities: []*protocolv1alpha2.EntityRef{
				{EntityId: "player:local", DefinitionId: "player/local"},
				{EntityId: "creature:alpha", DefinitionId: "villager/farmer"},
			},
		},
		Observation: &protocolv1alpha2.Observation{WorldId: "world-a", EntityId: "creature:alpha"},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	if agentCtx.AgentDescriptor.SessionKey.EntityID != "creature:alpha" {
		t.Fatalf("AgentDescriptor entity_id = %q, want creature:alpha", agentCtx.AgentDescriptor.SessionKey.EntityID)
	}
	if agentCtx.AgentDescriptor.DefinitionID != "villager/farmer" {
		t.Fatalf("AgentDescriptor.DefinitionID = %q, want villager/farmer", agentCtx.AgentDescriptor.DefinitionID)
	}
}

func TestEngineDoesNotReadDefinitionIDFromObservationState(t *testing.T) {
	agentCtx, err := buildProjection(t, agentcontext.BuildInput{
		SessionKey: session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "creature:alpha"},
		Event: &protocolv1alpha2.GameEvent{
			EventId:        "event-1",
			WorldId:        "world-a",
			TargetEntityId: "creature:alpha",
			Entities:       []*protocolv1alpha2.EntityRef{{EntityId: "creature:alpha"}},
		},
		Observation: &protocolv1alpha2.Observation{
			WorldId:  "world-a",
			EntityId: "creature:alpha",
			State: mustStruct(t, map[string]any{
				"definition_id": "legacy/observation-state",
			}),
		},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	if agentCtx.AgentDescriptor.DefinitionID != "" {
		t.Fatalf("AgentDescriptor.DefinitionID = %q, want empty", agentCtx.AgentDescriptor.DefinitionID)
	}
}

func TestEngineUsesInjectedDefinitionsAndDescriptor(t *testing.T) {
	renderer := agentcontext.NewRenderer()
	descriptor := definition.AgentInstanceDescriptor{
		SessionKey:   session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "creature:alpha"},
		EntityType:   "creature",
		DisplayName:  "Alpha",
		DefinitionID: "villager/farmer",
	}
	gameDefinition := definition.GameDefinition{
		SchemaVersion:        definition.SchemaVersionV1Alpha1,
		GameID:               "fake-game",
		Title:                "Fake Game",
		Summary:              "A test world.",
		WorldRules:           []string{"Time advances in turns."},
		Lore:                 []string{"Creatures live near the farm."},
		NarrativeConstraints: []string{"Stay grounded in observed facts."},
	}
	agentDefinition := definition.AgentDefinition{
		SchemaVersion:      definition.SchemaVersionV1Alpha1,
		GameID:             "fake-game",
		DefinitionID:       "villager/farmer",
		Identity:           "A farmer archetype.",
		Personality:        []string{"patient"},
		SpeechStyle:        []string{"plain"},
		Preferences:        []string{"fresh seeds"},
		BehaviorGuidelines: []string{"Answer as the current creature instance."},
	}

	agentCtx, err := buildProjection(t, agentcontext.BuildInput{
		SessionKey:      descriptor.SessionKey,
		AgentDescriptor: descriptor,
		GameDefinition:  &gameDefinition,
		AgentDefinition: &agentDefinition,
		RuntimePolicy:   "policy",
		Event:           &protocolv1alpha2.GameEvent{EventId: "event-1", WorldId: "world-a", TargetEntityId: "creature:alpha"},
		Observation:     &protocolv1alpha2.Observation{WorldId: "world-a", EntityId: "creature:alpha"},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	if agentCtx.AgentDescriptor.SessionKey.EntityID != "creature:alpha" {
		t.Fatalf("descriptor entity_id = %q, want creature:alpha", agentCtx.AgentDescriptor.SessionKey.EntityID)
	}
	if agentCtx.AgentDescriptor.DefinitionID != "villager/farmer" {
		t.Fatalf("descriptor definition_id = %q, want villager/farmer", agentCtx.AgentDescriptor.DefinitionID)
	}

	req, err := renderer.Render(agentCtx)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	assertContainsAll(
		t,
		req.Messages[0].Content,
		"[Game Definition]",
		"title: Fake Game",
		"summary: A test world.",
		"- Time advances in turns.",
		"[Agent Definition]",
		"identity: A farmer archetype.",
		"- Answer as the current creature instance.",
		"[Agent Descriptor]",
		"game_id: fake-game",
		"world_id: world-a",
		"entity_id: creature:alpha",
		"entity_type: creature",
		"display_name: Alpha",
		"definition_id: villager/farmer",
	)
}

func TestRendererOmitsFabricatedDefinitionsWhenFallbackIsUsed(t *testing.T) {
	renderer := agentcontext.NewRenderer()

	agentCtx, err := buildProjection(t, agentcontext.BuildInput{
		SessionKey: session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "creature:alpha"},
		AgentDescriptor: definition.AgentInstanceDescriptor{
			SessionKey:  session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "creature:alpha"},
			EntityType:  "creature",
			DisplayName: "Alpha",
		},
		RuntimePolicy: "policy with fallback npc style",
		Event:         &protocolv1alpha2.GameEvent{EventId: "event-1", WorldId: "world-a", TargetEntityId: "creature:alpha"},
		Observation:   &protocolv1alpha2.Observation{WorldId: "world-a", EntityId: "creature:alpha"},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	req, err := renderer.Render(agentCtx)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	content := req.Messages[0].Content
	assertContainsAll(t, content, "[Game Definition]\n(none)", "[Agent Definition]\n(none)", "definition_id: (unspecified)")
	for _, unwanted := range []string{
		"identity:",
		"personality:",
		"speech_style:",
		"preferences:",
		"behavior_guidelines:",
	} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("fallback render should not fabricate %q:\n%s", unwanted, content)
		}
	}
}

func TestRendererBuildsModelRequestWithMemoryObservationInstructionAndTools(t *testing.T) {
	renderer := agentcontext.NewRenderer()
	speakTool := model.ToolDefinition{Name: "speak", Description: "say text", InputSchema: `{"type":"object"}`}

	agentCtx, err := buildProjection(t, agentcontext.BuildInput{
		SessionKey: session.AgentSessionKey{GameID: "stardew-valley", WorldID: "world-a", EntityID: "npc:Abigail"},
		AgentDescriptor: definition.AgentInstanceDescriptor{
			SessionKey:   session.AgentSessionKey{GameID: "stardew-valley", WorldID: "world-a", EntityID: "npc:Abigail"},
			DefinitionID: "npc:Abigail",
		},
		RuntimePolicy: "You are controlling an NPC in a game.",
		Event: &protocolv1alpha2.GameEvent{
			EventId:        "event-1",
			EventType:      "player_interacted_with_npc",
			WorldId:        "world-a",
			TargetEntityId: "npc:Abigail",
			Entities: []*protocolv1alpha2.EntityRef{
				{EntityId: "npc:Abigail", DefinitionId: "npc:Abigail"},
			},
			GameTime: &protocolv1alpha2.GameTime{
				Year:   ptrInt32(1),
				Season: ptrInt32(1),
				Day:    ptrInt32(2),
				Hour:   ptrInt32(6),
				Minute: ptrInt32(20),
			},
		},
		Observation: &protocolv1alpha2.Observation{
			WorldId:  "world-a",
			EntityId: "npc:Abigail",
		},
		RecentMemories: []memory.Record{{
			MemoryID:      "mem-1",
			SourceTurnID:  "turn-1",
			SourceEventID: "event-1",
			EventType:     "player_interacted_with_npc",
			GameTime:      &memory.GameTimeSnapshot{Year: 1, Season: 1, Day: 2, Hour: 6, Minute: 20},
			Outcomes: []memory.TurnOutcome{{
				ToolName:      "speak",
				ToolArguments: map[string]any{"text": "hello from last turn"},
				ActionStatus:  "ACTION_STATUS_SUCCEEDED",
			}},
			CreatedAt: time.Unix(100, 0),
		}},
		TurnToolView: turnToolViewFromDefinitions(t, speakTool),
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	req, err := renderer.Render(agentCtx)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	if req.System != "You are controlling an NPC in a game." {
		t.Fatalf("System = %q", req.System)
	}
	if len(req.Tools) != 1 || req.Tools[0] != speakTool {
		t.Fatalf("Tools = %+v, want %+v", req.Tools, []model.ToolDefinition{speakTool})
	}
	if len(req.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(req.Messages))
	}
	if len(req.Controls) != 1 || req.Controls[0].Kind != model.ControlSettle {
		t.Fatalf("controls = %+v, want settle control", req.Controls)
	}
	content := req.Messages[0].Content
	for _, want := range []string{
		"[Recent Memory]",
		"today 06:20",
		`tool "speak" status "ACTION_STATUS_SUCCEEDED" arguments {"text":"hello from last turn"}`,
		"hello from last turn",
		"[Agent Descriptor]",
		"entity_id: npc:Abigail",
		"definition_id: npc:Abigail",
		"[Current Event]",
		"[Current Observation]",
		"[Instruction]",
		"Current Observation is the current truth.",
		"Recent Memory is historical context.",
		"from today",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("rendered content missing %q:\n%s", want, content)
		}
	}
	for _, unwanted := range []string{
		"mem-1",
		"source_turn_id",
		"action_status",
	} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("rendered content should not include storage field %q:\n%s", unwanted, content)
		}
	}
}

func TestRendererIncludesNestedObservationStateWithoutGameSpecificParser(t *testing.T) {
	renderer := agentcontext.NewRenderer()

	agentCtx, err := buildProjection(t, agentcontext.BuildInput{
		SessionKey:    session.AgentSessionKey{GameID: "stardew-valley", WorldID: "world-a", EntityID: "npc:Abigail"},
		RuntimePolicy: "You are controlling an NPC in a game.",
		Event: &protocolv1alpha2.GameEvent{
			EventId:        "event-1",
			WorldId:        "world-a",
			TargetEntityId: "npc:Abigail",
			Entities: []*protocolv1alpha2.EntityRef{
				{EntityId: "npc:Abigail", DefinitionId: "npc:Abigail"},
			},
		},
		Observation: &protocolv1alpha2.Observation{
			WorldId:  "world-a",
			EntityId: "npc:Abigail",
			State: mustStruct(t, map[string]any{
				"stardew": map[string]any{
					"schema_version": "0.1",
					"scene": map[string]any{
						"nearby_npcs": []any{
							map[string]any{"entity_id": "npc:Robin", "name": "Robin"},
						},
					},
					"conversation": map[string]any{
						"conversation_id": "conv_1",
						"active":          true,
						"recent_lines": []any{
							map[string]any{
								"role":              "npc",
								"speaker_entity_id": "npc:Abigail",
								"speaker_name":      "Abigail",
								"text":              "Want to explore the mines?",
								"time_of_day":       1820,
							},
						},
					},
				},
			}),
		},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	req, err := renderer.Render(agentCtx)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(req.Messages))
	}

	assertContainsAll(
		t,
		req.Messages[0].Content,
		"[Current Observation]",
		`"state":`,
		`"stardew":`,
		`"schema_version":`,
		`"0.1"`,
		`"nearby_npcs":`,
		`"npc:Robin"`,
		`"conversation_id":`,
		`"conv_1"`,
		`"recent_lines":`,
		`"Want to explore the mines?"`,
	)
}

func TestRendererConsumesContextProjectionOnly(t *testing.T) {
	renderer := agentcontext.NewRenderer()
	tool := model.ToolDefinition{Name: "speak", Description: "say text", InputSchema: `{"type":"object"}`}
	projection := agentcontext.ContextProjection{
		RuntimePolicy: "policy",
		Instruction:   "Use only the projected instruction.",
		AgentDescriptor: definition.AgentInstanceDescriptor{
			SessionKey:   session.AgentSessionKey{GameID: "stardew-valley", WorldID: "world-a", EntityID: "npc:Abigail"},
			EntityType:   "npc",
			DisplayName:  "Abigail",
			DefinitionID: "npc/abigail",
		},
		GameDefinition:  &definition.GameDefinition{Title: "Projected Game"},
		AgentDefinition: &definition.AgentDefinition{Identity: "Projected agent identity."},
		CurrentEvent: agentcontext.EventProjection{
			EventID:        "projected-event",
			EventType:      "projected_event",
			WorldID:        "world-a",
			TargetEntityID: "npc:Abigail",
			Payload:        map[string]any{"payload_value": "kept"},
		},
		CurrentEventContextFacts: []agentcontext.ContextFactProjection{{
			Kind:          "utterance",
			ActorEntityID: "player:local",
			Text:          "Projected fact.",
		}},
		CurrentObservation: agentcontext.ObservationProjection{
			WorldID:  "world-a",
			EntityID: "npc:Abigail",
			Revision: 9,
			NearbyEntities: []*protocolv1alpha2.EntityRef{{
				EntityId:     "player:local",
				EntityType:   "player",
				DisplayName:  "Player",
				DefinitionId: "player/local",
			}},
			Extensions: map[string]any{"adapter_revision": "fixture-1"},
			State:      map[string]any{"weather": "sunny"},
		},
		RecentMemory: []agentcontext.MemoryProjection{{
			TimeRelation: "today 06:20",
			Summaries:    []string{"projected memory summary"},
		}},
		CurrentTurnTranscript: []model.Message{{
			Role: model.RoleTool,
			ToolResults: []model.ToolResult{{
				ToolCallID: "call_projected",
				Name:       "speak",
				Status:     "succeeded",
				Code:       "action_succeeded",
				Output:     map[string]any{"line": "projected transcript"},
			}},
		}},
		Tools: []model.ToolDefinition{tool},
	}

	req, err := renderer.Render(projection)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	content := req.Messages[0].Content
	assertContainsAll(t, content,
		"[Recent Memory]",
		"projected memory summary",
		"[Current Event]",
		`"event_id": "projected-event"`,
		"[Current Event Context Facts]",
		`"text": "Projected fact."`,
		"[Current Observation]",
		`"revision": 9`,
		`"nearby_entities":`,
		`"entity_id": "player:local"`,
		`"extensions":`,
		`"adapter_revision": "fixture-1"`,
		`"weather": "sunny"`,
		"[Instruction]",
		"Use only the projected instruction.",
	)
	if strings.Contains(content, "Current Observation is the current truth.") {
		t.Fatalf("renderer injected the default instruction instead of consuming projection.Instruction:\n%s", content)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("message count = %d, want user context plus projected transcript", len(req.Messages))
	}
	assertContainsAll(t, req.Messages[1].Content, "call_projected", "projected transcript")
}

func TestRendererDoesNotInventInstructionWhenProjectionInstructionIsEmpty(t *testing.T) {
	renderer := agentcontext.NewRenderer()
	projection := agentcontext.ContextProjection{
		RuntimePolicy: "policy",
		CurrentEvent: agentcontext.EventProjection{
			EventID:        "projected-event",
			WorldID:        "world-a",
			TargetEntityID: "npc:Abigail",
		},
		CurrentObservation: agentcontext.ObservationProjection{
			WorldID:  "world-a",
			EntityID: "npc:Abigail",
		},
	}

	req, err := renderer.Render(projection)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	content := req.Messages[0].Content
	assertContainsAll(t, content, "[Instruction]")
	if strings.Contains(content, "Current Observation is the current truth.") ||
		strings.Contains(content, "Recent Memory is historical context.") ||
		strings.Contains(content, "Return tool calls only when an environment action is needed.") {
		t.Fatalf("renderer invented an instruction for empty projection.Instruction:\n%s", content)
	}
}

func TestRendererIncludesBatchToolCallTranscriptMessages(t *testing.T) {
	renderer := agentcontext.NewRenderer()

	agentCtx, err := buildProjection(t, agentcontext.BuildInput{
		SessionKey:    session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "npc:Abigail"},
		RuntimePolicy: "policy",
		Event:         &protocolv1alpha2.GameEvent{EventId: "event-1", TargetEntityId: "npc:Abigail"},
		Observation:   &protocolv1alpha2.Observation{WorldId: "world-a", EntityId: "npc:Abigail"},
		Transcript: []model.Message{
			{
				Role: model.RoleAssistant,
				ToolCalls: []model.ToolCall{
					{ID: "call_1", Name: "speak", Arguments: map[string]any{"text": "Hello."}},
					{ID: "call_2", Name: "emote", Arguments: map[string]any{"emote": "happy"}},
				},
			},
			{
				Role: model.RoleTool,
				ToolResults: []model.ToolResult{
					{ToolCallID: "call_1", Name: "speak", Status: "succeeded", Code: "action_succeeded", Output: map[string]any{"visible": true}},
					{ToolCallID: "call_2", Name: "emote", Status: "succeeded", Code: "action_succeeded", Output: map[string]any{"visible": true}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	req, err := renderer.Render(agentCtx)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	if got := len(req.Messages); got != 3 {
		t.Fatalf("len(Messages) = %d, want user context plus two transcript messages", got)
	}
	if req.Messages[1].Role != model.RoleAssistant {
		t.Fatalf("transcript call role = %q, want assistant", req.Messages[1].Role)
	}
	if req.Messages[2].Role != model.RoleTool {
		t.Fatalf("transcript result role = %q, want tool", req.Messages[2].Role)
	}
	if req.Messages[1].ToolCalls[0].ID != "call_1" || req.Messages[1].ToolCalls[1].ID != "call_2" {
		t.Fatalf("tool call order = %+v", req.Messages[1].ToolCalls)
	}
	if req.Messages[2].ToolResults[0].ToolCallID != "call_1" || req.Messages[2].ToolResults[1].ToolCallID != "call_2" {
		t.Fatalf("tool result order = %+v", req.Messages[2].ToolResults)
	}
	assertContainsAll(t, req.Messages[1].Content, "call_1", "speak", "Hello.", "call_2", "emote", "happy")
	assertContainsAll(t, req.Messages[2].Content, "call_1", "succeeded", "action_succeeded", "call_2")
}

func TestRendererSeparatesRecentMemoryFromIntraTurnTranscript(t *testing.T) {
	renderer := agentcontext.NewRenderer()

	agentCtx, err := buildProjection(t, agentcontext.BuildInput{
		SessionKey:    session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "npc:Abigail"},
		RuntimePolicy: "policy",
		Event:         &protocolv1alpha2.GameEvent{EventId: "event-1"},
		Observation:   &protocolv1alpha2.Observation{WorldId: "world-a", EntityId: "npc:Abigail"},
		RecentMemories: []memory.Record{{
			Outcomes: []memory.TurnOutcome{{ToolName: "speak", ToolArguments: map[string]any{"text": "previous turn line"}}},
		}},
		Transcript: []model.Message{
			{
				Role: model.RoleAssistant,
				ToolCalls: []model.ToolCall{{
					ID:        "call_1",
					Name:      "speak",
					Arguments: map[string]any{"text": "current turn line"},
				}},
			},
			{
				Role: model.RoleTool,
				ToolResults: []model.ToolResult{{
					ToolCallID: "call_1",
					Name:       "speak",
					Status:     "succeeded",
					Code:       "action_succeeded",
					Output:     map[string]any{"line": "current turn line"},
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	req, err := renderer.Render(agentCtx)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	if !strings.Contains(req.Messages[0].Content, "previous turn line") {
		t.Fatalf("user context missing recent memory:\n%s", req.Messages[0].Content)
	}
	if strings.Contains(req.Messages[0].Content, "current turn line") {
		t.Fatalf("user context leaked transcript:\n%s", req.Messages[0].Content)
	}
	resultContent := req.Messages[len(req.Messages)-1].Content
	if !strings.Contains(resultContent, "current turn line") {
		t.Fatalf("transcript message missing current turn result:\n%s", resultContent)
	}
}

func TestRendererDoesNotLeakRawToolResultInternals(t *testing.T) {
	renderer := agentcontext.NewRenderer()
	longDiagnostic := "adapter rejected request\nstack trace line\n{\"raw\":\"json\",\"action_id\":\"runtime-action-123\"}" + strings.Repeat("x", 180)

	agentCtx, err := buildProjection(t, agentcontext.BuildInput{
		SessionKey:    session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "npc:Abigail"},
		RuntimePolicy: "policy",
		Event:         &protocolv1alpha2.GameEvent{EventId: "event-1"},
		Observation:   &protocolv1alpha2.Observation{WorldId: "world-a", EntityId: "npc:Abigail"},
		Transcript: []model.Message{
			{
				Role: model.RoleAssistant,
				ToolCalls: []model.ToolCall{{
					ID:        "call_1",
					Name:      "speak",
					Arguments: map[string]any{"text": "Hello."},
				}},
			},
			{
				Role: model.RoleTool,
				ToolResults: []model.ToolResult{{
					ToolCallID: "call_1",
					Name:       "speak",
					Status:     "rejected",
					Code:       "adapter_rejected",
					Message:    longDiagnostic,
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	req, err := renderer.Render(agentCtx)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	content := req.Messages[len(req.Messages)-1].Content
	if strings.Contains(content, "stack trace") || strings.Contains(content, "runtime-action-123") || strings.Contains(content, `{"raw"`) {
		t.Fatalf("tool result content leaked raw diagnostic:\n%s", content)
	}
	if len(extractJSONField(t, content, "message")) > 120 {
		t.Fatalf("tool result message was not bounded:\n%s", content)
	}
}

func TestRendererExposesSettleControlInstruction(t *testing.T) {
	renderer := agentcontext.NewRenderer()

	agentCtx, err := buildProjection(t, agentcontext.BuildInput{
		SessionKey:    session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "npc:Abigail"},
		RuntimePolicy: "policy",
		Event:         &protocolv1alpha2.GameEvent{EventId: "event-1"},
		Observation:   &protocolv1alpha2.Observation{WorldId: "world-a", EntityId: "npc:Abigail"},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	req, err := renderer.Render(agentCtx)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	if len(req.Controls) != 1 || req.Controls[0].Kind != model.ControlSettle {
		t.Fatalf("controls = %+v, want settle control", req.Controls)
	}
	if !strings.Contains(req.Messages[0].Content, "settle the current turn") {
		t.Fatalf("instruction missing settle guidance:\n%s", req.Messages[0].Content)
	}
}

func TestToolResultNormalizationIsDeterministic(t *testing.T) {
	renderer := agentcontext.NewRenderer()

	input := agentcontext.BuildInput{
		SessionKey:    session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "npc:Abigail"},
		RuntimePolicy: "policy",
		Event:         &protocolv1alpha2.GameEvent{EventId: "event-1"},
		Observation:   &protocolv1alpha2.Observation{WorldId: "world-a", EntityId: "npc:Abigail"},
		Transcript: []model.Message{
			{
				Role: model.RoleAssistant,
				ToolCalls: []model.ToolCall{{
					ID:        "call_1",
					Name:      "inspect",
					Arguments: map[string]any{"query": "state"},
				}},
			},
			{
				Role: model.RoleTool,
				ToolResults: []model.ToolResult{{
					ToolCallID: "call_1",
					Name:       "inspect",
					Status:     "succeeded",
					Code:       "action_succeeded",
					Output:     map[string]any{"b": float64(2), "a": float64(1)},
				}},
			},
		},
	}
	firstCtx, err := buildProjection(t, input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	secondCtx, err := buildProjection(t, input)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	first, err := renderer.Render(firstCtx)
	if err != nil {
		t.Fatalf("first Render returned error: %v", err)
	}
	second, err := renderer.Render(secondCtx)
	if err != nil {
		t.Fatalf("second Render returned error: %v", err)
	}
	firstContent := first.Messages[len(first.Messages)-1].Content
	secondContent := second.Messages[len(second.Messages)-1].Content
	if firstContent != secondContent {
		t.Fatalf("tool result rendering is not deterministic:\nfirst=%s\nsecond=%s", firstContent, secondContent)
	}
	if strings.Index(firstContent, `"a"`) > strings.Index(firstContent, `"b"`) {
		t.Fatalf("tool result output keys are not stable:\n%s", firstContent)
	}
}

func TestToolResultNormalizationIsProviderNeutral(t *testing.T) {
	renderer := agentcontext.NewRenderer()

	agentCtx, err := buildProjection(t, agentcontext.BuildInput{
		SessionKey:    session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "npc:Abigail"},
		RuntimePolicy: "policy",
		Event:         &protocolv1alpha2.GameEvent{EventId: "event-1"},
		Observation:   &protocolv1alpha2.Observation{WorldId: "world-a", EntityId: "npc:Abigail"},
		Transcript: []model.Message{
			{
				Role: model.RoleAssistant,
				ToolCalls: []model.ToolCall{{
					ID:        "call_1",
					Name:      "speak",
					Arguments: map[string]any{"text": "Hello."},
				}},
			},
			{
				Role: model.RoleTool,
				ToolResults: []model.ToolResult{{
					ToolCallID: "call_1",
					Name:       "speak",
					Status:     "succeeded",
					Code:       "action_succeeded",
					Output:     map[string]any{"visible": true},
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	req, err := renderer.Render(agentCtx)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	content := req.Messages[len(req.Messages)-1].Content
	for _, unwanted := range []string{"structpb", "protocolv1alpha2", "ActionResult", "protobuf"} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("provider-neutral transcript leaked %q:\n%s", unwanted, content)
		}
	}
}

func TestToolResultIncludesBoundedStructuredOutput(t *testing.T) {
	renderer := agentcontext.NewRenderer()

	agentCtx, err := buildProjection(t, agentcontext.BuildInput{
		SessionKey:    session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "npc:Abigail"},
		RuntimePolicy: "policy",
		Event:         &protocolv1alpha2.GameEvent{EventId: "event-1"},
		Observation:   &protocolv1alpha2.Observation{WorldId: "world-a", EntityId: "npc:Abigail"},
		Transcript: []model.Message{
			{
				Role: model.RoleAssistant,
				ToolCalls: []model.ToolCall{{
					ID:        "call_1",
					Name:      "inspect",
					Arguments: map[string]any{"query": "state"},
				}},
			},
			{
				Role: model.RoleTool,
				ToolResults: []model.ToolResult{{
					ToolCallID: "call_1",
					Name:       "inspect",
					Status:     "succeeded",
					Code:       "action_succeeded",
					Output: map[string]any{
						"visible": true,
						"nested":  map[string]any{"mood": "happy"},
					},
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	req, err := renderer.Render(agentCtx)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	assertContainsAll(t, req.Messages[len(req.Messages)-1].Content, `"visible": true`, `"mood": "happy"`)
}

func TestToolResultOutputProjectionAppliesBounds(t *testing.T) {
	content := renderToolResultOutputContent(t, 300, map[string]any{
		"a": []any{"one", "two", "three"},
		"b": map[string]any{"nested": map[string]any{"leaf": "too deep"}},
		"c": "extra field",
	})
	assertContainsAll(t, content, `"a"`, `"one"`, `"two"`, "_truncated")
	for _, unwanted := range []string{"three", "extra field", "too deep"} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("bounded projection leaked %q:\n%s", unwanted, content)
		}
	}
	outputTokens, err := tokenestimate.EstimateStableJSON(extractJSONFieldMap(t, content, "output"))
	if err != nil {
		t.Fatalf("EstimateStableJSON(output) returned error: %v", err)
	}
	if outputTokens > 300 {
		t.Fatalf("tool result output = %d estimated tokens, want <= 300:\n%s", outputTokens, content)
	}
}

func TestToolArgumentsProjectionUsesMarkerWithinTokenBounds(t *testing.T) {
	markerTokens, err := tokenestimate.EstimateStableJSON(map[string]any{"_truncated": true})
	if err != nil {
		t.Fatalf("EstimateStableJSON(marker) returned error: %v", err)
	}
	contents := renderToolTranscriptContents(t, markerTokens, map[string]any{
		"text": strings.Repeat("overflow ", 100),
	}, map[string]any{"visible": true})

	arguments := extractJSONFieldMap(t, contents[0], "arguments")
	if got, ok := arguments["_truncated"].(bool); !ok || !got {
		t.Fatalf("arguments marker = %#v, want true", arguments["_truncated"])
	}
	argumentTokens, err := tokenestimate.EstimateStableJSON(arguments)
	if err != nil {
		t.Fatalf("EstimateStableJSON(arguments) returned error: %v", err)
	}
	if argumentTokens > markerTokens {
		t.Fatalf("tool arguments marker = %d estimated tokens, want <= %d:\n%s", argumentTokens, markerTokens, contents[0])
	}
}

func TestToolResultOutputProjectionUsesMarkerWithinTokenBounds(t *testing.T) {
	markerTokens, err := tokenestimate.EstimateStableJSON(map[string]any{"_truncated": true})
	if err != nil {
		t.Fatalf("EstimateStableJSON(marker) returned error: %v", err)
	}
	content := renderToolResultOutputContent(t, markerTokens, map[string]any{
		"text": strings.Repeat("overflow ", 100),
	})

	output := extractJSONFieldMap(t, content, "output")
	if got, ok := output["_truncated"].(bool); !ok || !got {
		t.Fatalf("output marker = %#v, want true", output["_truncated"])
	}
	outputTokens, err := tokenestimate.EstimateStableJSON(output)
	if err != nil {
		t.Fatalf("EstimateStableJSON(output) returned error: %v", err)
	}
	if outputTokens > markerTokens {
		t.Fatalf("tool result marker = %d estimated tokens, want <= %d:\n%s", outputTokens, markerTokens, content)
	}
}

func TestToolResultOutputProjectionDropsOutputWhenMarkerCannotFit(t *testing.T) {
	markerTokens, err := tokenestimate.EstimateStableJSON(map[string]any{"_truncated": true})
	if err != nil {
		t.Fatalf("EstimateStableJSON(marker) returned error: %v", err)
	}
	content := renderToolResultOutputContent(t, markerTokens-1, map[string]any{
		"text": strings.Repeat("overflow ", 100),
	})

	entry := extractJSONEntry(t, content)
	if _, ok := entry["output"]; ok {
		t.Fatalf("tool result output = %#v, want omitted when marker exceeds token bound", entry["output"])
	}
}

func TestRendererRendersProjectedRecentMemory(t *testing.T) {
	renderer := agentcontext.NewRenderer()
	projection := agentcontext.ContextProjection{
		RuntimePolicy: "policy",
		AgentDescriptor: definition.AgentInstanceDescriptor{
			SessionKey: session.AgentSessionKey{GameID: "stardew-valley", WorldID: "world-a", EntityID: "npc:Abigail"},
		},
		CurrentEvent:       agentcontext.EventProjection{EventID: "event-2", EventType: "player_interacted_with_npc"},
		CurrentObservation: agentcontext.ObservationProjection{WorldID: "world-a", EntityID: "npc:Abigail"},
		RecentMemory: []agentcontext.MemoryProjection{{
			TimeRelation: "previous interaction",
			Summaries:    []string{`tool "speak" arguments {"text":"` + strings.Repeat("x", 100) + `"}`},
		}},
	}

	req, err := renderer.Render(projection)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	content := req.Messages[0].Content
	if !strings.Contains(content, strings.Repeat("x", 100)) {
		t.Fatalf("rendered content should include projected memory:\n%s", content)
	}
	if strings.Contains(content, "old") {
		t.Fatalf("renderer should not reconstruct raw memory selection:\n%s", content)
	}
}

func TestRendererSummarizesNonSpeakToolMemory(t *testing.T) {
	renderer := agentcontext.NewRenderer()

	agentCtx, err := buildProjection(t, agentcontext.BuildInput{
		SessionKey:    session.AgentSessionKey{GameID: "stardew-valley", WorldID: "world-a", EntityID: "npc:Abigail"},
		RuntimePolicy: "policy",
		Event: &protocolv1alpha2.GameEvent{
			EventId:   "event-2",
			EventType: "player_interacted_with_npc",
			GameTime: &protocolv1alpha2.GameTime{
				Year:   ptrInt32(1),
				Season: ptrInt32(1),
				Day:    ptrInt32(2),
				Hour:   ptrInt32(6),
				Minute: ptrInt32(30),
			},
		},
		Observation: &protocolv1alpha2.Observation{WorldId: "world-a", EntityId: "npc:Abigail"},
		RecentMemories: []memory.Record{{
			Outcomes: []memory.TurnOutcome{{
				ToolName:      "emote",
				ToolArguments: map[string]any{"emote": "happy"},
			}},
			GameTime: &memory.GameTimeSnapshot{Year: 1, Season: 1, Day: 2, Hour: 6, Minute: 20},
		}},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	req, err := renderer.Render(agentCtx)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	content := req.Messages[0].Content
	for _, want := range []string{
		"today 06:20",
		`tool "emote" arguments {"emote":"happy"}`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("rendered content missing %q:\n%s", want, content)
		}
	}
}

func TestRendererSummarizesDialogueToolMemory(t *testing.T) {
	renderer := agentcontext.NewRenderer()

	agentCtx, err := buildProjection(t, agentcontext.BuildInput{
		SessionKey:    session.AgentSessionKey{GameID: "stardew-valley", WorldID: "world-a", EntityID: "npc:Abigail"},
		RuntimePolicy: "policy",
		Event: &protocolv1alpha2.GameEvent{
			EventId:   "event-2",
			EventType: "player_said_to_npc",
		},
		Observation: &protocolv1alpha2.Observation{WorldId: "world-a", EntityId: "npc:Abigail"},
		RecentMemories: []memory.Record{{
			Outcomes: []memory.TurnOutcome{
				{ToolName: "present_dialogue", ToolArguments: map[string]any{"text": "Want to explore the mines?"}},
				{ToolName: "face_player"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	req, err := renderer.Render(agentCtx)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	content := req.Messages[0].Content
	assertContainsAll(t, content, `tool "present_dialogue" arguments {"text":"Want to explore the mines?"}`, `tool "face_player"`)
}

func TestRendererSummarizesContextFactsBeforeOutcomes(t *testing.T) {
	renderer := agentcontext.NewRenderer()

	agentCtx, err := buildProjection(t, agentcontext.BuildInput{
		SessionKey:    session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "npc:Abigail"},
		RuntimePolicy: "policy",
		Event: &protocolv1alpha2.GameEvent{
			EventId:   "event-2",
			EventType: "player_said_to_npc",
			GameTime: &protocolv1alpha2.GameTime{
				Year:   ptrInt32(1),
				Season: ptrInt32(1),
				Day:    ptrInt32(2),
				Hour:   ptrInt32(6),
				Minute: ptrInt32(30),
			},
		},
		Observation: &protocolv1alpha2.Observation{WorldId: "world-a", EntityId: "npc:Abigail"},
		RecentMemories: []memory.Record{{
			SourceContextFacts: []memory.SourceContextFact{{
				Kind:          "utterance",
				ActorEntityID: "player:local",
				Text:          "Let's go fishing.",
			}},
			Outcomes: []memory.TurnOutcome{{
				ToolName:      "present_dialogue",
				ToolArguments: map[string]any{"text": "Sure."},
			}},
			GameTime: &memory.GameTimeSnapshot{Year: 1, Season: 1, Day: 2, Hour: 6, Minute: 20},
		}},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	req, err := renderer.Render(agentCtx)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	content := req.Messages[0].Content
	wantFact := `player:local said "Let's go fishing."`
	wantOutcome := `tool "present_dialogue" arguments {"text":"Sure."}`
	assertContainsAll(t, content, wantFact, wantOutcome)
	if strings.Index(content, wantFact) > strings.Index(content, wantOutcome) {
		t.Fatalf("context fact should render before action outcome:\n%s", content)
	}
}

func TestRendererFiltersFutureGameTimeBeforeMemoryBudget(t *testing.T) {
	renderer := agentcontext.NewRenderer()

	agentCtx, err := buildProjection(t, agentcontext.BuildInput{
		SessionKey:    session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "npc:Abigail"},
		RuntimePolicy: "policy",
		Event: &protocolv1alpha2.GameEvent{
			EventId:   "event-2",
			EventType: "player_said_to_npc",
			GameTime: &protocolv1alpha2.GameTime{
				Year:   ptrInt32(1),
				Season: ptrInt32(1),
				Day:    ptrInt32(2),
				Hour:   ptrInt32(6),
				Minute: ptrInt32(30),
			},
		},
		Observation: &protocolv1alpha2.Observation{WorldId: "world-a", EntityId: "npc:Abigail"},
		RecentMemories: []memory.Record{
			{
				Outcomes: []memory.TurnOutcome{{
					ToolName:      "speak",
					ToolArguments: map[string]any{"text": "current request"},
				}},
				GameTime: &memory.GameTimeSnapshot{Year: 1, Season: 1, Day: 2, Hour: 6, Minute: 20},
			},
			{
				Outcomes: []memory.TurnOutcome{{
					ToolName:      "speak",
					ToolArguments: map[string]any{"text": "future request"},
				}},
				GameTime: &memory.GameTimeSnapshot{Year: 1, Season: 1, Day: 2, Hour: 7, Minute: 10},
			},
		},
	}, agentcontext.EngineConfig{MaxRecentMemoryTokens: 256})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	req, err := renderer.Render(agentCtx)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	content := req.Messages[0].Content
	assertContainsAll(t, content, `tool "speak" arguments {"text":"current request"}`)
	if strings.Contains(content, "future request") {
		t.Fatalf("future memory should be filtered before budget trim:\n%s", content)
	}
}

func TestRendererUsesCreatedAtForEntireBatchWhenAnyGameTimeIsUnknown(t *testing.T) {
	renderer := agentcontext.NewRenderer()
	gameTime := &memory.GameTimeSnapshot{Year: 1, Season: 1, Day: 2, Hour: 6, Minute: 20}

	agentCtx, err := buildProjection(t, agentcontext.BuildInput{
		SessionKey:    session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "npc:Abigail"},
		RuntimePolicy: "policy",
		Event: &protocolv1alpha2.GameEvent{
			EventId:   "event-3",
			EventType: "player_said_to_npc",
			GameTime: &protocolv1alpha2.GameTime{
				Year:   ptrInt32(1),
				Season: ptrInt32(1),
				Day:    ptrInt32(2),
				Hour:   ptrInt32(6),
				Minute: ptrInt32(30),
			},
		},
		Observation: &protocolv1alpha2.Observation{WorldId: "world-a", EntityId: "npc:Abigail"},
		RecentMemories: []memory.Record{
			{
				CreatedAt:           time.Unix(100, 0),
				SourceEventSequence: 2,
				Outcomes: []memory.TurnOutcome{{
					ToolName:      "speak",
					ToolArguments: map[string]any{"text": "second"},
				}},
				GameTime: gameTime,
			},
			{
				CreatedAt:           time.Unix(200, 0),
				SourceEventSequence: 1,
				Outcomes: []memory.TurnOutcome{{
					ToolName:      "speak",
					ToolArguments: map[string]any{"text": "first"},
				}},
				GameTime: gameTime,
			},
			{
				CreatedAt:           time.Unix(300, 0),
				SourceEventSequence: 99,
				Outcomes: []memory.TurnOutcome{{
					ToolName:      "speak",
					ToolArguments: map[string]any{"text": "unknown time"},
				}},
			},
			{
				CreatedAt:           time.Unix(400, 0),
				SourceEventSequence: 3,
				Outcomes: []memory.TurnOutcome{{
					ToolName:      "speak",
					ToolArguments: map[string]any{"text": "later"},
				}},
				GameTime: &memory.GameTimeSnapshot{Year: 1, Season: 1, Day: 2, Hour: 6, Minute: 25},
			},
		},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	req, err := renderer.Render(agentCtx)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	content := req.Messages[0].Content
	first := strings.Index(content, `tool "speak" arguments {"text":"first"}`)
	second := strings.Index(content, `tool "speak" arguments {"text":"second"}`)
	unknown := strings.Index(content, `tool "speak" arguments {"text":"unknown time"}`)
	later := strings.Index(content, `tool "speak" arguments {"text":"later"}`)
	if first == -1 || second == -1 || second > first {
		t.Fatalf("mixed time batch should render by CreatedAt:\n%s", content)
	}
	if unknown == -1 || later == -1 || first > unknown || unknown > later {
		t.Fatalf("mixed time batch should keep chronological CreatedAt order:\n%s", content)
	}
}

func TestRendererPreservesMemoryStoreOrderWhenSequenceIsMissing(t *testing.T) {
	renderer := agentcontext.NewRenderer()
	gameTime := &memory.GameTimeSnapshot{Year: 1, Season: 1, Day: 2, Hour: 6, Minute: 20}

	agentCtx, err := buildProjection(t, agentcontext.BuildInput{
		SessionKey:    session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "npc:Abigail"},
		RuntimePolicy: "policy",
		Event: &protocolv1alpha2.GameEvent{
			EventId:   "event-3",
			EventType: "player_said_to_npc",
			GameTime: &protocolv1alpha2.GameTime{
				Year:   ptrInt32(1),
				Season: ptrInt32(1),
				Day:    ptrInt32(2),
				Hour:   ptrInt32(6),
				Minute: ptrInt32(30),
			},
		},
		Observation: &protocolv1alpha2.Observation{WorldId: "world-a", EntityId: "npc:Abigail"},
		RecentMemories: []memory.Record{
			{
				SourceEventSequence: 2,
				Outcomes: []memory.TurnOutcome{{
					ToolName:      "speak",
					ToolArguments: map[string]any{"text": "with sequence"},
				}},
				GameTime: gameTime,
			},
			{
				Outcomes: []memory.TurnOutcome{{
					ToolName:      "speak",
					ToolArguments: map[string]any{"text": "missing sequence"},
				}},
				GameTime: gameTime,
			},
		},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	req, err := renderer.Render(agentCtx)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	content := req.Messages[0].Content
	withSequence := strings.Index(content, `tool "speak" arguments {"text":"with sequence"}`)
	missingSequence := strings.Index(content, `tool "speak" arguments {"text":"missing sequence"}`)
	if withSequence == -1 || missingSequence == -1 || withSequence > missingSequence {
		t.Fatalf("records with missing source event sequence should keep MemoryStore order:\n%s", content)
	}
}

func TestRendererSummarizesMultiOutcomeMemory(t *testing.T) {
	renderer := agentcontext.NewRenderer()

	agentCtx, err := buildProjection(t, agentcontext.BuildInput{
		SessionKey:    session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "npc:Abigail"},
		RuntimePolicy: "policy",
		Event: &protocolv1alpha2.GameEvent{
			EventId:   "event-2",
			EventType: "player_interacted_with_npc",
		},
		Observation: &protocolv1alpha2.Observation{WorldId: "world-a", EntityId: "npc:Abigail"},
		RecentMemories: []memory.Record{{
			Outcomes: []memory.TurnOutcome{
				{ToolName: "speak", ToolArguments: map[string]any{"text": "hello"}},
				{ToolName: "emote", ToolArguments: map[string]any{"emote": "happy"}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	req, err := renderer.Render(agentCtx)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	content := req.Messages[0].Content
	assertContainsAll(t, content, `tool "speak" arguments {"text":"hello"}`, `tool "emote" arguments {"emote":"happy"}`)
}

func TestRendererMarksPreviousDayMemory(t *testing.T) {
	renderer := agentcontext.NewRenderer()

	agentCtx, err := buildProjection(t, agentcontext.BuildInput{
		SessionKey:    session.AgentSessionKey{GameID: "stardew-valley", WorldID: "world-a", EntityID: "npc:Abigail"},
		RuntimePolicy: "policy",
		Event: &protocolv1alpha2.GameEvent{
			EventId:   "event-2",
			EventType: "player_interacted_with_npc",
			GameTime: &protocolv1alpha2.GameTime{
				Year:   ptrInt32(1),
				Season: ptrInt32(1),
				Day:    ptrInt32(3),
				Hour:   ptrInt32(7),
				Minute: ptrInt32(10),
			},
		},
		Observation: &protocolv1alpha2.Observation{WorldId: "world-a", EntityId: "npc:Abigail"},
		RecentMemories: []memory.Record{{
			Outcomes: []memory.TurnOutcome{{
				ToolName:      "speak",
				ToolArguments: map[string]any{"text": "see you tomorrow"},
			}},
			GameTime: &memory.GameTimeSnapshot{Year: 1, Season: 1, Day: 2, Hour: 18, Minute: 20},
		}},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	req, err := renderer.Render(agentCtx)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	content := req.Messages[0].Content
	for _, want := range []string{
		"previous day Y1 S1 D2 18:20",
		`tool "speak" arguments {"text":"see you tomorrow"}`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("rendered content missing %q:\n%s", want, content)
		}
	}
}

func TestEngineRejectsMissingCurrentObservation(t *testing.T) {
	_, err := agentcontext.NewEngine(agentcontext.EngineConfig{}).Build(agentcontext.BuildInput{
		SessionKey: session.AgentSessionKey{GameID: "stardew-valley", WorldID: "world-a", EntityID: "npc:Abigail"},
		Event:      &protocolv1alpha2.GameEvent{EventId: "event-1"},
	})
	if err == nil {
		t.Fatal("Build returned nil error, want structural failure")
	}
}

func buildProjection(t *testing.T, input agentcontext.BuildInput, configs ...agentcontext.EngineConfig) (agentcontext.ContextProjection, error) {
	t.Helper()

	input = completeEngineInput(input)
	config := agentcontext.EngineConfig{}
	if len(configs) > 0 {
		config = configs[0]
	}
	result, err := agentcontext.NewEngine(config).Build(input)
	return result.Projection, err
}

func completeEngineInput(input agentcontext.BuildInput) agentcontext.BuildInput {
	key := input.SessionKey
	if input.RuntimePolicy == "" {
		input.RuntimePolicy = "policy"
	}
	if input.CanonicalTarget == nil {
		input.CanonicalTarget = &protocolv1alpha2.EntityRef{
			EntityId:     key.EntityID,
			EntityType:   input.AgentDescriptor.EntityType,
			DisplayName:  input.AgentDescriptor.DisplayName,
			DefinitionId: input.AgentDescriptor.DefinitionID,
		}
	}
	if input.AgentDescriptor.SessionKey == (session.AgentSessionKey{}) {
		input.AgentDescriptor.SessionKey = key
	}
	if input.AgentDescriptor.EntityType == "" {
		input.AgentDescriptor.EntityType = input.CanonicalTarget.GetEntityType()
	}
	if input.AgentDescriptor.DisplayName == "" {
		input.AgentDescriptor.DisplayName = input.CanonicalTarget.GetDisplayName()
	}
	if input.AgentDescriptor.DefinitionID == "" {
		input.AgentDescriptor.DefinitionID = input.CanonicalTarget.GetDefinitionId()
	}
	if input.Event != nil {
		if input.Event.WorldId == "" {
			input.Event.WorldId = key.WorldID
		}
		if input.Event.TargetEntityId == "" {
			input.Event.TargetEntityId = key.EntityID
		}
	}
	if input.Observation != nil {
		if input.Observation.WorldId == "" {
			input.Observation.WorldId = key.WorldID
		}
		if input.Observation.EntityId == "" {
			input.Observation.EntityId = key.EntityID
		}
	}
	return input
}

func turnToolViewFromDefinitions(t *testing.T, definitions ...model.ToolDefinition) tool.TurnToolView {
	t.Helper()

	capabilities := make([]*protocolv1alpha2.Capability, 0, len(definitions))
	for _, definition := range definitions {
		capabilities = append(capabilities, &protocolv1alpha2.Capability{
			Name:            definition.Name,
			Description:     definition.Description,
			InputSchemaJson: definition.InputSchema,
		})
	}

	catalog, _, err := tool.BuildEnvironmentToolCatalog(&protocolv1alpha2.CapabilityList{
		Capabilities: capabilities,
	})
	if err != nil {
		t.Fatalf("BuildEnvironmentToolCatalog returned error: %v", err)
	}
	return catalog.Snapshot()
}

func ptrInt32(value int32) *int32 {
	return &value
}

func mustStruct(t *testing.T, fields map[string]any) *structpb.Struct {
	t.Helper()

	value, err := structpb.NewStruct(fields)
	if err != nil {
		t.Fatalf("NewStruct returned error: %v", err)
	}
	return value
}

func assertContainsAll(t *testing.T, content string, values ...string) {
	t.Helper()

	for _, want := range values {
		if !strings.Contains(content, want) {
			t.Fatalf("content missing %q:\n%s", want, content)
		}
	}
}

func extractJSONField(t *testing.T, content string, field string) string {
	t.Helper()

	got, _ := extractJSONEntry(t, content)[field].(string)
	return got
}

func extractJSONFieldMap(t *testing.T, content string, field string) map[string]any {
	t.Helper()

	got, ok := extractJSONEntry(t, content)[field].(map[string]any)
	if !ok {
		t.Fatalf("content field %q is not an object: %s", field, content)
	}
	return got
}

func extractJSONEntry(t *testing.T, content string) map[string]any {
	t.Helper()

	var values []map[string]any
	if err := json.Unmarshal([]byte(content), &values); err != nil {
		t.Fatalf("content is not JSON array: %v\n%s", err, content)
	}
	if len(values) == 0 {
		t.Fatalf("content has no values: %s", content)
	}
	return values[0]
}

func renderToolResultOutputContent(t *testing.T, maxOutputTokens int, output map[string]any) string {
	t.Helper()

	contents := renderToolTranscriptContents(t, maxOutputTokens, map[string]any{"query": "state"}, output)
	return contents[len(contents)-1]
}

func renderToolTranscriptContents(t *testing.T, maxOutputTokens int, arguments map[string]any, output map[string]any) []string {
	t.Helper()

	key := session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "npc:Abigail"}
	target := &protocolv1alpha2.EntityRef{EntityId: key.EntityID, DefinitionId: key.EntityID}
	engine := agentcontext.NewEngine(agentcontext.EngineConfig{
		MaxToolResultOutputTokens:     maxOutputTokens,
		MaxToolResultOutputDepth:      2,
		MaxToolResultOutputFields:     2,
		MaxToolResultOutputArrayItems: 2,
	})
	result, err := engine.Build(agentcontext.BuildInput{
		SessionKey:      key,
		CanonicalTarget: target,
		AgentDescriptor: definition.NewAgentInstanceDescriptor(key, target),
		RuntimePolicy:   "policy",
		Event:           &protocolv1alpha2.GameEvent{EventId: "event-1", WorldId: key.WorldID, TargetEntityId: key.EntityID},
		Observation:     &protocolv1alpha2.Observation{WorldId: key.WorldID, EntityId: key.EntityID},
		Transcript: []model.Message{
			{
				Role: model.RoleAssistant,
				ToolCalls: []model.ToolCall{{
					ID:        "call_1",
					Name:      "inspect",
					Arguments: arguments,
				}},
			},
			{
				Role: model.RoleTool,
				ToolResults: []model.ToolResult{{
					ToolCallID: "call_1",
					Name:       "inspect",
					Status:     "succeeded",
					Code:       "action_succeeded",
					Output:     output,
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Engine.Build returned error: %v", err)
	}

	req, err := agentcontext.NewRenderer().Render(result.Projection)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	contents := make([]string, 0, len(req.Messages))
	for _, message := range req.Messages {
		if message.Role == model.RoleAssistant || message.Role == model.RoleTool {
			contents = append(contents, message.Content)
		}
	}
	if len(contents) == 0 {
		t.Fatalf("rendered request has no transcript messages: %+v", req)
	}
	return contents
}

func mustMarshalJSONBytes(t *testing.T, value any) []byte {
	t.Helper()

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	return data
}
