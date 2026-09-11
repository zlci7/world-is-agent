package task

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func TestRecordMutationsReserveCleanupCompleteCapacity(t *testing.T) {
	t.Run("RegisterOperation before minimal cleanup", func(t *testing.T) {
		fixture, created, _ := newCapacityChainFixture(t, 1)
		exec := operationExecution(fixture, created.Task, "capacity-chain-register")
		candidate := registeredOperation(exec, "operation-capacity-chain-register")
		current := readCapacityChainRecord(t, fixture, created.Task)
		updated := current
		updated.Operations = append(cloneOperations(current.Operations), candidate)
		projected := cleanupCompleteCapacityRecord(updated)
		createJSON, historyJSON := taskIdempotencyMetadata(t, fixture.store, current.Owner, current.ID)
		weak := 2*len(mustJSONBytes(t, updated)) + len(createJSON) + len(historyJSON) + intentTerminalStructuralReserve
		strong := 2*len(mustJSONBytes(t, projected)) + len(createJSON) + len(historyJSON) + intentTerminalStructuralReserve
		assertUpstreamMutationReservesCleanup(t, fixture, current, weak, strong,
			func(svc *Service) error {
				_, err := svc.RegisterOperation(context.Background(), exec, candidate)
				return err
			},
			func(svc *Service) error {
				return recordAllMinimalCleanups(svc, fixture.head.Binding, projected)
			})
	})

	t.Run("AdmitEvidence and Reconcile before all minimal cleanups", func(t *testing.T) {
		fixture, created, operations := newCapacityChainFixture(t, 2)
		evidence := operationEvidence(operations[0], created.Task.ID, "fact-capacity-chain-admit", EvidenceKindProgress)
		current := readCapacityChainRecord(t, fixture, created.Task)
		admitted := current
		admitted.Evidence = append(cloneEvidenceSlice(current.Evidence), evidence)
		admitted.NeedsReconcile = true
		admittedProjected := cleanupCompleteCapacityRecord(admitted)
		reconciled := reconciledCapacityRecord(admitted, evidence, StateRunning, "")
		reconciledProjected := cleanupCompleteCapacityRecord(reconciled)
		createJSON, historyJSON := taskIdempotencyMetadata(t, fixture.store, current.Owner, current.ID)
		metadata := len(createJSON) + len(historyJSON) + intentTerminalStructuralReserve
		weakAdmit := 2*len(mustJSONBytes(t, admitted)) + metadata
		weakReconcile := len(mustJSONBytes(t, reconciled)) + len(mustJSONBytes(t, reconciledProjected)) + metadata
		weak := weakAdmit
		if weakReconcile > weak {
			weak = weakReconcile
		}
		strongAdmit := 2*len(mustJSONBytes(t, admittedProjected)) + metadata
		strongCleanup := 2*len(mustJSONBytes(t, reconciledProjected)) + metadata
		strong := strongAdmit
		if strongCleanup < strong {
			strong = strongCleanup
		}
		assertUpstreamMutationReservesCleanup(t, fixture, current, weak, strong,
			func(svc *Service) error {
				added, err := svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence)
				if err == nil && !added {
					return errors.New("evidence was not added")
				}
				return err
			},
			func(svc *Service) error {
				if _, err := svc.Reconcile(context.Background(), reconcileExecution(fixture, current, current.Revision)); err != nil {
					return err
				}
				return recordAllMinimalCleanups(svc, fixture.head.Binding, reconciledProjected)
			})
	})

	for _, kind := range []string{"wait", "cancel"} {
		kind := kind
		t.Run("ApplyIntent "+kind+" before cleanup", func(t *testing.T) {
			fixture, created, _ := newCapacityChainFixture(t, 2)
			wake, err := loadOnlyWake(t, fixture.store, created.Task.Owner)
			if err != nil {
				t.Fatal(err)
			}
			current := readCapacityChainRecord(t, fixture, created.Task)
			exec := intentExecution(fixture, current, wake.ID, current.Revision, "capacity-chain-"+kind)
			intent := Intent{Kind: kind}
			updated := current
			updated.Revision++
			updated.NeedsReconcile = false
			updated.PauseReason = ""
			updated.NoProgressAttempts = 0
			updated.ReconcileAttempts = 0
			if kind == "wait" {
				nextWake := int64(250)
				intent.NextWakeAt = &nextWake
				updated.State = StateWaiting
				updated.NextWakeAt = &nextWake
				updated.Result = nil
			} else {
				intent.Reason = "capacity-chain-cancel"
				updated.State = StateCancelled
				updated.NextWakeAt = nil
				updated.Result = &Result{
					ID: "result_capacity_chain", TaskID: updated.ID, Revision: updated.Revision,
					State: updated.State, Reason: intent.Reason, OccurredAt: exec.Clock.Tick,
					EvidenceRefs: []string{}, Source: exec.Source,
				}
			}
			projected := cleanupCompleteCapacityRecord(updated)
			createJSON, _ := taskIdempotencyMetadata(t, fixture.store, current.Owner, current.ID)
			historyJSON := intentHistoryAfterCapacityMutation(t, exec, intent, updated)
			metadata := len(createJSON) + len(historyJSON)
			weak := len(mustJSONBytes(t, updated)) + metadata
			if kind == "wait" {
				weak += len(mustJSONBytes(t, updated)) + intentTerminalStructuralReserve
			}
			strong := 2*len(mustJSONBytes(t, projected)) + metadata
			if kind == "wait" {
				strong += intentTerminalStructuralReserve
			}
			assertUpstreamMutationReservesCleanup(t, fixture, current, weak, strong,
				func(svc *Service) error {
					svc.newID = func(prefix string) string { return prefix + "_capacity_chain" }
					_, err := svc.ApplyIntent(context.Background(), exec, intent)
					return err
				},
				func(svc *Service) error {
					return recordAllMinimalCleanups(svc, fixture.head.Binding, projected)
				})
		})
	}

	for _, terminal := range []bool{false, true} {
		terminal := terminal
		name := "nonterminal"
		if terminal {
			name = "terminal"
		}
		t.Run("Reconcile "+name+" before first cleanup", func(t *testing.T) {
			fixture, created, operations := newCapacityChainFixture(t, 2)
			kind := EvidenceKindProgress
			state := StateRunning
			resultID := ""
			if terminal {
				kind = EvidenceKindSatisfied
				state = StateSucceeded
				resultID = "result_capacity_chain"
			}
			evidence := operationEvidence(operations[0], created.Task.ID, "fact-capacity-chain-reconcile-"+name, kind)
			if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
				t.Fatalf("AdmitEvidence() = (%v, %v)", added, err)
			}
			current := readCapacityChainRecord(t, fixture, created.Task)
			updated := reconciledCapacityRecord(current, evidence, state, resultID)
			projected := cleanupCompleteCapacityRecord(updated)
			createJSON, historyJSON := taskIdempotencyMetadata(t, fixture.store, current.Owner, current.ID)
			metadata := len(createJSON) + len(historyJSON)
			weak := len(mustJSONBytes(t, updated)) + len(mustJSONBytes(t, projected)) + metadata
			strong := 2*len(mustJSONBytes(t, projected)) + metadata
			if !terminal {
				weak += intentTerminalStructuralReserve
				strong += intentTerminalStructuralReserve
			}
			assertUpstreamMutationReservesCleanup(t, fixture, current, weak, strong,
				func(svc *Service) error {
					svc.newID = func(prefix string) string { return prefix + "_capacity_chain" }
					_, err := svc.Reconcile(context.Background(), reconcileExecution(fixture, current, current.Revision))
					return err
				},
				func(svc *Service) error {
					return recordAllMinimalCleanups(svc, fixture.head.Binding, projected)
				})
		})
	}
}

