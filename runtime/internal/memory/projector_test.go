package memory_test

import (
	"errors"
	"testing"
	"time"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"

	"google.golang.org/protobuf/types/known/structpb"
)

func TestProjectorBuildsRecordFromSuccessfulTurn(t *testing.T) {
	now := time.Unix(200, 0)
	projector := memory.NewProjector(func() time.Time { return now })
	key := session.AgentSessionKey{GameID: "stardew-valley", WorldID: "world-a", EntityID: "npc:Abigail"}
	args := map[string]any{"text": "hello"}

	record, err := projector.Project(memory.ProjectInput{
		SessionKey:     key,
		TurnID:         "turn-1",
		ProjectionKind: memory.ProjectionKindSettledTurn,
		Event: &protocolv1alpha2.GameEvent{
			EventId:   "event-1",
			EventType: "player_interacted_with_npc",
			Sequence:  42,
			GameTime: &protocolv1alpha2.GameTime{
				Year:   ptrInt32(1),
				Season: ptrInt32(2),
				Day:    ptrInt32(3),
				Hour:   ptrInt32(6),
				Minute: ptrInt32(20),
				Tick:   ptrInt64(9001),
			},
		},
		ToolCall: model.ToolCall{
			Name:      "speak",
			Arguments: args,
		},
		ActionResult: &protocolv1alpha2.ActionResult{
			ActionId: "act-1",
			Status:   protocolv1alpha2.ActionStatus_ACTION_STATUS_SUCCEEDED,
		},
	})
	if err != nil {
		t.Fatalf("Project returned error: %v", err)
	}

	if record.MemoryID == "" {
		t.Fatal("MemoryID is empty")
	}
	if record.ProjectionKind != memory.ProjectionKindSettledTurn {
		t.Fatalf("ProjectionKind = %q, want %q", record.ProjectionKind, memory.ProjectionKindSettledTurn)
	}
	if record.ProjectionVersion != memory.ProjectionVersionRecentV2 {
		t.Fatalf("ProjectionVersion = %d, want %d", record.ProjectionVersion, memory.ProjectionVersionRecentV2)
	}
	if record.ProjectionBatchKey != `["stardew-valley","world-a","npc:Abigail","turn-1","event-1","settled_turn",2]` {
		t.Fatalf("ProjectionBatchKey = %q", record.ProjectionBatchKey)
	}
	if record.SessionKey != key {
		t.Fatalf("SessionKey = %+v, want %+v", record.SessionKey, key)
	}
	if record.SourceTurnID != "turn-1" {
		t.Fatalf("SourceTurnID = %q, want turn-1", record.SourceTurnID)
	}
	if record.SourceEventID != "event-1" {
		t.Fatalf("SourceEventID = %q, want event-1", record.SourceEventID)
	}
	if record.SourceEventSequence != 42 {
		t.Fatalf("SourceEventSequence = %d, want 42", record.SourceEventSequence)
	}
	if record.EventType != "player_interacted_with_npc" {
		t.Fatalf("EventType = %q", record.EventType)
	}
	if record.GameTime == nil {
		t.Fatal("GameTime is nil")
	}
	if *record.GameTime != (memory.GameTimeSnapshot{Year: 1, Season: 2, Day: 3, Hour: 6, Minute: 20, Tick: 9001, PresentFields: 63}) {
		t.Fatalf("GameTime = %+v", *record.GameTime)
	}
	if got := len(record.Outcomes); got != 1 {
		t.Fatalf("outcome count = %d, want 1", got)
	}
	if record.Outcomes[0].ToolName != "speak" {
		t.Fatalf("ToolName = %q, want speak", record.Outcomes[0].ToolName)
	}
	if got := record.Outcomes[0].ToolArguments["text"]; got != "hello" {
		t.Fatalf("ToolArguments[text] = %v, want hello", got)
	}
	if record.Outcomes[0].ActionStatus != "ACTION_STATUS_SUCCEEDED" {
		t.Fatalf("ActionStatus = %q, want ACTION_STATUS_SUCCEEDED", record.Outcomes[0].ActionStatus)
	}
	if !record.CreatedAt.Equal(now) {
		t.Fatalf("CreatedAt = %v, want %v", record.CreatedAt, now)
	}
}

