package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/llm"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	historyAcceptanceAddress             = "127.0.0.1:50051"
	historyAcceptanceKeyEnv              = "GAMEAGENT_HISTORY_ACCEPTANCE_LOCAL_KEY"
	historyAcceptancePast                = "青禾说：我不喜欢雨天，昨天提到的数字是7319。"
	historyAcceptanceNPC                 = "守林说：我喜欢雨天，但我没有答应代送包裹。"
	historyAcceptanceClaim               = "青禾说：我已经把包裹交给阿岚。"
	historyAcceptanceFailure             = "包裹投递失败，收件人不在场，包裹没有送达。"
	historyAcceptanceRecent              = "青禾说：我把蓝色雨伞放在门口，编号9462。"
	historyAcceptanceBack                = "青禾说：我把白色便签留在门边，编号5824。本轮无需动作。"
	historyAcceptanceSummary             = "青禾说自己不喜欢雨天，并提到数字7319。守林说自己喜欢雨天，但没有答应代送包裹。青禾声称已经把包裹交给阿岚；模型曾请求投递包裹，游戏回执确认投递失败、包裹没有送达，收件人不在场。日志记录木箱保持原样，没有新增投递。"
	historyAcceptanceLLMTimeoutMS        = 8000
	historyAcceptanceSummaryTokens       = 2048
	historyAcceptanceCompactionTimeoutMS = 10000
)

var historyAcceptanceOwner = session.AgentSessionKey{
	GameID: "history-acceptance", WorldID: "world:controlled", EntityID: "agent:shoulin",
}

// Both gates are required for real calls. Fake uses the production DeepSeek
// HTTP encoder/decoder against a loopback fixture; neither path replaces main.
func TestHistoryServerAcceptance(t *testing.T) {
	if os.Getenv("GAMEAGENT_HISTORY_SERVER_ACCEPTANCE") != "1" {
		t.Skip("opt in with GAMEAGENT_HISTORY_SERVER_ACCEPTANCE=1; requires free 127.0.0.1:50051")
	}
	t.Run("fake", func(t *testing.T) { runHistoryServerAcceptance(t, false) })
	t.Run("real", func(t *testing.T) {
		if os.Getenv("GAMEAGENT_HISTORY_REAL_ACCEPTANCE") != "1" {
			t.Skip("real DeepSeek additionally requires GAMEAGENT_HISTORY_REAL_ACCEPTANCE=1")
		}
		runHistoryServerAcceptance(t, true)
	})
}

type historyAcceptanceWire struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

type historyAcceptanceProjection struct {
	SummaryID   string `json:"summary_id"`
	SummaryText string `json:"summary_text"`
	Sources     []struct {
		ID       string              `json:"source_id"`
		Sequence int64               `json:"sequence"`
		Complete bool                `json:"complete"`
		Content  memory.HistoryBatch `json:"content"`
	} `json:"sources"`
}

type historyAcceptanceCall struct {
	Body    []byte
	Summary bool
}

type historyAcceptanceHTTP struct {
	mu     sync.Mutex
	active sync.WaitGroup
	calls  []historyAcceptanceCall
	errs   []string
}

