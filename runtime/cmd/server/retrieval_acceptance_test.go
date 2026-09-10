package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	agentcontext "gameagent/runtime/internal/context"
	"gameagent/runtime/internal/llm"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/model"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	retrievalAcceptanceLiteral     = "青禾说：钓鱼时记下的数字是7319。"
	retrievalAcceptanceSummary     = "守林没有答应代送包裹。青禾谈过钓鱼并留下记录，摘要未保留具体数字。"
	retrievalAcceptanceQuery       = "青禾钓鱼时记下的数字是多少？请调用 record_answer 记录答案，依据当前可见的原文回答，不要猜测。"
	retrievalAcceptanceDescription = "记录对当前问题的回答。status 为 known 时，subject 填原文发言者的实体ID，number 填原文中的数字字符串；没有可用原文时 status 为 unknown，subject 和 number 都填空字符串。成功后本轮结束。"
)

// Each mode owns three real server processes. The real gate only authorizes
// decisions; the summary is a Store fixture and never requires an LLM call.
func TestRetrievalServerAcceptance(t *testing.T) {
	if os.Getenv("GAMEAGENT_RETRIEVAL_SERVER_ACCEPTANCE") != "1" {
		t.Skip("opt in with GAMEAGENT_RETRIEVAL_SERVER_ACCEPTANCE=1; requires free 127.0.0.1:50051")
	}
	t.Run("fake", func(t *testing.T) { runRetrievalServerAcceptance(t, false) })
	t.Run("real", func(t *testing.T) {
		if os.Getenv("GAMEAGENT_HISTORY_REAL_ACCEPTANCE") != "1" {
			t.Skip("real DeepSeek additionally requires GAMEAGENT_HISTORY_REAL_ACCEPTANCE=1")
		}
		runRetrievalServerAcceptance(t, true)
	})
}

type retrievalAcceptanceAnswer struct {
	Status  string `json:"status"`
	Subject string `json:"subject"`
	Number  string `json:"number"`
}

type retrievalAcceptanceBinary struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

func retrievalAcceptanceIdentifyBinary(path string) (retrievalAcceptanceBinary, error) {
	file, err := os.Open(path)
	if err != nil {
		return retrievalAcceptanceBinary{}, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return retrievalAcceptanceBinary{}, err
	}
	return retrievalAcceptanceBinary{SHA256: hex.EncodeToString(hash.Sum(nil)), Bytes: size}, nil
}

func TestRetrievalAcceptanceEvidenceMetadata(t *testing.T) {
	t.Run("binary identity", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "server.fixture")
		if err := os.WriteFile(path, []byte("abc"), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := retrievalAcceptanceIdentifyBinary(path)
		want := retrievalAcceptanceBinary{SHA256: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", Bytes: 3}
		if err != nil || got != want {
			t.Fatalf("binary identity = %+v, error = %v; want %+v", got, err, want)
		}
		if _, err := retrievalAcceptanceIdentifyBinary(filepath.Join(t.TempDir(), "absent")); err == nil {
			t.Fatal("absent binary must not produce an identity")
		}
	})
	t.Run("write timing retains its meaning", func(t *testing.T) {
		for _, event := range []string{"context_updated", "context_update_failed"} {
			path := filepath.Join(t.TempDir(), "trace.jsonl")
			historyAcceptanceWriteJSON(t, path, map[string]any{
				"event": event, "fields": map[string]any{
					"history_write_duration_ms": 17, "history_sequence": 4, "history_bytes": 512,
					"index_elapsed_ms": nil, "maintenance_elapsed_ms": -1, "wait_ms": "unmeasured", "unrelated": 42,
				},
			})
			got, err := retrievalAcceptanceReadTrace(path)
			want := map[string]int64{"history_write_duration_ms": 17, "history_sequence": 4, "history_bytes": 512}
			if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0].Fields, want) {
				t.Fatalf("%s safe evidence = %+v, error = %v; want only %v", event, got, err, want)
			}
		}
	})
	t.Run("literal appears only in retrieved section", func(t *testing.T) {
		store := memory.NewSQLiteHistoryStore(memory.SQLiteStoreOptions{Root: t.TempDir()}, memory.HistoryLimits{})
		source, checkpoint := retrievalAcceptanceSeed(t, store)
		historyJSON, err := json.Marshal(historyAcceptanceProjection{SummaryID: checkpoint.ID, SummaryText: checkpoint.Text})
		if err != nil {
			t.Fatal(err)
		}
		retrievedJSON, err := json.Marshal(agentcontext.RetrievedHistoryProjection{Snippets: []agentcontext.RetrievedHistorySnippet{{
			SourceID: source.ID, Sequence: source.Sequence, CreatedAt: source.CreatedAt, Times: source.Times,
			ActorID: "player:qinghe", Kind: "utterance", Path: "/event/facts/0/Text",
			End: utf8.RuneCountInString(retrievalAcceptanceLiteral), Text: retrievalAcceptanceLiteral,
		}}})
		if err != nil {
			t.Fatal(err)
		}
		content := "[History]\n" + string(historyJSON) + "\n\n[Retrieved History]\n" + string(retrievedJSON)
		request := func(extra map[string]any) []byte {
			value := map[string]any{"model": "controlled", "messages": []any{map[string]any{"role": "user", "content": content}}}
			for key, item := range extra {
				value[key] = item
			}
			body, marshalErr := json.Marshal(value)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			return body
		}
		if _, err := retrievalAcceptanceCheckRequest(request(nil), source, checkpoint, true); err != nil {
			t.Fatalf("clean request rejected: %v", err)
		}
		t.Run("wrong retrieved source identity", func(t *testing.T) {
			wrongSourceJSON, marshalErr := json.Marshal(agentcontext.RetrievedHistoryProjection{Snippets: []agentcontext.RetrievedHistorySnippet{{
				SourceID: "history_wrong_source", Sequence: source.Sequence, CreatedAt: source.CreatedAt, Times: source.Times,
				ActorID: "player:qinghe", Kind: "utterance", Path: "/event/facts/0/Text",
				End: utf8.RuneCountInString(retrievalAcceptanceLiteral), Text: retrievalAcceptanceLiteral,
			}}})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			wrongSourceContent := "[History]\n" + string(historyJSON) + "\n\n[Retrieved History]\n" + string(wrongSourceJSON)
			body, marshalErr := json.Marshal(map[string]any{"model": "controlled", "messages": []any{map[string]any{"role": "user", "content": wrongSourceContent}}})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if _, checkErr := retrievalAcceptanceCheckRequest(body, source, checkpoint, true); checkErr == nil {
				t.Fatal("retrieved literal with the wrong source identity was accepted")
			}
		})
		for name, extra := range map[string]map[string]any{
			"tool": {"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "record_answer", "description": "hidden 7319"}}}},
			"message": {"messages": []any{
				map[string]any{"role": "system", "content": "hidden 7319"},
				map[string]any{"role": "user", "content": content},
			}},
		} {
			t.Run(name, func(t *testing.T) {
				if _, err := retrievalAcceptanceCheckRequest(request(extra), source, checkpoint, true); err == nil {
					t.Fatal("literal outside Retrieved History was accepted")
				}
			})
		}
	})
}

