package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestAdmissionReservesDeterministicReconciliationGrowth(t *testing.T) {
	for _, kind := range []string{EvidenceKindSatisfied, EvidenceKindProgress} {
		for _, size := range []int{48, 80} {
			t.Run(fmt.Sprintf("%s_%dKiB", kind, size), func(t *testing.T) {
				fixture, created, operations := newCapacityChainFixture(t, 3)
				evidence := operationEvidence(operations[0], created.Task.ID, "large-fact", kind)
				payload := json.RawMessage(`{"opaque":"` + strings.Repeat("x", size<<10) + `"}`)
				if kind == EvidenceKindSatisfied {
					evidence.Source.Facts = payload
				} else {
					evidence.Details = payload
				}
				if size == 80 {
					assertGrowthAdmissionRejected(t, fixture, created.Task, evidence)
					return
				}
				admitGrowthEvidence(t, fixture, evidence)
				got, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
				if err != nil {
					t.Fatalf("accepted fact cannot reconcile: %v", err)
				}
				if kind == EvidenceKindSatisfied {
					if got.Task.State != StateSucceeded || !bytes.Equal(got.Task.Result.Source.Facts, payload) {
						t.Fatal("terminal source was not preserved")
					}
				} else if got.Task.State != StateRunning || !bytes.Equal(got.Task.Progress, payload) {
					t.Fatal("progress was not preserved")
				}
				if err := recordAllMinimalCleanups(fixture.svc, fixture.head.Binding, got.Task); err != nil {
					t.Fatalf("accepted fact cannot finish all cleanups: %v", err)
				}
			})
		}
	}
}

func TestAdmissionProjectsAllPendingEvidenceSelections(t *testing.T) {
	for _, branch := range []string{"terminal", "wait", "progress"} {
		t.Run(branch, func(t *testing.T) {
			fixture, created, operations := newCapacityChainFixture(t, 4)
			winner := operationEvidence(operations[0], created.Task.ID, "a-winner", EvidenceKindProgress)
			winner.OccurredAt = 95
			payload := json.RawMessage(`{"opaque":"` + strings.Repeat("x", 48<<10) + `"}`)
			winner.Details = payload
			if branch == "terminal" {
				winner.Kind = EvidenceKindSatisfied
				winner.OccurredAt = 80
				winner.Source.Facts = payload
				winner.Details = nil
			}
			admitGrowthEvidence(t, fixture, winner)
			wait := operationEvidence(operations[1], created.Task.ID, "wait", EvidenceKindProgress)
			if branch != "progress" {
				due := int64(150)
				wait.WaitUntil = &due
			}
			admitGrowthEvidence(t, fixture, wait)
			loser := operationEvidence(operations[2], created.Task.ID, "z-loser", winner.Kind)
			loser.OccurredAt = winner.OccurredAt
			admitGrowthEvidence(t, fixture, loser)
			// The incoming fact is small enough by itself; the earlier selected
			// fact still supplies the large copy in the deterministic next Record.
			overflow := operationEvidence(operations[3], created.Task.ID, "overflow", EvidenceKindProgress)
			overflow.OccurredAt = 70
			overflow.Details = json.RawMessage(`{"opaque":"` + strings.Repeat("y", 30<<10) + `"}`)
			assertGrowthAdmissionRejected(t, fixture, created.Task, overflow)
			got, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
			if err != nil {
				t.Fatalf("pending facts cannot reconcile: %v", err)
			}
			if branch == "terminal" {
				if got.Task.State != StateSucceeded || got.Task.Result.EvidenceRefs[0] != winner.FactID || !bytes.Equal(got.Task.Result.Source.Facts, payload) {
					t.Fatal("terminal selection changed")
				}
			} else {
				if !bytes.Equal(got.Task.Progress, payload) {
					t.Fatal("progress selection changed")
				}
				if branch == "wait" && (got.Task.State != StateWaiting || got.Task.NextWakeAt == nil || *got.Task.NextWakeAt != 150) {
					t.Fatal("wait selection changed")
				}
				if branch == "progress" && got.Task.State != StateRunning {
					t.Fatal("ordinary progress state changed")
				}
			}
			for _, fact := range got.Task.Evidence {
				if !fact.Applied {
					t.Fatal("pending fact was not applied")
				}
			}
			if err := recordAllMinimalCleanups(fixture.svc, fixture.head.Binding, got.Task); err != nil {
				t.Fatalf("cleanup cannot complete: %v", err)
			}
		})
	}
}

