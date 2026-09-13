package tool

import (
	"context"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/task"

	"google.golang.org/protobuf/types/known/structpb"
)

type Kind string

const (
	KindEnvironment Kind = "environment"
	KindRuntime     Kind = "runtime"
)

type ConcurrencyMode string

const (
	ConcurrencySequential   ConcurrencyMode = "sequential"
	ConcurrencyParallelSafe ConcurrencyMode = "parallel_safe"
)

type ExecutionMode string

const (
	ExecutionSync  ExecutionMode = "sync"
	ExecutionAsync ExecutionMode = "async"
)

type Entry struct {
	Definition  model.ToolDefinition
	Kind        Kind
	Concurrency ConcurrencyMode
	Execution   ExecutionMode
	Policy      ToolPolicy
	Executor    RuntimeExecutor
}

type RuntimeCallContext struct {
	Execution           task.ExecutionContext
	InteractionSourceID string
	ObservedTask        *task.Record
	AuthorityEpoch      uint64
	WakeReason          string
}

type RuntimeExecutor interface {
	Execute(context.Context, RuntimeCallContext, model.ToolCall) (model.ToolResult, error)
}

type ToolPolicy struct {
	ExclusivePerStep   bool
	SettleAfterSuccess bool
}

func concurrencyModeFromCapability(capability *protocolv1alpha2.Capability) ConcurrencyMode {
	if capability.GetConcurrencyMode() == protocolv1alpha2.CapabilityConcurrencyMode_CAPABILITY_CONCURRENCY_MODE_PARALLEL_SAFE {
		return ConcurrencyParallelSafe
	}
	return ConcurrencySequential
}

func executionModeFromCapability(capability *protocolv1alpha2.Capability) ExecutionMode {
	if capability.GetExecutionMode() == protocolv1alpha2.ExecutionMode_EXECUTION_MODE_ASYNC {
		return ExecutionAsync
	}
	return ExecutionSync
}

func toolPolicyFromCapability(capability *protocolv1alpha2.Capability) (ToolPolicy, bool) {
	extensions := capability.GetExtensions()
	if extensions == nil {
		return ToolPolicy{}, true
	}

	gameagentValue, ok := extensions.GetFields()["gameagent"]
	if !ok {
		return ToolPolicy{}, true
	}
	gameagent := gameagentValue.GetStructValue()
	if gameagent == nil {
		return ToolPolicy{}, false
	}

	toolPolicyValue, ok := gameagent.GetFields()["tool_policy"]
	if !ok {
		return ToolPolicy{}, true
	}
	toolPolicyStruct := toolPolicyValue.GetStructValue()
	if toolPolicyStruct == nil {
		return ToolPolicy{}, false
	}

	var policy ToolPolicy
	if value, ok := toolPolicyStruct.GetFields()["exclusive_per_step"]; ok {
		boolValue, ok := value.GetKind().(*structpb.Value_BoolValue)
		if !ok {
			return ToolPolicy{}, false
		}
		policy.ExclusivePerStep = boolValue.BoolValue
	}
	if value, ok := toolPolicyStruct.GetFields()["settle_after_success"]; ok {
		boolValue, ok := value.GetKind().(*structpb.Value_BoolValue)
		if !ok {
			return ToolPolicy{}, false
		}
		policy.SettleAfterSuccess = boolValue.BoolValue
	}
	return policy, true
}
