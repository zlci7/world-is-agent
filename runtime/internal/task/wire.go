package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

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
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return session.AgentSessionKey{}, ErrInvalidTaskSpec
	}

	owner := session.AgentSessionKey{}
	seen := make(map[string]struct{}, 3)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return session.AgentSessionKey{}, ErrInvalidTaskSpec
		}
		key, ok := keyToken.(string)
		if !ok {
			return session.AgentSessionKey{}, ErrInvalidTaskSpec
		}
		if _, duplicate := seen[key]; duplicate {
			return session.AgentSessionKey{}, ErrInvalidTaskSpec
		}
		seen[key] = struct{}{}

		switch key {
		case "game_id", "world_id", "entity_id":
		default:
			return session.AgentSessionKey{}, ErrInvalidTaskSpec
		}

		valueToken, err := decoder.Token()
		if err != nil {
			return session.AgentSessionKey{}, ErrInvalidTaskSpec
		}
		value, ok := valueToken.(string)
		if !ok {
			return session.AgentSessionKey{}, ErrInvalidTaskSpec
		}

		switch key {
		case "game_id":
			owner.GameID = value
		case "world_id":
			owner.WorldID = value
		case "entity_id":
			owner.EntityID = value
		}
	}

	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') || len(seen) != 3 {
		return session.AgentSessionKey{}, ErrInvalidTaskSpec
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return session.AgentSessionKey{}, ErrInvalidTaskSpec
	}

	if err := validateOwner(owner); err != nil {
		return session.AgentSessionKey{}, err
	}
	return owner, nil
}
