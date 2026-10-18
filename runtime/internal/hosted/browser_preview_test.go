package hosted

import (
	"context"
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
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	f.s.Config.Origin = "http://localhost:" + strconv.Itoa(port)
	handler := f.s.Handler()
	faultFile := os.Getenv("WIA_HOSTED_PREVIEW_FAULT_FILE")
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
