package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"

	"gameagent/runtime/internal/session"
)

func TestStateValidationAcceptsEverySupportedState(t *testing.T) {
	for _, state := range []State{
		"waiting",
		"running",
		"paused",
		"succeeded",
		"failed",
		"cancelled",
	} {
		if err := state.Validate(); err != nil {
			t.Fatalf("State(%q).Validate() returned error: %v", state, err)
		}
		if !state.Valid() {
			t.Fatalf("State(%q).Valid() = false", state)
		}
	}
}

func TestStateValidationRejectsUnknownState(t *testing.T) {
	for _, state := range []State{"", "queued", "WAITING"} {
		if err := state.Validate(); err == nil {
			t.Fatalf("State(%q).Validate() returned nil", state)
		}
		if state.Valid() {
			t.Fatalf("State(%q).Valid() = true", state)
		}
	}
}

func TestBindingValidationRejectsMissingIdentityAndInvalidGeneration(t *testing.T) {
	valid := testBinding()
	tests := []struct {
		name   string
		mutate func(*Binding)
	}{
		{name: "empty game id", mutate: func(value *Binding) { value.World.GameID = "" }},
		{name: "blank game id", mutate: func(value *Binding) { value.World.GameID = " \t\n" }},
		{name: "empty world id", mutate: func(value *Binding) { value.World.WorldID = "" }},
		{name: "blank world id", mutate: func(value *Binding) { value.World.WorldID = " \t\n" }},
		{name: "empty run id", mutate: func(value *Binding) { value.RunID = "" }},
		{name: "blank run id", mutate: func(value *Binding) { value.RunID = " \t\n" }},
		{name: "zero generation", mutate: func(value *Binding) { value.Generation = 0 }},
		{name: "generation above max int64", mutate: func(value *Binding) { value.Generation = uint64(math.MaxInt64) + 1 }},
	}

	if err := valid.Validate(); err != nil {
		t.Fatalf("valid Binding.Validate() returned error: %v", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := valid
			tt.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("Binding.Validate() returned nil")
			}
		})
	}
}

func TestClockValidationRejectsMissingIDAndNegativeTick(t *testing.T) {
	valid := testClock()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid Clock.Validate() returned error: %v", err)
	}

	for _, clock := range []Clock{
		{ID: "", Tick: 1},
		{ID: " \t\n", Tick: 1},
		{ID: "fake.minute.v1", Tick: -1},
	} {
		if err := clock.Validate(); err == nil {
			t.Fatalf("Clock(%+v).Validate() returned nil", clock)
		}
	}
}

func TestTaskSourceValidationUsesOnlyGenericKinds(t *testing.T) {
	for _, kind := range []string{"internal", "interaction", "task_wake", "environment", "lifecycle"} {
		source := testSource()
		source.Kind = kind
		if err := source.Validate(); err != nil {
			t.Fatalf("SourceRef kind %q returned error: %v", kind, err)
		}
	}

	for _, source := range []SourceRef{
		{Kind: ""},
		{Kind: " \t\n"},
		{Kind: "player_said_to_npc"},
		{Kind: "internal", EventID: " \t\n"},
		{Kind: "internal", TurnID: " \t\n"},
		{Kind: "internal", CallID: " \t\n"},
	} {
		if err := source.Validate(); err == nil {
			t.Fatalf("SourceRef(%+v).Validate() returned nil", source)
		}
	}
}

func TestTaskSpecValidationRejectsMissingIdentityAndInvalidWindow(t *testing.T) {
	valid := testTaskSpec()
	tests := []struct {
		name   string
		mutate func(*TaskSpec)
	}{
		{name: "empty instruction", mutate: func(value *TaskSpec) { value.Instruction = "" }},
		{name: "blank instruction", mutate: func(value *TaskSpec) { value.Instruction = " \t\n" }},
		{name: "empty clock id", mutate: func(value *TaskSpec) { value.ClockID = "" }},
		{name: "blank clock id", mutate: func(value *TaskSpec) { value.ClockID = " \t\n" }},
		{name: "negative wake tick", mutate: func(value *TaskSpec) { value.WakeAt = -1 }},
		{name: "negative deadline tick", mutate: func(value *TaskSpec) { value.DeadlineAt = -1 }},
		{name: "wake after deadline", mutate: func(value *TaskSpec) { value.WakeAt = 31 }},
		{name: "unknown result contract", mutate: func(value *TaskSpec) { value.ResultContract = "model_claim" }},
		{name: "invalid contract json", mutate: func(value *TaskSpec) { value.Contract = json.RawMessage(`{"amount":`) }},
		{name: "invalid source", mutate: func(value *TaskSpec) { value.Source.Kind = "stardew_event" }},
	}

	if err := valid.Validate(); err != nil {
		t.Fatalf("valid TaskSpec.Validate() returned error: %v", err)
	}
	emptyContract := valid
	emptyContract.Contract = nil
	if err := emptyContract.Validate(); err != nil {
		t.Fatalf("TaskSpec with empty Contract returned error: %v", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := valid
			tt.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("TaskSpec.Validate() returned nil")
			}
		})
	}
}