type retrievalAcceptanceServerMetric struct {
	Time   time.Time        `json:"time"`
	Stage  string           `json:"stage"`
	Fields map[string]int64 `json:"fields"`
}

type retrievalAcceptanceServerLog struct {
	mu       sync.Mutex
	pending  []byte
	dropping bool
	dropped  int
	metrics  []retrievalAcceptanceServerMetric
	rebuilt  chan struct{}
	once     sync.Once
}

var retrievalAcceptanceRebuildLine = regexp.MustCompile(`^(?P<time>[0-9]{4}/[0-9]{2}/[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,9})?) history maintenance owner=` +
	regexp.QuoteMeta(historyAcceptanceOwner.DiagnosticID()) +
	` stage=rebuild processed=(?P<processed>[0-9]+) bytes=(?P<bytes>[0-9]+)(?: read_sources=(?P<read_sources>[0-9]+) read_bytes=(?P<read_bytes>[0-9]+))? next_after=(?P<next_after>[0-9]+) more=(?P<more>true|false) valid=(?P<valid>true|false) diagnostics=\[(?:"(?:[^"\\\r\n]|\\.)*" ?)*\] elapsed_ms=(?P<elapsed>[0-9]+) error=(?P<error>[^\r\n]*)$`)

var retrievalAcceptanceQueueLine = regexp.MustCompile(`^([0-9]{4}/[0-9]{2}/[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,9})?) history maintenance owner=` +
	regexp.QuoteMeta(historyAcceptanceOwner.DiagnosticID()) + ` stage=queue wait_ms=([0-9]+)$`)

// Raw output is never written to disk. Oversized lines are discarded through
// their newline, so a retained suffix cannot impersonate a complete log entry.
func (l *retrievalAcceptanceServerLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	size := len(p)
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		part := p
		if end >= 0 {
			part = p[:end]
		}
		if !l.dropping {
			if len(l.pending)+len(part) > 8192 {
				l.pending = l.pending[:0]
				l.dropping = true
				l.dropped++
			} else {
				l.pending = append(l.pending, part...)
			}
		}
		if end < 0 {
			break
		}
		if !l.dropping {
			if metric, ok := retrievalAcceptanceParseMetric(strings.TrimSuffix(string(l.pending), "\r")); ok {
				if len(l.metrics) < 32 {
					l.metrics = append(l.metrics, metric)
					if metric.Stage == "rebuild" {
						l.once.Do(func() { close(l.rebuilt) })
					}
				} else {
					l.dropped++
				}
			}
		}
		l.pending = l.pending[:0]
		l.dropping = false
		p = p[end+1:]
	}
	return size, nil
}

func retrievalAcceptanceParseMetric(line string) (retrievalAcceptanceServerMetric, bool) {
	if queue := retrievalAcceptanceQueueLine.FindStringSubmatch(line); queue != nil {
		timestamp, timeErr := time.ParseInLocation("2006/01/02 15:04:05", queue[1], time.Local)
		wait, waitErr := strconv.ParseInt(queue[2], 10, 64)
		return retrievalAcceptanceServerMetric{Time: timestamp, Stage: "queue", Fields: map[string]int64{"queue_wait_ms": wait}}, timeErr == nil && waitErr == nil
	}
	metric := retrievalAcceptanceServerMetric{Stage: "rebuild", Fields: map[string]int64{}}
	match := retrievalAcceptanceRebuildLine.FindStringSubmatch(line)
	if match == nil {
		return metric, false
	}
	for index, name := range retrievalAcceptanceRebuildLine.SubexpNames() {
		if name == "" || match[index] == "" {
			continue
		}
		value := match[index]
		switch name {
		case "time":
			parsed, err := time.ParseInLocation("2006/01/02 15:04:05", value, time.Local)
			if err != nil {
				return metric, false
			}
			metric.Time = parsed
		case "error":
			metric.Fields["succeeded"] = 0
			if value == "<nil>" {
				metric.Fields["succeeded"] = 1
			}
		case "more", "valid":
			metric.Fields[name] = 0
			if value == "true" {
				metric.Fields[name] = 1
			}
		default:
			number, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return metric, false
			}
			if name == "elapsed" {
				metric.Fields["rebuild_elapsed_ms"] = number
				metric.Fields["maintenance_elapsed_ms"] = number
			} else {
				metric.Fields[name] = number
			}
		}
	}
	return metric, true
}

