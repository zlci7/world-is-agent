package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"gameagent/runtime/config"
	"gameagent/runtime/internal/bootstrap"
)

type gameOption struct {
	config.Game
	AssetsReady bool   `json:"assets_ready"`
	AssetsError string `json:"assets_error,omitempty"`
}

func (s *Server) handleSetupGames(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "method_not_allowed", "games are read with GET")
		return
	}
	games, err := config.Games()
	if err != nil {
		writeSetupError(w, err, "game_setup_failed")
		return
	}
	result := make([]gameOption, 0, len(games))
	for _, game := range games {
		item := gameOption{Game: game, AssetsReady: true}
		if err := config.AssetsStatus(s.options.Runtime.Layout().ConfigDir(), game.ID); err != nil {
			item.AssetsReady = false
			item.AssetsError = err.Error()
		}
		result = append(result, item)
	}
	writeJSON(w, 200, struct {
		Games []gameOption `json:"games"`
	}{result})
}

func (s *Server) handleSetupGame(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "method_not_allowed", "game selection is written with POST")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.url {
		writeError(w, 403, "origin_not_allowed", "unexpected origin")
		return
	}
	snapshot := s.options.Runtime.Snapshot()
	if snapshot.State == bootstrap.StateBlocked {
		writeError(w, 400, "setup_blocked", snapshot.Reason)
		return
	}
	var body *struct {
		GameID string `json:"game_id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSetupBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || body == nil {
		writeError(w, 400, "invalid_request", "expected an object with game_id")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, 400, "invalid_request", "expected one JSON object")
		return
	}
	if err := s.options.Runtime.SelectGame(body.GameID); err != nil {
		writeSetupError(w, err, "game_setup_failed")
		return
	}
	writeJSON(w, 200, s.statusPayload())
}

func writeSetupError(w http.ResponseWriter, err error, fallback string) {
	code, status := fallback, http.StatusInternalServerError
	var setup *config.Error
	if errors.As(err, &setup) {
		code = setup.Code
	}
	if code == config.CodeStorageUnavailable {
		code = fallback
	}
	switch code {
	case "migration_conflict", "profile_invalid":
		status = http.StatusConflict
	case "invalid_game", "invalid_request", "game_not_selected", "game_selection_invalid", "profile_assets_missing", "setup_blocked", "already_configured", "override_configuration_invalid":
		status = http.StatusBadRequest
	}
	writeError(w, status, code, err.Error())
}