func TestTaskExecutionContextValidatesRequiredObjectsAndOptionalRevision(t *testing.T) {
	valid := ExecutionContext{
		Owner:   testOwner(),
		Binding: testBinding(),
		Clock:   testClock(),
		Source:  testSource(),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("create ExecutionContext with no expected revision returned error: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ExecutionContext)
	}{
		{name: "blank owner game", mutate: func(value *ExecutionContext) { value.Owner.GameID = " \t" }},
		{name: "blank owner world", mutate: func(value *ExecutionContext) { value.Owner.WorldID = " \t" }},
		{name: "blank owner entity", mutate: func(value *ExecutionContext) { value.Owner.EntityID = " \t" }},
		{name: "owner game differs from binding", mutate: func(value *ExecutionContext) { value.Owner.GameID = "other-game" }},
		{name: "owner world differs from binding", mutate: func(value *ExecutionContext) { value.Owner.WorldID = "other-world" }},
		{name: "invalid binding", mutate: func(value *ExecutionContext) { value.Binding.Generation = 0 }},
		{name: "invalid clock", mutate: func(value *ExecutionContext) { value.Clock.Tick = -1 }},
		{name: "invalid source", mutate: func(value *ExecutionContext) { value.Source.Kind = "game_specific" }},
		{name: "blank optional task id", mutate: func(value *ExecutionContext) { value.TaskID = " \t" }},
		{name: "blank optional wake id", mutate: func(value *ExecutionContext) { value.WakeID = " \t" }},
		{name: "expected revision above max int64", mutate: func(value *ExecutionContext) { value.ExpectedRevision = uint64(math.MaxInt64) + 1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := valid
			tt.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("ExecutionContext.Validate() returned nil")
			}
		})
	}
}

func TestTaskDurableCounterValidationAndCheckedIncrement(t *testing.T) {
	for _, value := range []uint64{1, uint64(math.MaxInt64)} {
		if err := ValidateDurableCounter(value); err != nil {
			t.Fatalf("ValidateDurableCounter(%d) returned error: %v", value, err)
		}
	}
	for _, value := range []uint64{0, uint64(math.MaxInt64) + 1, math.MaxUint64} {
		if err := ValidateDurableCounter(value); err == nil {
			t.Fatalf("ValidateDurableCounter(%d) returned nil", value)
		}
	}

	for _, tt := range []struct {
		current uint64
		want    uint64
	}{
		{current: 0, want: 1},
		{current: 1, want: 2},
		{current: uint64(math.MaxInt64) - 1, want: uint64(math.MaxInt64)},
	} {
		got, err := NextDurableCounter(tt.current)
		if err != nil || got != tt.want {
			t.Fatalf("NextDurableCounter(%d) = (%d, %v), want (%d, nil)", tt.current, got, err, tt.want)
		}
	}
	for _, value := range []uint64{uint64(math.MaxInt64), uint64(math.MaxInt64) + 1, math.MaxUint64} {
		if got, err := NextDurableCounter(value); err == nil || got != 0 {
			t.Fatalf("NextDurableCounter(%d) = (%d, %v), want (0, error)", value, got, err)
		}
	}
}

