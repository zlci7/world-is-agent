package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
)

const (
	HistorySchemaVersion  = "phase8_2_history_v1"
	HistoryKindTerminal   = "terminal_turn"
	HistoryKindLegacy     = "legacy_recent"
	HistoryKindTaskResult = "task_result"
	HistoryVersion        = 1
	HistoryAvailable      = "available"
	HistoryPruned         = "pruned"
)

var (
	ErrInvalidHistory             = errors.New("invalid history")
	ErrHistoryConflict            = errors.New("history batch conflict")
	ErrHistoryNotFound            = errors.New("history source not found")
	ErrHistoryCapacity            = errors.New("history capacity exceeded")
	ErrHistoryMaintenanceNotReady = errors.New("history maintenance not_ready")
	ErrSummaryConflict            = errors.New("summary publication conflict")
)

type HistoryEvent struct {
	ID       string              `json:"id"`
	Type     string              `json:"type"`
	Sequence uint64              `json:"sequence"`
	GameTime *GameTimeSnapshot   `json:"game_time"`
	Facts    []SourceContextFact `json:"facts"`
}

type HistoryObservation struct {
	Step     int               `json:"step"`
	Revision uint64            `json:"revision"`
	GameTime *GameTimeSnapshot `json:"game_time"`
}

type HistoryExecution struct {
	Call          model.ToolCall         `json:"call"`
	ActionID      string                 `json:"action_id"`
	Started       bool                   `json:"started"`
	ActionResult  *protocol.ActionResult `json:"action_result,omitempty"`
	RuntimeResult *model.ToolResult      `json:"runtime_result,omitempty"`
	RuntimeError  string                 `json:"runtime_error,omitempty"`
}

type HistoryStep struct {
	Index      int                 `json:"index"`
	Decision   model.ModelDecision `json:"decision"`
	Executions []HistoryExecution  `json:"executions"`
}

type HistoryTerminal struct {
	Status string `json:"status"`
	Stage  string `json:"stage,omitempty"`
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
}

// HistoryTaskResult records one committed terminal task result. The owner comes
// from HistoryBatch, and the source event carries the facts and the game time
// the result belongs to; a result without a model turn keeps them absent.
type HistoryTaskResult struct {
	ResultID     string   `json:"result_id"`
	TaskID       string   `json:"task_id"`
	Revision     uint64   `json:"revision"`
	State        string   `json:"state"`
	Reason       string   `json:"reason"`
	OccurredAt   int64    `json:"occurred_at"`
	EvidenceRefs []string `json:"evidence_refs"`
}

type HistoryBatch struct {
	Owner        session.AgentSessionKey `json:"owner"`
	Kind         string                  `json:"kind"`
	Version      int                     `json:"version"`
	TurnID       string                  `json:"turn_id"`
	Event        HistoryEvent            `json:"event"`
	Observations []HistoryObservation    `json:"observations"`
	Steps        []HistoryStep           `json:"steps"`
	Terminal     HistoryTerminal         `json:"terminal"`
	Legacy       *Record                 `json:"legacy,omitempty"`
	TaskResult   *HistoryTaskResult      `json:"task_result,omitempty"`
}

type HistoryTime struct {
	Source string            `json:"source"`
	Step   int               `json:"step"`
	Value  *GameTimeSnapshot `json:"value"`
}

type HistorySource struct {
	ID             string
	Sequence       int64
	Owner          session.AgentSessionKey
	BatchKey       string
	Fingerprint    string
	CreatedAt      time.Time
	Times          []HistoryTime
	Batch          *HistoryBatch
	Bytes          int
	Availability   string
	LegacyMemoryID string
}

type HistorySnapshot struct {
	Owner           session.AgentSessionKey
	Watermark       int64
	SummaryRevision int64
	LeaseID         string
}

type HistoryReadLimits struct {
	Records int
	Bytes   int
}

// Pages are newest first; Before is exclusive and zero starts at the snapshot watermark.
type HistoryPage struct {
	Sources     []HistorySource
	NextBefore  int64
	More        bool
	Scanned     int
	Bytes       int
	Diagnostics []string
}

