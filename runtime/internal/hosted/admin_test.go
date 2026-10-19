package hosted

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAdminPermissionsAndReadOnlyWorkerInspection(t *testing.T) {
	f := newFixture(t)
	admin := f.user(t, "operator@example.com")
	player := f.user(t, "player@example.com")
	cookie, csrf := f.webSession(t, admin.ID)
	if r := f.request(t, "GET", "/api/admin/users", "", cookie, ""); r.Code != 401 {
		t.Fatal("ordinary session saw admin data")
	}
	if err := f.store.SetAdmin(t.Context(), admin.Email, true); err != nil {
		t.Fatal(err)
	}
	cookie, err := f.store.NewAdminSession(t.Context(), admin.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	adminSession, err := f.store.Session(t.Context(), cookie)
	if err != nil {
		t.Fatal(err)
	}
	csrf = adminSession.CSRF
	if r := f.request(t, "POST", "/api/admin/invitations", "{}", cookie, ""); r.Code != 403 {
		t.Fatal("admin CSRF bypass")
	}
	r := f.request(t, "POST", "/api/admin/invitations", "{}", cookie, csrf)
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	var body struct {
		Items struct {
			Secret string `json:"secret"`
		}
	}
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil || body.Items.Secret == "" {
		t.Fatal("missing one-time invitation")
	}
	r = f.request(t, "GET", "/api/admin/invitations", "", cookie, "")
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if strings.Contains(r.Body.String(), body.Items.Secret) {
		t.Fatal("invitation plaintext persisted in list")
	}
	f.s.Workers = NewDockerWorkers(f.s.Config, f.store)
	r = f.request(t, "GET", "/api/admin/users/"+player.ID+"/runtime", "", cookie, "")
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	generation, initialized, err := f.store.WorkerIdentity(context.Background(), player.ID)
	if err != nil || generation != "" || initialized {
		t.Fatal("inspection allocated worker data")
	}
	r = f.request(t, "PATCH", "/api/admin/users/"+player.ID, `{"enabled":false}`, cookie, csrf)
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if err = f.store.SetAdmin(t.Context(), admin.Email, false); err != nil {
		t.Fatal(err)
	}
	if r = f.request(t, "GET", "/api/admin/users", "", cookie, ""); r.Code != 403 {
		t.Fatal("role change was cached")
	}
}

func TestWorkerInspectionChecksExistingResourcesWithoutMutating(t *testing.T) {
	f := newFixture(t)
	u := f.user(t, "inspect@example.com")
	manager := NewDockerWorkers(f.s.Config, f.store)
	generation, err := f.store.Generation(t.Context(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.MarkVolumeInitialized(t.Context(), u.ID); err != nil {
		t.Fatal(err)
	}
	engine := &dockerFixture{user: u.ID, generation: generation, volume: true, container: true, running: true, deployment: DeploymentID(f.store), image: f.s.Config.WorkerImage}
	manager.run = engine.run
	state, err := manager.Inspect(t.Context(), u.ID)
	if err != nil || state.State != "running" {
		t.Fatalf("inspect %+v %v", state, err)
	}
	engine.volume = false
	state, err = manager.Inspect(t.Context(), u.ID)
	if err != nil || state.State != "recovery_blocked" {
		t.Fatalf("missing volume %+v %v", state, err)
	}
	if engine.created != 0 || engine.started != 0 {
		t.Fatal("inspection mutated resources")
	}
	for _, args := range engine.commands {
		if args[0] != "ps" && args[0] != "inspect" && !(args[0] == "volume" && (args[1] == "ls" || args[1] == "inspect")) {
			t.Fatal("mutation command", args[0])
		}
	}
}
