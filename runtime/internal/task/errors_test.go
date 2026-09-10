package task

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestErrorCodesSupportStableClassificationAndWrapping(t *testing.T) {
	tests := []struct {
		code Code
		err  *Error
	}{
		{CodeInvalidTaskSpec, ErrInvalidTaskSpec},
		{CodeTaskNotFound, ErrTaskNotFound},
		{CodeTaskConflict, ErrTaskConflict},
		{CodeTaskChanged, ErrTaskChanged},
		{CodeIdempotencyConflict, ErrIdempotencyConflict},
		{CodeEvidenceConflict, ErrEvidenceConflict},
		{CodeSourceInvalid, ErrSourceInvalid},
		{CodeWorldMismatch, ErrWorldMismatch},
		{CodeGenerationStale, ErrGenerationStale},
		{CodeClockMismatch, ErrClockMismatch},
		{CodeClockRewound, ErrClockRewound},
		{CodeWorldNotReady, ErrWorldNotReady},
		{CodeSaveInProgress, ErrSaveInProgress},
		{CodeTaskTerminal, ErrTaskTerminal},
		{CodeTaskCapacityExceeded, ErrTaskCapacityExceeded},
		{CodeCheckpointUnconfirmed, ErrCheckpointUnconfirmed},
		{CodeCheckpointMissing, ErrCheckpointMissing},
		{CodeCheckpointInvalid, ErrCheckpointInvalid},
		{CodeStoreInUse, ErrStoreInUse},
	}

	for _, tt := range tests {
		t.Run(string(tt.code), func(t *testing.T) {
			if got := tt.err.Error(); got != string(tt.code) {
				t.Fatalf("Error() = %q, want %q", got, tt.code)
			}
			if !tt.code.Valid() {
				t.Fatalf("Code(%q).Valid() = false", tt.code)
			}

			wrapped := fmt.Errorf("task operation failed: %w", &Error{Code: tt.code})
			if !errors.Is(wrapped, tt.err) {
				t.Fatalf("errors.Is(%v, %v) = false", wrapped, tt.err)
			}
			var typed *Error
			if !errors.As(wrapped, &typed) {
				t.Fatalf("errors.As(%v, *Error) = false", wrapped)
			}
			if typed.Code != tt.code {
				t.Fatalf("wrapped Code = %q, want %q", typed.Code, tt.code)
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

func TestErrorWrapPreservesCauseWithoutExposingIt(t *testing.T) {
	cause := errors.New(`C:\Users\alice\private\tasks.sqlite: SELECT record_json FROM tasks: {"payload":"secret"}`)
	err := WrapError(CodeStoreInUse, cause)

	if !errors.Is(err, ErrStoreInUse) {
		t.Fatalf("errors.Is(%v, ErrStoreInUse) = false", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("errors.Is(%v, cause) = false", err)
	}
	if got := err.Error(); got != "store_in_use" {
		t.Fatalf("Error() = %q, want store_in_use", got)
	}
}

func TestErrorNilReceiverIsSafe(t *testing.T) {
	var err *Error
	if got := err.Error(); got != "task_error" {
		t.Fatalf("nil Error() = %q, want task_error", got)
	}
}