func runHistoryServerAcceptance(t *testing.T, real bool) {
	t.Helper()
	mode := "fake"
	if real {
		mode = "real"
	}
	reportDir := historyAcceptanceReportDirectory(t, mode)
	if err := historyAcceptanceCheckPort(historyAcceptanceAddress); err != nil {
		t.Fatalf("BLOCKED: %s is occupied or unavailable; existing processes were left untouched", historyAcceptanceAddress)
	}
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	config := llm.Config{Provider: "deepseek", Model: "controlled-history",
		WindowLimits: model.WindowLimits{ContextTokens: 32768, OutputTokens: 2048},
	}
	var upstream, apiKey string
	if real {
		config, err = llm.LoadConfig(filepath.Join(repo, "runtime", "config", "model.json"))
		if err != nil {
			t.Fatal("BLOCKED: cannot read repository runtime/config/model.json")
		}
		upstream, err = historyAcceptanceUpstream(config)
		if err != nil {
			t.Fatal(err)
		}
		if config.WindowLimits.Validate() != nil || config.ContextTokens == 0 || config.OutputTokens < historyAcceptanceSummaryTokens {
			t.Fatal("BLOCKED: configured model window must support compaction and at least 2048 output tokens")
		}
		if !strings.HasPrefix(config.APIKey, "env:") {
			t.Fatal("BLOCKED: real API credentials must reference a process environment variable")
		}
		apiKey = strings.TrimSpace(os.Getenv(strings.TrimSpace(strings.TrimPrefix(config.APIKey, "env:"))))
		if apiKey == "" {
			t.Fatal("BLOCKED: the API credential referenced by model.json is absent from the process environment")
		}
	}

	binary := filepath.Join(root, "history-server")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelBuild()
	build := exec.CommandContext(buildCtx, "go", "build", "-o", binary, "./runtime/cmd/server")
	build.Dir = repo
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build production server: %v\n%s", err, output)
	}

	store := memory.NewSQLiteHistoryStore(memory.SQLiteStoreOptions{Root: filepath.Join(root, "memory")}, memory.HistoryLimits{})
	seeds := historyAcceptanceSeed(t, store)
	var checkpoint *memory.SummaryCheckpoint
	var completed []memory.HistorySource
	pids := map[int]bool{}
	for phaseIndex, phase := range []struct {
		name string
		tick int64
		text string
	}{
		{"compact", 130, "青禾说：请核对已有对话与包裹结果，本轮无需动作。"},
		{"rollback", 100, historyAcceptanceBack},
		{"catchup", 130, "青禾说：请核对已有对话、雨伞与便签，本轮无需动作。"},
	} {
		if !t.Run(phase.name, func(t *testing.T) {
			if len(completed) != phaseIndex {
				t.Fatal("select the whole fake or real scenario; this stage requires its preceding server processes")
			}
			work := filepath.Join(root, phase.name)
			process := &historyAcceptanceProcess{}
			var httpServer *httptest.Server
			var capture *historyAcceptanceHTTP
			finish := func() bool {
				if !process.stop(t) {
					return false
				}
				if httpServer != nil {
					httpServer.CloseClientConnections()
					httpServer.Close()
					capture.active.Wait()
				}
				return true
			}
			t.Cleanup(func() {
				if finish() && reportDir != "" {
					historyAcceptanceExport(t, reportDir, mode, phase.name, phase.tick, process, capture, store, work, t.Failed())
				}
			})
			configRoot := filepath.Join(work, "runtime", "config")
			if err := os.MkdirAll(configRoot, 0700); err != nil {
				t.Fatal(err)
			}
			var secret [32]byte
			if _, err := rand.Read(secret[:]); err != nil {
				t.Fatal(err)
			}
			localKey := hex.EncodeToString(secret[:])
			httpServer, capture = historyAcceptanceProxy(t, localKey, upstream, apiKey, phase.name)
			childConfig := config
			childConfig.APIKey = "env:" + historyAcceptanceKeyEnv
			childConfig.BaseURL = httpServer.URL
			modelPath := filepath.Join(configRoot, "model.json")
			agentPath := filepath.Join(configRoot, "agent.json")
			historyAcceptanceWriteJSON(t, modelPath, childConfig)
			historyAcceptanceWriteJSON(t, agentPath, map[string]any{
				"memory_enabled":          true,
				"memory_store":            map[string]any{"kind": "sqlite", "root": filepath.Join(root, "memory")},
				"definition_catalog_root": "",
				"turn_timeout_ms":         90000, "llm_timeout_ms": historyAcceptanceLLMTimeoutMS, "observe_timeout_ms": 5000,
				"compaction_enabled": phase.name == "compact", "compaction_timeout_ms": historyAcceptanceCompactionTimeoutMS,
				"keep_recent_tokens": 1000, "max_summary_tokens": historyAcceptanceSummaryTokens,
				"max_summary_input_tokens": 32768, "max_steps": 1,
				"max_request_tokens": 16000, "max_user_message_tokens": 6000, "max_transcript_tokens": 1024,
			})
			t.Logf("phase=%s llm_timeout_ms=%d compaction_timeout_ms=%d max_summary_tokens=%d model_context_window_tokens=%d model_max_output_tokens=%d",
				phase.name, historyAcceptanceLLMTimeoutMS, historyAcceptanceCompactionTimeoutMS, historyAcceptanceSummaryTokens, config.ContextTokens, config.OutputTokens)
			historyAcceptanceStart(t, binary, work, modelPath, agentPath, localKey, process)
			if pids[process.cmd.Process.Pid] {
				t.Fatal("server stages must run in distinct operating-system processes")
			}
			pids[process.cmd.Process.Pid] = true
			t.Logf("phase=%s pid=%d bound=%s cwd=%s", phase.name, process.cmd.Process.Pid, historyAcceptanceAddress, work)
			eventID := "acceptance:" + phase.name
			turnID := historyAcceptanceTurn(t, process, eventID, phase.tick, phase.text)
			process.requireAlive(t)
			if reportDir != "" {
				historyAcceptanceWaitTrace(t, work, eventID)
			}
			if !finish() {
				return
			}

			capture.mu.Lock()
			calls, captureErrors := append([]historyAcceptanceCall(nil), capture.calls...), append([]string(nil), capture.errs...)
			capture.mu.Unlock()
			if len(captureErrors) != 0 {
				t.Fatalf("model transport failed: %v", captureErrors)
			}
			var decisions, summaries []historyAcceptanceCall
			for _, call := range calls {
				if call.Summary {
					summaries = append(summaries, call)
				} else {
					decisions = append(decisions, call)
				}
			}
			if len(decisions) != 1 {
				t.Fatalf("final HTTP decision requests=%d, want exactly one", len(decisions))
			}
			t.Logf("phase=%s decision_requests=%d summary_requests=%d final_request_bytes=%d", phase.name, len(decisions), len(summaries), len(decisions[0].Body))
			projection := historyAcceptanceReadProjection(t, decisions[0].Body)
			wantSummaryCalls := 0
			if phase.name == "compact" {
				wantSummaryCalls = 1
			}
			if len(summaries) != wantSummaryCalls {
				t.Fatalf("summary HTTP requests=%d, want %d", len(summaries), wantSummaryCalls)
			}
			page, storedSummary := historyAcceptanceReadStore(t, store, 130)
			if len(page.Sources) != len(seeds)+len(completed)+1 {
				t.Fatalf("committed history sources=%d, want %d", len(page.Sources), len(seeds)+len(completed)+1)
			}
			latest := page.Sources[0]
			if latest.Batch.Event.ID != eventID || latest.Batch.TurnID != turnID || latest.Batch.Terminal.Status != "completed" {
				t.Fatal("gRPC TurnCompletion was not followed by its committed terminal history")
			}
			for _, source := range projection.Sources {
				if !source.Complete || source.ID == "" || source.Content.Owner != historyAcceptanceOwner || source.Content.Event.ID == eventID {
					t.Fatal("final HTTP history has incomplete, foreign, or current-turn sources")
				}
			}
			switch phase.name {
			case "compact":
				if storedSummary == nil || projection.SummaryID == "" {
					t.Fatal("compaction did not publish and inject a summary; production summary failures/timeouts do not count as acceptance")
				}
				checkpoint = storedSummary
				wantRefs := []memory.HistorySourceRef{
					{ID: seeds[0].ID, Sequence: seeds[0].Sequence, Fingerprint: seeds[0].Fingerprint},
					{ID: seeds[1].ID, Sequence: seeds[1].Sequence, Fingerprint: seeds[1].Fingerprint},
				}
				if checkpoint.Owner != historyAcceptanceOwner || checkpoint.Revision != 1 || !reflect.DeepEqual(checkpoint.Sources, wantRefs) {
					t.Fatal("summary coverage must contain exactly the two earlier complete sources")
				}
				historyAcceptanceAssertSummaryInput(t, summaries[0].Body, seeds[:2])
				historyAcceptanceAssertFidelity(t, checkpoint.Text)
				historyAcceptanceAssertSummary(t, projection, checkpoint)
				historyAcceptanceAssertRaw(t, projection, []memory.HistorySource{seeds[2]})
			case "rollback":
				if projection.SummaryID != "" || projection.SummaryText != "" {
					t.Fatal("rollback exposed a summary containing future sources")
				}
				historyAcceptanceAssertRaw(t, projection, seeds[:1])
				if strings.Contains(string(decisions[0].Body), "9462") || strings.Contains(string(decisions[0].Body), historyAcceptanceFailure) {
					t.Fatal("rollback leaked future package results or recent dialogue into the final HTTP request")
				}
				_, hidden := historyAcceptanceReadStore(t, store, 100)
				if hidden != nil {
					t.Fatal("rollback still considers the future checkpoint visible")
				}
			case "catchup":
				historyAcceptanceAssertSummary(t, projection, checkpoint)
				historyAcceptanceAssertRaw(t, projection, append([]memory.HistorySource{seeds[2]}, completed...))
			}
			if storedSummary == nil || storedSummary.ID != checkpoint.ID || storedSummary.Text != checkpoint.Text || storedSummary.Revision != checkpoint.Revision {
				t.Fatal("restarts changed the persisted checkpoint instead of recovering it")
			}
			if info, err := os.Stat(historyAcceptanceTracePath(work)); err != nil || info.Size() == 0 {
				t.Fatal("production trace was not written beneath the temporary server data root")
			}
			completed = append(completed, latest)
		}) {
			return
		}
	}
	if len(completed) != 3 {
		t.Fatal("acceptance requires all three stages; select the whole fake or real scenario")
	}
	t.Log("three production server processes completed compact -> rollback -> catchup; seeded Memory sources, not Adapter collection acceptance")
}

