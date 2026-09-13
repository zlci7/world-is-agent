package tool

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/task"
)

func TestTaskToolsProposalValidationAndStorageFailure(t *testing.T) {
	svc, world, rc := taskToolFixture(t)
	tt := NewTaskTools(svc, world, rc)
	for _, mutate := range []func(*protocol.ActionResult){
		func(r *protocol.ActionResult) { r.Status = protocol.ActionStatus_ACTION_STATUS_FAILED },
		func(r *protocol.ActionResult) { r.TaskProposal.Clock = nil },
		func(r *protocol.ActionResult) { r.TaskProposal.WakeAt = 100 },
		func(r *protocol.ActionResult) { r.TaskProposal.DeadlineAt = 119 },
		func(r *protocol.ActionResult) { r.TaskProposal.ParticipantEntityIds = []string{" "} },
	} {
		r := taskProposalResult()
		mutate(r)
		ref, err := tt.CaptureProposal(context.Background(), rc, r)
		if err != nil || ref != "" {
			t.Fatalf("invalid proposal: %q %v", ref, err)
		}
	}
	store, err := task.OpenSQLiteStore(context.Background(), task.StoreOptions{Path: filepath.Join(t.TempDir(), "closed.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	closed := task.NewService(store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	tt = NewTaskTools(closed, world, rc)
	ref := captureTaskProposal(t, tt, rc)
	got, err := tt.Execute(context.Background(), rc, taskCreateCall(ref))
	if err == nil || got.Status != "" || strings.Contains(err.Error(), "database") {
		t.Fatalf("storage error became business result: %+v %v", got, err)
	}
}
