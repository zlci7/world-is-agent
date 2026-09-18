// Package httpapi serves the local control plane.
//
// The control plane is deliberately small and deliberately local. It exists so
// the process can be configured and observed while the agent core is not ready,
// which is the one thing the gRPC adapter connection cannot do: an adapter only
// connects once the game is running. Nothing here is a second transport for the
// adapter, and nothing here reaches beyond the loopback interface.
//
// Two protections are not optional, because this is the only network-facing
// surface the Runtime has:
//
//   - The Host header must name a loopback host. A browser will happily resolve
//     an attacker-controlled name to 127.0.0.1, so without this check a web page
//     could read this API through DNS rebinding.
//   - Every /api route but the session exchange requires a session credential.
//     The credential is handed to the browser by the Runtime itself, never
//     copied by the user, and never leaves a same-site, HTTP-only cookie.
package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"gameagent/runtime/internal/bootstrap"
	"gameagent/runtime/internal/llm"
	"gameagent/runtime/internal/traceview"
)

// Options configures the control plane.
type Options struct {
	// Addr is the listen address. The host must resolve to a loopback address,
	// so "127.0.0.1:0" is valid and ":8080" is not.
	Addr string
	// Runtime is the bootstrap Runtime the client observes.
	Runtime *bootstrap.Runtime
	// Assets is the built client, rooted at its web root, so that "index.html"
	// is served for "/". A nil or empty Assets is reported to the browser as a
	// missing build rather than served as a blank page.
	Assets fs.FS
	// GRPCAddr is shown to the client so it can report where the adapter
	// connects. It is informational only.
	GRPCAddr string
	// Version is shown to the client.
	Version string
	// Logger receives lifecycle messages and HTTP server errors.
	Logger *log.Logger
}

// Server is the local control plane. It owns its listener, so the effective
// address is known before it starts serving.
type Server struct {
	options   Options
	listener  net.Listener
	server    *http.Server
	sessions  *sessionStore
	turns     *traceview.Reader
	tracePath string
	hasIndex  bool

	hosts map[string]struct{}
	url   string
}

// New binds the control plane listener and prepares the routes.
func New(options Options) (*Server, error) {
	if options.Runtime == nil {
		return nil, errors.New("httpapi: Runtime is required")
	}
	if options.Logger == nil {
		return nil, errors.New("httpapi: Logger is required")
	}
	if err := requireLoopback(options.Addr); err != nil {
		return nil, err
	}

	listener, err := net.Listen("tcp", options.Addr)
	if err != nil {
		return nil, fmt.Errorf("httpapi: listen on %s: %w", options.Addr, err)
	}

	sessions, err := newSessionStore()
	if err != nil {
		listener.Close()
		return nil, err
	}

	tracePath := options.Runtime.Layout().TracePath()
	server := &Server{
		options:   options,
		listener:  listener,
		sessions:  sessions,
		turns:     traceview.NewReader(tracePath, traceview.Options{MaxBytes: traceview.DefaultMaxBytes, MaxTurns: traceview.DefaultMaxTurns}),
		tracePath: tracePath,
		hosts:     allowedHosts(listener.Addr()),
		url:       "http://" + listener.Addr().String(),
	}
	server.hasIndex = server.assetExists(indexFile)

	server.server = &http.Server{
		Handler:           http.HandlerFunc(server.handle),
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          options.Logger,
	}
	return server, nil
}

// URL is the control plane origin.
func (s *Server) URL() string { return s.url }

// BrowserURL is the URL the Runtime hands to the browser. The bootstrap token
// travels in the fragment, which a browser never sends to the server, so it does
// not reach the Runtime log or any proxy.
func (s *Server) BrowserURL() string { return s.url + "/#token=" + s.sessions.bootstrapToken() }

