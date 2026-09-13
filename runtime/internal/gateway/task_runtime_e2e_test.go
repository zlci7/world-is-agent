package gateway

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

type taskE2EModel struct {
	calls  atomic.Int32
	create bool
	mode   string
	taskID string
}

func (p *taskE2EModel) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	n := p.calls.Add(1)
	d := model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlContinue}}
	switch p.mode {
	case "player_cancel":
		if n == 1 {
			return model.Response{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlContinue}, ToolCalls: []model.ToolCall{{ID: "cancel", Name: "update_task", Arguments: map[string]any{"task_id": p.taskID, "intent": "cancel", "reason": "player request"}}}}}, nil
		}
		return model.Response{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}}, nil
	case "timeout":
		<-ctx.Done()
		return model.Response{}, ctx.Err()
	case "cancelled":
		return model.Response{}, context.Canceled
	case "no_progress":
		return model.Response{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}}, nil
	case "budget":
		return model.Response{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlContinue}, ToolCalls: []model.ToolCall{{ID: "one", Name: "follow_route", Arguments: map[string]any{}}, {ID: "two", Name: "follow_route", Arguments: map[string]any{}}}}}, nil
	}
	if !p.create {
		return model.Response{}, errors.New("model must not be needed to confirm evidence")
	}
	switch n {
	case 1:
		d.ToolCalls = []model.ToolCall{{ID: "proposal", Name: "inspect_contract", Arguments: map[string]any{}}}
	case 2:
		ref := ""
		for _, m := range req.Messages {
			for _, r := range m.ToolResults {
				if value, ok := r.Output["proposal_ref"].(string); ok {
					ref = value
				}
			}
		}
		d.ToolCalls = []model.ToolCall{{ID: "create", Name: "create_task", Arguments: map[string]any{"proposal_ref": ref, "instruction": "Inspect the agreed location later"}}}
	case 3:
		d.Control.Kind = model.ControlSettle
	case 4:
		if !strings.Contains(req.Messages[0].Content, "task_wake") {
			return model.Response{}, errors.New("missing task wake trigger")
		}
		d.ToolCalls = []model.ToolCall{{ID: "travel", Name: "follow_route", Arguments: map[string]any{}}}
	default:
		return model.Response{}, errors.New("unexpected repeated model turn")
	}
	return model.Response{Decision: d}, nil
}

type taskWireFixture struct {
	t        *testing.T
	ctx      context.Context
	server   *Server
	service  *task.Service
	client   protocol.GameAgentGateway_ConnectClient
	messages chan *protocol.RuntimeMessage
	head     task.Head
	key      session.AgentSessionKey
	model    *taskE2EModel
	dbPath   string
	store    *task.SQLiteStore
}

