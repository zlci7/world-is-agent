package storyapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"gameagent/runtime/internal/storyapp"
)

type Options struct {
	Addr    string
	App     *storyapp.App
	Assets  fs.FS
	Version string
	Logger  *log.Logger
}

type Server struct {
	app      *storyapp.App
	assets   fs.FS
	listener net.Listener
	http     *http.Server
	url      string
	version  string
	sessions *sessions
	hosts    map[string]struct{}
	hasIndex bool
}

func New(options Options) (*Server, error) {
	if options.App == nil {
		return nil, errors.New("storyapi: app is required")
	}
	if options.Logger == nil {
		options.Logger = log.Default()
	}
	if err := requireLoopback(options.Addr); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", options.Addr)
	if err != nil {
		return nil, err
	}
	sessionStore, err := newSessions()
	if err != nil {
		listener.Close()
		return nil, err
	}
	s := &Server{app: options.App, assets: options.Assets, listener: listener, url: "http://" + listener.Addr().String(), version: options.Version, sessions: sessionStore, hosts: allowedHosts(listener.Addr()), hasIndex: assetExists(options.Assets, "index.html")}
	s.http = &http.Server{Handler: http.HandlerFunc(s.handle), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, ErrorLog: options.Logger}
	return s, nil
}
func (s *Server) URL() string        { return s.url }
func (s *Server) BrowserURL() string { return s.url + "/#token=" + s.sessions.bootstrap }
func (s *Server) Serve() error {
	err := s.http.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func (s *Server) Shutdown() error { return s.http.Close() }

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.hosts[strings.ToLower(strings.TrimSpace(r.Host))]; !ok {
		writeError(w, http.StatusForbidden, "host_not_allowed", "local browser access is required")
		return
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "http" {
			writeError(w, http.StatusForbidden, "origin_not_allowed", "the browser origin is not allowed")
			return
		}
		if _, ok := s.hosts[strings.ToLower(parsed.Host)]; !ok {
			writeError(w, http.StatusForbidden, "origin_not_allowed", "the browser origin is not allowed")
			return
		}
	}
	if r.URL.Path == "/api/session" {
		s.handleSession(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if !s.sessions.valid(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "open the URL printed by the Runtime to connect this browser")
			return
		}
		s.handleAPI(w, r)
		return
	}
	s.handleAssets(w, r)
}

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/v1/status":
		s.status(w, r)
	case "/api/v1/games":
		s.games(w, r)
	case "/api/v1/model-profiles":
		s.modelProfiles(w, r)
	case "/api/v1/worlds":
		s.worlds(w, r)
	case "/api/v1/active-world":
		s.activeWorld(w, r)
	default:
		if strings.HasPrefix(r.URL.Path, "/api/v1/world-copy-operations/") {
			s.copyOperation(w, r, strings.TrimPrefix(r.URL.Path, "/api/v1/world-copy-operations/"))
			return
		}
		s.handleWorldRoute(w, r)
	}
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "method_not_allowed", "status is read with GET")
		return
	}
	status, err := s.app.Status(r.Context())
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"status": status, "version": s.version})
}
func (s *Server) games(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "method_not_allowed", "games are read with GET")
		return
	}
	writeJSON(w, 200, map[string]any{"games": s.app.Games()})
}
func (s *Server) modelProfiles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		status, err := s.app.Status(r.Context())
		if err != nil {
			writeAppError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"model": status.Model, "model_error": status.ModelError, "providers": []map[string]string{{"provider": "deepseek", "model": "deepseek-v4-flash"}, {"provider": "openai", "model": "gpt-5-mini"}}})
	case "POST":
		var request storyapp.ModelConfigRequest
		if !decodeJSON(w, r, &request) {
			return
		}
		status, err := s.app.ConfigureModel(r.Context(), request)
		if err != nil {
			writeAppError(w, err)
			return
		}
		writeJSON(w, 200, status)
	default:
		writeError(w, 405, "method_not_allowed", "model profiles use GET or POST")
	}
}

func (s *Server) worlds(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		worlds, err := s.app.ListWorlds(r.Context())
		if err != nil {
			writeAppError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"worlds": worlds})
	case "POST":
		var request struct {
			Name          string `json:"name"`
			Mode          string `json:"mode"`
			PlayerName    string `json:"player_name"`
			PlayerProfile string `json:"player_profile"`
			Activate      bool   `json:"activate"`
		}
		if !decodeJSON(w, r, &request) {
			return
		}
		world, err := s.app.CreateWorld(r.Context(), request.Name, request.Mode, request.PlayerName, request.PlayerProfile, request.Activate)
		if err != nil {
			writeAppError(w, err)
			return
		}
		writeJSON(w, 201, map[string]any{"world": world})
	default:
		writeError(w, 405, "method_not_allowed", "worlds use GET or POST")
	}
}
func (s *Server) activeWorld(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "method_not_allowed", "active world is read with GET")
		return
	}
	status, err := s.app.Status(r.Context())
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, 200, status)
}

