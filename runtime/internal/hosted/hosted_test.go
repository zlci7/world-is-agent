package hosted

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/accounts"
	"gameagent/runtime/internal/bootstrap"
	"gameagent/runtime/internal/dataroot"
	"gameagent/runtime/internal/experience"
	"gameagent/runtime/internal/memory"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/workerapi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type localWorkers struct {
	t           *testing.T
	mu          sync.Mutex
	root        string
	assignments map[string]Assignment
	runtimes    map[string]*bootstrap.Runtime
	stops       map[string]func()
	preview     bool
}

func (w *localWorkers) Inspect(_ context.Context, user string) (WorkerStatus, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	state := WorkerStatus{State: "not_allocated"}
	if _, ok := w.assignments[user]; ok {
		state.State = "running"
	}
	return state, nil
}

func (w *localWorkers) Ensure(ctx context.Context, user string) (Assignment, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if a, ok := w.assignments[user]; ok {
		return a, nil
	}
	root := filepath.Join(w.root, user)
	if err := os.MkdirAll(filepath.Join(root, "config"), 0755); err != nil {
		return Assignment{}, err
	}
	if err := os.WriteFile(filepath.Join(root, "config", "model.json"), []byte(`{"provider":"fake"}`), 0644); err != nil {
		return Assignment{}, err
	}
	if w.preview {
		if err := seedPreviewWorld(ctx, root); err != nil {
			return Assignment{}, err
		}
	}
	env := dataroot.Env{GOOS: "windows", Getenv: func(string) string { return "" }, HomeDir: func() (string, error) { return root, nil }}
	r, err := bootstrap.Open(root, env)
	if err != nil {
		return Assignment{}, err
	}
	if !r.Ready() {
		if err = r.SelectGame("stardew-valley"); err != nil {
			r.Close()
			return Assignment{}, err
		}
	}
	a := Assignment{UserID: user, Generation: "test-generation", Token: strings.Repeat(user, 2)}
	web := httptest.NewServer(workerapi.Handler(r, a.Token, "test"))
	a.API = web.URL
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		web.Close()
		r.Close()
		return Assignment{}, err
	}
	g := grpc.NewServer(grpc.StreamInterceptor(workerapi.StreamAuth(a.Token)))
	protocol.RegisterGameAgentGatewayServer(g, r)
	go g.Serve(l)
	a.GRPC = l.Addr().String()
	w.assignments[user] = a
	w.runtimes[user] = r
	w.stops[user] = func() { g.Stop(); web.Close(); r.Close() }
	return a, nil
}
func (w *localWorkers) Authenticate(ctx context.Context, token string) (Assignment, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, a := range w.assignments {
		if subtleToken(token, a.Token) {
			return a, nil
		}
	}
	return Assignment{}, accounts.ErrCredentials
}
func (w *localWorkers) Close(context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, stop := range w.stops {
		stop()
	}
	w.assignments = map[string]Assignment{}
	w.runtimes = map[string]*bootstrap.Runtime{}
	w.stops = map[string]func(){}
	return nil
}