func TestRetrievalAcceptanceOwnedLog(t *testing.T) {
	line := "2026/09/10 01:02:03 history maintenance owner=" + historyAcceptanceOwner.DiagnosticID() +
		" stage=rebuild processed=2 bytes=512 read_sources=3 read_bytes=768 next_after=4 more=false valid=true diagnostics=[] elapsed_ms=7 error=<nil>\n"
	t.Run("gateway queue timing is observed separately", func(t *testing.T) {
		capture := &retrievalAcceptanceServerLog{rebuilt: make(chan struct{})}
		queue := "2026/09/10 01:02:03 history maintenance owner=" + historyAcceptanceOwner.DiagnosticID() + " stage=queue wait_ms=0\n"
		_, _ = capture.Write([]byte(queue))
		if len(capture.metrics) != 1 || capture.metrics[0].Stage != "queue" || !reflect.DeepEqual(capture.metrics[0].Fields, map[string]int64{"queue_wait_ms": 0}) {
			t.Fatal("observed zero queue wait was not preserved")
		}
		select {
		case <-capture.rebuilt:
			t.Fatal("queue log cannot complete maintenance")
		default:
		}
		_, _ = capture.Write([]byte(strings.Replace(queue, "wait_ms=0", "wait_ms=-1", 1)))
		_, _ = capture.Write([]byte(strings.Replace(queue, "wait_ms=0", "wait_ms=9223372036854775808", 1)))
		_, _ = capture.Write([]byte(strings.Replace(queue, "agent:shoulin", "agent:other", 1)))
		if len(capture.metrics) != 1 {
			t.Fatal("invalid queue log was admitted")
		}
		_, _ = capture.Write([]byte(line))
		select {
		case <-capture.rebuilt:
		default:
			t.Fatal("rebuild did not complete maintenance")
		}
	})
	t.Run("chunked safe numeric projection", func(t *testing.T) {
		capture := &retrievalAcceptanceServerLog{rebuilt: make(chan struct{})}
		for _, part := range []string{line[:31], line[31:]} {
			if n, err := capture.Write([]byte(part)); err != nil || n != len(part) {
				t.Fatal("short log write")
			}
		}
		want := map[string]int64{"processed": 2, "bytes": 512, "read_sources": 3, "read_bytes": 768, "next_after": 4, "more": 0, "valid": 1, "succeeded": 1, "rebuild_elapsed_ms": 7, "maintenance_elapsed_ms": 7}
		if len(capture.metrics) != 1 || capture.metrics[0].Stage != "rebuild" || !reflect.DeepEqual(capture.metrics[0].Fields, want) || capture.metrics[0].Time.IsZero() {
			t.Fatalf("safe maintenance metrics = %+v; want %v", capture.metrics, want)
		}
		select {
		case <-capture.rebuilt:
		default:
			t.Fatal("completed rebuild did not signal the Adapter")
		}
	})
	t.Run("ignore foreign unbounded and unstructured text", func(t *testing.T) {
		capture := &retrievalAcceptanceServerLog{rebuilt: make(chan struct{})}
		for _, input := range []string{
			"unstructured diagnostic\n", strings.Replace(line, "agent:shoulin", "agent:other", 1),
			strings.Replace(line, "elapsed_ms=7", "elapsed_ms=-1", 1),
			strings.Replace(line, "elapsed_ms=7", "elapsed_ms=9223372036854775808", 1),
			strings.Repeat("x", 8193), line,
		} {
			_, _ = capture.Write([]byte(input))
		}
		if len(capture.metrics) != 0 || len(capture.pending) > 8192 {
			t.Fatal("unsafe or oversized log was admitted")
		}
		_, _ = capture.Write([]byte(line))
		if len(capture.metrics) != 1 {
			t.Fatal("bounded parser did not recover at the next complete line")
		}
	})
	t.Run("errors remain outside evidence", func(t *testing.T) {
		capture := &retrievalAcceptanceServerLog{rebuilt: make(chan struct{})}
		_, _ = capture.Write([]byte(strings.Replace(line, "error=<nil>", "error=private-error-marker", 1)))
		data, _ := json.Marshal(capture.metrics)
		if len(capture.metrics) != 1 || capture.metrics[0].Fields["succeeded"] != 0 || bytes.Contains(data, []byte("private-error-marker")) {
			t.Fatal("unsafe failure evidence")
		}
	})
	t.Run("bounded event count", func(t *testing.T) {
		capture := &retrievalAcceptanceServerLog{rebuilt: make(chan struct{})}
		_, _ = capture.Write([]byte(strings.Repeat(line, 40)))
		if len(capture.metrics) != 32 || capture.dropped != 8 {
			t.Fatal("maintenance evidence must be bounded")
		}
	})
}

