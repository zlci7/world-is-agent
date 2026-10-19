package agent

import (
	"context"
	"strings"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	agentcontext "gameagent/runtime/internal/context"
	"gameagent/runtime/internal/knowledge"
	"gameagent/runtime/internal/tool"
	"gameagent/runtime/internal/trace"
)

func WithKnowledge(service *knowledge.Service) LoopOption {
	return func(l *Loop) { l.knowledge = service }
}

func admitReadTools(catalog *tool.EnvironmentToolCatalog, config tool.ToolAdmissionConfig, entries []tool.Entry, rc tool.RuntimeCallContext) (tool.ToolAdmissionResult, error) {
	if rc.Knowledge == nil {
		return catalog.BuildTurnToolView(config), nil
	}
	registry, err := tool.NewRegistry(catalog, entries)
	if err != nil {
		return tool.ToolAdmissionResult{}, err
	}
	return registry.BuildTurnToolView(config, &rc), nil
}

func (l *Loop) prepareKnowledge(ctx context.Context, tracer trace.TurnTracer, scope knowledge.ReadScope, event *protocol.GameEvent) *agentcontext.RetrievedKnowledgeInput {
	started := time.Now()
	query := historyEventQuery(event, knowledge.MaxQueryChars)
	input := &agentcontext.RetrievedKnowledgeInput{Scope: scope}
	if strings.TrimSpace(query) == "" {
		input.Result = l.knowledge.EmptyResult()
		input.Result.Status = "skipped"
		input.Diagnostic = "knowledge_no_query"
	} else {
		var err error
		input.Result, err = l.knowledge.Search(ctx, scope, query)
		if err != nil {
			input.Diagnostic = "knowledge_query_failed"
			input.Result.Status = "unavailable"
		} else if len(input.Result.Entries) == 0 {
			input.Diagnostic = "knowledge_no_match"
		}
	}
	tracer.Emit("knowledge_retrieved", trace.EventData{Fields: trace.Fields{
		"entry_point": "automatic", "source_version": input.Result.SourceVersion, "content_hash": input.Result.ContentHash,
		"candidate_count": len(input.Result.Entries), "diagnostic": input.Diagnostic, "elapsed_ms": time.Since(started).Milliseconds(),
	}})
	return input
}
