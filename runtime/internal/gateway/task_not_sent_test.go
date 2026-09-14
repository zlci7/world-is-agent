package gateway

import (
	"context"
	"errors"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tool"
)

func TestTaskCancelledBeforeTransportIsNotSent(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(map[bool]string{false: "sync", true: "async"}[async], func(t *testing.T) {
			f := newTaskWireFixture(t, false)
			env, exec, record, _ := beginWireExecution(t, f)
			_, epoch, _ := env.taskAuthority.Current()
			req := &protocol.ActionRequest{ActionId: "not-sent", WorldId: "world", EntityId: "actor", Capability: "follow_route"}
			if err := env.RegisterTaskAction(f.ctx, tool.RuntimeCallContext{Execution: exec, ObservedTask: &record, AuthorityEpoch: epoch}, req); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(f.ctx)
			cancel()
			var err error
			if async {
				_, err = env.StartAction(ctx, req)
			} else {
				_, err = env.SubmitAction(ctx, req)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel: %v", err)
			}
			r, err := f.service.Read(f.ctx, f.key, record.ID)
			if err != nil || r.Operations[1].Status != task.OperationStatusNotSent {
				t.Fatalf("not-sent state: %+v %v", r, err)
			}
			if taskOperationAwaitingEvidence(task.Record{Operations: []task.Operation{r.Operations[1]}}) {
				t.Fatal("waiting for evidence of an unsent action")
			}
			select {
			case m := <-f.messages:
				t.Fatalf("unsent action emitted message: %v", m)
			default:
			}
		})
	}
}

func TestTransportFailureAfterStartIsNotClassifiedUnsent(t *testing.T) {
	stream := &failedSendStream{captureStream: captureStream{sent: make(chan *protocol.RuntimeMessage, 1)}}
	env := newStreamEnvironment(stream)
	err := env.sendGuarded(context.Background(), &protocol.RuntimeMessage{}, nil)
	var notSent transportNotStartedError
	if err == nil || errors.As(err, &notSent) {
		t.Fatalf("started send classified unsent: %v", err)
	}
	if stream.calls != 1 {
		t.Fatalf("transport calls=%d", stream.calls)
	}
}

func TestTaskTransportErrorPreservesUncertainDelivery(t *testing.T) {
	f := newTaskWireFixture(t, false)
	env, exec, record, _ := beginWireExecution(t, f)
	_, epoch, _ := env.taskAuthority.Current()
	req := &protocol.ActionRequest{ActionId: "may-have-sent", WorldId: "world", EntityId: "actor", Capability: "follow_route"}
	if err := env.RegisterTaskAction(f.ctx, tool.RuntimeCallContext{Execution: exec, ObservedTask: &record, AuthorityEpoch: epoch}, req); err != nil {
		t.Fatal(err)
	}
	stream := &failedSendStream{captureStream: captureStream{sent: make(chan *protocol.RuntimeMessage, 1)}}
	env.stream = stream
	if err := env.sendActionRequest(f.ctx, req); err == nil {
		t.Fatal("transport failure swallowed")
	}
	r, err := f.service.Read(f.ctx, f.key, record.ID)
	if err != nil || r.Operations[1].Status != task.OperationStatusRegistered || !taskOperationAwaitingEvidence(task.Record{Operations: []task.Operation{r.Operations[1]}}) {
		t.Fatalf("possible delivery must still need evidence: %+v %v", r, err)
	}
}

type failedSendStream struct {
	captureStream
	calls int
}

func (s *failedSendStream) Send(*protocol.RuntimeMessage) error {
	s.calls++
	return errors.New("transport failed")
}
