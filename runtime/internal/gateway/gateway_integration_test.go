package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/llm/fake"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/trace"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"
)

type recordingGatewayProvider struct {
	mu       sync.Mutex
	requests []model.Request
}

func gatewayTestConfig(t *testing.T) agent.Config {
	t.Helper()

	config := agent.DefaultConfig()
	config.MemoryStore.Root = t.TempDir()
	return config
}

func (p *recordingGatewayProvider) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()

	return model.Response{
		Decision: model.ModelDecision{
			ToolCalls: []model.ToolCall{{
				ID:        "call_1",
				Name:      "speak",
				Arguments: map[string]any{"text": "gateway memory line"},
			}},
			Control: model.ControlDirective{Kind: model.ControlSettle},
		},
	}, nil
}

func (p *recordingGatewayProvider) Requests() []model.Request {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]model.Request, len(p.requests))
	copy(out, p.requests)
	return out
}

type scriptedGatewayProvider struct {
	mu        sync.Mutex
	requests  []model.Request
	responses []model.Response
}

func (p *scriptedGatewayProvider) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.requests = append(p.requests, req)
	if len(p.responses) == 0 {
		return model.Response{
			Decision: model.ModelDecision{
				Control: model.ControlDirective{Kind: model.ControlSettle},
			},
		}, nil
	}
	resp := p.responses[0]
	p.responses = p.responses[1:]
	return resp, nil
}

func (p *scriptedGatewayProvider) Requests() []model.Request {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]model.Request, len(p.requests))
	copy(out, p.requests)
	return out
}

type sameNameToolGatewayProvider struct {
	mu       sync.Mutex
	requests []model.Request
}

func (p *sameNameToolGatewayProvider) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()

	if requestMessagesContain(req.Messages, "action_succeeded") {
		return model.Response{
			Decision: model.ModelDecision{
				Control: model.ControlDirective{Kind: model.ControlSettle},
			},
		}, nil
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "shared_action" {
		return model.Response{}, fmt.Errorf("unexpected tools for shared action request: %+v", req.Tools)
	}
	schema := req.Tools[0].InputSchema
	switch {
	case strings.Contains(schema, `"text"`):
		return model.Response{
			Decision: model.ModelDecision{
				ToolCalls: []model.ToolCall{{
					ID:        "call_shared_text",
					Name:      "shared_action",
					Arguments: map[string]any{"text": "alpha"},
				}},
				Control: model.ControlDirective{Kind: model.ControlContinue},
			},
		}, nil
	case strings.Contains(schema, `"value"`):
		return model.Response{
			Decision: model.ModelDecision{
				ToolCalls: []model.ToolCall{{
					ID:        "call_shared_value",
					Name:      "shared_action",
					Arguments: map[string]any{"value": "beta"},
				}},
				Control: model.ControlDirective{Kind: model.ControlContinue},
			},
		}, nil
	default:
		return model.Response{}, fmt.Errorf("unexpected shared_action schema: %s", schema)
	}
}

func (p *sameNameToolGatewayProvider) Requests() []model.Request {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]model.Request, len(p.requests))
	copy(out, p.requests)
	return out
}

type recordingGatewayTraceRecorder struct {
	mu     sync.Mutex
	events []trace.Event
}

func (r *recordingGatewayTraceRecorder) Record(event trace.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recordingGatewayTraceRecorder) Close(ctx context.Context) error {
	return nil
}

func (r *recordingGatewayTraceRecorder) Count(eventName trace.EventName) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	count := 0
	for _, event := range r.events {
		if event.Event == eventName {
			count++
		}
	}
	return count
}

func (r *recordingGatewayTraceRecorder) Events() []trace.Event {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]trace.Event, len(r.events))
	copy(out, r.events)
	return out
}

func TestConnectRunsOneTurnWithFakeAdapter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	loop := agent.NewLoop(fake.NewProvider(), trace.NoopRecorder{}, gatewayTestConfig(t))
	protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, NewServer(loop))

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		select {
		case <-serverErrCh:
		case <-time.After(time.Second):
			t.Fatal("gRPC server did not stop")
		}
	})

	conn, err := grpc.DialContext(
		ctx,
		"bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("DialContext returned error: %v", err)
	}
	defer conn.Close()

	client := protocolv1alpha2.NewGameAgentGatewayClient(conn)
	stream, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}

	timeline(t, "adapter -> AdapterHello")
	if err := stream.Send(adapterHelloMessage()); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	timeline(t, "runtime -> EnvironmentReady")
	ready := recvRuntimeMessage(t, stream)
	if got := ready.GetEnvironmentReady(); got == nil || got.SessionId != "session:test" {
		t.Fatalf("environment ready = %+v, want environment session:test", got)
	}

	timeline(t, "runtime -> CapabilityRequest")
	capabilityRequest := recvRuntimeMessage(t, stream)
	if capabilityRequest.GetCapabilityRequest() == nil {
		t.Fatalf("expected capability request, got %+v", capabilityRequest.Payload)
	}

	timeline(t, "adapter -> CapabilityList(speak)")
	if err := stream.Send(capabilityListMessage(capabilityRequest.MessageId)); err != nil {
		t.Fatalf("send capability list: %v", err)
	}

	timeline(t, "adapter -> GameEvent(player_interacted_with_npc)")
	if err := stream.Send(npcInteractionEventMessage()); err != nil {
		t.Fatalf("send game event: %v", err)
	}

	timeline(t, "runtime -> EventAck(ACCEPTED)")
	eventAck := recvRuntimeMessage(t, stream)
	ack := eventAck.GetEventAck()
	if ack == nil {
		t.Fatalf("expected event ack, got %+v", eventAck.Payload)
	}
	if eventAck.CorrelationId != "event_msg_1" {
		t.Fatalf("event ack correlation id = %q, want %q", eventAck.CorrelationId, "event_msg_1")
	}
	if ack.EventId != "event_1" {
		t.Fatalf("event ack id = %q, want %q", ack.EventId, "event_1")
	}
	if ack.Status != protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("event ack status = %v, want accepted", ack.Status)
	}

	timeline(t, "runtime -> ObserveRequest(npc:Linus)")
	observeRequest := recvRuntimeMessage(t, stream)
	observe := observeRequest.GetObserve()
	if observe == nil {
		t.Fatalf("expected observe request, got %+v", observeRequest.Payload)
	}
	if observe.EntityId != "npc:Linus" {
		t.Fatalf("observe entity id = %q, want %q", observe.EntityId, "npc:Linus")
	}
	if observe.WorldId != "world:test" {
		t.Fatalf("observe world id = %q, want %q", observe.WorldId, "world:test")
	}

	timeline(t, "adapter -> Observation(npc:Linus)")
	if err := stream.Send(observationMessage(observeRequest.MessageId)); err != nil {
		t.Fatalf("send observation: %v", err)
	}

	timeline(t, "runtime -> ActionRequest(speak)")
	actionRequest := recvRuntimeMessage(t, stream)
	action := actionRequest.GetAction()
	if action == nil {
		t.Fatalf("expected action request, got %+v", actionRequest.Payload)
	}
	if action.EntityId != "npc:Linus" {
		t.Fatalf("action entity id = %q, want %q", action.EntityId, "npc:Linus")
	}
	if action.WorldId != "world:test" {
		t.Fatalf("action world id = %q, want %q", action.WorldId, "world:test")
	}
	if action.Capability != "speak" {
		t.Fatalf("action capability = %q, want %q", action.Capability, "speak")
	}
	if action.ActionId == "" {
		t.Fatal("expected action id to be set")
	}
	if action.SourceEventId != "event_1" {
		t.Fatalf("action source_event_id = %q, want event_1", action.SourceEventId)
	}
	if action.SourceTurnId == "" {
		t.Fatal("action source_turn_id is empty")
	}
	text := action.Arguments.GetFields()["text"].GetStringValue()
	if text == "" {
		t.Fatal("expected speak action text to be set")
	}

	timeline(t, "adapter -> ActionResult(SUCCEEDED)")
	if err := stream.Send(actionResultMessage(action.ActionId)); err != nil {
		t.Fatalf("send action result: %v", err)
	}
	completion := recvTurnCompletion(t, stream, "event_1", "npc:Linus", protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED)
	if completion.TurnId != action.SourceTurnId {
		t.Fatalf("completion turn_id = %q, want action source_turn_id %q", completion.TurnId, action.SourceTurnId)
	}
	timeline(t, "adapter -> CloseSend")
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}

	timeline(t, "runtime -> EOF")
	_, err = stream.Recv()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("final recv error = %v, want EOF", err)
	}
}

func TestConnectRejectsIllegalCapabilityListEntityScope(t *testing.T) {
	tests := []struct {
		name     string
		entityID string
		wantErr  string
	}{
		{
			name:     "entity scoped capability list",
			entityID: "npc:Abigail",
			wantErr:  "unsupported entity scope",
		},
		{
			name:     "whitespace entity scope",
			entityID: " \t ",
			wantErr:  "invalid entity scope",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			listener := bufconn.Listen(1024 * 1024)
			grpcServer := grpc.NewServer()
			provider := &scriptedGatewayProvider{}
			loop := agent.NewLoop(provider, trace.NoopRecorder{}, gatewayTestConfig(t))
			protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, NewServer(loop))
			startGatewayServer(t, grpcServer, listener)

			conn := dialGateway(t, ctx, listener)
			defer conn.Close()

			client := protocolv1alpha2.NewGameAgentGatewayClient(conn)
			stream, err := client.Connect(ctx)
			if err != nil {
				t.Fatalf("Connect returned error: %v", err)
			}
			if err := stream.Send(adapterHelloMessage()); err != nil {
				t.Fatalf("send hello: %v", err)
			}
			_ = recvRuntimeMessage(t, stream)
			capabilityRequest := recvRuntimeMessage(t, stream)

			if err := stream.Send(capabilityListWithEntityIDMessage(capabilityRequest.MessageId, tt.entityID)); err != nil {
				t.Fatalf("send capability list: %v", err)
			}
			_, err = stream.Recv()
			if err == nil {
				t.Fatal("stream.Recv returned nil error, want bootstrap failure")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("stream.Recv error = %v, want containing %q", err, tt.wantErr)
			}
			if got := len(provider.Requests()); got != 0 {
				t.Fatalf("provider request count = %d, want 0", got)
			}
		})
	}
}

