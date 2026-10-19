package hosted

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"gameagent/runtime/internal/accounts"
)

func (s *Server) ModelHandler() http.Handler { return http.HandlerFunc(s.serveModel) }
func (s *Server) serveModel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !(r.Method == "POST" && r.URL.Path == "/v1/chat/completions") && !(r.Method == "GET" && r.URL.Path == "/v1/preferences") {
		problem(w, 404, "not_found", "接口不存在")
		return
	}
	a, err := s.Workers.Authenticate(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if err != nil {
		problem(w, 401, "worker_unauthenticated", "内部凭证无效")
		return
	}
	if r.URL.Path == "/v1/preferences" {
		p, err := s.Accounts.ReadPreferences(r.Context(), a.UserID)
		if err != nil {
			s.storageError(w)
			return
		}
		writeJSON(w, 200, p)
		return
	}
	s.mu.Lock()
	c := s.connections[a.UserID]
	s.mu.Unlock()
	if c == nil || s.Accounts.KeyActive(r.Context(), c.key) != nil {
		problem(w, 403, "connection_inactive", "游戏连接已结束")
		return
	}
	select {
	case s.modelSlots <- struct{}{}:
		defer func() { <-s.modelSlots }()
	default:
		problem(w, 429, "service_busy", "服务繁忙，请稍后再试")
		return
	}
	s.mu.Lock()
	if s.modelActive[a.UserID] {
		s.mu.Unlock()
		problem(w, 429, "model_busy", "当前账号已有模型请求")
		return
	}
	s.modelActive[a.UserID] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.modelActive, a.UserID); delete(s.modelCancels, a.UserID); s.mu.Unlock() }()
	data, err := io.ReadAll(io.LimitReader(r.Body, int64(s.Config.ContextTokens)+1))
	if err != nil || len(data) > s.Config.ContextTokens-s.Config.OutputTokens-1024 {
		problem(w, 400, "model_input_limit", "请求超过平台输入预算")
		return
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(data, &doc) != nil {
		problem(w, 400, "invalid_json", "请求格式不正确")
		return
	}
	var modelName string
	var streaming bool
	var maxTokens int
	_ = json.Unmarshal(doc["model"], &modelName)
	_ = json.Unmarshal(doc["stream"], &streaming)
	_ = json.Unmarshal(doc["max_tokens"], &maxTokens)
	if modelName != s.Config.Model || streaming || maxTokens < 1 || maxTokens > s.Config.OutputTokens || len(doc["messages"]) == 0 {
		problem(w, 400, "model_policy_invalid", "请求不符合平台模型配置")
		return
	}
	requestID := r.Header.Get("X-WIA-Request-ID")
	if requestID == "" {
		requestID = accounts.NewID("call_")
	}
	if len(requestID) > 80 || !safeName.MatchString(requestID) {
		problem(w, 400, "request_id_invalid", "请求标识不正确")
		return
	}
	if err = s.Accounts.Reserve(r.Context(), a.UserID, requestID, int64(s.Config.ContextTokens), s.Config.DailyTokens, s.Config.GlobalTokenLimit(), s.Config.Model); err != nil {
		if errors.Is(err, accounts.ErrLimit) || errors.Is(err, accounts.ErrConflict) {
			problem(w, 429, "quota_exceeded", "账号或全站今日额度不足，或请求已处理")
		} else if errors.Is(err, accounts.ErrCredentials) {
			problem(w, 403, "connection_inactive", "账号状态已变化")
		} else {
			s.storageError(w)
		}
		return
	}
	settled := false
	outcome, code := "uncertain", "request_interrupted"
	defer func() {
		if !settled {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if s.Accounts.MarkCallOutcome(ctx, a.UserID, requestID, outcome, code) != nil {
				log.Print("model outcome recording failed; reservation retained")
			}
		}
	}()
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(s.Config.ModelTimeoutSeconds)*time.Second)
	defer cancel()
	s.mu.Lock()
	if s.connections[a.UserID] != c {
		s.mu.Unlock()
		problem(w, 403, "connection_inactive", "游戏连接已结束")
		return
	}
	s.modelCancels[a.UserID] = cancel
	s.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(s.Config.UpstreamURL, "/")+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		problem(w, 503, "upstream_unavailable", "模型服务暂不可用")
		return
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv(s.Config.ModelKeyEnv))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", requestID)
	response, err := s.client.Do(req)
	if err != nil {
		problem(w, 503, "upstream_uncertain", "模型请求未确认，预留额度保留")
		return
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(body) > 4<<20 || !json.Valid(body) {
		problem(w, 503, "upstream_uncertain", "模型响应未确认，预留额度保留")
		return
	}
	if response.StatusCode != 200 {
		outcome, code = "failed", "upstream_failed"
		problem(w, 503, "upstream_failed", "模型服务暂不可用，预留额度保留")
		return
	}
	var usage struct {
		Usage *struct {
			Prompt     *int64 `json:"prompt_tokens"`
			Completion *int64 `json:"completion_tokens"`
			Total      int64  `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &usage) == nil && usage.Usage != nil && usage.Usage.Total > 0 {
		settleCtx, settleCancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = s.Accounts.Settle(settleCtx, a.UserID, requestID, usage.Usage.Total, usage.Usage.Prompt, usage.Usage.Completion)
		settleCancel()
		if errors.Is(err, accounts.ErrInput) {
			code = "usage_missing"
		} else if err != nil {
			code = "settlement_unavailable"
			problem(w, 503, "settlement_unavailable", "额度结算失败，预留额度保留")
			return
		} else {
			settled = true
		}
	} else {
		code = "usage_missing"
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-WIA-Request-ID", requestID)
	w.WriteHeader(200)
	_, _ = w.Write(body)
}