func TestRecordValidationRejectsInvalidIdentityRevisionAndTimestamps(t *testing.T) {
	valid := testRecord()
	tests := []struct {
		name   string
		mutate func(*Record)
	}{
		{name: "blank task id", mutate: func(value *Record) { value.ID = " \t\n" }},
		{name: "blank owner", mutate: func(value *Record) { value.Owner.EntityID = " \t\n" }},
		{name: "invalid spec", mutate: func(value *Record) { value.Spec.WakeAt = -1 }},
		{name: "unknown state", mutate: func(value *Record) { value.State = "queued" }},
		{name: "zero revision", mutate: func(value *Record) { value.Revision = 0 }},
		{name: "revision above max int64", mutate: func(value *Record) { value.Revision = uint64(math.MaxInt64) + 1 }},
		{name: "negative created game tick", mutate: func(value *Record) { value.CreatedAtGameTick = -1 }},
		{name: "negative created unix ms", mutate: func(value *Record) { value.CreatedAtUnixMS = -1 }},
		{name: "negative next wake tick", mutate: func(value *Record) { value.NextWakeAt = testInt64(-1) }},
		{name: "negative no progress attempts", mutate: func(value *Record) { value.NoProgressAttempts = -1 }},
		{name: "negative reconcile attempts", mutate: func(value *Record) { value.ReconcileAttempts = -1 }},
		{name: "invalid progress json", mutate: func(value *Record) { value.Progress = json.RawMessage(`{"step":`) }},
	}

	if err := valid.Validate(); err != nil {
		t.Fatalf("valid Record.Validate() returned error: %v", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := valid
			tt.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("Record.Validate() returned nil")
			}
		})
	}
}

func TestRecordValidationRejectsNestedWorldMismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Record)
	}{
		{name: "operation game", mutate: func(value *Record) { value.Operations[0].Binding.World.GameID = "other-game" }},
		{name: "operation world", mutate: func(value *Record) { value.Operations[0].Binding.World.WorldID = "other-world" }},
		{name: "evidence game", mutate: func(value *Record) { value.Evidence[0].Binding.World.GameID = "other-game" }},
		{name: "evidence world", mutate: func(value *Record) { value.Evidence[0].Binding.World.WorldID = "other-world" }},
		{name: "revalidated game", mutate: func(value *Record) { value.Evidence[0].RevalidatedIn.World.GameID = "other-game" }},
		{name: "revalidated world", mutate: func(value *Record) { value.Evidence[0].RevalidatedIn.World.WorldID = "other-world" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := testRecord()
			record.Operations = []Operation{testOperation()}
			record.Evidence = []Evidence{testEvidence()}
			tt.mutate(&record)

			err := record.Validate()
			if !errors.Is(err, ErrWorldMismatch) {
				t.Fatalf("Record.Validate() error = %v, want ErrWorldMismatch", err)
			}
		})
	}
}

func TestRecordValidationAllowsNestedHistoricalBindingForSameWorld(t *testing.T) {
	record := testRecord()
	operation := testOperation()
	operation.Binding.RunID = "historical-run"
	operation.Binding.Generation = 2
	evidence := testEvidence()
	evidence.Binding.RunID = "earlier-run"
	evidence.Binding.Generation = 3
	evidence.RevalidatedIn.RunID = "current-run"
	evidence.RevalidatedIn.Generation = 4
	record.Operations = []Operation{operation}
	record.Evidence = []Evidence{evidence}

	if err := record.Validate(); err != nil {
		t.Fatalf("Record.Validate() returned error for same-world historical bindings: %v", err)
	}
}

func TestRecordNestedValuesValidateDurableCountersAndTimestamps(t *testing.T) {
	validOperation := testOperation()
	validEvidence := testEvidence()
	validResult := testResult()
	validWake := testWake()

	tests := []struct {
		name     string
		validate func() error
	}{
		{name: "operation zero start revision", validate: func() error { value := validOperation; value.StartRevision = 0; return value.Validate() }},
		{name: "operation start revision overflow", validate: func() error {
			value := validOperation
			value.StartRevision = uint64(math.MaxInt64) + 1
			return value.Validate()
		}},
		{name: "evidence zero start revision", validate: func() error { value := validEvidence; value.StartRevision = 0; return value.Validate() }},
		{name: "evidence start revision overflow", validate: func() error {
			value := validEvidence
			value.StartRevision = uint64(math.MaxInt64) + 1
			return value.Validate()
		}},
		{name: "evidence negative occurred tick", validate: func() error { value := validEvidence; value.OccurredAt = -1; return value.Validate() }},
		{name: "evidence negative wait tick", validate: func() error { value := validEvidence; value.WaitUntil = testInt64(-1); return value.Validate() }},
		{name: "result zero revision", validate: func() error { value := validResult; value.Revision = 0; return value.Validate() }},
		{name: "result revision overflow", validate: func() error {
			value := validResult
			value.Revision = uint64(math.MaxInt64) + 1
			return value.Validate()
		}},
		{name: "result negative occurred tick", validate: func() error { value := validResult; value.OccurredAt = -1; return value.Validate() }},
		{name: "wake zero expected revision", validate: func() error { value := validWake; value.ExpectedRevision = 0; return value.Validate() }},
		{name: "wake expected revision overflow", validate: func() error {
			value := validWake
			value.ExpectedRevision = uint64(math.MaxInt64) + 1
			return value.Validate()
		}},
		{name: "wake zero generation", validate: func() error { value := validWake; value.Generation = 0; return value.Validate() }},
		{name: "wake generation overflow", validate: func() error {
			value := validWake
			value.Generation = uint64(math.MaxInt64) + 1
			return value.Validate()
		}},
		{name: "wake negative due tick", validate: func() error { value := validWake; value.DueTick = -1; return value.Validate() }},
		{name: "wake negative attempt", validate: func() error { value := validWake; value.Attempt = -1; return value.Validate() }},
		{name: "wake negative retry unix ms", validate: func() error { value := validWake; value.RetryAfterUnixMS = -1; return value.Validate() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.validate(); err == nil {
				t.Fatal("Validate() returned nil")
			}
		})
	}

	for name, validate := range map[string]func() error{
		"operation": validOperation.Validate,
		"evidence":  validEvidence.Validate,
		"result":    validResult.Validate,
		"wake":      validWake.Validate,
	} {
		if err := validate(); err != nil {
			t.Fatalf("valid %s returned error: %v", name, err)
		}
	}
}