func (s *Server) handleWorldRoute(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(path.Clean(r.URL.Path), "/"), "/")
	if len(parts) < 4 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "worlds" {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, 404, "not_found", "no such API route")
		} else {
			writeError(w, 404, "not_found", "no such route")
		}
		return
	}
	worldID := parts[3]
	if len(parts) == 4 {
		s.world(w, r, worldID)
		return
	}
	if len(parts) == 5 && parts[4] == "messages" {
		s.messages(w, r, worldID)
		return
	}
	if len(parts) == 5 && parts[4] == "entities" {
		s.entities(w, r, worldID)
		return
	}
	if len(parts) == 5 && parts[4] == "activate" {
		s.activate(w, r, worldID)
		return
	}
	if len(parts) == 5 && parts[4] == "save-as" {
		s.saveAs(w, r, worldID)
		return
	}
	if len(parts) == 5 && parts[4] == "runs" {
		s.runs(w, r, worldID)
		return
	}
	if len(parts) == 6 && parts[4] == "runs" {
		s.run(w, r, worldID, parts[5])
		return
	}
	if len(parts) == 7 && parts[4] == "runs" && parts[6] == "cancel" {
		s.cancel(w, r, worldID, parts[5])
		return
	}
	if len(parts) == 7 && parts[4] == "runs" && parts[6] == "retry" {
		s.retry(w, r, worldID, parts[5])
		return
	}
	writeError(w, 404, "not_found", "no such world route")
}
func (s *Server) world(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case "GET":
		snapshot, err := s.app.ReadWorld(r.Context(), id, 100)
		if err != nil {
			writeAppError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"world": snapshot.Summary, "player_name": snapshot.PlayerName, "player_profile": snapshot.PlayerProfile, "messages": snapshot.Messages, "characters": storyapp.PublicCharacterViews(snapshot.Characters), "bystanders": snapshot.Bystanders})
	case "DELETE":
		if err := s.app.DeleteWorld(r.Context(), id); err != nil {
			writeAppError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, 405, "method_not_allowed", "world uses GET or DELETE")
	}
}
func (s *Server) messages(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != "GET" {
		writeError(w, 405, "method_not_allowed", "messages are read with GET")
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n < 500 {
			limit = n
		}
	}
	messages, err := s.app.ReadMessages(r.Context(), id, limit)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"messages": messages})
}
func (s *Server) entities(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != "GET" {
		writeError(w, 405, "method_not_allowed", "entities are read with GET")
		return
	}
	entities, err := s.app.ReadCharacters(r.Context(), id)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"entities": storyapp.PublicCharacterViews(entities)})
}
func (s *Server) activate(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != "POST" {
		writeError(w, 405, "method_not_allowed", "activation uses POST")
		return
	}
	var request struct {
		ExpectedActiveRevision int64  `json:"expected_active_revision"`
		RequestKey             string `json:"request_key"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	status, err := s.app.ActivateWorld(r.Context(), id, request.ExpectedActiveRevision, request.RequestKey)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, 200, status)
}
func (s *Server) saveAs(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != "POST" {
		writeError(w, 405, "method_not_allowed", "save-as uses POST")
		return
	}
	var request struct {
		Name                   string `json:"name"`
		RequestKey             string `json:"request_key"`
		ExpectedActiveRevision int64  `json:"expected_active_revision"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	operation, err := s.app.SaveAs(r.Context(), id, request.Name, request.RequestKey, request.ExpectedActiveRevision)
	if err != nil && operation.OperationID == "" {
		writeAppError(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"operation": operation})
}

func (s *Server) copyOperation(w http.ResponseWriter, r *http.Request, operationID string) {
	if r.Method != "GET" {
		writeError(w, 405, "method_not_allowed", "copy operation status is read with GET")
		return
	}
	operation, err := s.app.CopyOperation(r.Context(), operationID)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"operation": operation})
}
func (s *Server) runs(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method == "GET" {
		runs, err := s.app.ListRuns(r.Context(), id)
		if err != nil {
			writeAppError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"runs": runs})
		return
	}
	if r.Method != "POST" {
		writeError(w, 405, "method_not_allowed", "runs use GET or POST")
		return
	}
	var request storyapp.RunRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	run, err := s.app.SubmitRun(r.Context(), id, request)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"run": run})
}
func (s *Server) run(w http.ResponseWriter, r *http.Request, id, runID string) {
	if r.Method != "GET" {
		writeError(w, 405, "method_not_allowed", "run status is read with GET")
		return
	}
	run, err := s.app.Run(r.Context(), id, runID)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"run": run})
}
func (s *Server) cancel(w http.ResponseWriter, r *http.Request, id, runID string) {
	if r.Method != "POST" {
		writeError(w, 405, "method_not_allowed", "cancellation uses POST")
		return
	}
	if err := s.app.CancelRun(r.Context(), id, runID); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, 202, map[string]string{"status": "cancelling"})
}
func (s *Server) retry(w http.ResponseWriter, r *http.Request, id, runID string) {
	if r.Method != "POST" {
		writeError(w, 405, "method_not_allowed", "retry uses POST")
		return
	}
	var request struct {
		RequestKey string `json:"request_key"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	run, err := s.app.RetryRun(r.Context(), id, runID, request.RequestKey)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"run": run})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "method_not_allowed", "session exchange uses POST")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	value, ok := s.sessions.issue(body.Token)
	if !ok {
		writeError(w, 401, "invalid_bootstrap_token", "use the URL printed by the Runtime")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "wia_session", Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAssets(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		writeError(w, 405, "method_not_allowed", "assets are read with GET")
		return
	}
	if !s.hasIndex {
		writeError(w, 503, "assets_not_built", "build console/web before starting the Runtime")
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if s.serveAsset(w, r, name) {
		return
	}
	if name != "index.html" && s.serveAsset(w, r, "index.html") {
		return
	}
	writeError(w, 404, "not_found", "no such asset")
}
func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, name string) bool {
	file, err := s.assets.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		return false
	}
	content, ok := file.(io.ReadSeeker)
	if !ok {
		data, e := io.ReadAll(file)
		if e != nil {
			return false
		}
		content = strings.NewReader(string(data))
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, info.Name(), info.ModTime(), content)
	return true
}
func assetExists(assets fs.FS, name string) bool {
	if assets == nil {
		return false
	}
	info, err := fs.Stat(assets, name)
	return err == nil && !info.IsDir()
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, 400, "invalid_request", "request body is not valid for this operation")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, 400, "invalid_request", "request must contain one JSON object")
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func writeAppError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "storage_unavailable"
	switch {
	case errors.Is(err, storyapp.ErrInvalidRequest):
		status = 400
		code = "invalid_request"
	case errors.Is(err, storyapp.ErrUnauthorized):
		status = 401
		code = "unauthorized"
	case errors.Is(err, storyapp.ErrForbidden):
		status = 403
		code = "forbidden"
	case errors.Is(err, storyapp.ErrWorldNotFound):
		status = 404
		code = "world_not_found"
	case errors.Is(err, storyapp.ErrRunNotFound):
		status = 404
		code = "run_not_found"
	case errors.Is(err, storyapp.ErrWorldNotReady):
		status = 409
		code = "world_not_ready"
	case errors.Is(err, storyapp.ErrWorldBusy):
		status = 409
		code = "world_busy"
	case errors.Is(err, storyapp.ErrAppBusy):
		status = 409
		code = "app_busy"
	case errors.Is(err, storyapp.ErrVersionConflict):
		status = 409
		code = "version_conflict"
	case errors.Is(err, storyapp.ErrIdempotencyConflict):
		status = 409
		code = "idempotency_conflict"
	case errors.Is(err, storyapp.ErrModelNotConfigured):
		status = 409
		code = "model_not_configured"
	case strings.HasPrefix(err.Error(), "model_not_configured:"):
		status = 409
		code = "model_not_configured"
	case errors.Is(err, storyapp.ErrSaveFailed):
		status = 409
		code = "save_failed"
	}
	writeError(w, status, code, err.Error())
}

type sessions struct {
	mu        sync.Mutex
	bootstrap string
	values    map[string]struct{}
}

func newSessions() (*sessions, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	return &sessions{bootstrap: base64.RawURLEncoding.EncodeToString(buf), values: map[string]struct{}{}}, nil
}
func (s *sessions) issue(candidate string) (string, bool) {
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(candidate)), []byte(s.bootstrap)) != 1 {
		return "", false
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", false
	}
	value := base64.RawURLEncoding.EncodeToString(buf)
	s.mu.Lock()
	s.values[value] = struct{}{}
	s.mu.Unlock()
	return value, true
}
func (s *sessions) valid(r *http.Request) bool {
	cookie, err := r.Cookie("wia_session")
	if err != nil || cookie.Value == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.values[cookie.Value]
	return ok
}
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if strings.TrimSpace(host) == "" {
		return errors.New("listen address must be loopback")
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("listen address must be loopback")
	}
	return nil
}
func allowedHosts(addr net.Addr) map[string]struct{} {
	result := map[string]struct{}{}
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return result
	}
	port := strconv.Itoa(tcp.Port)
	for _, host := range []string{"127.0.0.1", "localhost", "::1"} {
		result[strings.ToLower(net.JoinHostPort(host, port))] = struct{}{}
	}
	return result
}

var _ = fmt.Sprintf