func TestConnectAcceptsPaddedEventIdentityThroughContextBuild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	provider := &scriptedGatewayProvider{}
	loop := agent.NewLoop(provider, trace.NoopRecorder{}, gatewayTestConfig(t))
	protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, NewServer(loop))
	startGatewayServer(t, grpcServer, listener)

	conn := dialGateway(t, ctx, listener)
	defer conn.Close()

	client := protocolv1alpha2.NewGameAgentGatewayClient(conn)
	stream := connectReadyStream(t, ctx, client, "session:padded-event")

	eventMessage := npcInteractionEventMessage()
	event := eventMessage.GetEvent()
	event.WorldId = " world:test "
	event.TargetEntityId = "\tnpc:Linus\n"
	if err := stream.Send(eventMessage); err != nil {
		t.Fatalf("send padded game event: %v", err)
	}

	ack := recvRuntimeMessage(t, stream).GetEventAck()
	if ack == nil || ack.Status != protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("ack = %+v, want accepted", ack)
	}

	observeMessage := recvRuntimeMessage(t, stream)
	observe := observeMessage.GetObserve()
	if observe == nil {
		t.Fatalf("expected observe request, got %+v", observeMessage.Payload)
	}
	if observe.WorldId != "world:test" {
		t.Fatalf("observe world_id = %q, want trimmed world:test", observe.WorldId)
	}
	if observe.EntityId != "npc:Linus" {
		t.Fatalf("observe entity_id = %q, want trimmed npc:Linus", observe.EntityId)
	}

	if err := stream.Send(observationMessage(observeMessage.MessageId)); err != nil {
		t.Fatalf("send observation: %v", err)
	}
	recvTurnCompletion(t, stream, "event_1", "npc:Linus", protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED)
	waitForGatewayProviderRequestCount(t, provider, 1)

	requests := provider.Requests()
	if got := len(requests); got != 1 {
		t.Fatalf("provider request count = %d, want 1", got)
	}
	prompt := requests[0].Messages[0].Content
	for _, want := range []string{`"world_id": "world:test"`, `"target_entity_id": "npc:Linus"`} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, unwanted := range []string{`"world_id": " world:test "`, `"target_entity_id": "\tnpc:Linus\n"`} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("prompt leaked padded event identity %q:\n%s", unwanted, prompt)
		}
	}

	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}
}

func TestConnectForwardsDynamicEmoteToolCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	loop := agent.NewLoop(fake.NewProvider(), trace.NoopRecorder{}, gatewayTestConfig(t))
	protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, NewServer(loop))

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		select {
		case <-serverErrCh:
		case <-time.After(time.Second):
			t.Fatal("gRPC server did not stop")
		}
	})

	conn, err := grpc.DialContext(
		ctx,
		"bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("DialContext returned error: %v", err)
	}
	defer conn.Close()

	client := protocolv1alpha2.NewGameAgentGatewayClient(conn)
	stream, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}

	timeline(t, "adapter -> AdapterHello")
	if err := stream.Send(adapterHelloMessage()); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	timeline(t, "runtime -> EnvironmentReady")
	ready := recvRuntimeMessage(t, stream)
	if got := ready.GetEnvironmentReady(); got == nil || got.SessionId != "session:test" {
		t.Fatalf("environment ready = %+v, want environment session:test", got)
	}

	timeline(t, "runtime -> CapabilityRequest")
	capabilityRequest := recvRuntimeMessage(t, stream)
	if capabilityRequest.GetCapabilityRequest() == nil {
		t.Fatalf("expected capability request, got %+v", capabilityRequest.Payload)
	}

	timeline(t, "adapter -> CapabilityList(speak, emote)")
	if err := stream.Send(capabilityListWithEmoteMessage(capabilityRequest.MessageId)); err != nil {
		t.Fatalf("send capability list: %v", err)
	}

	timeline(t, "adapter -> GameEvent(player_interacted_with_npc)")
	if err := stream.Send(npcInteractionEventMessage()); err != nil {
		t.Fatalf("send game event: %v", err)
	}

	timeline(t, "runtime -> EventAck(ACCEPTED)")
	eventAck := recvRuntimeMessage(t, stream)
	if ack := eventAck.GetEventAck(); ack == nil || ack.EventId != "event_1" {
		t.Fatalf("expected event ack for event_1, got %+v", eventAck.Payload)
	}

	timeline(t, "runtime -> ObserveRequest(npc:Linus)")
	observeRequest := recvRuntimeMessage(t, stream)
	observe := observeRequest.GetObserve()
	if observe == nil {
		t.Fatalf("expected observe request, got %+v", observeRequest.Payload)
	}
	if observe.EntityId != "npc:Linus" {
		t.Fatalf("observe entity id = %q, want %q", observe.EntityId, "npc:Linus")
	}
	if observe.WorldId != "world:test" {
		t.Fatalf("observe world id = %q, want %q", observe.WorldId, "world:test")
	}

	timeline(t, "adapter -> Observation(npc:Linus)")
	if err := stream.Send(observationMessage(observeRequest.MessageId)); err != nil {
		t.Fatalf("send observation: %v", err)
	}

	timeline(t, "runtime -> ActionRequest(emote)")
	actionRequest := recvRuntimeMessage(t, stream)
	action := actionRequest.GetAction()
	if action == nil {
		t.Fatalf("expected action request, got %+v", actionRequest.Payload)
	}
	if action.EntityId != "npc:Linus" {
		t.Fatalf("action entity id = %q, want %q", action.EntityId, "npc:Linus")
	}
	if action.WorldId != "world:test" {
		t.Fatalf("action world id = %q, want %q", action.WorldId, "world:test")
	}
	if action.Capability != "emote" {
		t.Fatalf("action capability = %q, want %q", action.Capability, "emote")
	}
	if got := action.Arguments.GetFields()["emote"].GetStringValue(); got != "happy" {
		t.Fatalf("action emote = %q, want %q", got, "happy")
	}
	if action.ActionId == "" {
		t.Fatal("expected action id to be set")
	}
	if action.SourceEventId != "event_1" {
		t.Fatalf("action source_event_id = %q, want event_1", action.SourceEventId)
	}
	if action.SourceTurnId == "" {
		t.Fatal("action source_turn_id is empty")
	}

	timeline(t, "adapter -> ActionResult(SUCCEEDED)")
	if err := stream.Send(actionResultMessage(action.ActionId)); err != nil {
		t.Fatalf("send action result: %v", err)
	}
	completion := recvTurnCompletion(t, stream, "event_1", "npc:Linus", protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED)
	if completion.TurnId != action.SourceTurnId {
		t.Fatalf("completion turn_id = %q, want action source_turn_id %q", completion.TurnId, action.SourceTurnId)
	}
	timeline(t, "adapter -> CloseSend")
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}

	timeline(t, "runtime -> EOF")
	_, err = stream.Recv()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("final recv error = %v, want EOF", err)
	}
}

func TestConnectSettlesAfterPresentDialogueAndPreservesSourceCorrelation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	provider := &scriptedGatewayProvider{responses: []model.Response{{
		Decision: model.ModelDecision{
			ToolCalls: []model.ToolCall{{
				ID:        "call_present",
				Name:      "present_dialogue",
				Arguments: map[string]any{"text": "Want to walk to the bridge?", "reply_options": []any{"Yes", "Not now"}},
			}},
			Control: model.ControlDirective{Kind: model.ControlContinue},
		},
	}}}
	recorder := &recordingGatewayTraceRecorder{}
	loop := agent.NewLoop(provider, recorder, gatewayTestConfig(t))
	protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, NewServer(loop))
	startGatewayServer(t, grpcServer, listener)

	conn := dialGateway(t, ctx, listener)
	defer conn.Close()

	client := protocolv1alpha2.NewGameAgentGatewayClient(conn)
	stream := connectReadyStreamWithCapabilities(t, ctx, client, "session:test-present-dialogue", capabilityListWithPresentDialogueMessage)

	if err := stream.Send(npcInteractionEventMessage()); err != nil {
		t.Fatalf("send game event: %v", err)
	}
	if ack := recvRuntimeMessage(t, stream).GetEventAck(); ack == nil || ack.Status != protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("ack = %+v, want accepted", ack)
	}
	observeMessage := recvRuntimeMessage(t, stream)
	if err := stream.Send(observationMessage(observeMessage.MessageId)); err != nil {
		t.Fatalf("send observation: %v", err)
	}

	action := recvRuntimeMessage(t, stream).GetAction()
	if action == nil || action.Capability != "present_dialogue" {
		t.Fatalf("action = %+v, want present_dialogue", action)
	}
	if action.SourceEventId != "event_1" {
		t.Fatalf("action source_event_id = %q, want event_1", action.SourceEventId)
	}
	if action.SourceTurnId == "" {
		t.Fatal("action source_turn_id is empty")
	}

	if err := stream.Send(actionResultMessage(action.ActionId)); err != nil {
		t.Fatalf("send present_dialogue result: %v", err)
	}
	completion := recvTurnCompletion(t, stream, "event_1", "npc:Linus", protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED)
	if completion.TurnId != action.SourceTurnId {
		t.Fatalf("completion turn_id = %q, want action source_turn_id %q", completion.TurnId, action.SourceTurnId)
	}
	if got := len(provider.Requests()); got != 1 {
		t.Fatalf("provider request count = %d, want settle_after_success without next request", got)
	}
	waitForTraceEventCount(t, recorder, trace.EventTurnCompleted, 1)
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}
}