func runRetrievalServerAcceptance(t *testing.T, real bool) {
	mode := "retrieval-fake"
	if real {
		mode = "retrieval-real"
	}
	reportDir := historyAcceptanceReportDirectory(t, mode)
	if err := historyAcceptanceCheckPort(historyAcceptanceAddress); err != nil {
		t.Fatal("BLOCKED: 127.0.0.1:50051 is occupied or unavailable; existing processes were left untouched")
	}
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	config := llm.Config{Provider: "deepseek", Model: "controlled-retrieval", WindowLimits: model.WindowLimits{ContextTokens: 65536, OutputTokens: 2048}}
	var upstream, key string
	if real {
		config, err = llm.LoadConfig(filepath.Join(repo, "runtime", "config", "model.json"))
		if err != nil {
			t.Fatal("BLOCKED: cannot read repository runtime/config/model.json")
		}
		upstream, err = historyAcceptanceUpstream(config)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(config.APIKey, "env:") {
			t.Fatal("BLOCKED: credentials must reference a process environment variable")
		}
		key = strings.TrimSpace(os.Getenv(strings.TrimSpace(strings.TrimPrefix(config.APIKey, "env:"))))
		if key == "" {
			t.Fatal("BLOCKED: model.json credential is absent from the process environment")
		}
	}
	if config.WindowLimits.Validate() != nil || config.ContextTokens == 0 || config.OutputTokens < 2048 {
		t.Fatal("BLOCKED: model window must support at least 2048 output tokens")
	}
	configBytes, err := os.ReadFile(filepath.Join(repo, "runtime", "config", "agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	var agentConfig map[string]any
	if err := json.Unmarshal(configBytes, &agentConfig); err != nil {
		t.Fatal(err)
	}
	memRoot := filepath.Join(root, "memory")
	agentConfig["memory_store"] = map[string]any{"kind": "sqlite", "root": memRoot}
	agentConfig["memory_enabled"] = true
	agentConfig["definition_catalog_root"] = ""
	agentConfig["compaction_enabled"] = false

	binary := filepath.Join(root, "retrieval-server")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./runtime/cmd/server")
	build.Dir = repo
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build production server: %v\n%s", err, out)
	}
	binaryIdentity, err := retrievalAcceptanceIdentifyBinary(binary)
	if err != nil {
		t.Fatal("cannot identify the built production server binary")
	}
	if reportDir != "" {
		historyAcceptanceWriteJSON(t, filepath.Join(reportDir, "server-binary.json"), binaryIdentity)
	}
	store := memory.NewSQLiteHistoryStore(memory.SQLiteStoreOptions{Root: memRoot}, memory.HistoryLimits{})
	detail, checkpoint := retrievalAcceptanceSeed(t, store)
	var firstSnippet *agentcontext.RetrievedHistorySnippet
	pids := map[int]bool{}
	completed := 0
	for index, phase := range []struct {
		name    string
		tick    int64
		visible bool
	}{
		{"query", 130, true}, {"rollback", 100, false},
		// Catch up to the source, before the first answer: that answer cannot
		// supply the number through Recent History or its own search index.
		{"catchup", 120, true},
	} {
		if !t.Run(phase.name, func(t *testing.T) {
			if completed != index {
				t.Fatal("select the whole fake or real scenario, including preceding processes")
			}
			work := filepath.Join(root, phase.name)
			process := &historyAcceptanceProcess{}
			serverLog := &retrievalAcceptanceServerLog{rebuilt: make(chan struct{})}
			var server *httptest.Server
			var capture *historyAcceptanceHTTP
			evidence := map[string]any{
				"mode": mode, "phase": phase.name, "game_time": historyAcceptanceTime(phase.tick),
				"server_binary": binaryIdentity,
				"source":        detail, "fixture_checkpoint": checkpoint, "human_review_required": true,
				"truth_label_contract": "known: player:qinghe / 7319; hidden: unknown / empty subject and number",
			}
			t.Cleanup(func() {
				if !process.stop(t) {
					return
				}
				if server != nil {
					server.CloseClientConnections()
					server.Close()
					capture.active.Wait()
				}
				serverLog.mu.Lock()
				evidence["safe_server_log"] = append([]retrievalAcceptanceServerMetric(nil), serverLog.metrics...)
				evidence["server_log_dropped_lines"] = serverLog.dropped
				serverLog.mu.Unlock()
				retrievalAcceptanceExport(t, reportDir, phase.name, work, memRoot, process, capture, evidence)
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
			if real {
				server, capture = historyAcceptanceProxy(t, localKey, upstream, key, phase.name)
			} else {
				server, capture = retrievalAcceptanceFakeProxy(t, localKey)
			}
			childConfig := config
			childConfig.APIKey = "env:" + historyAcceptanceKeyEnv
			childConfig.BaseURL = server.URL
			modelPath, agentPath := filepath.Join(configRoot, "model.json"), filepath.Join(configRoot, "agent.json")
			historyAcceptanceWriteJSON(t, modelPath, childConfig)
			historyAcceptanceWriteJSON(t, agentPath, agentConfig)
			actual, err := agent.LoadConfigFile(agentPath)
			if err != nil {
				t.Fatal(err)
			}
			if actual.LLMTimeout != 8*time.Second || !actual.Retrieval.EnabledValue() || actual.Retrieval.Limit != 5 || actual.Retrieval.MaxTokens != 1024 || actual.RetentionDays != 0 || actual.Compaction.MaxSummaryTokens != 2048 {
				t.Fatal("BLOCKED: configured acceptance limits differ from 8s / retrieval enabled 5 / 1024 tokens / retention 0 / summary 2048")
			}
			evidence["parameters"] = map[string]any{
				"llm_timeout_ms": actual.LLMTimeout.Milliseconds(), "retrieval": actual.Retrieval,
				"history": actual.History, "history_index": actual.HistoryIndex, "history_maintenance": actual.HistoryMaintenance,
				"retention_days": actual.RetentionDays, "keep_recent_tokens": actual.Compaction.KeepRecentTokens,
				"max_summary_tokens": actual.Compaction.MaxSummaryTokens, "compaction_enabled": false,
				"model_window": config.WindowLimits, "provider_thinking": "production unchanged",
			}
			t.Logf("phase=%s llm_timeout_ms=8000 retrieval_limit=5 retrieval_tokens=1024 keep_recent_tokens=%d summary_tokens=2048 retention_days=0 thinking=production", phase.name, actual.Compaction.KeepRecentTokens)
			before, err := retrievalAcceptanceMeasure(memRoot)
			if err != nil {
				t.Fatal(err)
			}
			evidence["before_start"] = before
			historyAcceptanceStart(t, binary, work, modelPath, agentPath, localKey, process, serverLog)
			pid := process.cmd.Process.Pid
			if pids[pid] {
				t.Fatal("stages must use distinct server PIDs")
			}
			pids[pid] = true
			afterStart, err := retrievalAcceptanceMeasure(memRoot)
			if err != nil {
				t.Fatal(err)
			}
			evidence["after_restart_before_turn"] = afterStart
			if !reflect.DeepEqual(before, afterStart) {
				t.Fatal("server startup changed the seeded history/index before any Turn")
			}
			saved, err := store.ReadHistorySource(context.Background(), historyAcceptanceOwner, detail.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(saved, detail) {
				t.Fatal("original source ID, bytes or header changed across restart")
			}
			snapshot, err := store.BeginHistorySnapshot(context.Background(), historyAcceptanceOwner)
			if err != nil {
				t.Fatal(err)
			}
			evidence["snapshot_before_turn"] = snapshot
			store.ReleaseHistorySnapshot(snapshot)
			_, visibleSummary := historyAcceptanceReadStore(t, store, phase.tick)
			evidence["visible_checkpoint_before_turn"] = visibleSummary
			eventID := "retrieval:" + phase.name
			retrievalAcceptanceTurn(t, process, eventID, phase.tick, evidence, serverLog)
			historyAcceptanceWaitTrace(t, work, eventID)
			capture.mu.Lock()
			calls, transportErrors := append([]historyAcceptanceCall(nil), capture.calls...), append([]string(nil), capture.errs...)
			capture.mu.Unlock()
			if len(transportErrors) != 0 || len(calls) != 1 || calls[0].Summary {
				t.Fatal("expected one decision HTTP request and no summary request or transport error")
			}
			snippet, err := retrievalAcceptanceCheckRequest(calls[0].Body, detail, checkpoint, phase.visible)
			if err != nil {
				t.Fatal(err)
			}
			if phase.visible {
				if firstSnippet == nil {
					firstSnippet = snippet
				} else if !reflect.DeepEqual(*firstSnippet, *snippet) {
					t.Fatal("catchup did not restore the same original snippet bytes, ID, subject and times")
				}
			}
			answer := evidence["answer"].(retrievalAcceptanceAnswer)
			want := retrievalAcceptanceAnswer{Status: "unknown"}
			if phase.visible {
				want = retrievalAcceptanceAnswer{Status: "known", Subject: "player:qinghe", Number: "7319"}
			}
			if answer != want {
				t.Fatal("answer truth labels disagree with visible source; review the controlled answer evidence independently")
			}
			page, durableSummary := historyAcceptanceReadStore(t, store, 130)
			if durableSummary == nil || durableSummary.ID != checkpoint.ID || durableSummary.Text != checkpoint.Text {
				t.Fatal("fixture cumulative checkpoint changed")
			}
			foundTurn := false
			for _, source := range page.Sources {
				if source.Batch != nil && source.Batch.Event.ID == eventID {
					foundTurn = true
					evidence["completed_turn_source"] = source
				}
			}
			if !foundTurn {
				t.Fatal("gRPC Turn did not persist terminal history")
			}
			t.Logf("phase=%s server_pid=%d source_id=%s answer=%s raw_bytes=%d index_text_bytes=%d", phase.name, pid, detail.ID, answer.Status, afterStart.RawBytes, afterStart.IndexTextBytes)
			completed++
		}) {
			return
		}
	}
	if completed != 3 {
		t.Fatal("acceptance requires query, rollback, and catchup in three server processes")
	}
}

func retrievalAcceptanceSeed(t *testing.T, store *memory.SQLiteHistoryStore) (memory.HistorySource, memory.SummaryCheckpoint) {
	t.Helper()
	appendSource := func(id string, tick int64, actor, text string) memory.HistorySource {
		source, err := store.AppendHistory(context.Background(), memory.HistoryBatch{
			Owner: historyAcceptanceOwner, Kind: memory.HistoryKindTerminal, Version: memory.HistoryVersion, TurnID: "turn:retrieval-seed:" + id,
			Event: memory.HistoryEvent{ID: "retrieval-seed:" + id, Type: "acceptance.input", GameTime: memory.SnapshotGameTime(historyAcceptanceTime(tick)),
				Facts: []memory.SourceContextFact{{Kind: "utterance", ActorEntityID: actor, Text: text}}},
			Terminal: memory.HistoryTerminal{Status: "completed"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return source
	}
	commit := func(parent, text string, source memory.HistorySource) memory.SummaryCheckpoint {
		snapshot, err := store.BeginHistorySnapshot(context.Background(), historyAcceptanceOwner)
		if err != nil {
			t.Fatal(err)
		}
		defer store.ReleaseHistorySnapshot(snapshot)
		checkpoint, err := store.CommitSummary(context.Background(), memory.SummaryCommit{
			Snapshot: snapshot, ExpectedRevision: snapshot.SummaryRevision, ParentID: parent,
			Version: "retrieval-controlled-v1", Text: text, MaxOutputTokens: 2048,
			Sources: []memory.HistorySourceRef{{ID: source.ID, Sequence: source.Sequence, Fingerprint: source.Fingerprint}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return checkpoint
	}
	first := appendSource("past", 70, "agent:shoulin", "守林说：我没有答应代送包裹。")
	parent := commit("", "守林没有答应代送包裹。", first)
	detail := appendSource("detail", 120, "player:qinghe", retrievalAcceptanceLiteral)
	checkpoint := commit(parent.ID, retrievalAcceptanceSummary, detail)
	appendSource("recent", 90, "player:qinghe", "青禾说：门口的白色木箱保持原样。")
	if strings.Contains(checkpoint.Text, "7319") || len(checkpoint.Sources) != 2 {
		t.Fatal("invalid controlled summary coverage")
	}
	return detail, checkpoint
}

// Fixture checks are exact provenance/label checks, not an independent semantic
// evaluation of arbitrary natural-language answers.
func retrievalAcceptanceCheckRequest(body []byte, source memory.HistorySource, checkpoint memory.SummaryCheckpoint, visible bool) (*agentcontext.RetrievedHistorySnippet, error) {
	wire, err := historyAcceptanceDecodeRequest(body)
	if err != nil {
		return nil, err
	}
	var history historyAcceptanceProjection
	var retrieved agentcontext.RetrievedHistoryProjection
	historyCount, retrievalCount := 0, 0
	for _, message := range wire.Messages {
		for _, section := range strings.Split(message.Content, "\n\n") {
			if strings.HasPrefix(section, "[History]\n") {
				historyCount++
				if err := json.Unmarshal([]byte(strings.TrimPrefix(section, "[History]\n")), &history); err != nil {
					return nil, err
				}
			}
			if strings.HasPrefix(section, "[Retrieved History]\n") {
				retrievalCount++
				if err := json.Unmarshal([]byte(strings.TrimPrefix(section, "[Retrieved History]\n")), &retrieved); err != nil {
					return nil, err
				}
			}
		}
	}
	if historyCount != 1 || retrievalCount > 1 {
		return nil, errors.New("unexpected History/Retrieved History section count")
	}
	outsideRetrieved, err := retrievalAcceptanceRequestOutsideRetrieved(body)
	if err != nil {
		return nil, err
	}
	if bytes.Contains(outsideRetrieved, []byte("7319")) || bytes.Contains(outsideRetrieved, []byte(source.ID)) {
		return nil, errors.New("literal or source identity appeared outside Retrieved History")
	}
	raw, _ := json.Marshal(history.Sources)
	if bytes.Contains(raw, []byte("7319")) {
		return nil, errors.New("literal leaked into recent raw tail instead of retrieval")
	}
	for _, item := range history.Sources {
		if item.ID == source.ID {
			return nil, errors.New("summary-covered source remained in recent raw tail")
		}
	}
	if !visible {
		if history.SummaryID == checkpoint.ID || history.SummaryText == checkpoint.Text || bytes.Contains(body, []byte("7319")) || bytes.Contains(body, []byte(source.ID)) {
			return nil, errors.New("rollback exposed future literal/source or cumulative summary")
		}
		return nil, nil
	}
	if history.SummaryID != checkpoint.ID || history.SummaryText != checkpoint.Text {
		return nil, errors.New("visible cumulative summary was not restored")
	}
	if len(retrieved.Snippets) > 5 {
		return nil, errors.New("retrieval exceeded five snippets")
	}
	var found *agentcontext.RetrievedHistorySnippet
	for i := range retrieved.Snippets {
		snippet := retrieved.Snippets[i]
		if snippet.SourceID != source.ID && !strings.Contains(snippet.Text, "7319") {
			continue
		}
		if found != nil {
			return nil, errors.New("retrieved literal or source identity appeared in multiple snippets")
		}
		if snippet.SourceID != source.ID || snippet.Text != retrievalAcceptanceLiteral || snippet.ActorID != "player:qinghe" || snippet.Sequence != source.Sequence ||
			snippet.Start != 0 || snippet.End != utf8.RuneCountInString(retrievalAcceptanceLiteral) || snippet.Cropped || !reflect.DeepEqual(snippet.Times, source.Times) || snippet.Path == "" || snippet.Kind != "utterance" {
			return nil, errors.New("retrieved literal lost original bytes, subject, span, kind or source times")
		}
		found = &retrieved.Snippets[i]
	}
	if found == nil {
		return nil, errors.New("BLOCKED: final decision HTTP request has no original 7319 retrieval snippet; verify Agent/Context/Store retrieval wiring")
	}
	return found, nil
}

func retrievalAcceptanceRequestOutsideRetrieved(body []byte) ([]byte, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	messages, ok := request["messages"].([]any)
	if !ok {
		return nil, errors.New("model request messages are unavailable")
	}
	for _, value := range messages {
		message, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("invalid model request message")
		}
		content, ok := message["content"].(string)
		if !ok {
			return nil, errors.New("invalid model request message content")
		}
		sections := strings.Split(content, "\n\n")
		for i, section := range sections {
			if strings.HasPrefix(section, "[Retrieved History]\n") {
				sections[i] = "[Retrieved History]\n{}"
			}
		}
		message["content"] = strings.Join(sections, "\n\n")
	}
	return json.Marshal(request)
}

func retrievalAcceptanceFakeProxy(t *testing.T, localKey string) (*httptest.Server, *historyAcceptanceHTTP) {
	capture := &historyAcceptanceHTTP{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.active.Add(1)
		defer capture.active.Done()
		fail := func() {
			capture.mu.Lock()
			capture.errs = append(capture.errs, "invalid controlled decision request")
			capture.mu.Unlock()
			http.Error(w, "invalid controlled request", 400)
		}
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+localKey {
			fail()
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
		if err != nil || len(body) > 1<<20 {
			fail()
			return
		}
		if _, err := historyAcceptanceDecodeRequest(body); err != nil {
			fail()
			return
		}
		capture.mu.Lock()
		count := len(capture.calls)
		capture.calls = append(capture.calls, historyAcceptanceCall{Body: body})
		capture.mu.Unlock()
		if count != 0 {
			fail()
			return
		}
		answer := retrievalAcceptanceAnswer{Status: "unknown"}
		if bytes.Contains(body, []byte("7319")) {
			answer = retrievalAcceptanceAnswer{Status: "known", Subject: "player:qinghe", Number: "7319"}
		}
		args, _ := json.Marshal(answer)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
				"id": "controlled-answer", "type": "function", "function": map[string]any{"name": "record_answer", "arguments": string(args)},
			}}},
		}}})
	}))
	t.Cleanup(server.Close)
	if host, _, err := net.SplitHostPort(server.Listener.Addr().String()); err != nil || host != "127.0.0.1" {
		t.Fatal("fixture must listen on 127.0.0.1 only")
	}
	return server, capture
}

func retrievalAcceptanceTurn(t *testing.T, process *historyAcceptanceProcess, eventID string, tick int64, evidence map[string]any, serverLog *retrievalAcceptanceServerLog) {
	t.Helper()
	process.requireAlive(t)
	ctx, cancel := context.WithTimeout(process.ctx, 30*time.Second)
	defer cancel()
	dialCtx, cancelDial := context.WithTimeout(ctx, 5*time.Second)
	defer cancelDial()
	var mu sync.Mutex
	dialed := false
	conn, err := grpc.DialContext(dialCtx, historyAcceptanceAddress, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock(),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			mu.Lock()
			defer mu.Unlock()
			if dialed || process.ctx.Err() != nil {
				return nil, errors.New("acceptance cannot redial another service")
			}
			dialed = true
			return (&net.Dialer{}).DialContext(ctx, "tcp4", historyAcceptanceAddress)
		}))
	if err != nil {
		t.Fatal(err)
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
			t.Fatal(err)
		}
	}
	recv := func() *protocol.RuntimeMessage {
		t.Helper()
		message, err := stream.Recv()
		if err != nil {
			t.Fatal("controlled gRPC response failed")
		}
		return message
	}
	sessionID := "session:" + eventID
	send(&protocol.AdapterMessage{MessageId: "hello", Payload: &protocol.AdapterMessage_Hello{Hello: &protocol.AdapterHello{
		AdapterId: "controlled-retrieval-adapter", AdapterVersion: "0.1.0", ProtocolVersion: "v1alpha2", GameId: historyAcceptanceOwner.GameID, GameVersion: "0.1.0", SessionId: sessionID,
	}}})
	if ready := recv().GetEnvironmentReady(); ready == nil || ready.SessionId != sessionID {
		t.Fatal("unexpected EnvironmentReady identity")
	}
	request := recv()
	if request.GetCapabilityRequest() == nil {
		t.Fatal("expected capability discovery")
	}
	policy, err := structpb.NewStruct(map[string]any{"gameagent": map[string]any{"tool_policy": map[string]any{"exclusive_per_step": true, "settle_after_success": true}}})
	if err != nil {
		t.Fatal(err)
	}
	send(&protocol.AdapterMessage{MessageId: "capabilities", CorrelationId: request.MessageId, Payload: &protocol.AdapterMessage_Capabilities{Capabilities: &protocol.CapabilityList{
		Revision: 1, Capabilities: []*protocol.Capability{{Name: "record_answer", Version: "0.1.0", Description: retrievalAcceptanceDescription,
			InputSchemaJson: `{"type":"object","properties":{"status":{"type":"string","enum":["known","unknown"]},"subject":{"type":"string"},"number":{"type":"string"}},"required":["status","subject","number"],"additionalProperties":false}`,
			ExecutionMode:   protocol.ExecutionMode_EXECUTION_MODE_SYNC, ConcurrencyMode: protocol.CapabilityConcurrencyMode_CAPABILITY_CONCURRENCY_MODE_SEQUENTIAL, Extensions: policy}},
	}}})
	send(&protocol.AdapterMessage{MessageId: "event", Payload: &protocol.AdapterMessage_Event{Event: &protocol.GameEvent{
		EventId: eventID, EventType: "acceptance.input", WorldId: historyAcceptanceOwner.WorldID, TargetEntityId: historyAcceptanceOwner.EntityID, Sequence: 1, GameTime: historyAcceptanceTime(tick),
		Entities:     []*protocol.EntityRef{{EntityId: historyAcceptanceOwner.EntityID, EntityType: "agent", DisplayName: "守林", DefinitionId: "agent:shoulin"}, {EntityId: "player:qinghe", EntityType: "player", DisplayName: "青禾", DefinitionId: "player:qinghe"}},
		ContextFacts: []*protocol.ContextFact{{Kind: "utterance", ActorEntityId: "player:qinghe", TargetEntityId: historyAcceptanceOwner.EntityID, Text: retrievalAcceptanceQuery}},
	}}})
	if ack := recv().GetEventAck(); ack == nil || ack.EventId != eventID || ack.Status != protocol.EventAckStatus_EVENT_ACK_STATUS_ACCEPTED {
		t.Fatal("controlled event was not accepted")
	}
	observe := recv()
	if request := observe.GetObserve(); request == nil || request.WorldId != historyAcceptanceOwner.WorldID || request.EntityId != historyAcceptanceOwner.EntityID {
		t.Fatal("Observe has the wrong owner")
	}
	send(&protocol.AdapterMessage{MessageId: "observation", CorrelationId: observe.MessageId, Payload: &protocol.AdapterMessage_Observation{Observation: &protocol.Observation{
		WorldId: historyAcceptanceOwner.WorldID, EntityId: historyAcceptanceOwner.EntityID, Revision: 1, GameTime: historyAcceptanceTime(tick),
	}}})
	message := recv()
	action := message.GetAction()
	if completion := message.GetTurnCompletion(); completion != nil {
		evidence["completion_status"] = completion.Status.String()
	}
	if action == nil || action.Capability != "record_answer" || action.SourceEventId != eventID || action.SourceTurnId == "" {
		t.Fatal("model did not issue a source-correlated record_answer action")
	}
	arguments, err := json.Marshal(action.GetArguments().AsMap())
	if err != nil {
		t.Fatal(err)
	}
	var answer retrievalAcceptanceAnswer
	if err := json.Unmarshal(arguments, &answer); err != nil {
		t.Fatal("invalid answer truth-label JSON")
	}
	evidence["answer"] = answer
	evidence["answer_tool_arguments"] = json.RawMessage(arguments)
	evidence["turn_id"] = action.SourceTurnId
	send(&protocol.AdapterMessage{MessageId: "answer-result", CorrelationId: message.MessageId, Payload: &protocol.AdapterMessage_ActionResult{ActionResult: &protocol.ActionResult{
		ActionId: action.ActionId, Status: protocol.ActionStatus_ACTION_STATUS_SUCCEEDED,
	}}})
	completion := recv().GetTurnCompletion()
	if completion == nil {
		t.Fatal("expected terminal Turn after record_answer success")
	}
	evidence["completion_status"] = completion.Status.String()
	evidence["settle_after_success"] = true
	if completion.EventId != eventID || completion.EntityId != historyAcceptanceOwner.EntityID || completion.TurnId != action.SourceTurnId || completion.Status != protocol.TurnCompletionStatus_TURN_COMPLETION_STATUS_COMPLETED {
		t.Fatal("controlled Turn did not complete successfully")
	}
	// Keep the Adapter connected while the post-Turn maintenance lane runs.
	// Closing it earlier can cancel the queued task before it emits evidence.
	select {
	case <-serverLog.rebuilt:
	case <-ctx.Done():
		t.Fatal("controlled Adapter ended before maintenance evidence arrived")
	case <-time.After(5 * time.Second):
		t.Fatal("BLOCKED: owned server emitted no bounded maintenance completion log")
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatal("Adapter stream did not drain terminal history to EOF")
	}
}