// Serve blocks until the server stops. A clean shutdown is not an error.
func (s *Server) Serve() error {
	err := s.server.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown stops the server and releases the listener.
func (s *Server) Shutdown() error {
	return s.server.Close()
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	// Every route is behind the Host check, including static assets: DNS
	// rebinding does not care which route it reads.
	if !s.hostAllowed(r.Host) {
		writeError(w, http.StatusForbidden, "host_not_allowed",
			fmt.Sprintf("the local control plane only answers loopback host names, received %q", r.Host))
		return
	}

	if strings.HasPrefix(r.URL.Path, "/api/") {
		s.handleAPI(w, r)
		return
	}
	s.handleAssets(w, r)
}

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/session" {
		s.handleSession(w, r)
		return
	}
	if !s.sessions.valid(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized",
			"this route needs a session credential; open the URL the Runtime printed at startup")
		return
	}

	switch r.URL.Path {
	case "/api/status":
		s.handleStatus(w, r)
	case "/api/turns":
		s.handleTurns(w, r)
	default:
		writeError(w, http.StatusNotFound, "not_found", "no such route")
	}
}

type statusResponse struct {
	State  string `json:"state"`
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"`

	DataRoot        string `json:"data_root"`
	ConfigDir       string `json:"config_dir"`
	ModelConfigPath string `json:"model_config_path"`
	AgentConfigPath string `json:"agent_config_path"`
	TracePath       string `json:"trace_path"`

	GRPCAddr string `json:"grpc_addr,omitempty"`
	Version  string `json:"version,omitempty"`

	// Model describes the configuration without its credential. ModelError
	// explains why there is nothing to describe.
	Model      *llm.ConfigSummary `json:"model,omitempty"`
	ModelError string             `json:"model_error,omitempty"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "status is read with GET")
		return
	}

	runtime := s.options.Runtime
	layout := runtime.Layout()
	response := statusResponse{
		State:           string(runtime.State()),
		Ready:           runtime.Ready(),
		Reason:          runtime.Reason(),
		DataRoot:        layout.Root(),
		ConfigDir:       layout.ConfigDir(),
		ModelConfigPath: runtime.ModelConfigPath(),
		AgentConfigPath: runtime.AgentConfigPath(),
		TracePath:       s.tracePath,
		GRPCAddr:        s.options.GRPCAddr,
		Version:         s.options.Version,
	}
	// A missing or broken model configuration is exactly what the client has to
	// report, so it is a field rather than a failed request.
	if summary, err := llm.DescribeConfig(runtime.ModelConfigPath()); err != nil {
		response.ModelError = err.Error()
	} else {
		response.Model = &summary
	}
	writeJSON(w, http.StatusOK, response)
}

type turnsResponse struct {
	TracePath string           `json:"trace_path"`
	Turns     []traceview.Turn `json:"turns"`
}

// MaxTurnsPerRequest bounds one response. The client shows recent activity, and
// an unbounded response would grow with the trace.
const MaxTurnsPerRequest = 200

// defaultTurnsPerRequest is what the client gets when it does not ask.
const defaultTurnsPerRequest = 50

func (s *Server) handleTurns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "turns are read with GET")
		return
	}

	limit, err := turnLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_limit", err.Error())
		return
	}

	turns, err := s.turns.Turns()
	if err != nil {
		// The client can still show status; a trace it cannot read is reported
		// instead of failing the whole view.
		writeError(w, http.StatusInternalServerError, "trace_unreadable", err.Error())
		return
	}
	if len(turns) > limit {
		turns = turns[:limit]
	}
	if turns == nil {
		turns = []traceview.Turn{}
	}
	writeJSON(w, http.StatusOK, turnsResponse{TracePath: s.tracePath, Turns: turns})
}

func turnLimit(value string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return defaultTurnsPerRequest, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit <= 0 {
		return 0, fmt.Errorf("limit must be a positive integer, received %q", value)
	}
	if limit > MaxTurnsPerRequest {
		return MaxTurnsPerRequest, nil
	}
	return limit, nil
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "a session is created with POST")
		return
	}
	// A same-site cookie already stops a cross-site POST from carrying a
	// credential. Rejecting a foreign Origin is the second lock, and it is the
	// only state-changing route this server has.
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.url {
		writeError(w, http.StatusForbidden, "origin_not_allowed", fmt.Sprintf("unexpected origin %q", origin))
		return
	}

	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "expected a JSON object with a token field")
		return
	}

	value, ok := s.sessions.issue(body.Token)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid_bootstrap_token",
			"the bootstrap token is not valid; use the URL the Runtime printed at startup")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		// No Secure attribute: the control plane is plain HTTP on loopback, and
		// Secure would stop the browser from ever storing the cookie.
	})
	w.WriteHeader(http.StatusNoContent)
}

// indexFile is the client entry point inside Assets.
const indexFile = "index.html"

const assetsMissingMessage = `the client assets are not built.

Build them with:
    cd console/web && npm install && npm run build

Then embed them by rebuilding the Runtime.`

func (s *Server) handleAssets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "the client is read with GET")
		return
	}
	if !s.hasIndex {
		writeError(w, http.StatusServiceUnavailable, "assets_not_built", assetsMissingMessage)
		return
	}

	name := strings.TrimPrefix(assetPath(r), "/")
	if name == "" {
		name = indexFile
	}
	if s.serveAsset(w, r, name) {
		return
	}
	// A client-side route has no file behind it, so the entry point answers and
	// the client resolves the route itself.
	if name != indexFile && s.serveAsset(w, r, indexFile) {
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "no such asset")
}

func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, name string) bool {
	file, err := s.options.Assets.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || info.IsDir() {
		return false
	}

	// http.ServeContent needs a seeker for range requests. Embedded files have
	// one; anything else is buffered rather than refused.
	content, ok := file.(io.ReadSeeker)
	if !ok {
		data, readErr := io.ReadAll(file)
		if readErr != nil {
			return false
		}
		content = bytes.NewReader(data)
	}
	// The client is embedded in the binary, so a cached copy can only be staler
	// than what this process serves. Refusing to store it removes a class of
	// "the UI did not change after a rebuild" confusion, at the cost of
	// re-reading a small bundle over loopback.
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, info.Name(), info.ModTime(), content)
	return true
}

func (s *Server) assetExists(name string) bool {
	if s.options.Assets == nil {
		return false
	}
	info, err := fs.Stat(s.options.Assets, name)
	return err == nil && !info.IsDir()
}

// assetPath returns the cleaned request path. Cleaning happens before the asset
// tree is consulted, so "../" cannot address anything outside the client.
func assetPath(r *http.Request) string {
	return cleanRequestPath(r.URL.Path)
}

func cleanRequestPath(value string) string {
	return path.Clean("/" + strings.TrimSpace(value))
}

func (s *Server) hostAllowed(host string) bool {
	_, ok := s.hosts[strings.ToLower(strings.TrimSpace(host))]
	return ok
}

func allowedHosts(addr net.Addr) map[string]struct{} {
	hosts := make(map[string]struct{}, 3)
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok {
		return hosts
	}
	port := strconv.Itoa(tcpAddr.Port)
	// The listener is on a loopback address, so any of these names may legally
	// appear in the Host header of a request that reached it.
	for _, name := range []string{"127.0.0.1", "localhost", "::1"} {
		hosts[strings.ToLower(net.JoinHostPort(name, port))] = struct{}{}
	}
	return hosts
}

// requireLoopback rejects any listen address that could be reached from another
// machine. This is the boundary the product promised: local browser only.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("httpapi: %q is not a host:port listen address", addr)
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("httpapi: %q listens on every interface; the control plane is loopback only", addr)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("httpapi: %q is not a loopback address; the control plane is loopback only", addr)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("httpapi: %q is not a loopback address; the control plane is loopback only", addr)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	// The status line is already written, so an encode failure cannot be turned
	// into an error response. A partial body is the honest outcome.
	_ = json.NewEncoder(w).Encode(payload)
}

type errorResponse struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: errorBody{Code: code, Message: message}})
}