func TestConnectRunsAsyncMoveToSuspendResumeTurnLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	provider := &scriptedGatewayProvider{responses: []model.Response{
		{
			Decision: model.ModelDecision{
				ToolCalls: []model.ToolCall{{
					ID:   "call_move",
					Name: "move_to",
					Arguments: map[string]any{
						"location": "Mountain",
						"tile":     map[string]any{"x": 42, "y": 18},
					},
				}},
				Control: model.ControlDirective{Kind: model.ControlContinue},
			},
		},
		{
			Decision: model.ModelDecision{
				Control: model.ControlDirective{Kind: model.ControlSettle},
			},
		},
	}}
	recorder := &recordingGatewayTraceRecorder{}
	loop := agent.NewLoop(provider, recorder, gatewayTestConfig(t))
	protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, NewServer(loop))
	startGatewayServer(t, grpcServer, listener)

	conn := dialGateway(t, ctx, listener)
	defer conn.Close()

	client := protocolv1alpha2.NewGameAgentGatewayClient(conn)
	stream := connectReadyStreamWithCapabilities(t, ctx, client, "session:test-move-to", capabilityListWithMoveToMessage)

	if err := stream.Send(npcInteractionEventMessage()); err != nil {
		t.Fatalf("send game event: %v", err)
	}
	if ack := recvRuntimeMessage(t, stream).GetEventAck(); ack == nil || ack.Status != protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("ack = %+v, want accepted", ack)
	}
	observeMessage := recvRuntimeMessage(t, stream)
	if err := stream.Send(observationMessage(observeMessage.MessageId)); err != nil {
		t.Fatalf("send initial observation: %v", err)
	}

	action := recvRuntimeMessage(t, stream).GetAction()
	if action == nil || action.Capability != "move_to" {
		t.Fatalf("action = %+v, want move_to", action)
	}
	if action.SourceEventId != "event_1" {
		t.Fatalf("action source_event_id = %q, want event_1", action.SourceEventId)
	}
	if action.SourceTurnId == "" {
		t.Fatal("action source_turn_id is empty")
	}
	if got := action.Arguments.GetFields()["location"].GetStringValue(); got != "Mountain" {
		t.Fatalf("move_to location = %q, want Mountain", got)
	}
	if got := action.Arguments.GetFields()["tile"].GetStructValue().GetFields()["x"].GetNumberValue(); got != 42 {
		t.Fatalf("move_to tile.x = %v, want 42", got)
	}

	if err := stream.Send(actionStatusUpdateMessage(action.ActionId, protocolv1alpha2.ActionStatus_ACTION_STATUS_ACCEPTED)); err != nil {
		t.Fatalf("send accepted status: %v", err)
	}
	if err := stream.Send(actionStatusUpdateMessage(action.ActionId, protocolv1alpha2.ActionStatus_ACTION_STATUS_RUNNING)); err != nil {
		t.Fatalf("send running status: %v", err)
	}
	if err := stream.Send(actionResultMessage(action.ActionId)); err != nil {
		t.Fatalf("send move_to action result: %v", err)
	}

	resumeObserve := recvRuntimeMessage(t, stream)
	if observe := resumeObserve.GetObserve(); observe == nil || observe.EntityId != "npc:Linus" || observe.WorldId != "world:test" {
		t.Fatalf("resume observe = %+v, want npc:Linus/world:test", observe)
	}
	if err := stream.Send(observationMessage(resumeObserve.MessageId)); err != nil {
		t.Fatalf("send resumed observation: %v", err)
	}

	waitForGatewayProviderRequestCount(t, provider, 2)
	completion := recvTurnCompletion(t, stream, "event_1", "npc:Linus", protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED)
	if completion.TurnId != action.SourceTurnId {
		t.Fatalf("completion turn_id = %q, want action source_turn_id %q", completion.TurnId, action.SourceTurnId)
	}
	if !requestMessagesContain(provider.Requests()[1].Messages, "action_succeeded") {
		t.Fatalf("resumed request missing async tool result transcript: %+v", provider.Requests()[1].Messages)
	}
	waitForTraceEventCount(t, recorder, trace.EventActionStatusUpdateReceived, 1)
	waitForTraceEventCount(t, recorder, trace.EventTurnSuspended, 1)
	waitForTraceEventCount(t, recorder, trace.EventTurnResumed, 1)
	waitForTraceEventCount(t, recorder, trace.EventTurnCompleted, 1)
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}
}

func TestConnectRunsSingleStepBatchWithTwoActionsAndSettle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	provider := &scriptedGatewayProvider{responses: []model.Response{{
		Decision: model.ModelDecision{
			ToolCalls: []model.ToolCall{
				{ID: "call_1", Name: "speak", Arguments: map[string]any{"text": "hello"}},
				{ID: "call_2", Name: "emote", Arguments: map[string]any{"emote": "happy"}},
			},
			Control: model.ControlDirective{Kind: model.ControlSettle},
		},
	}}}
	recorder := &recordingGatewayTraceRecorder{}
	loop := agent.NewLoop(provider, recorder, gatewayTestConfig(t))
	protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, NewServer(loop))
	startGatewayServer(t, grpcServer, listener)

	conn := dialGateway(t, ctx, listener)
	defer conn.Close()

	client := protocolv1alpha2.NewGameAgentGatewayClient(conn)
	stream := connectReadyStreamWithCapabilities(t, ctx, client, "session:test-batch", capabilityListWithEmoteMessage)

	if err := stream.Send(npcInteractionEventMessage()); err != nil {
		t.Fatalf("send game event: %v", err)
	}
	if ack := recvRuntimeMessage(t, stream).GetEventAck(); ack == nil || ack.Status != protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("ack = %+v, want accepted", ack)
	}
	observeMessage := recvRuntimeMessage(t, stream)
	if err := stream.Send(observationMessage(observeMessage.MessageId)); err != nil {
		t.Fatalf("send observation: %v", err)
	}

	first := recvRuntimeMessage(t, stream).GetAction()
	if first == nil || first.Capability != "speak" {
		t.Fatalf("first action = %+v, want speak", first)
	}
	if err := stream.Send(actionResultMessage(first.ActionId)); err != nil {
		t.Fatalf("send first action result: %v", err)
	}
	second := recvRuntimeMessage(t, stream).GetAction()
	if second == nil || second.Capability != "emote" {
		t.Fatalf("second action = %+v, want emote", second)
	}
	if err := stream.Send(actionResultMessage(second.ActionId)); err != nil {
		t.Fatalf("send second action result: %v", err)
	}
	recvTurnCompletion(t, stream, "event_1", "npc:Linus", protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED)

	waitForTraceEventCount(t, recorder, trace.EventTurnCompleted, 1)
	if got := len(provider.Requests()); got != 1 {
		t.Fatalf("provider request count = %d, want 1", got)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}
}

func TestConnectRunsParallelSafeBatchAndOrdersTranscriptByToolCallOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	provider := &scriptedGatewayProvider{responses: []model.Response{
		{Decision: model.ModelDecision{
			ToolCalls: []model.ToolCall{
				{ID: "call_a", Name: "sense", Arguments: map[string]any{"label": "first"}},
				{ID: "call_b", Name: "sense", Arguments: map[string]any{"label": "second"}},
			},
			Control: model.ControlDirective{Kind: model.ControlContinue},
		}},
		{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}},
	}}
	recorder := &recordingGatewayTraceRecorder{}
	loop := agent.NewLoop(provider, recorder, gatewayTestConfig(t))
	protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, NewServer(loop))
	startGatewayServer(t, grpcServer, listener)

	conn := dialGateway(t, ctx, listener)
	defer conn.Close()

	client := protocolv1alpha2.NewGameAgentGatewayClient(conn)
	stream := connectReadyStreamWithCapabilities(t, ctx, client, "session:test-parallel", capabilityListWithParallelSenseMessage)

	if err := stream.Send(npcInteractionEventMessage()); err != nil {
		t.Fatalf("send game event: %v", err)
	}
	if ack := recvRuntimeMessage(t, stream).GetEventAck(); ack == nil || ack.Status != protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("ack = %+v, want accepted", ack)
	}
	observeMessage := recvRuntimeMessage(t, stream)
	if err := stream.Send(observationMessage(observeMessage.MessageId)); err != nil {
		t.Fatalf("send observation: %v", err)
	}

	firstAction := recvRuntimeMessage(t, stream).GetAction()
	secondAction := recvRuntimeMessage(t, stream).GetAction()
	if firstAction == nil || secondAction == nil {
		t.Fatalf("actions = %+v, %+v; want two parallel actions", firstAction, secondAction)
	}
	if err := stream.Send(actionResultMessage(secondAction.ActionId)); err != nil {
		t.Fatalf("send second action result first: %v", err)
	}
	if err := stream.Send(actionResultMessage(firstAction.ActionId)); err != nil {
		t.Fatalf("send first action result second: %v", err)
	}

	waitForGatewayProviderRequestCount(t, provider, 2)
	recvTurnCompletion(t, stream, "event_1", "npc:Linus", protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED)
	requests := provider.Requests()
	resultTranscript := requests[1].Messages[2].Content
	if strings.Index(resultTranscript, "call_a") > strings.Index(resultTranscript, "call_b") {
		t.Fatalf("tool result transcript not ordered by ToolCall order:\n%s", resultTranscript)
	}
	waitForTraceEventCount(t, recorder, trace.EventTurnCompleted, 1)
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}
}

func TestConnectRunsMultiStepForNonStardewTriggerWithDefinitionID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	provider := &scriptedGatewayProvider{responses: []model.Response{
		{Decision: model.ModelDecision{
			ToolCalls: []model.ToolCall{{ID: "call_1", Name: "speak", Arguments: map[string]any{"text": "growl"}}},
			Control:   model.ControlDirective{Kind: model.ControlContinue},
		}},
		{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}},
	}}
	recorder := &recordingGatewayTraceRecorder{}
	loop := agent.NewLoop(provider, recorder, gatewayTestConfig(t))
	protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, NewServer(loop))
	startGatewayServer(t, grpcServer, listener)

	conn := dialGateway(t, ctx, listener)
	defer conn.Close()

	client := protocolv1alpha2.NewGameAgentGatewayClient(conn)
	stream := connectReadyStream(t, ctx, client, "session:test-survival")

	if err := stream.Send(gameEventMessageForEntityDefinition(1, "damage_received", "creature:alpha", "creature/generic", "creature", "Alpha")); err != nil {
		t.Fatalf("send non-stardew event: %v", err)
	}
	if ack := recvRuntimeMessage(t, stream).GetEventAck(); ack == nil || ack.Status != protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("ack = %+v, want accepted", ack)
	}
	observeMessage := recvRuntimeMessage(t, stream)
	if err := stream.Send(observationMessageForEntity(observeMessage.MessageId, "creature:alpha", "creature", "Alpha")); err != nil {
		t.Fatalf("send observation: %v", err)
	}
	action := recvRuntimeMessage(t, stream).GetAction()
	if action == nil || action.EntityId != "creature:alpha" {
		t.Fatalf("action = %+v, want creature:alpha", action)
	}
	if err := stream.Send(actionResultMessage(action.ActionId)); err != nil {
		t.Fatalf("send action result: %v", err)
	}

	waitForGatewayProviderRequestCount(t, provider, 2)
	recvTurnCompletion(t, stream, "event_1", "creature:alpha", protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED)
	secondPrompt := provider.Requests()[1].Messages[0].Content
	if !strings.Contains(secondPrompt, "definition_id: creature/generic") {
		t.Fatalf("prompt missing target definition_id:\n%s", secondPrompt)
	}
	if !strings.Contains(secondPrompt, "entity_id: creature:alpha") {
		t.Fatalf("prompt missing target entity_id:\n%s", secondPrompt)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}
}

func TestConnectRetriesAfterRejectedActionResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	provider := &scriptedGatewayProvider{responses: []model.Response{
		{Decision: model.ModelDecision{
			ToolCalls: []model.ToolCall{{ID: "call_1", Name: "speak", Arguments: map[string]any{"text": "blocked"}}},
			Control:   model.ControlDirective{Kind: model.ControlSettle},
		}},
		{Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}}},
	}}
	recorder := &recordingGatewayTraceRecorder{}
	loop := agent.NewLoop(provider, recorder, gatewayTestConfig(t))
	protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, NewServer(loop))
	startGatewayServer(t, grpcServer, listener)

	conn := dialGateway(t, ctx, listener)
	defer conn.Close()

	client := protocolv1alpha2.NewGameAgentGatewayClient(conn)
	stream := connectReadyStream(t, ctx, client, "session:test-retry")

	if err := stream.Send(npcInteractionEventMessage()); err != nil {
		t.Fatalf("send game event: %v", err)
	}
	if ack := recvRuntimeMessage(t, stream).GetEventAck(); ack == nil || ack.Status != protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("ack = %+v, want accepted", ack)
	}
	observeMessage := recvRuntimeMessage(t, stream)
	if err := stream.Send(observationMessage(observeMessage.MessageId)); err != nil {
		t.Fatalf("send observation: %v", err)
	}
	action := recvRuntimeMessage(t, stream).GetAction()
	if action == nil {
		t.Fatal("expected action")
	}
	if err := stream.Send(actionResultStatusMessage(action.ActionId, protocolv1alpha2.ActionStatus_ACTION_STATUS_REJECTED)); err != nil {
		t.Fatalf("send rejected action result: %v", err)
	}

	waitForGatewayProviderRequestCount(t, provider, 2)
	recvTurnCompletion(t, stream, "event_1", "npc:Linus", protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED)
	waitForTraceEventCount(t, recorder, trace.EventTurnCompleted, 1)
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}
}

func TestConnectMaxStepsExceededProducesSingleTerminalTrace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	provider := &scriptedGatewayProvider{responses: []model.Response{
		{Decision: model.ModelDecision{
			ToolCalls: []model.ToolCall{{ID: "call_1", Name: "speak", Arguments: map[string]any{"text": "loop"}}},
			Control:   model.ControlDirective{Kind: model.ControlContinue},
		}},
	}}
	recorder := &recordingGatewayTraceRecorder{}
	config := gatewayTestConfig(t)
	config.MaxSteps = 1
	loop := agent.NewLoop(provider, recorder, config)
	protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, NewServer(loop))
	startGatewayServer(t, grpcServer, listener)

	conn := dialGateway(t, ctx, listener)
	defer conn.Close()

	client := protocolv1alpha2.NewGameAgentGatewayClient(conn)
	stream := connectReadyStream(t, ctx, client, "session:test-max-steps")

	if err := stream.Send(npcInteractionEventMessage()); err != nil {
		t.Fatalf("send game event: %v", err)
	}
	if ack := recvRuntimeMessage(t, stream).GetEventAck(); ack == nil || ack.Status != protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("ack = %+v, want accepted", ack)
	}
	observeMessage := recvRuntimeMessage(t, stream)
	if err := stream.Send(observationMessage(observeMessage.MessageId)); err != nil {
		t.Fatalf("send observation: %v", err)
	}
	action := recvRuntimeMessage(t, stream).GetAction()
	if action == nil {
		t.Fatal("expected action")
	}
	if err := stream.Send(actionResultMessage(action.ActionId)); err != nil {
		t.Fatalf("send action result: %v", err)
	}

	recvTurnCompletion(t, stream, "event_1", "npc:Linus", protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_FAILED)
	waitForTraceEventCount(t, recorder, trace.EventTurnFailed, 1)
	events := recorder.Events()
	terminalCount := 0
	var terminal trace.Event
	for _, event := range events {
		if event.Event == trace.EventTurnCompleted || event.Event == trace.EventTurnFailed {
			terminalCount++
			terminal = event
		}
	}
	if terminalCount != 1 || terminal.Event != trace.EventTurnFailed || terminal.Reason != "max_steps_exceeded" {
		t.Fatalf("terminal trace = %+v count=%d; want single max_steps_exceeded failure", terminal, terminalCount)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}
}

func TestConnectRejectsGameEventWhenEventQueueIsFull(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	loop := agent.NewLoop(fake.NewProvider(), trace.NoopRecorder{}, gatewayTestConfig(t))
	protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, NewServer(loop))

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		select {
		case <-serverErrCh:
		case <-time.After(time.Second):
			t.Fatal("gRPC server did not stop")
		}
	})

	conn, err := grpc.DialContext(
		ctx,
		"bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("DialContext returned error: %v", err)
	}
	defer conn.Close()

	client := protocolv1alpha2.NewGameAgentGatewayClient(conn)
	stream, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}

	if err := stream.Send(adapterHelloMessage()); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	_ = recvRuntimeMessage(t, stream)
	capabilityRequest := recvRuntimeMessage(t, stream)
	if err := stream.Send(capabilityListMessage(capabilityRequest.MessageId)); err != nil {
		t.Fatalf("send capability list: %v", err)
	}

	const eventCount = 25
	for i := 0; i < eventCount; i++ {
		if err := stream.Send(npcInteractionEventMessageWithIDs(i)); err != nil {
			t.Fatalf("send game event %d: %v", i, err)
		}
	}

	ackCount := 0
	rejectedCount := 0
	for ackCount < eventCount {
		msg := recvRuntimeMessage(t, stream)
		ack := msg.GetEventAck()
		if ack == nil {
			continue
		}

		ackCount++
		if ack.Status == protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_REJECTED {
			rejectedCount++
			if ack.Error == nil || ack.Error.Code != "session_queue_full" {
				t.Fatalf("rejected ack error = %+v, want session_queue_full", ack.Error)
			}
		}
	}

	if rejectedCount == 0 {
		t.Fatal("expected at least one rejected event ack when event queue is full")
	}

	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}
}

func TestConnectRoutesDifferentNPCsToIndependentLanes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	loop := agent.NewLoop(fake.NewProvider(), trace.NoopRecorder{}, gatewayTestConfig(t))
	protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, NewServer(loop))

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		select {
		case <-serverErrCh:
		case <-time.After(time.Second):
			t.Fatal("gRPC server did not stop")
		}
	})

	conn, err := grpc.DialContext(
		ctx,
		"bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("DialContext returned error: %v", err)
	}
	defer conn.Close()

	client := protocolv1alpha2.NewGameAgentGatewayClient(conn)
	stream, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}

	if err := stream.Send(adapterHelloMessage()); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	_ = recvRuntimeMessage(t, stream)
	capabilityRequest := recvRuntimeMessage(t, stream)
	if err := stream.Send(capabilityListMessage(capabilityRequest.MessageId)); err != nil {
		t.Fatalf("send capability list: %v", err)
	}

	if err := stream.Send(npcInteractionEventMessageForNPC(1, "npc:Linus", "Linus")); err != nil {
		t.Fatalf("send linus event: %v", err)
	}
	linusAck := recvRuntimeMessage(t, stream).GetEventAck()
	if linusAck == nil || linusAck.Status != protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("linus ack = %+v, want accepted", linusAck)
	}
	linusObserve := recvRuntimeMessage(t, stream).GetObserve()
	if linusObserve == nil || linusObserve.EntityId != "npc:Linus" {
		t.Fatalf("linus observe = %+v, want npc:Linus", linusObserve)
	}

	if err := stream.Send(npcInteractionEventMessageForNPC(2, "npc:Robin", "Robin")); err != nil {
		t.Fatalf("send robin event: %v", err)
	}
	robinAck := recvRuntimeMessage(t, stream).GetEventAck()
	if robinAck == nil || robinAck.Status != protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("robin ack = %+v, want accepted", robinAck)
	}
	robinObserve := recvRuntimeMessageWithin(t, stream, 300*time.Millisecond).GetObserve()
	if robinObserve == nil || robinObserve.EntityId != "npc:Robin" {
		t.Fatalf("robin observe = %+v, want npc:Robin", robinObserve)
	}

	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}
}