type fixture struct {
	s       *Server
	workers *localWorkers
	store   *accounts.Store
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	store, err := accounts.Open(filepath.Join(root, "accounts.db"), []byte(strings.Repeat("p", 32)))
	if err != nil {
		t.Fatal(err)
	}
	w := &localWorkers{t: t, root: root, assignments: map[string]Assignment{}, runtimes: map[string]*bootstrap.Runtime{}, stops: map[string]func(){}}
	c := Config{GameID: "stardew-valley", Origin: "https://wia.example", GatewayURL: "https://gateway.example", DailyTokens: 1000000, ContextTokens: 16384, OutputTokens: 512, GlobalModelConcurrency: 2, ModelTimeoutSeconds: 3, SessionHours: 24, KeyDays: 30, MinAdapterVersion: "0.1.2", Model: "test-model", ModelKeyEnv: "WIA_TEST_PLATFORM_KEY"}
	f := &fixture{s: New(c, store, w, fstest.MapFS{"hosted.html": {Data: []byte("hosted-player-entry")}, "index.html": {Data: []byte("local-management-entry")}}), workers: w, store: store}
	t.Cleanup(func() { f.s.Close(); w.Close(context.Background()); store.Close() })
	return f
}
func (f *fixture) user(t *testing.T, email string) accounts.User {
	t.Helper()
	invite, err := f.store.Invite(context.Background(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	u, err := f.store.Register(context.Background(), email, "a long test password", invite, f.proof(t, email, "register", invite))
	if err != nil {
		t.Fatal(err)
	}
	return u
}
func (f *fixture) request(t *testing.T, method, path, body, secret, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "https://wia.example"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://wia.example")
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	if secret != "" {
		name := sessionCookie
		if strings.HasPrefix(path, "/api/admin/") {
			name = adminCookie
		}
		r.AddCookie(&http.Cookie{Name: name, Value: secret})
	}
	r.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, r)
	return rec
}
func (f *fixture) webSession(t *testing.T, user string) (string, string) {
	t.Helper()
	secret, err := f.store.NewSession(context.Background(), user, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	s, err := f.store.Session(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	return secret, s.CSRF
}

func TestHostedRegistrationSessionCsrfAndPrivateSurface(t *testing.T) {
	f := newFixture(t)
	invite, err := f.store.Invite(context.Background(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	proof := f.proof(t, "player@example.com", "register", invite)
	body, _ := json.Marshal(map[string]string{"email": "player@example.com", "password": "a long test password", "invitation": invite, "challenge_id": proof.ChallengeID, "code": proof.Code})
	r := f.request(t, "POST", "/api/invitations/redeem", string(body), "", "")
	if r.Code != 201 {
		t.Fatalf("register %d %s", r.Code, r.Body.String())
	}
	cookies := r.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe session cookie")
	}
	var session accounts.Session
	if json.Unmarshal(r.Body.Bytes(), &session) != nil {
		t.Fatal("decode session")
	}
	secret := cookies[0].Value
	if r = f.request(t, "GET", "/api/me", "", secret, ""); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if r = f.request(t, "POST", "/api/me/keys", "{}", secret, ""); r.Code != 403 {
		t.Fatal("CSRF bypass")
	}
	if r = f.request(t, "POST", "/api/me/keys", "{}", secret, session.CSRF); r.Code != 201 {
		t.Fatal(r.Body.String())
	}
	var key struct {
		Key    accounts.Key `json:"key"`
		Secret string       `json:"secret"`
	}
	if err = json.Unmarshal(r.Body.Bytes(), &key); err != nil {
		t.Fatal(err)
	}
	if r = f.request(t, "GET", "/api/me/keys", "", secret, ""); strings.Contains(r.Body.String(), key.Secret) {
		t.Fatal("key plaintext returned twice")
	}
	for _, path := range []string{"/api/setup/model", "/api/status", "/api/me/../../status", "/internal/model/v1/chat/completions"} {
		if r = f.request(t, "GET", path, "", secret, ""); r.Code == 200 {
			t.Fatalf("management path exposed: %s", path)
		}
	}
	if r = f.request(t, "GET", "/index.html", "", "", ""); strings.Contains(r.Body.String(), "local-management-entry") {
		t.Fatal("served local control plane")
	}
	if r = f.request(t, "DELETE", "/api/session", "", secret, session.CSRF); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if r = f.request(t, "GET", "/api/me", "", secret, ""); r.Code != 401 {
		t.Fatal("logout retained web session")
	}
}

func TestTwoAccountsKeepIdenticalWorldExperienceIDsIsolatedAcrossRestart(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := f.user(t, "first@example.com")
	b := f.user(t, "second@example.com")
	owner := session.AgentSessionKey{GameID: "stardew-valley", WorldID: "same-world", EntityID: "same-npc"}
	appendFact := func(user, text string) string {
		_, err := f.workers.Ensure(ctx, user)
		if err != nil {
			t.Fatal(err)
		}
		record, err := f.workers.runtimes[user].HistoryStore().AppendHistory(ctx, memory.HistoryBatch{Owner: owner, Kind: memory.HistoryKindExperience, Version: memory.HistoryVersion, Experience: &experience.Record{ClockID: "generic.clock", OccurredTick: 100, FocusUntilTick: 400, Priority: 50}, Event: memory.HistoryEvent{ID: "same-gift-id", Type: "test.gift", GameTime: memory.TickGameTime(100), Facts: []memory.SourceContextFact{{Kind: "interaction", ActorEntityID: "player", TargetEntityID: owner.EntityID, Text: text}}}})
		if err != nil {
			t.Fatal(err)
		}
		return record.ID
	}
	idA := appendFact(a.ID, "玩家送了蓝莓。")
	idB := appendFact(b.ID, "玩家送了南瓜。")
	f.workers.Close(ctx)
	for _, item := range []struct{ user, id, want, reject string }{{a.ID, idA, "蓝莓", "南瓜"}, {b.ID, idB, "南瓜", "蓝莓"}} {
		if _, err := f.workers.Ensure(ctx, item.user); err != nil {
			t.Fatal(err)
		}
		fact, err := f.workers.runtimes[item.user].HistoryStore().ReadHistorySource(ctx, owner, item.id)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(fact)
		if !strings.Contains(string(data), item.want) || strings.Contains(string(data), item.reject) {
			t.Fatalf("isolation failed %s", data)
		}
		secret, _ := f.webSession(t, item.user)
		r := f.request(t, "GET", "/api/me/status?user_id="+b.ID, "", secret, "")
		if r.Code != 200 || strings.Contains(r.Body.String(), f.workers.root) || strings.Contains(r.Body.String(), "http://") || strings.Contains(r.Body.String(), "model.json") {
			t.Fatalf("status exposed server data %d %s", r.Code, r.Body.String())
		}
		var state struct {
			ID string `json:"instance_id"`
		}
		_ = json.Unmarshal(r.Body.Bytes(), &state)
		query := fmt.Sprintf("/api/me/task-worlds?instance_id=%s&game_id=stardew-valley&user_id=%s", state.ID, b.ID)
		if r = f.request(t, "GET", query, "", secret, ""); r.Code != 400 {
			t.Fatalf("owner parameter was accepted by task query: %d", r.Code)
		}
	}
}

func (f *fixture) gatewayClient(t *testing.T) protocol.GameAgentGatewayClient {
	t.Helper()
	certificateServer := httptest.NewTLSServer(http.NotFoundHandler())
	cert := certificateServer.TLS.Certificates[0]
	certificateServer.Close()
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})))
	protocol.RegisterGameAgentGatewayServer(g, f.s)
	go g.Serve(l)
	c, err := grpc.NewClient(l.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: roots, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); g.Stop(); l.Close() })
	return protocol.NewGameAgentGatewayClient(c)
}
func openHosted(t *testing.T, client protocol.GameAgentGatewayClient, key, device string) (protocol.GameAgentGateway_ConnectClient, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+key, "x-wia-device-id", device))
	stream, err := client.Connect(ctx)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	err = stream.Send(&protocol.AdapterMessage{MessageId: "same-hello", Payload: &protocol.AdapterMessage_Hello{Hello: &protocol.AdapterHello{AdapterId: "test", AdapterVersion: "0.1.2", ProtocolVersion: "v1alpha2", GameId: "stardew-valley", SessionId: "same-session"}}})
	if err != nil && !errors.Is(err, io.EOF) {
		cancel()
		t.Fatal(err)
	}
	return stream, cancel
}
func readyHosted(t *testing.T, stream protocol.GameAgentGateway_ConnectClient) {
	t.Helper()
	message, err := stream.Recv()
	if err != nil || message.GetEnvironmentReady() == nil {
		t.Fatalf("ready: %v %v", message, err)
	}
	message, err = stream.Recv()
	if err != nil || message.GetCapabilityRequest() == nil {
		t.Fatalf("capabilities: %v %v", message, err)
	}
	if err = stream.Send(&protocol.AdapterMessage{MessageId: "same-capabilities", CorrelationId: message.MessageId, Payload: &protocol.AdapterMessage_Capabilities{Capabilities: &protocol.CapabilityList{Revision: 1}}}); err != nil {
		t.Fatal(err)
	}
}