func historyAcceptanceSeed(t *testing.T, store *memory.SQLiteHistoryStore) []memory.HistorySource {
	t.Helper()
	batch := func(id string, tick int64, facts []memory.SourceContextFact) memory.HistoryBatch {
		return memory.HistoryBatch{
			Owner: historyAcceptanceOwner, Kind: memory.HistoryKindTerminal, Version: memory.HistoryVersion, TurnID: "turn:" + id,
			Event:    memory.HistoryEvent{ID: "seed:" + id, Type: "acceptance.input", GameTime: memory.SnapshotGameTime(historyAcceptanceTime(tick)), Facts: facts},
			Terminal: memory.HistoryTerminal{Status: "completed"},
		}
	}
	past := batch("past", 80, []memory.SourceContextFact{
		{Kind: "utterance", ActorEntityID: "player:qinghe", TargetEntityID: historyAcceptanceOwner.EntityID, Text: historyAcceptancePast},
		{Kind: "utterance", ActorEntityID: historyAcceptanceOwner.EntityID, TargetEntityID: "player:qinghe", Text: historyAcceptanceNPC},
	})
	failed := batch("package", 120, []memory.SourceContextFact{
		{Kind: "utterance", ActorEntityID: "player:qinghe", Text: historyAcceptanceClaim},
		{Kind: "fact", Text: strings.Repeat("日志记录：门口的木箱保持原样，没有新增投递。", 280)},
	})
	call := model.ToolCall{ID: "call:package", Name: "deliver_package", Arguments: map[string]any{"recipient": "阿岚", "item": "包裹"}}
	failed.Steps = []memory.HistoryStep{{
		Index: 1, Decision: model.ModelDecision{ToolCalls: []model.ToolCall{call}, Control: model.ControlDirective{Kind: model.ControlSettle}},
		Executions: []memory.HistoryExecution{{Call: call, ActionID: "action:package", Started: true,
			ActionResult: &protocol.ActionResult{ActionId: "action:package", Status: protocol.ActionStatus_ACTION_STATUS_FAILED,
				Error: &protocol.Error{Code: "recipient_unavailable", Message: historyAcceptanceFailure}},
		}},
	}}
	recent := batch("recent", 125, []memory.SourceContextFact{{Kind: "utterance", ActorEntityID: "player:qinghe", Text: historyAcceptanceRecent}})
	var seeds []memory.HistorySource
	for _, source := range []memory.HistoryBatch{past, failed, recent} {
		stored, err := store.AppendHistory(context.Background(), source)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, stored)
	}
	return seeds
}

func historyAcceptanceTime(tick int64) *protocol.GameTime { return &protocol.GameTime{Tick: &tick} }

func historyAcceptanceReadStore(t *testing.T, store *memory.SQLiteHistoryStore, tick int64) (memory.HistoryPage, *memory.SummaryCheckpoint) {
	t.Helper()
	page, summary, err := historyAcceptanceStoreState(store, tick)
	if err != nil {
		t.Fatal(err)
	}
	return page, summary
}

