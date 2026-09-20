package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"gameagent/runtime/internal/bootstrap"
	"gameagent/runtime/internal/dataroot"
)

type fixture struct {
	server  *Server
	runtime *bootstrap.Runtime
	root    string
	host    string
	origin  string
	token   string
}

// fixtureEnv keeps the tests independent from whatever the developer has exported.
func fixtureEnv(root string) dataroot.Env {
	return dataroot.Env{
		GOOS:    "windows",
		Getenv:  func(string) string { return "" },
		HomeDir: func() (string, error) { return root, nil },
	}
}

func newFixture(t *testing.T, assets fs.FS) *fixture {
	t.Helper()

	root := t.TempDir()
	runtime, err := bootstrap.Open(root, fixtureEnv(root))
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	server, err := New(Options{
		Addr:     "127.0.0.1:0",
		Runtime:  runtime,
		Assets:   assets,
		GRPCAddr: "127.0.0.1:50051",
		Version:  "test",
		Logger:   log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(func() { _ = server.Shutdown() })

	_, port, err := net.SplitHostPort(server.listener.Addr().String())
	if err != nil {
		t.Fatalf("listener address: %v", err)
	}
	host := "127.0.0.1:" + port
	return &fixture{
		server:  server,
		runtime: runtime,
		root:    root,
		host:    host,
		origin:  "http://" + host,
		token:   server.sessions.bootstrapToken(),
	}
}

func (f *fixture) do(t *testing.T, method, target, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, f.origin+target, reader)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	f.server.server.Handler.ServeHTTP(recorder, request)
	return recorder
}

// session exchanges the bootstrap token the way the client does, and returns the
// cookie that authorizes the rest of the API.
func (f *fixture) session(t *testing.T) *http.Cookie {
	t.Helper()
	recorder := f.do(t, http.MethodPost, "/api/session", `{"token":"`+f.token+`"}`)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("session exchange status = %d, want 204: %s", recorder.Code, recorder.Body.String())
	}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			return cookie
		}
	}
	t.Fatalf("session exchange set no %s cookie", sessionCookieName)
	return nil
}

func decodeBody[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %s: %v", recorder.Body.String(), err)
	}
	return value
}

func TestControlPlaneRejectsANonLoopbackHostHeader(t *testing.T) {
	f := newFixture(t, nil)
	cookie := f.session(t)

	// DNS rebinding looks exactly like this: an attacker-controlled name that
	// resolves to 127.0.0.1. The Host header is the only place it shows.
	for _, host := range []string{"evil.example.com", "127.0.0.1.nip.io:" + port(t, f.host), "localhost", "attacker.test:80"} {
		request := httptest.NewRequest(http.MethodGet, f.origin+"/api/status", nil)
		request.Host = host
		request.AddCookie(cookie)
		recorder := httptest.NewRecorder()
		f.server.server.Handler.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusForbidden {
			t.Errorf("Host %q: status = %d, want 403", host, recorder.Code)
		}
	}
}

func TestControlPlaneAcceptsLoopbackHostNames(t *testing.T) {
	f := newFixture(t, nil)
	cookie := f.session(t)

	for _, host := range []string{f.host, "localhost:" + port(t, f.host), "LOCALHOST:" + port(t, f.host)} {
		request := httptest.NewRequest(http.MethodGet, f.origin+"/api/status", nil)
		request.Host = host
		request.AddCookie(cookie)
		recorder := httptest.NewRecorder()
		f.server.server.Handler.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Errorf("Host %q: status = %d, want 200", host, recorder.Code)
		}
	}
}

func TestAPIRequiresASessionCredential(t *testing.T) {
	f := newFixture(t, nil)

	for _, target := range []string{"/api/status", "/api/turns"} {
		recorder := f.do(t, http.MethodGet, target, "")
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s without a session: status = %d, want 401", target, recorder.Code)
		}
	}

	// A cookie that was never issued must not be accepted either.
	recorder := f.do(t, http.MethodGet, "/api/status", "", &http.Cookie{Name: sessionCookieName, Value: "guessed"})
	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("status with an unknown cookie = %d, want 401", recorder.Code)
	}
}

