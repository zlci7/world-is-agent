package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
	"gameagent/runtime/internal/tool"
)

// Server 实现 Runtime 侧的 GameAgentGateway gRPC 服务。
type Server struct {
	protocolv1alpha2.UnimplementedGameAgentGatewayServer

	agentLoop   eventHandler
	worlds      *WorldRegistry
	dispatcher  *task.Dispatcher
	mu          sync.Mutex
	connections map[*worldConnection]struct{}
	stopped     bool
}

type eventHandler interface {
	HandleEvent(context.Context, agent.Environment, agent.ConnectionContext, session.AgentSessionKey, *protocolv1alpha2.EntityRef, *tool.EnvironmentToolCatalog, *protocolv1alpha2.GameEvent) error
}

type historyMaintainer interface {
	MaintainHistory(context.Context, session.AgentSessionKey, *memory.GameTimeSnapshot)
}

type ServerOption func(*Server)

func WithTaskService(service *task.Service) ServerOption {
	return func(s *Server) {
		if service != nil {
			s.worlds = NewWorldRegistry(service)
		}
	}
}

func NewServer(agentLoop eventHandler, options ...ServerOption) *Server {
	s := &Server{
		agentLoop:   agentLoop,
		connections: make(map[*worldConnection]struct{}),
	}
	for _, option := range options {
		option(s)
	}
	return s
}

func (s *Server) WorldRegistry() *WorldRegistry { return s.worlds }

func (s *Server) StopTaskAdmission() {
	s.mu.Lock()
	s.stopped = true
	dispatcher := s.dispatcher
	s.mu.Unlock()
	if dispatcher != nil {
		dispatcher.Stop()
	}
	if s.worlds != nil {
		s.worlds.StopAdmission()
	}
}

func (s *Server) Close(ctx context.Context) error {
	s.StopTaskAdmission()
	s.mu.Lock()
	connections := make([]*worldConnection, 0, len(s.connections))
	for c := range s.connections {
		connections = append(connections, c)
	}
	s.mu.Unlock()
	var result error
	for _, c := range connections {
		result = errors.Join(result, s.closeConnection(ctx, c))
	}
	return result
}

func (s *Server) closeConnection(ctx context.Context, c *worldConnection) error {
	if s.worlds != nil {
		return s.worlds.disconnect(ctx, c, "connection_closed")
	}
	c.env.close()
	c.transport.close()
	c.lanes.CloseAndWait()
	return nil
}