func TestConnectAcceptsNonStardewTriggerWithRoutedEntity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	loop := agent.NewLoop(fake.NewProvider(), trace.NoopRecorder{}, gatewayTestConfig(t))
	protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, NewServer(loop))

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		select {
		case <-serverErrCh:
		case <-time.After(time.Second):
			t.Fatal("gRPC server did not stop")
		}
	})

	conn, err := grpc.DialContext(
		ctx,
		"bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("DialContext returned error: %v", err)
	}
	defer conn.Close()

	client := protocolv1alpha2.NewGameAgentGatewayClient(conn)
	stream := connectReadyStream(t, ctx, client, "session:test-survival")

	if err := stream.Send(gameEventMessageForEntity(1, "damage_received", "creature:alpha", "creature", "Alpha")); err != nil {
		t.Fatalf("send non-stardew event: %v", err)
	}
	ack := recvRuntimeMessage(t, stream).GetEventAck()
	if ack == nil || ack.Status != protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("non-stardew ack = %+v, want accepted", ack)
	}
	observeMessage := recvRuntimeMessage(t, stream)
	observe := observeMessage.GetObserve()
	if observe == nil || observe.EntityId != "creature:alpha" || observe.WorldId != "world:test" {
		t.Fatalf("observe = %+v, want world:test creature:alpha", observe)
	}

	if err := stream.Send(observationMessageForEntity(observeMessage.MessageId, "creature:alpha", "creature", "Alpha")); err != nil {
		t.Fatalf("send non-stardew observation: %v", err)
	}
	action := recvRuntimeMessage(t, stream).GetAction()
	if action == nil || action.EntityId != "creature:alpha" || action.WorldId != "world:test" {
		t.Fatalf("action = %+v, want world:test creature:alpha", action)
	}

	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}
}

func TestResolveAgentSessionKeyRejectsMissingEventType(t *testing.T) {
	event := gameEventMessageForEntity(1, "   ", "creature:alpha", "creature", "Alpha").GetEvent()

	_, ackErr := resolveAgentSessionKey(agent.ConnectionContext{GameID: "fake-game"}, event)
	if ackErr == nil {
		t.Fatal("resolveAgentSessionKey returned nil error, want event_type_missing")
	}
	if ackErr.Code != "event_type_missing" {
		t.Fatalf("error code = %q, want event_type_missing", ackErr.Code)
	}
}

func TestConnectSerializesEventsForSameNPC(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	loop := agent.NewLoop(fake.NewProvider(), trace.NoopRecorder{}, gatewayTestConfig(t))
	protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, NewServer(loop))

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		select {
		case <-serverErrCh:
		case <-time.After(time.Second):
			t.Fatal("gRPC server did not stop")
		}
	})

	conn, err := grpc.DialContext(
		ctx,
		"bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("DialContext returned error: %v", err)
	}
	defer conn.Close()

	client := protocolv1alpha2.NewGameAgentGatewayClient(conn)
	stream, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}

	if err := stream.Send(adapterHelloMessage()); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	_ = recvRuntimeMessage(t, stream)
	capabilityRequest := recvRuntimeMessage(t, stream)
	if err := stream.Send(capabilityListMessage(capabilityRequest.MessageId)); err != nil {
		t.Fatalf("send capability list: %v", err)
	}

	if err := stream.Send(npcInteractionEventMessageWithIDs(1)); err != nil {
		t.Fatalf("send first event: %v", err)
	}
	firstAck := recvRuntimeMessage(t, stream).GetEventAck()
	if firstAck == nil || firstAck.Status != protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("first ack = %+v, want accepted", firstAck)
	}
	firstObserveMessage := recvRuntimeMessage(t, stream)
	firstObserve := firstObserveMessage.GetObserve()
	if firstObserve == nil || firstObserve.EntityId != "npc:Linus" {
		t.Fatalf("first observe = %+v, want npc:Linus", firstObserve)
	}

	if err := stream.Send(npcInteractionEventMessageWithIDs(2)); err != nil {
		t.Fatalf("send second event: %v", err)
	}
	secondAck := recvRuntimeMessage(t, stream).GetEventAck()
	if secondAck == nil || secondAck.Status != protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("second ack = %+v, want accepted", secondAck)
	}

	if err := stream.Send(npcInteractionEventMessageWithIDs(3)); err != nil {
		t.Fatalf("send third event: %v", err)
	}
	thirdAck := recvRuntimeMessage(t, stream).GetEventAck()
	if thirdAck == nil || thirdAck.Status != protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_REJECTED {
		t.Fatalf("third ack = %+v, want rejected", thirdAck)
	}
	if thirdAck.Error == nil || thirdAck.Error.Code != "session_queue_full" {
		t.Fatalf("third ack error = %+v, want session_queue_full", thirdAck.Error)
	}

	if err := stream.Send(observationMessage(firstObserveMessage.MessageId)); err != nil {
		t.Fatalf("send first observation: %v", err)
	}
	firstAction := recvRuntimeMessage(t, stream).GetAction()
	if firstAction == nil {
		t.Fatal("expected first action")
	}
	if err := stream.Send(actionResultMessage(firstAction.ActionId)); err != nil {
		t.Fatalf("send first action result: %v", err)
	}
	recvTurnCompletion(t, stream, "event_1", "npc:Linus", protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED)

	secondObserve := recvRuntimeMessageWithin(t, stream, 300*time.Millisecond).GetObserve()
	if secondObserve == nil || secondObserve.EntityId != "npc:Linus" {
		t.Fatalf("second observe = %+v, want npc:Linus", secondObserve)
	}

	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}
}

func TestConnectQueuedSameNPCEventReadsPreviousTurnMemory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	provider := &recordingGatewayProvider{}
	recorder := &recordingGatewayTraceRecorder{}
	loop := agent.NewLoop(provider, recorder, gatewayTestConfig(t))
	protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, NewServer(loop))

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		select {
		case <-serverErrCh:
		case <-time.After(time.Second):
			t.Fatal("gRPC server did not stop")
		}
	})

	conn, err := grpc.DialContext(
		ctx,
		"bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("DialContext returned error: %v", err)
	}
	defer conn.Close()

	client := protocolv1alpha2.NewGameAgentGatewayClient(conn)
	stream, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}

	if err := stream.Send(adapterHelloMessage()); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	_ = recvRuntimeMessage(t, stream)
	capabilityRequest := recvRuntimeMessage(t, stream)
	if err := stream.Send(capabilityListMessage(capabilityRequest.MessageId)); err != nil {
		t.Fatalf("send capability list: %v", err)
	}

	if err := stream.Send(npcInteractionEventMessageWithIDs(1)); err != nil {
		t.Fatalf("send first event: %v", err)
	}
	firstAck := recvRuntimeMessage(t, stream).GetEventAck()
	if firstAck == nil || firstAck.Status != protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("first ack = %+v, want accepted", firstAck)
	}
	firstObserveMessage := recvRuntimeMessage(t, stream)
	if firstObserve := firstObserveMessage.GetObserve(); firstObserve == nil || firstObserve.EntityId != "npc:Linus" {
		t.Fatalf("first observe = %+v, want npc:Linus", firstObserve)
	}

	if err := stream.Send(npcInteractionEventMessageWithIDs(2)); err != nil {
		t.Fatalf("send queued event: %v", err)
	}
	secondAck := recvRuntimeMessage(t, stream).GetEventAck()
	if secondAck == nil || secondAck.Status != protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("second ack = %+v, want accepted", secondAck)
	}

	if err := stream.Send(observationMessage(firstObserveMessage.MessageId)); err != nil {
		t.Fatalf("send first observation: %v", err)
	}
	firstAction := recvRuntimeMessage(t, stream).GetAction()
	if firstAction == nil {
		t.Fatal("expected first action")
	}
	if err := stream.Send(actionResultMessage(firstAction.ActionId)); err != nil {
		t.Fatalf("send first action result: %v", err)
	}
	recvTurnCompletion(t, stream, "event_1", "npc:Linus", protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED)

	secondObserveMessage := recvRuntimeMessage(t, stream)
	if secondObserve := secondObserveMessage.GetObserve(); secondObserve == nil || secondObserve.EntityId != "npc:Linus" {
		t.Fatalf("second observe = %+v, want npc:Linus", secondObserve)
	}
	if err := stream.Send(observationMessage(secondObserveMessage.MessageId)); err != nil {
		t.Fatalf("send second observation: %v", err)
	}
	secondAction := recvRuntimeMessage(t, stream).GetAction()
	if secondAction == nil {
		t.Fatal("expected second action")
	}

	requests := provider.Requests()
	if len(requests) != 2 {
		t.Fatalf("provider request count = %d, want 2", len(requests))
	}
	secondPrompt := requests[1].Messages[0].Content
	for _, want := range []string{
		"[Recent Memory]",
		"previous interaction",
		`tool "speak" status "ACTION_STATUS_SUCCEEDED" arguments {"text":"gateway memory line"}`,
		"gateway memory line",
	} {
		if !strings.Contains(secondPrompt, want) {
			t.Fatalf("second prompt missing %q:\n%s", want, secondPrompt)
		}
	}
	for _, unwanted := range []string{
		"event_1",
		"source_turn_id",
	} {
		if strings.Contains(secondPrompt, unwanted) {
			t.Fatalf("second prompt should not expose storage field %q:\n%s", unwanted, secondPrompt)
		}
	}

	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}
}

func TestConnectSameAgentSessionReadsMemoryAfterReconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	provider := &recordingGatewayProvider{}
	recorder := &recordingGatewayTraceRecorder{}
	loop := agent.NewLoop(provider, recorder, gatewayTestConfig(t))
	protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, NewServer(loop))

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		select {
		case <-serverErrCh:
		case <-time.After(time.Second):
			t.Fatal("gRPC server did not stop")
		}
	})

	conn, err := grpc.DialContext(
		ctx,
		"bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("DialContext returned error: %v", err)
	}
	defer conn.Close()

	client := protocolv1alpha2.NewGameAgentGatewayClient(conn)
	firstStream := connectReadyStream(t, ctx, client, "session:first")
	runSuccessfulNPCInteraction(t, firstStream, 1, "npc:Linus", "Linus")
	waitForTraceEventCount(t, recorder, trace.EventContextUpdated, 1)
	if err := firstStream.CloseSend(); err != nil {
		t.Fatalf("close first stream: %v", err)
	}
	if _, err := firstStream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("first stream final recv error = %v, want EOF", err)
	}

	secondStream := connectReadyStream(t, ctx, client, "session:second")
	secondObserveMessage := sendAcceptedNPCEvent(t, secondStream, 2, "npc:Linus", "Linus")
	if err := secondStream.Send(observationMessageForNPC(secondObserveMessage.MessageId, "npc:Linus", "Linus")); err != nil {
		t.Fatalf("send second observation: %v", err)
	}
	if action := recvRuntimeMessage(t, secondStream).GetAction(); action == nil {
		t.Fatal("expected second action")
	}

	requests := provider.Requests()
	if len(requests) != 2 {
		t.Fatalf("provider request count = %d, want 2", len(requests))
	}
	secondPrompt := requests[1].Messages[0].Content
	for _, want := range []string{
		"[Recent Memory]",
		"previous interaction",
		`tool "speak" status "ACTION_STATUS_SUCCEEDED" arguments {"text":"gateway memory line"}`,
		"gateway memory line",
	} {
		if !strings.Contains(secondPrompt, want) {
			t.Fatalf("reconnected prompt missing %q:\n%s", want, secondPrompt)
		}
	}

	if err := secondStream.CloseSend(); err != nil {
		t.Fatalf("close second stream: %v", err)
	}
}

