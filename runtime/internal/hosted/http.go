package hosted

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/accounts"
)

const sessionCookie = "wia_session"

type Server struct {
	protocol.UnimplementedGameAgentGatewayServer
	Config       Config
	Accounts     *accounts.Store
	Workers      Workers
	Assets       fs.FS
	mu           sync.Mutex
	connections  map[string]*connection
	modelSlots   chan struct{}
	modelActive  map[string]bool
	modelCancels map[string]context.CancelFunc
	client       *http.Client
	mailer       MailSender
}

func New(c Config, store *accounts.Store, workers Workers, assets fs.FS) *Server {
	return &Server{Config: c, Accounts: store, Workers: workers, Assets: assets, connections: map[string]*connection{}, modelSlots: make(chan struct{}, c.GlobalModelConcurrency), modelActive: map[string]bool{}, modelCancels: map[string]context.CancelFunc{}, mailer: &SMTPMail{Config: c.Mail}, client: &http.Client{Timeout: time.Duration(c.ModelTimeoutSeconds) * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func subtleToken(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func problem(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		problem(w, 415, "invalid_json", "请使用 JSON 请求")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		problem(w, 400, "invalid_json", "请求格式不正确")
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		problem(w, 400, "invalid_json", "请求格式不正确")
		return false
	}
	return true
}

func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }
func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	u, _ := url.Parse(s.Config.Origin)
	if r.Host != u.Host {
		problem(w, 400, "invalid_host", "服务地址不正确")
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("Origin") != strings.TrimSuffix(s.Config.Origin, "/") {
		problem(w, 403, "invalid_origin", "请从本站操作")
		return
	}
	if r.URL.Path == "/healthz" && r.Method == "GET" {
		writeJSON(w, 200, map[string]string{"status": "ok"})
		return
	}
	if r.URL.Path == "/api/invitations/redeem" && r.Method == "POST" {
		s.register(w, r)
		return
	}
	if r.URL.Path == "/api/session" && r.Method == "POST" {
		s.login(w, r)
		return
	}
	if s.publicMailRoute(w, r) {
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		s.asset(w, r)
		return
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		problem(w, 401, "unauthenticated", "请先登录")
		return
	}
	session, err := s.Accounts.Session(r.Context(), cookie.Value)
	if err != nil {
		problem(w, 401, "unauthenticated", "登录已过期，请重新登录")
		return
	}
	if r.Method != "GET" && !subtleToken(r.Header.Get("X-CSRF-Token"), session.CSRF) {
		problem(w, 403, "csrf_invalid", "请刷新页面后重试")
		return
	}
	if r.URL.Path == "/api/session" && r.Method == "DELETE" {
		if s.Accounts.Logout(r.Context(), cookie.Value) != nil {
			problem(w, 503, "storage_unavailable", "退出失败，请重试")
			return
		}
		s.cookie(w, "", -1)
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	user := session.User.ID
	if r.URL.Path == "/api/me" && r.Method == "GET" {
		writeJSON(w, 200, session)
		return
	}
	if s.verificationRoute(w, r, session, cookie.Value) {
		return
	}
	if session.User.VerifiedAt == 0 {
		problem(w, 403, "email_verification_required", "请先验证邮箱")
		return
	}
	if s.adminRoute(w, r, session) {
		return
	}
	switch {
	case r.URL.Path == "/api/me" && r.Method == "GET":
		writeJSON(w, 200, session)
	case r.URL.Path == "/api/me/connection" && r.Method == "GET":
		writeJSON(w, 200, map[string]string{"gateway_url": s.Config.GatewayURL, "game_id": s.Config.GameID, "min_adapter_version": s.Config.MinAdapterVersion})
	case r.URL.Path == "/api/me/keys" && r.Method == "GET":
		keys, err := s.Accounts.Keys(r.Context(), user)
		if err != nil {
			s.storageError(w)
			return
		}
		writeJSON(w, 200, map[string]any{"keys": keys})
	case r.URL.Path == "/api/me/keys" && r.Method == "POST":
		key, secret, err := s.Accounts.CreateKey(r.Context(), user, time.Duration(s.Config.KeyDays)*24*time.Hour)
		if errors.Is(err, accounts.ErrLimit) {
			problem(w, 409, "key_limit", "最多保留五个有效密钥，请先撤销旧密钥")
			return
		}
		if err != nil {
			s.storageError(w)
			return
		}
		writeJSON(w, 201, map[string]any{"key": key, "secret": secret})
	case strings.HasPrefix(r.URL.Path, "/api/me/keys/") && r.Method == "DELETE":
		id := strings.TrimPrefix(r.URL.Path, "/api/me/keys/")
		if err := s.Accounts.RevokeKey(r.Context(), user, id); err != nil {
			s.storageError(w)
			return
		}
		s.disconnectKey(user, id)
		writeJSON(w, 200, map[string]bool{"ok": true})
	case r.URL.Path == "/api/me/preferences" && r.Method == "GET":
		p, err := s.Accounts.ReadPreferences(r.Context(), user)
		if err != nil {
			s.storageError(w)
			return
		}
		writeJSON(w, 200, p)
	case r.URL.Path == "/api/me/preferences" && r.Method == "PUT":
		var p accounts.Preferences
		if !decode(w, r, &p) {
			return
		}
		p, err = s.Accounts.SavePreferences(r.Context(), user, p)
		if errors.Is(err, accounts.ErrConflict) {
			problem(w, 409, "preferences_changed", "偏好已更新，请刷新后重试")
			return
		}
		if errors.Is(err, accounts.ErrInput) {
			problem(w, 400, "preferences_invalid", "请检查称呼、语言与回复长度")
			return
		}
		if err != nil {
			s.storageError(w)
			return
		}
		writeJSON(w, 200, p)
	case r.URL.Path == "/api/me/usage" && r.Method == "GET":
		usage, err := s.Accounts.Usage(r.Context(), user, s.Config.DailyTokens)
		if err != nil {
			s.storageError(w)
			return
		}
		writeJSON(w, 200, usage)
	case r.Method == "GET" && (r.URL.Path == "/api/me/status" || r.URL.Path == "/api/me/task-worlds" || r.URL.Path == "/api/me/tasks" || r.URL.Path == "/api/me/tasks/detail" || r.URL.Path == "/api/me/turns"):
		s.workerRead(w, r, user)
	default:
		problem(w, 404, "not_found", "接口不存在")
	}
}

func (s *Server) authBudget(w http.ResponseWriter, r *http.Request, email string) bool {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	for _, item := range []struct {
		subject string
		limit   int
	}{{"ip:" + ip, 30}, {"email:" + strings.ToLower(strings.TrimSpace(email)), 6}} {
		if err := s.Accounts.RateLimit(r.Context(), item.subject, item.limit); err != nil {
			w.Header().Set("Retry-After", "60")
			problem(w, 429, "auth_rate_limited", "尝试较多，请稍后再试")
			return false
		}
	}
	return true
}
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var b struct {
		accounts.Proof
		Email      string `json:"email"`
		Password   string `json:"password"`
		Invitation string `json:"invitation"`
	}
	if !decode(w, r, &b) || !s.authBudget(w, r, b.Email) {
		return
	}
	u, err := s.Accounts.Register(r.Context(), b.Email, b.Password, b.Invitation, b.Proof)
	if err != nil {
		if errors.Is(err, accounts.ErrVerification) {
			problem(w, 400, "verification_invalid", "验证码错误、已失效或尝试次数已用完，请重新获取")
		} else if errors.Is(err, accounts.ErrInput) {
			problem(w, 400, "account_invalid", "请填写有效邮箱和 12–128 个字符的密码")
		} else if errors.Is(err, accounts.ErrCredentials) || errors.Is(err, accounts.ErrConflict) {
			problem(w, 400, "registration_rejected", "邀请码不可用或邮箱已注册")
		} else {
			s.storageError(w)
		}
		return
	}
	s.issueSession(w, r, u, 201)
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(w, r, &b) || !s.authBudget(w, r, b.Email) {
		return
	}
	u, err := s.Accounts.Login(r.Context(), b.Email, b.Password)
	if err != nil {
		if errors.Is(err, accounts.ErrCredentials) {
			problem(w, 401, "login_rejected", "邮箱或密码不正确")
		} else {
			s.storageError(w)
		}
		return
	}
	s.issueSession(w, r, u, 200)
}
func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, u accounts.User, status int) {
	secret, err := s.Accounts.NewSession(r.Context(), u.ID, time.Duration(s.Config.SessionHours)*time.Hour)
	if err != nil {
		s.storageError(w)
		return
	}
	s.cookie(w, secret, s.Config.SessionHours*3600)
	writeJSON(w, status, accounts.Session{User: u, CSRF: s.Accounts.Digest("csrf:" + secret)})
}
func (s *Server) cookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: value, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
}
func (s *Server) storageError(w http.ResponseWriter) {
	problem(w, 503, "storage_unavailable", "服务暂时不可用，请稍后重试")
}

