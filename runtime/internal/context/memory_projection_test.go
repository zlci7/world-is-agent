package context_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	agentcontext "gameagent/runtime/internal/context"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/session"
)

func TestRecentMemoryUsesCreatedAtForEmptyGameTime(t *testing.T) {
	records := []memory.Record{
		timelineRecord(t, "older", 100, timelineClock(11), 1),
		timelineRecord(t, "newer", 200, &protocolv1alpha2.GameTime{}, 2),
	}
	assertTimelineIDs(t, timelineProjection(t, records, 1, timelineClock(12), nil), []string{"newer"})
}

func TestRecentMemoryOrderIsIndependentOfInputPermutation(t *testing.T) {
	cases := []struct {
		name    string
		records []memory.Record
	}{
		{
			name: "unknown-time",
			records: []memory.Record{
				timelineRecord(t, "a", 100, timelineClock(11), 0),
				timelineRecord(t, "b", 200, nil, 0),
				timelineRecord(t, "c", 300, timelineClock(10), 0),
			},
		},
		{
			name: "missing-sequence",
			records: []memory.Record{
				timelineRecord(t, "a", 100, timelineClock(11), 2),
				timelineRecord(t, "b", 200, timelineClock(11), 0),
				timelineRecord(t, "c", 300, timelineClock(11), 1),
			},
		},
		{
			name: "mixed-clock-precision",
			records: []memory.Record{
				timelineRecord(t, "a", 100, timelineClockWithTick(11, 2), 0),
				timelineRecord(t, "b", 200, timelineClock(11), 0),
				timelineRecord(t, "c", 300, timelineClockWithTick(11, 1), 0),
			},
		},
	}
	permutations := [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, order := range permutations {
				records := []memory.Record{tc.records[order[0]], tc.records[order[1]], tc.records[order[2]]}
				assertTimelineIDs(t, timelineProjection(t, records, 3, timelineClock(12), nil), []string{"a", "b", "c"})
				assertTimelineIDs(t, timelineProjection(t, records, 1, timelineClock(12), nil), []string{"c"})
			}
		})
	}
}

func TestRecentMemoryUsesGameTimeThenCompleteSequenceGroup(t *testing.T) {
	records := []memory.Record{
		timelineRecord(t, "later", 100, timelineClock(11), 1),
		timelineRecord(t, "second", 200, timelineClock(10), 2),
		timelineRecord(t, "first", 300, timelineClock(10), 1),
	}
	assertTimelineIDs(t, timelineProjection(t, records, 3, timelineClock(12), nil), []string{"first", "second", "later"})
	assertTimelineIDs(t, timelineProjection(t, records, 2, timelineClock(12), nil), []string{"second", "later"})
}

func TestRecentMemoryChoosesSequencePolicyPerGameTimeGroup(t *testing.T) {
	records := []memory.Record{
		timelineRecord(t, "d", 400, timelineClock(11), 0),
		timelineRecord(t, "c", 300, timelineClock(11), 2),
		timelineRecord(t, "b", 100, timelineClock(10), 2),
		timelineRecord(t, "a", 200, timelineClock(10), 1),
	}
	assertTimelineIDs(t, timelineProjection(t, records, 4, timelineClock(12), nil), []string{"a", "b", "c", "d"})
}

func TestRecentMemoryUsesMemoryIDForEqualCreatedAt(t *testing.T) {
	records := []memory.Record{
		timelineRecord(t, "b", 100, nil, 0),
		timelineRecord(t, "a", 100, nil, 0),
	}
	assertTimelineIDs(t, timelineProjection(t, records, 2, nil, nil), []string{"a", "b"})
}

func TestRecentMemoryRetainsUnknownTimeAndFiltersComparableFuture(t *testing.T) {
	records := []memory.Record{
		timelineRecord(t, "visible", 100, timelineClock(11), 0),
		timelineRecord(t, "partial", 200, &protocolv1alpha2.GameTime{Year: ptrInt32(2)}, 0),
		timelineRecord(t, "future", 300, timelineClock(13), 0),
	}
	assertTimelineIDs(t, timelineProjection(t, records, 2, timelineClock(12), nil), []string{"visible", "partial"})
}

func TestRecentMemoryUsesObservationTimeWhenEventTimeIsEmpty(t *testing.T) {
	records := []memory.Record{
		timelineRecord(t, "visible", 100, timelineClock(11), 0),
		timelineRecord(t, "future", 200, timelineClock(13), 0),
	}
	assertTimelineIDs(t, timelineProjection(t, records, 2, &protocolv1alpha2.GameTime{}, timelineClock(12)), []string{"visible"})
}