func newTaskWireFixture(t *testing.T, create bool, options ...func(*agent.Config)) *taskWireFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	dbPath := filepath.Join(t.TempDir(), "tasks.sqlite")
	store, err := task.OpenSQLiteStore(ctx, task.StoreOptions{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	service := task.NewService(store)
	p := &taskE2EModel{create: create}
	cfg := gatewayTestConfig(t)
	cfg.Task.Enabled = true
	for _, option := range options {
		option(&cfg)
	}
	loop := agent.NewLoop(p, nil, cfg, agent.WithMemoryStore(memory.NewInMemoryStore()))
	s := NewServer(loop, WithTaskService(service))
	if err := s.StartTaskDispatcher(ctx, task.DispatcherConfig{ScanInterval: time.Hour, BatchSize: 8, RetryMin: time.Millisecond, RetryMax: 5 * time.Millisecond}, nil, nil); err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	transport := grpc.NewServer()
	protocol.RegisterGameAgentGatewayServer(transport, s)
	go transport.Serve(lis)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	client, err := protocol.NewGameAgentGatewayClient(conn).Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f := &taskWireFixture{t: t, ctx: ctx, server: s, service: service, client: client, messages: make(chan *protocol.RuntimeMessage, 64), key: session.AgentSessionKey{GameID: "sim", WorldID: "world", EntityID: "actor"}, model: p, dbPath: dbPath, store: store}
	t.Cleanup(func() {
		s.StopTaskAdmission()
		cancel()
		_ = s.Close(context.Background())
		transport.Stop()
		conn.Close()
		lis.Close()
		store.Close()
	})
	go func() {
		defer close(f.messages)
		for {
			m, err := client.Recv()
			if err != nil {
				return
			}
			f.messages <- m
		}
	}()
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_Hello{Hello: &protocol.AdapterHello{GameId: "sim", SessionId: "wire", SupportedExtensions: []string{taskExtension}}}})
	if f.next().GetEnvironmentReady() == nil || f.next().GetCapabilityRequest() == nil {
		t.Fatal("bootstrap missing")
	}
	caps := []*protocol.Capability{}
	for _, name := range []string{"inspect_contract", "follow_route"} {
		caps = append(caps, &protocol.Capability{Name: name, Description: "Generic operation", InputSchemaJson: `{"type":"object","properties":{},"additionalProperties":false}`, ExecutionMode: protocol.ExecutionMode_EXECUTION_MODE_SYNC, ConcurrencyMode: protocol.CapabilityConcurrencyMode_CAPABILITY_CONCURRENCY_MODE_SEQUENTIAL})
	}
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_Capabilities{Capabilities: &protocol.CapabilityList{Capabilities: caps}}})
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_WorldBinding{WorldBinding: worldRequest()}})
	r := f.next().GetWorldBindingReady()
	if r.GetStatus() != "ready" {
		t.Fatalf("bind: %v", r)
	}
	f.head = task.Head{Binding: task.Binding{World: task.WorldKey{GameID: "sim", WorldID: "world"}, RunID: "run", Generation: r.Scope.ExecutionGeneration}, Clock: task.Clock{ID: "game", Tick: 10, Sequence: 1}, Status: "ready"}
	return f
}
func (f *taskWireFixture) send(m *protocol.AdapterMessage) {
	f.t.Helper()
	if err := f.client.Send(m); err != nil {
		f.t.Fatal(err)
	}
}
func (f *taskWireFixture) next() *protocol.RuntimeMessage {
	f.t.Helper()
	select {
	case m := <-f.messages:
		if m == nil {
			f.t.Fatal("stream closed")
		}
		return m
	case <-time.After(4 * time.Second):
		f.t.Fatal("runtime wire response timeout")
		return nil
	}
}
func (f *taskWireFixture) observe(m *protocol.RuntimeMessage, evidence ...*protocol.TaskEvidence) {
	f.t.Helper()
	if m.GetObserve() == nil {
		f.t.Fatalf("wanted ObserveRequest: %v", m)
	}
	f.send(&protocol.AdapterMessage{CorrelationId: m.MessageId, Payload: &protocol.AdapterMessage_Observation{Observation: &protocol.Observation{WorldId: "world", EntityId: "actor", TaskEvidence: evidence}}})
}
func (f *taskWireFixture) event(id string, evidence []*protocol.TaskEvidence, interaction *protocol.InteractionSource) {
	f.send(&protocol.AdapterMessage{MessageId: id, Payload: &protocol.AdapterMessage_Event{Event: &protocol.GameEvent{EventId: id, EventType: "generic_fact", WorldId: "world", TargetEntityId: "actor", Entities: worldRequest().Entities, TaskEvidence: evidence, InteractionSource: interaction}}})
}
func (f *taskWireFixture) result(req *protocol.ActionRequest, evidence []*protocol.TaskEvidence, proposal *protocol.TaskProposal) *protocol.ActionResult {
	r := &protocol.ActionResult{ActionId: req.ActionId, Status: protocol.ActionStatus_ACTION_STATUS_SUCCEEDED, TaskEvidence: evidence, TaskProposal: proposal}
	f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_ActionResult{ActionResult: r}})
	return r
}
func (f *taskWireFixture) awaitRecord(id string, pred func(task.Record) bool) task.Record {
	f.t.Helper()
	end := time.Now().Add(4 * time.Second)
	for {
		r, err := f.service.Read(f.ctx, f.key, id)
		if err != nil {
			f.t.Fatal(err)
		}
		if pred(r) {
			return r
		}
		if time.Now().After(end) {
			f.t.Fatalf("durable state did not settle: %+v", r)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTaskRuntimeEndToEnd(t *testing.T) {
	for _, outcome := range []string{"satisfied", "unsatisfied"} {
		t.Run(outcome, func(t *testing.T) {
			f := newTaskWireFixture(t, true)
			f.event("player", nil, &protocol.InteractionSource{SourceId: "player-input", Kind: "player", PlayerEntityId: "player", Scope: taskScopeToProtocol(f.head.Binding)})
			if f.next().GetEventAck().GetStatus() != protocol.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
				t.Fatal("player not accepted")
			}
			f.observe(f.next())
			proposalReq := f.next().GetAction()
			if proposalReq.GetCapability() != "inspect_contract" {
				t.Fatal(proposalReq)
			}
			proposal := &protocol.TaskProposal{Clock: worldRequest().Clock, WakeAt: 11, DeadlineAt: 100, ParticipantEntityIds: []string{"actor"}, EquivalenceKey: "agreement"}
			f.result(proposalReq, nil, proposal)
			turnA := f.next().GetTurnCompletion()
			if turnA.GetStatus() != protocol.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED {
				t.Fatal(turnA)
			}
			records, err := f.service.ListActive(f.ctx, f.key, 1)
			if err != nil || len(records) != 1 {
				t.Fatal(records, err)
			}
			f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_WorldClock{WorldClock: &protocol.WorldClockUpdate{Scope: taskScopeToProtocol(f.head.Binding), Clock: &protocol.WorldClock{ClockId: "game", NowTick: 11, Sequence: 2}}}})
			f.observe(f.next())
			action := f.next().GetAction()
			source := action.GetTaskSource()
			if source == nil {
				t.Fatal("task action was sent without registered task_source")
			}
			r, err := f.service.Read(f.ctx, f.key, records[0].ID)
			if err != nil || len(r.Operations) != 1 {
				t.Fatal(r, err)
			}
			op := r.Operations[0]
			if source.TaskId != r.ID || source.OperationId != op.ID || source.StartRevision != op.StartRevision || op.StartRevision != 2 || op.ActionID != action.ActionId || !proto.Equal(source.Scope, taskScopeToProtocol(f.head.Binding)) || !proto.Equal(source.TaskContract, proposal) || source.WakeId == "" || action.SourceTurnId == turnA.TurnId {
				t.Fatalf("untrusted operation correlation: %v %+v", action, op)
			}
			wait := int64(20)
			progress := &protocol.TaskEvidence{FactId: "progress", TaskId: r.ID, OperationId: op.ID, Scope: source.Scope, StartRevision: op.StartRevision, OccurredAt: 11, Outcome: "progress", WaitUntil: &wait}
			progressResult := f.result(action, []*protocol.TaskEvidence{progress}, nil)
			turnB := f.next().GetTurnCompletion()
			if turnB.GetStatus() != protocol.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED || turnB.TurnId == turnA.TurnId {
				t.Fatalf("background turn: %v", turnB)
			}
			f.awaitRecord(r.ID, func(r task.Record) bool {
				return r.State == task.StateWaiting && r.NextWakeAt != nil && *r.NextWakeAt == 20
			})
			terminal := proto.Clone(progress).(*protocol.TaskEvidence)
			terminal.FactId = "terminal"
			terminal.Outcome = outcome
			terminal.WaitUntil = nil
			f.event("receipt", []*protocol.TaskEvidence{terminal}, nil)
			ack := f.next().GetEventAck()
			if ack.GetStatus() != protocol.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
				t.Fatal(ack)
			}
			control := f.next().GetTaskControl()
			if control.GetOperationId() != op.ID || control.GetTaskId() != r.ID || control.GetRequestId() == "" || !proto.Equal(control.Scope, source.Scope) {
				t.Fatalf("release: %v", control)
			}
			f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_TaskControlResult{TaskControlResult: &protocol.TaskControlResult{RequestId: control.RequestId, TaskId: r.ID, OperationId: op.ID, Scope: source.Scope, Status: "released"}}})
			final := f.awaitRecord(r.ID, func(r task.Record) bool { return r.Result != nil && len(r.Cleanup) == 1 })
			want := task.StateSucceeded
			if outcome == "unsatisfied" {
				want = task.StateFailed
			}
			if final.State != want || len(final.Evidence) != 2 || final.Cleanup[0].Status != "released" {
				t.Fatalf("terminal: %+v", final)
			}
			f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_ActionResult{ActionResult: progressResult}})
			f.send(&protocol.AdapterMessage{CorrelationId: "old-observation", Payload: &protocol.AdapterMessage_Observation{Observation: &protocol.Observation{WorldId: "world", EntityId: "actor", TaskEvidence: []*protocol.TaskEvidence{terminal}}}})
			f.event("receipt-duplicate", []*protocol.TaskEvidence{terminal}, nil)
			if ack := f.next().GetEventAck(); ack == nil {
				t.Fatal("duplicate evidence started work")
			}
			after, _ := f.service.Read(f.ctx, f.key, r.ID)
			if after.Result.ID != final.Result.ID || len(after.Cleanup) != 1 || len(after.Operations) != 1 || f.model.calls.Load() != 4 {
				t.Fatalf("duplicate work: %+v model=%d", after, f.model.calls.Load())
			}
		})
	}
}