func TestProjectorBuildsDifferentProjectionBatchKeysForProjectionKinds(t *testing.T) {
	projector := memory.NewProjector(func() time.Time { return time.Unix(200, 0) })
	key := session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "agent-1"}
	event := &protocolv1alpha2.GameEvent{EventId: "event-1"}

	settled, err := projector.Project(memory.ProjectInput{
		SessionKey:     key,
		TurnID:         "turn-1",
		ProjectionKind: memory.ProjectionKindSettledTurn,
		Event:          event,
	})
	if err != nil {
		t.Fatalf("settled Project returned error: %v", err)
	}
	priorActions, err := projector.Project(memory.ProjectInput{
		SessionKey:     key,
		TurnID:         "turn-1",
		ProjectionKind: memory.ProjectionKindPriorSuccessfulActions,
		Event:          event,
		Outcomes: []memory.ProjectOutcome{{
			ToolCall: model.ToolCall{Name: "speak"},
			ActionResult: &protocolv1alpha2.ActionResult{
				Status: protocolv1alpha2.ActionStatus_ACTION_STATUS_SUCCEEDED,
			},
		}},
	})
	if err != nil {
		t.Fatalf("prior actions Project returned error: %v", err)
	}

	if settled.ProjectionBatchKey == priorActions.ProjectionBatchKey {
		t.Fatalf("ProjectionBatchKey should differ by projection kind: %q", settled.ProjectionBatchKey)
	}
	if priorActions.ProjectionBatchKey != `["fake-game","world-a","agent-1","turn-1","event-1","prior_successful_actions",2]` {
		t.Fatalf("prior actions ProjectionBatchKey = %q", priorActions.ProjectionBatchKey)
	}
}

func TestProjectorCopiesToolCallArgumentMap(t *testing.T) {
	projector := memory.NewProjector(func() time.Time { return time.Unix(200, 0) })
	args := map[string]any{"text": "hello"}

	record, err := projector.Project(memory.ProjectInput{
		SessionKey:     session.AgentSessionKey{GameID: "stardew-valley", WorldID: "world-a", EntityID: "npc:Abigail"},
		TurnID:         "turn-1",
		ProjectionKind: memory.ProjectionKindSettledTurn,
		Event:          &protocolv1alpha2.GameEvent{EventId: "event-1"},
		ToolCall: model.ToolCall{
			Name:      "speak",
			Arguments: args,
		},
		ActionResult: &protocolv1alpha2.ActionResult{
			Status: protocolv1alpha2.ActionStatus_ACTION_STATUS_SUCCEEDED,
		},
	})
	if err != nil {
		t.Fatalf("Project returned error: %v", err)
	}

	args["text"] = "changed"
	if got := record.Outcomes[0].ToolArguments["text"]; got != "hello" {
		t.Fatalf("ToolArguments[text] = %v, want copied hello", got)
	}
}

