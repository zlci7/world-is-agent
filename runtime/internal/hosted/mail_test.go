package hosted

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"gameagent/runtime/internal/accounts"
)

type testMailer struct {
	mu       sync.Mutex
	messages []MailMessage
	err      error
}

func (m *testMailer) Send(_ context.Context, message MailMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.messages = append(m.messages, message)
	return nil
}
func (f *fixture) proof(t *testing.T, email, purpose, invite string) accounts.Proof {
	t.Helper()
	c, code, err := f.store.BeginChallenge(t.Context(), email, purpose, invite)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.ActivateChallenge(t.Context(), c.ID); err != nil {
		t.Fatal(err)
	}
	return accounts.Proof{ChallengeID: c.ID, Code: code}
}

func TestMailFailuresDoNotReturnCredentials(t *testing.T) {
	f := newFixture(t)
	f.s.mailer = &testMailer{err: errors.New("private SMTP credential")}
	invite, err := f.store.Invite(t.Context(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r := f.request(t, "POST", "/api/registration-code", `{"email":"mail@example.com","invitation":"`+invite+`"}`, "", "")
	if r.Code != 503 || r.Body.String() == "" {
		t.Fatal(r.Body.String())
	}
	if r = f.request(t, "POST", "/api/password-resets", `{"email":"unknown@example.com"}`, "", ""); r.Code != 202 {
		t.Fatal(r.Body.String())
	}
}

func TestHTTPMailRegistrationAndPasswordReset(t *testing.T) {
	f := newFixture(t)
	mailer := &testMailer{}
	f.s.mailer = mailer
	invite, err := f.store.Invite(t.Context(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"email":"register@example.com","invitation":"` + invite + `"}`
	r := f.request(t, "POST", "/api/registration-code", payload, "", "")
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	var c accounts.Challenge
	if err = json.Unmarshal(r.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	code := regexp.MustCompile(`[0-9]{6}`).FindString(mailer.messages[0].Body)
	if code == "" || strings.Contains(r.Body.String(), code) {
		t.Fatal("verification secret returned by API")
	}
	body, _ := json.Marshal(map[string]string{"email": "register@example.com", "invitation": invite, "password": "a long test password", "challenge_id": c.ID, "code": code})
	r = f.request(t, "POST", "/api/invitations/redeem", string(body), "", "")
	if r.Code != 201 {
		t.Fatal(r.Body.String())
	}
	var session accounts.Session
	if err = json.Unmarshal(r.Body.Bytes(), &session); err != nil || session.User.VerifiedAt == 0 {
		t.Fatal("registration not verified")
	}
	cookie := r.Result().Cookies()[0].Value
	r = f.request(t, "POST", "/api/password-resets", `{"email":"register@example.com"}`, "", "")
	if r.Code != 202 {
		t.Fatal(r.Body.String())
	}
	link := regexp.MustCompile(`token=([^\s]+)`).FindStringSubmatch(mailer.messages[1].Body)
	if len(link) != 2 {
		t.Fatal("reset link absent")
	}
	if r = f.request(t, "GET", "/", "", "", ""); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	reset, _ := json.Marshal(map[string]string{"token": link[1], "password": "replacement password"})
	r = f.request(t, "POST", "/api/password-resets/complete", string(reset), "", "")
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if r = f.request(t, "GET", "/api/me", "", cookie, ""); r.Code != 401 {
		t.Fatal("old web session survived")
	}
	if r = f.request(t, "POST", "/api/password-resets/complete", string(reset), "", ""); r.Code != 400 {
		t.Fatal("reset replay succeeded")
	}
}

func TestLegacyVerificationRestrictsSessionAndRotatesIt(t *testing.T) {
	f := newFixture(t)
	u := f.user(t, "legacy@example.com")
	cookie, csrf := f.webSession(t, u.ID)
	db, err := sql.Open("sqlite", filepath.Join(f.workers.root, "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("UPDATE users SET verified=0 WHERE id=?", u.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/me/keys", "/api/me/status", "/api/admin/users"} {
		if r := f.request(t, "GET", path, "", cookie, ""); r.Code != 403 {
			t.Fatal("unverified session escaped", path)
		}
	}
	mailer := &testMailer{}
	f.s.mailer = mailer
	r := f.request(t, "POST", "/api/me/email-verification", "{}", cookie, csrf)
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	var c accounts.Challenge
	if err = json.Unmarshal(r.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(accounts.Proof{ChallengeID: c.ID, Code: regexp.MustCompile(`[0-9]{6}`).FindString(mailer.messages[0].Body)})
	r = f.request(t, "POST", "/api/me/email-verification/confirm", string(body), cookie, csrf)
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if len(r.Result().Cookies()) != 1 || r.Result().Cookies()[0].Value == cookie {
		t.Fatal("verification did not rotate session")
	}
	if r = f.request(t, "GET", "/api/me", "", cookie, ""); r.Code != 401 {
		t.Fatal("old verification session survived")
	}
}
