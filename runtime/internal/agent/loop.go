package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	agentcontext "gameagent/runtime/internal/context"
	"gameagent/runtime/internal/definition"
	"gameagent/runtime/internal/idgen"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/tool"
	"gameagent/runtime/internal/trace"
)

// Environment 定义 Agent Loop 需要的最小环境能力。
//
// Loop 只依赖这个接口，不直接依赖 gateway 或具体游戏 Adapter。
type Environment interface {
	Observe(ctx context.Context, worldID string, entityID string) (*protocolv1alpha2.Observation, error)
	SubmitAction(ctx context.Context, req *protocolv1alpha2.ActionRequest) (*protocolv1alpha2.ActionResult, error)
	StartAction(ctx context.Context, req *protocolv1alpha2.ActionRequest) (ActionStart, error)
	WaitActionResult(ctx context.Context, actionID string) (*protocolv1alpha2.ActionResult, error)
	CancelAction(actionID string, reason string)
	SendTurnCompletion(ctx context.Context, completion *protocolv1alpha2.TurnCompletion) error
}

type ActionStart struct {
	Update *protocolv1alpha2.ActionStatusUpdate
	Result *protocolv1alpha2.ActionResult
}

// Loop 执行 Runtime MVP0 的 one-turn AgentRun。
type Loop struct {
	model    model.Provider
	recorder trace.Recorder
	config   Config

	memoryStore     memory.Store
	memoryProjector memoryProjector
	definitions     definition.Catalog
	contextEngine   agentcontext.Engine
	contextRenderer agentcontext.Renderer
}

type memoryProjector interface {
	Project(memory.ProjectInput) (memory.Record, error)
}

var (
	errInvalidModelDecision = errors.New("invalid model response")
)

type LoopOption func(*Loop)

// WithMemoryStore 覆盖 Loop 默认使用的 MemoryStore。
// 主要用于测试或未来替换成持久化 Memory backend。
func WithMemoryStore(store memory.Store) LoopOption {
	return func(loop *Loop) {
		if store == nil {
			return
		}
		loop.memoryStore = store
	}
}

// WithMemoryProjector 覆盖 Loop 默认使用的 MemoryProjector。
// 主要用于测试 Memory 投影失败等 fail-open 分支。
func WithMemoryProjector(projector interface {
	Project(memory.ProjectInput) (memory.Record, error)
}) LoopOption {
	return func(loop *Loop) {
		if projector == nil {
			return
		}
		loop.memoryProjector = projector
	}
}

func WithDefinitionCatalog(catalog definition.Catalog) LoopOption {
	return func(loop *Loop) {
		loop.definitions = catalog
	}
}

type ConnectionContext struct {
	GameID    string
	SessionID string
}

// NewLoop 创建 Agent Loop。
// Phase4 在 Loop 中接入 MemoryStore、MemoryProjector、ContextBuilder 和 Renderer，
// 让一次 Agent Turn 可以读取历史 Memory 并在成功 Action 后更新 Memory。
func NewLoop(modelProvider model.Provider, recorder trace.Recorder, config Config, options ...LoopOption) *Loop {
	if recorder == nil {
		recorder = trace.NoopRecorder{}
	}
	config = config.WithDefaults()
	if err := config.Validate(); err != nil {
		panic(err)
	}

	loop := &Loop{
		model:           modelProvider,
		recorder:        recorder,
		config:          config,
		memoryStore:     newDefaultMemoryStore(config),
		memoryProjector: memory.NewProjector(nil),
		contextEngine: agentcontext.NewEngine(agentcontext.EngineConfig{
			MaxRequestTokens:              config.MaxRequestTokens,
			MaxSystemTokens:               config.MaxSystemTokens,
			MaxUserMessageTokens:          config.MaxUserMessageTokens,
			MaxDefinitionTokens:           config.MaxDefinitionTokens,
			MaxObservationTokens:          config.MaxObservationTokens,
			MaxEventTokens:                config.MaxEventTokens,
			MaxContextFactsTokens:         config.MaxContextFactsTokens,
			MaxRecentMemoryTokens:         config.MaxRecentMemoryTokens,
			MaxRecentMemoryRecords:        config.RecentMemoryLimit,
			MaxTranscriptTokens:           config.MaxTranscriptTokens,
			MaxToolCount:                  config.MaxToolCount,
			MaxToolDescriptionTokens:      config.MaxToolDescriptionTokens,
			MaxToolSchemaTokens:           config.MaxToolSchemaTokens,
			MaxTotalToolSchemaTokens:      config.MaxTotalToolSchemaTokens,
			MaxToolResultOutputTokens:     config.MaxToolResultOutputTokens,
			MaxToolResultOutputDepth:      config.MaxToolResultOutputDepth,
			MaxToolResultOutputFields:     config.MaxToolResultOutputFields,
			MaxToolResultOutputArrayItems: config.MaxToolResultOutputArrayItems,
		}),
		contextRenderer: agentcontext.NewRenderer(),
	}
	for _, option := range options {
		if option != nil {
			option(loop)
		}
	}
	return loop
}