func TestRecordNestedValuesRejectMissingIdentities(t *testing.T) {
	validOperation := testOperation()
	validEvidence := testEvidence()
	validResult := testResult()
	validWake := testWake()
	tests := []struct {
		name     string
		validate func() error
	}{
		{name: "operation id", validate: func() error { value := validOperation; value.ID = " \t"; return value.Validate() }},
		{name: "operation action id", validate: func() error { value := validOperation; value.ActionID = " \t"; return value.Validate() }},
		{name: "operation command fingerprint", validate: func() error { value := validOperation; value.CommandFingerprint = " \t"; return value.Validate() }},
		{name: "evidence fact id", validate: func() error { value := validEvidence; value.FactID = " \t"; return value.Validate() }},
		{name: "evidence task id", validate: func() error { value := validEvidence; value.TaskID = " \t"; return value.Validate() }},
		{name: "evidence optional operation id", validate: func() error { value := validEvidence; value.OperationID = " \t"; return value.Validate() }},
		{name: "result id", validate: func() error { value := validResult; value.ID = " \t"; return value.Validate() }},
		{name: "result task id", validate: func() error { value := validResult; value.TaskID = " \t"; return value.Validate() }},
		{name: "result evidence ref", validate: func() error { value := validResult; value.EvidenceRefs = []string{" \t"}; return value.Validate() }},
		{name: "wake id", validate: func() error { value := validWake; value.ID = " \t"; return value.Validate() }},
		{name: "wake task id", validate: func() error { value := validWake; value.TaskID = " \t"; return value.Validate() }},
		{name: "wake owner", validate: func() error { value := validWake; value.Owner.EntityID = " \t"; return value.Validate() }},
		{name: "wake optional claim id", validate: func() error { value := validWake; value.ClaimID = " \t"; return value.Validate() }},
		{name: "wake optional claimed by", validate: func() error { value := validWake; value.ClaimedBy = " \t"; return value.Validate() }},
		{name: "cleanup operation id", validate: func() error { return (Cleanup{OperationID: " \t", Status: "handed_off"}).Validate() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.validate(); err == nil {
				t.Fatal("Validate() returned nil")
			}
		})
	}
}

func TestTaskGenericOutcomeValidationUsesFixedValues(t *testing.T) {
	for _, next := range []string{"decide", "observe", "settled"} {
		value := ReconcileResult{Task: testRecord(), Next: next}
		if err := value.Validate(); err != nil {
			t.Fatalf("ReconcileResult.Next %q returned error: %v", next, err)
		}
	}
	if err := (ReconcileResult{Task: testRecord(), Next: "retry_model"}).Validate(); err == nil {
		t.Fatal("unknown ReconcileResult.Next returned nil")
	}

	for _, kind := range []string{"progress", "no_progress", "reconcile_failed"} {
		if err := (AttemptOutcome{Kind: kind}).Validate(); err != nil {
			t.Fatalf("AttemptOutcome.Kind %q returned error: %v", kind, err)
		}
	}
	if err := (AttemptOutcome{Kind: "player_failed"}).Validate(); err == nil {
		t.Fatal("unknown AttemptOutcome.Kind returned nil")
	}
}