func TestConnectKeepsSameNameToolsIsolatedPerEnvironmentSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	provider := &sameNameToolGatewayProvider{}
	loop := agent.NewLoop(provider, trace.NoopRecorder{}, gatewayTestConfig(t))
	protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, NewServer(loop))
	startGatewayServer(t, grpcServer, listener)

	conn := dialGateway(t, ctx, listener)
	defer conn.Close()

	client := protocolv1alpha2.NewGameAgentGatewayClient(conn)
	firstStream := connectReadyStreamWithCapabilities(t, ctx, client, "session:shared-text", capabilityListWithSharedTextActionMessage)
	secondStream := connectReadyStreamWithCapabilities(t, ctx, client, "session:shared-value", capabilityListWithSharedValueActionMessage)

	firstObserveMessage := sendAcceptedNPCEvent(t, firstStream, 1, "npc:Linus", "Linus")
	if err := firstStream.Send(observationMessageForNPC(firstObserveMessage.MessageId, "npc:Linus", "Linus")); err != nil {
		t.Fatalf("send first observation: %v", err)
	}
	firstAction := recvRuntimeMessage(t, firstStream).GetAction()
	if firstAction == nil || firstAction.Capability != "shared_action" {
		t.Fatalf("first action = %+v, want shared_action", firstAction)
	}
	if got := firstAction.GetArguments().GetFields()["text"].GetStringValue(); got != "alpha" {
		t.Fatalf("first action text = %q, want alpha", got)
	}
	if _, ok := firstAction.GetArguments().GetFields()["value"]; ok {
		t.Fatalf("first action arguments = %+v, want no value field", firstAction.GetArguments().GetFields())
	}
	if err := firstStream.Send(actionResultMessage(firstAction.ActionId)); err != nil {
		t.Fatalf("send first action result: %v", err)
	}
	recvTurnCompletion(t, firstStream, "event_1", "npc:Linus", protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED)

	secondObserveMessage := sendAcceptedNPCEvent(t, secondStream, 2, "npc:Linus", "Linus")
	if err := secondStream.Send(observationMessageForNPC(secondObserveMessage.MessageId, "npc:Linus", "Linus")); err != nil {
		t.Fatalf("send second observation: %v", err)
	}
	secondAction := recvRuntimeMessage(t, secondStream).GetAction()
	if secondAction == nil || secondAction.Capability != "shared_action" {
		t.Fatalf("second action = %+v, want shared_action", secondAction)
	}
	if got := secondAction.GetArguments().GetFields()["value"].GetStringValue(); got != "beta" {
		t.Fatalf("second action value = %q, want beta", got)
	}
	if _, ok := secondAction.GetArguments().GetFields()["text"]; ok {
		t.Fatalf("second action arguments = %+v, want no text field", secondAction.GetArguments().GetFields())
	}
	if err := secondStream.Send(actionResultMessage(secondAction.ActionId)); err != nil {
		t.Fatalf("send second action result: %v", err)
	}
	recvTurnCompletion(t, secondStream, "event_2", "npc:Linus", protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED)

	requests := provider.Requests()
	if got := len(requests); got != 3 {
		t.Fatalf("provider request count = %d, want first stream one step and second stream two steps", got)
	}
	assertSharedActionRequestSchema(t, requests[0], `"text"`, `"value"`)
	assertSharedActionRequestSchema(t, requests[1], `"value"`, `"text"`)
	assertSharedActionRequestSchema(t, requests[2], `"value"`, `"text"`)

	if err := firstStream.CloseSend(); err != nil {
		t.Fatalf("close first stream: %v", err)
	}
	if err := secondStream.CloseSend(); err != nil {
		t.Fatalf("close second stream: %v", err)
	}
}

func TestConnectDoesNotLeakMemoryAcrossNPCs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	provider := &recordingGatewayProvider{}
	recorder := &recordingGatewayTraceRecorder{}
	loop := agent.NewLoop(provider, recorder, gatewayTestConfig(t))
	protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, NewServer(loop))

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		select {
		case <-serverErrCh:
		case <-time.After(time.Second):
			t.Fatal("gRPC server did not stop")
		}
	})

	conn, err := grpc.DialContext(
		ctx,
		"bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("DialContext returned error: %v", err)
	}
	defer conn.Close()

	client := protocolv1alpha2.NewGameAgentGatewayClient(conn)
	stream := connectReadyStream(t, ctx, client, "session:test")

	runSuccessfulNPCInteraction(t, stream, 1, "npc:Abigail", "Abigail")
	waitForTraceEventCount(t, recorder, trace.EventContextUpdated, 1)

	linusObserveMessage := sendAcceptedNPCEvent(t, stream, 2, "npc:Linus", "Linus")
	if err := stream.Send(observationMessageForNPC(linusObserveMessage.MessageId, "npc:Linus", "Linus")); err != nil {
		t.Fatalf("send linus observation: %v", err)
	}
	if action := recvRuntimeMessage(t, stream).GetAction(); action == nil {
		t.Fatal("expected linus action")
	}

	requests := provider.Requests()
	if len(requests) != 2 {
		t.Fatalf("provider request count = %d, want 2", len(requests))
	}
	linusPrompt := requests[1].Messages[0].Content
	for _, unwanted := range []string{
		"event_1",
		"gateway memory line",
	} {
		if strings.Contains(linusPrompt, unwanted) {
			t.Fatalf("linus prompt unexpectedly contains Abigail memory %q:\n%s", unwanted, linusPrompt)
		}
	}

	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}
}

func TestConnectDrainsQueuedEventOnDisconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	loop := agent.NewLoop(fake.NewProvider(), trace.NoopRecorder{}, gatewayTestConfig(t))
	protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, NewServer(loop))

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		select {
		case <-serverErrCh:
		case <-time.After(time.Second):
			t.Fatal("gRPC server did not stop")
		}
	})

	conn, err := grpc.DialContext(
		ctx,
		"bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("DialContext returned error: %v", err)
	}
	defer conn.Close()

	client := protocolv1alpha2.NewGameAgentGatewayClient(conn)
	stream, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}

	if err := stream.Send(adapterHelloMessage()); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	_ = recvRuntimeMessage(t, stream)
	capabilityRequest := recvRuntimeMessage(t, stream)
	if err := stream.Send(capabilityListMessage(capabilityRequest.MessageId)); err != nil {
		t.Fatalf("send capability list: %v", err)
	}

	if err := stream.Send(npcInteractionEventMessageWithIDs(1)); err != nil {
		t.Fatalf("send first event: %v", err)
	}
	firstAck := recvRuntimeMessage(t, stream).GetEventAck()
	if firstAck == nil || firstAck.Status != protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("first ack = %+v, want accepted", firstAck)
	}
	firstObserve := recvRuntimeMessage(t, stream).GetObserve()
	if firstObserve == nil || firstObserve.EntityId != "npc:Linus" {
		t.Fatalf("first observe = %+v, want npc:Linus", firstObserve)
	}

	if err := stream.Send(npcInteractionEventMessageWithIDs(2)); err != nil {
		t.Fatalf("send queued event: %v", err)
	}
	secondAck := recvRuntimeMessage(t, stream).GetEventAck()
	if secondAck == nil || secondAck.Status != protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("second ack = %+v, want accepted", secondAck)
	}

	logs := captureStandardLog(t)
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}

	msg, err := stream.Recv()
	if err == nil {
		t.Fatalf("received runtime message after disconnect: %+v", msg.Payload)
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("final recv error = %v, want EOF", err)
	}

	waitForLogContains(t, logs, `abort queued game event "event_2": connection_closed`)
}

func TestConnectReturnsDuplicateAckForRepeatedEventID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	loop := agent.NewLoop(fake.NewProvider(), trace.NoopRecorder{}, gatewayTestConfig(t))
	protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, NewServer(loop))

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		select {
		case <-serverErrCh:
		case <-time.After(time.Second):
			t.Fatal("gRPC server did not stop")
		}
	})

	conn, err := grpc.DialContext(
		ctx,
		"bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("DialContext returned error: %v", err)
	}
	defer conn.Close()

	client := protocolv1alpha2.NewGameAgentGatewayClient(conn)
	stream, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}

	if err := stream.Send(adapterHelloMessage()); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	_ = recvRuntimeMessage(t, stream)
	capabilityRequest := recvRuntimeMessage(t, stream)
	if err := stream.Send(capabilityListMessage(capabilityRequest.MessageId)); err != nil {
		t.Fatalf("send capability list: %v", err)
	}

	first := npcInteractionEventMessageWithIDs(1)
	if err := stream.Send(first); err != nil {
		t.Fatalf("send first event: %v", err)
	}
	firstAck := recvRuntimeMessage(t, stream).GetEventAck()
	if firstAck == nil || firstAck.Status != protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("first ack = %+v, want accepted", firstAck)
	}
	if observe := recvRuntimeMessage(t, stream).GetObserve(); observe == nil {
		t.Fatal("expected first event to start observe")
	}

	duplicate := npcInteractionEventMessageWithIDs(2)
	duplicate.GetEvent().EventId = first.GetEvent().EventId
	if err := stream.Send(duplicate); err != nil {
		t.Fatalf("send duplicate event: %v", err)
	}
	duplicateAck := recvRuntimeMessage(t, stream).GetEventAck()
	if duplicateAck == nil {
		t.Fatal("expected duplicate event ack")
	}
	if duplicateAck.EventId != first.GetEvent().EventId {
		t.Fatalf("duplicate ack event id = %q, want %q", duplicateAck.EventId, first.GetEvent().EventId)
	}
	if duplicateAck.Status != protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_DUPLICATE {
		t.Fatalf("duplicate ack status = %v, want duplicate", duplicateAck.Status)
	}

	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}
}

type capturedLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *capturedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *capturedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func captureStandardLog(t *testing.T) *capturedLog {
	t.Helper()

	captured := &capturedLog{}
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(captured)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})
	return captured
}

func waitForLogContains(t *testing.T, logs *capturedLog, want string) {
	t.Helper()

	deadline := time.After(time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()

	for {
		if strings.Contains(logs.String(), want) {
			return
		}

		select {
		case <-deadline:
			t.Fatalf("log output %q does not contain %q", logs.String(), want)
		case <-tick.C:
		}
	}
}

func timeline(t *testing.T, step string) {
	t.Helper()
	t.Logf("[timeline] %s", step)
}

func recvRuntimeMessage(t *testing.T, stream protocolv1alpha2.GameAgentGateway_ConnectClient) *protocolv1alpha2.RuntimeMessage {
	t.Helper()

	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv runtime message: %v", err)
	}
	return msg
}

func recvRuntimeMessageWithin(t *testing.T, stream protocolv1alpha2.GameAgentGateway_ConnectClient, timeout time.Duration) *protocolv1alpha2.RuntimeMessage {
	t.Helper()

	type recvResult struct {
		msg *protocolv1alpha2.RuntimeMessage
		err error
	}
	ch := make(chan recvResult, 1)
	go func() {
		msg, err := stream.Recv()
		ch <- recvResult{msg: msg, err: err}
	}()

	select {
	case result := <-ch:
		if result.err != nil {
			t.Fatalf("recv runtime message: %v", result.err)
		}
		return result.msg
	case <-time.After(timeout):
		t.Fatalf("runtime message was not received within %s", timeout)
		return nil
	}
}

func waitForTraceEventCount(t *testing.T, recorder *recordingGatewayTraceRecorder, eventName trace.EventName, want int) {
	t.Helper()

	deadline := time.After(time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()

	for {
		if got := recorder.Count(eventName); got >= want {
			return
		}

		select {
		case <-deadline:
			t.Fatalf("trace event %q count = %d, want at least %d", eventName, recorder.Count(eventName), want)
		case <-tick.C:
		}
	}
}

func waitForGatewayProviderRequestCount(t *testing.T, provider *scriptedGatewayProvider, want int) {
	t.Helper()

	deadline := time.After(time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()

	for {
		if got := len(provider.Requests()); got >= want {
			return
		}

		select {
		case <-deadline:
			t.Fatalf("provider request count = %d, want at least %d", len(provider.Requests()), want)
		case <-tick.C:
		}
	}
}

func requestMessagesContain(messages []model.Message, needle string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, needle) {
			return true
		}
	}
	return false
}

func assertSharedActionRequestSchema(t *testing.T, req model.Request, wantToken string, unwantedToken string) {
	t.Helper()

	if len(req.Tools) != 1 {
		t.Fatalf("request tools = %+v, want exactly one shared_action tool", req.Tools)
	}
	if req.Tools[0].Name != "shared_action" {
		t.Fatalf("request tool name = %q, want shared_action", req.Tools[0].Name)
	}
	if !strings.Contains(req.Tools[0].InputSchema, wantToken) {
		t.Fatalf("shared_action schema missing %s: %s", wantToken, req.Tools[0].InputSchema)
	}
	if strings.Contains(req.Tools[0].InputSchema, unwantedToken) {
		t.Fatalf("shared_action schema unexpectedly contains %s: %s", unwantedToken, req.Tools[0].InputSchema)
	}
}

func startGatewayServer(t *testing.T, grpcServer *grpc.Server, listener *bufconn.Listener) {
	t.Helper()

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		select {
		case <-serverErrCh:
		case <-time.After(time.Second):
			t.Fatal("gRPC server did not stop")
		}
	})
}

func dialGateway(t *testing.T, ctx context.Context, listener *bufconn.Listener) *grpc.ClientConn {
	t.Helper()

	conn, err := grpc.DialContext(
		ctx,
		"bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("DialContext returned error: %v", err)
	}
	return conn
}

func connectReadyStream(
	t *testing.T,
	ctx context.Context,
	client protocolv1alpha2.GameAgentGatewayClient,
	sessionID string,
) protocolv1alpha2.GameAgentGateway_ConnectClient {
	return connectReadyStreamWithCapabilities(t, ctx, client, sessionID, capabilityListMessage)
}

func connectReadyStreamWithCapabilities(
	t *testing.T,
	ctx context.Context,
	client protocolv1alpha2.GameAgentGatewayClient,
	sessionID string,
	capabilityMessage func(string) *protocolv1alpha2.AdapterMessage,
) protocolv1alpha2.GameAgentGateway_ConnectClient {
	t.Helper()

	stream, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}
	if err := stream.Send(adapterHelloMessageWithSession(sessionID)); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	_ = recvRuntimeMessage(t, stream)
	capabilityRequest := recvRuntimeMessage(t, stream)
	if err := stream.Send(capabilityMessage(capabilityRequest.MessageId)); err != nil {
		t.Fatalf("send capability list: %v", err)
	}
	return stream
}

func sendAcceptedNPCEvent(
	t *testing.T,
	stream protocolv1alpha2.GameAgentGateway_ConnectClient,
	index int,
	entityID string,
	displayName string,
) *protocolv1alpha2.RuntimeMessage {
	t.Helper()

	if err := stream.Send(npcInteractionEventMessageForNPC(index, entityID, displayName)); err != nil {
		t.Fatalf("send %s event: %v", entityID, err)
	}
	ack := recvRuntimeMessage(t, stream).GetEventAck()
	if ack == nil || ack.Status != protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatalf("%s ack = %+v, want accepted", entityID, ack)
	}
	observeMessage := recvRuntimeMessage(t, stream)
	if observe := observeMessage.GetObserve(); observe == nil || observe.EntityId != entityID {
		t.Fatalf("%s observe = %+v, want %s", entityID, observe, entityID)
	}
	return observeMessage
}

func runSuccessfulNPCInteraction(
	t *testing.T,
	stream protocolv1alpha2.GameAgentGateway_ConnectClient,
	index int,
	entityID string,
	displayName string,
) {
	t.Helper()

	observeMessage := sendAcceptedNPCEvent(t, stream, index, entityID, displayName)
	if err := stream.Send(observationMessageForNPC(observeMessage.MessageId, entityID, displayName)); err != nil {
		t.Fatalf("send %s observation: %v", entityID, err)
	}
	action := recvRuntimeMessage(t, stream).GetAction()
	if action == nil {
		t.Fatalf("expected %s action", entityID)
	}
	if err := stream.Send(actionResultMessage(action.ActionId)); err != nil {
		t.Fatalf("send %s action result: %v", entityID, err)
	}
	completion := recvTurnCompletion(t, stream, "event_"+strconv.Itoa(index), entityID, protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED)
	if action.SourceEventId != completion.EventId {
		t.Fatalf("action source_event_id = %q, want completion event_id %q", action.SourceEventId, completion.EventId)
	}
	if action.SourceTurnId != completion.TurnId {
		t.Fatalf("action source_turn_id = %q, want completion turn_id %q", action.SourceTurnId, completion.TurnId)
	}
}

func recvTurnCompletion(
	t *testing.T,
	stream protocolv1alpha2.GameAgentGateway_ConnectClient,
	eventID string,
	entityID string,
	status protocolv1alpha2.TurnCompletionStatus,
) *protocolv1alpha2.TurnCompletion {
	t.Helper()

	msg := recvRuntimeMessage(t, stream)
	completion := msg.GetTurnCompletion()
	if completion == nil {
		t.Fatalf("expected TurnCompletion, got %+v", msg.Payload)
	}
	if completion.EventId != eventID {
		t.Fatalf("turn completion event_id = %q, want %q", completion.EventId, eventID)
	}
	if completion.EntityId != entityID {
		t.Fatalf("turn completion entity_id = %q, want %q", completion.EntityId, entityID)
	}
	if completion.Status != status {
		t.Fatalf("turn completion status = %s, want %s", completion.Status, status)
	}
	if completion.TurnId == "" {
		t.Fatal("turn completion turn_id is empty")
	}
	return completion
}

func adapterHelloMessage() *protocolv1alpha2.AdapterMessage {
	return adapterHelloMessageWithSession("session:test")
}

func adapterHelloMessageWithSession(sessionID string) *protocolv1alpha2.AdapterMessage {
	return &protocolv1alpha2.AdapterMessage{
		MessageId: "hello_msg_1",
		Payload: &protocolv1alpha2.AdapterMessage_Hello{
			Hello: &protocolv1alpha2.AdapterHello{
				AdapterId:       "fake-adapter",
				AdapterVersion:  "0.1.0",
				ProtocolVersion: "v1alpha2",
				GameId:          "fake-game",
				GameVersion:     "0.1.0",
				SessionId:       sessionID,
			},
		},
	}
}

func capabilityListMessage(correlationID string) *protocolv1alpha2.AdapterMessage {
	return &protocolv1alpha2.AdapterMessage{
		MessageId:     "capabilities_msg_1",
		CorrelationId: correlationID,
		Payload: &protocolv1alpha2.AdapterMessage_Capabilities{
			Capabilities: &protocolv1alpha2.CapabilityList{
				Capabilities: []*protocolv1alpha2.Capability{
					{
						Name:            "speak",
						Version:         "0.1.0",
						Description:     "Make the NPC speak.",
						InputSchemaJson: `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`,
						ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_SYNC,
					},
				},
				Revision: 1,
			},
		},
	}
}

func capabilityListWithEntityIDMessage(correlationID string, entityID string) *protocolv1alpha2.AdapterMessage {
	msg := capabilityListMessage(correlationID)
	msg.GetCapabilities().EntityId = &entityID
	return msg
}

func capabilityListWithSharedTextActionMessage(correlationID string) *protocolv1alpha2.AdapterMessage {
	return capabilityListWithSharedActionMessage(
		correlationID,
		`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`,
		toolPolicyExtensionsForGateway(false, true),
	)
}