// Connect 管理一条 Adapter stream 的完整生命周期。
//
// 它先完成 Environment Bootstrap，发现 Adapter 提供的 capabilities，
// 然后进入 recvLoop 持续分发 GameEvent / Observation / ActionResult。
func (s *Server) Connect(stream protocolv1alpha2.GameAgentGateway_ConnectServer) error {
	firstMessage, err := stream.Recv()
	if err != nil {
		return err
	}

	hello := firstMessage.GetHello()
	if hello == nil {
		return fmt.Errorf("expected adapter hello as first message")
	}
	acceptedExtensions := []string{}
	if s.worlds != nil {
		for _, extension := range hello.SupportedExtensions {
			if extension == taskExtension {
				acceptedExtensions = append(acceptedExtensions, extension)
				break
			}
		}
	}
	tasksNegotiated := len(acceptedExtensions) != 0

	// EnvironmentReady 只表示协议连接已经建立；Runtime 是否能执行
	// AgentRun，还要等 capability discovery 完成。
	readyMessageID := newMessageID("runtime_ready")
	readyMessage := &protocolv1alpha2.RuntimeMessage{
		MessageId: readyMessageID,
		Payload: &protocolv1alpha2.RuntimeMessage_EnvironmentReady{
			EnvironmentReady: &protocolv1alpha2.EnvironmentReady{
				SessionId:          hello.SessionId,
				AcceptedExtensions: acceptedExtensions,
				ServerTimeUnixMs:   time.Now().UnixMilli(),
			},
		},
	}

	conn := agent.ConnectionContext{
		GameID:    hello.GameId,
		SessionID: hello.SessionId,
	}

	if err := stream.Send(readyMessage); err != nil {
		return err
	}

	// MVP0 先发现 environment-level capability；未来如果不同 entity
	// 能力不同，再把 CapabilityRequest 细化到 entity_id 维度。
	capabilityRequestID := newMessageID("cap_req")
	capabilityRequestMessage := &protocolv1alpha2.RuntimeMessage{
		MessageId: capabilityRequestID,
		Payload: &protocolv1alpha2.RuntimeMessage_CapabilityRequest{
			CapabilityRequest: &protocolv1alpha2.CapabilityRequest{},
		},
	}

	if err := stream.Send(capabilityRequestMessage); err != nil {
		return err
	}

	capabilityMessage, err := stream.Recv()
	if err != nil {
		return err
	}

	capabilityList := capabilityMessage.GetCapabilities()
	if capabilityList == nil {
		return fmt.Errorf("expected capability list")
	}

	catalog, diagnostics, err := tool.BuildEnvironmentToolCatalog(capabilityList)
	logCapabilityBootstrapDiagnostics(hello.SessionId, diagnostics)
	if err != nil {
		return err
	}

	env := newStreamEnvironment(stream)
	laneStore, err := session.NewLaneStore(stream.Context(), session.DefaultQueueSize)
	if err != nil {
		return err
	}
	connection := &worldConnection{id: newMessageID("connection"), hello: hello, env: env, lanes: laneStore, catalog: catalog}
	connection.transport = newStreamEnvironment(stream)
	connection.transport.sendSlot = env.sendSlot
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		env.close()
		laneStore.CloseAndWait()
		return task.ErrWorldNotReady
	}
	s.connections[connection] = struct{}{}
	s.mu.Unlock()
	defer func() {
		if err := s.closeConnection(context.Background(), connection); err != nil {
			log.Printf("task disconnect: %s", logSafeError(err))
		}
		s.mu.Lock()
		delete(s.connections, connection)
		s.mu.Unlock()
	}()
	seenEventIDs := make(map[string]struct{})

	// recvLoop 只负责接收和分发 AdapterMessage，避免被单次 AgentRun 阻塞。
	for {
		msg, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		// Event 会启动新的 AgentRun；Observation / ActionResult 用来唤醒
		// 已经在 streamEnvironment 中等待的同步调用。
		env, laneStore = connection.env, connection.lanes
		if hasInlineTaskPayload(msg) {
			ready := false
			if tasksNegotiated && connection.world != nil {
				_, ready = s.worlds.Current(*connection.world)
			}
			if !ready {
				if err := env.send(taskProtocolError(msg.MessageId, task.ErrWorldNotReady)); err != nil {
					return err
				}
				continue
			}
		}
		switch payload := msg.Payload.(type) {
		case *protocolv1alpha2.AdapterMessage_WorldBinding:
			if !tasksNegotiated {
				if err := env.send(taskProtocolError(msg.MessageId, task.ErrWorldNotReady)); err != nil {
					return err
				}
				continue
			}
			head, bindErr := s.worlds.bind(stream.Context(), connection, payload.WorldBinding)
			status := "ready"
			if bindErr != nil {
				status = "paused"
			}
			response := &protocolv1alpha2.WorldBindingReady{Scope: taskScopeToProtocol(head.Binding), Status: status, Error: taskErrorToProtocol(bindErr)}
			if err := connection.transport.send(&protocolv1alpha2.RuntimeMessage{MessageId: newMessageID("world_ready"), CorrelationId: msg.MessageId, Payload: &protocolv1alpha2.RuntimeMessage_WorldBindingReady{WorldBindingReady: response}}); err != nil {
				return err
			}
			if bindErr == nil {
				if err := s.worlds.markReady(connection, head); err != nil {
					return err
				}
			}
		case *protocolv1alpha2.AdapterMessage_WorldClock:
			clockErr := error(task.ErrWorldNotReady)
			if tasksNegotiated && connection.world != nil {
				clockErr = s.worlds.UpdateClock(stream.Context(), env, payload.WorldClock)
			}
			if clockErr != nil {
				if err := env.send(taskProtocolError(msg.MessageId, clockErr)); err != nil {
					return err
				}
			}
		case *protocolv1alpha2.AdapterMessage_CheckpointPrepare, *protocolv1alpha2.AdapterMessage_CheckpointFinish, *protocolv1alpha2.AdapterMessage_TaskControlResult:
			if err := env.send(taskProtocolError(msg.MessageId, task.ErrWorldNotReady)); err != nil {
				return err
			}
		case *protocolv1alpha2.AdapterMessage_Event:
			if payload.Event == nil {
				continue
			}
			if tasksNegotiated {
				if connection.world == nil {
					if err := env.send(taskProtocolError(msg.MessageId, task.ErrWorldNotReady)); err != nil {
						return err
					}
					continue
				}
				if _, ok := s.worlds.Current(*connection.world); !ok {
					if err := env.send(taskProtocolError(msg.MessageId, task.ErrWorldNotReady)); err != nil {
						return err
					}
					continue
				}
			}
			if err := s.dispatchGameEvent(env, laneStore, seenEventIDs, conn, catalog, msg.MessageId, payload.Event); err != nil {
				return err
			}

		case *protocolv1alpha2.AdapterMessage_Observation:
			if payload.Observation == nil {
				continue
			}
			env.resolveObservation(msg.CorrelationId, payload.Observation)

		case *protocolv1alpha2.AdapterMessage_ActionResult:
			if payload.ActionResult == nil {
				continue
			}
			env.resolveActionResult(payload.ActionResult.ActionId, payload.ActionResult)

		case *protocolv1alpha2.AdapterMessage_ActionStatus:
			if payload.ActionStatus == nil {
				continue
			}
			env.resolveActionStatusUpdate(payload.ActionStatus.ActionId, payload.ActionStatus)

		case *protocolv1alpha2.AdapterMessage_Error:
			if payload.Error == nil {
				continue
			}
			env.failObservation(msg.CorrelationId, adapterError{
				code:    payload.Error.Code,
				message: payload.Error.Message,
			})
		}
	}

}