func TestTaskIntentAndAdmissionRejectNegativeValues(t *testing.T) {
	if err := (Intent{Kind: "wait", NextWakeAt: testInt64(-1)}).Validate(); err == nil {
		t.Fatal("negative Intent.NextWakeAt returned nil")
	}
	if err := (Admission{MaxActivePerOwner: -1}).Validate(); err == nil {
		t.Fatal("negative Admission.MaxActivePerOwner returned nil")
	}
	if err := (Admission{}).Validate(); err != nil {
		t.Fatalf("zero Admission returned error: %v", err)
	}
}

func TestRawJSONValidationRejectsEveryMalformedOpaqueField(t *testing.T) {
	invalid := json.RawMessage(`{"unfinished":`)
	tests := []struct {
		name     string
		validate func() error
	}{
		{name: "source game time", validate: func() error { value := testSource(); value.GameTime = invalid; return value.Validate() }},
		{name: "source facts", validate: func() error { value := testSource(); value.Facts = invalid; return value.Validate() }},
		{name: "task contract", validate: func() error { value := testTaskSpec(); value.Contract = invalid; return value.Validate() }},
		{name: "record progress", validate: func() error { value := testRecord(); value.Progress = invalid; return value.Validate() }},
		{name: "operation receipt", validate: func() error { value := testOperation(); value.Receipt = invalid; return value.Validate() }},
		{name: "evidence details", validate: func() error { value := testEvidence(); value.Details = invalid; return value.Validate() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.validate(); err == nil {
				t.Fatal("Validate() returned nil")
			}
		})
	}
}

func TestRawJSONRoundTripPreservesIntegerAndDecimalLexemes(t *testing.T) {
	want := json.RawMessage(`{"integer":9007199254740993,"decimal":1.2500}`)
	record := testRecord()
	record.Spec.Contract = append(json.RawMessage(nil), want...)
	record.Spec.Source.GameTime = append(json.RawMessage(nil), want...)
	record.Spec.Source.Facts = append(json.RawMessage(nil), want...)
	record.Progress = append(json.RawMessage(nil), want...)
	record.Operations = []Operation{testOperation()}
	record.Operations[0].Receipt = append(json.RawMessage(nil), want...)
	record.Evidence = []Evidence{testEvidence()}
	record.Evidence[0].Details = append(json.RawMessage(nil), want...)

	if err := record.Validate(); err != nil {
		t.Fatalf("Record.Validate() returned error: %v", err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("json.Marshal() returned error: %v", err)
	}
	for _, lexeme := range [][]byte{[]byte("9007199254740993"), []byte("1.2500")} {
		if !bytes.Contains(data, lexeme) {
			t.Fatalf("marshaled JSON lost numeric lexeme %q: %s", lexeme, data)
		}
	}

	var got Record
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal() returned error: %v", err)
	}
	for name, raw := range map[string]json.RawMessage{
		"contract":  got.Spec.Contract,
		"game_time": got.Spec.Source.GameTime,
		"facts":     got.Spec.Source.Facts,
		"progress":  got.Progress,
		"receipt":   got.Operations[0].Receipt,
		"details":   got.Evidence[0].Details,
	} {
		if !bytes.Equal(raw, want) {
			t.Fatalf("%s round trip = %s, want exact %s", name, raw, want)
		}
	}
}

func TestRecordJSONUsesSnakeCaseForRepresentativeNestedTask(t *testing.T) {
	record := testRecord()
	record.Operations = []Operation{testOperation()}
	record.Evidence = []Evidence{testEvidence()}
	record.Result = testResultPointer()
	record.Cleanup = []Cleanup{{OperationID: "operation-a", Status: "handed_off", Reason: "complete"}}

	root := marshalJSONObject(t, record)
	requireJSONKeys(t, root,
		"id", "owner", "spec", "state", "revision", "created_at_game_tick", "created_at_unix_ms",
		"next_wake_at", "progress", "needs_reconcile", "pause_reason", "no_progress_attempts",
		"reconcile_attempts", "operations", "evidence", "result", "cleanup",
	)
	requireNoPascalCaseKeys(t, root)

	spec := decodeJSONObject(t, root["spec"])
	requireJSONKeys(t, spec, "instruction", "clock_id", "wake_at", "deadline_at", "result_contract", "contract", "equivalence_key", "source")
	requireNoPascalCaseKeys(t, spec)
	source := decodeJSONObject(t, spec["source"])
	requireJSONKeys(t, source, "kind", "event_id", "turn_id", "call_id", "game_time", "facts")
	requireNoPascalCaseKeys(t, source)

	operation := decodeFirstJSONObject(t, root["operations"])
	requireJSONKeys(t, operation, "id", "action_id", "command_fingerprint", "start_revision", "binding", "status", "receipt")
	requireNoPascalCaseKeys(t, operation)
	binding := decodeJSONObject(t, operation["binding"])
	requireJSONKeys(t, binding, "world", "run_id", "generation")
	requireNoPascalCaseKeys(t, binding)
	world := decodeJSONObject(t, binding["world"])
	requireJSONKeys(t, world, "game_id", "world_id")
	requireNoPascalCaseKeys(t, world)

	evidence := decodeFirstJSONObject(t, root["evidence"])
	requireJSONKeys(t, evidence, "fact_id", "task_id", "operation_id", "binding", "start_revision", "occurred_at", "kind", "wait_until", "details", "source", "applied", "revalidated_in")
	requireNoPascalCaseKeys(t, evidence)
	result := decodeJSONObject(t, root["result"])
	requireJSONKeys(t, result, "id", "task_id", "revision", "state", "reason", "occurred_at", "evidence_refs", "source")
	requireNoPascalCaseKeys(t, result)
	cleanup := decodeFirstJSONObject(t, root["cleanup"])
	requireJSONKeys(t, cleanup, "operation_id", "status", "reason")
	requireNoPascalCaseKeys(t, cleanup)
}

