package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"gameagent/runtime/internal/task"
)

func TestTaskAPIsKeepLocalAuthenticationAndValidateScope(t *testing.T) {
	f := newFixture(t, nil)
	for _, route := range []string{"/api/task-worlds", "/api/tasks", "/api/tasks/detail"} {
		if response := f.do(t, http.MethodGet, route, ""); response.Code != 401 {
			t.Fatalf("unauthenticated %s: %d", route, response.Code)
		}
	}
	cookie := f.session(t)
	if err := os.WriteFile(f.runtime.ModelConfigPath(), []byte(`{"provider":"fake"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.runtime.SelectGame("stardew-valley"); err != nil {
		t.Fatal(err)
	}
	first := f.runtime.Snapshot().InstanceID
	query := url.Values{"instance_id": {first}, "game_id": {"stardew-valley"}}
	response := f.do(t, http.MethodGet, "/api/task-worlds?"+query.Encode(), "", cookie)
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("worlds: %d %s", response.Code, response.Body.String())
	}
	worlds := decodeBody[task.ConsoleWorlds](t, response)
	if !worlds.Supported || worlds.InstanceID != first || len(worlds.Worlds) != 0 {
		t.Fatalf("worlds: %+v", worlds)
	}
	for _, extra := range []string{"&limit=51", "&limit=0", "&path=secrets/model.key", "&game_id=other"} {
		response = f.do(t, http.MethodGet, "/api/task-worlds?"+query.Encode()+extra, "", cookie)
		if response.Code != 400 {
			t.Fatalf("query %s: %d %s", extra, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodGet, f.origin+"/api/task-worlds?"+query.Encode(), nil)
	request.Host = "outside.example"
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	f.server.server.Handler.ServeHTTP(recorder, request)
	if recorder.Code != 403 {
		t.Fatalf("outside Host: %d", recorder.Code)
	}
	if response = f.do(t, http.MethodPost, "/api/task-worlds?"+query.Encode(), "", cookie); response.Code != 405 {
		t.Fatalf("mutation allowed: %d", response.Code)
	}
	if err := f.runtime.SelectGame("stardew-valley"); err != nil {
		t.Fatal(err)
	}
	if next := f.runtime.Snapshot().InstanceID; next == "" || next == first {
		t.Fatal("same-game instance replacement kept its identity")
	}
	if response = f.do(t, http.MethodGet, "/api/task-worlds?"+query.Encode(), "", cookie); response.Code != 409 {
		t.Fatalf("stale instance: %d %s", response.Code, response.Body.String())
	}
	if err := f.runtime.SelectGame("rimworld"); err != nil {
		t.Fatal(err)
	}
	query.Set("game_id", "rimworld")
	query.Set("instance_id", f.runtime.Snapshot().InstanceID)
	response = f.do(t, http.MethodGet, "/api/task-worlds?"+query.Encode(), "", cookie)
	if response.Code != 200 || decodeBody[task.ConsoleWorlds](t, response).Supported {
		t.Fatalf("disabled: %d %s", response.Code, response.Body.String())
	}
	if err := f.runtime.Close(); err != nil {
		t.Fatal(err)
	}
	response = f.do(t, http.MethodGet, "/api/task-worlds?"+query.Encode(), "", cookie)
	if response.Code != 503 {
		t.Fatalf("closed runtime: %d", response.Code)
	}
}