func taskProtocolError(correlationID string, err error) *protocolv1alpha2.RuntimeMessage {
	return &protocolv1alpha2.RuntimeMessage{MessageId: newMessageID("task_error"), CorrelationId: correlationID, Payload: &protocolv1alpha2.RuntimeMessage_Error{Error: taskErrorToProtocol(err)}}
}

func hasInlineTaskPayload(msg *protocolv1alpha2.AdapterMessage) bool {
	return len(msg.GetEvent().GetTaskEvidence()) != 0 || len(msg.GetObservation().GetTaskEvidence()) != 0 || len(msg.GetActionResult().GetTaskEvidence()) != 0 || msg.GetActionResult().GetTaskProposal() != nil
}

// dispatchGameEvent 处理单个 GameEvent 的 admission 全流程：
//
//	Step 1  幂等：event_id 校验 + EnvironmentSession 内去重
//	Step 2  契约：resolveAgentTarget 完成 pre-turn 校验、身份解析和 canonical target 解析
//	Step 3  路由：LaneStore.GetOrCreate 按身份拿/建串行队列
//	Step 4  包装：组装 task（Run 执行 Turn / Abort 断连取消）+ admission barrier
//	Step 5  背压：非阻塞入队，队列满 REJECTED
//	Step 6  确认：先发 ACCEPTED，再记 seen，最后放行 barrier
func (s *Server) dispatchGameEvent(
	env *streamEnvironment,
	laneStore *session.LaneStore,
	seenEventIDs map[string]struct{},
	conn agent.ConnectionContext,
	catalog *tool.EnvironmentToolCatalog,
	messageID string,
	event *protocolv1alpha2.GameEvent,
) error {
	// Step 1a：event_id 是去重与 trace 关联的基础，缺失直接拒绝。
	if event.EventId == "" {
		return env.send(eventAckMessage(
			messageID,
			event.EventId,
			protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_REJECTED,
			&protocolv1alpha2.Error{
				Code:    "event_id_missing",
				Message: "GameEvent.event_id is required",
			},
		))
	}
	// Step 1b：同一 EnvironmentSession 内已接纳过该 event_id
	// → Adapter 重发/重试的同一条消息，回 DUPLICATE，不重复执行 Turn。
	// 注意 REJECTED 的事件不写 seen，Adapter 修好后重发仍能重新 admission。
	if _, ok := seenEventIDs[event.EventId]; ok {
		return env.send(eventAckMessage(
			messageID,
			event.EventId,
			protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_DUPLICATE,
			nil,
		))
	}

	// Step 2：pre-turn 校验 + 身份解析（event_type 非空 / target_entity_id /
	// 目标在 entities 内 / Resolve 三要素），任一失败都 REJECTED，
	// 不创建 lane、不创建 turn trace。
	resolved, ackErr := resolveAgentTarget(conn, event)
	if ackErr != nil {
		return env.send(eventAckMessage(
			messageID,
			event.EventId,
			protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_REJECTED,
			ackErr,
		))
	}
	key := resolved.Key

	// Step 3：按身份 key 拿/建该 Agent 的 ExecutionLane。
	// 每个 Agent 一条 lane = 同一 Agent 的事件 FIFO 串行、不同 Agent 并行。
	// GetOrCreate 失败（store 已 Close）→ environment_closed。
	lane, err := laneStore.GetOrCreate(key)
	if err != nil {
		return env.send(eventAckMessage(
			messageID,
			event.EventId,
			protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_REJECTED,
			&protocolv1alpha2.Error{
				Code:    "environment_closed",
				Message: err.Error(),
			},
		))
	}

	// Step 4：把"执行一次 Turn"包装成 task。
	// lane 只负责串行调度，不关心跑什么——Run 回调注入具体执行
	// （Observe → LLM → Action）；Admitted 是 barrier，控制 worker
	// 何时允许开跑（必须等 EventAck 发出）；Abort 处理断连时未轮到的事件。
	admitted := make(chan struct{})
	task := session.Task{
		ID:       event.EventId,
		Admitted: admitted,
		Run: func(taskCtx context.Context) {
			maintenance, canMaintain := s.agentLoop.(historyMaintainer)
			var turnEnv agent.Environment = env
			timedEnv := &historyMaintenanceEnvironment{Environment: env, event: event, currentTime: memory.CurrentGameTime(event, nil)}
			if canMaintain {
				turnEnv = timedEnv
			}
			if err := s.agentLoop.HandleEvent(taskCtx, turnEnv, conn, key, resolved.Target, catalog, event); err != nil {
				fmt.Printf("agent loop failed: %s\n", logSafeError(err))
			}
			if canMaintain {
				// HandleEvent has completed terminal persistence and released its snapshot.
				scheduledAt := time.Now()
				if err := lane.EnqueueMaintenance(session.Task{
					ID: "history-maintenance:" + event.EventId,
					Run: func(maintenanceCtx context.Context) {
						log.Printf("history maintenance owner=%s stage=queue wait_ms=%d", key.DiagnosticID(), time.Since(scheduledAt).Milliseconds())
						maintenance.MaintainHistory(maintenanceCtx, key, timedEnv.currentTime)
					},
				}); err != nil && !errors.Is(err, session.ErrLaneClosed) {
					log.Printf("schedule history maintenance owner=%s: %s", key.DiagnosticID(), logSafeError(err))
				}
			}
		},
		Abort: func(reason session.AbortReason) {
			log.Printf("abort queued game event %q: %s", event.EventId, reason)
		},
	}

	// Step 5：非阻塞入队。队列上限 = 1（1 active + 1 queued），
	// 第 3 个同 Agent 事件立即拒绝，避免积压陈旧上下文；
	// 连接正在关闭时入队失败用 environment_closed 表达。
	if err := lane.Enqueue(task); err != nil {
		ackErr := &protocolv1alpha2.Error{
			Code:    "session_queue_full",
			Message: "Runtime is still processing previous events for this agent session",
		}
		if errors.Is(err, session.ErrLaneClosed) {
			ackErr.Code = "environment_closed"
			ackErr.Message = "Runtime event lane is closed"
		}
		log.Printf("drop game event %q for %s: %s", event.EventId, key.DiagnosticID(), ackErr.Code)
		return env.send(eventAckMessage(
			messageID,
			event.EventId,
			protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_REJECTED,
			ackErr,
		))
	}

	// Step 6：确认 + 记账 + 放行，顺序不可交换：
	// ① 先发 ACCEPTED —— Adapter 先收到确认，ObserveRequest 才会发出；
	// ② 再记 seen —— ACK 发送失败则不占去重名额，Adapter 重试可重新 admission；
	// ③ 最后 close(admitted) —— 放行 worker 开始执行 task.Run。
	// EventAck 表示 Runtime 是否接收该 GameEvent；真正的 action
	// 执行结果会在后续 ActionResult 中体现。ack 成功发出后，
	// admitted 才释放，避免 ObserveRequest 早于 ACCEPTED 到达 Adapter。
	if err := env.send(eventAckMessage(
		messageID,
		event.EventId,
		protocolv1alpha2.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED,
		nil,
	)); err != nil {
		return err
	}
	seenEventIDs[event.EventId] = struct{}{}
	close(admitted)
	return nil
}

