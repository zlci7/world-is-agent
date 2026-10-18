package agent

import (
	"context"
	"strings"
	"testing"

	"gameagent/runtime/internal/experience"
	"gameagent/runtime/internal/memory"
)

func TestExperienceSourcesStayOutsideOrdinaryCompaction(t *testing.T) {
	store := newPreparationStore()
	gift, err := store.AppendHistory(context.Background(), memory.HistoryBatch{Owner: preparationOwner, Kind: memory.HistoryKindExperience, Version: memory.HistoryVersion,
		Experience: &experience.Record{ClockID: "generic.clock", OccurredTick: 1, FocusUntilTick: 10, Priority: 50},
		Event:      memory.HistoryEvent{ID: "confirmed-receipt", GameTime: preparationTime(8), Facts: []memory.SourceContextFact{{Kind: "interaction", ActorEntityID: "player", TargetEntityID: preparationOwner.EntityID, Text: "confirmed-experience-detail"}}}})
	if err != nil {
		t.Fatal(err)
	}
	appendPreparationSource(t, store, "older", strings.Repeat("ordinary conversation ", 40), preparationTime(8))
	appendPreparationSource(t, store, "newest", strings.Repeat("latest conversation ", 100), preparationTime(8))
	generator := &preparationGenerator{}
	loop := preparationLoop(store)
	enablePreparationCompaction(loop, generator)
	prepared := loop.prepareHistory(context.Background(), nil, preparationOwner, preparationTime(10), 200)
	defer prepared.Release()
	if len(generator.requests) != 1 || prepared.Input.Summary == nil {
		t.Fatal("ordinary compaction did not execute")
	}
	for _, source := range prepared.Input.Sources {
		if source.ID == gift.ID {
			t.Fatal("experience entered ordinary recent history")
		}
	}
	for _, ref := range prepared.Input.Summary.Sources {
		if ref.ID == gift.ID {
			t.Fatal("ordinary summary claimed coverage of an experience")
		}
	}
}
