package tool

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/task"
)

type registryExecutor func(context.Context, RuntimeCallContext, model.ToolCall) (model.ToolResult, error)

func (f registryExecutor) Execute(ctx context.Context, rc RuntimeCallContext, call model.ToolCall) (model.ToolResult, error) {
	return f(ctx, rc, call)
}

func runtimeTestEntry(name string) Entry {
	return Entry{Definition: model.ToolDefinition{Name: name, InputSchema: `{"type":"object"}`}, Kind: KindRuntime, Execution: ExecutionSync, Concurrency: ConcurrencySequential,
		Executor: registryExecutor(func(_ context.Context, rc RuntimeCallContext, call model.ToolCall) (model.ToolResult, error) {
			return model.ToolResult{ToolCallID: call.ID, Name: call.Name, Status: "succeeded", Output: map[string]any{"source": rc.InteractionSourceID}}, nil
		})}
}

func TestRegistryMixedImmutableBindings(t *testing.T) {
	env, _, err := BuildEnvironmentToolCatalog(&protocol.CapabilityList{Capabilities: []*protocol.Capability{{Name: "alpha", InputSchemaJson: `{"type":"object"}`}}})
	if err != nil {
		t.Fatal(err)
	}
	entries := []Entry{runtimeTestEntry("zeta"), runtimeTestEntry("beta")}
	registry, err := NewRegistry(env, entries)
	if err != nil {
		t.Fatal(err)
	}
	tick := int64(7)
	rc := RuntimeCallContext{InteractionSourceID: "original", Execution: task.ExecutionContext{Source: task.SourceRef{Facts: json.RawMessage(`{"fact":1}`)}}, ObservedTask: &task.Record{ID: "task-original", NextWakeAt: &tick, Operations: []task.Operation{{Receipt: json.RawMessage(`{"ok":1}`)}}, Result: &task.Result{EvidenceRefs: []string{"evidence-original"}}}}
	view := registry.BuildTurnToolView(ToolAdmissionConfig{}, &rc).View
	if got := toolNames(view.Available()); !reflect.DeepEqual(got, []string{"alpha", "beta", "zeta"}) {
		t.Fatalf("names = %v", got)
	}
	if got := toolNames(registry.BuildTurnToolView(ToolAdmissionConfig{}, nil).View.Available()); !reflect.DeepEqual(got, []string{"alpha"}) {
		t.Fatalf("nil context names = %v", got)
	}
	entries[0].Executor = nil
	rc.InteractionSourceID = "changed"
	rc.ObservedTask.ID = "changed"
	tick = 99
	rc.Execution.Source.Facts[0] = 'X'
	rc.ObservedTask.Operations[0].Receipt[0] = 'X'
	rc.ObservedTask.Result.EvidenceRefs[0] = "changed"
	entry, _ := view.Lookup("zeta")
	result, err := view.ExecuteRuntime(context.Background(), model.ToolCall{ID: "call", Name: "zeta", Arguments: map[string]any{"executor": "other"}})
	if err != nil || result.Output["source"] != "original" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	entry.Executor = nil
	if _, err := view.ExecuteRuntime(context.Background(), model.ToolCall{Name: "zeta"}); err != nil {
		t.Fatal(err)
	}
	if _, err := view.ExecuteRuntime(context.Background(), model.ToolCall{Name: "alpha"}); err == nil {
		t.Fatal("environment entry executed locally")
	}
	if _, ok := registry.Lookup("zeta"); !ok {
		t.Fatal("runtime lookup missing")
	}
	// A second executor observes all nested authority values and mutates its own copy.
	checker := runtimeTestEntry("check")
	checker.Executor = registryExecutor(func(_ context.Context, captured RuntimeCallContext, _ model.ToolCall) (model.ToolResult, error) {
		if captured.ObservedTask.ID != "task-original" || *captured.ObservedTask.NextWakeAt != 7 || string(captured.Execution.Source.Facts) != `{"fact":1}` || string(captured.ObservedTask.Operations[0].Receipt) != `{"ok":1}` || captured.ObservedTask.Result.EvidenceRefs[0] != "evidence-original" {
			t.Fatalf("authority changed: %+v", captured)
		}
		captured.ObservedTask.ID = "executor-mutated"
		return model.ToolResult{Status: "succeeded"}, nil
	})
	// Reconstruct from the original values to test executor-call isolation as well.
	tick = 7
	rc.InteractionSourceID = "original"
	rc.ObservedTask.ID = "task-original"
	rc.Execution.Source.Facts = json.RawMessage(`{"fact":1}`)
	rc.ObservedTask.Operations[0].Receipt = json.RawMessage(`{"ok":1}`)
	rc.ObservedTask.Result.EvidenceRefs[0] = "evidence-original"
	checkRegistry, _ := NewRegistry(nil, []Entry{checker})
	checkView := checkRegistry.BuildTurnToolView(ToolAdmissionConfig{}, &rc).View
	rc.ObservedTask.ID = "external-mutated"
	tick = 99
	rc.Execution.Source.Facts[0] = 'X'
	rc.ObservedTask.Operations[0].Receipt[0] = 'X'
	rc.ObservedTask.Result.EvidenceRefs[0] = "changed"
	for range 2 {
		if _, err := checkView.ExecuteRuntime(context.Background(), model.ToolCall{Name: "check"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRegistryRejectsDuplicatesAndInvalidRuntimeEntries(t *testing.T) {
	env, _, _ := BuildEnvironmentToolCatalog(&protocol.CapabilityList{Capabilities: []*protocol.Capability{{Name: "same", InputSchemaJson: `{}`}}})
	duplicateEnv, _, _ := BuildEnvironmentToolCatalog(&protocol.CapabilityList{Capabilities: []*protocol.Capability{{Name: "dup", InputSchemaJson: `{}`}, {Name: "dup", InputSchemaJson: `{}`}}})
	if _, err := NewRegistry(duplicateEnv, nil); err == nil {
		t.Fatal("environment duplicates accepted")
	}
	tests := []struct {
		name    string
		env     *EnvironmentToolCatalog
		entries []Entry
	}{
		{"runtime duplicate", nil, []Entry{runtimeTestEntry("same"), runtimeTestEntry("same")}},
		{"cross source duplicate", env, []Entry{runtimeTestEntry("same")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewRegistry(tt.env, tt.entries); err == nil {
				t.Fatal("duplicate accepted")
			}
		})
	}
	invalid := []func(*Entry){func(e *Entry) { e.Definition.Name = " " }, func(e *Entry) { e.Definition.InputSchema = `[]` }, func(e *Entry) { e.Kind = KindEnvironment }, func(e *Entry) { e.Executor = nil }, func(e *Entry) { var f registryExecutor; e.Executor = f }, func(e *Entry) { e.Execution = ExecutionAsync }, func(e *Entry) { e.Concurrency = "invalid" }}
	for i, mutate := range invalid {
		entry := runtimeTestEntry("local")
		mutate(&entry)
		if _, err := NewRegistry(nil, []Entry{entry}); err == nil {
			t.Errorf("invalid entry %d accepted", i)
		}
	}
}

func TestRegistryAdmissionBudgetParity(t *testing.T) {
	for _, config := range []ToolAdmissionConfig{{MaxToolCount: 1}, {MaxToolDescriptionTokens: 1}, {MaxToolSchemaTokens: 1}, {MaxTotalToolSchemaTokens: 5}} {
		caps := []*protocol.Capability{{Name: "alpha", Description: strings.Repeat("word ", 10), InputSchemaJson: `{"type":"object"}`}, {Name: "beta", InputSchemaJson: `{"type":"object"}`}}
		env, _, _ := BuildEnvironmentToolCatalog(&protocol.CapabilityList{Capabilities: caps})
		entries := []Entry{runtimeTestEntry("beta"), runtimeTestEntry("alpha")}
		entries[1].Definition.Description = caps[0].Description
		registry, err := NewRegistry(nil, entries)
		if err != nil {
			t.Fatal(err)
		}
		got := registry.BuildTurnToolView(config, &RuntimeCallContext{})
		want := env.BuildTurnToolView(config)
		if !reflect.DeepEqual(got.Report, want.Report) || !reflect.DeepEqual(got.View.Available(), want.View.Available()) {
			t.Fatalf("budget parity: got %+v, want %+v", got, want)
		}
		if got.Report.DroppedToolCount == 0 {
			t.Fatalf("test budget did not drop tools: %+v", config)
		}
	}
}