func newDefaultMemoryStore(config Config) memory.Store {
	if config.MemoryStore.Kind == MemoryStoreKindSQLite {
		return memory.NewSQLiteMemoryStore(memory.SQLiteStoreOptions{
			Root:                          config.MemoryStore.Root,
			BusyTimeout:                   config.MemoryStore.BusyTimeout,
			MaxRecordsPerEntity:           config.MemoryStore.MaxRecordsPerEntity,
			MaxProjectionBatchesPerEntity: config.MemoryStore.MaxProjectionBatchesPerEntity,
		})
	}
	return memory.NewInMemoryStoreWithMaxRecords(defaultMemoryStoreMaxRecords(config.RecentMemoryLimit))
}

// defaultMemoryStoreMaxRecords 计算 InMemory backend 的保留上限。
// 保留数量必须不少于 RecentMemoryLimit，避免配置被 store 层隐式截断。
func defaultMemoryStoreMaxRecords(recentMemoryLimit int) int {
	return max(recentMemoryLimit, memory.DefaultMaxRecordsPerSession)
}

// HandleEvent 处理一次 GameEvent，并在需要时执行完整的 Agent Turn。
//
// 每个 Turn 先获取一次 Observation，再在预算内执行 0..N 个 AgentStep。
func (l *Loop) HandleEvent(
	ctx context.Context,
	env Environment,
	conn ConnectionContext,
	key session.AgentSessionKey,
	target *protocolv1alpha2.EntityRef,
	catalog *tool.EnvironmentToolCatalog,
	event *protocolv1alpha2.GameEvent,
) error {
	if key.EntityID == "" {
		return fmt.Errorf("agent session entity id is empty")
	}
	if key.WorldID == "" {
		return fmt.Errorf("agent session world id is empty")
	}
	if target == nil {
		return fmt.Errorf("canonical target is required")
	}
	targetEntityID := strings.TrimSpace(target.GetEntityId())
	if targetEntityID == "" {
		return fmt.Errorf("canonical target entity id is empty")
	}
	if targetEntityID != key.EntityID {
		return fmt.Errorf("target entity id %q does not match agent session entity id %q", targetEntityID, key.EntityID)
	}
	if catalog == nil {
		return fmt.Errorf("environment tool catalog is required")
	}
	toolAdmission := catalog.BuildTurnToolView(l.toolAdmissionConfig())
	toolView := toolAdmission.View
	tools := toolView.Available()
	ctx, cancelTurn := context.WithTimeout(ctx, l.config.TurnTimeout)
	defer cancelTurn()

	turnID := idgen.New("turn")
	// 为本次有效 GameEvent 创建 TurnTracer。
	turnTracer := trace.NewTurnTracerWithID(l.recorder, trace.TurnContext{
		GameID:    key.GameID,
		WorldID:   key.WorldID,
		SessionID: conn.SessionID,
		EventID:   event.EventId,
		EventType: event.EventType,
		EntityID:  key.EntityID,
	}, turnID)
	turnTracer.Emit(trace.EventTurnStarted, trace.EventData{
		Fields: trace.Fields{
			"turn_tool_count":                    len(tools),
			"turn_tool_names":                    append([]string(nil), toolAdmission.Report.AcceptedToolNames...),
			"turn_tool_names_truncated_count":    toolAdmission.Report.AcceptedToolNamesTruncatedCount,
			"dropped_tool_count":                 toolAdmission.Report.DroppedToolCount,
			"dropped_tool_names":                 append([]string(nil), toolAdmission.Report.DroppedToolNames...),
			"dropped_tool_names_truncated_count": toolAdmission.Report.DroppedToolNamesTruncatedCount,
			"dropped_tool_reason_counts":         copyStringIntMap(toolAdmission.Report.DroppedReasonCounts),
			"tool_schema_total_estimated_tokens": toolAdmission.Report.TotalSchemaEstimatedTokens,
		},
	})
	turnTracer.Emit(trace.EventObservationRequested, trace.EventData{})

	observeCtx, cancelObserve := context.WithTimeout(ctx, l.config.ObserveTimeout)
	obs, err := env.Observe(observeCtx, key.WorldID, key.EntityID)
	cancelObserve()

	if err != nil {
		reason := observationFailureReason(err)
		l.failTurn(ctx, env, turnTracer, key, event, turnID, "observation", reason, err, trace.EventData{})
		return err
	}
	turnTracer.Emit(trace.EventObservationReceived, trace.EventData{})

	descriptor := definition.NewAgentInstanceDescriptor(key, target)
	recentMemories := l.loadRecentMemories(ctx, turnTracer, key)
	return l.runBoundedSteps(ctx, env, key, target, descriptor, event, obs, recentMemories, toolView, toolAdmission.Report, turnID, turnTracer)
}

