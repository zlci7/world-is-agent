package traceview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gameagent/runtime/internal/trace"
)

func writeTrace(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "traces.jsonl")
	body := ""
	if len(lines) > 0 {
		body = strings.Join(lines, "\n") + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write trace: %v", err)
	}
	return path
}

func eventLine(t *testing.T, event trace.Event) string {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return string(data)
}

func turnStarted(t *testing.T, turnID, entityID, eventType string, tools ...string) string {
	t.Helper()
	fields := trace.Fields{}
	if len(tools) > 0 {
		items := make([]any, 0, len(tools))
		for _, tool := range tools {
			items = append(items, tool)
		}
		fields["turn_tool_names"] = items
	}
	return eventLine(t, trace.Event{
		SchemaVersion: 1,
		TraceID:       turnID,
		TurnID:        turnID,
		Seq:           1,
		Event:         trace.EventTurnStarted,
		Time:          time.Date(2026, 9, 18, 21, 8, 11, 0, time.UTC),
		GameID:        "stardew-valley",
		WorldID:       "火锅_416823588",
		EventID:       "event_1",
		EventType:     eventType,
		EntityID:      entityID,
		Fields:        fields,
	})
}

func stepStarted(t *testing.T, turnID string, seq uint32, elapsedMS int64) string {
	t.Helper()
	return eventLine(t, trace.Event{
		TurnID:    turnID,
		Seq:       seq,
		Event:     trace.EventAgentStepStarted,
		Time:      time.Date(2026, 9, 18, 21, 8, 11, 0, time.UTC),
		ElapsedMS: elapsedMS,
		EntityID:  "npc:Penny",
	})
}

func toolSelected(t *testing.T, turnID string, seq uint32, elapsedMS int64, tool string) string {
	t.Helper()
	return eventLine(t, trace.Event{
		TurnID:    turnID,
		Seq:       seq,
		Event:     trace.EventToolCallSelected,
		Time:      time.Date(2026, 9, 18, 21, 8, 12, 0, time.UTC),
		ElapsedMS: elapsedMS,
		EntityID:  "npc:Penny",
		Tool:      tool,
	})
}

func TestTurnsIgnoresAMissingTrace(t *testing.T) {
	reader := NewReader(filepath.Join(t.TempDir(), "absent.jsonl"), Options{})
	turns, err := reader.Turns()
	if err != nil {
		t.Fatalf("Turns returned error for a missing trace: %v", err)
	}
	if len(turns) != 0 {
		t.Fatalf("turns = %+v, want none", turns)
	}
}

func TestTurnsProjectsACompletedTurn(t *testing.T) {
	path := writeTrace(t,
		turnStarted(t, "turn_a", "npc:Penny", "player_said_to_npc", "send_mail", "present_dialogue"),
		stepStarted(t, "turn_a", 2, 40),
		toolSelected(t, "turn_a", 3, 120, "send_mail"),
		stepStarted(t, "turn_a", 4, 400),
		toolSelected(t, "turn_a", 5, 900, "send_mail"),
		toolSelected(t, "turn_a", 6, 1500, "present_dialogue"),
		eventLine(t, trace.Event{
			TurnID: "turn_a", Seq: 7, Event: trace.EventTurnCompleted,
			Time: time.Date(2026, 9, 18, 21, 8, 14, 0, time.UTC), ElapsedMS: 2541,
			EntityID: "npc:Penny", Tool: "present_dialogue",
		}),
	)

	reader := NewReader(path, Options{})
	turns, err := reader.Turns()
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("len(turns) = %d, want 1", len(turns))
	}
	turn := turns[0]
	if turn.Status != StatusCompleted {
		t.Errorf("status = %q, want %q", turn.Status, StatusCompleted)
	}
	if turn.Steps != 2 {
		t.Errorf("steps = %d, want 2", turn.Steps)
	}
	if turn.ElapsedMS != 2541 {
		t.Errorf("elapsed = %d, want 2541", turn.ElapsedMS)
	}
	if turn.SettledBy != "present_dialogue" {
		t.Errorf("settled by = %q, want present_dialogue", turn.SettledBy)
	}
	if turn.EntityID != "npc:Penny" || turn.EventType != "player_said_to_npc" {
		t.Errorf("turn identity = %q/%q, want npc:Penny/player_said_to_npc", turn.EntityID, turn.EventType)
	}
	// A repeated tool is collapsed: the client shows what the model chose, not
	// how many times it retried.
	if got := strings.Join(turn.Tools, ","); got != "send_mail,present_dialogue" {
		t.Errorf("tools = %q, want send_mail,present_dialogue", got)
	}
	if got := strings.Join(turn.AvailableTools, ","); got != "send_mail,present_dialogue" {
		t.Errorf("available tools = %q", got)
	}
}

func TestTurnsProjectsAFailedTurn(t *testing.T) {
	path := writeTrace(t,
		turnStarted(t, "turn_a", "npc:Linus", "player_interacted_with_npc"),
		eventLine(t, trace.Event{
			TurnID: "turn_a", Seq: 9, Event: trace.EventTurnFailed,
			Time: time.Date(2026, 9, 18, 21, 9, 0, 0, time.UTC), ElapsedMS: 30000,
			EntityID: "npc:Linus", Reason: "max_steps_exceeded", ErrorMessage: "max steps exceeded: max 5",
		}),
	)

	turns, err := NewReader(path, Options{}).Turns()
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	turn := turns[0]
	if turn.Status != StatusFailed {
		t.Errorf("status = %q, want %q", turn.Status, StatusFailed)
	}
	if turn.Reason != "max_steps_exceeded" {
		t.Errorf("reason = %q, want max_steps_exceeded", turn.Reason)
	}
	if turn.Error == "" {
		t.Error("a failed turn must keep its error message")
	}
}