func TestTaskJSONUsesSnakeCaseForStandaloneContracts(t *testing.T) {
	values := []struct {
		name string
		got  any
		keys []string
	}{
		{name: "execution context", got: ExecutionContext{Owner: testOwner(), Binding: testBinding(), Clock: testClock(), Source: testSource(), TaskID: "task-a", WakeID: "wake-a", ExpectedRevision: 1}, keys: []string{"owner", "binding", "clock", "source", "task_id", "wake_id", "expected_revision"}},
		{name: "wake", got: testWake(), keys: []string{"id", "task_id", "owner", "expected_revision", "due_tick", "reason", "status", "claim_id", "claimed_by", "generation", "attempt", "retry_after_unix_ms"}},
		{name: "intent", got: Intent{Kind: "wait", NextWakeAt: testInt64(20), ProgressNote: "moving", Reason: "later"}, keys: []string{"kind", "next_wake_at", "progress_note", "reason"}},
		{name: "admission", got: Admission{MaxActivePerOwner: 1}, keys: []string{"max_active_per_owner"}},
		{name: "create result", got: CreateResult{Task: testRecord(), Created: true}, keys: []string{"task", "created"}},
		{name: "reconcile result", got: ReconcileResult{Task: testRecord(), Next: "observe"}, keys: []string{"task", "next"}},
		{name: "checkpoint ref", got: CheckpointRef{Status: "confirmed", ID: "checkpoint-a", Checksum: "sum", SchemaVersion: 1, World: testWorld(), Reason: "save"}, keys: []string{"status", "id", "checksum", "schema_version", "world", "reason"}},
		{name: "head", got: Head{Binding: testBinding(), Clock: testClock(), CheckpointID: "checkpoint-a", Status: "ready", Reason: "active"}, keys: []string{"binding", "clock", "checkpoint_id", "status", "reason"}},
		{name: "prepared", got: Prepared{Head: Head{Binding: testBinding(), Clock: testClock(), CheckpointID: "checkpoint-a", Status: "ready", Reason: "active"}, Reference: CheckpointRef{Status: "confirmed", ID: "checkpoint-a", Checksum: "sum", SchemaVersion: 1, World: testWorld(), Reason: "save"}, SaveRequestID: "save-a"}, keys: []string{"head", "reference", "save_request_id"}},
		{name: "attempt outcome", got: AttemptOutcome{Kind: "progress", Reason: "advanced"}, keys: []string{"kind", "reason"}},
	}

	for _, tt := range values {
		t.Run(tt.name, func(t *testing.T) {
			object := marshalJSONObject(t, tt.got)
			requireJSONKeys(t, object, tt.keys...)
			requireNoPascalCaseKeys(t, object)
		})
	}
}

