package task

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"gameagent/runtime/internal/session"
)

const (
	wakeReasonIntentWait = "intent_wait"
	wakeStatusConsumed   = "consumed"

	intentStageTaskUpdated   = "task_updated"
	intentStageWakesConsumed = "wakes_consumed"
	intentStageWakeInserted  = "wake_inserted"
	intentStageHistoryStored = "history_stored"

	intentTerminalStructuralReserve = 8 << 10
)

type intentRequest struct {
	TaskID           string    `json:"task_id"`
	WakeID           string    `json:"wake_id,omitempty"`
	ExpectedRevision uint64    `json:"expected_revision"`
	Source           SourceRef `json:"source"`
	Intent           Intent    `json:"intent"`
}

type intentCall struct {
	RequestJSON  json.RawMessage `json:"request"`
	RequestHash  string          `json:"request_hash"`
	ResponseJSON json.RawMessage `json:"response"`
	ResponseHash string          `json:"response_hash"`
}

type intentCallKey struct {
	eventID string
	turnID  string
	callID  string
}

type ownerCallIdentityMatch struct {
	isCreate          bool
	createResult      CreateResult
	createFingerprint string
	intentCall        intentCall
	intentResponse    Record
}

type preparedIntentRequest struct {
	request     intentRequest
	requestJSON []byte
	fingerprint string
}

type storedIntentTask struct {
	record  Record
	history []intentCall
}

type preparedIntentMutation struct {
	record      Record
	recordJSON  []byte
	historyJSON []byte
	historyHash string
}

func prepareIntentRequest(exec ExecutionContext, intent Intent) (preparedIntentRequest, error) {
	request := intentRequest{
		TaskID: exec.TaskID, WakeID: exec.WakeID, ExpectedRevision: exec.ExpectedRevision,
		Source: exec.Source, Intent: intent,
	}
	data, err := json.Marshal(request)
	if err != nil {
		return preparedIntentRequest{}, ErrInvalidTaskSpec
	}
	var cloned intentRequest
	if err := json.Unmarshal(data, &cloned); err != nil || cloned.validate() != nil {
		return preparedIntentRequest{}, ErrInvalidTaskSpec
	}
	canonical, err := json.Marshal(cloned)
	if err != nil || !bytes.Equal(data, canonical) {
		return preparedIntentRequest{}, ErrInvalidTaskSpec
	}
	return preparedIntentRequest{request: cloned, requestJSON: canonical, fingerprint: sha256Hex(canonical)}, nil
}

func (r intentRequest) validate() error {
	if !requiredIdentity(r.TaskID) || !optionalIdentity(r.WakeID) {
		return ErrInvalidTaskSpec
	}
	if err := ValidateDurableCounter(r.ExpectedRevision); err != nil {
		return err
	}
	if err := r.Source.Validate(); err != nil {
		return err
	}
	if !requiredIdentity(r.Source.EventID) || !requiredIdentity(r.Source.TurnID) || !requiredIdentity(r.Source.CallID) {
		return ErrSourceInvalid
	}
	return r.Intent.Validate()
}

func decodeIntentHistory(raw []byte, hash string, current Record) ([]intentCall, error) {
	if hash != sha256Hex(raw) || len(raw) == 0 {
		return nil, ErrInvalidTaskSpec
	}
	var history []intentCall
	if err := json.Unmarshal(raw, &history); err != nil || history == nil {
		return nil, ErrInvalidTaskSpec
	}
	canonical, err := json.Marshal(history)
	if err != nil || !bytes.Equal(raw, canonical) {
		return nil, ErrInvalidTaskSpec
	}
	seen := make(map[intentCallKey]struct{}, len(history))
	for _, call := range history {
		request, _, err := validateIntentCall(call, current)
		if err != nil {
			return nil, err
		}
		key := intentExactKey(request.Source)
		if _, duplicate := seen[key]; duplicate {
			return nil, ErrInvalidTaskSpec
		}
		seen[key] = struct{}{}
	}
	return history, nil
}