func (l *Loop) runBoundedSteps(
	ctx context.Context,
	env Environment,
	key session.AgentSessionKey,
	target *protocolv1alpha2.EntityRef,
	descriptor definition.AgentInstanceDescriptor,
	event *protocolv1alpha2.GameEvent,
	obs *protocolv1alpha2.Observation,
	recentMemories []memory.Record,
	toolView tool.TurnToolView,
	toolAdmissionReport tool.ToolAdmissionReport,
	turnID string,
	turnTracer trace.TurnTracer,
) error {
	transcript := make([]model.Message, 0)
	successfulActions := make([]completedToolAction, 0)
	totalToolCalls := 0
	asyncActionsStarted := 0
	seenToolCallIDs := make(map[string]struct{})

	for stepIndex := 1; stepIndex <= l.config.MaxSteps; stepIndex++ {
		turnTracer.Emit(trace.EventAgentStepStarted, trace.EventData{
			Fields: trace.Fields{"step_index": stepIndex},
		})

		req, buildReport, err := l.buildModelRequest(key, target, descriptor, event, obs, recentMemories, toolView, toolAdmissionReport, transcript)
		if err != nil {
			turnTracer.Emit(trace.EventContextRequestBuildFailed, trace.EventData{
				Fields: contextBuildTraceFields(stepIndex, buildReport),
			})
			turnTracer.Emit(trace.EventAgentStepFailed, trace.EventData{
				Fields: trace.Fields{"step_index": stepIndex, "reason": contextFailureReason(err)},
			})
			l.failTurn(ctx, env, turnTracer, key, event, turnID, "context", contextFailureReason(err), err, trace.EventData{
				Fields: trace.Fields{"step_index": stepIndex},
			})
			return err
		}

		turnTracer.Emit(trace.EventContextRequestBuilt, trace.EventData{
			Fields: contextBuildTraceFields(stepIndex, buildReport),
		})
		turnTracer.Emit(trace.EventModelRequestStarted, trace.EventData{
			Fields: trace.Fields{
				"tool_count":                     len(req.Tools),
				"step_index":                     stepIndex,
				"transcript":                     len(transcript),
				"max_steps":                      l.config.MaxSteps,
				"turn_budget":                    l.config.MaxToolCallsPerTurn,
				"request_total_estimated_tokens": buildReport.FinalRequestSize.TotalEstimatedTokens,
			},
		})
		modelCtx, cancelLLM := context.WithTimeout(ctx, l.config.LLMTimeout)
		rep, err := l.model.Generate(modelCtx, req)
		cancelLLM()
		if err != nil {
			reason := "provider_failed"
			if errors.Is(err, context.DeadlineExceeded) {
				reason = "provider_timeout"
			}
			turnTracer.Emit(trace.EventAgentStepFailed, trace.EventData{
				Fields: trace.Fields{"step_index": stepIndex, "reason": reason},
			})
			l.failTurn(ctx, env, turnTracer, key, event, turnID, "model", reason, err, trace.EventData{
				Fields: trace.Fields{"step_index": stepIndex},
			})
			return err
		}

		turnTracer.Emit(trace.EventModelResponseReceived, trace.EventData{
			Fields: trace.Fields{"step_index": stepIndex},
		})

		decision := rep.Decision
		if err := validateControlDirective(decision.Control); err != nil {
			turnTracer.Emit(trace.EventAgentStepFailed, trace.EventData{
				Fields: trace.Fields{"step_index": stepIndex, "reason": "invalid_model_response"},
			})
			l.failTurn(ctx, env, turnTracer, key, event, turnID, "model", "invalid_model_response", err, trace.EventData{
				Fields: trace.Fields{"step_index": stepIndex},
			})
			return err
		}

		calls := decision.ToolCalls
		if len(calls) == 0 {
			if decision.Control.Kind == model.ControlSettle {
				turnTracer.Emit(trace.EventTurnSettled, trace.EventData{
					Fields: trace.Fields{"step_index": stepIndex},
				})
				turnTracer.Emit(trace.EventAgentStepCompleted, trace.EventData{
					Fields: trace.Fields{"step_index": stepIndex},
				})
				l.updateMemoryForCompletedTurn(ctx, turnTracer, key, turnID, event, successfulActions, memory.ProjectionKindSettledTurn)
				l.completeTurn(ctx, env, turnTracer, key, event, turnID, lastCompletedActionEventData(successfulActions))
				return nil
			}
			err := fmt.Errorf("%w: continue control requires tool calls", errInvalidModelDecision)
			turnTracer.Emit(trace.EventAgentStepFailed, trace.EventData{
				Fields: trace.Fields{"step_index": stepIndex, "reason": "invalid_model_response"},
			})
			l.failTurn(ctx, env, turnTracer, key, event, turnID, "model", "invalid_model_response", err, trace.EventData{
				Fields: trace.Fields{"step_index": stepIndex},
			})
			return err
		}

		if len(calls) > l.config.MaxToolCallsPerStep {
			err := fmt.Errorf("max tool calls per step exceeded: got %d, max %d", len(calls), l.config.MaxToolCallsPerStep)
			turnTracer.Emit(trace.EventAgentStepFailed, trace.EventData{
				Fields: trace.Fields{"step_index": stepIndex, "reason": "max_tool_calls_per_step_exceeded"},
			})
			l.failTurn(ctx, env, turnTracer, key, event, turnID, "model", "max_tool_calls_per_step_exceeded", err, trace.EventData{
				Fields: trace.Fields{
					"step_index":      stepIndex,
					"tool_call_count": len(calls),
				},
			})
			return err
		}
		if totalToolCalls+len(calls) > l.config.MaxToolCallsPerTurn {
			err := fmt.Errorf("max tool calls per turn exceeded: got %d, max %d", totalToolCalls+len(calls), l.config.MaxToolCallsPerTurn)
			turnTracer.Emit(trace.EventAgentStepFailed, trace.EventData{
				Fields: trace.Fields{"step_index": stepIndex, "reason": "max_tool_calls_per_turn_exceeded"},
			})
			l.failTurn(ctx, env, turnTracer, key, event, turnID, "step", "max_tool_calls_per_turn_exceeded", err, trace.EventData{
				Fields: trace.Fields{
					"step_index":       stepIndex,
					"tool_call_count":  len(calls),
					"total_tool_calls": totalToolCalls + len(calls),
				},
			})
			return err
		}
		if duplicateID, ok := duplicateToolCallIDWithinStep(calls); ok {
			err := fmt.Errorf("%w: duplicate tool call id %q", errInvalidModelDecision, duplicateID)
			turnTracer.Emit(trace.EventAgentStepFailed, trace.EventData{
				Fields: trace.Fields{"step_index": stepIndex, "reason": "invalid_model_response"},
			})
			l.failTurn(ctx, env, turnTracer, key, event, turnID, "model", "invalid_model_response", err, trace.EventData{
				Fields: trace.Fields{
					"step_index":   stepIndex,
					"tool_call_id": duplicateID,
				},
			})
			return err
		}
		totalToolCalls += len(calls)
		idValidationResults, hasPriorStepDuplicateID := validateToolCallIDsAcrossSteps(calls, seenToolCallIDs)
		rememberToolCallIDs(calls, seenToolCallIDs)

		transcript = append(transcript, model.Message{
			Role:      model.RoleAssistant,
			ToolCalls: copyToolCallsForTranscript(calls),
		})
		for _, call := range calls {
			turnTracer.Emit(trace.EventToolCallSelected, trace.EventData{
				Tool: call.Name,
				Fields: trace.Fields{
					"step_index":   stepIndex,
					"tool_call_id": call.ID,
				},
			})
		}

		turnTracer.Emit(trace.EventToolBatchStarted, trace.EventData{
			Fields: trace.Fields{
				"step_index":         stepIndex,
				"tool_call_count":    len(calls),
				"concurrency_modes":  l.concurrencyModesForCalls(calls, toolView),
				"max_parallel_calls": l.config.MaxParallelToolCalls,
			},
		})
		if hasPriorStepDuplicateID {
			transcript = append(transcript, model.Message{
				Role:        model.RoleTool,
				ToolResults: copyToolResultsForTranscript(idValidationResults),
			})
			turnTracer.Emit(trace.EventToolBatchFailed, trace.EventData{
				Fields: trace.Fields{
					"step_index":           stepIndex,
					"tool_call_count":      len(calls),
					"tool_result_call_ids": toolResultCallIDs(idValidationResults),
				},
			})
			turnTracer.Emit(trace.EventAgentStepFailed, trace.EventData{
				Fields: trace.Fields{"step_index": stepIndex, "reason": "model_visible_tool_failure"},
			})
			continue
		}
		scheduler := l.newToolBatchScheduler(turnTracer, stepIndex, event, turnID, toolView, asyncActionsStarted >= l.config.MaxAsyncActionsPerTurn)
		outcome, err := scheduler.Run(ctx, env, key.WorldID, key.EntityID, calls)
		if err != nil {
			successfulActions = append(successfulActions, outcome.SuccessfulActions...)
			l.updateMemoryForCompletedTurn(ctx, turnTracer, key, turnID, event, successfulActions, memory.ProjectionKindPriorSuccessfulActions)
			reason := actionFailureReason(err)
			turnTracer.Emit(trace.EventToolBatchFailed, trace.EventData{
				Fields: trace.Fields{
					"step_index":      stepIndex,
					"tool_call_count": len(calls),
					"reason":          reason,
				},
			})
			turnTracer.Emit(trace.EventAgentStepFailed, trace.EventData{
				Fields: trace.Fields{"step_index": stepIndex, "reason": reason},
			})
			l.failTurn(ctx, env, turnTracer, key, event, turnID, "action", reason, err, trace.EventData{
				Fields: trace.Fields{"step_index": stepIndex},
			})
			return err
		}

		transcript = append(transcript, model.Message{
			Role:        model.RoleTool,
			ToolResults: copyToolResultsForTranscript(outcome.Results),
		})
		successfulActions = append(successfulActions, outcome.SuccessfulActions...)
		if outcome.AsyncActionStarted {
			asyncActionsStarted++
			turnTracer.Emit(trace.EventObservationRequested, trace.EventData{
				Fields: trace.Fields{"step_index": stepIndex, "reason": "async_resume"},
			})
			resumeObserveCtx, cancelResumeObserve := context.WithTimeout(ctx, l.config.ObserveTimeout)
			resumedObservation, err := env.Observe(resumeObserveCtx, key.WorldID, key.EntityID)
			cancelResumeObserve()
			if err != nil {
				l.updateMemoryForCompletedTurn(ctx, turnTracer, key, turnID, event, successfulActions, memory.ProjectionKindPriorSuccessfulActions)
				reason := observationFailureReason(err)
				turnTracer.Emit(trace.EventAgentStepFailed, trace.EventData{
					Fields: trace.Fields{"step_index": stepIndex, "reason": reason},
				})
				l.failTurn(ctx, env, turnTracer, key, event, turnID, "observation", reason, err, trace.EventData{
					Fields: trace.Fields{"step_index": stepIndex},
				})
				return err
			}
			obs = resumedObservation
			turnTracer.Emit(trace.EventObservationReceived, trace.EventData{
				Fields: trace.Fields{"step_index": stepIndex, "reason": "async_resume"},
			})
		}

		if outcome.HasModelVisibleFailure {
			turnTracer.Emit(trace.EventToolBatchFailed, trace.EventData{
				Fields: trace.Fields{
					"step_index":           stepIndex,
					"tool_call_count":      len(calls),
					"tool_result_call_ids": toolResultCallIDs(outcome.Results),
				},
			})
			turnTracer.Emit(trace.EventAgentStepFailed, trace.EventData{
				Fields: trace.Fields{"step_index": stepIndex, "reason": "model_visible_tool_failure"},
			})
			continue
		}
		turnTracer.Emit(trace.EventToolBatchCompleted, trace.EventData{
			Fields: trace.Fields{
				"step_index":           stepIndex,
				"tool_call_count":      len(calls),
				"tool_result_call_ids": toolResultCallIDs(outcome.Results),
			},
		})
		turnTracer.Emit(trace.EventAgentStepCompleted, trace.EventData{
			Fields: trace.Fields{"step_index": stepIndex},
		})
		if outcome.AsyncActionStarted {
			continue
		}
		if decision.Control.Kind == model.ControlSettle || outcome.SettleAfterSuccess {
			turnTracer.Emit(trace.EventTurnSettled, trace.EventData{
				Fields: trace.Fields{"step_index": stepIndex},
			})
			l.updateMemoryForCompletedTurn(ctx, turnTracer, key, turnID, event, successfulActions, memory.ProjectionKindSettledTurn)
			l.completeTurn(ctx, env, turnTracer, key, event, turnID, lastCompletedActionEventData(successfulActions))
			return nil
		}
	}

	err := fmt.Errorf("max steps exceeded: max %d", l.config.MaxSteps)
	turnTracer.Emit(trace.EventAgentStepFailed, trace.EventData{
		Fields: trace.Fields{"reason": "max_steps_exceeded"},
	})
	l.failTurn(ctx, env, turnTracer, key, event, turnID, "step", "max_steps_exceeded", err, trace.EventData{
		Fields: trace.Fields{
			"max_steps":        l.config.MaxSteps,
			"total_tool_calls": totalToolCalls,
		},
	})
	return err
}

