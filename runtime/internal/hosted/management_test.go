package hosted

import (
	"database/sql"
	"encoding/json"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestManagementSessionIsolationAndRoutes(t *testing.T) {
	f := newFixture(t)
	admin := f.user(t, "owner@example.test")
	player := f.user(t, "player@example.test")
	if err := f.store.SetAdmin(t.Context(), admin.Email, true); err != nil {
		t.Fatal(err)
	}
	playerSecret, _ := f.webSession(t, admin.ID)
	if r := f.request(t, "GET", "/api/admin/overview", "", playerSecret, ""); r.Code != 401 {
		t.Fatal("player session authorized admin")
	}
	r := f.request(t, "POST", "/api/admin/session", `{"email":"owner@example.test","password":"a long test password"}`, "", "")
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	cookie := r.Result().Cookies()[0]
	if cookie.Name != adminCookie || cookie.Path != "/api/admin" || !cookie.Secure || !cookie.HttpOnly {
		t.Fatal(cookie)
	}
	secret := cookie.Value
	var session struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if r = f.request(t, "GET", "/api/me", "", secret, ""); r.Code != 401 {
		t.Fatal("admin session authorized player API")
	}
	if r = f.request(t, "GET", "/api/admin/overview", "", secret, ""); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if generation, initialized, err := f.store.WorkerIdentity(t.Context(), admin.ID); err != nil || generation != "" || initialized {
		t.Fatal("admin read started worker")
	}
	for _, path := range []string{"/api/admin/usage?from=2026-01-01&to=2026-12-31", "/api/admin/usage?status=other", "/api/admin/usage?user=../another", "/api/admin/usage?from=bad"} {
		if r = f.request(t, "GET", path, "", secret, ""); r.Code != 400 {
			t.Fatal(path, r.Body.String())
		}
	}
	if r = f.request(t, "PUT", "/api/admin/users/"+player.ID+"/limit", `{"daily_limit":50,"reason":"试用额度"}`, secret, ""); r.Code != 403 {
		t.Fatal("CSRF bypass")
	}
	if r = f.request(t, "PUT", "/api/admin/users/"+player.ID+"/limit", `{"daily_limit":50,"reason":"试用额度"}`, secret, session.CSRF); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if r = f.request(t, "GET", "/api/admin/users?q=player%40example.test", "", secret, ""); r.Code != 200 || !strings.Contains(r.Body.String(), `"effective_limit":50`) {
		t.Fatal(r.Body.String())
	}
	if r = f.request(t, "POST", "/api/admin/invitations", `{"hours":24,"note":"受邀玩家"}`, secret, session.CSRF); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if r = f.request(t, "GET", "/api/admin/invitations", "", secret, ""); !strings.Contains(r.Body.String(), "受邀玩家") {
		t.Fatal(r.Body.String())
	}
	f.s.Assets = fstest.MapFS{"admin.html": &fstest.MapFile{Data: []byte("admin-entry"), Mode: fs.FileMode(0644)}, "hosted.html": &fstest.MapFile{Data: []byte("player-entry")}}
	for _, path := range []string{"/admin", "/admin/login", "/admin/users", "/admin/usage"} {
		if r = f.request(t, "GET", path, "", "", ""); r.Code != 200 || !strings.Contains(r.Body.String(), "admin-entry") {
			t.Fatal(path, r.Body.String())
		}
	}
	if err := f.store.SetAdmin(t.Context(), admin.Email, false); err != nil {
		t.Fatal(err)
	}
	if r = f.request(t, "GET", "/api/admin/session", "", secret, ""); r.Code != 403 {
		t.Fatal("revoked administrator retained session access")
	}
}

func TestUsageExportMatchesFilterAndNeutralizesSpreadsheetFormula(t *testing.T) {
	f := newFixture(t)
	admin := f.user(t, "admin@example.test")
	user := f.user(t, "=formula@example.test")
	if err := f.store.SetAdmin(t.Context(), admin.Email, true); err != nil {
		t.Fatal(err)
	}
	secret, err := f.store.NewAdminSession(t.Context(), admin.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.Reserve(t.Context(), user.ID, "test-export", 100, 1000, 1000, "test-model"); err != nil {
		t.Fatal(err)
	}
	if err = f.store.Settle(t.Context(), user.ID, "test-export", 25, tokenPointer(20), tokenPointer(5)); err != nil {
		t.Fatal(err)
	}
	r := f.request(t, "GET", "/api/admin/usage/export?user="+user.ID+"&status=settled", "", secret, "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "'=formula@example.test") || !strings.Contains(r.Body.String(), "test-export") || !strings.Contains(r.Header().Get("Content-Type"), "text/csv") {
		t.Fatal(r.Body.String())
	}
	r = f.request(t, "GET", "/api/admin/usage/export?status=uncertain", "", secret, "")
	if r.Code != 200 || strings.Contains(r.Body.String(), "test-export") {
		t.Fatal("export ignored filter")
	}
}

func tokenPointer(v int64) *int64 { return &v }

func TestExportRejectsOversizedResultBeforeSendingCSV(t *testing.T) {
	f := newFixture(t)
	admin := f.user(t, "export-owner@example.test")
	if err := f.store.SetAdmin(t.Context(), admin.Email, true); err != nil {
		t.Fatal(err)
	}
	secret, err := f.store.NewAdminSession(t.Context(), admin.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(f.workers.root, "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`WITH RECURSIVE n(v) AS (SELECT 1 UNION ALL SELECT v+1 FROM n WHERE v<5001) INSERT INTO model_calls(id,user_id,period,reserved,status) SELECT 'export_'||v,?,?,1,'uncertain' FROM n`, admin.ID, time.Now().UTC().Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	r := f.request(t, "GET", "/api/admin/usage/export", "", secret, "")
	if r.Code != 413 || !strings.Contains(r.Body.String(), "5000") || strings.Contains(r.Header().Get("Content-Type"), "text/csv") {
		t.Fatal(r.Code, r.Body.String())
	}
}
