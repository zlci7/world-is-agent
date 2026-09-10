package agent

import (
	"cmp"
	"context"
	"errors"
	"reflect"
	"slices"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"

	"google.golang.org/protobuf/proto"
)

type turnHistoryCollector struct {
	batch memory.HistoryBatch
}

func newTurnHistoryCollector(key session.AgentSessionKey, turnID string, event *protocol.GameEvent) *turnHistoryCollector {
	collector := &turnHistoryCollector{batch: memory.HistoryBatch{
		Owner: key, Kind: memory.HistoryKindTerminal, Version: memory.HistoryVersion, TurnID: turnID,
		Event: memory.HistoryEvent{
			ID: event.GetEventId(), Type: event.GetEventType(), Sequence: event.GetSequence(),
			GameTime: memory.SnapshotGameTime(event.GetGameTime()),
		},
	}}
	for _, fact := range event.GetContextFacts() {
		if fact == nil {
			continue
		}
		collector.batch.Event.Facts = append(collector.batch.Event.Facts, memory.SourceContextFact{
			Kind: fact.GetKind(), ActorEntityID: fact.GetActorEntityId(), TargetEntityID: fact.GetTargetEntityId(),
			ScopeID: fact.GetScopeId(), Text: fact.GetText(), Label: fact.GetLabel(), Attributes: fact.GetAttributes().AsMap(),
		})
	}
	return collector
}

func (c *turnHistoryCollector) Observe(step int, obs *protocol.Observation) {
	if obs != nil {
		c.batch.Observations = append(c.batch.Observations, memory.HistoryObservation{
			Step: step, Revision: obs.GetRevision(), GameTime: memory.SnapshotGameTime(obs.GetGameTime()),
		})
	}
}

func (c *turnHistoryCollector) Decision(step int, decision model.ModelDecision) {
	c.step(step).Decision = cloneHistoryDecision(decision)
}

func (c *turnHistoryCollector) Executions(step int, executions []memory.HistoryExecution) {
	if len(executions) == 0 {
		return
	}
	target := c.step(step)
	for _, execution := range executions {
		target.Executions = append(target.Executions, cloneHistoryExecution(execution))
	}
}

func (c *turnHistoryCollector) Finish(status, stage, reason string, err error) memory.HistoryBatch {
	batch := c.batch
	batch.Event.GameTime = cloneHistoryTime(batch.Event.GameTime)
	batch.Event.Facts = slices.Clone(batch.Event.Facts)
	for i := range batch.Event.Facts {
		batch.Event.Facts[i].Attributes = cloneHistoryMap(batch.Event.Facts[i].Attributes)
	}
	batch.Observations = slices.Clone(batch.Observations)
	for i := range batch.Observations {
		batch.Observations[i].GameTime = cloneHistoryTime(batch.Observations[i].GameTime)
	}
	slices.SortStableFunc(batch.Observations, func(a, b memory.HistoryObservation) int { return cmp.Compare(a.Step, b.Step) })
	batch.Steps = slices.Clone(batch.Steps)
	for i := range batch.Steps {
		step := &batch.Steps[i]
		step.Decision = cloneHistoryDecision(step.Decision)
		step.Executions = slices.Clone(step.Executions)
		for j := range step.Executions {
			step.Executions[j] = cloneHistoryExecution(step.Executions[j])
		}
	}
	slices.SortFunc(batch.Steps, func(a, b memory.HistoryStep) int { return cmp.Compare(a.Index, b.Index) })
	batch.Terminal = memory.HistoryTerminal{Status: status, Stage: stage, Reason: reason}
	// Provider and context errors can contain prompts; retain source diagnostics only.
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			batch.Terminal.Error = context.Canceled.Error()
		case errors.Is(err, context.DeadlineExceeded):
			batch.Terminal.Error = context.DeadlineExceeded.Error()
		default:
			batch.Terminal.Error = reason
			if batch.Terminal.Error == "" {
				batch.Terminal.Error = "turn failed"
			}
		}
	}
	return batch
}

func (c *turnHistoryCollector) step(index int) *memory.HistoryStep {
	for i := range c.batch.Steps {
		if c.batch.Steps[i].Index == index {
			return &c.batch.Steps[i]
		}
	}
	c.batch.Steps = append(c.batch.Steps, memory.HistoryStep{Index: index})
	return &c.batch.Steps[len(c.batch.Steps)-1]
}

func cloneHistoryTime(value *memory.GameTimeSnapshot) *memory.GameTimeSnapshot {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneHistoryDecision(decision model.ModelDecision) model.ModelDecision {
	decision.ToolCalls = slices.Clone(decision.ToolCalls)
	for i := range decision.ToolCalls {
		decision.ToolCalls[i] = cloneHistoryCall(decision.ToolCalls[i])
	}
	return decision
}

func cloneHistoryCall(call model.ToolCall) model.ToolCall {
	call.Arguments = cloneHistoryMap(call.Arguments)
	return call
}

func cloneHistoryExecution(execution memory.HistoryExecution) memory.HistoryExecution {
	execution.Call = cloneHistoryCall(execution.Call)
	if execution.ActionResult != nil {
		execution.ActionResult = proto.Clone(execution.ActionResult).(*protocol.ActionResult)
	}
	if execution.RuntimeResult != nil {
		result := *execution.RuntimeResult
		result.Output = cloneHistoryMap(result.Output)
		execution.RuntimeResult = &result
	}
	return execution
}

func cloneHistoryMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	return cloneHistoryValue(reflect.ValueOf(values)).Interface().(map[string]any)
}

// JSON containers may be typed by a provider; scalar copies retain integer precision.
func cloneHistoryValue(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return value
		}
		out := reflect.New(value.Type()).Elem()
		out.Set(cloneHistoryValue(value.Elem()))
		return out
	case reflect.Map:
		if value.IsNil() {
			return value
		}
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), cloneHistoryValue(iter.Value()))
		}
		return out
	case reflect.Slice, reflect.Array:
		var out reflect.Value
		if value.Kind() == reflect.Slice {
			if value.IsNil() {
				return value
			}
			out = reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		} else {
			out = reflect.New(value.Type()).Elem()
		}
		for i := 0; i < value.Len(); i++ {
			out.Index(i).Set(cloneHistoryValue(value.Index(i)))
		}
		return out
	default:
		return value
	}
}