type historyMaintenanceEnvironment struct {
	agent.Environment
	event       *protocolv1alpha2.GameEvent
	currentTime *memory.GameTimeSnapshot
	observed    bool
}

func (e *historyMaintenanceEnvironment) Observe(ctx context.Context, worldID, entityID string) (*protocolv1alpha2.Observation, error) {
	observation, err := e.Environment.Observe(ctx, worldID, entityID)
	if err == nil && !e.observed && observation.GetWorldId() == worldID && observation.GetEntityId() == entityID {
		e.observed = true
		e.currentTime = memory.CurrentGameTime(e.event, observation)
	}
	return observation, err
}

func logCapabilityBootstrapDiagnostics(sessionID string, diagnostics tool.BootstrapDiagnostics) {
	log.Printf(
		"capability bootstrap session_id=%s revision=%d accepted=%d catalog=%d skipped_nil=%d invalid_names=%v invalid_schema=%v invalid_policy=%v duplicates=%v unsupported_entity_id=%q invalid_entity_id=%q",
		sessionID,
		diagnostics.CapabilityRevision,
		diagnostics.AcceptedToolCount,
		diagnostics.CatalogToolCount,
		diagnostics.SkippedNilCapabilityCount,
		diagnostics.InvalidToolNames,
		diagnostics.SkippedInvalidSchemaNames,
		diagnostics.SkippedInvalidToolPolicyNames,
		diagnostics.DuplicateToolNames,
		diagnostics.UnsupportedEntityID,
		diagnostics.InvalidEntityID,
	)
}