func historyAcceptanceStoreState(store *memory.SQLiteHistoryStore, tick int64) (memory.HistoryPage, *memory.SummaryCheckpoint, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snapshot, err := store.BeginHistorySnapshot(ctx, historyAcceptanceOwner)
	if err != nil {
		return memory.HistoryPage{}, nil, err
	}
	defer store.ReleaseHistorySnapshot(snapshot)
	page, err := store.ReadHistorySnapshot(ctx, snapshot, 0, memory.HistoryReadLimits{Records: 16, Bytes: 1 << 20})
	if err != nil || page.More {
		return page, nil, errors.New("temporary history read failed or was incomplete")
	}
	summary, err := store.ReadSummary(ctx, snapshot, memory.SnapshotGameTime(historyAcceptanceTime(tick)), memory.SummaryReadLimits{})
	if err != nil {
		return page, nil, err
	}
	return page, summary.Checkpoint, nil
}

func historyAcceptanceReportDirectory(t *testing.T, mode string) string {
	t.Helper()
	requested := strings.TrimSpace(os.Getenv("GAMEAGENT_HISTORY_ACCEPTANCE_REPORT_DIR"))
	if requested == "" {
		return ""
	}
	base, err := filepath.Abs(requested)
	if err != nil {
		t.Fatal("invalid acceptance report directory")
	}
	for _, part := range strings.Split(filepath.ToSlash(base), "/") {
		if strings.EqualFold(part, ".claude") {
			t.Fatal("acceptance reports must stay outside .claude")
		}
	}
	playerRoot, err := filepath.Abs(filepath.Join("..", "..", "..", "runtime", ".local"))
	if err != nil {
		t.Fatal(err)
	}
	if relative, err := filepath.Rel(playerRoot, base); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
		t.Fatal("acceptance reports must stay outside the player's runtime/.local")
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, fmt.Sprintf("%s-%d-", mode, os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("acceptance_report_dir=%s", dir)
	t.Cleanup(func() {
		status := "passed"
		if t.Failed() {
			status = "failed"
		}
		historyAcceptanceWriteJSON(t, filepath.Join(dir, "run.json"), map[string]any{
			"mode": mode, "test_pid": os.Getpid(), "status": status, "human_review_required": true,
		})
	})
	return dir
}

// The typed projection is an allowlist: raw trace errors, request headers and
// provider response fields never enter the exported evidence.
type historyAcceptanceTraceEvidence struct {
	Event     string    `json:"event"`
	Time      time.Time `json:"time"`
	TurnID    string    `json:"turn_id"`
	EventID   string    `json:"event_id"`
	ElapsedMS int64     `json:"elapsed_ms"`
	Fields    struct {
		ElapsedMS       *int64 `json:"elapsed_ms,omitempty"`
		SummaryReadMS   *int64 `json:"summary_read_ms,omitempty"`
		HistoryReadMS   *int64 `json:"history_read_ms,omitempty"`
		MaxInputTokens  *int64 `json:"max_input_tokens,omitempty"`
		MaxOutputTokens *int64 `json:"max_output_tokens,omitempty"`
		HistoryBudget   *int64 `json:"history_budget_tokens,omitempty"`
		HistoryTokens   *int64 `json:"history_estimated_tokens,omitempty"`
	} `json:"fields"`
}

func historyAcceptanceReadSafeTrace(path string) ([]historyAcceptanceTraceEvidence, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	var events []historyAcceptanceTraceEvidence
	for {
		var event historyAcceptanceTraceEvidence
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				return events, nil
			}
			return events, errors.New("partial or invalid temporary trace")
		}
		switch event.Event {
		case "history_prepared", "compaction_started", "compaction_completed", "compaction_failed", "compaction_skipped",
			"model_request_started", "model_response_received", "turn_completed", "turn_failed", "context_updated", "context_update_failed":
			events = append(events, event)
		}
	}
}