func TestProjectorBuildsRecordWithMultipleOutcomes(t *testing.T) {
	now := time.Unix(300, 0)
	projector := memory.NewProjector(func() time.Time { return now })
	key := session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "npc:Abigail"}

	record, err := projector.Project(memory.ProjectInput{
		SessionKey:     key,
		TurnID:         "turn-1",
		ProjectionKind: memory.ProjectionKindSettledTurn,
		Event: &protocolv1alpha2.GameEvent{
			EventId:   "event-1",
			EventType: "player_interacted_with_npc",
			Sequence:  7,
		},
		Outcomes: []memory.ProjectOutcome{
			{
				ToolCall: model.ToolCall{Name: "speak", Arguments: map[string]any{"text": "hello"}},
				ActionResult: &protocolv1alpha2.ActionResult{
					Status: protocolv1alpha2.ActionStatus_ACTION_STATUS_SUCCEEDED,
				},
			},
			{
				ToolCall: model.ToolCall{Name: "emote", Arguments: map[string]any{"emote": "happy"}},
				ActionResult: &protocolv1alpha2.ActionResult{
					Status: protocolv1alpha2.ActionStatus_ACTION_STATUS_SUCCEEDED,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Project returned error: %v", err)
	}

	if record.SessionKey != key {
		t.Fatalf("SessionKey = %+v, want %+v", record.SessionKey, key)
	}
	if got := len(record.Outcomes); got != 2 {
		t.Fatalf("outcome count = %d, want 2", got)
	}
	if record.Outcomes[0].ToolName != "speak" || record.Outcomes[1].ToolName != "emote" {
		t.Fatalf("outcome order = %+v, want speak then emote", record.Outcomes)
	}
}

func TestProjectorCopiesEventContextFacts(t *testing.T) {
	projector := memory.NewProjector(func() time.Time { return time.Unix(400, 0) })
	attributes, err := structpb.NewStruct(map[string]any{
		"input_kind":            "option",
		"trigger":               "dialogue_option",
		"selected_option_index": float64(1),
	})
	if err != nil {
		t.Fatalf("NewStruct returned error: %v", err)
	}

	event := &protocolv1alpha2.GameEvent{
		EventId:   "event-1",
		EventType: "player_said_to_npc",
		Sequence:  43,
		ContextFacts: []*protocolv1alpha2.ContextFact{{
			Kind:           "utterance",
			ActorEntityId:  "player:local",
			TargetEntityId: "npc:Abigail",
			ScopeId:        "conv_1",
			Text:           "Let's go fishing.",
			Attributes:     attributes,
		}},
	}

	record, err := projector.Project(memory.ProjectInput{
		SessionKey:     session.AgentSessionKey{GameID: "stardew-valley", WorldID: "world-a", EntityID: "npc:Abigail"},
		TurnID:         "turn-1",
		ProjectionKind: memory.ProjectionKindSettledTurn,
		Event:          event,
		Outcomes: []memory.ProjectOutcome{{
			ToolCall: model.ToolCall{Name: "present_dialogue", Arguments: map[string]any{"text": "Sure."}},
			ActionResult: &protocolv1alpha2.ActionResult{
				Status: protocolv1alpha2.ActionStatus_ACTION_STATUS_SUCCEEDED,
			},
		}},
	})
	if err != nil {
		t.Fatalf("Project returned error: %v", err)
	}

	event.ContextFacts[0].Text = "changed"
	attributes.Fields["input_kind"] = structpb.NewStringValue("changed")

	if got := len(record.SourceContextFacts); got != 1 {
		t.Fatalf("context fact count = %d, want 1", got)
	}
	fact := record.SourceContextFacts[0]
	if fact.Kind != "utterance" || fact.ActorEntityID != "player:local" || fact.TargetEntityID != "npc:Abigail" || fact.ScopeID != "conv_1" || fact.Text != "Let's go fishing." {
		t.Fatalf("context fact = %+v", fact)
	}
	if got := fact.Attributes["input_kind"]; got != "option" {
		t.Fatalf("context fact input_kind = %v, want option", got)
	}
	if got := fact.Attributes["selected_option_index"]; got != float64(1) {
		t.Fatalf("context fact selected_option_index = %v, want 1", got)
	}
}

func TestProjectorBuildsRecordFromContextFactsWithoutOutcomes(t *testing.T) {
	projector := memory.NewProjector(func() time.Time { return time.Unix(500, 0) })

	record, err := projector.Project(memory.ProjectInput{
		SessionKey:     session.AgentSessionKey{GameID: "stardew-valley", WorldID: "world-a", EntityID: "npc:Abigail"},
		TurnID:         "turn-1",
		ProjectionKind: memory.ProjectionKindSettledTurn,
		Event: &protocolv1alpha2.GameEvent{
			EventId:   "event-1",
			EventType: "player_said_to_npc",
			ContextFacts: []*protocolv1alpha2.ContextFact{{
				Kind:           "utterance",
				ActorEntityId:  "player:local",
				TargetEntityId: "npc:Abigail",
				ScopeId:        "conv_1",
				Text:           "I need a moment.",
			}},
		},
	})
	if err != nil {
		t.Fatalf("Project returned error: %v", err)
	}

	if got := len(record.SourceContextFacts); got != 1 {
		t.Fatalf("context fact count = %d, want 1", got)
	}
	if got := len(record.Outcomes); got != 0 {
		t.Fatalf("outcome count = %d, want 0", got)
	}
}

func TestProjectorRejectsMissingTurnID(t *testing.T) {
	projector := memory.NewProjector(func() time.Time { return time.Unix(200, 0) })

	_, err := projector.Project(memory.ProjectInput{
		SessionKey:     session.AgentSessionKey{GameID: "stardew-valley", WorldID: "world-a", EntityID: "npc:Abigail"},
		ProjectionKind: memory.ProjectionKindSettledTurn,
		Event: &protocolv1alpha2.GameEvent{
			EventId:   "event-1",
			EventType: "player_interacted_with_npc",
		},
		ToolCall: model.ToolCall{Name: "speak"},
		ActionResult: &protocolv1alpha2.ActionResult{
			Status: protocolv1alpha2.ActionStatus_ACTION_STATUS_SUCCEEDED,
		},
	})
	if !errors.Is(err, memory.ErrProject) {
		t.Fatalf("Project error = %v, want ErrProject", err)
	}
}

func TestProjectorRejectsMissingProjectionKind(t *testing.T) {
	projector := memory.NewProjector(func() time.Time { return time.Unix(200, 0) })

	_, err := projector.Project(memory.ProjectInput{
		SessionKey: session.AgentSessionKey{GameID: "stardew-valley", WorldID: "world-a", EntityID: "npc:Abigail"},
		TurnID:     "turn-1",
		Event: &protocolv1alpha2.GameEvent{
			EventId:   "event-1",
			EventType: "player_interacted_with_npc",
		},
	})
	if !errors.Is(err, memory.ErrProject) {
		t.Fatalf("Project error = %v, want ErrProject", err)
	}
}

func TestProjectorRejectsNilActionResult(t *testing.T) {
	projector := memory.NewProjector(func() time.Time { return time.Unix(200, 0) })

	_, err := projector.Project(memory.ProjectInput{
		SessionKey:     session.AgentSessionKey{GameID: "stardew-valley", WorldID: "world-a", EntityID: "npc:Abigail"},
		TurnID:         "turn-1",
		ProjectionKind: memory.ProjectionKindSettledTurn,
		Event: &protocolv1alpha2.GameEvent{
			EventId:   "event-1",
			EventType: "player_interacted_with_npc",
		},
		ToolCall: model.ToolCall{Name: "speak"},
	})
	if !errors.Is(err, memory.ErrProject) {
		t.Fatalf("Project error = %v, want ErrProject", err)
	}
}

func ptrInt32(value int32) *int32 {
	return &value
}

func ptrInt64(value int64) *int64 {
	return &value
}
