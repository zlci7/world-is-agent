package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/experience"
	"gameagent/runtime/internal/session"
)

const HistoryKindExperience = "experience"

type ExperienceQuery struct {
	Snapshot HistorySnapshot
	View     *experience.View
	Limit    int
}

type ExperienceStore interface {
	ReadExperiences(context.Context, ExperienceQuery) ([]HistorySource, error)
}

func experienceFactID(batch HistoryBatch) string {
	if batch.Experience != nil {
		return batch.Event.ID
	}
	return ""
}

func validExperienceBatch(batch HistoryBatch) bool {
	if batch.Experience == nil || batch.Experience.Validate() != nil || batch.Event.ID == "" || len(batch.Event.ID) > 128 || batch.Event.GameTime == nil || len(batch.Event.Facts) != 1 {
		return false
	}
	fact := batch.Event.Facts[0]
	if fact.Kind != "interaction" || fact.ActorEntityID == "" || len(fact.ActorEntityID) > 256 || fact.TargetEntityID != batch.Owner.EntityID || strings.TrimSpace(fact.Text) == "" || utf8.RuneCountInString(fact.Text) > 1024 || len(fact.ScopeID) > 128 || len(fact.Label) > 128 {
		return false
	}
	attributes, err := json.Marshal(fact.Attributes)
	if err != nil || len(attributes) > 4096 {
		return false
	}
	return batch.Legacy == nil && batch.TaskResult == nil && batch.TurnID == "" && batch.Terminal == (HistoryTerminal{}) && len(batch.Steps) == 0 && len(batch.Observations) == 0
}

func ExperienceHistoryBatch(owner session.AgentSessionKey, event *protocol.GameEvent) (HistoryBatch, error) {
	meta, err := experience.ParseEvent(event)
	if err != nil {
		return HistoryBatch{}, err
	}
	if owner.WorldID != event.WorldId || owner.EntityID != event.TargetEntityId || event.EventId == "" || len(event.EventId) > 128 {
		return HistoryBatch{}, fmt.Errorf("invalid experience owner or id")
	}
	if SnapshotGameTime(event.GameTime) == nil {
		return HistoryBatch{}, fmt.Errorf("experience game time must contain a field")
	}
	f := event.ContextFacts[0]
	return HistoryBatch{Owner: owner, Kind: HistoryKindExperience, Version: HistoryVersion, Experience: &meta,
		Event: HistoryEvent{ID: event.EventId, Type: event.EventType, GameTime: SnapshotGameTime(event.GameTime), Facts: []SourceContextFact{{Kind: f.Kind, ActorEntityID: f.ActorEntityId, TargetEntityID: f.TargetEntityId, ScopeID: f.ScopeId, Text: f.Text, Label: f.Label, Attributes: f.GetAttributes().AsMap()}}}}, nil
}

// ExperienceVisible applies the save's receipt membership even after focus expiry.
func ExperienceVisible(source HistorySource, view *experience.View) bool {
	if source.Experience == nil && (source.Batch == nil || source.Batch.Kind != HistoryKindExperience) {
		return true
	}
	meta := source.Experience
	id := source.FactID
	if source.Batch != nil {
		meta = source.Batch.Experience
		id = source.Batch.Event.ID
	}
	return view.Allows(id, meta)
}
