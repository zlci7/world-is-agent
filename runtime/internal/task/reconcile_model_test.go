package task

import (
	"errors"
	"testing"
)

func TestResultValidationRequiresTerminalStateReasonAndUniqueEvidenceRefs(t *testing.T) {
	valid := testResult()
	valid.State = StateSucceeded
	valid.Reason = EvidenceKindSatisfied

	for _, state := range []State{StateSucceeded, StateFailed, StateCancelled} {
		candidate := valid
		candidate.State = state
		if err := candidate.Validate(); err != nil {
			t.Fatalf("Result.Validate() terminal state %q error = %v", state, err)
		}
	}

	tests := []struct {
		name   string
		mutate func(*Result)
	}{
		{name: "nonterminal state", mutate: func(result *Result) { result.State = StateRunning }},
		{name: "blank reason", mutate: func(result *Result) { result.Reason = " \t" }},
		{name: "duplicate evidence ref", mutate: func(result *Result) { result.EvidenceRefs = []string{"fact-a", "fact-a"} }},
		{name: "nil evidence refs", mutate: func(result *Result) { result.EvidenceRefs = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := valid
			tt.mutate(&candidate)
			if err := candidate.Validate(); !errors.Is(err, ErrInvalidTaskSpec) {
				t.Fatalf("Result.Validate() error = %v, want ErrInvalidTaskSpec", err)
			}
		})
	}
}

func TestCleanupValidationUsesFixedStatusesAndReasonShape(t *testing.T) {
	valid := []Cleanup{
		{OperationID: "operation-a", Status: "released"},
		{OperationID: "operation-a", Status: "handed_off", Reason: "dialogue owns control"},
		{OperationID: "operation-a", Status: "unconfirmed", Reason: "connection closed"},
	}
	for _, cleanup := range valid {
		if err := cleanup.Validate(); err != nil {
			t.Fatalf("Cleanup.Validate(%+v) error = %v", cleanup, err)
		}
	}

	invalid := []Cleanup{
		{OperationID: "operation-a", Status: ""},
		{OperationID: "operation-a", Status: "pending"},
		{OperationID: "operation-a", Status: "unconfirmed"},
		{OperationID: "operation-a", Status: "released", Reason: " \t"},
		{OperationID: "operation-a", Status: "handed_off", Reason: "\n"},
	}
	for _, cleanup := range invalid {
		if err := cleanup.Validate(); !errors.Is(err, ErrInvalidTaskSpec) {
			t.Fatalf("Cleanup.Validate(%+v) error = %v, want ErrInvalidTaskSpec", cleanup, err)
		}
	}
}

func TestRecordValidationResultEvidenceCleanupGraph(t *testing.T) {
	valid := validSucceededRecordForReconcileModelTest()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid evidence-derived terminal Record.Validate() error = %v", err)
	}

	cancelled := testRecord()
	cancelled.State = StateCancelled
	cancelled.Revision = 2
	cancelled.NextWakeAt = nil
	cancelled.NeedsReconcile = false
	cancelled.PauseReason = ""
	cancelled.NoProgressAttempts = 0
	cancelled.ReconcileAttempts = 0
	cancelled.Result = &Result{
		ID: "result-cancelled", TaskID: cancelled.ID, Revision: cancelled.Revision,
		State: StateCancelled, Reason: "cancelled by caller", OccurredAt: 20,
		EvidenceRefs: []string{}, Source: testSource(),
	}
	if err := cancelled.Validate(); err != nil {
		t.Fatalf("valid cancelled Record.Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Record)
	}{
		{name: "terminal without result", mutate: func(record *Record) { record.Result = nil }},
		{name: "nonterminal with result", mutate: func(record *Record) { record.State = StateRunning; record.Result.State = StateRunning }},
		{name: "result task mismatch", mutate: func(record *Record) { record.Result.TaskID = "task-other" }},
		{name: "result revision mismatch", mutate: func(record *Record) { record.Result.Revision-- }},
		{name: "result state mismatch", mutate: func(record *Record) { record.Result.State = StateFailed }},
		{name: "terminal has next wake", mutate: func(record *Record) { record.NextWakeAt = testInt64(29) }},
		{name: "terminal reconcile marker", mutate: func(record *Record) { record.NeedsReconcile = true }},
		{name: "terminal unapplied evidence", mutate: func(record *Record) { record.Evidence[0].Applied = false; record.NeedsReconcile = true }},
		{name: "missing evidence ref", mutate: func(record *Record) { record.Result.EvidenceRefs = []string{"fact-missing"} }},
		{name: "unapplied evidence ref", mutate: func(record *Record) { record.Evidence[0].Applied = false; record.NeedsReconcile = true }},
		{name: "success references unsatisfied", mutate: func(record *Record) {
			record.Evidence[0].Kind = EvidenceKindUnsatisfied
			record.Result.Reason = EvidenceKindUnsatisfied
		}},
		{name: "failure references satisfied", mutate: func(record *Record) { record.State = StateFailed; record.Result.State = StateFailed }},
		{name: "result occurrence mismatch", mutate: func(record *Record) { record.Result.OccurredAt++ }},
		{name: "result source mismatch", mutate: func(record *Record) { record.Result.Source.CallID = "call-other" }},
		{name: "orphan cleanup", mutate: func(record *Record) { record.Cleanup[0].OperationID = "operation-missing" }},
		{name: "duplicate cleanup", mutate: func(record *Record) { record.Cleanup = append(record.Cleanup, record.Cleanup[0]) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := validSucceededRecordForReconcileModelTest()
			tt.mutate(&candidate)
			if err := candidate.Validate(); !errors.Is(err, ErrInvalidTaskSpec) {
				t.Fatalf("Record.Validate() error = %v, want ErrInvalidTaskSpec", err)
			}
		})
	}
}

func validSucceededRecordForReconcileModelTest() Record {
	record := testRecord()
	operation := testOperation()
	evidence := testEvidence()
	evidence.Kind = EvidenceKindSatisfied
	evidence.WaitUntil = nil
	evidence.OccurredAt = 25
	evidence.Source = SourceRef{Kind: SourceKindEnvironment, EventID: "terminal-event", TurnID: "terminal-turn", CallID: "terminal-call"}
	evidence.Applied = true
	evidence.RevalidatedIn = nil
	record.State = StateSucceeded
	record.Revision = 2
	record.NextWakeAt = nil
	record.NeedsReconcile = false
	record.PauseReason = ""
	record.NoProgressAttempts = 0
	record.ReconcileAttempts = 0
	record.Operations = []Operation{operation}
	record.Evidence = []Evidence{evidence}
	record.Result = &Result{
		ID: "result-terminal", TaskID: record.ID, Revision: record.Revision,
		State: StateSucceeded, Reason: EvidenceKindSatisfied, OccurredAt: evidence.OccurredAt,
		EvidenceRefs: []string{evidence.FactID}, Source: evidence.Source,
	}
	record.Cleanup = []Cleanup{{OperationID: operation.ID, Status: "released"}}
	return record
}
