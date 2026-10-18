package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gameagent/runtime/internal/accounts"
)

type WorkerStatus struct {
	State     string `json:"state"`
	Connected bool   `json:"connected"`
	ReadAt    int64  `json:"read_at"`
}
type WorkerInspector interface {
	Inspect(context.Context, string) (WorkerStatus, error)
}

func (s *Server) adminRoute(w http.ResponseWriter, r *http.Request, session accounts.Session) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/admin/") {
		return false
	}
	if !session.User.IsAdmin {
		problem(w, 403, "admin_required", "当前账号没有管理权限")
		return true
	}
	page := 0
	if raw := r.URL.Query().Get("page"); raw != "" {
		var err error
		page, err = strconv.Atoi(raw)
		if err != nil || page < 0 || page > 10000 {
			problem(w, 400, "page_invalid", "页码不正确")
			return true
		}
	}
	actor := session.User.ID
	var data any
	var err error
	switch {
	case r.URL.Path == "/api/admin/invitations" && r.Method == "GET":
		data, err = s.Accounts.Invitations(r.Context(), page)
	case r.URL.Path == "/api/admin/invitations" && r.Method == "POST":
		var invite accounts.Invitation
		var secret string
		invite, secret, err = s.Accounts.NewInvitation(r.Context(), actor, 72*time.Hour)
		data = map[string]any{"invitation": invite, "secret": secret}
	case strings.HasPrefix(r.URL.Path, "/api/admin/invitations/") && r.Method == "DELETE":
		err = s.Accounts.RevokeInvitation(r.Context(), actor, strings.TrimPrefix(r.URL.Path, "/api/admin/invitations/"))
		data = map[string]bool{"ok": true}
	case r.URL.Path == "/api/admin/users" && r.Method == "GET":
		data, err = s.Accounts.Users(r.Context(), page)
	case r.URL.Path == "/api/admin/audit" && r.Method == "GET":
		data, err = s.Accounts.Audit(r.Context(), page)
	case strings.HasPrefix(r.URL.Path, "/api/admin/users/"):
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/admin/users/"), "/")
		if !safeID.MatchString(parts[0]) {
			problem(w, 400, "user_invalid", "用户编号不正确")
			return true
		}
		user := parts[0]
		switch {
		case len(parts) == 1 && r.Method == "PATCH":
			var b struct {
				Enabled *bool `json:"enabled"`
			}
			if !decode(w, r, &b) {
				return true
			}
			if b.Enabled == nil {
				problem(w, 400, "user_invalid", "请选择账号状态")
				return true
			}
			err = s.Accounts.SetEnabled(r.Context(), actor, user, *b.Enabled)
			if err == nil && !*b.Enabled {
				s.disconnectUser(user)
			}
			data = map[string]bool{"ok": true}
		case len(parts) == 2 && parts[1] == "usage" && r.Method == "GET":
			_, _, err = s.Accounts.WorkerIdentity(r.Context(), user)
			if err == nil {
				data, err = s.Accounts.Usage(r.Context(), user, s.Config.DailyTokens)
			}
		case len(parts) == 2 && parts[1] == "runtime" && r.Method == "GET":
			inspector, ok := s.Workers.(WorkerInspector)
			if !ok {
				problem(w, 503, "status_unavailable", "运行状态暂不可用")
				return true
			}
			var state WorkerStatus
			state, err = inspector.Inspect(r.Context(), user)
			s.mu.Lock()
			state.Connected = s.connections[user] != nil
			s.mu.Unlock()
			state.ReadAt = time.Now().Unix()
			data = state
		default:
			problem(w, 404, "not_found", "接口不存在")
			return true
		}
	default:
		problem(w, 404, "not_found", "接口不存在")
		return true
	}
	if err != nil {
		switch {
		case errors.Is(err, accounts.ErrInput):
			problem(w, 400, "operation_invalid", "无法对当前账号执行该操作")
		case errors.Is(err, accounts.ErrCredentials):
			problem(w, 403, "operation_rejected", "账号状态或权限已变化，请刷新")
		case errors.Is(err, accounts.ErrConflict):
			problem(w, 409, "invitation_changed", "邀请码状态已变化，请刷新")
		default:
			s.storageError(w)
		}
		return true
	}
	writeJSON(w, 200, map[string]any{"items": data, "page": page})
	return true
}

func (d *DockerWorkers) Inspect(ctx context.Context, user string) (WorkerStatus, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !safeID.MatchString(user) {
		return WorkerStatus{}, accounts.ErrInput
	}
	generation, initialized, err := d.Accounts.WorkerIdentity(ctx, user)
	if err != nil {
		return WorkerStatus{}, err
	}
	state := WorkerStatus{State: "not_allocated"}
	if generation == "" && !initialized {
		return state, nil
	}
	state.State = "stopped"
	volumes, err := d.run(ctx, "volume", "ls", "--format", "{{.Name}}")
	if err != nil {
		return WorkerStatus{}, err
	}
	volumeExists := false
	for _, name := range strings.Fields(string(volumes)) {
		if name == "wia-"+user+"-data" {
			volumeExists = true
		}
	}
	if initialized && !volumeExists {
		state.State = "recovery_blocked"
		return state, nil
	}
	if volumeExists {
		data, e := d.run(ctx, "volume", "inspect", "wia-"+user+"-data")
		if e != nil {
			return WorkerStatus{}, e
		}
		var items []struct{ Labels map[string]string }
		if json.Unmarshal(data, &items) != nil || len(items) != 1 || items[0].Labels["wia.user"] != user || items[0].Labels["wia.deployment"] != DeploymentID(d.Accounts) {
			state.State = "recovery_blocked"
			return state, nil
		}
	}
	names, err := d.run(ctx, "ps", "-a", "--filter", "name=^/wia-"+user+"$", "--format", "{{.Names}}")
	if err != nil {
		return WorkerStatus{}, err
	}
	exists := false
	for _, name := range strings.Fields(string(names)) {
		if name == "wia-"+user {
			exists = true
		}
	}
	if !exists {
		return state, nil
	}
	data, err := d.run(ctx, "inspect", "wia-"+user)
	if err != nil {
		return WorkerStatus{}, err
	}
	var items []dockerState
	if json.Unmarshal(data, &items) != nil || len(items) != 1 || items[0].Config.Labels["wia.user"] != user || items[0].Config.Labels["wia.deployment"] != DeploymentID(d.Accounts) || items[0].Config.Labels["wia.generation"] != generation || items[0].Config.Image != d.Config.WorkerImage {
		state.State = "recovery_blocked"
		return state, nil
	}
	if items[0].State.Running {
		state.State = "running"
	}
	return state, nil
}