func newCapacityChainFixture(t *testing.T, operationCount int) (createFixture, CreateResult, []Operation) {
	t.Helper()
	fixture, created, _ := newIntentFixture(t, StoreOptions{Path: filepath.Join(t.TempDir(), "tasks.sqlite")})
	operations := make([]Operation, operationCount)
	for index := range operations {
		exec := operationExecution(fixture, created.Task, fmt.Sprintf("capacity-chain-%d", index))
		operations[index] = registeredOperation(exec, fmt.Sprintf("operation-capacity-chain-%d", index))
		if _, err := fixture.svc.RegisterOperation(context.Background(), exec, operations[index]); err != nil {
			t.Fatal(err)
		}
	}
	return fixture, created, operations
}

func readCapacityChainRecord(t *testing.T, fixture createFixture, created Record) Record {
	t.Helper()
	record, err := fixture.svc.Read(context.Background(), created.Owner, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func cleanupCompleteCapacityRecord(record Record) Record {
	projected := record
	projected.Cleanup = append([]Cleanup(nil), record.Cleanup...)
	cleaned := make(map[string]struct{}, len(projected.Cleanup))
	for _, cleanup := range projected.Cleanup {
		cleaned[cleanup.OperationID] = struct{}{}
	}
	for _, operation := range projected.Operations {
		if _, found := cleaned[operation.ID]; !found {
			projected.Cleanup = append(projected.Cleanup, Cleanup{OperationID: operation.ID, Status: CleanupStatusReleased})
		}
	}
	return projected
}

func intentHistoryAfterCapacityMutation(t *testing.T, exec ExecutionContext, intent Intent, response Record) []byte {
	t.Helper()
	request, err := prepareIntentRequest(exec, intent)
	if err != nil {
		t.Fatal(err)
	}
	responseJSON := mustJSONBytes(t, response)
	return mustJSONBytes(t, []intentCall{{
		RequestJSON: request.requestJSON, RequestHash: request.fingerprint,
		ResponseJSON: responseJSON, ResponseHash: sha256Hex(responseJSON),
	}})
}

func assertUpstreamMutationReservesCleanup(
	t *testing.T,
	fixture createFixture,
	current Record,
	weak int,
	strong int,
	mutate func(*Service) error,
	finishCleanup func(*Service) error,
) {
	t.Helper()
	if weak >= strong-1 {
		t.Fatalf("capacity window missing: weak=%d strong=%d", weak, strong)
	}
	limit := weak + (strong-weak)/2
	path := fixture.store.path
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	store := openTaskTestStore(t, StoreOptions{Path: path, MaxTaskBytes: limit})
	svc := NewService(store)
	beforeTask, beforeWakes, beforeHistory := snapshotIntentRows(t, store, current.Owner, current.ID)
	mutationErr := mutate(svc)
	if mutationErr == nil {
		cleanupErr := finishCleanup(svc)
		if errors.Is(cleanupErr, ErrInvalidTaskSpec) {
			t.Fatalf("upstream mutation was admitted at weak=%d, then cleanup was rejected at strong=%d (limit=%d)", weak, strong, limit)
		}
		t.Fatalf("upstream mutation error = nil; downstream cleanup error = %v", cleanupErr)
	}
	if !errors.Is(mutationErr, ErrInvalidTaskSpec) {
		t.Fatalf("upstream mutation error = %v, want ErrInvalidTaskSpec", mutationErr)
	}
	afterTask, afterWakes, afterHistory := snapshotIntentRows(t, store, current.Owner, current.ID)
	if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWakes, afterWakes) || !bytes.Equal(beforeHistory, afterHistory) {
		t.Fatal("rejected upstream mutation changed task/index/wake/create/history bytes")
	}
}

func recordAllMinimalCleanups(svc *Service, binding Binding, record Record) error {
	for _, operation := range record.Operations {
		if err := svc.RecordCleanup(context.Background(), binding, record.ID,
			Cleanup{OperationID: operation.ID, Status: CleanupStatusReleased}); err != nil {
			return err
		}
	}
	return nil
}