func TestAdmissionReservesOnlySelectedOpaqueCopy(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprintf("terminal_%v", terminal), func(t *testing.T) {
			fixture, created, operations := newCapacityChainFixture(t, 2)
			winner := operationEvidence(operations[0], created.Task.ID, "winner", EvidenceKindProgress)
			winner.OccurredAt = 95
			if terminal {
				winner.Kind = EvidenceKindSatisfied
			}
			admitGrowthEvidence(t, fixture, winner)
			loser := operationEvidence(operations[1], created.Task.ID, "loser", EvidenceKindProgress)
			loser.Details = json.RawMessage(`{"opaque":"` + strings.Repeat("x", 80<<10) + `"}`)
			admitGrowthEvidence(t, fixture, loser)
			got, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
			if err != nil {
				t.Fatalf("unselected payload prevented reconciliation: %v", err)
			}
			if terminal {
				if got.Task.State != StateSucceeded || got.Task.Result.EvidenceRefs[0] != winner.FactID {
					t.Fatal("terminal selection changed")
				}
			} else if !bytes.Equal(got.Task.Progress, winner.Details) {
				t.Fatal("newest progress selection changed")
			}
			if !bytes.Equal(got.Task.Evidence[1].Details, loser.Details) {
				t.Fatal("unselected opaque details changed")
			}
			if err := recordAllMinimalCleanups(fixture.svc, fixture.head.Binding, got.Task); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCleanupPreservesPendingReconciliationGrowth(t *testing.T) {
	for _, kind := range []string{EvidenceKindSatisfied, EvidenceKindProgress} {
		t.Run(kind, func(t *testing.T) {
			fixture, created, operations := newCapacityChainFixture(t, 3)
			evidence := operationEvidence(operations[0], created.Task.ID, "pending", kind)
			payload := json.RawMessage(`{"opaque":"` + strings.Repeat("x", 48<<10) + `"}`)
			if kind == EvidenceKindSatisfied {
				evidence.Source.Facts = payload
			} else {
				evidence.Details = payload
			}
			admitGrowthEvidence(t, fixture, evidence)
			beforeTask, beforeWakes, beforeHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
			large := Cleanup{OperationID: operations[0].ID, Status: CleanupStatusReleased, Reason: strings.Repeat("x", 32<<10)}
			for retry := 0; retry < 2; retry++ {
				if err := fixture.svc.RecordCleanup(context.Background(), fixture.head.Binding, created.Task.ID, large); !errors.Is(err, ErrInvalidTaskSpec) {
					t.Fatalf("cleanup consumed pending reconciliation capacity: %v", err)
				}
				afterTask, afterWakes, afterHistory := snapshotIntentRows(t, fixture.store, created.Task.Owner, created.Task.ID)
				if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWakes, afterWakes) || !bytes.Equal(beforeHistory, afterHistory) {
					t.Fatal("rejected cleanup changed task/index/wakes/create/history bytes")
				}
			}
			// Minimal cleanup is reachable on both sides of reconciliation.
			minimal := Cleanup{OperationID: operations[0].ID, Status: CleanupStatusReleased}
			if err := fixture.svc.RecordCleanup(context.Background(), fixture.head.Binding, created.Task.ID, minimal); err != nil {
				t.Fatal(err)
			}
			got, err := fixture.svc.Reconcile(context.Background(), reconcileExecution(fixture, created.Task, 1))
			if err != nil {
				t.Fatal(err)
			}
			if err := recordAllMinimalCleanups(fixture.svc, fixture.head.Binding, got.Task); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func admitGrowthEvidence(t *testing.T, fixture createFixture, evidence Evidence) {
	t.Helper()
	if added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence); err != nil || !added {
		t.Fatalf("AdmitEvidence(%s) = %v, %v", evidence.FactID, added, err)
	}
}

func assertGrowthAdmissionRejected(t *testing.T, fixture createFixture, record Record, evidence Evidence) {
	t.Helper()
	beforeTask, beforeWakes, beforeHistory := snapshotIntentRows(t, fixture.store, record.Owner, record.ID)
	for retry := 0; retry < 2; retry++ {
		added, err := fixture.svc.AdmitEvidence(context.Background(), fixture.head.Binding, evidence)
		if added || !errors.Is(err, ErrInvalidTaskSpec) {
			t.Fatalf("oversized deterministic reconciliation admitted: added=%v error=%v", added, err)
		}
		afterTask, afterWakes, afterHistory := snapshotIntentRows(t, fixture.store, record.Owner, record.ID)
		if !bytes.Equal(beforeTask, afterTask) || !bytes.Equal(beforeWakes, afterWakes) || !bytes.Equal(beforeHistory, afterHistory) {
			t.Fatal("rejected admission changed task/index/wakes/create/history bytes")
		}
	}
}