func (l *Loop) completeTurn(
	ctx context.Context,
	env Environment,
	turnTracer trace.TurnTracer,
	key session.AgentSessionKey,
	event *protocolv1alpha2.GameEvent,
	turnID string,
	data trace.EventData,
) {
	l.sendTurnCompletion(ctx, env, turnTracer, key, event, turnID, protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED, nil)
	turnTracer.Complete(data)
}

func (l *Loop) failTurn(
	ctx context.Context,
	env Environment,
	turnTracer trace.TurnTracer,
	key session.AgentSessionKey,
	event *protocolv1alpha2.GameEvent,
	turnID string,
	stage string,
	reason string,
	err error,
	data trace.EventData,
) {
	l.sendTurnCompletion(ctx, env, turnTracer, key, event, turnID, protocolv1alpha2.TurnCompletionStatus_TURN_COMPLETION_STATUS_FAILED, turnCompletionError(reason, err))
	turnTracer.Fail(stage, reason, err, data)
}

func (l *Loop) sendTurnCompletion(
	ctx context.Context,
	env Environment,
	turnTracer trace.TurnTracer,
	key session.AgentSessionKey,
	event *protocolv1alpha2.GameEvent,
	turnID string,
	status protocolv1alpha2.TurnCompletionStatus,
	errDetail *protocolv1alpha2.Error,
) {
	completion := &protocolv1alpha2.TurnCompletion{
		TurnId:   turnID,
		EventId:  event.GetEventId(),
		WorldId:  key.WorldID,
		EntityId: key.EntityID,
		Status:   status,
		Error:    errDetail,
	}

	sendCtx := context.WithoutCancel(ctx)
	if err := env.SendTurnCompletion(sendCtx, completion); err != nil {
		turnTracer.Emit(trace.EventTurnCompletionSendFailed, trace.EventData{
			Fields: trace.Fields{
				"turn_completion_status": status.String(),
			},
		})
		return
	}

	turnTracer.Emit(trace.EventTurnCompletionSent, trace.EventData{
		Fields: trace.Fields{
			"turn_completion_status": status.String(),
		},
	})
}

