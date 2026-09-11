package task

import (
	"encoding/json"
	"math"
	"strings"

	"gameagent/runtime/internal/session"
)

type State string

const (
	StateWaiting   State = "waiting"
	StateRunning   State = "running"
	StatePaused    State = "paused"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

const (
	ResultContractAuthoritativeEvidence = "authoritative_evidence"

	SourceKindInternal    = "internal"
	SourceKindInteraction = "interaction"
	SourceKindTaskWake    = "task_wake"
	SourceKindEnvironment = "environment"
	SourceKindLifecycle   = "lifecycle"

	ReconcileNextDecide  = "decide"
	ReconcileNextObserve = "observe"
	ReconcileNextSettled = "settled"

	AttemptOutcomeKindProgress        = "progress"
	AttemptOutcomeKindNoProgress      = "no_progress"
	AttemptOutcomeKindReconcileFailed = "reconcile_failed"

	OperationStatusRegistered = "registered"
	OperationStatusUncertain  = "uncertain"

	EvidenceKindProgress    = "progress"
	EvidenceKindSatisfied   = "satisfied"
	EvidenceKindUnsatisfied = "unsatisfied"
	EvidenceKindInterrupted = "interrupted"
)

type WorldKey struct {
	GameID  string `json:"game_id"`
	WorldID string `json:"world_id"`
}

type Binding struct {
	World      WorldKey `json:"world"`
	RunID      string   `json:"run_id"`
	Generation uint64   `json:"generation"`
}

type Clock struct {
	ID       string `json:"id"`
	Tick     int64  `json:"tick"`
	Sequence uint64 `json:"sequence"`
}

type SourceRef struct {
	Kind     string          `json:"kind"`
	EventID  string          `json:"event_id,omitempty"`
	TurnID   string          `json:"turn_id,omitempty"`
	CallID   string          `json:"call_id,omitempty"`
	GameTime json.RawMessage `json:"game_time,omitempty"`
	Facts    json.RawMessage `json:"facts,omitempty"`
}

type ExecutionContext struct {
	Owner            session.AgentSessionKey `json:"owner"`
	Binding          Binding                 `json:"binding"`
	Clock            Clock                   `json:"clock"`
	Source           SourceRef               `json:"source"`
	TaskID           string                  `json:"task_id,omitempty"`
	WakeID           string                  `json:"wake_id,omitempty"`
	ExpectedRevision uint64                  `json:"expected_revision,omitempty"`
}

type TaskSpec struct {
	Instruction    string          `json:"instruction"`
	ClockID        string          `json:"clock_id"`
	WakeAt         int64           `json:"wake_at"`
	DeadlineAt     int64           `json:"deadline_at"`
	ResultContract string          `json:"result_contract"`
	Contract       json.RawMessage `json:"contract,omitempty"`
	EquivalenceKey string          `json:"equivalence_key,omitempty"`
	Source         SourceRef       `json:"source"`
}

type Record struct {
	ID                 string                  `json:"id"`
	Owner              session.AgentSessionKey `json:"owner"`
	Spec               TaskSpec                `json:"spec"`
	State              State                   `json:"state"`
	Revision           uint64                  `json:"revision"`
	CreatedAtGameTick  int64                   `json:"created_at_game_tick"`
	CreatedAtUnixMS    int64                   `json:"created_at_unix_ms"`
	NextWakeAt         *int64                  `json:"next_wake_at,omitempty"`
	Progress           json.RawMessage         `json:"progress,omitempty"`
	NeedsReconcile     bool                    `json:"needs_reconcile"`
	PauseReason        string                  `json:"pause_reason,omitempty"`
	NoProgressAttempts int                     `json:"no_progress_attempts"`
	ReconcileAttempts  int                     `json:"reconcile_attempts"`
	Operations         []Operation             `json:"operations"`
	Evidence           []Evidence              `json:"evidence"`
	Result             *Result                 `json:"result,omitempty"`
	Cleanup            []Cleanup               `json:"cleanup"`
}

type Operation struct {
	ID                 string          `json:"id"`
	ActionID           string          `json:"action_id"`
	CommandFingerprint string          `json:"command_fingerprint"`
	StartRevision      uint64          `json:"start_revision"`
	Binding            Binding         `json:"binding"`
	Status             string          `json:"status"`
	Receipt            json.RawMessage `json:"receipt,omitempty"`
}

type Evidence struct {
	FactID        string          `json:"fact_id"`
	TaskID        string          `json:"task_id"`
	OperationID   string          `json:"operation_id,omitempty"`
	Binding       Binding         `json:"binding"`
	StartRevision uint64          `json:"start_revision"`
	OccurredAt    int64           `json:"occurred_at"`
	Kind          string          `json:"kind"`
	WaitUntil     *int64          `json:"wait_until,omitempty"`
	Details       json.RawMessage `json:"details,omitempty"`
	Source        SourceRef       `json:"source"`
	Applied       bool            `json:"applied"`
	RevalidatedIn *Binding        `json:"revalidated_in,omitempty"`
}

type Result struct {
	ID           string    `json:"id"`
	TaskID       string    `json:"task_id"`
	Revision     uint64    `json:"revision"`
	State        State     `json:"state"`
	Reason       string    `json:"reason"`
	OccurredAt   int64     `json:"occurred_at"`
	EvidenceRefs []string  `json:"evidence_refs"`
	Source       SourceRef `json:"source"`
}

type Cleanup struct {
	OperationID string `json:"operation_id"`
	Status      string `json:"status"`
	Reason      string `json:"reason"`
}

type Wake struct {
	ID               string                  `json:"id"`
	TaskID           string                  `json:"task_id"`
	Owner            session.AgentSessionKey `json:"owner"`
	ExpectedRevision uint64                  `json:"expected_revision"`
	DueTick          int64                   `json:"due_tick"`
	Reason           string                  `json:"reason"`
	Status           string                  `json:"status"`
	ClaimID          string                  `json:"claim_id,omitempty"`
	ClaimedBy        string                  `json:"claimed_by,omitempty"`
	Generation       uint64                  `json:"generation"`
	Attempt          int                     `json:"attempt"`
	RetryAfterUnixMS int64                   `json:"retry_after_unix_ms"`
}

type Intent struct {
	Kind         string `json:"kind"`
	NextWakeAt   *int64 `json:"next_wake_at,omitempty"`
	ProgressNote string `json:"progress_note,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

type Admission struct {
	MaxActivePerOwner int `json:"max_active_per_owner"`
}

type CreateResult struct {
	Task    Record `json:"task"`
	Created bool   `json:"created"`
}

type ReconcileResult struct {
	Task Record `json:"task"`
	Next string `json:"next"`
}

type CheckpointRef struct {
	Status        string   `json:"status"`
	ID            string   `json:"id,omitempty"`
	Checksum      string   `json:"checksum,omitempty"`
	SchemaVersion int      `json:"schema_version"`
	World         WorldKey `json:"world"`
	Reason        string   `json:"reason,omitempty"`
}

type Head struct {
	Binding      Binding `json:"binding"`
	Clock        Clock   `json:"clock"`
	CheckpointID string  `json:"checkpoint_id,omitempty"`
	Status       string  `json:"status"`
	Reason       string  `json:"reason,omitempty"`
}

type Prepared struct {
	Head          Head          `json:"head"`
	Reference     CheckpointRef `json:"reference"`
	SaveRequestID string        `json:"save_request_id"`
}

type AttemptOutcome struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason,omitempty"`
}

func (s State) Valid() bool {
	switch s {
	case StateWaiting, StateRunning, StatePaused, StateSucceeded, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}

func (s State) Validate() error {
	if !s.Valid() {
		return ErrInvalidTaskSpec
	}
	return nil
}

func (w WorldKey) Validate() error {
	if !requiredIdentity(w.GameID) || !requiredIdentity(w.WorldID) {
		return ErrInvalidTaskSpec
	}
	return nil
}

func (b Binding) Validate() error {
	if err := b.World.Validate(); err != nil {
		return err
	}
	if !requiredIdentity(b.RunID) {
		return ErrInvalidTaskSpec
	}
	return ValidateDurableCounter(b.Generation)
}

func (c Clock) Validate() error {
	if !requiredIdentity(c.ID) || c.Tick < 0 {
		return ErrInvalidTaskSpec
	}
	return nil
}

func (s SourceRef) Validate() error {
	switch s.Kind {
	case SourceKindInternal, SourceKindInteraction, SourceKindTaskWake, SourceKindEnvironment, SourceKindLifecycle:
	default:
		return ErrSourceInvalid
	}
	if !optionalIdentity(s.EventID) || !optionalIdentity(s.TurnID) || !optionalIdentity(s.CallID) {
		return ErrSourceInvalid
	}
	if !validRawJSON(s.GameTime) || !validRawJSON(s.Facts) {
		return ErrSourceInvalid
	}
	return nil
}

func (e ExecutionContext) Validate() error {
	if err := validateOwner(e.Owner); err != nil {
		return err
	}
	if err := e.Binding.Validate(); err != nil {
		return err
	}
	if e.Owner.GameID != e.Binding.World.GameID || e.Owner.WorldID != e.Binding.World.WorldID {
		return ErrWorldMismatch
	}
	if err := e.Clock.Validate(); err != nil {
		return err
	}
	if err := e.Source.Validate(); err != nil {
		return err
	}
	if !optionalIdentity(e.TaskID) || !optionalIdentity(e.WakeID) {
		return ErrInvalidTaskSpec
	}
	if e.ExpectedRevision != 0 {
		return ValidateDurableCounter(e.ExpectedRevision)
	}
	return nil
}

func (s TaskSpec) Validate() error {
	if !requiredIdentity(s.Instruction) || !requiredIdentity(s.ClockID) ||
		s.WakeAt < 0 || s.DeadlineAt < 0 || s.WakeAt > s.DeadlineAt ||
		s.ResultContract != ResultContractAuthoritativeEvidence ||
		!optionalIdentity(s.EquivalenceKey) || !validRawJSON(s.Contract) {
		return ErrInvalidTaskSpec
	}
	return s.Source.Validate()
}

func (r Record) Validate() error {
	if !requiredIdentity(r.ID) {
		return ErrInvalidTaskSpec
	}
	if err := validateOwner(r.Owner); err != nil {
		return err
	}
	if err := r.Spec.Validate(); err != nil {
		return err
	}
	if err := r.State.Validate(); err != nil {
		return err
	}
	if err := ValidateDurableCounter(r.Revision); err != nil {
		return err
	}
	if r.CreatedAtGameTick < 0 || r.CreatedAtUnixMS < 0 ||
		!validOptionalTick(r.NextWakeAt) || !validRawJSON(r.Progress) ||
		r.NoProgressAttempts < 0 || r.ReconcileAttempts < 0 {
		return ErrInvalidTaskSpec
	}
	operations := make(map[string]Operation, len(r.Operations))
	for _, operation := range r.Operations {
		if err := operation.Validate(); err != nil {
			return err
		}
		if !sameOwnerWorld(r.Owner, operation.Binding.World) {
			return ErrWorldMismatch
		}
		if operation.StartRevision > r.Revision {
			return ErrInvalidTaskSpec
		}
		if _, duplicate := operations[operation.ID]; duplicate {
			return ErrInvalidTaskSpec
		}
		operations[operation.ID] = operation
	}
	facts := make(map[string]struct{}, len(r.Evidence))
	hasUnappliedEvidence := false
	for _, evidence := range r.Evidence {
		if err := evidence.Validate(); err != nil {
			return err
		}
		if !sameOwnerWorld(r.Owner, evidence.Binding.World) ||
			evidence.RevalidatedIn != nil && !sameOwnerWorld(r.Owner, evidence.RevalidatedIn.World) {
			return ErrWorldMismatch
		}
		if evidence.TaskID != r.ID || evidence.StartRevision > r.Revision {
			return ErrInvalidTaskSpec
		}
		if evidence.WaitUntil != nil &&
			(evidence.OccurredAt >= *evidence.WaitUntil || *evidence.WaitUntil > r.Spec.DeadlineAt) {
			return ErrInvalidTaskSpec
		}
		if _, duplicate := facts[evidence.FactID]; duplicate {
			return ErrInvalidTaskSpec
		}
		facts[evidence.FactID] = struct{}{}
		hasUnappliedEvidence = hasUnappliedEvidence || !evidence.Applied
		if evidence.OperationID == "" {
			if evidence.RevalidatedIn != nil {
				return ErrInvalidTaskSpec
			}
			continue
		}
		operation, found := operations[evidence.OperationID]
		if !found || evidence.StartRevision != operation.StartRevision || evidence.Binding != operation.Binding {
			return ErrInvalidTaskSpec
		}
		if evidence.RevalidatedIn != nil &&
			(evidence.Binding.RunID != evidence.RevalidatedIn.RunID || evidence.Binding.Generation >= evidence.RevalidatedIn.Generation) {
			return ErrInvalidTaskSpec
		}
	}
	if hasUnappliedEvidence && !r.NeedsReconcile {
		return ErrInvalidTaskSpec
	}
	if !recordEvidenceSourceIdentityValid(r) {
		return ErrInvalidTaskSpec
	}
	if r.Result != nil {
		if err := r.Result.Validate(); err != nil {
			return err
		}
	}
	for _, cleanup := range r.Cleanup {
		if err := cleanup.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (o Operation) Validate() error {
	if !requiredIdentity(o.ID) || !requiredIdentity(o.ActionID) || !requiredIdentity(o.CommandFingerprint) {
		return ErrInvalidTaskSpec
	}
	if err := ValidateDurableCounter(o.StartRevision); err != nil {
		return err
	}
	if err := o.Binding.Validate(); err != nil {
		return err
	}
	switch o.Status {
	case OperationStatusRegistered, OperationStatusUncertain:
	default:
		return ErrInvalidTaskSpec
	}
	if !validRawJSON(o.Receipt) {
		return ErrInvalidTaskSpec
	}
	return nil
}

func (e Evidence) Validate() error {
	if !requiredIdentity(e.FactID) || !requiredIdentity(e.TaskID) || !optionalIdentity(e.OperationID) {
		return ErrInvalidTaskSpec
	}
	if err := e.Binding.Validate(); err != nil {
		return err
	}
	if err := ValidateDurableCounter(e.StartRevision); err != nil {
		return err
	}
	if e.OccurredAt < 0 || !validOptionalTick(e.WaitUntil) || !validRawJSON(e.Details) {
		return ErrInvalidTaskSpec
	}
	switch e.Kind {
	case EvidenceKindProgress:
	case EvidenceKindSatisfied, EvidenceKindUnsatisfied, EvidenceKindInterrupted:
		if e.WaitUntil != nil {
			return ErrInvalidTaskSpec
		}
	default:
		return ErrInvalidTaskSpec
	}
	if err := e.Source.Validate(); err != nil {
		return err
	}
	if e.RevalidatedIn != nil {
		return e.RevalidatedIn.Validate()
	}
	return nil
}

func (r Result) Validate() error {
	if !requiredIdentity(r.ID) || !requiredIdentity(r.TaskID) {
		return ErrInvalidTaskSpec
	}
	if err := ValidateDurableCounter(r.Revision); err != nil {
		return err
	}
	if err := r.State.Validate(); err != nil {
		return err
	}
	if r.OccurredAt < 0 {
		return ErrInvalidTaskSpec
	}
	for _, ref := range r.EvidenceRefs {
		if !requiredIdentity(ref) {
			return ErrInvalidTaskSpec
		}
	}
	return r.Source.Validate()
}

func (c Cleanup) Validate() error {
	if !requiredIdentity(c.OperationID) {
		return ErrInvalidTaskSpec
	}
	return nil
}

func (w Wake) Validate() error {
	if !requiredIdentity(w.ID) || !requiredIdentity(w.TaskID) ||
		!optionalIdentity(w.ClaimID) || !optionalIdentity(w.ClaimedBy) {
		return ErrInvalidTaskSpec
	}
	if err := validateOwner(w.Owner); err != nil {
		return err
	}
	if err := ValidateDurableCounter(w.ExpectedRevision); err != nil {
		return err
	}
	if err := ValidateDurableCounter(w.Generation); err != nil {
		return err
	}
	if w.DueTick < 0 || w.Attempt < 0 || w.RetryAfterUnixMS < 0 {
		return ErrInvalidTaskSpec
	}
	return nil
}

func (i Intent) Validate() error {
	switch i.Kind {
	case "wait":
		if i.NextWakeAt == nil || !validOptionalTick(i.NextWakeAt) || i.Reason != "" ||
			i.ProgressNote != "" && strings.TrimSpace(i.ProgressNote) == "" {
			return ErrInvalidTaskSpec
		}
		return nil
	case "cancel":
		if i.NextWakeAt != nil || i.ProgressNote != "" || !requiredIdentity(i.Reason) {
			return ErrInvalidTaskSpec
		}
		return nil
	default:
		return ErrInvalidTaskSpec
	}
}

func (a Admission) Validate() error {
	if a.MaxActivePerOwner < 0 {
		return ErrInvalidTaskSpec
	}
	return nil
}

func (r ReconcileResult) Validate() error {
	if err := r.Task.Validate(); err != nil {
		return err
	}
	switch r.Next {
	case ReconcileNextDecide, ReconcileNextObserve, ReconcileNextSettled:
		return nil
	default:
		return ErrInvalidTaskSpec
	}
}

func (a AttemptOutcome) Validate() error {
	switch a.Kind {
	case AttemptOutcomeKindProgress, AttemptOutcomeKindNoProgress, AttemptOutcomeKindReconcileFailed:
		return nil
	default:
		return ErrInvalidTaskSpec
	}
}

func ValidateDurableCounter(value uint64) error {
	if value == 0 || value > uint64(math.MaxInt64) {
		return ErrInvalidTaskSpec
	}
	return nil
}

func NextDurableCounter(current uint64) (uint64, error) {
	if current >= uint64(math.MaxInt64) {
		return 0, ErrInvalidTaskSpec
	}
	return current + 1, nil
}

func validateOwner(owner session.AgentSessionKey) error {
	if _, err := session.Resolve(owner.GameID, owner.WorldID, owner.EntityID); err != nil {
		return ErrInvalidTaskSpec
	}
	return nil
}

func sameOwnerWorld(owner session.AgentSessionKey, world WorldKey) bool {
	return owner.GameID == world.GameID && owner.WorldID == world.WorldID
}

func requiredIdentity(value string) bool {
	return strings.TrimSpace(value) != ""
}

func optionalIdentity(value string) bool {
	return value == "" || requiredIdentity(value)
}

func validOptionalTick(value *int64) bool {
	return value == nil || *value >= 0
}

func validRawJSON(value json.RawMessage) bool {
	return len(value) == 0 || json.Valid(value)
}