func (s *Server) workerRead(w http.ResponseWriter, r *http.Request, user string) {
	a, err := s.Workers.Ensure(r.Context(), user)
	if err != nil {
		if workerRecoveryBlocked(err) {
			problem(w, 503, "worker_recovery_blocked", "账号数据暂时无法恢复，请联系管理员")
			return
		}
		problem(w, 503, "worker_unavailable", "运行实例暂不可用，请稍后重试")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), "GET", a.API+strings.TrimPrefix(r.URL.Path, "/api/me")+"?"+r.URL.RawQuery, nil)
	if err != nil {
		s.storageError(w)
		return
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	client := &http.Client{Timeout: 8 * time.Second}
	response, err := client.Do(req)
	if err != nil {
		problem(w, 503, "worker_unavailable", "运行实例暂不可用，请稍后重试")
		return
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (512<<10)+1))
	if err != nil || len(data) > 512<<10 || !json.Valid(data) {
		s.storageError(w)
		return
	}
	w.Header().Set("X-WIA-Generation", a.Generation)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(data)
}

func (s *Server) asset(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		problem(w, 405, "method_not_allowed", "请求方式不正确")
		return
	}
	if s.Assets == nil {
		http.Error(w, "Web assets have not been built", 503)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "hosted.html"
	}
	if name == "index.html" {
		name = "hosted.html"
	}
	if _, err := fs.Stat(s.Assets, name); err != nil {
		http.NotFound(w, r)
		return
	}
	r2 := r.Clone(r.Context())
	u := *r.URL
	r2.URL = &u
	r2.URL.Path = "/" + name
	http.FileServer(http.FS(s.Assets)).ServeHTTP(w, r2)
}