type retrievalAcceptanceSize struct {
	Schema         string `json:"schema"`
	Head           int64  `json:"history_head"`
	SummaryHead    int64  `json:"summary_head"`
	Sources        int64  `json:"raw_sources"`
	RawBytes       int64  `json:"raw_payload_bytes"`
	IndexSources   int64  `json:"indexed_sources"`
	Fragments      int64  `json:"index_fragments"`
	Terms          int64  `json:"index_term_associations"`
	IndexTextBytes int64  `json:"index_text_column_bytes"`
	DatabaseBytes  int64  `json:"database_page_bytes"`
}

// Read-only SQL measures logical UTF-8 index text separately from physical
// database pages; neither number is an estimate of isolated index disk usage.
func retrievalAcceptanceMeasure(root string) (retrievalAcceptanceSize, error) {
	var result retrievalAcceptanceSize
	path, err := memory.SQLiteDatabasePath(root, historyAcceptanceOwner.GameID, historyAcceptanceOwner.WorldID)
	if err != nil {
		return result, err
	}
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return result, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = db.QueryRowContext(ctx, `SELECT
		(SELECT value FROM memory_schema_metadata WHERE key='schema_version'),
		COALESCE(MAX(sequence),0), COUNT(*), COALESCE(SUM(length(CAST(payload_json AS BLOB))),0),
		(SELECT COALESCE(MAX(revision),0) FROM context_summaries),
		(SELECT COUNT(*) FROM history_index_sources), (SELECT COUNT(*) FROM history_fragments), (SELECT COUNT(*) FROM history_terms),
		(SELECT COALESCE(SUM(length(CAST(source_id||content_fingerprint||index_signature||status AS BLOB))),0) FROM history_index_sources) +
		(SELECT COALESCE(SUM(length(CAST(source_id||field_path||actor_id||field_kind||original_text AS BLOB))),0) FROM history_fragments) +
		(SELECT COALESCE(SUM(length(CAST(game_id||world_id||entity_id||term||source_id||field_path AS BLOB))),0) FROM history_terms),
		(SELECT page_count FROM pragma_page_count) * (SELECT page_size FROM pragma_page_size)
		FROM session_history`).Scan(&result.Schema, &result.Head, &result.Sources, &result.RawBytes, &result.SummaryHead, &result.IndexSources, &result.Fragments, &result.Terms, &result.IndexTextBytes, &result.DatabaseBytes)
	if err == nil && result.Schema != "phase8_3_history_v1" {
		err = errors.New("BLOCKED: temporary store is not phase8_3_history_v1")
	}
	return result, err
}

