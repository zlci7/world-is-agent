package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	agentcontext "gameagent/runtime/internal/context"
	"gameagent/runtime/internal/llm"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/trace"
)

type processHistoryReport struct {
	PID            int
	Profile        string
	Request        string
	Watermark      int64
	SummaryID      string
	SummaryText    string
	SummarySources []memory.HistorySourceRef
	LastSeedID     string
	TurnError      string
	Events         []trace.Event
	ElapsedMS      int64
}

type processSummaryGenerator struct{}

func (processSummaryGenerator) GenerateText(_ context.Context, _ model.TextRequest) (model.TextResponse, error) {
	return model.TextResponse{Text: "玩家说：\"请保密\"。\n应保留保密要求，代号为𠮷。"}, nil
}

type recordingProcessModel struct {
	provider model.Provider
	requests []model.Request
}

func (p *recordingProcessModel) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	p.requests = append(p.requests, req)
	return p.provider.Generate(ctx, req)
}

func (p *recordingProcessModel) ModelWindow() model.WindowLimits {
	if provider, ok := p.provider.(model.WindowProvider); ok {
		return provider.ModelWindow()
	}
	return model.WindowLimits{}
}

type processWindowModel struct {
	model.Provider
	window model.WindowLimits
}

func (p processWindowModel) ModelWindow() model.WindowLimits { return p.window }

func TestRecordingProcessModelForwardsWindow(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider model.Provider
		want     model.WindowLimits
	}{
		{
			name: "configured window",
			provider: processWindowModel{Provider: &scriptedProvider{},
				window: model.WindowLimits{ContextTokens: 32768, OutputTokens: 4096}},
			want: model.WindowLimits{ContextTokens: 32768, OutputTokens: 4096},
		},
		{
			name: "explicit fake window",
			provider: processWindowModel{Provider: &scriptedProvider{},
				window: model.WindowLimits{ContextTokens: 65536, OutputTokens: 8192}},
			want: model.WindowLimits{ContextTokens: 65536, OutputTokens: 8192},
		},
		{
			name:     "unconfigured provider stays unconfigured",
			provider: &scriptedProvider{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &recordingProcessModel{provider: tc.provider}
			windowed, ok := any(recorder).(model.WindowProvider)
			if !ok {
				t.Fatal("recording process model hides the provider window")
			}
			if got := windowed.ModelWindow(); got != tc.want {
				t.Fatalf("model window = %+v, want %+v", got, tc.want)
			}
		})
	}
}

type processRequestHistory struct {
	SummaryID   string            `json:"summary_id"`
	SummaryText string            `json:"summary_text"`
	Sources     []json.RawMessage `json:"sources"`
}

func decodeProcessRequestHistory(t *testing.T, request string) processRequestHistory {
	t.Helper()
	var history processRequestHistory
	data, present := strings.CutPrefix(request, "[History]\n")
	if !present {
		return history
	}
	if err := json.NewDecoder(strings.NewReader(data)).Decode(&history); err != nil {
		t.Fatalf("decode request history: %v", err)
	}
	return history
}

func TestProcessRequestHistoryDecodesSummary(t *testing.T) {
	text := "玩家说：\"请保密\"。\n应保留保密要求，代号为𠮷。"
	source, err := json.Marshal(map[string]string{"summary_text": text})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		projection agentcontext.HistoryProjection
	}{
		{
			name:       "newline quotes and Unicode",
			projection: agentcontext.HistoryProjection{SummaryID: "summary-1", SummaryText: text},
		},
		{
			name: "source text is not a summary",
			projection: agentcontext.HistoryProjection{Sources: []agentcontext.HistoryUnit{{
				SourceID: "source-1", Content: string(source), Complete: true,
			}}},
		},
		{name: "no history"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := agentcontext.RenderHistoryProjection(tc.projection) + "[Game Definition]\n(none)\n"
			got := decodeProcessRequestHistory(t, request)
			if got.SummaryID != tc.projection.SummaryID || got.SummaryText != tc.projection.SummaryText {
				t.Fatalf("decoded summary = %q / %q, want %q / %q", got.SummaryID, got.SummaryText, tc.projection.SummaryID, tc.projection.SummaryText)
			}
			if len(got.Sources) != len(tc.projection.Sources) {
				t.Fatalf("decoded sources = %d, want %d", len(got.Sources), len(tc.projection.Sources))
			}
		})
	}
}

