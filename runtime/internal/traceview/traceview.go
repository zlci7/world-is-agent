// Package traceview projects the Runtime JSONL trace into turn summaries.
//
// The trace is the only durable record of what an agent did, and the client has
// to be able to show it while the agent core is still unconfigured. This
// projection is deliberately lossy: it answers "which turns happened, who they
// involved and how they ended", not "replay this turn". Turn detail belongs to
// the trace itself.
//
// A trace is append-only and may be written by a live process, so a partially
// written trailing line is expected and is skipped rather than reported.
package traceview

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"sync"
	"time"

	"gameagent/runtime/internal/trace"
)

// Turn status values. A turn is terminal when the Runtime writes either
// turn_completed or turn_failed; a turn with neither has no recorded terminal
// state, which means the process stopped before the turn ended.
const (
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
	StatusUnfinished = "unfinished"
)

// Turn is one AgentTurn as the client sees it.
type Turn struct {
	TurnID    string    `json:"turn_id"`
	GameID    string    `json:"game_id,omitempty"`
	WorldID   string    `json:"world_id,omitempty"`
	EventID   string    `json:"event_id,omitempty"`
	EventType string    `json:"event_type,omitempty"`
	EntityID  string    `json:"entity_id,omitempty"`
	StartedAt time.Time `json:"started_at"`
	ElapsedMS int64     `json:"elapsed_ms"`
	Status    string    `json:"status"`

	// Reason and Error are set by a failed turn, for example
	// max_steps_exceeded. A completed turn has neither.
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`

	// Steps is the number of model steps the turn took.
	Steps int `json:"steps"`
	// Tools lists the tools the model selected, in order, with consecutive
	// repeats collapsed. It answers "what did it decide to do".
	Tools []string `json:"tools,omitempty"`
	// AvailableTools is the Turn Tool View snapshot recorded at turn start.
	AvailableTools []string `json:"available_tools,omitempty"`
	// SettledBy is the tool whose success ended the turn, when it completed.
	SettledBy string `json:"settled_by,omitempty"`
}

// Options bounds the projection. Both bounds exist because a trace grows without
// limit while a client only ever shows recent activity.
type Options struct {
	// MaxBytes bounds the read to the tail of the trace. Zero means no bound.
	MaxBytes int64
	// MaxTurns bounds how many of the most recent turns are retained. Zero
	// means no bound.
	MaxTurns int
	// MaxLineBytes bounds one JSONL line. Zero means the default.
	MaxLineBytes int
}

// Defaults for Options.
const (
	DefaultMaxBytes     = 32 << 20
	DefaultMaxTurns     = 200
	DefaultMaxLineBytes = 4 << 20
)

// Reader caches the projection and refreshes it when the trace file changes.
// It is safe for concurrent use.
type Reader struct {
	path    string
	options Options

	mu    sync.Mutex
	valid bool
	stamp fileStamp
	turns []Turn
}

type fileStamp struct {
	size    int64
	modTime time.Time
}

// NewReader returns a Reader over the trace at path.
func NewReader(path string, options Options) *Reader {
	if options.MaxLineBytes <= 0 {
		options.MaxLineBytes = DefaultMaxLineBytes
	}
	return &Reader{path: path, options: options}
}

// Turns returns the most recent turns, most recent first. A trace that does not
// exist yet is not an error: it means no turn has been recorded.
func (r *Reader) Turns() ([]Turn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, err := os.Stat(r.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	stamp := fileStamp{size: info.Size(), modTime: info.ModTime()}
	if r.valid && r.stamp == stamp {
		return r.turns, nil
	}

	turns, err := r.read(info.Size())
	if err != nil {
		return nil, err
	}
	r.stamp, r.turns, r.valid = stamp, turns, true
	return turns, nil
}

func (r *Reader) read(size int64) ([]Turn, error) {
	file, err := os.Open(r.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Reading only the tail can start in the middle of a line, so the first
	// line of a truncated read is discarded unconditionally.
	truncated := false
	if r.options.MaxBytes > 0 && size > r.options.MaxBytes {
		if _, err := file.Seek(size-r.options.MaxBytes, io.SeekStart); err != nil {
			return nil, err
		}
		truncated = true
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), r.options.MaxLineBytes)
	if truncated {
		scanner.Scan()
	}

	acc := newAccumulator(r.options.MaxTurns)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event trace.Event
		// A malformed line is either a torn tail or a schema the Runtime no
		// longer writes. Neither may take the whole view down.
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		acc.add(event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return acc.turns(), nil
}

type accumulator struct {
	maxTurns int
	order    []string
	byID     map[string]*turnBuilder
}

type turnBuilder struct {
	turn       Turn
	seenStart  bool
	terminated bool
}

func newAccumulator(maxTurns int) *accumulator {
	return &accumulator{maxTurns: maxTurns, byID: make(map[string]*turnBuilder)}
}

func (a *accumulator) add(event trace.Event) {
	if event.TurnID == "" {
		return
	}
	builder, ok := a.byID[event.TurnID]
	if !ok {
		builder = &turnBuilder{turn: Turn{TurnID: event.TurnID}}
		a.byID[event.TurnID] = builder
		a.order = append(a.order, event.TurnID)
		a.evict()
	}
	builder.observe(event)
}

// evict drops the oldest turns once the bound is exceeded, so a long trace does
// not make the projection grow without limit. The client only shows recent
// activity, and the map is pruned with the order slice to stay bounded too.
func (a *accumulator) evict() {
	if a.maxTurns <= 0 || len(a.order) <= a.maxTurns {
		return
	}
	drop := len(a.order) - a.maxTurns
	for _, id := range a.order[:drop] {
		delete(a.byID, id)
	}
	a.order = append(a.order[:0], a.order[drop:]...)
}

func (b *turnBuilder) observe(event trace.Event) {
	if !b.seenStart {
		b.seenStart = true
		b.turn.StartedAt = event.Time
		b.turn.GameID = event.GameID
		b.turn.WorldID = event.WorldID
		b.turn.EventID = event.EventID
		b.turn.EventType = event.EventType
		b.turn.EntityID = event.EntityID
	}
	// Later events carry the same turn metadata, but a turn whose first
	// retained line is not turn_started still gets filled in here.
	if b.turn.EventType == "" {
		b.turn.EventType = event.EventType
	}
	if b.turn.EntityID == "" {
		b.turn.EntityID = event.EntityID
	}
	if event.ElapsedMS > b.turn.ElapsedMS {
		b.turn.ElapsedMS = event.ElapsedMS
	}

	switch event.Event {
	case trace.EventTurnStarted:
		b.turn.AvailableTools = stringList(event.Fields["turn_tool_names"])
	case trace.EventAgentStepStarted:
		b.turn.Steps++
	case trace.EventToolCallSelected:
		if tool := event.Tool; tool != "" && (len(b.turn.Tools) == 0 || b.turn.Tools[len(b.turn.Tools)-1] != tool) {
			b.turn.Tools = append(b.turn.Tools, tool)
		}
	case trace.EventTurnCompleted:
		b.terminated = true
		b.turn.Status = StatusCompleted
		b.turn.SettledBy = event.Tool
		b.turn.Reason = ""
		b.turn.Error = ""
	case trace.EventTurnFailed:
		b.terminated = true
		b.turn.Status = StatusFailed
		b.turn.Reason = event.Reason
		b.turn.Error = event.ErrorMessage
	}
}

func (a *accumulator) turns() []Turn {
	turns := make([]Turn, 0, len(a.order))
	// Most recent first: a client showing a trace wants a recent-activity feed.
	for i := len(a.order) - 1; i >= 0; i-- {
		builder := a.byID[a.order[i]]
		turn := builder.turn
		if !builder.terminated {
			turn.Status = StatusUnfinished
		}
		turns = append(turns, turn)
	}
	return turns
}

// stringList reads a JSON-decoded string array defensively: the field is
// optional, and a schema change must not panic the projection.
func stringList(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}
