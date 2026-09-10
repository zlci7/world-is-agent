package task

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestErrorCodesSupportStableClassificationAndWrapping(t *testing.T) {
	tests := []struct {
		name string
		want string
		code Code
		err  *Error
	}{
		{name: "invalid task spec", want: "invalid_task_spec", code: CodeInvalidTaskSpec, err: ErrInvalidTaskSpec},
		{name: "task not found", want: "task_not_found", code: CodeTaskNotFound, err: ErrTaskNotFound},
		{name: "task conflict", want: "task_conflict", code: CodeTaskConflict, err: ErrTaskConflict},
		{name: "task changed", want: "task_changed", code: CodeTaskChanged, err: ErrTaskChanged},
		{name: "idempotency conflict", want: "idempotency_conflict", code: CodeIdempotencyConflict, err: ErrIdempotencyConflict},
		{name: "evidence conflict", want: "evidence_conflict", code: CodeEvidenceConflict, err: ErrEvidenceConflict},
		{name: "source invalid", want: "source_invalid", code: CodeSourceInvalid, err: ErrSourceInvalid},
		{name: "world mismatch", want: "world_mismatch", code: CodeWorldMismatch, err: ErrWorldMismatch},
		{name: "generation stale", want: "generation_stale", code: CodeGenerationStale, err: ErrGenerationStale},
		{name: "clock mismatch", want: "clock_mismatch", code: CodeClockMismatch, err: ErrClockMismatch},
		{name: "clock rewound", want: "clock_rewound", code: CodeClockRewound, err: ErrClockRewound},
		{name: "world not ready", want: "world_not_ready", code: CodeWorldNotReady, err: ErrWorldNotReady},
		{name: "save in progress", want: "save_in_progress", code: CodeSaveInProgress, err: ErrSaveInProgress},
		{name: "task terminal", want: "task_terminal", code: CodeTaskTerminal, err: ErrTaskTerminal},
		{name: "task capacity exceeded", want: "task_capacity_exceeded", code: CodeTaskCapacityExceeded, err: ErrTaskCapacityExceeded},
		{name: "checkpoint unconfirmed", want: "checkpoint_unconfirmed", code: CodeCheckpointUnconfirmed, err: ErrCheckpointUnconfirmed},
		{name: "checkpoint missing", want: "checkpoint_missing", code: CodeCheckpointMissing, err: ErrCheckpointMissing},
		{name: "checkpoint invalid", want: "checkpoint_invalid", code: CodeCheckpointInvalid, err: ErrCheckpointInvalid},
		{name: "store in use", want: "store_in_use", code: CodeStoreInUse, err: ErrStoreInUse},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(tt.code); got != tt.want {
				t.Fatalf("Code constant = %q, want literal %q", got, tt.want)
			}
			if got := tt.err.Code; got != Code(tt.want) {
				t.Fatalf("sentinel Code = %q, want literal %q", got, tt.want)
			}
			if got := tt.err.Error(); got != tt.want {
				t.Fatalf("Error() = %q, want literal %q", got, tt.want)
			}
			if !Code(tt.want).Valid() {
				t.Fatalf("Code(literal %q).Valid() = false", tt.want)
			}

			wrapped := fmt.Errorf("task operation failed: %w", &Error{Code: Code(tt.want)})
			if !errors.Is(wrapped, tt.err) {
				t.Fatalf("errors.Is(%v, %v) = false", wrapped, tt.err)
			}
			var typed *Error
			if !errors.As(wrapped, &typed) {
				t.Fatalf("errors.As(%v, *Error) = false", wrapped)
			}
			if typed.Code != Code(tt.want) {
				t.Fatalf("wrapped Code = %q, want literal %q", typed.Code, tt.want)
			}
		})
	}
}

func TestErrorRejectsUnknownCodeAndKeepsOutputSanitized(t *testing.T) {
	if Code("database_failed").Valid() {
		t.Fatal("unknown error code reported valid")
	}

	err := fmt.Errorf("open task store: %w", ErrStoreInUse)
	got := err.Error()
	for _, secret := range []string{
		`C:\\Users\\alice\\private\\tasks.sqlite`,
		"SELECT record_json FROM tasks",
		`{"complete_game_payload":"secret"}`,
	} {
		if strings.Contains(got, secret) {
			t.Fatalf("error output %q exposed %q", got, secret)
		}
	}
	if got != "open task store: store_in_use" {
		t.Fatalf("sanitized error = %q", got)
	}
}

func TestErrorWrapDropsUnknownCauseFromEntireChain(t *testing.T) {
	cause := errors.New(`C:\Users\alice\private\tasks.sqlite: SELECT record_json FROM tasks: {"payload":"secret"}`)
	err := WrapError(CodeStoreInUse, fmt.Errorf("driver failure: %w", cause))

	if !errors.Is(err, ErrStoreInUse) {
		t.Fatalf("errors.Is(%v, ErrStoreInUse) = false", err)
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodeStoreInUse {
		t.Fatalf("errors.As(%v) = %+v", err, typed)
	}
	if errors.Is(err, cause) {
		t.Fatal("unknown sensitive cause remained accessible")
	}
	requireSanitizedErrorChain(t, err, `C:\Users\alice\private\tasks.sqlite`, "SELECT record_json FROM tasks", `{"payload":"secret"}`)
}

func TestErrorWrapCanonicalizesSafeClassificationCauses(t *testing.T) {
	cancelled := WrapError(CodeTaskChanged, fmt.Errorf(`C:\private\tasks.sqlite: %w`, context.Canceled))
	if !errors.Is(cancelled, context.Canceled) {
		t.Fatal("context cancellation classification was not preserved")
	}
	requireSanitizedErrorChain(t, cancelled, `C:\private\tasks.sqlite`)
	deadline := WrapError(CodeTaskChanged, fmt.Errorf(`SELECT secret FROM tasks: %w`, context.DeadlineExceeded))
	if !errors.Is(deadline, context.DeadlineExceeded) {
		t.Fatal("context deadline classification was not preserved")
	}
	requireSanitizedErrorChain(t, deadline, "SELECT secret FROM tasks")

	inner := WrapError(CodeTaskChanged, errors.New(`SELECT secret FROM tasks`))
	outer := WrapError(CodeTaskConflict, fmt.Errorf(`{"opaque":"complete"}: %w`, inner))
	if !errors.Is(outer, ErrTaskChanged) {
		t.Fatal("nested task error classification was not preserved")
	}
	requireSanitizedErrorChain(t, outer, "SELECT secret FROM tasks", `{"opaque":"complete"}`)
}

func TestErrorNilReceiverIsSafe(t *testing.T) {
	var err *Error
	if got := err.Error(); got != "task_error" {
		t.Fatalf("nil Error() = %q, want task_error", got)
	}
}

func requireSanitizedErrorChain(t *testing.T, err error, forbidden ...string) {
	t.Helper()
	for depth := 0; err != nil; depth++ {
		if depth >= 16 {
			t.Fatal("error unwrap chain exceeded 16 entries")
		}
		for _, value := range forbidden {
			if strings.Contains(err.Error(), value) {
				t.Fatalf("error chain exposed %q in %q", value, err.Error())
			}
		}
		err = errors.Unwrap(err)
	}
}
