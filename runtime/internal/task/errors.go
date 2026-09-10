package task

type Code string

const (
	CodeInvalidTaskSpec       Code = "invalid_task_spec"
	CodeTaskNotFound          Code = "task_not_found"
	CodeTaskConflict          Code = "task_conflict"
	CodeTaskChanged           Code = "task_changed"
	CodeIdempotencyConflict   Code = "idempotency_conflict"
	CodeEvidenceConflict      Code = "evidence_conflict"
	CodeSourceInvalid         Code = "source_invalid"
	CodeWorldMismatch         Code = "world_mismatch"
	CodeGenerationStale       Code = "generation_stale"
	CodeClockMismatch         Code = "clock_mismatch"
	CodeClockRewound          Code = "clock_rewound"
	CodeWorldNotReady         Code = "world_not_ready"
	CodeSaveInProgress        Code = "save_in_progress"
	CodeTaskTerminal          Code = "task_terminal"
	CodeTaskCapacityExceeded  Code = "task_capacity_exceeded"
	CodeCheckpointUnconfirmed Code = "checkpoint_unconfirmed"
	CodeCheckpointMissing     Code = "checkpoint_missing"
	CodeCheckpointInvalid     Code = "checkpoint_invalid"
	CodeStoreInUse            Code = "store_in_use"
)

type Error struct {
	Code  Code
	cause error
}

var (
	ErrInvalidTaskSpec       = &Error{Code: CodeInvalidTaskSpec}
	ErrTaskNotFound          = &Error{Code: CodeTaskNotFound}
	ErrTaskConflict          = &Error{Code: CodeTaskConflict}
	ErrTaskChanged           = &Error{Code: CodeTaskChanged}
	ErrIdempotencyConflict   = &Error{Code: CodeIdempotencyConflict}
	ErrEvidenceConflict      = &Error{Code: CodeEvidenceConflict}
	ErrSourceInvalid         = &Error{Code: CodeSourceInvalid}
	ErrWorldMismatch         = &Error{Code: CodeWorldMismatch}
	ErrGenerationStale       = &Error{Code: CodeGenerationStale}
	ErrClockMismatch         = &Error{Code: CodeClockMismatch}
	ErrClockRewound          = &Error{Code: CodeClockRewound}
	ErrWorldNotReady         = &Error{Code: CodeWorldNotReady}
	ErrSaveInProgress        = &Error{Code: CodeSaveInProgress}
	ErrTaskTerminal          = &Error{Code: CodeTaskTerminal}
	ErrTaskCapacityExceeded  = &Error{Code: CodeTaskCapacityExceeded}
	ErrCheckpointUnconfirmed = &Error{Code: CodeCheckpointUnconfirmed}
	ErrCheckpointMissing     = &Error{Code: CodeCheckpointMissing}
	ErrCheckpointInvalid     = &Error{Code: CodeCheckpointInvalid}
	ErrStoreInUse            = &Error{Code: CodeStoreInUse}
)

func (c Code) Valid() bool {
	switch c {
	case CodeInvalidTaskSpec,
		CodeTaskNotFound,
		CodeTaskConflict,
		CodeTaskChanged,
		CodeIdempotencyConflict,
		CodeEvidenceConflict,
		CodeSourceInvalid,
		CodeWorldMismatch,
		CodeGenerationStale,
		CodeClockMismatch,
		CodeClockRewound,
		CodeWorldNotReady,
		CodeSaveInProgress,
		CodeTaskTerminal,
		CodeTaskCapacityExceeded,
		CodeCheckpointUnconfirmed,
		CodeCheckpointMissing,
		CodeCheckpointInvalid,
		CodeStoreInUse:
		return true
	default:
		return false
	}
}

func (e *Error) Error() string {
	if e == nil || !e.Code.Valid() {
		return "task_error"
	}
	return string(e.Code)
}

func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && e != nil && other != nil && e.Code.Valid() && e.Code == other.Code
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func WrapError(code Code, cause error) *Error {
	return &Error{Code: code, cause: cause}
}
