package accounts

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func proofFixture(t *testing.T, s *Store, email, purpose, invite string) Proof {
	t.Helper()
	c, code, err := s.BeginChallenge(t.Context(), email, purpose, invite)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ActivateChallenge(t.Context(), c.ID); err != nil {
		t.Fatal(err)
	}
	return Proof{ChallengeID: c.ID, Code: code}
}

func TestVerificationLifecycleAndRestart(t *testing.T) {
	s, path := accountFixture(t)
	ctx := t.Context()
	invite, err := s.Invite(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c, code, err := s.BeginChallenge(ctx, "new@example.com", "register", invite)
	if err != nil {
		t.Fatal(err)
	}
	proof := Proof{c.ID, code}
	if _, err = s.Register(ctx, "new@example.com", "a long test password", invite, proof); !errors.Is(err, ErrVerification) {
		t.Fatalf("pending accepted: %v", err)
	}
	if err = s.ActivateChallenge(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.BeginChallenge(ctx, c.Email, "register", invite); !errors.Is(err, ErrLimit) {
		t.Fatalf("resend allowed: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err = s.Register(ctx, c.Email, "a long test password", invite, Proof{c.ID, "wrong"}); !errors.Is(err, ErrVerification) {
			t.Fatal(err)
		}
	}
	s.Close()
	reopened, err := Open(path, []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err = reopened.Register(ctx, c.Email, "a long test password", invite, proof); !errors.Is(err, ErrVerification) {
		t.Fatalf("restart cleared attempts: %v", err)
	}
	if _, err = reopened.db.Exec("UPDATE mail_challenges SET created=created-61 WHERE id=?", c.ID); err != nil {
		t.Fatal(err)
	}
	replacement := proofFixture(t, reopened, c.Email, "register", invite)
	u, err := reopened.Register(ctx, c.Email, "a long test password", invite, replacement)
	if err != nil || u.VerifiedAt == 0 {
		t.Fatalf("register: %+v %v", u, err)
	}
	if _, err = reopened.Register(ctx, "different@example.com", "a long test password", invite, replacement); !errors.Is(err, ErrCredentials) {
		t.Fatal(err)
	}
}

func TestRegistrationRollsBackProofAndInvitationTogether(t *testing.T) {
	s, _ := accountFixture(t)
	ctx := t.Context()
	invite, err := s.Invite(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	proof := proofFixture(t, s, "atomic@example.com", "register", invite)
	if _, err = s.db.Exec("CREATE TRIGGER refuse_preferences BEFORE INSERT ON preferences BEGIN SELECT RAISE(ABORT,'test interruption'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Register(ctx, "atomic@example.com", "a long test password", invite, proof); err == nil {
		t.Fatal("interrupted registration succeeded")
	}
	if _, err = s.db.Exec("DROP TRIGGER refuse_preferences"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.Register(ctx, "atomic@example.com", "a long test password", invite, proof)
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for e := range results {
		if e == nil {
			successes++
		} else if !errors.Is(e, ErrConflict) && !errors.Is(e, ErrCredentials) && !errors.Is(e, ErrVerification) {
			t.Fatal(e)
		}
	}
	if successes != 1 {
		t.Fatalf("successful registrations: %d", successes)
	}
}

func TestExpiredFailedAndSupersededChallenges(t *testing.T) {
	s, _ := accountFixture(t)
	ctx := t.Context()
	invite, err := s.Invite(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c, code, err := s.BeginChallenge(ctx, "expiry@example.com", "register", invite)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FailChallenge(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.ActivateChallenge(ctx, c.ID); !errors.Is(err, ErrVerification) {
		t.Fatal("failed send activated")
	}
	if _, err = s.db.Exec("UPDATE mail_challenges SET created=created-61 WHERE id=?", c.ID); err != nil {
		t.Fatal(err)
	}
	old := proofFixture(t, s, c.Email, "register", invite)
	if _, err = s.db.Exec("UPDATE mail_challenges SET created=created-61 WHERE id=?", old.ChallengeID); err != nil {
		t.Fatal(err)
	}
	latest := proofFixture(t, s, c.Email, "register", invite)
	for _, p := range []Proof{{c.ID, code}, old} {
		if _, err = s.Register(ctx, c.Email, "a long test password", invite, p); !errors.Is(err, ErrVerification) {
			t.Fatal("inactive proof accepted")
		}
	}
	if _, err = s.db.Exec("UPDATE mail_challenges SET expires=0 WHERE id=?", latest.ChallengeID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Register(ctx, c.Email, "a long test password", invite, latest); !errors.Is(err, ErrVerification) {
		t.Fatal("expired proof accepted")
	}
}

func TestResetIsOneUseAndRevokesCredentials(t *testing.T) {
	s, _ := accountFixture(t)
	ctx := t.Context()
	u := registerFixture(t, s, "reset@example.com")
	session, err := s.NewSession(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := s.CreateKey(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	proof := proofFixture(t, s, u.Email, "reset", "")
	owner, err := s.ResetWithProof(ctx, proof, "replacement password")
	if err != nil || owner != u.ID {
		t.Fatal(err)
	}
	if _, err = s.ResetWithProof(ctx, proof, "another replacement"); !errors.Is(err, ErrVerification) {
		t.Fatalf("reset replayed: %v", err)
	}
	if _, err = s.Session(ctx, session); !errors.Is(err, ErrCredentials) {
		t.Fatal("old session accepted")
	}
	if _, _, err = s.AuthenticateKey(ctx, key, "device-one", true); !errors.Is(err, ErrCredentials) {
		t.Fatal("old key accepted")
	}
	login, err := s.Login(ctx, u.Email, "replacement password")
	if err != nil || login.ID != u.ID {
		t.Fatal("identity changed")
	}
}

func TestLegacyMigrationPreservesOwnershipAndRequiresVerification(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE users(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,password TEXT NOT NULL,created INTEGER NOT NULL,enabled INTEGER NOT NULL DEFAULT 1);
CREATE TABLE invitations(digest TEXT PRIMARY KEY,expires INTEGER NOT NULL,used_by TEXT REFERENCES users(id));
INSERT INTO users(id,email,password,created) VALUES('usr_legacy','legacy@example.com','legacy-password',100);`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path, []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	backups, err := filepath.Glob(path + ".pre-v1-*.sqlite")
	if err != nil || len(backups) != 1 {
		t.Fatal("migration backup missing")
	}
	var verified int64
	if err = s.db.QueryRow("SELECT verified FROM users WHERE id='usr_legacy'").Scan(&verified); err != nil || verified != 0 {
		t.Fatal("legacy account incorrectly verified")
	}
	if _, _, err = s.CreateKey(ctx, "usr_legacy", time.Hour); !errors.Is(err, ErrCredentials) {
		t.Fatal("unverified key issued")
	}
	proof := proofFixture(t, s, "legacy@example.com", "verify", "")
	if err = s.VerifyEmail(ctx, "usr_legacy", proof); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.CreateKey(ctx, "usr_legacy", time.Hour); err != nil {
		t.Fatal(err)
	}
}