func validateIntentCall(call intentCall, current Record) (intentRequest, Record, error) {
	if call.RequestHash != sha256Hex(call.RequestJSON) || call.ResponseHash != sha256Hex(call.ResponseJSON) {
		return intentRequest{}, Record{}, ErrInvalidTaskSpec
	}
	var request intentRequest
	if err := json.Unmarshal(call.RequestJSON, &request); err != nil || request.validate() != nil {
		return intentRequest{}, Record{}, ErrInvalidTaskSpec
	}
	requestJSON, err := json.Marshal(request)
	if err != nil || !bytes.Equal(call.RequestJSON, requestJSON) {
		return intentRequest{}, Record{}, ErrInvalidTaskSpec
	}
	var response Record
	if err := json.Unmarshal(call.ResponseJSON, &response); err != nil || response.Validate() != nil {
		return intentRequest{}, Record{}, ErrInvalidTaskSpec
	}
	responseJSON, err := json.Marshal(response)
	if err != nil || !bytes.Equal(call.ResponseJSON, responseJSON) {
		return intentRequest{}, Record{}, ErrInvalidTaskSpec
	}
	if request.TaskID != current.ID || response.ID != current.ID || response.Owner != current.Owner ||
		response.CreatedAtGameTick != current.CreatedAtGameTick || response.CreatedAtUnixMS != current.CreatedAtUnixMS ||
		!taskSpecsEqual(response.Spec, current.Spec) || response.Revision <= request.ExpectedRevision ||
		response.Revision != request.ExpectedRevision+1 || response.Revision > current.Revision ||
		response.NeedsReconcile || response.PauseReason != "" ||
		response.NoProgressAttempts != 0 || response.ReconcileAttempts != 0 {
		return intentRequest{}, Record{}, ErrInvalidTaskSpec
	}
	if response.Revision == current.Revision && !bytes.Equal(response.Progress, current.Progress) {
		return intentRequest{}, Record{}, ErrInvalidTaskSpec
	}
	switch request.Intent.Kind {
	case "wait":
		if response.State != StateWaiting || response.NextWakeAt == nil ||
			request.Intent.NextWakeAt == nil || *response.NextWakeAt != *request.Intent.NextWakeAt || response.Result != nil {
			return intentRequest{}, Record{}, ErrInvalidTaskSpec
		}
		if request.Intent.ProgressNote != "" {
			want, err := modelProgressJSON(request.Intent.ProgressNote)
			if err != nil || !bytes.Equal(response.Progress, want) {
				return intentRequest{}, Record{}, ErrInvalidTaskSpec
			}
		}
	case "cancel":
		if response.State != StateCancelled || response.NextWakeAt != nil || response.Result == nil ||
			!strings.HasPrefix(response.Result.ID, "result_") || response.Result.TaskID != response.ID ||
			response.Result.Revision != response.Revision || response.Result.State != StateCancelled ||
			response.Result.Reason != request.Intent.Reason || response.Result.Source.Kind != request.Source.Kind ||
			!sourceRefsEqual(response.Result.Source, request.Source) || response.Result.EvidenceRefs == nil ||
			len(response.Result.EvidenceRefs) != 0 || current.State != StateCancelled ||
			current.Revision != response.Revision || current.NextWakeAt != nil || current.Result == nil ||
			!reflect.DeepEqual(current.Result, response.Result) {
			return intentRequest{}, Record{}, ErrInvalidTaskSpec
		}
	default:
		return intentRequest{}, Record{}, ErrInvalidTaskSpec
	}
	return request, response, nil
}

func intentExactKey(source SourceRef) intentCallKey {
	return intentCallKey{eventID: source.EventID, turnID: source.TurnID, callID: source.CallID}
}

func taskSpecsEqual(left, right TaskSpec) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func modelProgressJSON(note string) ([]byte, error) {
	return json.Marshal(struct {
		Author string `json:"author"`
		Kind   string `json:"kind"`
		Note   string `json:"note"`
	}{Author: "model", Kind: "explanation", Note: note})
}

func (s *SQLiteStore) loadIntentTaskTx(ctx context.Context, tx *sql.Tx, owner session.AgentSessionKey, taskID string) (storedIntentTask, error) {
	row := tx.QueryRowContext(ctx, taskSelectSQL+` WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?`,
		owner.GameID, owner.WorldID, owner.EntityID, taskID)
	record, _, _, history, err := scanTaskRowWithMetadata(row)
	if errors.Is(err, sql.ErrNoRows) {
		return storedIntentTask{}, ErrTaskNotFound
	}
	if err != nil {
		return storedIntentTask{}, err
	}
	return storedIntentTask{record: record, history: history}, nil
}