func historyAcceptanceWaitTrace(t *testing.T, work, eventID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events, err := historyAcceptanceReadSafeTrace(historyAcceptanceTracePath(work))
		if err == nil {
			for _, event := range events {
				if event.Event == "turn_completed" && event.EventID == eventID {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("completed Turn trace did not flush before test-owned process termination")
}

func historyAcceptanceExport(t *testing.T, reportDir, mode, phase string, tick int64, process *historyAcceptanceProcess, capture *historyAcceptanceHTTP, store *memory.SQLiteHistoryStore, work string, failed bool) {
	t.Helper()
	pid := 0
	if process.cmd != nil && process.cmd.Process != nil {
		pid = process.cmd.Process.Pid
	}
	dir := filepath.Join(reportDir, fmt.Sprintf("%s-%d", phase, pid))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var calls []historyAcceptanceCall
	var transportErrors []string
	if capture != nil {
		capture.mu.Lock()
		calls, transportErrors = append(calls, capture.calls...), append(transportErrors, capture.errs...)
		capture.mu.Unlock()
	}
	var requestFiles, evidenceErrors []string
	for i, call := range calls {
		if _, err := historyAcceptanceDecodeRequest(call.Body); err != nil {
			evidenceErrors = append(evidenceErrors, "request_not_recordable")
			continue
		}
		kind := "decision"
		if call.Summary {
			kind = "summary"
		}
		name := fmt.Sprintf("http-%d-%s.json", i+1, kind)
		if err := os.WriteFile(filepath.Join(dir, name), call.Body, 0600); err != nil {
			t.Fatal(err)
		}
		requestFiles = append(requestFiles, name)
	}
	page, checkpoint, err := historyAcceptanceStoreState(store, 130)
	if err != nil {
		evidenceErrors = append(evidenceErrors, "history_state_unavailable")
	}
	var sourceIDs []string
	for _, source := range page.Sources {
		sourceIDs = append(sourceIDs, source.ID)
	}
	trace, err := historyAcceptanceReadSafeTrace(historyAcceptanceTracePath(work))
	if err != nil {
		evidenceErrors = append(evidenceErrors, "trace_missing_or_partial")
	}
	if len(evidenceErrors) > 0 && !failed && !t.Failed() {
		t.Errorf("acceptance report evidence incomplete: %v", evidenceErrors)
	}
	status := "passed"
	if failed || t.Failed() {
		status = "failed"
	}
	historyAcceptanceWriteJSON(t, filepath.Join(dir, "evidence.json"), map[string]any{
		"mode": mode, "phase": phase, "pid": pid, "status": status, "game_time_tick": tick,
		"owner": historyAcceptanceOwner, "source_ids": sourceIDs, "checkpoint": checkpoint, "checkpoint_read_tick": 130,
		"request_files": requestFiles, "trace": trace, "transport_errors": transportErrors, "evidence_errors": evidenceErrors,
		"llm_timeout_ms": historyAcceptanceLLMTimeoutMS, "compaction_timeout_ms": historyAcceptanceCompactionTimeoutMS,
		"max_summary_tokens": historyAcceptanceSummaryTokens, "compaction_enabled": phase == "compact", "human_review_required": true,
	})
	t.Logf("phase=%s pid=%d status=%s evidence=%s", phase, pid, status, filepath.Join(dir, "evidence.json"))
}

func historyAcceptanceReadProjection(t *testing.T, body []byte) historyAcceptanceProjection {
	t.Helper()
	wire, err := historyAcceptanceDecodeRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	var found []historyAcceptanceProjection
	for _, message := range wire.Messages {
		if i := strings.Index(message.Content, "[History]\n"); i >= 0 {
			var projection historyAcceptanceProjection
			if err := json.NewDecoder(strings.NewReader(message.Content[i+len("[History]\n"):])).Decode(&projection); err != nil {
				t.Fatal("final HTTP request contains invalid History JSON")
			}
			found = append(found, projection)
		}
	}
	if len(found) != 1 {
		t.Fatalf("final HTTP request has %d History sections, want one", len(found))
	}
	return found[0]
}

func historyAcceptanceAssertRaw(t *testing.T, projection historyAcceptanceProjection, want []memory.HistorySource) {
	t.Helper()
	if len(projection.Sources) != len(want) {
		t.Fatalf("final HTTP raw sources=%d, want %d", len(projection.Sources), len(want))
	}
	for i, source := range projection.Sources {
		got, err := json.Marshal(source.Content)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := json.Marshal(want[i].Batch)
		if err != nil {
			t.Fatal(err)
		}
		if source.ID != want[i].ID || source.Sequence != want[i].Sequence || !source.Complete || !bytes.Equal(got, expected) {
			t.Fatalf("raw source %d lost its identity, order, or complete original content", i)
		}
	}
}

func historyAcceptanceAssertSummary(t *testing.T, projection historyAcceptanceProjection, want *memory.SummaryCheckpoint) {
	t.Helper()
	if projection.SummaryID != want.ID || projection.SummaryText != want.Text {
		t.Fatal("persisted summary ID/text missing or changed in the final HTTP model request")
	}
	for _, source := range projection.Sources {
		for _, covered := range want.Sources {
			if source.ID == covered.ID {
				t.Fatal("summary and recent raw content duplicate a covered source")
			}
		}
	}
}

// These are fixture marker checks, not semantic acceptance. Final factual
// accuracy requires an independent human review of the sources and summary.
func historyAcceptanceAssertFidelity(t *testing.T, summary string) {
	t.Helper()
	for _, pattern := range []string{
		`青禾[^。！？\n]{0,60}不喜欢雨天`, `守林[^。！？\n]{0,60}喜欢雨天`, `7319`,
		`(没有答应|未答应|未承诺)[^。！？\n]{0,30}包裹`,
		`包裹[^。！？\n]{0,80}(失败|没有送达|未送达)`,
		`(模型|系统|Runtime|守林|agent:shoulin)[^。！？\n]{0,40}(请求|调用|尝试)`,
	} {
		if !regexp.MustCompile(pattern).MatchString(summary) {
			t.Fatalf("summary fixture marker missing: %q; independent factual review is required", pattern)
		}
	}
	if regexp.MustCompile(`守林[^。！？\n]{0,15}不喜欢雨天`).MatchString(summary) {
		t.Fatal("summary moved the player's negation to the agent")
	}
	t.Log("summary fixture markers passed; final factual accuracy requires independent human review")
}

func historyAcceptanceAssertSummaryInput(t *testing.T, body []byte, want []memory.HistorySource) {
	t.Helper()
	wire, err := historyAcceptanceDecodeRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		Sources []struct {
			ID      string              `json:"id"`
			Content memory.HistoryBatch `json:"content"`
		} `json:"sources"`
	}
	if json.Unmarshal([]byte(wire.Messages[len(wire.Messages)-1].Content), &input) != nil || len(input.Sources) != len(want) {
		t.Fatal("summary HTTP request omitted the complete compaction sources")
	}
	for i, source := range input.Sources {
		got, _ := json.Marshal(source.Content)
		expected, _ := json.Marshal(want[i].Batch)
		if source.ID != want[i].ID || !bytes.Equal(got, expected) {
			t.Fatal("summary input changed the controlled actors, statements, or actual failed receipt")
		}
	}
}

func historyAcceptanceUpstream(config llm.Config) (string, error) {
	if config.Provider != "deepseek" {
		return "", errors.New("BLOCKED: real acceptance requires the configured DeepSeek provider")
	}
	switch strings.TrimRight(config.BaseURL, "/") {
	case "", "https://api.deepseek.com":
		return "https://api.deepseek.com/chat/completions", nil
	case "https://api.deepseek.com/v1":
		return "https://api.deepseek.com/v1/chat/completions", nil
	default:
		return "", errors.New("BLOCKED: real acceptance only forwards to fixed HTTPS api.deepseek.com endpoints")
	}
}

func historyAcceptanceDecodeRequest(body []byte) (historyAcceptanceWire, error) {
	var wire historyAcceptanceWire
	var value any
	if json.Unmarshal(body, &value) != nil || json.Unmarshal(body, &wire) != nil || wire.Model == "" || len(wire.Messages) == 0 {
		return wire, errors.New("invalid model request body")
	}
	var safe func(any) bool
	safe = func(value any) bool {
		switch v := value.(type) {
		case map[string]any:
			for key, child := range v {
				switch strings.ToLower(key) {
				case "reasoning_content", "authorization", "api_key", "headers":
					return false
				}
				if !safe(child) {
					return false
				}
			}
		case []any:
			for _, child := range v {
				if !safe(child) {
					return false
				}
			}
		}
		return true
	}
	if !safe(value) || bytes.Contains(body, []byte("reasoning_content")) {
		return historyAcceptanceWire{}, errors.New("model request contains fields that cannot be recorded")
	}
	return wire, nil
}

func historyAcceptanceProxy(t *testing.T, localKey, upstream, apiKey, phase string) (*httptest.Server, *historyAcceptanceHTTP) {
	t.Helper()
	capture := &historyAcceptanceHTTP{}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, Timeout: 65 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	t.Cleanup(transport.CloseIdleConnections)
	fail := func(w http.ResponseWriter, message string) {
		capture.mu.Lock()
		capture.errs = append(capture.errs, message)
		capture.mu.Unlock()
		http.Error(w, "acceptance transport failed", http.StatusBadGateway)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.active.Add(1)
		defer capture.active.Done()
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+localKey {
			fail(w, "unexpected loopback request")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
		if err != nil || len(body) > 1<<20 {
			fail(w, "model request exceeded the capture limit")
			return
		}
		wire, err := historyAcceptanceDecodeRequest(body)
		if err != nil {
			fail(w, "model request cannot be safely recorded")
			return
		}
		var summaryInput struct {
			Sources []json.RawMessage `json:"sources"`
		}
		isSummary := json.Unmarshal([]byte(wire.Messages[len(wire.Messages)-1].Content), &summaryInput) == nil && len(summaryInput.Sources) > 0
		capture.mu.Lock()
		limit := 1
		if phase == "compact" {
			limit = 2
		}
		allowed := len(capture.calls) < limit && (!isSummary || phase == "compact")
		if allowed {
			capture.calls = append(capture.calls, historyAcceptanceCall{Body: body, Summary: isSummary})
		}
		capture.mu.Unlock()
		if !allowed {
			fail(w, "unexpected additional model call")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if upstream == "" {
			content := "已核对，本轮无需动作。"
			if isSummary {
				content = historyAcceptanceSummary
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
				"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": content},
			}}})
			return
		}
		// Forward the exact production body. No thinking/provider-mode rewrite,
		// inherited proxy, redirect, request headers, or response-body recording.
		request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstream, bytes.NewReader(body))
		if err != nil {
			fail(w, "cannot construct fixed upstream request")
			return
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+apiKey)
		response, err := client.Do(request)
		if err != nil {
			fail(w, "upstream request failed or timed out")
			return
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			fail(w, fmt.Sprintf("upstream HTTP status %d", response.StatusCode))
			return
		}
		if _, err := io.Copy(w, io.LimitReader(response.Body, (1<<20)+1)); err != nil {
			capture.mu.Lock()
			capture.errs = append(capture.errs, "upstream response interrupted")
			capture.mu.Unlock()
		}
	}))
	server.Start()
	t.Cleanup(server.Close)
	if host, _, err := net.SplitHostPort(server.Listener.Addr().String()); err != nil || host != "127.0.0.1" {
		t.Fatal("model proxy must listen only on 127.0.0.1")
	}
	return server, capture
}