func TestTLSGatewayAuthenticatesRoutesAndRevokesActiveStreams(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := f.user(t, "tls-first@example.com")
	b := f.user(t, "tls-second@example.com")
	client := f.gatewayClient(t)
	bad, cancelBad := openHosted(t, client, "invalid-secret", "device-one")
	defer cancelBad()
	if _, err := bad.Recv(); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthorized admitted: %v", err)
	}
	keyA, secretA, err := f.store.CreateKey(ctx, a.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, secretB, err := f.store.CreateKey(ctx, b.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	streamA, cancelA := openHosted(t, client, secretA, "device-one")
	defer cancelA()
	readyHosted(t, streamA)
	streamB, cancelB := openHosted(t, client, secretB, "device-two")
	defer cancelB()
	readyHosted(t, streamB)
	_, secondKey, err := f.store.CreateKey(ctx, a.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	second, secondCancel := openHosted(t, client, secondKey, "other-device")
	defer secondCancel()
	if _, err = second.Recv(); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("second device admitted: %v", err)
	}
	web, csrf := f.webSession(t, a.ID)
	r := f.request(t, "DELETE", "/api/me/keys/"+keyA.ID, "", web, csrf)
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if _, err = streamA.Recv(); err == nil {
		t.Fatal("revoked stream remained open")
	}
	if err = streamB.Send(&protocol.AdapterMessage{MessageId: "still-open"}); err != nil {
		t.Fatalf("other user affected: %v", err)
	}
	if _, _, err = f.store.AuthenticateKey(ctx, secretA, "device-one", false); !errors.Is(err, accounts.ErrCredentials) {
		t.Fatal("revoked credential readmitted")
	}
}

func TestModelProxyReservationSettlementAndCancellation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user(t, "model@example.com")
	_, secret, err := f.store.CreateKey(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := f.store.AuthenticateKey(ctx, secret, "device-one", true)
	if err != nil {
		t.Fatal(err)
	}
	a, err := f.workers.Ensure(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, cancel := context.WithCancel(ctx)
	defer cancel()
	f.s.connections[u.ID] = &connection{key: key, device: "device-one", cancel: cancel, done: make(chan struct{})}
	var calls atomic.Int32
	started, cancelled := make(chan struct{}), make(chan struct{})
	t.Setenv("WIA_TEST_PLATFORM_KEY", "platform-secret-test-only")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer platform-secret-test-only" {
			t.Error("upstream credential not platform-owned")
		}
		if strings.Contains(r.Header.Get("Authorization"), a.Token) {
			t.Error("worker credential leaked upstream")
		}
		if r.Header.Get("X-Request-ID") == "uncertain" {
			http.Error(w, "internal upstream detail", 500)
			return
		}
		if r.Header.Get("X-Request-ID") == "cancelled" {
			io.Copy(io.Discard, r.Body)
			close(started)
			<-r.Context().Done()
			close(cancelled)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":50,"completion_tokens":25,"total_tokens":75}}`)
	}))
	defer upstream.Close()
	f.s.Config.UpstreamURL = upstream.URL
	call := func(id, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://private/v1/chat/completions", strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":"hello"}],"stream":false,"max_tokens":512}`))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-WIA-Request-ID", id)
		rec := httptest.NewRecorder()
		f.s.ModelHandler().ServeHTTP(rec, r)
		return rec
	}
	if r := call("first", a.Token); r.Code != 200 {
		t.Fatalf("model %d %s", r.Code, r.Body.String())
	}
	if r := call("first", a.Token); r.Code != 429 || calls.Load() != 1 {
		t.Fatal("request replay reached upstream")
	}
	if r := call("uncertain", a.Token); r.Code != 503 || strings.Contains(r.Body.String(), "internal upstream detail") {
		t.Fatal("upstream failure leaked")
	}
	usage, err := f.store.Usage(ctx, u.ID, f.s.Config.DailyTokens)
	if err != nil || usage.Used != 75 || usage.Pending != int64(f.s.Config.ContextTokens) {
		t.Fatalf("usage: %+v %v", usage, err)
	}
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() { finished <- call("cancelled", a.Token) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upstream request did not start")
	}
	f.s.disconnectKey(u.ID, key)
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("revocation did not cancel the upstream request")
	}
	if result := <-finished; result.Code != 503 {
		t.Fatalf("cancelled call: %d", result.Code)
	}
	usage, err = f.store.Usage(ctx, u.ID, f.s.Config.DailyTokens)
	if err != nil || usage.Pending != int64(2*f.s.Config.ContextTokens) {
		t.Fatal("cancelled request released uncertain reservation")
	}
	if err = f.store.RevokeKey(ctx, u.ID, key); err != nil {
		t.Fatal(err)
	}
	if r := call("revoked", a.Token); r.Code != 403 || calls.Load() != 3 {
		t.Fatal("revoked account invoked model")
	}
	if r := call("foreign", "unknown-token"); r.Code != 401 {
		t.Fatal("unknown worker invoked model")
	}
}

