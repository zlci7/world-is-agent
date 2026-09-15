package memory

import (
	"cmp"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
)

const (
	gameTimeYear uint8 = 1 << iota
	gameTimeSeason
	gameTimeDay
	gameTimeHour
	gameTimeMinute
	gameTimeTick
	gameTimeCalendarFields = gameTimeYear | gameTimeSeason | gameTimeDay | gameTimeHour | gameTimeMinute
	gameTimeAllFields      = gameTimeCalendarFields | gameTimeTick
)

type GameTimeBasis uint8

const (
	GameTimeUnknown GameTimeBasis = iota
	GameTimeCalendar
	GameTimeCalendarWithTick
	GameTimeTick
)

// TickGameTime builds a tick-only snapshot for a source that carries no calendar
// fields. It compares only against other tick-only sources.
func TickGameTime(tick int64) *GameTimeSnapshot {
	return &GameTimeSnapshot{Tick: tick, PresentFields: gameTimeTick}
}

func SnapshotGameTime(value *protocolv1alpha2.GameTime) *GameTimeSnapshot {
	if value == nil {
		return nil
	}
	var fields uint8
	for i, present := range []bool{value.Year != nil, value.Season != nil, value.Day != nil, value.Hour != nil, value.Minute != nil, value.Tick != nil} {
		if present {
			fields |= 1 << i
		}
	}
	if fields == 0 {
		return nil
	}
	return &GameTimeSnapshot{
		Year: value.GetYear(), Season: value.GetSeason(), Day: value.GetDay(),
		Hour: value.GetHour(), Minute: value.GetMinute(), Tick: value.GetTick(),
		PresentFields: fields,
	}
}

func CurrentGameTime(event *protocolv1alpha2.GameEvent, observation *protocolv1alpha2.Observation) *GameTimeSnapshot {
	if value := SnapshotGameTime(event.GetGameTime()); SharedGameTimeBasis(value) != GameTimeUnknown {
		return value
	}
	return SnapshotGameTime(observation.GetGameTime())
}

func (s *GameTimeSnapshot) presentFields() uint8 {
	if s == nil {
		return 0
	}
	if s.PresentFields != 0 {
		if s.PresentFields & ^uint8(gameTimeAllFields) != 0 {
			return 0
		}
		return s.PresentFields
	}
	// V1 has no presence information. Its all-zero payload cannot establish a time.
	if s.Year == 0 && s.Season == 0 && s.Day == 0 && s.Hour == 0 && s.Minute == 0 && s.Tick == 0 {
		return 0
	}
	return gameTimeAllFields
}

// SharedGameTimeBasis fixes one comparison basis for a whole candidate set.
func SharedGameTimeBasis(values ...*GameTimeSnapshot) GameTimeBasis {
	if len(values) == 0 {
		return GameTimeUnknown
	}
	calendar, withTick, tickOnly := true, true, true
	for _, value := range values {
		fields := value.presentFields()
		calendar = calendar && fields&gameTimeCalendarFields == gameTimeCalendarFields
		withTick = withTick && fields&gameTimeTick != 0
		tickOnly = tickOnly && fields == gameTimeTick
	}
	if calendar {
		if withTick {
			return GameTimeCalendarWithTick
		}
		return GameTimeCalendar
	}
	if tickOnly {
		return GameTimeTick
	}
	return GameTimeUnknown
}

func (basis GameTimeBasis) Compare(left, right *GameTimeSnapshot) int {
	if basis == GameTimeUnknown || left == nil || right == nil {
		return 0
	}
	if basis == GameTimeCalendar || basis == GameTimeCalendarWithTick {
		leftCalendar := [...]int32{left.Year, left.Season, left.Day, left.Hour, left.Minute}
		rightCalendar := [...]int32{right.Year, right.Season, right.Day, right.Hour, right.Minute}
		for i := range leftCalendar {
			if order := cmp.Compare(leftCalendar[i], rightCalendar[i]); order != 0 {
				return order
			}
		}
	}
	if basis == GameTimeTick || basis == GameTimeCalendarWithTick {
		return cmp.Compare(left.Tick, right.Tick)
	}
	return 0
}