func (s *SQLiteStore) loadOwnerCallIdentityTx(ctx context.Context, tx *sql.Tx, owner session.AgentSessionKey, target intentCallKey) (ownerCallIdentityMatch, bool, error) {
	rows, err := tx.QueryContext(ctx, taskSelectSQL+` WHERE game_id = ? AND world_id = ? AND entity_id = ? ORDER BY task_id`,
		owner.GameID, owner.WorldID, owner.EntityID)
	if err != nil {
		return ownerCallIdentityMatch{}, false, err
	}
	defer rows.Close()
	seen := make(map[intentCallKey]struct{})
	var match *ownerCallIdentityMatch
	for rows.Next() {
		current, createResult, createFingerprint, history, err := scanTaskRowWithMetadata(rows)
		if err != nil {
			return ownerCallIdentityMatch{}, false, err
		}
		createKey := intentExactKey(current.Spec.Source)
		if _, duplicate := seen[createKey]; duplicate {
			return ownerCallIdentityMatch{}, false, ErrInvalidTaskSpec
		}
		seen[createKey] = struct{}{}
		if createKey == target {
			candidate := ownerCallIdentityMatch{
				isCreate: true, createResult: createResult, createFingerprint: createFingerprint,
			}
			match = &candidate
		}
		for _, call := range history {
			request, response, err := validateIntentCall(call, current)
			if err != nil {
				return ownerCallIdentityMatch{}, false, err
			}
			key := intentExactKey(request.Source)
			if _, duplicate := seen[key]; duplicate {
				return ownerCallIdentityMatch{}, false, ErrInvalidTaskSpec
			}
			seen[key] = struct{}{}
			if key == target {
				candidate := ownerCallIdentityMatch{
					intentCall: call, intentResponse: response,
				}
				match = &candidate
			}
		}
	}
	if err := rows.Err(); err != nil {
		return ownerCallIdentityMatch{}, false, err
	}
	if match == nil {
		return ownerCallIdentityMatch{}, false, nil
	}
	return *match, true, nil
}

func (s *SQLiteStore) loadExactIntentTx(ctx context.Context, tx *sql.Tx, owner session.AgentSessionKey, incoming preparedIntentRequest) (Record, bool, error) {
	match, found, err := s.loadOwnerCallIdentityTx(ctx, tx, owner, intentExactKey(incoming.request.Source))
	if err != nil || !found {
		return Record{}, found, err
	}
	if match.isCreate || match.intentCall.RequestHash != incoming.fingerprint ||
		!bytes.Equal(match.intentCall.RequestJSON, incoming.requestJSON) {
		return Record{}, true, ErrIdempotencyConflict
	}
	return match.intentResponse, true, nil
}

func (s *SQLiteStore) prepareIntentMutation(current storedIntentTask, response Record, request preparedIntentRequest, reserveTerminal bool) (preparedIntentMutation, error) {
	if err := response.Validate(); err != nil {
		return preparedIntentMutation{}, err
	}
	recordJSON, err := json.Marshal(response)
	if err != nil {
		return preparedIntentMutation{}, ErrInvalidTaskSpec
	}
	responseJSON := append([]byte(nil), recordJSON...)
	call := intentCall{
		RequestJSON: append(json.RawMessage(nil), request.requestJSON...), RequestHash: request.fingerprint,
		ResponseJSON: responseJSON, ResponseHash: sha256Hex(responseJSON),
	}
	history := append(make([]intentCall, 0, len(current.history)+1), current.history...)
	history = append(history, call)
	historyJSON, err := json.Marshal(history)
	if err != nil {
		return preparedIntentMutation{}, ErrInvalidTaskSpec
	}
	createResponseJSON, err := json.Marshal(initialCreateResult(response))
	if err != nil {
		return preparedIntentMutation{}, ErrInvalidTaskSpec
	}
	parts := []int{len(recordJSON), len(createResponseJSON), len(historyJSON)}
	if reserveTerminal {
		parts = append(parts, len(recordJSON), intentTerminalStructuralReserve)
	}
	if !taskBytesFit(s.options.MaxTaskBytes, parts...) {
		return preparedIntentMutation{}, ErrInvalidTaskSpec
	}
	return preparedIntentMutation{
		record: response, recordJSON: recordJSON,
		historyJSON: historyJSON, historyHash: sha256Hex(historyJSON),
	}, nil
}

