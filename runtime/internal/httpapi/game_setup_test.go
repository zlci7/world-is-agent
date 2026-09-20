package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestGameSelectionStatusAndSecurity(t *testing.T) {
	f := newFixture(t, nil)
	cookie := f.session(t)
	for _, route := range []string{"/api/setup/games", "/api/setup/game"} {
		if got := f.do(t, http.MethodGet, route, ""); got.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d", route, got.Code)
		}
	}
	status := decodeBody[map[string]any](t, f.do(t, http.MethodGet, "/api/status", "", cookie))
	if status["reason_code"] != "game_not_selected" || status["loaded_game"] != nil || status["connection_count"] != float64(0) {
		t.Fatalf("fresh status: %+v", status)
	}
	response := f.do(t, http.MethodPost, "/api/setup/model", "not-json", cookie)
	if got := decodeBody[errorResponse](t, response).Error.Code; got != "game_not_selected" {
		t.Fatalf("model gate read body first: %s", got)
	}
	response = f.do(t, http.MethodPost, "/api/setup/game", `{"game_id":"rimworld"}`, cookie)
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	status = decodeBody[map[string]any](t, response)
	if status["reason_code"] != "model_configuration_required" || status["loaded_game"] != nil {
		t.Fatalf("selected: %+v", status)
	}
	for _, body := range []string{`{"game_id":"../outside"}`, `{"game_id":"missing"}`} {
		response := f.do(t, http.MethodPost, "/api/setup/game", body, cookie)
		if response.Code != 400 || decodeBody[errorResponse](t, response).Error.Code != "invalid_game" {
			t.Fatalf("invalid: %s", response.Body.String())
		}
	}
	if _, err := os.Stat(filepath.Join(f.root, "secrets", "model.key")); !os.IsNotExist(err) {
		t.Fatalf("game flow wrote model key: %v", err)
	}
}

func TestGameSelectionWithModelAppliesImmediately(t *testing.T) {
	f := newFixture(t, nil)
	cookie := f.session(t)
	if err := os.WriteFile(f.runtime.ModelConfigPath(), []byte(`{"provider":"fake"}`), 0600); err != nil {
		t.Fatal(err)
	}
	first := f.do(t, http.MethodPost, "/api/setup/game", `{"game_id":"rimworld"}`, cookie)
	if first.Code != 200 {
		t.Fatal(first.Body.String())
	}
	status := decodeBody[map[string]any](t, first)
	if status["ready"] != true || status["reason_code"] != nil {
		t.Fatalf("ready: %+v", status)
	}
	second := f.do(t, http.MethodPost, "/api/setup/game", `{"game_id":"stardew-valley"}`, cookie)
	if second.Code != 200 {
		t.Fatal(second.Body.String())
	}
	status = decodeBody[map[string]any](t, second)
	if status["restart_required"] != false || status["loaded_game"].(map[string]any)["id"] != "stardew-valley" || status["configured_game"].(map[string]any)["id"] != "stardew-valley" {
		t.Fatalf("next: %+v", status)
	}
}
