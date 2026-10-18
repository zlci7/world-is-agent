// Package experience defines the optional, game-independent confirmed-experience extension.
package experience

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"google.golang.org/protobuf/types/known/structpb"
)

const Extension = "gameagent.experiences.v1"

type Record struct {
	ClockID        string `json:"clock_id"`
	OccurredTick   int64  `json:"occurred_tick"`
	FocusUntilTick int64  `json:"focus_until_tick"`
	Priority       int    `json:"priority"`
}

type View struct {
	ClockID      string
	NowTick      int64
	ValidFactIDs map[string]struct{}
}

func (r Record) Validate() error {
	if strings.TrimSpace(r.ClockID) == "" || len(r.ClockID) > 128 || r.OccurredTick < 0 || r.FocusUntilTick <= r.OccurredTick || r.Priority < 0 || r.Priority > 100 {
		return fmt.Errorf("invalid experience clock, focus interval or priority")
	}
	return nil
}

func (v *View) Allows(id string, r *Record) bool {
	if v == nil || r == nil || r.Validate() != nil || r.ClockID != v.ClockID || r.OccurredTick > v.NowTick {
		return false
	}
	_, ok := v.ValidFactIDs[id]
	return ok
}

func HasEvent(event *protocol.GameEvent) bool {
	_, ok := event.GetPayload().GetFields()[Extension]
	return ok
}

func ParseEvent(event *protocol.GameEvent) (Record, error) {
	fields, err := extensionFields(event.GetPayload())
	if err != nil {
		return Record{}, err
	}
	occurred, e1 := tick(fields, "occurred_tick")
	until, e2 := tick(fields, "focus_until_tick")
	priority := fields["priority"].GetNumberValue()
	_, numericPriority := fields["priority"].GetKind().(*structpb.Value_NumberValue)
	r := Record{ClockID: fields["clock_id"].GetStringValue(), OccurredTick: occurred, FocusUntilTick: until, Priority: int(priority)}
	if e1 != nil || e2 != nil || !numericPriority || priority != float64(r.Priority) || r.Validate() != nil {
		return Record{}, fmt.Errorf("invalid experience metadata")
	}
	if event.GetGameTime() == nil || len(event.GetContextFacts()) != 1 || len(event.GetTaskEvidence()) != 0 || event.GetInteractionSource() != nil {
		return Record{}, fmt.Errorf("experience requires game time and exactly one confirmed fact")
	}
	fact := event.ContextFacts[0]
	if fact.GetKind() != "interaction" || fact.GetTargetEntityId() != event.GetTargetEntityId() || fact.GetActorEntityId() == "" || len(fact.ActorEntityId) > 256 || len(fact.Label) > 128 || len(fact.ScopeId) > 128 || strings.TrimSpace(fact.GetText()) == "" || utf8.RuneCountInString(fact.Text) > 1024 {
		return Record{}, fmt.Errorf("invalid confirmed interaction fact")
	}
	actorFound := false
	for _, entity := range event.GetEntities() {
		if entity.GetEntityId() == fact.ActorEntityId {
			actorFound = true
			break
		}
	}
	if !actorFound {
		return Record{}, fmt.Errorf("experience actor is not in event entities")
	}
	if data, err := json.Marshal(fact.GetAttributes().AsMap()); err != nil || len(data) > 4096 {
		return Record{}, fmt.Errorf("experience attributes exceed limit")
	}
	return r, nil
}

func ParseView(observation *protocol.Observation) (*View, error) {
	if _, ok := observation.GetExtensions().GetFields()[Extension]; !ok {
		return nil, nil
	}
	fields, err := extensionFields(observation.GetExtensions())
	if err != nil {
		return nil, err
	}
	now, err := tick(fields, "now_tick")
	if err != nil || fields["clock_id"].GetStringValue() == "" {
		return nil, fmt.Errorf("invalid experience view clock")
	}
	ids := fields["valid_fact_ids"].GetListValue()
	if ids == nil {
		return nil, fmt.Errorf("experience view requires valid_fact_ids")
	}
	view := &View{ClockID: fields["clock_id"].GetStringValue(), NowTick: now, ValidFactIDs: make(map[string]struct{}, len(ids.Values))}
	for _, value := range ids.Values {
		id := value.GetStringValue()
		if id == "" || len(id) > 128 {
			return nil, fmt.Errorf("invalid experience fact id")
		}
		view.ValidFactIDs[id] = struct{}{}
	}
	return view, nil
}

func extensionFields(value *structpb.Struct) (map[string]*structpb.Value, error) {
	fields := value.GetFields()[Extension].GetStructValue().GetFields()
	if fields == nil || fields["version"].GetNumberValue() != 1 {
		return nil, fmt.Errorf("unsupported experience extension version")
	}
	return fields, nil
}

func tick(fields map[string]*structpb.Value, key string) (int64, error) {
	text := fields[key].GetStringValue()
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || n < 0 || strconv.FormatInt(n, 10) != text {
		return 0, fmt.Errorf("%s must be a nonnegative decimal int64 string", key)
	}
	return n, nil
}