func historyAcceptanceCheckPort(address string) error {
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		return err
	}
	return listener.Close()
}

type historyAcceptanceOutput struct {
	mu         sync.Mutex
	pending    string
	bound      chan struct{}
	bindFailed chan struct{}
	boundOnce  sync.Once
	failOnce   sync.Once
}

func (o *historyAcceptanceOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	lines := strings.Split(o.pending+string(p), "\n")
	o.pending = lines[len(lines)-1]
	if len(o.pending) > 4096 {
		o.pending = o.pending[len(o.pending)-4096:]
	}
	for _, line := range lines[:len(lines)-1] {
		if strings.HasSuffix(strings.TrimSpace(line), "GameAgent Runtime listening on "+historyAcceptanceAddress) {
			o.boundOnce.Do(func() { close(o.bound) })
		}
		if strings.Contains(line, "listen failed:") {
			o.failOnce.Do(func() { close(o.bindFailed) })
		}
	}
	return len(p), nil
}

type historyAcceptanceProcess struct {
	cmd    *exec.Cmd
	done   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
}

// historyAcceptanceTracePath is where a child writes its trace for the data root
// it was started with.
func historyAcceptanceTracePath(work string) string {
	return filepath.Join(work, "data", "traces.jsonl")
}

func historyAcceptanceStart(t *testing.T, binary, work, modelPath, agentPath, localKey string, process *historyAcceptanceProcess, observers ...io.Writer) {
	t.Helper()
	// Release the probe immediately before spawning. A race for the port is
	// resolved by this child's own bind result, before any gRPC connection.
	if err := historyAcceptanceCheckPort(historyAcceptanceAddress); err != nil {
		t.Fatalf("BLOCKED: %s became occupied; no existing process was stopped", historyAcceptanceAddress)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	// --no-open keeps the local client from launching a browser during a test run.
	*process = historyAcceptanceProcess{cmd: exec.CommandContext(ctx, binary, "--data-root", work, "--no-open"), done: make(chan struct{}), ctx: ctx, cancel: cancel}
	process.cmd.Dir = work
	process.cmd.Env = historyAcceptanceEnv(map[string]string{
		"GAMEAGENT_MODEL_CONFIG": modelPath, "GAMEAGENT_AGENT_CONFIG": agentPath, historyAcceptanceKeyEnv: localKey,
	})
	output := &historyAcceptanceOutput{bound: make(chan struct{}), bindFailed: make(chan struct{})}
	writer := io.MultiWriter(append([]io.Writer{output}, observers...)...)
	process.cmd.Stdout, process.cmd.Stderr = writer, writer
	if err := process.cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start production server: %v", err)
	}
	go func() {
		_ = process.cmd.Wait()
		cancel()
		close(process.done)
	}()
	t.Cleanup(func() { process.stop(t) })
	select {
	case <-output.bound:
		process.requireAlive(t)
	case <-output.bindFailed:
		t.Fatal("BLOCKED: test server could not bind 50051; no gRPC requests were sent")
	case <-process.done:
		t.Fatal("BLOCKED: production server exited before confirming its listener; no gRPC requests were sent")
	case <-time.After(10 * time.Second):
		t.Fatal("BLOCKED: production server never confirmed its listener; no gRPC requests were sent")
	}
}