type unavailableWorkers struct {
	Workers
	failure error
}

func (w unavailableWorkers) Ensure(context.Context, string) (Assignment, error) {
	return Assignment{}, w.failure
}

func TestRecoveryBlockAndTransientCapacityHaveDifferentClientFailures(t *testing.T) {
	for _, item := range []struct {
		failure error
		code    codes.Code
		message string
	}{
		{ErrWorkerDataMissing, codes.FailedPrecondition, "worker_recovery_blocked"},
		{ErrWorkerVolumeOwnership, codes.FailedPrecondition, "worker_recovery_blocked"},
		{ErrWorkerCapacity, codes.ResourceExhausted, "service_busy"},
		{errors.New("inventory operation failed"), codes.Unavailable, "worker_unavailable"},
	} {
		t.Run(item.message+item.failure.Error(), func(t *testing.T) {
			f := newFixture(t)
			u := f.user(t, "unavailable@example.com")
			f.s.Workers = unavailableWorkers{Workers: f.workers, failure: item.failure}
			_, key, err := f.store.CreateKey(context.Background(), u.ID, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			stream, cancel := openHosted(t, f.gatewayClient(t), key, "device-one")
			defer cancel()
			if _, err = stream.Recv(); status.Code(err) != item.code || status.Convert(err).Message() != item.message {
				t.Fatalf("gateway failure %v", err)
			}
			web, _ := f.webSession(t, u.ID)
			result := f.request(t, "GET", "/api/me/status", "", web, "")
			if result.Code != 503 || strings.Contains(result.Body.String(), "inventory operation") {
				t.Fatalf("status %d %s", result.Code, result.Body.String())
			}
			if workerRecoveryBlocked(item.failure) && !strings.Contains(result.Body.String(), "worker_recovery_blocked") {
				t.Fatal("recovery failure hidden")
			}
		})
	}
}

func TestDeploymentLockRejectsConcurrentController(t *testing.T) {
	path := filepath.Join(t.TempDir(), "controller.lock")
	unlock, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := Lock(path); err == nil {
		second()
		t.Fatal("concurrent controller admitted")
	}
	unlock()
	last, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	last()
}