func turnCompletionError(reason string, err error) *protocolv1alpha2.Error {
	message := reason
	if err != nil {
		message = err.Error()
	}
	return &protocolv1alpha2.Error{
		Code:    reason,
		Message: message,
	}
}

func (l *Loop) buildModelRequest(
	key session.AgentSessionKey,
	target *protocolv1alpha2.EntityRef,
	descriptor definition.AgentInstanceDescriptor,
	event *protocolv1alpha2.GameEvent,
	obs *protocolv1alpha2.Observation,
	recentMemories []memory.Record,
	toolView tool.TurnToolView,
	toolAdmissionReport tool.ToolAdmissionReport,
	transcript []model.Message,
) (model.Request, agentcontext.ContextBuildReport, error) {
	gameDefinition, agentDefinition := l.resolveDefinitions(key, descriptor)
	toolAdmissionSummary := agentcontext.ToolAdmissionSummaryFromReport(toolAdmissionReport)
	buildResult, err := l.contextEngine.Build(agentcontext.BuildInput{
		SessionKey:      key,
		CanonicalTarget: target,
		AgentDescriptor: descriptor,
		GameDefinition:  gameDefinition,
		AgentDefinition: agentDefinition,
		RuntimePolicy:   BuildSystemPrompt(l.config.Prompt),
		Event:           event,
		Observation:     obs,
		RecentMemories:  recentMemories,
		TurnToolView:    toolView,
		Transcript:      transcript,
	})
	if err != nil {
		return model.Request{}, buildResult.Report.WithToolAdmission(toolAdmissionSummary), err
	}
	report := buildResult.Report.WithToolAdmission(toolAdmissionSummary)

	req, err := l.contextRenderer.Render(buildResult.Projection)
	if err != nil {
		return model.Request{}, report, err
	}
	size, err := agentcontext.EstimateRequestTokens(req)
	if err != nil {
		return model.Request{}, report, err
	}
	report = report.WithFinalRequestSize(size)
	if agentcontext.RequestEstimatedTokensExceedBudget(size, report.EffectiveBudget) {
		// Engine should already fit successful projections to the request
		// budget. Keep this renderer-side hard gate as the final defense if
		// request formatting, tools, or controls change later.
		report = report.WithReason(agentcontext.ReasonRequiredContextOverBudget)
		if agentcontext.RequiredRequestSectionExceedsBudget(size, report.EffectiveBudget) {
			report = report.WithReason(agentcontext.ReasonRequiredSectionOverBudget)
		}
		return model.Request{}, report, agentcontext.RequestTokenBudgetError(size, report.EffectiveBudget)
	}
	return req, report, nil
}

