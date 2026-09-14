package gateway

import (
	"errors"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/task"
)

func TestFailedTaskExecutionReleasesNonterminalControl(t *testing.T) {
	for _, status := range []string{"released", "handed_off"} {
		t.Run(status, func(t *testing.T) {
			f := newTaskWireFixture(t, false)
			env, exec, record, op := beginWireExecution(t, f)
			done := make(chan error, 1)
			go func() { done <- env.finishTaskExecution(f.ctx, exec, errors.New("model failed")) }()
			for i := 0; i < 3; i++ {
				f.observe(f.next())
			}
			control := f.next().GetTaskControl()
			if control == nil || control.OperationId != op.ID || control.Reason != "execution_ended" {
				t.Fatalf("failed execution did not release its control: %v", control)
			}
			f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_TaskControlResult{TaskControlResult: &protocol.TaskControlResult{Scope: control.Scope, TaskId: control.TaskId, OperationId: control.OperationId, RequestId: control.RequestId, Status: status}}})
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			r, err := f.service.Read(f.ctx, f.key, record.ID)
			if err != nil || r.State != task.StatePaused || r.Result != nil || len(r.Cleanup) != 1 || r.Cleanup[0].Status != status || r.Cleanup[0].Reason != "execution_ended" {
				t.Fatalf("release changed business result: %+v %v", r, err)
			}
			if err := env.finishTaskExecution(f.ctx, exec, errors.New("duplicate finish")); err != nil {
				t.Fatal(err)
			}
			select {
			case m := <-f.messages:
				t.Fatalf("duplicate release: %v", m)
			default:
			}
		})
	}
}

func TestFailedTaskExecutionPreservesRegisteredWait(t *testing.T) {
	f := newTaskWireFixture(t, false)
	env, exec, record, _ := beginWireExecution(t, f)
	exec.Source = task.SourceRef{Kind: task.SourceKindTaskWake, EventID: exec.WakeID, TurnID: "turn", CallID: "wait"}
	wait := int64(70)
	if _, err := f.service.ApplyIntent(f.ctx, exec, task.Intent{Kind: "wait", NextWakeAt: &wait}); err != nil {
		t.Fatal(err)
	}
	if err := env.finishTaskExecution(f.ctx, exec, errors.New("late failure")); err != nil {
		t.Fatal(err)
	}
	// The release decision may have been prepared before the wait committed.
	if err := env.releaseTaskOperations(f.ctx, exec.Binding, record, "execution_ended"); err != nil {
		t.Fatal(err)
	}
	r, err := f.service.Read(f.ctx, f.key, record.ID)
	if err != nil || r.State != task.StateWaiting || len(r.Cleanup) != 0 {
		t.Fatalf("wait released: %+v %v", r, err)
	}
	select {
	case m := <-f.messages:
		t.Fatalf("waiting action released: %v", m)
	default:
	}
}