func TestSessionExchangeRejectsAWrongBootstrapToken(t *testing.T) {
	f := newFixture(t, nil)

	recorder := f.do(t, http.MethodPost, "/api/session", `{"token":"not-the-token"}`)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if len(recorder.Result().Cookies()) != 0 {
		t.Fatal("a rejected exchange must not set a cookie")
	}

	// An empty token is not a way in either.
	recorder = f.do(t, http.MethodPost, "/api/session", `{"token":""}`)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("empty token status = %d, want 401", recorder.Code)
	}
}

func TestSessionExchangeRejectsAForeignOrigin(t *testing.T) {
	f := newFixture(t, nil)

	request := httptest.NewRequest(http.MethodPost, f.origin+"/api/session", strings.NewReader(`{"token":"`+f.token+`"}`))
	request.Header.Set("Origin", "http://evil.example.com")
	recorder := httptest.NewRecorder()
	f.server.server.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	if len(recorder.Result().Cookies()) != 0 {
		t.Fatal("a rejected exchange must not set a cookie")
	}
}

func TestSessionExchangeAcceptsTheRuntimeOrigin(t *testing.T) {
	f := newFixture(t, nil)

	request := httptest.NewRequest(http.MethodPost, f.origin+"/api/session", strings.NewReader(`{"token":"`+f.token+`"}`))
	request.Header.Set("Origin", f.origin)
	recorder := httptest.NewRecorder()
	f.server.server.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", recorder.Code, recorder.Body.String())
	}
}

func TestSessionCookieIsStrictAndHttpOnly(t *testing.T) {
	f := newFixture(t, nil)

	recorder := f.do(t, http.MethodPost, "/api/session", `{"token":"`+f.token+`"}`)
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if !cookie.HttpOnly {
		t.Error("the session cookie must not be readable from JavaScript")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Error("the session cookie must be SameSite=Strict so a cross-site POST cannot use it")
	}
	if cookie.Path != "/" {
		t.Errorf("cookie path = %q, want /", cookie.Path)
	}
}

func TestStatusReportsConfigurationWithoutTheCredential(t *testing.T) {
	t.Setenv("WIA_STATUS_TEST_KEY", "sk-status-must-not-leak")

	f := newFixture(t, nil)
	writeModelConfig(t, f.root, `{
		"provider": "deepseek",
		"model": "deepseek-v4-flash",
		"api_key": "env:WIA_STATUS_TEST_KEY",
		"base_url": "https://api.deepseek.com"
	}`)
	_ = f.runtime.Configure()

	recorder := f.do(t, http.MethodGet, "/api/status", "", f.session(t))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "sk-status-must-not-leak") {
		t.Fatal("the status response echoed the credential")
	}

	status := decodeBody[statusResponse](t, recorder)
	if status.State != string(bootstrap.StateNeedsConfiguration) {
		t.Errorf("state = %q, want %q", status.State, bootstrap.StateNeedsConfiguration)
	}
	if status.Ready {
		t.Error("a runtime without an agent configuration is not ready")
	}
	if status.DataRoot != f.root {
		t.Errorf("data root = %q, want %q", status.DataRoot, f.root)
	}
	if status.TracePath != f.runtime.Layout().TracePath() {
		t.Errorf("trace path = %q, want %q", status.TracePath, f.runtime.Layout().TracePath())
	}
	if status.Model == nil {
		t.Fatal("the model configuration must be described")
	}
	if status.Model.Provider != "deepseek" || status.Model.Model != "deepseek-v4-flash" {
		t.Errorf("model = %+v", *status.Model)
	}
	if !status.Model.APIKeyConfigured {
		t.Error("api_key_configured = false, want true")
	}
}

// A missing model configuration is the normal first-run state, and the client has
// to be able to report it rather than see a failed request.
func TestStatusReportsAMissingModelConfiguration(t *testing.T) {
	f := newFixture(t, nil)

	recorder := f.do(t, http.MethodGet, "/api/status", "", f.session(t))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	status := decodeBody[statusResponse](t, recorder)
	if status.ModelError == "" {
		t.Error("model_error is empty, want the reason the client has to display")
	}
	if status.Model != nil {
		t.Errorf("model = %+v, want none", *status.Model)
	}
}