type HistoryStore interface {
	AppendHistory(context.Context, HistoryBatch) (HistorySource, error)
	BeginHistorySnapshot(context.Context, session.AgentSessionKey) (HistorySnapshot, error)
	ReleaseHistorySnapshot(HistorySnapshot)
	ReadHistorySnapshot(context.Context, HistorySnapshot, int64, HistoryReadLimits) (HistoryPage, error)
	ReadHistorySource(context.Context, session.AgentSessionKey, string) (HistorySource, error)
}

type HistoryLimits struct {
	MaxBatchBytes      int `json:"max_batch_bytes"`
	PageRecords        int `json:"page_records"`
	PageBytes          int `json:"page_bytes"`
	ScanRecords        int `json:"scan_records"`
	ScanBytes          int `json:"scan_bytes"`
	ReadTimeoutMS      int `json:"read_timeout_ms"`
	WriteTimeoutMS     int `json:"write_timeout_ms"`
	SummarySources     int `json:"summary_sources"`
	SummaryCheckpoints int `json:"summary_checkpoints"`
}

// DefaultHistoryLimits returns the limits a store uses when the configuration
// does not name them.
//
// ReadTimeoutMS is sized against SummarySources rather than chosen on its own.
// Reading the largest coverage a summary may have costs roughly 0.06ms per
// source, so the full 16384-source cap costs about a second on a local database
// and a 1000ms budget sat at 100% of it: the read succeeded or failed on
// scheduling noise. That did not surface as an error, because the Runtime reports
// a read that runs out of budget as the summary_read_timeout diagnostic and serves
// the turn without a summary, so the symptom is history that quietly stops being
// compacted. This budget keeps the declared capacity reachable on a machine of
// that speed, with the cost of one read spent in the background rather than in a
// turn.
func DefaultHistoryLimits() HistoryLimits {
	return HistoryLimits{MaxBatchBytes: 8 << 20, PageRecords: 64, PageBytes: 8 << 20, ScanRecords: 512, ScanBytes: 32 << 20, ReadTimeoutMS: 2000, WriteTimeoutMS: 5000, SummarySources: 16384, SummaryCheckpoints: 64}
}

func CanonicalHistoryBatch(batch HistoryBatch, maxBytes int) ([]byte, string, string, error) {
	if _, err := session.Resolve(batch.Owner.GameID, batch.Owner.WorldID, batch.Owner.EntityID); err != nil || batch.Version != HistoryVersion {
		return nil, "", "", ErrInvalidHistory
	}
	var keyParts []any
	switch batch.Kind {
	case HistoryKindTerminal:
		if batch.TurnID == "" || batch.Legacy != nil || batch.TaskResult != nil ||
			(batch.Terminal.Status != "completed" && batch.Terminal.Status != "failed" && batch.Terminal.Status != "cancelled") {
			return nil, "", "", ErrInvalidHistory
		}
		keyParts = []any{batch.Owner.GameID, batch.Owner.WorldID, batch.Owner.EntityID, batch.TurnID, batch.Event.ID, batch.Kind, batch.Version}
	case HistoryKindLegacy:
		if batch.Legacy == nil || batch.Legacy.SessionKey != batch.Owner || validateSQLiteRecord(*batch.Legacy) != nil {
			return nil, "", "", ErrInvalidHistory
		}
		if batch.TaskResult != nil {
			return nil, "", "", ErrInvalidHistory
		}
		keyParts = []any{batch.Owner.GameID, batch.Owner.WorldID, batch.Owner.EntityID, batch.Legacy.ProjectionBatchKey, batch.Kind, batch.Version}
	case HistoryKindTaskResult:
		result := batch.TaskResult
		if result == nil || batch.Legacy != nil || !validTaskResultBatch(batch) {
			return nil, "", "", ErrInvalidHistory
		}
		keyParts = []any{batch.Owner.GameID, batch.Owner.WorldID, batch.Owner.EntityID, result.ResultID, batch.Kind, batch.Version}
	default:
		return nil, "", "", ErrInvalidHistory
	}
	data, err := json.Marshal(batch)
	if err != nil {
		return nil, "", "", fmt.Errorf("%w: %v", ErrInvalidHistory, err)
	}
	if maxBytes > 0 && len(data) > maxBytes {
		return nil, "", "", fmt.Errorf("%w: batch bytes %d exceed %d", ErrHistoryCapacity, len(data), maxBytes)
	}
	key, err := json.Marshal(keyParts)
	if err != nil {
		return nil, "", "", err
	}
	return data, string(key), sha256LowerHex(string(data)), nil
}