type retrievalAcceptanceTrace struct {
	Event     string           `json:"event"`
	Time      time.Time        `json:"time"`
	TurnID    string           `json:"turn_id"`
	EventID   string           `json:"event_id"`
	ElapsedMS int64            `json:"elapsed_ms"`
	Fields    map[string]int64 `json:"fields"`
}

func retrievalAcceptanceReadTrace(path string) ([]retrievalAcceptanceTrace, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	var events []retrievalAcceptanceTrace
	for {
		var raw struct {
			Event     string                     `json:"event"`
			Time      time.Time                  `json:"time"`
			TurnID    string                     `json:"turn_id"`
			EventID   string                     `json:"event_id"`
			ElapsedMS int64                      `json:"elapsed_ms"`
			Fields    map[string]json.RawMessage `json:"fields"`
		}
		if err := decoder.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				return events, nil
			}
			return events, errors.New("partial or invalid temporary trace")
		}
		switch raw.Event {
		case "history_prepared", "history_retrieved", "history_maintenance_completed", "history_maintenance_failed", "history_index_rebuilt", "history_written",
			"model_request_started", "model_response_received", "context_built", "context_updated", "context_update_failed", "turn_completed", "turn_failed", "compaction_started":
		default:
			continue
		}
		event := retrievalAcceptanceTrace{Event: raw.Event, Time: raw.Time, TurnID: raw.TurnID, EventID: raw.EventID, ElapsedMS: raw.ElapsedMS, Fields: map[string]int64{}}
		for _, key := range []string{"elapsed_ms", "summary_read_ms", "history_read_ms", "search_scanned", "search_bytes_read", "candidate_count", "snapshot_watermark", "snapshot_summary_revision",
			"history_write_duration_ms", "history_sequence", "history_bytes",
			"index_elapsed_ms", "maintenance_elapsed_ms", "maintenance_wait_ms", "wait_ms", "processed", "bytes", "max_input_tokens", "max_output_tokens", "estimated_tokens", "history_estimated_tokens", "retrieved_history_estimated_tokens", "retrieved_history_retained_snippets", "request_total_estimated_tokens", "request_tools_estimated_tokens", "request_messages_estimated_tokens"} {
			var value *int64
			if data, ok := raw.Fields[key]; ok && json.Unmarshal(data, &value) == nil && value != nil && *value >= 0 {
				event.Fields[key] = *value
			}
		}
		events = append(events, event)
	}
}

