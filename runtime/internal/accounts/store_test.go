package accounts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func accountFixture(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "accounts.db")
	s, err := Open(path, []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}
func registerFixture(t *testing.T, s *Store, email string) User {
	t.Helper()
	ctx := context.Background()
	invite, err := s.Invite(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.Register(ctx, email, "a long test password", invite, proofFixture(t, s, email, "register", invite))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestInvitationIdentityAndCredentialsPersist(t *testing.T) {
	s, path := accountFixture(t)
	ctx := context.Background()
	invite, err := s.Invite(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.Register(ctx, "Player@Example.com", "a long test password", invite, proofFixture(t, s, "Player@Example.com", "register", invite))
	if err != nil {
		t.Fatal(err)
	}
	if !safeUserID(u.ID) || u.Email != "player@example.com" {
		t.Fatalf("identity: %+v", u)
	}
	if _, err = s.Register(ctx, "other@example.com", "a long test password", invite, Proof{}); !errors.Is(err, ErrCredentials) {
		t.Fatalf("invite reused: %v", err)
	}
	if _, err = s.Login(ctx, "player@example.com", "wrong"); !errors.Is(err, ErrCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err = s.Login(ctx, "missing@example.com", "wrong"); !errors.Is(err, ErrCredentials) {
		t.Fatalf("unknown user: %v", err)
	}
	session, err := s.NewSession(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	key, secret, err := s.CreateKey(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AuthenticateKey(ctx, session, "device-one", true); !errors.Is(err, ErrCredentials) {
		t.Fatalf("web credential accepted by mod: %v", err)
	}
	if _, err = s.Session(ctx, secret); !errors.Is(err, ErrCredentials) {
		t.Fatalf("mod credential accepted by web: %v", err)
	}
	if _, _, err = s.AuthenticateKey(ctx, secret, "device-one", true); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AuthenticateKey(ctx, secret, "device-two", true); !errors.Is(err, ErrCredentials) {
		t.Fatalf("device changed: %v", err)
	}
	s.Close()
	s2, err := Open(path, []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	login, err := s2.Login(ctx, "player@example.com", "a long test password")
	if err != nil || login.ID != u.ID {
		t.Fatalf("restart identity: %v %+v", err, login)
	}
	current, err := s2.Session(ctx, session)
	if err != nil || current.User.ID != u.ID || current.CSRF == "" {
		t.Fatalf("restart session: %v", err)
	}
	if err = s2.RevokeKey(ctx, u.ID, key.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s2.AuthenticateKey(ctx, secret, "device-one", false); !errors.Is(err, ErrCredentials) {
		t.Fatalf("revoked key accepted: %v", err)
	}
	_, replacement, err := s2.CreateKey(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := s2.AuthenticateKey(ctx, replacement, "device-one", true)
	if err != nil || owner != u.ID {
		t.Fatalf("rotation changed owner: %v", err)
	}
	var stored string
	if err = s2.db.QueryRow("SELECT password FROM users WHERE id=?", u.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "a long test password") {
		t.Fatal("plaintext password persisted")
	}
}
func safeUserID(value string) bool { return len(value) == 36 && strings.HasPrefix(value, "usr_") }

func TestPreferencesAndKeysAreOwnedByAuthenticatedUser(t *testing.T) {
	s, _ := accountFixture(t)
	ctx := context.Background()
	a := registerFixture(t, s, "first@example.com")
	b := registerFixture(t, s, "second@example.com")
	key, secret, err := s.CreateKey(ctx, a.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeKey(ctx, b.ID, key.ID); err != nil {
		t.Fatal(err)
	}
	if owner, _, err := s.AuthenticateKey(ctx, secret, "device-one", true); err != nil || owner != a.ID {
		t.Fatal("foreign revoke affected owner")
	}
	p, err := s.ReadPreferences(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	p.Name = "小雨"
	saved, err := s.SavePreferences(ctx, a.ID, p)
	if err != nil || saved.Version != 1 {
		t.Fatal(err)
	}
	if _, err = s.SavePreferences(ctx, a.ID, p); !errors.Is(err, ErrConflict) {
		t.Fatal("stale update accepted")
	}
	other, err := s.ReadPreferences(ctx, b.ID)
	if err != nil || other.Name != "" {
		t.Fatal("preferences crossed owners")
	}
	if err = s.ResetPassword(ctx, a.Email, "replacement password"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AuthenticateKey(ctx, secret, "device-one", false); !errors.Is(err, ErrCredentials) {
		t.Fatal("password reset kept device credential")
	}
	if _, err = s.Login(ctx, a.Email, "replacement password"); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaIsAtomicDurableAndUnknownCallsStayReserved(t *testing.T) {
	s, path := accountFixture(t)
	ctx := context.Background()
	u := registerFixture(t, s, "quota@example.com")
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.Reserve(ctx, u.ID, NewID("call_"), 100, 500, 0, "test") }()
	}
	wg.Wait()
	close(results)
	count := 0
	for err := range results {
		if err == nil {
			count++
		} else if !errors.Is(err, ErrLimit) {
			t.Fatal(err)
		}
	}
	if count != 5 {
		t.Fatalf("admitted %d", count)
	}
	u1, err := s.Usage(ctx, u.ID, 500)
	if err != nil || u1.Pending != 500 || u1.Remaining != 0 {
		t.Fatalf("usage %+v %v", u1, err)
	}
	s.Close()
	s2, err := Open(path, []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if err = s2.Reserve(ctx, u.ID, "after-restart", 100, 500, 0, "test"); !errors.Is(err, ErrLimit) {
		t.Fatalf("restart cleared quota: %v", err)
	}
	if err = s2.Reserve(ctx, u.ID, "idempotent", 100, 1000, 0, "test"); err != nil {
		t.Fatal(err)
	}
	if err = s2.Reserve(ctx, u.ID, "idempotent", 100, 1000, 0, "test"); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate request accepted")
	}
	if err = s2.Settle(ctx, u.ID, "idempotent", 25, tokenPointer(20), tokenPointer(5)); err != nil {
		t.Fatal(err)
	}
	if err = s2.Settle(ctx, u.ID, "idempotent", 0, nil, nil); err == nil {
		t.Fatal("settlement replayed")
	}
	usage, err := s2.Usage(ctx, u.ID, 1000)
	if err != nil || usage.Used != 25 || usage.Pending != 500 {
		t.Fatalf("settlement %+v %v", usage, err)
	}
}

func TestBackupRestoresAccountAndQuota(t *testing.T) {
	s, _ := accountFixture(t)
	ctx := context.Background()
	u := registerFixture(t, s, "backup@example.com")
	if err := s.Reserve(ctx, u.ID, "uncertain", 100, 500, 0, "test"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "restored.db")
	if err := s.Backup(ctx, path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(path, []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	login, err := restored.Login(ctx, u.Email, "a long test password")
	if err != nil || login.ID != u.ID {
		t.Fatal("restore changed identity")
	}
	usage, err := restored.Usage(ctx, u.ID, 500)
	if err != nil || usage.Pending != 100 {
		t.Fatal("restore lost reservation")
	}
}