func TestTaskOwnerJSONUsesExactSnakeCaseInEveryContainer(t *testing.T) {
	values := []struct {
		name  string
		value any
	}{
		{name: "execution context", value: ExecutionContext{Owner: testOwner(), Binding: testBinding(), Clock: testClock(), Source: testSource(), TaskID: "task-a", WakeID: "wake-a", ExpectedRevision: 1}},
		{name: "record", value: testRecord()},
		{name: "wake", value: testWake()},
	}

	for _, tt := range values {
		t.Run(tt.name, func(t *testing.T) {
			root := marshalJSONObject(t, tt.value)
			owner := decodeJSONObject(t, root["owner"])
			if len(owner) != 3 {
				t.Fatalf("owner key count = %d, want 3: %+v", len(owner), owner)
			}
			requireJSONKeys(t, owner, "game_id", "world_id", "entity_id")
			requireNoPascalCaseKeys(t, owner)
			if got := decodeJSONString(t, owner["game_id"]); got != "fake-game" {
				t.Fatalf("game_id = %q, want fake-game", got)
			}
			if got := decodeJSONString(t, owner["world_id"]); got != "world-a" {
				t.Fatalf("world_id = %q, want world-a", got)
			}
			if got := decodeJSONString(t, owner["entity_id"]); got != "actor-a" {
				t.Fatalf("entity_id = %q, want actor-a", got)
			}
		})
	}
}

func TestTaskOwnerJSONDecodesSnakeCaseAndRoundTripsEveryContainer(t *testing.T) {
	exec := ExecutionContext{Owner: testOwner(), Binding: testBinding(), Clock: testClock(), Source: testSource(), TaskID: "task-a", WakeID: "wake-a", ExpectedRevision: 1}
	requireJSONRoundTrip(t, exec)
	requireJSONRoundTrip(t, testRecord())
	requireJSONRoundTrip(t, testWake())

	owner := json.RawMessage(`{"game_id":"fake-game","world_id":"world-a","entity_id":"actor-a"}`)
	for _, tt := range []struct {
		name   string
		decode func([]byte) (session.AgentSessionKey, error)
	}{
		{name: "execution context", decode: func(data []byte) (session.AgentSessionKey, error) {
			var value ExecutionContext
			err := json.Unmarshal(data, &value)
			return value.Owner, err
		}},
		{name: "record", decode: func(data []byte) (session.AgentSessionKey, error) {
			var value Record
			err := json.Unmarshal(data, &value)
			return value.Owner, err
		}},
		{name: "wake", decode: func(data []byte) (session.AgentSessionKey, error) {
			var value Wake
			err := json.Unmarshal(data, &value)
			return value.Owner, err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := ownerContainerJSON(t, tt.name, owner)
			got, err := tt.decode(data)
			if err != nil {
				t.Fatalf("json.Unmarshal() returned error: %v", err)
			}
			if got != testOwner() {
				t.Fatalf("decoded owner = %+v, want %+v", got, testOwner())
			}
		})
	}
}

func TestTaskOwnerJSONRejectsPascalCaseOnlyInputInEveryContainer(t *testing.T) {
	owner := json.RawMessage(`{"GameID":"fake-game","WorldID":"world-a","EntityID":"actor-a"}`)
	for _, tt := range []struct {
		name   string
		decode func([]byte) error
	}{
		{name: "execution context", decode: func(data []byte) error { return json.Unmarshal(data, &ExecutionContext{}) }},
		{name: "record", decode: func(data []byte) error { return json.Unmarshal(data, &Record{}) }},
		{name: "wake", decode: func(data []byte) error { return json.Unmarshal(data, &Wake{}) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.decode(ownerContainerJSON(t, tt.name, owner)); err == nil {
				t.Fatal("json.Unmarshal() accepted PascalCase-only owner")
			}
		})
	}
}

func testWorld() WorldKey {
	return WorldKey{GameID: "fake-game", WorldID: "world-a"}
}

func testOwner() session.AgentSessionKey {
	return session.AgentSessionKey{GameID: "fake-game", WorldID: "world-a", EntityID: "actor-a"}
}

func testBinding() Binding {
	return Binding{World: testWorld(), RunID: "run-a", Generation: 1}
}

func testClock() Clock {
	return Clock{ID: "fake.minute.v1", Tick: 10, Sequence: 1}
}

func testSource() SourceRef {
	return SourceRef{
		Kind:     "internal",
		EventID:  "event-a",
		TurnID:   "turn-a",
		CallID:   "call-a",
		GameTime: json.RawMessage(`{"tick":10}`),
		Facts:    json.RawMessage(`[{"kind":"clock"}]`),
	}
}

func testTaskSpec() TaskSpec {
	return TaskSpec{
		Instruction:    "inspect later",
		ClockID:        "fake.minute.v1",
		WakeAt:         20,
		DeadlineAt:     30,
		ResultContract: "authoritative_evidence",
		Contract:       json.RawMessage(`{"expected":"complete"}`),
		EquivalenceKey: "inspect-later",
		Source:         testSource(),
	}
}