func (l *Loop) resolveDefinitions(key session.AgentSessionKey, descriptor definition.AgentInstanceDescriptor) (*definition.GameDefinition, *definition.AgentDefinition) {
	var gameDefinition *definition.GameDefinition
	if game, ok := l.definitions.FindGame(key.GameID); ok {
		gameDefinition = &game
	}

	var agentDefinition *definition.AgentDefinition
	if descriptor.DefinitionID != "" {
		if agent, ok := l.definitions.FindAgent(key.GameID, descriptor.DefinitionID); ok {
			agentDefinition = &agent
		}
	}
	return gameDefinition, agentDefinition
}

func (l *Loop) newToolBatchScheduler(
	turnTracer trace.TurnTracer,
	stepIndex int,
	event *protocolv1alpha2.GameEvent,
	turnID string,
	toolView tool.TurnToolView,
	asyncActionLimitFull bool,
) toolBatchScheduler {
	return toolBatchScheduler{
		view:                 toolView,
		maxParallelToolCalls: l.config.MaxParallelToolCalls,
		actionTimeout:        l.config.ActionTimeout,
		actionStartTimeout:   l.config.ActionStartTimeout,
		asyncActionTimeout:   l.config.AsyncActionTimeout,
		asyncActionLimitFull: asyncActionLimitFull,
		sourceEventID:        event.GetEventId(),
		sourceTurnID:         turnID,
		onActionSubmit: func(item plannedToolCall) {
			turnTracer.Emit(trace.EventActionSubmitStarted, trace.EventData{
				ActionID: item.request.GetActionId(),
				Tool:     item.request.GetCapability(),
				Fields: trace.Fields{
					"step_index":   stepIndex,
					"tool_call_id": item.call.ID,
				},
			})
		},
		onActionStatusUpdate: func(item plannedToolCall, update *protocolv1alpha2.ActionStatusUpdate) {
			turnTracer.Emit(trace.EventActionStatusUpdateReceived, trace.EventData{
				ActionID: update.GetActionId(),
				Tool:     item.request.GetCapability(),
				Fields: trace.Fields{
					"step_index":    stepIndex,
					"tool_call_id":  item.call.ID,
					"action_status": update.GetStatus().String(),
				},
			})
			if item.entry.Execution == tool.ExecutionAsync {
				turnTracer.Emit(trace.EventTurnSuspended, trace.EventData{
					ActionID: update.GetActionId(),
					Tool:     item.request.GetCapability(),
					Fields: trace.Fields{
						"step_index":   stepIndex,
						"tool_call_id": item.call.ID,
					},
				})
			}
		},
		onActionResult: func(item plannedToolCall, result *protocolv1alpha2.ActionResult) {
			turnTracer.Emit(trace.EventActionResultReceived, trace.EventData{
				ActionID: result.GetActionId(),
				Tool:     item.request.GetCapability(),
				Fields: trace.Fields{
					"step_index":    stepIndex,
					"tool_call_id":  item.call.ID,
					"action_status": result.GetStatus().String(),
				},
			})
			if item.entry.Execution == tool.ExecutionAsync {
				turnTracer.Emit(trace.EventTurnResumed, trace.EventData{
					ActionID: result.GetActionId(),
					Tool:     item.request.GetCapability(),
					Fields: trace.Fields{
						"step_index":   stepIndex,
						"tool_call_id": item.call.ID,
					},
				})
			}
		},
	}
}

func (l *Loop) concurrencyModesForCalls(calls []model.ToolCall, toolView tool.TurnToolView) []string {
	modes := make([]string, 0, len(calls))
	for _, call := range calls {
		entry, ok := toolView.Lookup(call.Name)
		if !ok {
			modes = append(modes, "unregistered")
			continue
		}
		modes = append(modes, string(entry.Concurrency))
	}
	return modes
}