func (s *SQLiteStore) updateIntentRecordTx(ctx context.Context, tx *sql.Tx, before Record, prepared preparedIntentMutation) error {
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET state = ?, revision = ?, next_wake_at = ?, record_json = ?
		WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ? AND revision = ?`,
		string(prepared.record.State), int64(prepared.record.Revision), nullableTick(prepared.record.NextWakeAt), prepared.recordJSON,
		before.Owner.GameID, before.Owner.WorldID, before.Owner.EntityID, before.ID, int64(before.Revision))
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrTaskChanged
	}
	return s.afterIntentStage(ctx, intentStageTaskUpdated)
}

func (s *SQLiteStore) consumeIntentWakesTx(ctx context.Context, tx *sql.Tx, record Record) error {
	rows, err := tx.QueryContext(ctx, `SELECT wake_id, game_id, world_id, entity_id, task_id, clock_id,
		expected_revision, due_tick, reason, status, claim_id, claimed_by, generation, attempt,
		retry_after_unix_ms, wake_json FROM task_wakeups
		WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ?
			AND status IN ('pending', 'claimed', 'enqueued', 'running') ORDER BY wake_id`,
		record.Owner.GameID, record.Owner.WorldID, record.Owner.EntityID, record.ID)
	if err != nil {
		return err
	}
	var wakes []Wake
	for rows.Next() {
		var indexed Wake
		var clockID string
		var revision, generation int64
		var raw []byte
		if err := rows.Scan(&indexed.ID, &indexed.Owner.GameID, &indexed.Owner.WorldID, &indexed.Owner.EntityID,
			&indexed.TaskID, &clockID, &revision, &indexed.DueTick, &indexed.Reason, &indexed.Status,
			&indexed.ClaimID, &indexed.ClaimedBy, &generation, &indexed.Attempt, &indexed.RetryAfterUnixMS, &raw); err != nil {
			_ = rows.Close()
			return err
		}
		if revision <= 0 || generation <= 0 {
			_ = rows.Close()
			return ErrInvalidTaskSpec
		}
		indexed.ExpectedRevision, indexed.Generation = uint64(revision), uint64(generation)
		var stored Wake
		canonical, marshalErr := []byte(nil), error(nil)
		if err := json.Unmarshal(raw, &stored); err == nil {
			canonical, marshalErr = json.Marshal(stored)
		} else {
			marshalErr = err
		}
		if marshalErr != nil || stored.Validate() != nil || !bytes.Equal(raw, canonical) ||
			!wakeIndexedValuesEqual(stored, indexed) || clockID != record.Spec.ClockID {
			_ = rows.Close()
			return ErrInvalidTaskSpec
		}
		wakes = append(wakes, stored)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, wake := range wakes {
		priorStatus := wake.Status
		wake.Status = wakeStatusConsumed
		wakeJSON, err := json.Marshal(wake)
		if err != nil {
			return ErrInvalidTaskSpec
		}
		result, err := tx.ExecContext(ctx, `UPDATE task_wakeups SET status = ?, wake_json = ?
			WHERE wake_id = ? AND game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ? AND status = ?`,
			wake.Status, wakeJSON, wake.ID, wake.Owner.GameID, wake.Owner.WorldID, wake.Owner.EntityID, wake.TaskID, priorStatus)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil || affected != 1 {
			if err != nil {
				return err
			}
			return ErrTaskChanged
		}
	}
	return s.afterIntentStage(ctx, intentStageWakesConsumed)
}

func (s *SQLiteStore) insertIntentWakeTx(ctx context.Context, tx *sql.Tx, record Record, wake Wake) error {
	if err := validateTaskWakePair(record, wake); err != nil {
		return err
	}
	wakeJSON, err := json.Marshal(wake)
	if err != nil {
		return ErrInvalidTaskSpec
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_wakeups (
		wake_id, game_id, world_id, entity_id, task_id, clock_id, expected_revision,
		due_tick, reason, status, claim_id, claimed_by, generation, attempt,
		retry_after_unix_ms, wake_json
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		wake.ID, wake.Owner.GameID, wake.Owner.WorldID, wake.Owner.EntityID, wake.TaskID,
		record.Spec.ClockID, int64(wake.ExpectedRevision), wake.DueTick, wake.Reason, wake.Status,
		wake.ClaimID, wake.ClaimedBy, int64(wake.Generation), wake.Attempt, wake.RetryAfterUnixMS, wakeJSON)
	if err != nil {
		return err
	}
	return s.afterIntentStage(ctx, intentStageWakeInserted)
}

func (s *SQLiteStore) storeIntentHistoryTx(ctx context.Context, tx *sql.Tx, before Record, prepared preparedIntentMutation) error {
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET intent_history_json = ?, intent_history_hash = ?
		WHERE game_id = ? AND world_id = ? AND entity_id = ? AND task_id = ? AND revision = ?`,
		prepared.historyJSON, prepared.historyHash, before.Owner.GameID, before.Owner.WorldID,
		before.Owner.EntityID, before.ID, int64(prepared.record.Revision))
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		if err != nil {
			return err
		}
		return ErrTaskChanged
	}
	return s.afterIntentStage(ctx, intentStageHistoryStored)
}

func (s *SQLiteStore) afterIntentStage(ctx context.Context, stage string) error {
	if s.testAfterIntentStage != nil {
		return s.testAfterIntentStage(ctx, stage)
	}
	return ctx.Err()
}