func TestHistoryProcessRestartRollbackAndCatchup(t *testing.T) {
	root := t.TempDir()
	reports := make([]processHistoryReport, 0, 3)
	for _, phase := range []string{"compact", "rollback", "catchup"} {
		reports = append(reports, runHistoryProcess(t, root, phase, false))
	}
	first, rollback, catchup := reports[0], reports[1], reports[2]
	firstHistory := decodeProcessRequestHistory(t, first.Request)
	if first.TurnError != "" || first.SummaryID == "" || firstHistory.SummaryID != first.SummaryID || firstHistory.SummaryText != first.SummaryText {
		t.Fatalf("committed summary missing from final model request: %+v", first)
	}
	rollbackHistory := decodeProcessRequestHistory(t, rollback.Request)
	if rollback.TurnError != "" || rollback.SummaryID != "" || rollbackHistory.SummaryID != "" || rollbackHistory.SummaryText != "" || len(rollbackHistory.Sources) != 0 {
		t.Fatalf("future history leaked after process restart and rollback: %+v", rollback)
	}
	catchupHistory := decodeProcessRequestHistory(t, catchup.Request)
	if catchup.TurnError != "" || catchup.SummaryID != first.SummaryID || catchupHistory.SummaryID != first.SummaryID || catchupHistory.SummaryText != first.SummaryText {
		t.Fatalf("visible summary not restored after catch-up: %+v", catchup)
	}
	assertProcessRecentTail(t, firstHistory, first.LastSeedID, first.SummarySources)
	assertProcessRecentTail(t, catchupHistory, first.LastSeedID, catchup.SummarySources)
	if first.PID == rollback.PID || rollback.PID == catchup.PID {
		t.Fatal("verification did not create fresh Runtime processes")
	}
	for i, r := range reports {
		t.Logf("phase=%d pid=%d watermark=%d summary=%s elapsed_ms=%d", i, r.PID, r.Watermark, r.SummaryID, r.ElapsedMS)
	}
}

func TestHistoryRealModelProcessAcceptance(t *testing.T) {
	if os.Getenv("GAMEAGENT_HISTORY_REAL_ACCEPTANCE") != "1" {
		t.Skip("real model acceptance requires explicit opt-in and configured credentials/window")
	}
	verifyRealHistoryProcessCycle(t, "")
}

func verifyRealHistoryProcessCycle(t *testing.T, profile string) {
	t.Helper()
	root := t.TempDir()
	first := runHistoryProcessWithProfile(t, root, "compact", true, profile)
	logHistoryProcessReport(t, "compact", first)
	if first.TurnError != "" {
		t.Errorf("compact Turn failed: %s", first.TurnError)
	}
	firstHistory := decodeProcessRequestHistory(t, first.Request)
	if first.SummaryID == "" || firstHistory.SummaryID != first.SummaryID || firstHistory.SummaryText != first.SummaryText {
		t.Fatal("real compaction did not publish into decision request")
	}
	assertProcessRecentTail(t, firstHistory, first.LastSeedID, first.SummarySources)
	rollback := runHistoryProcessWithProfile(t, root, "rollback", true, profile)
	logHistoryProcessReport(t, "rollback", rollback)
	if rollback.TurnError != "" {
		t.Errorf("rollback Turn failed: %s", rollback.TurnError)
	}
	rollbackHistory := decodeProcessRequestHistory(t, rollback.Request)
	if rollback.SummaryID != "" || rollbackHistory.SummaryID != "" || rollbackHistory.SummaryText != "" || len(rollbackHistory.Sources) != 0 {
		t.Fatal("real rollback exposed future history")
	}
	catchup := runHistoryProcessWithProfile(t, root, "catchup", true, profile)
	logHistoryProcessReport(t, "catchup", catchup)
	if catchup.TurnError != "" {
		t.Errorf("catchup Turn failed: %s", catchup.TurnError)
	}
	catchupHistory := decodeProcessRequestHistory(t, catchup.Request)
	if catchup.SummaryID != first.SummaryID || catchupHistory.SummaryID != first.SummaryID || catchupHistory.SummaryText != first.SummaryText {
		t.Fatal("real catchup did not restore summary")
	}
	assertProcessRecentTail(t, catchupHistory, first.LastSeedID, catchup.SummarySources)
	if first.PID == rollback.PID || first.PID == catchup.PID || rollback.PID == catchup.PID {
		t.Fatal("real acceptance did not create three distinct Runtime processes")
	}
}

func assertProcessRecentTail(t *testing.T, history processRequestHistory, lastSeedID string, covered []memory.HistorySourceRef) {
	t.Helper()
	if lastSeedID == "" || len(covered) != 11 {
		t.Fatalf("expected 11 covered sources and one known recent tail: covered=%d tail=%q", len(covered), lastSeedID)
	}
	found := false
	for _, raw := range history.Sources {
		var unit struct {
			ID       string `json:"source_id"`
			Complete bool   `json:"complete"`
		}
		if err := json.Unmarshal(raw, &unit); err != nil {
			t.Fatal(err)
		}
		for _, ref := range covered {
			if unit.ID == ref.ID {
				t.Fatalf("covered source duplicated in recent tail: %s", unit.ID)
			}
		}
		if unit.ID == lastSeedID && unit.Complete {
			found = true
		}
	}
	if !found {
		t.Fatal("complete recent source missing from final model request")
	}
}