func TestTurnsMarksATurnWithoutTerminalEventAsUnfinished(t *testing.T) {
	path := writeTrace(t,
		turnStarted(t, "turn_a", "npc:Penny", "player_said_to_npc"),
		stepStarted(t, "turn_a", 2, 40),
	)

	turns, err := NewReader(path, Options{}).Turns()
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if turns[0].Status != StatusUnfinished {
		t.Fatalf("status = %q, want %q", turns[0].Status, StatusUnfinished)
	}
}

func TestTurnsReturnsTheMostRecentTurnFirst(t *testing.T) {
	path := writeTrace(t,
		turnStarted(t, "turn_old", "npc:Penny", "player_said_to_npc"),
		eventLine(t, trace.Event{TurnID: "turn_old", Seq: 2, Event: trace.EventTurnCompleted}),
		turnStarted(t, "turn_new", "npc:Linus", "player_said_to_npc"),
		eventLine(t, trace.Event{TurnID: "turn_new", Seq: 2, Event: trace.EventTurnCompleted}),
	)

	turns, err := NewReader(path, Options{}).Turns()
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("len(turns) = %d, want 2", len(turns))
	}
	if turns[0].TurnID != "turn_new" || turns[1].TurnID != "turn_old" {
		t.Fatalf("order = %q,%q, want turn_new,turn_old", turns[0].TurnID, turns[1].TurnID)
	}
}

// A live Runtime appends to the trace while the client reads it, so the last
// line can be a torn write. It must not fail the projection.
func TestTurnsSkipsATornTrailingLine(t *testing.T) {
	path := writeTrace(t,
		turnStarted(t, "turn_a", "npc:Penny", "player_said_to_npc"),
		`{"turn_id":"turn_a","seq":2,"event":"turn_comple`,
	)

	turns, err := NewReader(path, Options{}).Turns()
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("len(turns) = %d, want 1", len(turns))
	}
	if turns[0].Status != StatusUnfinished {
		t.Fatalf("status = %q, want %q: the terminal event was not readable", turns[0].Status, StatusUnfinished)
	}
}

func TestTurnsSkipsAMalformedLine(t *testing.T) {
	path := writeTrace(t,
		turnStarted(t, "turn_a", "npc:Penny", "player_said_to_npc"),
		`{"turn_id":"turn_a",,"seq":2}`,
		eventLine(t, trace.Event{TurnID: "turn_a", Seq: 3, Event: trace.EventTurnCompleted}),
	)

	turns, err := NewReader(path, Options{}).Turns()
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if turns[0].Status != StatusCompleted {
		t.Fatalf("status = %q, want %q", turns[0].Status, StatusCompleted)
	}
}

func TestTurnsRetainsOnlyTheMostRecentTurns(t *testing.T) {
	lines := make([]string, 0, 6)
	for _, id := range []string{"turn_1", "turn_2", "turn_3"} {
		lines = append(lines, turnStarted(t, id, "npc:Penny", "player_said_to_npc"))
		lines = append(lines, eventLine(t, trace.Event{TurnID: id, Seq: 2, Event: trace.EventTurnCompleted}))
	}

	turns, err := NewReader(writeTrace(t, lines...), Options{MaxTurns: 2}).Turns()
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("len(turns) = %d, want 2", len(turns))
	}
	if turns[0].TurnID != "turn_3" || turns[1].TurnID != "turn_2" {
		t.Fatalf("retained %q,%q, want turn_3,turn_2", turns[0].TurnID, turns[1].TurnID)
	}
}

// Bounding the read to the tail must not emit a fabricated turn built from a
// line that starts mid-record.
func TestTurnsReadsOnlyTheTailWhenBounded(t *testing.T) {
	old := turnStarted(t, "turn_old", "npc:Penny", "player_said_to_npc")
	newer := turnStarted(t, "turn_new", "npc:Linus", "player_said_to_npc")
	completed := eventLine(t, trace.Event{TurnID: "turn_new", Seq: 2, Event: trace.EventTurnCompleted})
	path := writeTrace(t, old, newer, completed)

	// The bound lands inside the first line, so that line is unreadable.
	reader := NewReader(path, Options{MaxBytes: int64(len(newer) + len(completed) + 5)})
	turns, err := reader.Turns()
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("len(turns) = %d, want 1: a partial line must not become a turn", len(turns))
	}
	if turns[0].TurnID != "turn_new" || turns[0].Status != StatusCompleted {
		t.Fatalf("turn = %+v, want the completed turn_new", turns[0])
	}
}

func TestTurnsRefreshesAfterTheTraceGrows(t *testing.T) {
	path := writeTrace(t, turnStarted(t, "turn_a", "npc:Penny", "player_said_to_npc"))
	reader := NewReader(path, Options{})

	first, err := reader.Turns()
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if len(first) != 1 || first[0].Status != StatusUnfinished {
		t.Fatalf("first read = %+v, want one unfinished turn", first)
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open trace for append: %v", err)
	}
	if _, err := file.WriteString(eventLine(t, trace.Event{TurnID: "turn_a", Seq: 2, Event: trace.EventTurnCompleted}) + "\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := reader.Turns()
	if err != nil {
		t.Fatalf("Turns after append: %v", err)
	}
	if second[0].Status != StatusCompleted {
		t.Fatalf("status after append = %q, want %q: the cache must notice a grown trace", second[0].Status, StatusCompleted)
	}
}
