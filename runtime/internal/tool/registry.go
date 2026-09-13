package tool

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/task"
)

type Registry struct {
	tools map[string]Entry
}

func NewRegistry(environment *EnvironmentToolCatalog, runtimeEntries []Entry) (*Registry, error) {
	entries := make(map[string]Entry)
	if environment != nil {
		if len(environment.duplicateNames) > 0 {
			return nil, fmt.Errorf("duplicate environment tool names: %v", environment.duplicateNames)
		}
		entries = cloneEntries(environment.tools)
	}
	for _, entry := range runtimeEntries {
		name := entry.Definition.Name
		if _, exists := entries[name]; exists {
			return nil, fmt.Errorf("duplicate tool name: %q", name)
		}
		if !isValidCapabilityName(name) || !isObjectInputSchema(entry.Definition.InputSchema) ||
			entry.Kind != KindRuntime || entry.Execution != ExecutionSync ||
			(entry.Concurrency != ConcurrencySequential && entry.Concurrency != ConcurrencyParallelSafe) ||
			nilRuntimeExecutor(entry.Executor) {
			return nil, fmt.Errorf("invalid runtime tool entry: %q", name)
		}
		entries[name] = entry
	}
	return &Registry{tools: entries}, nil
}

func nilRuntimeExecutor(executor RuntimeExecutor) bool {
	if executor == nil {
		return true
	}
	value := reflect.ValueOf(executor)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (r *Registry) Lookup(name string) (Entry, bool) {
	entry, ok := r.tools[name]
	return entry, ok
}

func (r *Registry) BuildTurnToolView(config ToolAdmissionConfig, runtimeContext *RuntimeCallContext) ToolAdmissionResult {
	entries := make(map[string]Entry, len(r.tools))
	for name, entry := range r.tools {
		if entry.Kind == KindEnvironment || runtimeContext != nil {
			entries[name] = entry
		}
	}
	result := buildTurnToolView(entries, config)
	if runtimeContext != nil {
		captured := cloneRuntimeCallContext(*runtimeContext)
		result.View.runtimeContext = &captured
	}
	return result
}

// ExecuteRuntime resolves authority and executor exclusively from the admitted view.
func (v TurnToolView) ExecuteRuntime(ctx context.Context, call model.ToolCall) (model.ToolResult, error) {
	entry, ok := v.tools[call.Name]
	if !ok || entry.Kind != KindRuntime || v.runtimeContext == nil || nilRuntimeExecutor(entry.Executor) {
		return model.ToolResult{}, fmt.Errorf("runtime tool %q is not bound", call.Name)
	}
	return entry.Executor.Execute(ctx, cloneRuntimeCallContext(*v.runtimeContext), call)
}

// RuntimeContext returns the immutable authority snapshot captured by this view.
func (v TurnToolView) RuntimeContext() *RuntimeCallContext {
	if v.runtimeContext == nil {
		return nil
	}
	value := cloneRuntimeCallContext(*v.runtimeContext)
	return &value
}

func cloneRuntimeCallContext(value RuntimeCallContext) RuntimeCallContext {
	value.Execution.Source = cloneRuntimeSource(value.Execution.Source)
	if value.ObservedTask == nil {
		return value
	}
	record := *value.ObservedTask
	record.Spec.Contract = slices.Clone(record.Spec.Contract)
	record.Spec.Source = cloneRuntimeSource(record.Spec.Source)
	record.Progress = slices.Clone(record.Progress)
	if record.NextWakeAt != nil {
		tick := *record.NextWakeAt
		record.NextWakeAt = &tick
	}
	record.Operations = slices.Clone(record.Operations)
	for i := range record.Operations {
		record.Operations[i].Receipt = slices.Clone(record.Operations[i].Receipt)
	}
	record.Evidence = slices.Clone(record.Evidence)
	for i := range record.Evidence {
		evidence := &record.Evidence[i]
		evidence.Details = slices.Clone(evidence.Details)
		evidence.Source = cloneRuntimeSource(evidence.Source)
		if evidence.WaitUntil != nil {
			tick := *evidence.WaitUntil
			evidence.WaitUntil = &tick
		}
		if evidence.RevalidatedIn != nil {
			binding := *evidence.RevalidatedIn
			evidence.RevalidatedIn = &binding
		}
	}
	if record.Result != nil {
		result := *record.Result
		result.EvidenceRefs = slices.Clone(result.EvidenceRefs)
		result.Source = cloneRuntimeSource(result.Source)
		record.Result = &result
	}
	record.Cleanup = slices.Clone(record.Cleanup)
	value.ObservedTask = &record
	return value
}

func cloneRuntimeSource(source task.SourceRef) task.SourceRef {
	source.GameTime = slices.Clone(source.GameTime)
	source.Facts = slices.Clone(source.Facts)
	return source
}
