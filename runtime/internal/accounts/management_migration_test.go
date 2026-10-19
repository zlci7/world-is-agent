package accounts

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionOneUpgradePreservesIdentityLedgerAndUnknownMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
 CREATE TABLE users(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,password TEXT NOT NULL,created INTEGER NOT NULL,enabled INTEGER NOT NULL DEFAULT 1,verified INTEGER NOT NULL DEFAULT 0,admin INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE invitations(digest TEXT PRIMARY KEY,expires INTEGER NOT NULL,used_by TEXT,id TEXT NOT NULL,created INTEGER NOT NULL DEFAULT 0,revoked INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE audit(id INTEGER PRIMARY KEY,created INTEGER NOT NULL,user_id TEXT NOT NULL,event TEXT NOT NULL,actor TEXT NOT NULL DEFAULT 'operator',target TEXT NOT NULL DEFAULT '',result TEXT NOT NULL DEFAULT 'success');
 CREATE TABLE mail_challenges(id TEXT PRIMARY KEY,email TEXT NOT NULL,purpose TEXT NOT NULL,digest TEXT NOT NULL,created INTEGER NOT NULL,expires INTEGER NOT NULL,attempts INTEGER NOT NULL DEFAULT 0,state TEXT NOT NULL);
 CREATE TABLE model_calls(id TEXT PRIMARY KEY,user_id TEXT NOT NULL,period TEXT NOT NULL,reserved INTEGER NOT NULL,charged INTEGER,status TEXT NOT NULL);
 INSERT INTO users VALUES('usr_legacy','legacy@example.test','old-password',1,1,1,0);
 INSERT INTO invitations VALUES('old-digest',9999999999,'usr_legacy','inv_legacy',1,0);
 INSERT INTO model_calls VALUES('old-call','usr_legacy','2026-10-18',100,NULL,'pending');
 PRAGMA user_version=1;`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(path, []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backups, err := filepath.Glob(path + ".pre-v2-*.sqlite")
	if err != nil || len(backups) != 1 {
		t.Fatal(backups, err)
	}
	var version int
	if err = store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatal(version, err)
	}
	users, err := store.Users(t.Context(), 0, "legacy", 1000)
	if err != nil || len(users) != 1 || users[0].ID != "usr_legacy" || users[0].InvitationID != "inv_legacy" {
		t.Fatal(users, err)
	}
	report, err := store.UsageReport(t.Context(), UsageFilter{From: "2026-10-18", To: "2026-10-18"})
	if err != nil || report.Totals.Pending != 100 || report.Calls[0].Created != 0 || report.Calls[0].Prompt != nil {
		t.Fatal(report, err)
	}
}

func TestQuotaChangeRollsBackWhenAuditFails(t *testing.T) {
	s, _ := accountFixture(t)
	admin := registerFixture(t, s, "admin@example.test")
	user := registerFixture(t, s, "player@example.test")
	if err := s.SetAdmin(t.Context(), admin.Email, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("CREATE TRIGGER fail_limit_audit BEFORE INSERT ON audit WHEN NEW.event='daily_limit_changed' BEGIN SELECT RAISE(ABORT,'test audit failure'); END"); err != nil {
		t.Fatal(err)
	}
	limit := int64(200)
	if err := s.SetDailyLimit(t.Context(), admin.ID, user.ID, &limit, "test"); err == nil {
		t.Fatal("audit failure ignored")
	}
	usage, err := s.Usage(t.Context(), user.ID, 1000)
	if err != nil || usage.Limit != 1000 {
		t.Fatal("partial quota update", usage, err)
	}
}
