package storyapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"gameagent/runtime/internal/model"
	"gameagent/runtime/internal/storyapp"
)

type apiGenerator struct{}

func (apiGenerator) GenerateText(ctx context.Context, request model.TextRequest) (model.TextResponse, error) {
	if err := ctx.Err(); err != nil {
		return model.TextResponse{}, err
	}
	if strings.Contains(request.System, "结构化回合意图") {
		return model.TextResponse{Text: `{"intent_type":"speak","addressee_id":"npc:innkeeper","visibility":"private"}`}, nil
	}
	if strings.Contains(request.System, "场景协调 Agent") {
		var candidates []storyapp.Event
		start := strings.Index(request.Input, "待裁定行动(JSON)：")
		end := strings.Index(request.Input, "\n所有可用重要人物：")
		if start < 0 || end < start || json.Unmarshal([]byte(request.Input[start+len("待裁定行动(JSON)："):end]), &candidates) != nil {
			return model.TextResponse{Text: `{}`}, nil
		}
		outcomes := make([]map[string]any, 0, len(candidates))
		for _, candidate := range candidates {
			outcomes = append(outcomes, map[string]any{"action_id": candidate.EventID, "status": "succeeded", "content": "行动已经完成。", "recipients": []string{"player", "npc:innkeeper", "npc:mercenary"}})
		}
		data, _ := json.Marshal(map[string]any{"time_minutes": 0, "scene": "旧渡口客栈", "scene_characters": []string{"npc:innkeeper", "npc:mercenary"}, "outcomes": outcomes})
		return model.TextResponse{Text: string(data)}, nil
	}
	if strings.Contains(request.System, "玩家正文 Agent") {
		return model.TextResponse{Text: `{"narrative":"雨声沿着窗棂滑下，客栈里的人都听见了这句话。"}`}, nil
	}
	return model.TextResponse{Text: `{"speech":"我听见了。","action_intent":"继续观察","silent":false,"memory":"我记住了这次交谈。"}`}, nil
}