func logSafeError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.Join(strings.Fields(err.Error()), " ")
	if len([]rune(message)) <= 240 {
		return message
	}
	runes := []rune(message)
	return strings.TrimSpace(string(runes[:240]))
}

// resolveAgentSessionKey 保留 key-only 视图；pre-turn admission 使用
// resolveAgentTarget 同时获得 AgentSessionKey 和 canonical Target EntityRef。
// 失败返回 REJECTED 用的 *protocolv1alpha2.Error（pre-turn rejected，不创建
// lane、不创建 turn trace）。
//
//	校验 1  event_type 必须非空，但不做 game-specific allowlist
//	校验 2  target_entity_id 必须显式声明，不能靠列表顺序/类型猜
//	校验 3  目标必须真实存在于 event.Entities，防 Adapter 声明矛盾
//	校验 4  Resolve 三要素（game/world/entity）缺一 → 拒绝
func resolveAgentSessionKey(conn agent.ConnectionContext, event *protocolv1alpha2.GameEvent) (session.AgentSessionKey, *protocolv1alpha2.Error) {
	resolved, ackErr := resolveAgentTarget(conn, event)
	if ackErr != nil {
		return session.AgentSessionKey{}, ackErr
	}
	return resolved.Key, nil
}

type resolvedAgentTarget struct {
	Key    session.AgentSessionKey
	Target *protocolv1alpha2.EntityRef
}