func testRecord() Record {
	return Record{
		ID:                 "task-a",
		Owner:              testOwner(),
		Spec:               testTaskSpec(),
		State:              "waiting",
		Revision:           1,
		CreatedAtGameTick:  10,
		CreatedAtUnixMS:    1_700_000_000_000,
		NextWakeAt:         testInt64(20),
		Progress:           json.RawMessage(`{"status":"scheduled"}`),
		NeedsReconcile:     true,
		PauseReason:        "pending_review",
		NoProgressAttempts: 1,
		ReconcileAttempts:  1,
		Operations:         []Operation{},
		Evidence:           []Evidence{},
		Cleanup:            []Cleanup{},
	}
}

func testOperation() Operation {
	return Operation{
		ID:                 "operation-a",
		ActionID:           "action-a",
		CommandFingerprint: "command-sum",
		StartRevision:      1,
		Binding:            testBinding(),
		Status:             "registered",
		Receipt:            json.RawMessage(`{"accepted":true}`),
	}
}

func testEvidence() Evidence {
	revalidated := testBinding()
	return Evidence{
		FactID:        "fact-a",
		TaskID:        "task-a",
		OperationID:   "operation-a",
		Binding:       testBinding(),
		StartRevision: 1,
		OccurredAt:    20,
		Kind:          "progress",
		WaitUntil:     testInt64(25),
		Details:       json.RawMessage(`{"distance":1.2500}`),
		Source:        testSource(),
		Applied:       true,
		RevalidatedIn: &revalidated,
	}
}

func testResult() Result {
	return Result{
		ID:           "result-a",
		TaskID:       "task-a",
		Revision:     2,
		State:        "succeeded",
		Reason:       "authoritative evidence",
		OccurredAt:   25,
		EvidenceRefs: []string{"fact-a"},
		Source:       testSource(),
	}
}

func testResultPointer() *Result {
	result := testResult()
	return &result
}

func testWake() Wake {
	return Wake{
		ID:               "wake-a",
		TaskID:           "task-a",
		Owner:            testOwner(),
		ExpectedRevision: 1,
		DueTick:          20,
		Reason:           "scheduled",
		Status:           "claimed",
		ClaimID:          "claim-a",
		ClaimedBy:        "runtime-a",
		Generation:       1,
		Attempt:          1,
		RetryAfterUnixMS: 1_700_000_000_000,
	}
}

func testInt64(value int64) *int64 {
	return &value
}

func marshalJSONObject(t *testing.T, value any) map[string]json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(%T) returned error: %v", value, err)
	}
	return decodeJSONObject(t, data)
}

func ownerContainerJSON(t *testing.T, name string, owner json.RawMessage) []byte {
	t.Helper()
	var value any
	switch name {
	case "execution context":
		value = ExecutionContext{Owner: testOwner(), Binding: testBinding(), Clock: testClock(), Source: testSource(), TaskID: "task-a", WakeID: "wake-a", ExpectedRevision: 1}
	case "record":
		value = testRecord()
	case "wake":
		value = testWake()
	default:
		t.Fatalf("unknown owner container %q", name)
	}
	root := marshalJSONObject(t, value)
	root["owner"] = owner
	data, err := json.Marshal(root)
	if err != nil {
		t.Fatalf("json.Marshal(owner container) returned error: %v", err)
	}
	return data
}

func requireJSONRoundTrip[T any](t *testing.T, want T) {
	t.Helper()
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal(%T) returned error: %v", want, err)
	}
	var got T
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal(%T) returned error: %v", want, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON round trip for %T = %+v, want %+v", want, got, want)
	}
}

func decodeJSONString(t *testing.T, data []byte) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("json.Unmarshal(string) returned error: %v", err)
	}
	return value
}

func decodeJSONObject(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatalf("json.Unmarshal(object) returned error: %v", err)
	}
	return object
}

func decodeFirstJSONObject(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()
	var values []json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		t.Fatalf("json.Unmarshal(array) returned error: %v", err)
	}
	if len(values) != 1 {
		t.Fatalf("array length = %d, want 1", len(values))
	}
	return decodeJSONObject(t, values[0])
}

func requireJSONKeys(t *testing.T, object map[string]json.RawMessage, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			t.Errorf("JSON object missing key %q: %+v", key, object)
		}
	}
}

func requireNoPascalCaseKeys(t *testing.T, object map[string]json.RawMessage) {
	t.Helper()
	for key := range object {
		for _, char := range key {
			if char >= 'A' && char <= 'Z' {
				t.Errorf("JSON key %q is not snake_case", key)
				break
			}
		}
	}
}