func TestTurnsProjectsTheTrace(t *testing.T) {
	f := newFixture(t, nil)
	tracePath := f.runtime.Layout().TracePath()
	body := strings.Join([]string{
		`{"turn_id":"turn_1","seq":1,"event":"turn_started","time":"2026-09-18T21:08:11Z","game_id":"stardew-valley","event_type":"player_said_to_npc","entity_id":"npc:Penny","fields":{"turn_tool_names":["send_mail","present_dialogue"]}}`,
		`{"turn_id":"turn_1","seq":2,"event":"agent_step_started"}`,
		`{"turn_id":"turn_1","seq":3,"event":"tool_call_selected","tool":"send_mail"}`,
		`{"turn_id":"turn_1","seq":4,"event":"turn_completed","tool":"send_mail"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(tracePath, []byte(body), 0o600); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	recorder := f.do(t, http.MethodGet, "/api/turns?game_id=stardew-valley", "", f.session(t))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	response := decodeBody[turnsResponse](t, recorder)
	if len(response.Turns) != 1 {
		t.Fatalf("turns = %d, want 1", len(response.Turns))
	}
	turn := response.Turns[0]
	if turn.TurnID != "turn_1" || turn.Status != "completed" || turn.SettledBy != "send_mail" {
		t.Fatalf("turn = %+v", turn)
	}
	if turn.EntityID != "npc:Penny" {
		t.Errorf("entity = %q, want npc:Penny", turn.EntityID)
	}
}

func TestTurnsFiltersByGameBeforeApplyingTheLimit(t *testing.T) {
	f := newFixture(t, nil)
	tracePath := f.runtime.Layout().TracePath()
	lines := []string{
		`{"turn_id":"stardew-old","seq":1,"event":"turn_started","time":"2026-09-18T21:08:11Z","game_id":"stardew-valley","entity_id":"npc:Penny"}`,
		`{"turn_id":"stardew-old","seq":2,"event":"turn_completed","time":"2026-09-18T21:08:12Z","game_id":"stardew-valley"}`,
	}
	// More than the trace reader's former global turn cap proves that filtering
	// happens before either the projection cap or the HTTP response limit.
	for index := 0; index < 250; index++ {
		lines = append(lines,
			fmt.Sprintf(`{"turn_id":"rim-%d","seq":1,"event":"turn_started","time":"2026-09-18T21:09:11Z","game_id":"rimworld","entity_id":"pawn:%d"}`, index, index),
			fmt.Sprintf(`{"turn_id":"rim-%d","seq":2,"event":"turn_completed","time":"2026-09-18T21:09:12Z","game_id":"rimworld"}`, index),
		)
	}
	if err := os.WriteFile(tracePath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	recorder := f.do(t, http.MethodGet, "/api/turns?game_id=stardew-valley&limit=1", "", f.session(t))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	response := decodeBody[turnsResponse](t, recorder)
	if len(response.Turns) != 1 || response.Turns[0].TurnID != "stardew-old" {
		t.Fatalf("turns = %+v, want the Stardew turn even though newer RimWorld turns exceed the limit", response.Turns)
	}
}

func TestTurnsRequiresAGameContext(t *testing.T) {
	f := newFixture(t, nil)
	recorder := f.do(t, http.MethodGet, "/api/turns", "", f.session(t))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestTurnsValidatesTheLimit(t *testing.T) {
	f := newFixture(t, nil)
	cookie := f.session(t)

	for _, limit := range []string{"0", "-1", "many"} {
		recorder := f.do(t, http.MethodGet, "/api/turns?game_id=stardew-valley&limit="+limit, "", cookie)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("limit %q: status = %d, want 400", limit, recorder.Code)
		}
	}

	// An oversized limit is clamped rather than refused: it is a bound on the
	// response, not an error in the request.
	recorder := f.do(t, http.MethodGet, "/api/turns?game_id=stardew-valley&limit=100000", "", cookie)
	if recorder.Code != http.StatusOK {
		t.Fatalf("large limit: status = %d, want 200", recorder.Code)
	}
	if got := decodeBody[turnsResponse](t, recorder); len(got.Turns) > MaxTurnsPerRequest {
		t.Errorf("turns = %d, want at most %d", len(got.Turns), MaxTurnsPerRequest)
	}
}

func TestAssetsReportAMissingBuildInsteadOfServingNothing(t *testing.T) {
	f := newFixture(t, nil)

	recorder := f.do(t, http.MethodGet, "/", "")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	response := decodeBody[errorResponse](t, recorder)
	if response.Error.Code != "assets_not_built" {
		t.Errorf("code = %q, want assets_not_built", response.Error.Code)
	}
	if !strings.Contains(response.Error.Message, "npm run build") {
		t.Errorf("the message must tell the developer how to build the client, got %q", response.Error.Message)
	}
}

func TestAssetsServeTheClientAndItsRoutes(t *testing.T) {
	f := newFixture(t, fstest.MapFS{
		"index.html":    &fstest.MapFile{Data: []byte("<html>wia-client</html>")},
		"assets/app.js": &fstest.MapFile{Data: []byte("console.log('wia')")},
	})

	// The entry point is served for the root.
	recorder := f.do(t, http.MethodGet, "/", "")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "wia-client") {
		t.Fatalf("root: status = %d, body = %q", recorder.Code, recorder.Body.String())
	}

	// A built asset is served from the embedded tree.
	recorder = f.do(t, http.MethodGet, "/assets/app.js", "")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "console.log") {
		t.Fatalf("asset: status = %d, body = %q", recorder.Code, recorder.Body.String())
	}

	// A client-side route has no file behind it, so the entry point answers.
	recorder = f.do(t, http.MethodGet, "/turns/turn_1", "")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "wia-client") {
		t.Fatalf("client route: status = %d, body = %q", recorder.Code, recorder.Body.String())
	}

	// Static assets are served without a session: the entry point has to load
	// before it can exchange the bootstrap token.
	if recorder.Code != http.StatusOK {
		t.Fatalf("assets must not require a session, got %d", recorder.Code)
	}
}

func TestAssetPathCannotEscapeTheClientRoot(t *testing.T) {
	for input, want := range map[string]string{
		"/":                    "/",
		"":                     "/",
		"/assets/app.js":       "/assets/app.js",
		"/../secret.txt":       "/secret.txt",
		"/assets/../../secret": "/secret",
		"//assets//app.js":     "/assets/app.js",
		"/./assets/./app.js":   "/assets/app.js",
	} {
		if got := cleanRequestPath(input); got != want {
			t.Errorf("cleanRequestPath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRequireLoopbackRejectsReachableAddresses(t *testing.T) {
	rejected := []string{"", ":8080", "0.0.0.0:8080", "[::]:8080", "192.168.1.10:8080", "example.com:8080", "127.0.0.1"}
	for _, addr := range rejected {
		if err := requireLoopback(addr); err == nil {
			t.Errorf("requireLoopback(%q) = nil, want an error", addr)
		}
	}

	accepted := []string{"127.0.0.1:0", "127.0.0.1:8765", "localhost:0", "[::1]:0"}
	for _, addr := range accepted {
		if err := requireLoopback(addr); err != nil {
			t.Errorf("requireLoopback(%q) = %v, want nil", addr, err)
		}
	}
}

func TestUnknownAPIRouteIsNotFound(t *testing.T) {
	f := newFixture(t, nil)

	recorder := f.do(t, http.MethodGet, "/api/nothing", "", f.session(t))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

func TestBrowserURLKeepsTheTokenInTheFragment(t *testing.T) {
	f := newFixture(t, nil)

	url := f.server.BrowserURL()
	if !strings.HasPrefix(url, f.origin+"/#token=") {
		t.Fatalf("browser URL = %q, want a token in the fragment", url)
	}
	// A fragment is not sent to the server, so the token must not appear in the
	// path or query.
	if strings.Contains(strings.SplitN(url, "#", 2)[0], f.token) {
		t.Fatal("the bootstrap token must not be part of the request the browser sends")
	}
}

func writeModelConfig(t *testing.T, root, body string) {
	t.Helper()
	layout := dataroot.New(root)
	if err := os.MkdirAll(layout.ConfigDir(), 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	path := filepath.Join(layout.ConfigDir(), "model.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write model config: %v", err)
	}
}

func port(t *testing.T, host string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(host)
	if err != nil {
		t.Fatalf("split host: %v", err)
	}
	return port
}