func capabilityListWithSharedValueActionMessage(correlationID string) *protocolv1alpha2.AdapterMessage {
	return capabilityListWithSharedActionMessage(
		correlationID,
		`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`,
		nil,
	)
}

func capabilityListWithSharedActionMessage(correlationID string, inputSchema string, extensions *structpb.Struct) *protocolv1alpha2.AdapterMessage {
	capability := &protocolv1alpha2.Capability{
		Name:            "shared_action",
		Version:         "0.1.0",
		Description:     "A stream-local test action.",
		InputSchemaJson: inputSchema,
		ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_SYNC,
		ConcurrencyMode: protocolv1alpha2.CapabilityConcurrencyMode_CAPABILITY_CONCURRENCY_MODE_SEQUENTIAL,
		Extensions:      extensions,
	}
	return &protocolv1alpha2.AdapterMessage{
		MessageId:     "capabilities_msg_1",
		CorrelationId: correlationID,
		Payload: &protocolv1alpha2.AdapterMessage_Capabilities{
			Capabilities: &protocolv1alpha2.CapabilityList{
				Capabilities: []*protocolv1alpha2.Capability{capability},
				Revision:     1,
			},
		},
	}
}

func capabilityListWithEmoteMessage(correlationID string) *protocolv1alpha2.AdapterMessage {
	return &protocolv1alpha2.AdapterMessage{
		MessageId:     "capabilities_msg_1",
		CorrelationId: correlationID,
		Payload: &protocolv1alpha2.AdapterMessage_Capabilities{
			Capabilities: &protocolv1alpha2.CapabilityList{
				Capabilities: []*protocolv1alpha2.Capability{
					{
						Name:            "speak",
						Version:         "0.1.0",
						Description:     "Make the NPC speak.",
						InputSchemaJson: `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`,
						ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_SYNC,
					},
					{
						Name:            "emote",
						Version:         "0.1.0",
						Description:     "Make the NPC play an emote bubble.",
						InputSchemaJson: `{"type":"object","properties":{"emote":{"type":"string","enum":["happy","sad","surprised","neutral"]}},"required":["emote"],"additionalProperties":false}`,
						ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_SYNC,
					},
				},
				Revision: 1,
			},
		},
	}
}

func capabilityListWithPresentDialogueMessage(correlationID string) *protocolv1alpha2.AdapterMessage {
	return &protocolv1alpha2.AdapterMessage{
		MessageId:     "capabilities_msg_1",
		CorrelationId: correlationID,
		Payload: &protocolv1alpha2.AdapterMessage_Capabilities{
			Capabilities: &protocolv1alpha2.CapabilityList{
				Capabilities: []*protocolv1alpha2.Capability{
					{
						Name:            "present_dialogue",
						Version:         "0.1.0",
						Description:     "Show dialogue with reply options and wait for player_said_to_npc.",
						InputSchemaJson: `{"type":"object","properties":{"text":{"type":"string"},"reply_options":{"type":"array","items":{"type":"string"}}},"required":["text"],"additionalProperties":false}`,
						ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_SYNC,
						ConcurrencyMode: protocolv1alpha2.CapabilityConcurrencyMode_CAPABILITY_CONCURRENCY_MODE_SEQUENTIAL,
						Extensions:      toolPolicyExtensionsForGateway(true, true),
					},
				},
				Revision: 1,
			},
		},
	}
}

func capabilityListWithMoveToMessage(correlationID string) *protocolv1alpha2.AdapterMessage {
	return &protocolv1alpha2.AdapterMessage{
		MessageId:     "capabilities_msg_1",
		CorrelationId: correlationID,
		Payload: &protocolv1alpha2.AdapterMessage_Capabilities{
			Capabilities: &protocolv1alpha2.CapabilityList{
				Capabilities: []*protocolv1alpha2.Capability{
					{
						Name:            "move_to",
						Version:         "0.1.0",
						Description:     "Move to a reachable tile in the current location.",
						InputSchemaJson: `{"type":"object","properties":{"location":{"type":"string"},"tile":{"type":"object","properties":{"x":{"type":"integer"},"y":{"type":"integer"}},"required":["x","y"],"additionalProperties":false}},"required":["location","tile"],"additionalProperties":false}`,
						ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_ASYNC,
						ConcurrencyMode: protocolv1alpha2.CapabilityConcurrencyMode_CAPABILITY_CONCURRENCY_MODE_SEQUENTIAL,
					},
				},
				Revision: 1,
			},
		},
	}
}

func capabilityListWithParallelSenseMessage(correlationID string) *protocolv1alpha2.AdapterMessage {
	return &protocolv1alpha2.AdapterMessage{
		MessageId:     "capabilities_msg_1",
		CorrelationId: correlationID,
		Payload: &protocolv1alpha2.AdapterMessage_Capabilities{
			Capabilities: &protocolv1alpha2.CapabilityList{
				Capabilities: []*protocolv1alpha2.Capability{
					{
						Name:            "sense",
						Version:         "0.1.0",
						Description:     "Inspect nearby state.",
						InputSchemaJson: `{"type":"object","properties":{"label":{"type":"string"}},"required":["label"]}`,
						ExecutionMode:   protocolv1alpha2.ExecutionMode_EXECUTION_MODE_SYNC,
						ConcurrencyMode: protocolv1alpha2.CapabilityConcurrencyMode_CAPABILITY_CONCURRENCY_MODE_PARALLEL_SAFE,
					},
				},
				Revision: 1,
			},
		},
	}
}

func toolPolicyExtensionsForGateway(exclusivePerStep bool, settleAfterSuccess bool) *structpb.Struct {
	extensions, err := structpb.NewStruct(map[string]any{
		"gameagent": map[string]any{
			"tool_policy": map[string]any{
				"exclusive_per_step":   exclusivePerStep,
				"settle_after_success": settleAfterSuccess,
			},
		},
	})
	if err != nil {
		panic(err)
	}
	return extensions
}

func npcInteractionEventMessage() *protocolv1alpha2.AdapterMessage {
	return npcInteractionEventMessageWithIDs(1)
}

func npcInteractionEventMessageWithIDs(index int) *protocolv1alpha2.AdapterMessage {
	return npcInteractionEventMessageForNPC(index, "npc:Linus", "Linus")
}

func npcInteractionEventMessageForNPC(index int, entityID string, displayName string) *protocolv1alpha2.AdapterMessage {
	return gameEventMessageForEntity(index, "player_interacted_with_npc", entityID, "npc", displayName)
}

func gameEventMessageForEntity(index int, eventType string, entityID string, entityType string, displayName string) *protocolv1alpha2.AdapterMessage {
	return gameEventMessageForEntityDefinition(index, eventType, entityID, entityID, entityType, displayName)
}

func gameEventMessageForEntityDefinition(index int, eventType string, entityID string, definitionID string, entityType string, displayName string) *protocolv1alpha2.AdapterMessage {
	return &protocolv1alpha2.AdapterMessage{
		MessageId: "event_msg_" + strconv.Itoa(index),
		Payload: &protocolv1alpha2.AdapterMessage_Event{
			Event: &protocolv1alpha2.GameEvent{
				EventId:        "event_" + strconv.Itoa(index),
				EventType:      eventType,
				WorldId:        "world:test",
				TargetEntityId: entityID,
				Entities: []*protocolv1alpha2.EntityRef{
					{
						EntityId:     "player:local",
						EntityType:   "player",
						DisplayName:  "Player",
						DefinitionId: "player:local",
					},
					{
						EntityId:     entityID,
						EntityType:   entityType,
						DisplayName:  displayName,
						DefinitionId: definitionID,
					},
				},
				Sequence: 1,
			},
		},
	}
}

func observationMessage(correlationID string) *protocolv1alpha2.AdapterMessage {
	return observationMessageForNPC(correlationID, "npc:Linus", "Linus")
}

func observationMessageForNPC(correlationID string, entityID string, displayName string) *protocolv1alpha2.AdapterMessage {
	return observationMessageForEntity(correlationID, entityID, "npc", displayName)
}

func observationMessageForEntity(correlationID string, entityID string, entityType string, displayName string) *protocolv1alpha2.AdapterMessage {
	state, err := structpb.NewStruct(map[string]any{
		"entity_type": entityType,
		"name":        displayName,
		"location":    "Mountain",
		"weather":     "sunny",
	})
	if err != nil {
		panic(err)
	}

	return &protocolv1alpha2.AdapterMessage{
		MessageId:     "observation_msg_1",
		CorrelationId: correlationID,
		Payload: &protocolv1alpha2.AdapterMessage_Observation{
			Observation: &protocolv1alpha2.Observation{
				EntityId: entityID,
				WorldId:  "world:test",
				Revision: 1,
				State:    state,
			},
		},
	}
}

func actionResultMessage(actionID string) *protocolv1alpha2.AdapterMessage {
	return actionResultStatusMessage(actionID, protocolv1alpha2.ActionStatus_ACTION_STATUS_SUCCEEDED)
}

func actionStatusUpdateMessage(actionID string, status protocolv1alpha2.ActionStatus) *protocolv1alpha2.AdapterMessage {
	return &protocolv1alpha2.AdapterMessage{
		MessageId: "action_status_msg_1",
		Payload: &protocolv1alpha2.AdapterMessage_ActionStatus{
			ActionStatus: &protocolv1alpha2.ActionStatusUpdate{
				ActionId: actionID,
				Status:   status,
			},
		},
	}
}

func actionResultStatusMessage(actionID string, status protocolv1alpha2.ActionStatus) *protocolv1alpha2.AdapterMessage {
	return &protocolv1alpha2.AdapterMessage{
		MessageId: "action_result_msg_1",
		Payload: &protocolv1alpha2.AdapterMessage_ActionResult{
			ActionResult: &protocolv1alpha2.ActionResult{
				ActionId: actionID,
				Status:   status,
				Error: &protocolv1alpha2.Error{
					Code:    "adapter_" + strings.ToLower(strings.TrimPrefix(status.String(), "ACTION_STATUS_")),
					Message: "adapter returned " + status.String(),
				},
			},
		},
	}
}
