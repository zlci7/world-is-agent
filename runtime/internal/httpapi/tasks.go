package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"gameagent/runtime/internal/bootstrap"
	"gameagent/runtime/internal/task"
)

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "method_not_allowed", "task queries use GET")
		return
	}
	values := r.URL.Query()
	allowed := map[string]bool{"instance_id": true, "game_id": true, "world_id": true, "collection": true, "state": true, "cursor": true, "limit": true, "entity_id": true, "task_id": true}
	for key, values := range values {
		if !allowed[key] || len(values) != 1 {
			writeError(w, 400, "invalid_task_query", "invalid task query")
			return
		}
	}
	q := task.ConsoleQuery{InstanceID: values.Get("instance_id"), GameID: values.Get("game_id"), WorldID: values.Get("world_id"), Collection: values.Get("collection"), State: values.Get("state"), Cursor: values.Get("cursor"), EntityID: values.Get("entity_id"), TaskID: values.Get("task_id")}
	if raw, found := values["limit"]; found {
		limit, err := strconv.Atoi(raw[0])
		if err != nil || limit < 1 || limit > 50 {
			writeError(w, 400, "invalid_task_query", "limit must be between 1 and 50")
			return
		}
		q.Limit = limit
	}
	var result any
	var err error
	if r.URL.Path == "/api/task-worlds" {
		result, err = s.options.Runtime.ReadTaskWorlds(r.Context(), q)
	} else {
		result, err = s.options.Runtime.ReadTaskPage(r.Context(), q, r.URL.Path == "/api/tasks/detail")
	}
	if err != nil {
		status, code := 503, "task_query_failed"
		switch {
		case errors.Is(err, task.ErrConsoleQuery):
			status, code = 400, "invalid_task_query"
		case errors.Is(err, bootstrap.ErrConsoleRuntimeChanged):
			status, code = 409, "runtime_changed"
		case errors.Is(err, task.ErrConsoleSnapshotChanged):
			status, code = 409, "task_snapshot_changed"
		case errors.Is(err, task.ErrTaskNotFound):
			status, code = 404, "task_not_found"
		case errors.Is(err, bootstrap.ErrConsoleRuntimeNotReady):
			code = "runtime_not_ready"
		case errors.Is(err, task.ErrConsoleBudget):
			code = "task_query_budget_exceeded"
		}
		writeError(w, status, code, "task snapshot could not be read")
		return
	}
	data, err := json.Marshal(result)
	if err != nil || len(data) > task.ConsoleResponseBytes {
		writeError(w, 503, "task_query_budget_exceeded", "task response exceeds the read budget")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