func TestLocalSessionAndStoryRoutes(t *testing.T) {
	app, err := storyapp.Open(context.Background(), storyapp.Options{DataRoot: t.TempDir(), Generator: apiGenerator{}})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	server, err := New(Options{
		Addr:    "127.0.0.1:0",
		App:     app,
		Assets:  fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>story</html>")}},
		Version: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Shutdown()
	go func() { _ = server.Serve() }()

	plainClient := &http.Client{}
	response, body := requestJSON(t, plainClient, http.MethodGet, server.URL()+"/api/v1/status", nil)
	if response.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "unauthorized") {
		t.Fatalf("unauthorized status = %d, body = %s", response.StatusCode, body)
	}

	parsed, err := url.Parse(server.BrowserURL())
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(parsed.Fragment, "token=")
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	response, _ = requestJSON(t, client, http.MethodPost, server.URL()+"/api/session", map[string]string{"token": token})
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("session status = %d", response.StatusCode)
	}
	originRequest, err := http.NewRequest(http.MethodGet, server.URL()+"/api/v1/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	originRequest.Header.Set("Origin", "http://attacker.invalid")
	originResponse, err := client.Do(originRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = originResponse.Body.Close()
	if originResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("unexpected origin status = %d", originResponse.StatusCode)
	}

	response, body = requestJSON(t, client, http.MethodGet, server.URL()+"/", nil)
	if response.StatusCode != http.StatusOK || string(body) != "<html>story</html>" {
		t.Fatalf("asset response = %d/%q", response.StatusCode, body)
	}

	response, body = requestJSON(t, client, http.MethodGet, server.URL()+"/api/v1/status", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.StatusCode, body)
	}
	var statusEnvelope struct {
		Status storyapp.Status `json:"status"`
	}
	decodeJSONBody(t, body, &statusEnvelope)
	if !statusEnvelope.Status.Ready || statusEnvelope.Status.ActiveWorld != nil {
		t.Fatalf("initial status = %+v", statusEnvelope.Status)
	}

	response, body = requestJSON(t, client, http.MethodPost, server.URL()+"/api/v1/worlds", map[string]any{
		"name":           "HTTP 冒险",
		"mode":           "guided",
		"player_name":    "旅人",
		"player_profile": "寻找信使",
		"activate":       true,
	})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create world = %d, body = %s", response.StatusCode, body)
	}
	var worldEnvelope struct {
		World storyapp.WorldSummary `json:"world"`
	}
	decodeJSONBody(t, body, &worldEnvelope)
	world := worldEnvelope.World
	if world.WorldID == "" || world.MessageHead != 1 || world.EventHead != 1 {
		t.Fatalf("created world = %+v", world)
	}
	response, body = requestJSON(t, client, http.MethodPut, server.URL()+"/api/v1/worlds/"+url.PathEscape(world.WorldID)+"/agent-settings", map[string]any{
		"perspective":            "omniscient",
		"length":                 "standard",
		"detail":                 "balanced",
		"expected_context_epoch": world.ContextEpoch,
	})
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "invalid_request") {
		t.Fatalf("invalid agent settings = %d, body = %s", response.StatusCode, body)
	}
	response, body = requestJSON(t, client, http.MethodPut, server.URL()+"/api/v1/worlds/"+url.PathEscape(world.WorldID)+"/agent-settings", map[string]any{
		"perspective":            "third_person",
		"length":                 "concise",
		"detail":                 "restrained",
		"custom_instruction":     "对白留白。",
		"expected_context_epoch": world.ContextEpoch,
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("agent settings = %d, body = %s", response.StatusCode, body)
	}
	var settingsEnvelope struct {
		Settings storyapp.NarrativeSettings `json:"settings"`
		World    storyapp.WorldSummary      `json:"world"`
	}
	decodeJSONBody(t, body, &settingsEnvelope)
	if settingsEnvelope.Settings.Perspective != storyapp.PerspectiveThirdPerson || settingsEnvelope.World.ContextEpoch != world.ContextEpoch+1 {
		t.Fatalf("agent settings response = %+v", settingsEnvelope)
	}
	world = settingsEnvelope.World
	response, body = requestJSON(t, client, http.MethodGet, server.URL()+"/api/v1/worlds/"+url.PathEscape(world.WorldID)+"/runs", nil)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"runs":[]`) {
		t.Fatalf("empty run list = %d, body = %s", response.StatusCode, body)
	}

	response, body = requestJSON(t, client, http.MethodPost, server.URL()+"/api/v1/worlds/"+url.PathEscape(world.WorldID)+"/runs", map[string]any{
		"request_key":              "http-run-1",
		"input":                    "我私下对老板说：有人来过。",
		"expected_active_revision": 1,
		"expected_message_head":    1,
		"expected_event_head":      1,
		"expected_context_epoch":   world.ContextEpoch,
	})
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("submit run = %d, body = %s", response.StatusCode, body)
	}
	var runEnvelope struct {
		Run storyapp.Run `json:"run"`
	}
	decodeJSONBody(t, body, &runEnvelope)
	run := runEnvelope.Run
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, body = requestJSON(t, client, http.MethodGet, server.URL()+"/api/v1/worlds/"+url.PathEscape(world.WorldID)+"/runs/"+url.PathEscape(run.RunID), nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("read run = %d, body = %s", response.StatusCode, body)
		}
		decodeJSONBody(t, body, &runEnvelope)
		run = runEnvelope.Run
		if run.Status != "accepted" && run.Status != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run.Status != "completed" {
		t.Fatalf("run = %+v", run)
	}

	response, body = requestJSON(t, client, http.MethodGet, server.URL()+"/api/v1/worlds/"+url.PathEscape(world.WorldID)+"/runs", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list runs = %d, body = %s", response.StatusCode, body)
	}
	var runsEnvelope struct {
		Runs []storyapp.Run `json:"runs"`
	}
	decodeJSONBody(t, body, &runsEnvelope)
	if len(runsEnvelope.Runs) != 1 || runsEnvelope.Runs[0].RunID != run.RunID {
		t.Fatalf("runs = %+v", runsEnvelope.Runs)
	}

	response, body = requestJSON(t, client, http.MethodGet, server.URL()+"/api/v1/worlds/"+url.PathEscape(world.WorldID), nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("read world = %d, body = %s", response.StatusCode, body)
	}
	var readEnvelope struct {
		Messages          []storyapp.Message         `json:"messages"`
		Bystanders        []string                   `json:"bystanders"`
		Characters        []storyapp.PublicCharacter `json:"characters"`
		NarrativeSettings storyapp.NarrativeSettings `json:"narrative_settings"`
	}
	decodeJSONBody(t, body, &readEnvelope)
	if len(readEnvelope.Messages) != 3 || readEnvelope.Messages[1].Kind != "player" || readEnvelope.Messages[2].Kind != "narrative" {
		t.Fatalf("messages = %+v", readEnvelope.Messages)
	}
	if len(readEnvelope.Bystanders) != 10 || len(readEnvelope.Characters) != 2 || strings.Contains(string(body), "染血的信蜡") || strings.Contains(string(body), "知道失踪信使曾在今晚来过") {
		t.Fatalf("player projection leaked or is incomplete: %s", body)
	}
	if readEnvelope.NarrativeSettings != settingsEnvelope.Settings {
		t.Fatalf("narrative settings = %+v, want %+v", readEnvelope.NarrativeSettings, settingsEnvelope.Settings)
	}

	response, body = requestJSON(t, client, http.MethodPost, server.URL()+"/api/v1/worlds/"+url.PathEscape(world.WorldID)+"/save-as", map[string]any{
		"name":                     "HTTP 分支",
		"request_key":              "http-copy-1",
		"expected_active_revision": 1,
	})
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("save as = %d, body = %s", response.StatusCode, body)
	}
	var operationEnvelope struct {
		Operation storyapp.SaveOperation `json:"operation"`
	}
	decodeJSONBody(t, body, &operationEnvelope)
	operation := operationEnvelope.Operation
	for time.Now().Before(deadline) {
		response, body = requestJSON(t, client, http.MethodGet, server.URL()+"/api/v1/world-copy-operations/"+url.PathEscape(operation.OperationID), nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("read copy = %d, body = %s", response.StatusCode, body)
		}
		decodeJSONBody(t, body, &operationEnvelope)
		operation = operationEnvelope.Operation
		if operation.Status != "copying" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if operation.Status != "ready" {
		t.Fatalf("copy operation = %+v", operation)
	}
}

func requestJSON(t *testing.T, client *http.Client, method, endpoint string, value any) (*http.Response, []byte) {
	t.Helper()
	var body io.Reader
	if value != nil {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		t.Fatal(err)
	}
	if value != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return response, data
}

func decodeJSONBody(t *testing.T, body []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}