func (p *historyAcceptanceProcess) requireAlive(t *testing.T) {
	t.Helper()
	select {
	case <-p.done:
		t.Fatal("test-owned server exited unexpectedly")
	default:
	}
	if p.ctx.Err() != nil {
		t.Fatal("test-owned server lifetime expired")
	}
}

func (p *historyAcceptanceProcess) stop(t *testing.T) bool {
	t.Helper()
	if p.cmd == nil || p.cmd.Process == nil {
		return true
	}
	select {
	case <-p.done:
		return true
	default:
	}
	// The stream reaches EOF after lane persistence. Terminate only the child
	// handle we created; incomplete-turn/crash recovery is outside this test.
	_ = p.cmd.Process.Kill()
	p.cancel()
	select {
	case <-p.done:
		return true
	case <-time.After(5 * time.Second):
		t.Error("test-owned server did not exit after termination")
		return false
	}
}

func historyAcceptanceTurn(t *testing.T, process *historyAcceptanceProcess, eventID string, tick int64, text string) string {
	t.Helper()
	process.requireAlive(t)
	ctx, cancel := context.WithTimeout(process.ctx, 80*time.Second)
	defer cancel()
	dialCtx, cancelDial := context.WithTimeout(ctx, 5*time.Second)
	defer cancelDial()
	var dialMu sync.Mutex
	dialed := false
	conn, err := grpc.DialContext(dialCtx, historyAcceptanceAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock(),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			dialMu.Lock()
			defer dialMu.Unlock()
			if dialed || process.ctx.Err() != nil {
				return nil, errors.New("acceptance connection cannot redial another service")
			}
			dialed = true
			return (&net.Dialer{}).DialContext(ctx, "tcp4", historyAcceptanceAddress)
		}))
	if err != nil {
		t.Fatalf("connect confirmed test-owned listener: %v", err)
	}
	defer conn.Close()
	process.requireAlive(t)
	stream, err := protocol.NewGameAgentGatewayClient(conn).Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	send := func(message *protocol.AdapterMessage) {
		t.Helper()
		process.requireAlive(t)
		if err := stream.Send(message); err != nil {
			t.Fatalf("send controlled Adapter message: %v", err)
		}
	}
	recv := func() *protocol.RuntimeMessage {
		t.Helper()
		message, err := stream.Recv()
		if err != nil {
			t.Fatalf("receive controlled Runtime message: %v", err)
		}
		return message
	}
	sessionID := "session:" + eventID
	send(&protocol.AdapterMessage{MessageId: "hello", Payload: &protocol.AdapterMessage_Hello{Hello: &protocol.AdapterHello{
		AdapterId: "controlled-history-adapter", AdapterVersion: "0.1.0", ProtocolVersion: "v1alpha2",
		GameId: historyAcceptanceOwner.GameID, GameVersion: "0.1.0", SessionId: sessionID,
	}}})
	if ready := recv().GetEnvironmentReady(); ready == nil || ready.SessionId != sessionID {
		t.Fatal("unexpected EnvironmentReady identity")
	}
	capabilityRequest := recv()
	if capabilityRequest.GetCapabilityRequest() == nil {
		t.Fatal("expected capability discovery")
	}
	send(&protocol.AdapterMessage{MessageId: "capabilities", CorrelationId: capabilityRequest.MessageId,
		Payload: &protocol.AdapterMessage_Capabilities{Capabilities: &protocol.CapabilityList{Revision: 1}},
	})
	send(&protocol.AdapterMessage{MessageId: "event", Payload: &protocol.AdapterMessage_Event{Event: &protocol.GameEvent{
		EventId: eventID, EventType: "acceptance.input", WorldId: historyAcceptanceOwner.WorldID,
		TargetEntityId: historyAcceptanceOwner.EntityID, Sequence: 1, GameTime: historyAcceptanceTime(tick),
		Entities: []*protocol.EntityRef{
			{EntityId: historyAcceptanceOwner.EntityID, EntityType: "agent", DisplayName: "守林", DefinitionId: "agent:shoulin"},
			{EntityId: "player:qinghe", EntityType: "player", DisplayName: "青禾", DefinitionId: "player:qinghe"},
		},
		ContextFacts: []*protocol.ContextFact{{Kind: "utterance", ActorEntityId: "player:qinghe", TargetEntityId: historyAcceptanceOwner.EntityID, Text: text}},
	}}})
	if ack := recv().GetEventAck(); ack == nil || ack.EventId != eventID || ack.Status != protocol.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatal("controlled event was not accepted")
	}
	observe := recv()
	if request := observe.GetObserve(); request == nil || request.WorldId != historyAcceptanceOwner.WorldID || request.EntityId != historyAcceptanceOwner.EntityID {
		t.Fatal("Observe request has the wrong owner")
	}
	send(&protocol.AdapterMessage{MessageId: "observation", CorrelationId: observe.MessageId,
		Payload: &protocol.AdapterMessage_Observation{Observation: &protocol.Observation{
			WorldId: historyAcceptanceOwner.WorldID, EntityId: historyAcceptanceOwner.EntityID, Revision: 1, GameTime: historyAcceptanceTime(tick),
		}},
	})
	completion := recv().GetTurnCompletion()
	if completion == nil || completion.EventId != eventID || completion.EntityId != historyAcceptanceOwner.EntityID || completion.TurnId == "" || completion.Status != protocol.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED {
		t.Fatal("controlled Turn did not complete successfully")
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatal("Adapter stream did not drain terminal history to EOF")
	}
	return completion.TurnId
}

func historyAcceptanceWriteJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal("cannot encode temporary acceptance configuration")
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func historyAcceptanceEnv(overrides map[string]string) []string {
	var env []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		replaced := false
		for key := range overrides {
			if strings.EqualFold(name, key) {
				replaced = true
				break
			}
		}
		if !replaced {
			env = append(env, entry)
		}
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func TestHistoryAcceptanceSafety(t *testing.T) {
	t.Run("occupied port is left intact", func(t *testing.T) {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		if historyAcceptanceCheckPort(listener.Addr().String()) == nil {
			t.Fatal("port preflight accepted an occupied listener")
		}
		conn, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal("port preflight disturbed the existing listener")
		}
		conn.Close()
	})
	t.Run("fixed trusted upstream", func(t *testing.T) {
		for _, base := range []string{"http://api.deepseek.com", "https://api.deepseek.com.evil.invalid", "https://api.deepseek.com:443", "https://api.deepseek.com/?target=elsewhere", "https://user@api.deepseek.com"} {
			if _, err := historyAcceptanceUpstream(llm.Config{Provider: "deepseek", BaseURL: base}); err == nil {
				t.Fatal("untrusted upstream accepted")
			}
		}
		if got, err := historyAcceptanceUpstream(llm.Config{Provider: "deepseek", BaseURL: "https://api.deepseek.com"}); err != nil || got != "https://api.deepseek.com/chat/completions" {
			t.Fatal("trusted upstream not resolved to the fixed endpoint")
		}
	})
	t.Run("request recording excludes secrets and reasoning", func(t *testing.T) {
		for _, body := range []string{
			`{"model":"local","messages":[{"role":"user","content":"受控内容","reasoning_content":"private"}]}`,
			`{"model":"local","messages":[{"role":"user","content":"受控内容"}],"headers":{"Authorization":"private"}}`,
			`{"model":"local","messages":[{"role":"user","content":"受控内容"}],"extra":{"api_key":"private"}}`,
		} {
			if _, err := historyAcceptanceDecodeRequest([]byte(body)); err == nil {
				t.Fatal("sensitive model body was accepted for recording")
			}
		}
		body := []byte(`{"model":"local","messages":[{"role":"user","content":"受控内容"}],"thinking":{"type":"enabled"}}`)
		if _, err := historyAcceptanceDecodeRequest(body); err != nil {
			t.Fatal("request inspection must accept the original production mode")
		}
	})
	t.Run("forward original bytes without copying headers", func(t *testing.T) {
		body := []byte(`{"model":"local","messages":[{"role":"user","content":"受控内容"}],"thinking":{"type":"enabled"},"stream":false}`)
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, err := io.ReadAll(r.Body)
			if err != nil || !bytes.Equal(got, body) || r.URL.Path != "/chat/completions" {
				t.Error("proxy changed the original provider request")
			}
			if r.Header.Get("Authorization") != "Bearer upstream-test-token" || r.Header.Get("X-Private-Test") != "" {
				t.Error("proxy copied incoming headers instead of setting upstream credentials")
			}
			_, _ = io.WriteString(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"已核对","reasoning_content":"private-test-sentinel"}}]}`)
		}))
		defer upstream.Close()
		proxy, capture := historyAcceptanceProxy(t, "local-test-token", upstream.URL+"/chat/completions", "upstream-test-token", "rollback")
		request, err := http.NewRequest(http.MethodPost, proxy.URL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer local-test-token")
		request.Header.Set("X-Private-Test", "do-not-forward")
		client := &http.Client{Timeout: 5 * time.Second}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("loopback forwarding failed")
		}
		defer response.Body.Close()
		if _, err := io.Copy(io.Discard, response.Body); err != nil || response.StatusCode != http.StatusOK {
			t.Fatal("loopback response was not forwarded successfully")
		}
		capture.mu.Lock()
		defer capture.mu.Unlock()
		if len(capture.errs) != 0 || len(capture.calls) != 1 || !bytes.Equal(capture.calls[0].Body, body) {
			t.Fatal("capture must contain only the exact outgoing request, never response reasoning or headers")
		}
	})
}
