package memory

import (
	"errors"
	"strings"
	"testing"

	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
)

func testHistoryBatch(turn string) HistoryBatch {
	return HistoryBatch{Owner: session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "agent-1"}, Kind: HistoryKindTerminal, Version: HistoryVersion, TurnID: turn, Event: HistoryEvent{ID: "event-" + turn, Type: "generic.input", Facts: []SourceContextFact{{Kind: "utterance", ActorEntityID: "player", Text: "I do not like rain. The number is 7319."}}}, Terminal: HistoryTerminal{Status: "completed"}}
}

func TestCanonicalHistoryBatchStableAndComplete(t *testing.T) {
	a := testHistoryBatch("turn-1")
	a.Steps = []HistoryStep{{Index: 1, Decision: model.ModelDecision{ToolCalls: []model.ToolCall{{ID: "call-1", Name: "speak", Arguments: map[string]any{"z": 2, "a": "hello"}}}}}}
	data, key, fingerprint, err := CanonicalHistoryBatch(a, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "7319") || !strings.Contains(string(data), "do not like") {
		t.Fatalf("original lost: %s", data)
	}
	a.Steps[0].Decision.ToolCalls[0].Arguments = map[string]any{"a": "hello", "z": 2}
	_, key2, fp2, err := CanonicalHistoryBatch(a, 8<<20)
	if err != nil || key != key2 || fingerprint != fp2 {
		t.Fatalf("not canonical: %s %s %v", fingerprint, fp2, err)
	}
	a.Event.Facts[0].Text = "different"
	_, key3, fp3, err := CanonicalHistoryBatch(a, 8<<20)
	if err != nil || key3 != key || fp3 == fingerprint {
		t.Fatalf("identity/content conflated: %v", err)
	}
}

func TestCanonicalHistoryBatchCapacityBoundary(t *testing.T) {
	b := testHistoryBatch("t")
	data, _, _, err := CanonicalHistoryBatch(b, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, delta := range []int{-1, 0, 1} {
		_, _, _, err := CanonicalHistoryBatch(b, len(data)+delta)
		if delta < 0 && !errors.Is(err, ErrHistoryCapacity) {
			t.Fatalf("below: %v", err)
		}
		if delta >= 0 && err != nil {
			t.Fatalf("at/above: %v", err)
		}
	}
}

func TestCanonicalHistoryRejectsInvalidIdentity(t *testing.T) {
	for _, change := range []func(*HistoryBatch){func(b *HistoryBatch) { b.Owner.EntityID = "" }, func(b *HistoryBatch) { b.TurnID = "" }, func(b *HistoryBatch) { b.Kind = "invented" }, func(b *HistoryBatch) { b.Version = 0 }, func(b *HistoryBatch) { b.Terminal.Status = "" }} {
		b := testHistoryBatch("t")
		change(&b)
		if _, _, _, err := CanonicalHistoryBatch(b, 8<<20); !errors.Is(err, ErrInvalidHistory) {
			t.Fatalf("got %v", err)
		}
	}
}
