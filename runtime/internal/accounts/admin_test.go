package accounts

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAdminInvitationsAndAccountLifecycle(t *testing.T) {
	s, _ := accountFixture(t)
	ctx := t.Context()
	admin := registerFixture(t, s, "admin@example.com")
	player := registerFixture(t, s, "player@example.com")
	if _, _, err := s.NewInvitation(ctx, player.ID, time.Hour, ""); !errors.Is(err, ErrCredentials) {
		t.Fatal("ordinary user created invitation")
	}
	if err := s.SetAdmin(ctx, admin.Email, true); err != nil {
		t.Fatal(err)
	}
	inv, secret, err := s.NewInvitation(ctx, admin.ID, time.Hour, "")
	if err != nil || secret == "" {
		t.Fatal(err)
	}
	if err = s.RevokeInvitation(ctx, admin.ID, inv.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.BeginChallenge(ctx, "blocked@example.com", "register", secret); !errors.Is(err, ErrCredentials) {
		t.Fatal("revoked invite used")
	}
	session, err := s.NewSession(ctx, player.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := s.CreateKey(ctx, player.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := s.Audit(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Actor != player.ID || !strings.HasPrefix(entries[0].Target, "key_") {
		t.Fatal("key audit lost actual actor/target")
	}
	if err = s.SetEnabled(ctx, admin.ID, admin.ID, false); !errors.Is(err, ErrInput) {
		t.Fatal("admin disabled self")
	}
	if err = s.SetEnabled(ctx, admin.ID, player.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Session(ctx, session); !errors.Is(err, ErrCredentials) {
		t.Fatal("disabled session accepted")
	}
	if err = s.SetEnabled(ctx, admin.ID, player.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AuthenticateKey(ctx, key, "device-one", true); !errors.Is(err, ErrCredentials) {
		t.Fatal("enable restored revoked key")
	}
	if err = s.SetAdmin(ctx, admin.Email, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.NewInvitation(ctx, admin.ID, time.Hour, ""); !errors.Is(err, ErrCredentials) {
		t.Fatal("revoked admin retained authority")
	}
	records, err := s.Audit(ctx, 0)
	if err != nil || len(records) == 0 {
		t.Fatal(err)
	}
	found := false
	for _, entry := range records {
		if entry.Event == "account_disabled" && entry.Actor == admin.ID && entry.Target == player.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("admin action attribution missing")
	}
}
