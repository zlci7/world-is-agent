package hosted

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gameagent/console"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/task"
)

// This opt-in fixture exercises the real account HTTP surface and built UI with
// isolated local workers. It never connects to a game or paid model endpoint.
func TestHostedBrowserPreview(t *testing.T) {
	if os.Getenv("WIA_HOSTED_BROWSER_PREVIEW") != "1" {
		t.Skip("opt-in local browser fixture")
	}
	f := newFixture(t)
	f.workers.preview = true
	f.s.Assets = console.Assets()
	f.s.Config.MinAdapterVersion = "0.1.3"
	mailbox := &testMailer{}
	f.s.mailer = mailbox
	admin := f.user(t, "operator@example.test")
	if err := f.store.SetAdmin(t.Context(), admin.Email, true); err != nil {
		t.Fatal(err)
	}
	player := f.user(t, "player@example.test")
	for _, id := range []string{"preview-settled", "preview-failed", "preview-uncertain"} {
		if err := f.store.Reserve(t.Context(), player.ID, id, 1000, f.s.Config.DailyTokens, f.s.Config.GlobalTokenLimit(), "test-model"); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.Settle(t.Context(), player.ID, "preview-settled", 250, tokenPointer(200), tokenPointer(50)); err != nil {
		t.Fatal(err)
	}
	if err := f.store.MarkCallOutcome(t.Context(), player.ID, "preview-failed", "failed", "upstream_failed"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.MarkCallOutcome(t.Context(), player.ID, "preview-uncertain", "uncertain", "request_interrupted"); err != nil {
		t.Fatal(err)
	}
	legacy := f.user(t, "legacy@example.test")
	db, err := sql.Open("sqlite", filepath.Join(f.workers.root, "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE users SET verified=0 WHERE id=?", legacy.ID); err != nil {
		t.Fatal(err)
	}
	db.Close()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	f.s.Config.Origin = "http://localhost:" + strconv.Itoa(port)
	handler := f.s.Handler()
	faultFile := os.Getenv("WIA_HOSTED_PREVIEW_FAULT_FILE")
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/__preview/mail" {
			mailbox.mu.Lock()
			defer mailbox.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(mailbox.messages)
			return
		}
		if faultFile != "" && r.Method == http.MethodGet {
			data, _ := os.ReadFile(faultFile)
			for _, path := range strings.Fields(string(data)) {
				if r.URL.Path == path {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusServiceUnavailable)
					fmt.Fprint(w, `{"error":{"code":"preview_read_unavailable","message":"暂时无法读取，请重试"}}`)
					return
				}
			}
		}
		handler.ServeHTTP(w, r)
	})}
	defer server.Close()
	go server.Serve(l)
	invite, err := f.store.Invite(t.Context(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("PREVIEW_URL=%s\nPREVIEW_INVITATION=%s\n", f.s.Config.Origin, invite)
	stop := os.Getenv("WIA_HOSTED_PREVIEW_STOP_FILE")
	deadline := time.NewTimer(20 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			return
		case <-ticker.C:
			if stop != "" {
				if _, err := os.Stat(stop); err == nil {
					return
				}
			}
		}
	}
}

func seedPreviewWorld(ctx context.Context, root string) error {
	store, err := task.OpenSQLiteStore(ctx, task.StoreOptions{Path: filepath.Join(root, agent.DefaultTaskDBPath)})
	if err != nil {
		return err
	}
	defer store.Close()
	service := task.NewService(store)
	world := task.WorldKey{GameID: "stardew-valley", WorldID: "preview-world"}
	clock := task.Clock{ID: "preview.minute.v1", Tick: 100, Sequence: 1}
	head, err := service.ActivateWorld(ctx, world, "preview-run", clock, task.CheckpointRef{Status: "absent", World: world})
	if err != nil {
		return err
	}
	source := task.SourceRef{Kind: "internal", EventID: "preview-event", TurnID: "preview-turn", CallID: "preview-call"}
	_, err = service.Create(ctx, task.ExecutionContext{Owner: session.AgentSessionKey{GameID: world.GameID, WorldID: world.WorldID, EntityID: "Abigail"}, Binding: head.Binding, Clock: clock, Source: source}, task.TaskSpec{Instruction: "明天下午在镇上见面（本地测试）", ClockID: clock.ID, WakeAt: 200, DeadlineAt: 300, ResultContract: "authoritative_evidence", Source: source}, task.Admission{})
	return err
}