func toolResultCallIDs(results []model.ToolResult) []string {
	ids := make([]string, 0, len(results))
	for _, result := range results {
		ids = append(ids, result.ToolCallID)
	}
	return ids
}

func validateToolCallIDsAcrossSteps(calls []model.ToolCall, seen map[string]struct{}) ([]model.ToolResult, bool) {
	results := make([]model.ToolResult, len(calls))
	invalid := make([]bool, len(calls))
	hasFailure := false

	for i, call := range calls {
		id := strings.TrimSpace(call.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; !ok {
			continue
		}
		results[i] = invalidToolResult(call, toolResultCodeDuplicateToolCallID, "duplicate tool call id")
		invalid[i] = true
		hasFailure = true
	}

	if !hasFailure {
		return nil, false
	}
	for i, call := range calls {
		if invalid[i] {
			continue
		}
		results[i] = skippedToolResult(call, toolResultCodeBatchValidationFailed, "batch validation failed")
	}
	return results, true
}

func duplicateToolCallIDWithinStep(calls []model.ToolCall) (string, bool) {
	seen := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		id := strings.TrimSpace(call.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			return id, true
		}
		seen[id] = struct{}{}
	}
	return "", false
}

func rememberToolCallIDs(calls []model.ToolCall, seen map[string]struct{}) {
	for _, call := range calls {
		id := strings.TrimSpace(call.ID)
		if id == "" {
			continue
		}
		seen[id] = struct{}{}
	}
}

func validateControlDirective(control model.ControlDirective) error {
	switch control.Kind {
	case model.ControlSettle, model.ControlContinue:
		return nil
	default:
		return fmt.Errorf("%w: control is unspecified", errInvalidModelDecision)
	}
}

func (l *Loop) toolAdmissionConfig() tool.ToolAdmissionConfig {
	return tool.ToolAdmissionConfig{
		MaxToolCount:             l.config.MaxToolCount,
		MaxToolDescriptionTokens: l.config.MaxToolDescriptionTokens,
		MaxToolSchemaTokens:      l.config.MaxToolSchemaTokens,
		MaxTotalToolSchemaTokens: l.config.MaxTotalToolSchemaTokens,
	}
}

func contextBuildTraceFields(stepIndex int, report agentcontext.ContextBuildReport) trace.Fields {
	fields := trace.Fields{
		"step_index":                            stepIndex,
		"reason_codes":                          append([]string(nil), report.ReasonCodes...),
		"recent_memory_retained":                report.RecentMemory.RetainedCount,
		"recent_memory_dropped":                 report.RecentMemory.DroppedCount,
		"transcript_retained":                   report.Transcript.RetainedCount,
		"transcript_dropped":                    report.Transcript.DroppedCount,
		"accepted_tool_count":                   report.ToolAdmission.AcceptedToolCount,
		"accepted_tool_names":                   append([]string(nil), report.ToolAdmission.AcceptedToolNames...),
		"accepted_tool_names_truncated_count":   report.ToolAdmission.AcceptedToolNamesTruncatedCount,
		"dropped_tool_count":                    report.ToolAdmission.DroppedToolCount,
		"dropped_tool_names":                    append([]string(nil), report.ToolAdmission.DroppedToolNames...),
		"dropped_tool_names_truncated_count":    report.ToolAdmission.DroppedToolNamesTruncatedCount,
		"dropped_tool_reason_counts":            copyStringIntMap(report.ToolAdmission.DroppedReasonCounts),
		"tool_schema_total_estimated_tokens":    report.ToolAdmission.TotalSchemaEstimatedTokens,
		"request_total_estimated_tokens":        report.FinalRequestSize.TotalEstimatedTokens,
		"request_system_estimated_tokens":       report.FinalRequestSize.SystemEstimatedTokens,
		"request_messages_estimated_tokens":     report.FinalRequestSize.MessagesEstimatedTokens,
		"request_user_message_estimated_tokens": report.FinalRequestSize.UserMessageEstimatedTokens,
		"request_tools_estimated_tokens":        report.FinalRequestSize.ToolsEstimatedTokens,
		"request_controls_estimated_tokens":     report.FinalRequestSize.ControlsEstimatedTokens,
	}
	return fields
}

func copyStringIntMap(values map[string]int) map[string]int {
	if values == nil {
		return nil
	}
	out := make(map[string]int, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func contextFailureReason(err error) string {
	if errors.Is(err, agentcontext.ErrBudgetExceeded) {
		return agentcontext.ReasonRequiredContextOverBudget
	}
	if errors.Is(err, agentcontext.ErrInvalidInput) {
		return "context_build_failed"
	}
	return "context_render_failed"
}

func observationFailureReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "observe_timeout"
	}
	if reasoner, ok := err.(interface{ FailureReason() string }); ok {
		return reasoner.FailureReason()
	}
	return "observation_failed"
}

func actionFailureReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "action_timeout"
	}
	if strings.Contains(err.Error(), toolResultCodeNonTerminalActionState) {
		return toolResultCodeNonTerminalActionState
	}
	return "submit_action_failed"
}

func copyToolCallsForTranscript(calls []model.ToolCall) []model.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]model.ToolCall, len(calls))
	for i, call := range calls {
		out[i] = call
		out[i].Arguments = copyMapForTranscript(call.Arguments)
	}
	return out
}

