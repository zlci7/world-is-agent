package task

import (
	"encoding/json"

	"gameagent/runtime/internal/session"
)

type ownerWire struct {
	GameID   string `json:"game_id"`
	WorldID  string `json:"world_id"`
	EntityID string `json:"entity_id"`
}

func (e ExecutionContext) MarshalJSON() ([]byte, error) {
	type alias ExecutionContext
	return json.Marshal(struct {
		alias
		Owner ownerWire `json:"owner"`
	}{
		alias: alias(e),
		Owner: encodeOwner(e.Owner),
	})
}

func (e *ExecutionContext) UnmarshalJSON(data []byte) error {
	type alias ExecutionContext
	var wire struct {
		alias
		Owner json.RawMessage `json:"owner"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	owner, err := decodeOwner(wire.Owner)
	if err != nil {
		return err
	}
	wire.alias.Owner = owner
	*e = ExecutionContext(wire.alias)
	return nil
}

func (r Record) MarshalJSON() ([]byte, error) {
	type alias Record
	return json.Marshal(struct {
		alias
		Owner ownerWire `json:"owner"`
	}{
		alias: alias(r),
		Owner: encodeOwner(r.Owner),
	})
}

func (r *Record) UnmarshalJSON(data []byte) error {
	type alias Record
	var wire struct {
		alias
		Owner json.RawMessage `json:"owner"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	owner, err := decodeOwner(wire.Owner)
	if err != nil {
		return err
	}
	wire.alias.Owner = owner
	*r = Record(wire.alias)
	return nil
}

func (w Wake) MarshalJSON() ([]byte, error) {
	type alias Wake
	return json.Marshal(struct {
		alias
		Owner ownerWire `json:"owner"`
	}{
		alias: alias(w),
		Owner: encodeOwner(w.Owner),
	})
}

func (w *Wake) UnmarshalJSON(data []byte) error {
	type alias Wake
	var wire struct {
		alias
		Owner json.RawMessage `json:"owner"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	owner, err := decodeOwner(wire.Owner)
	if err != nil {
		return err
	}
	wire.alias.Owner = owner
	*w = Wake(wire.alias)
	return nil
}

func encodeOwner(owner session.AgentSessionKey) ownerWire {
	return ownerWire{
		GameID:   owner.GameID,
		WorldID:  owner.WorldID,
		EntityID: owner.EntityID,
	}
}

func decodeOwner(data json.RawMessage) (session.AgentSessionKey, error) {
	var fields map[string]json.RawMessage
	if len(data) == 0 || json.Unmarshal(data, &fields) != nil || len(fields) != 3 {
		return session.AgentSessionKey{}, ErrInvalidTaskSpec
	}
	for _, key := range []string{"game_id", "world_id", "entity_id"} {
		if _, ok := fields[key]; !ok {
			return session.AgentSessionKey{}, ErrInvalidTaskSpec
		}
	}

	var wire ownerWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return session.AgentSessionKey{}, ErrInvalidTaskSpec
	}
	owner := session.AgentSessionKey{
		GameID:   wire.GameID,
		WorldID:  wire.WorldID,
		EntityID: wire.EntityID,
	}
	if err := validateOwner(owner); err != nil {
		return session.AgentSessionKey{}, err
	}
	return owner, nil
}
