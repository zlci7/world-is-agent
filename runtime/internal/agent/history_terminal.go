package agent

import (
	"context"
	"time"

	"gameagent/runtime/internal/trace"
)

type turnHistoryContextKey struct{}
type terminalHistoryState struct {
	collector *turnHistoryCollector
	finished  bool
}

func turnHistoryFromContext(ctx context.Context) *terminalHistoryState {
	state, _ := ctx.Value(turnHistoryContextKey{}).(*terminalHistoryState)
	return state
}

func (l *Loop) persistTerminalHistory(ctx context.Context, tracer trace.TurnTracer, status, stage, reason string, cause error) {
	state := turnHistoryFromContext(ctx)
	if state == nil || state.finished || l.historyStore == nil || !l.config.MemoryEnabledValue() {
		return
	}
	state.finished = true
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(l.config.History.WriteTimeoutMS)*time.Millisecond)
	defer cancel()
	started := time.Now()
	source, err := l.historyStore.AppendHistory(writeCtx, state.collector.Finish(status, stage, reason, cause))
	fields := trace.Fields{"history_write_duration_ms": time.Since(started).Milliseconds(), "terminal_status": status}
	if err != nil {
		fields["reason"] = "history_write_failed"
		tracer.Emit(trace.EventContextUpdateFailed, trace.EventData{Fields: fields})
		return
	}
	fields["history_source_id"], fields["history_sequence"], fields["history_bytes"] = source.ID, source.Sequence, source.Bytes
	tracer.Emit(trace.EventContextUpdated, trace.EventData{Fields: fields})
}