func TestRecentMemoryPreservesZeroTickPresenceThroughJSON(t *testing.T) {
	records := []memory.Record{
		timelineRecord(t, "tick-one", 100, &protocolv1alpha2.GameTime{Tick: ptrInt64(1)}, 0),
		timelineRecord(t, "tick-zero", 200, &protocolv1alpha2.GameTime{Tick: ptrInt64(0)}, 0),
		timelineRecord(t, "future", 300, &protocolv1alpha2.GameTime{Tick: ptrInt64(3)}, 0),
	}
	for i := range records {
		data, err := json.Marshal(records[i].GameTime)
		if err != nil {
			t.Fatal(err)
		}
		var restored *memory.GameTimeSnapshot
		if err := json.Unmarshal(data, &restored); err != nil {
			t.Fatal(err)
		}
		records[i].GameTime = restored
	}
	assertTimelineIDs(t, timelineProjection(t, records, 3, &protocolv1alpha2.GameTime{Tick: ptrInt64(2)}, nil), []string{"tick-zero", "tick-one"})
}

func TestRecentMemoryUsesCreatedAtAcrossCalendarAndTickOnlyTime(t *testing.T) {
	records := []memory.Record{
		timelineRecord(t, "older-calendar", 100, timelineClock(11), 0),
		timelineRecord(t, "newer-tick", 200, &protocolv1alpha2.GameTime{Tick: ptrInt64(5)}, 0),
	}
	assertTimelineIDs(t, timelineProjection(t, records, 1, timelineClock(12), nil), []string{"newer-tick"})
}

func TestRecentMemoryTreatsLegacyZeroSnapshotAsUnknown(t *testing.T) {
	older := timelineRecord(t, "older", 100, timelineClock(11), 0)
	newer := timelineRecord(t, "newer", 200, nil, 0)
	if err := json.Unmarshal([]byte(`{"Year":0,"Season":0,"Day":0,"Hour":0,"Minute":0,"Tick":0}`), &newer.GameTime); err != nil {
		t.Fatal(err)
	}
	assertTimelineIDs(t, timelineProjection(t, []memory.Record{older, newer}, 1, timelineClock(12), nil), []string{"newer"})
}

func timelineRecord(t *testing.T, id string, created int64, gameTime *protocolv1alpha2.GameTime, sequence uint64) memory.Record {
	t.Helper()
	record, err := memory.NewProjector(func() time.Time { return time.Unix(created, 0) }).Project(memory.ProjectInput{
		SessionKey:     session.AgentSessionKey{GameID: "fake-game", WorldID: "world", EntityID: "entity"},
		TurnID:         "turn-" + id,
		ProjectionKind: memory.ProjectionKindSettledTurn,
		Event: &protocolv1alpha2.GameEvent{
			EventId: "event-" + id, GameTime: gameTime, Sequence: sequence,
			ContextFacts: []*protocolv1alpha2.ContextFact{{Kind: "utterance", Text: id}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	record.MemoryID = id
	return record
}

func timelineClock(hour int32) *protocolv1alpha2.GameTime {
	return &protocolv1alpha2.GameTime{Year: ptrInt32(1), Season: ptrInt32(1), Day: ptrInt32(1), Hour: ptrInt32(hour), Minute: ptrInt32(0)}
}

func timelineClockWithTick(hour int32, tick int64) *protocolv1alpha2.GameTime {
	clock := timelineClock(hour)
	clock.Tick = ptrInt64(tick)
	return clock
}

func timelineProjection(t *testing.T, records []memory.Record, limit int, eventTime, observationTime *protocolv1alpha2.GameTime) agentcontext.ContextProjection {
	t.Helper()
	projection, err := buildProjection(t, agentcontext.BuildInput{
		SessionKey:     session.AgentSessionKey{GameID: "fake-game", WorldID: "world", EntityID: "entity"},
		RecentMemories: records,
		Event:          &protocolv1alpha2.GameEvent{EventId: "current", GameTime: eventTime},
		Observation:    &protocolv1alpha2.Observation{GameTime: observationTime},
	}, agentcontext.EngineConfig{MaxRecentMemoryRecords: limit, MaxRecentMemoryTokens: 4096})
	if err != nil {
		t.Fatal(err)
	}
	return projection
}

func assertTimelineIDs(t *testing.T, projection agentcontext.ContextProjection, want []string) {
	t.Helper()
	got := make([]string, len(projection.RecentMemory))
	for i, item := range projection.RecentMemory {
		got[i] = item.MemoryID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recent memory IDs=%v, want %v", got, want)
	}
}