// A task result batch carries no model turn: the result, its stable identity and
// the game time are the whole source. Confirmed terminal states only.
func validTaskResultBatch(batch HistoryBatch) bool {
	result := batch.TaskResult
	if result.ResultID == "" || result.TaskID == "" || result.Revision == 0 || result.OccurredAt <= 0 || batch.Event.GameTime == nil {
		return false
	}
	switch result.State {
	case "succeeded", "failed", "cancelled":
	default:
		return false
	}
	for _, ref := range result.EvidenceRefs {
		if ref == "" {
			return false
		}
	}
	return batch.Terminal == HistoryTerminal{} && len(batch.Steps) == 0 && len(batch.Observations) == 0
}

// historyKeySegments keeps the canonical batch key segment count per kind, so a
// trimmed record is still identifiable after its payload is pruned.
func historyKeySegments(kind string) int {
	switch kind {
	case HistoryKindTerminal:
		return 7
	case HistoryKindLegacy, HistoryKindTaskResult:
		return 6
	default:
		return 0
	}
}

func (l HistoryLimits) WithDefaults() HistoryLimits {
	d := DefaultHistoryLimits()
	for _, p := range []struct {
		value    *int
		fallback int
	}{{&l.MaxBatchBytes, d.MaxBatchBytes}, {&l.PageRecords, d.PageRecords}, {&l.PageBytes, d.PageBytes}, {&l.ScanRecords, d.ScanRecords}, {&l.ScanBytes, d.ScanBytes}, {&l.ReadTimeoutMS, d.ReadTimeoutMS}, {&l.WriteTimeoutMS, d.WriteTimeoutMS}, {&l.SummarySources, d.SummarySources}, {&l.SummaryCheckpoints, d.SummaryCheckpoints}} {
		if *p.value == 0 {
			*p.value = p.fallback
		}
	}
	return l
}

func (l HistoryLimits) Validate() error {
	for _, value := range []int{l.MaxBatchBytes, l.PageRecords, l.PageBytes, l.ScanRecords, l.ScanBytes, l.ReadTimeoutMS, l.WriteTimeoutMS, l.SummarySources, l.SummaryCheckpoints} {
		if value <= 0 {
			return fmt.Errorf("history limits must be positive")
		}
	}
	if l.PageRecords > l.ScanRecords || l.PageBytes > l.ScanBytes {
		return fmt.Errorf("history page exceeds total scan limits")
	}
	return nil
}

func HistoryTimes(batch HistoryBatch) []HistoryTime {
	if batch.Legacy != nil {
		return []HistoryTime{{Source: "legacy_event", Value: batch.Legacy.GameTime}}
	}
	times := []HistoryTime{{Source: "event", Value: batch.Event.GameTime}}
	for _, obs := range batch.Observations {
		times = append(times, HistoryTime{Source: "observation", Step: obs.Step, Value: obs.GameTime})
	}
	return times
}

// Visibility is checked per source pair; an unknown source cannot hide a known future source.
func HistoryVisibility(times []HistoryTime, current *GameTimeSnapshot) (visible, unknown bool) {
	visible = true
	if len(times) == 0 {
		return true, true
	}
	for _, source := range times {
		basis := SharedGameTimeBasis(source.Value, current)
		unknown = unknown || basis == GameTimeUnknown
		if basis.Compare(source.Value, current) > 0 {
			visible = false
		}
	}
	return visible, unknown
}

func CloneHistoryBatch(batch HistoryBatch) (HistoryBatch, error) {
	data, err := json.Marshal(batch)
	if err != nil {
		return HistoryBatch{}, err
	}
	return decodeHistoryBatch(data)
}

func decodeHistoryBatch(data []byte) (HistoryBatch, error) {
	var batch HistoryBatch
	err := decodeHistoryJSON(data, &batch)
	return batch, err
}

func decodeHistoryJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: multiple JSON values", ErrInvalidHistory)
	}
	return nil
}
