package hosted

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
	case r.URL.Path == "/api/admin/overview" && r.Method == "GET":
		var overview accounts.Overview
		overview, err = s.Accounts.Overview(r.Context(), s.Config.DailyTokens, s.Config.GlobalTokenLimit())
		s.mu.Lock()
		overview.Connected = len(s.connections)
		s.mu.Unlock()
		data = overview
	case (r.URL.Path == "/api/admin/usage" || r.URL.Path == "/api/admin/usage/export") && r.Method == "GET":
		f, e := usageFilter(r, page)
		if e != nil {
			problem(w, 400, "filter_invalid", "日期范围最多 93 天，请检查日期、用户和状态")
			return true
		}
		if r.URL.Path == "/api/admin/usage/export" {
			s.exportUsage(w, r, f)
			return true
		}
		data, err = s.Accounts.UsageReport(r.Context(), f)
	case r.URL.Path == "/api/admin/invitations" && r.Method == "GET":
		data, err = s.Accounts.Invitations(r.Context(), page)
	case r.URL.Path == "/api/admin/invitations" && r.Method == "POST":
		var b struct {
			Hours int    `json:"hours"`
			Note  string `json:"note"`
		}
		if !decode(w, r, &b) {
			return true
		}
		if b.Hours == 0 {
			b.Hours = 72
		}
		if b.Hours < 1 || b.Hours > 720 {
			problem(w, 400, "invitation_invalid", "邀请有效期为 1–720 小时")
			return true
		}
		var invite accounts.Invitation
		var secret string
		invite, secret, err = s.Accounts.NewInvitation(r.Context(), actor, time.Duration(b.Hours)*time.Hour, b.Note)
		data = map[string]any{"invitation": invite, "secret": secret}
	case strings.HasPrefix(r.URL.Path, "/api/admin/invitations/") && r.Method == "DELETE":
		err = s.Accounts.RevokeInvitation(r.Context(), actor, strings.TrimPrefix(r.URL.Path, "/api/admin/invitations/"))
		data = map[string]bool{"ok": true}
	case r.URL.Path == "/api/admin/users" && r.Method == "GET":
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		if utf8.RuneCountInString(query) > 120 {
			problem(w, 400, "query_invalid", "搜索内容最多 120 个字符")
			return true
		}
		data, err = s.Accounts.Users(r.Context(), page, query, s.Config.DailyTokens)
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
		case len(parts) == 2 && parts[1] == "limit" && r.Method == "PUT":
			var b struct {
				Limit  json.RawMessage `json:"daily_limit"`
				Reason string          `json:"reason"`
			}
			if !decode(w, r, &b) {
				return true
			}
			if len(b.Limit) == 0 {
				problem(w, 400, "limit_invalid", "请选择继承默认额度或指定个人额度")
				return true
			}
			var limit *int64
			if json.Unmarshal(b.Limit, &limit) != nil {
				problem(w, 400, "limit_invalid", "额度必须是整数或默认值")
				return true
			}
			err = s.Accounts.SetDailyLimit(r.Context(), actor, user, limit, b.Reason)
			data = map[string]bool{"ok": true}
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

func usageFilter(r *http.Request, page int) (accounts.UsageFilter, error) {
	q := r.URL.Query()
	today := time.Now().UTC().Format("2006-01-02")
	f := accounts.UsageFilter{From: q.Get("from"), To: q.Get("to"), User: q.Get("user"), Status: q.Get("status"), Page: page}
	if f.From == "" {
		f.From = today
	}
	if f.To == "" {
		f.To = today
	}
	from, e := time.Parse("2006-01-02", f.From)
	if e != nil {
		return f, e
	}
	to, e := time.Parse("2006-01-02", f.To)
	if e != nil || to.Before(from) || to.Sub(from) > 92*24*time.Hour {
		return f, accounts.ErrInput
	}
	if f.User != "" && !safeID.MatchString(f.User) {
		return f, accounts.ErrInput
	}
	if f.Status != "" && f.Status != "pending" && f.Status != "settled" && f.Status != "uncertain" && f.Status != "failed" {
		return f, accounts.ErrInput
	}
	return f, nil
}
func (s *Server) exportUsage(w http.ResponseWriter, r *http.Request, f accounts.UsageFilter) {
	calls, err := s.Accounts.CallsForExport(r.Context(), f)
	if err != nil {
		s.storageError(w)
		return
	}
	if len(calls) > 5000 {
		problem(w, 413, "export_limit", "结果超过 5000 条，请缩小日期范围或选择账号")
		return
	}
	var buffer bytes.Buffer
	buffer.WriteString("\xef\xbb\xbf")
	writer := csv.NewWriter(&buffer)
	_ = writer.Write([]string{"请求ID", "用户ID", "邮箱", "日期UTC", "时间UTC", "模型", "状态", "预留tokens", "已结算tokens", "输入tokens", "输出tokens", "原因"})
	number := func(v *int64) string {
		if v == nil {
			return ""
		}
		return strconv.FormatInt(*v, 10)
	}
	for _, call := range calls {
		created := ""
		if call.Created > 0 {
			created = time.Unix(call.Created, 0).UTC().Format(time.RFC3339)
		}
		row := []string{call.ID, call.User, call.Email, call.Period, created, call.Model, call.Status, strconv.FormatInt(call.Reserved, 10), number(call.Charged), number(call.Prompt), number(call.Completion), call.ErrorCode}
		for i, value := range row {
			if strings.ContainsAny(firstRune(strings.TrimSpace(value)), "=+-@") || strings.ContainsAny(firstRune(value), "\t\r\n") {
				row[i] = "'" + value
			}
		}
		_ = writer.Write(row)
	}
	writer.Flush()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="wia-usage.csv"`)
	w.WriteHeader(200)
	_, _ = w.Write(buffer.Bytes())
}
func firstRune(v string) string {
	for _, r := range v {
		return string(r)
	}
	return ""
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