func retrievalAcceptanceExport(t *testing.T, reportDir, phase, work, memRoot string, process *historyAcceptanceProcess, capture *historyAcceptanceHTTP, evidence map[string]any) {
	t.Helper()
	pid := 0
	if process.cmd != nil && process.cmd.Process != nil {
		pid = process.cmd.Process.Pid
	}
	evidence["server_pid"] = pid
	after, err := retrievalAcceptanceMeasure(memRoot)
	if err != nil {
		evidence["measurement_error"] = "temporary read-only measurement failed"
		t.Error("cannot measure temporary history/index after process stop")
	} else {
		evidence["after_stop"] = after
	}
	events, err := retrievalAcceptanceReadTrace(filepath.Join(work, "runtime", ".local", "traces.jsonl"))
	if err != nil {
		evidence["trace_error"] = "temporary trace absent or partial"
	}
	evidence["safe_trace"] = events
	query, writeTiming, rebuildTiming, queueTiming := false, false, false, false
	for _, event := range events {
		if event.Event == "history_retrieved" {
			_, query = event.Fields["elapsed_ms"]
		}
		if _, ok := event.Fields["history_write_duration_ms"]; ok {
			writeTiming = true
		}
		if event.Event == "compaction_started" {
			t.Error("unexpected LLM summary attempt in retrieval-only fixture")
		}
	}
	serverMetrics, _ := evidence["safe_server_log"].([]retrievalAcceptanceServerMetric)
	for _, metric := range serverMetrics {
		if _, ok := metric.Fields["rebuild_elapsed_ms"]; ok {
			rebuildTiming = true
			if metric.Fields["valid"] != 1 || metric.Fields["succeeded"] != 1 {
				t.Error("owned server maintenance did not complete validly")
			}
		}
		if _, ok := metric.Fields["queue_wait_ms"]; ok {
			queueTiming = true
		}
	}
	evidence["performance_metric_scope"] = map[string]string{
		"history_write_duration_ms": "terminal database write including index maintenance",
		"rebuild_elapsed_ms":        "index rebuild phase including database open, queries and commit",
		"maintenance_elapsed_ms":    "rebuild-only maintenance elapsed time with retention_days=0",
		"queue_wait_ms":             "gateway maintenance scheduling to task Run",
	}
	if !t.Failed() && !query {
		t.Error("BLOCKED: production history_retrieved timing evidence is absent")
	}
	missing := []string{}
	if !query {
		missing = append(missing, "query_elapsed_ms")
	}
	if !writeTiming {
		missing = append(missing, "history_write_duration_ms")
	}
	if !rebuildTiming {
		missing = append(missing, "rebuild_elapsed_ms")
	}
	if !queueTiming {
		missing = append(missing, "queue_wait_ms")
	}
	if dropped, _ := evidence["server_log_dropped_lines"].(int); dropped > 0 {
		missing = append(missing, "server_log_capture_incomplete")
	}
	evidence["performance_evidence_missing"] = missing
	if len(missing) > 0 {
		t.Logf("performance evidence requires production instrumentation: %v", missing)
	}
	evidence["status"] = "passed"
	if t.Failed() {
		evidence["status"] = "failed"
	}
	if reportDir == "" {
		return
	}
	dir := filepath.Join(reportDir, fmt.Sprintf("%s-%d", phase, pid))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Error("cannot create phase evidence directory")
		return
	}
	historyAcceptanceWriteJSON(t, filepath.Join(dir, "ownedserver.log"), serverMetrics)
	if capture != nil {
		capture.mu.Lock()
		defer capture.mu.Unlock()
		evidence["transport_errors"] = append([]string(nil), capture.errs...)
		for index, call := range capture.calls {
			if _, err := historyAcceptanceDecodeRequest(call.Body); err != nil {
				t.Error("unsafe captured request was not exported")
				continue
			}
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("http-%02d-decision.json", index+1)), call.Body, 0600); err != nil {
				t.Error("cannot export controlled decision request")
			}
		}
	}
	historyAcceptanceWriteJSON(t, filepath.Join(dir, "evidence.json"), evidence)
}