func logHistoryProcessReport(t *testing.T, phase string, r processHistoryReport) {
	t.Helper()
	t.Logf("phase=%s profile=%q pid=%d watermark=%d summary=%s covered_sources=%d elapsed_ms=%d", phase, r.Profile, r.PID, r.Watermark, r.SummaryID, len(r.SummarySources), r.ElapsedMS)
	t.Logf("controlled_summary=%q", r.SummaryText)
	if r.TurnError != "" {
		t.Logf("turn_error=%q", r.TurnError)
	}
	for _, event := range r.Events {
		if strings.Contains(string(event.Event), "compaction") || strings.Contains(string(event.Event), "history") || event.Event == trace.EventContextUpdated || event.Event == trace.EventModelRequestStarted || event.Event == trace.EventModelResponseReceived {
			t.Logf("event=%s elapsed_ms=%d fields=%v", event.Event, event.ElapsedMS, event.Fields)
		}
	}
}

func runHistoryProcess(t *testing.T, root, phase string, real bool) processHistoryReport {
	t.Helper()
	return runHistoryProcessWithProfile(t, root, phase, real, "")
}

func runHistoryProcessWithProfile(t *testing.T, root, phase string, real bool, profile string) processHistoryReport {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	reportPath := filepath.Join(root, phase+".json")
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestHistoryProcessHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), "GAMEAGENT_HISTORY_PROCESS_ROOT="+root, "GAMEAGENT_HISTORY_PROCESS_PHASE="+phase, "GAMEAGENT_HISTORY_PROCESS_REPORT="+reportPath, "GAMEAGENT_HISTORY_PROCESS_DIAGNOSTIC="+profile, fmt.Sprintf("GAMEAGENT_HISTORY_PROCESS_REAL=%t", real))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("history process %s: %v\n%s", phase, err, output)
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report processHistoryReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if real {
		if dir := os.Getenv("GAMEAGENT_HISTORY_ACCEPTANCE_REPORT_DIR"); dir != "" {
			if !filepath.IsAbs(dir) {
				t.Fatal("acceptance report directory must be absolute")
			}
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			name := phase
			if profile != "" {
				name += "-" + profile
			}
			name += fmt.Sprintf("-%d", report.PID)
			if err := os.WriteFile(filepath.Join(dir, name+".json"), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return report
}

func TestHistoryProcessHelper(t *testing.T) {
	root := os.Getenv("GAMEAGENT_HISTORY_PROCESS_ROOT")
	if root == "" {
		t.Skip("subprocess helper")
	}
	started := time.Now()
	phase := os.Getenv("GAMEAGENT_HISTORY_PROCESS_PHASE")
	key := session.AgentSessionKey{GameID: "controlled-memory-test", WorldID: "isolated-process-world", EntityID: "witness"}
	cfg := agent.DefaultConfig()
	cfg.MemoryStore.Root = filepath.Join(root, "memory")
	cfg.MaxRequestTokens = 6000
	cfg.MaxUserMessageTokens = 6000
	cfg.MaxTranscriptTokens = 256
	cfg.Compaction.KeepRecentTokens = 800
	cfg.Compaction.MaxSummaryTokens = 256
	store := memory.NewSQLiteHistoryStore(memory.SQLiteStoreOptions{Root: cfg.MemoryStore.Root}, cfg.History)
	ctx := context.Background()
	var provider model.Provider = processWindowModel{
		Provider: &scriptedProvider{},
		window:   model.WindowLimits{ContextTokens: 65536, OutputTokens: 8192},
	}
	var generator model.TextGenerator = processSummaryGenerator{}
	if os.Getenv("GAMEAGENT_HISTORY_PROCESS_REAL") == "true" {
		cfg.Compaction.MaxSummaryTokens = 2048
		configureHistoryProcessDiagnostic(t, os.Getenv("GAMEAGENT_HISTORY_PROCESS_DIAGNOSTIC"))
		modelPath := strings.TrimSpace(os.Getenv(llm.ConfigEnvName))
		if modelPath == "" {
			modelPath = filepath.Join("..", "..", "..", "runtime", "config", "model.json")
		} else if !filepath.IsAbs(modelPath) {
			modelPath = filepath.Join("..", "..", "..", modelPath)
		}
		configured, modelConfig, err := llm.NewProviderFromConfigFile(modelPath)
		if err != nil {
			t.Fatal(err)
		}
		if modelConfig.ContextTokens <= 0 || modelConfig.OutputTokens < cfg.Compaction.MaxSummaryTokens {
			t.Fatal("real acceptance requires explicit model window and output limit")
		}
		provider = configured
		generator, _ = configured.(model.TextGenerator)
		if generator == nil {
			t.Fatal("configured provider has no text interface")
		}
	}
	tick := int64(100)
	lastSeedID := ""
	if phase == "compact" {
		for i := 0; i < 12; i++ {
			batch := memory.HistoryBatch{Owner: key, Kind: memory.HistoryKindTerminal, Version: memory.HistoryVersion, TurnID: fmt.Sprintf("seed-%02d", i), Event: memory.HistoryEvent{ID: fmt.Sprintf("source-%02d", i), GameTime: memory.SnapshotGameTime(&protocol.GameTime{Tick: &tick}), Facts: []memory.SourceContextFact{{Kind: "dialogue", ActorEntityID: "player", Text: strings.Repeat("请记住这段对话：不要向外人透露口令7319。", 45)}}}, Terminal: memory.HistoryTerminal{Status: "completed"}}
			if i == 0 {
				batch.Event.Facts = []memory.SourceContextFact{
					{Kind: "dialogue", ActorEntityID: "player", Text: "我不喜欢雨天，口令7319不能告诉旁人。我说包裹已经送达了。"},
					{Kind: "dialogue", ActorEntityID: "other-witness", Text: "我是另一位证人，我喜欢雨天。"},
					{Kind: "choice", ActorEntityID: "player", Label: "box_choice", Text: "蓝色盒子"},
				}
				call := model.ToolCall{ID: "parcel-call", Name: "deliver_parcel", Arguments: map[string]any{"recipient": "merchant", "item": "red parcel"}}
				batch.Steps = []memory.HistoryStep{{
					Decision: model.ModelDecision{Control: model.ControlDirective{Kind: model.ControlSettle}, ToolCalls: []model.ToolCall{call}},
					Executions: []memory.HistoryExecution{{Call: call, ActionID: "parcel-action", Started: true,
						ActionResult: &protocol.ActionResult{ActionId: "parcel-action", Status: protocol.ActionStatus_ACTION_STATUS_FAILED,
							Error: &protocol.Error{Code: "route_blocked", Message: "路线被障碍物阻挡，红色包裹没有送到商人手中。"}}}},
				}}
			}
			source, err := store.AppendHistory(ctx, batch)
			if err != nil {
				t.Fatal(err)
			}
			lastSeedID = source.ID
		}
	} else {
		cfg.Compaction.Enabled = boolPtr(false)
		if phase == "rollback" {
			tick = 50
		} else {
			tick = 101
		}
	}
	modelRecorder := &recordingProcessModel{provider: provider}
	recorder := &recordingTraceRecorder{}
	loop := agent.NewLoop(modelRecorder, recorder, cfg, agent.WithHistoryStore(store), agent.WithSummaryGenerator(generator))
	event := gameEvent("probe-"+phase, key)
	event.GameTime = &protocol.GameTime{Tick: &tick}
	event.ContextFacts = []*protocol.ContextFact{{Kind: "dialogue", ActorEntityId: "player", Text: "请简短确认你记得当前可见的对话；无需采取游戏动作。"}}
	env := &fakeEnvironment{observations: []*protocol.Observation{{WorldId: key.WorldID, EntityId: key.EntityID, GameTime: &protocol.GameTime{Tick: &tick}}}}
	turnErr := loop.HandleEvent(ctx, env, agent.ConnectionContext{GameID: key.GameID}, key, entityTarget(key), environmentCatalogFromCapabilities(nil), event)
	snapshot, err := store.BeginHistorySnapshot(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseHistorySnapshot(snapshot)
	summary, err := store.ReadSummary(ctx, snapshot, memory.SnapshotGameTime(event.GameTime), memory.SummaryReadLimits{})
	if err != nil {
		t.Fatal(err)
	}
	report := processHistoryReport{PID: os.Getpid(), Profile: os.Getenv("GAMEAGENT_HISTORY_PROCESS_DIAGNOSTIC"), LastSeedID: lastSeedID, Watermark: snapshot.Watermark, Events: recorder.events, ElapsedMS: time.Since(started).Milliseconds()}
	if turnErr != nil {
		report.TurnError = turnErr.Error()
	}
	if len(modelRecorder.requests) > 0 {
		for _, message := range modelRecorder.requests[0].Messages {
			report.Request += message.Content
		}
	}
	if summary.Checkpoint != nil {
		report.SummaryID = summary.Checkpoint.ID
		report.SummaryText = summary.Checkpoint.Text
		report.SummarySources = summary.Checkpoint.Sources
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("GAMEAGENT_HISTORY_PROCESS_REPORT"), data, 0600); err != nil {
		t.Fatal(err)
	}
}