func resolveAgentTarget(conn agent.ConnectionContext, event *protocolv1alpha2.GameEvent) (resolvedAgentTarget, *protocolv1alpha2.Error) {
	if strings.TrimSpace(event.EventType) == "" {
		return resolvedAgentTarget{}, &protocolv1alpha2.Error{
			Code:    "event_type_missing",
			Message: "GameEvent.event_type is required",
		}
	}

	// Gateway core 不解释具体 event_type；只要 GameEvent 是显式 routed
	// trigger，且满足 target/world/entity identity contract，就可以进入
	// AgentTurn。具体 trigger 语义由 Adapter / game-specific policy 解释。
	// 校验 2：typed target_entity_id 是路由依据，为空无法确定事件归属。
	targetEntityID := strings.TrimSpace(event.TargetEntityId)
	if targetEntityID == "" {
		return resolvedAgentTarget{}, &protocolv1alpha2.Error{
			Code:    "target_entity_missing",
			Message: "GameEvent.target_entity_id is required",
		}
	}
	// 校验 3：目标必须引用 entities 中已有的 EntityRef，
	// 否则会解析出一个"凭空存在"的 Agent 身份。
	target, ackErr := canonicalTargetEntity(event, targetEntityID)
	if ackErr != nil {
		return resolvedAgentTarget{}, ackErr
	}
	if target == nil {
		return resolvedAgentTarget{}, &protocolv1alpha2.Error{
			Code:    "target_entity_not_in_event",
			Message: fmt.Sprintf("target_entity_id %q is not present in GameEvent.entities", event.TargetEntityId),
		}
	}

	// 校验 4：身份 = GameScope + WorldScope + StableEntityIdentity；
	// 任一为空（如 world_id 未随事件传递）都拒绝，不用临时值兜底，
	// 否则会静默破坏"EnvironmentSession 变化不影响身份"的不变量。
	key, err := session.Resolve(strings.TrimSpace(conn.GameID), strings.TrimSpace(event.WorldId), target.GetEntityId())
	if err != nil {
		return resolvedAgentTarget{}, &protocolv1alpha2.Error{
			Code:    "identity_scope_missing",
			Message: err.Error(),
		}
	}
	return resolvedAgentTarget{Key: key, Target: target}, nil
}

func canonicalTargetEntity(event *protocolv1alpha2.GameEvent, targetEntityID string) (*protocolv1alpha2.EntityRef, *protocolv1alpha2.Error) {
	var resolved *protocolv1alpha2.EntityRef
	for _, entity := range event.GetEntities() {
		normalized := normalizeEntityRef(entity)
		if normalized == nil || normalized.GetEntityId() != targetEntityID {
			continue
		}
		if resolved == nil {
			resolved = normalized
			continue
		}
		if !sameCanonicalEntityRef(resolved, normalized) {
			return nil, &protocolv1alpha2.Error{
				Code:    "target_entity_conflict",
				Message: fmt.Sprintf("target_entity_id %q has conflicting EntityRef entries", event.TargetEntityId),
			}
		}
	}
	return resolved, nil
}

func normalizeEntityRef(entity *protocolv1alpha2.EntityRef) *protocolv1alpha2.EntityRef {
	if entity == nil {
		return nil
	}
	return &protocolv1alpha2.EntityRef{
		EntityId:     strings.TrimSpace(entity.GetEntityId()),
		EntityType:   strings.TrimSpace(entity.GetEntityType()),
		DisplayName:  strings.TrimSpace(entity.GetDisplayName()),
		DefinitionId: strings.TrimSpace(entity.GetDefinitionId()),
	}
}

func sameCanonicalEntityRef(a *protocolv1alpha2.EntityRef, b *protocolv1alpha2.EntityRef) bool {
	return a.GetEntityId() == b.GetEntityId() &&
		a.GetEntityType() == b.GetEntityType() &&
		a.GetDisplayName() == b.GetDisplayName() &&
		a.GetDefinitionId() == b.GetDefinitionId()
}

func eventAckMessage(
	correlationID string,
	eventID string,
	status protocolv1alpha2.EventAckStatus,
	err *protocolv1alpha2.Error,
) *protocolv1alpha2.RuntimeMessage {
	return &protocolv1alpha2.RuntimeMessage{
		MessageId:     newMessageID("event_ack"),
		CorrelationId: correlationID,
		Payload: &protocolv1alpha2.RuntimeMessage_EventAck{
			EventAck: &protocolv1alpha2.EventAck{
				EventId: eventID,
				Status:  status,
				Error:   err,
			},
		},
	}
}