func copyToolResultsForTranscript(results []model.ToolResult) []model.ToolResult {
	if len(results) == 0 {
		return nil
	}
	out := make([]model.ToolResult, len(results))
	for i, result := range results {
		out[i] = result
		out[i].Output = copyMapForTranscript(result.Output)
	}
	return out
}

func copyMapForTranscript(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

// loadRecentMemories 在模型调用前加载当前 AgentSession 的短期记忆。
// Memory 是 peripheral：读取失败不能影响 Observe -> LLM -> Action 的主链路。
func (l *Loop) loadRecentMemories(
	ctx context.Context,
	turnTracer trace.TurnTracer,
	key session.AgentSessionKey,
) []memory.Record {
	if !l.config.MemoryEnabledValue() || l.memoryStore == nil {
		turnTracer.Emit(trace.EventContextLoaded, trace.EventData{
			Fields: trace.Fields{
				"memory_enabled": false,
				"memory_count":   0,
			},
		})
		return nil
	}

	records, err := l.memoryStore.Recent(ctx, key, l.recentMemoryCandidateLimit())
	if err != nil {
		turnTracer.Emit(trace.EventContextLoadFailed, trace.EventData{
			Fields: trace.Fields{
				"memory_enabled": true,
				"reason":         err.Error(),
			},
		})
		return nil
	}

	turnTracer.Emit(trace.EventContextLoaded, trace.EventData{
		Fields: trace.Fields{
			"memory_enabled": true,
			"memory_count":   len(records),
			"memory_ids":     memoryIDs(records),
		},
	})
	return records
}

func (l *Loop) recentMemoryCandidateLimit() int {
	return max(l.config.MemoryStore.MaxRecordsPerEntity, l.config.RecentMemoryLimit)
}

func (l *Loop) updateMemoryForCompletedTurn(
	ctx context.Context,
	turnTracer trace.TurnTracer,
	key session.AgentSessionKey,
	turnID string,
	event *protocolv1alpha2.GameEvent,
	successfulActions []completedToolAction,
	projectionKind memory.ProjectionKind,
) {
	if !l.config.MemoryEnabledValue() || l.memoryStore == nil || l.memoryProjector == nil {
		return
	}

	outcomes := make([]memory.ProjectOutcome, 0, len(successfulActions))
	for _, action := range successfulActions {
		if action.ActionResult.GetStatus() != protocolv1alpha2.ActionStatus_ACTION_STATUS_SUCCEEDED {
			continue
		}
		outcomes = append(outcomes, memory.ProjectOutcome{
			ToolCall:     action.ToolCall,
			ActionResult: action.ActionResult,
		})
	}
	if len(outcomes) == 0 {
		if projectionKind != memory.ProjectionKindSettledTurn || !hasContextFacts(event) {
			return
		}
	}

	projectEvent := event
	if projectionKind == memory.ProjectionKindPriorSuccessfulActions {
		projectEvent = eventWithoutContextFacts(event)
	}

	record, err := l.memoryProjector.Project(memory.ProjectInput{
		SessionKey:     key,
		TurnID:         turnID,
		ProjectionKind: projectionKind,
		Event:          projectEvent,
		Outcomes:       outcomes,
	})
	if err != nil {
		turnTracer.Emit(trace.EventContextUpdateFailed, trace.EventData{
			Fields: trace.Fields{
				"reason": err.Error(),
			},
		})
		return
	}

	if err := l.memoryStore.Append(ctx, record); err != nil {
		turnTracer.Emit(trace.EventContextUpdateFailed, trace.EventData{
			Fields: trace.Fields{
				"memory_id":            record.MemoryID,
				"projection_kind":      string(record.ProjectionKind),
				"projection_batch_key": record.ProjectionBatchKey,
				"reason":               err.Error(),
			},
		})
		return
	}

	turnTracer.Emit(trace.EventContextUpdated, trace.EventData{
		Fields: trace.Fields{
			"memory_id":            record.MemoryID,
			"projection_kind":      string(record.ProjectionKind),
			"projection_batch_key": record.ProjectionBatchKey,
			"outcome_count":        len(record.Outcomes),
			"successful_ops":       len(outcomes),
		},
	})
}

func lastCompletedActionEventData(successfulActions []completedToolAction) trace.EventData {
	if len(successfulActions) == 0 {
		return trace.EventData{}
	}

	last := successfulActions[len(successfulActions)-1]
	return trace.EventData{
		ActionID: last.ActionResult.GetActionId(),
		Tool:     last.ToolCall.Name,
	}
}

func eventWithoutContextFacts(event *protocolv1alpha2.GameEvent) *protocolv1alpha2.GameEvent {
	if event == nil || len(event.GetContextFacts()) == 0 {
		return event
	}
	copyEvent := *event
	copyEvent.ContextFacts = nil
	return &copyEvent
}

func hasContextFacts(event *protocolv1alpha2.GameEvent) bool {
	return event != nil && len(event.GetContextFacts()) > 0
}

// memoryIDs 提取 MemoryRecord ID，用于 trace 中记录本轮加载了哪些 Memory。
func memoryIDs(records []memory.Record) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		if record.MemoryID != "" {
			ids = append(ids, record.MemoryID)
		}
	}
	return ids
}
