package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

type Invitation struct {
	ID         string `json:"invitation_id"`
	CreatedAt  int64  `json:"created_at"`
	ExpiresAt  int64  `json:"expires_at"`
	UsedBy     string `json:"used_by"`
	Status     string `json:"status"`
	Note       string `json:"note"`
	Email      string `json:"email"`
	RedeemedAt int64  `json:"redeemed_at"`
}
type ManagedUser struct {
	User
	Enabled        bool   `json:"enabled"`
	DailyLimit     *int64 `json:"daily_limit"`
	EffectiveLimit int64  `json:"effective_limit"`
	InvitationID   string `json:"invitation_id"`
	InvitationNote string `json:"invitation_note"`
	Used           int64  `json:"used"`
	Pending        int64  `json:"pending"`
	TotalUsed      int64  `json:"total_used"`
}
type AuditEntry struct {
	ID        int64  `json:"id"`
	CreatedAt int64  `json:"created_at"`
	Actor     string `json:"actor"`
	Target    string `json:"target"`
	Event     string `json:"event"`
	Result    string `json:"result"`
	Detail    string `json:"detail"`
}

func requireAdmin(ctx context.Context, tx *sql.Tx, actor string) error {
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE id=? AND enabled=1 AND verified>0 AND admin=1", actor).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return ErrCredentials
	}
	return nil
}

func (s *Store) SetAdmin(ctx context.Context, email string, enabled bool) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var user string
	if err = tx.QueryRowContext(ctx, "SELECT id FROM users WHERE email=? AND enabled=1 AND verified>0", email).Scan(&user); errors.Is(err, sql.ErrNoRows) {
		return ErrCredentials
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE users SET admin=? WHERE id=?", enabled, user); err != nil {
		return err
	}
	event := "admin_revoked"
	if enabled {
		event = "admin_granted"
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(created,user_id,event,target) VALUES(?,?,?,?)", time.Now().Unix(), user, event, user); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) NewInvitation(ctx context.Context, actor string, lifetime time.Duration, note string) (Invitation, string, error) {
	note = strings.TrimSpace(note)
	if lifetime <= 0 || lifetime > 30*24*time.Hour || utf8.RuneCountInString(note) > 120 {
		return Invitation{}, "", ErrInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Invitation{}, "", err
	}
	defer tx.Rollback()
	if actor != "operator" {
		if err = requireAdmin(ctx, tx, actor); err != nil {
			return Invitation{}, "", err
		}
	}
	now := time.Now()
	v := Invitation{ID: NewID("inv_"), CreatedAt: now.Unix(), ExpiresAt: now.Add(lifetime).Unix(), Status: "unused", Note: note}
	secret := "invite_" + NewSecret()
	if _, err = tx.ExecContext(ctx, "INSERT INTO invitations(digest,expires,id,created,note) VALUES(?,?,?,?,?)", s.Digest(secret), v.ExpiresAt, v.ID, v.CreatedAt, note); err != nil {
		return Invitation{}, "", err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(created,user_id,event,actor,target) VALUES(?,?,'invitation_created',?,?)", now.Unix(), actor, actor, v.ID); err != nil {
		return Invitation{}, "", err
	}
	return v, secret, tx.Commit()
}

func (s *Store) RevokeInvitation(ctx context.Context, actor, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = requireAdmin(ctx, tx, actor); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE invitations SET revoked=1 WHERE id=? AND used_by IS NULL AND revoked=0 AND expires>?", id, time.Now().Unix())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(created,user_id,event,actor,target) VALUES(?,?,'invitation_revoked',?,?)", time.Now().Unix(), actor, actor, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Invitations(ctx context.Context, page int) ([]Invitation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT i.id,i.created,i.expires,COALESCE(i.used_by,''),CASE WHEN i.used_by IS NOT NULL THEN 'used' WHEN i.revoked=1 THEN 'revoked' WHEN i.expires<=? THEN 'expired' ELSE 'unused' END,i.note,COALESCE(u.email,''),i.redeemed FROM invitations i LEFT JOIN users u ON u.id=i.used_by ORDER BY i.created DESC,i.id DESC LIMIT 50 OFFSET ?`, time.Now().Unix(), page*50)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invitation{}
	for rows.Next() {
		var v Invitation
		if err = rows.Scan(&v.ID, &v.CreatedAt, &v.ExpiresAt, &v.UsedBy, &v.Status, &v.Note, &v.Email, &v.RedeemedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) Users(ctx context.Context, page int, query string, defaultLimit int64) ([]ManagedUser, error) {
	pattern := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(query) + "%"
	rows, err := s.db.QueryContext(ctx, `SELECT u.id,u.email,u.created,u.verified,u.admin,u.enabled,u.daily_limit,COALESCE(u.daily_limit,?),COALESCE(i.id,''),COALESCE(i.note,''),COALESCE(m.used,0),COALESCE(m.pending,0),COALESCE(a.used,0) FROM users u LEFT JOIN invitations i ON i.used_by=u.id LEFT JOIN (SELECT user_id,SUM(COALESCE(charged,0)) used,SUM(CASE WHEN charged IS NULL THEN reserved ELSE 0 END) pending FROM model_calls WHERE period=? GROUP BY user_id) m ON m.user_id=u.id LEFT JOIN (SELECT user_id,SUM(COALESCE(charged,0)) used FROM model_calls GROUP BY user_id) a ON a.user_id=u.id WHERE u.email LIKE ? ESCAPE '\' OR u.id LIKE ? ESCAPE '\' ORDER BY u.created DESC,u.id DESC LIMIT 50 OFFSET ?`, defaultLimit, time.Now().UTC().Format("2006-01-02"), pattern, pattern, page*50)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ManagedUser{}
	for rows.Next() {
		var u ManagedUser
		if err = rows.Scan(&u.ID, &u.Email, &u.CreatedAt, &u.VerifiedAt, &u.IsAdmin, &u.Enabled, &u.DailyLimit, &u.EffectiveLimit, &u.InvitationID, &u.InvitationNote, &u.Used, &u.Pending, &u.TotalUsed); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
func (s *Store) SetEnabled(ctx context.Context, actor, user string, enabled bool) error {
	if actor == user {
		return ErrInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = requireAdmin(ctx, tx, actor); err != nil {
		return err
	}
	if err = setEnabledTx(ctx, tx, actor, user, enabled); err != nil {
		return err
	}
	return tx.Commit()
}

func setEnabledTx(ctx context.Context, tx *sql.Tx, actor, user string, enabled bool) error {
	result, err := tx.ExecContext(ctx, "UPDATE users SET enabled=? WHERE id=?", enabled, user)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrCredentials
	}
	event := "account_enabled"
	if !enabled {
		event = "account_disabled"
		for _, q := range []string{"DELETE FROM sessions WHERE user_id=?", "UPDATE device_keys SET revoked=1 WHERE user_id=?", "UPDATE mail_challenges SET state='consumed' WHERE email=(SELECT email FROM users WHERE id=?)"} {
			if _, err = tx.ExecContext(ctx, q, user); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(created,user_id,event,actor,target) VALUES(?,?,?,?,?)", time.Now().Unix(), user, event, actor, user); err != nil {
		return err
	}
	return nil
}
func (s *Store) Audit(ctx context.Context, page int) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,created,actor,CASE WHEN target='' THEN COALESCE(user_id,'') ELSE target END,event,result,detail FROM audit ORDER BY id DESC LIMIT 50 OFFSET ?", page*50)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var v AuditEntry
		if err = rows.Scan(&v.ID, &v.CreatedAt, &v.Actor, &v.Target, &v.Event, &v.Result, &v.Detail); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) NewAdminSession(ctx context.Context, user string, lifetime time.Duration) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err = requireAdmin(ctx, tx, user); err != nil {
		return "", err
	}
	secret := NewSecret()
	if _, err = tx.ExecContext(ctx, "INSERT INTO sessions(digest,user_id,expires,scope) VALUES(?,?,?,'admin')", s.Digest(secret), user, time.Now().Add(lifetime).Unix()); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(created,user_id,event,actor,target) VALUES(?,?,'admin_login',?,?)", time.Now().Unix(), user, user, user); err != nil {
		return "", err
	}
	return secret, tx.Commit()
}

func (s *Store) SetDailyLimit(ctx context.Context, actor, user string, limit *int64, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" || utf8.RuneCountInString(reason) > 200 || limit != nil && (*limit < 0 || *limit > 1_000_000_000_000) {
		return ErrInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = requireAdmin(ctx, tx, actor); err != nil {
		return err
	}
	var previous *int64
	if err = tx.QueryRowContext(ctx, "SELECT daily_limit FROM users WHERE id=?", user).Scan(&previous); errors.Is(err, sql.ErrNoRows) {
		return ErrCredentials
	} else if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE users SET daily_limit=? WHERE id=?", limit, user); err != nil {
		return err
	}
	detail, _ := json.Marshal(struct {
		Before *int64 `json:"before"`
		After  *int64 `json:"after"`
		Reason string `json:"reason"`
	}{previous, limit, reason})
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(created,user_id,event,actor,target,detail) VALUES(?,?,'daily_limit_changed',?,?,?)", time.Now().Unix(), user, actor, user, string(detail)); err != nil {
		return err
	}
	return tx.Commit()
}

// BootstrapAdmin is a private operator action; no public registration proof is implied.
func (s *Store) BootstrapAdmin(ctx context.Context, email, password string) error {
	email, err := normalizeEmail(email)
	if err != nil || utf8.RuneCountInString(password) < 12 || utf8.RuneCountInString(password) > 128 || len(password) > 512 {
		return ErrInput
	}
	salt := NewSecret()
	hash, err := s.password(ctx, password, salt)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	user := NewID("usr_")
	now := time.Now().Unix()
	if _, err = tx.ExecContext(ctx, "INSERT INTO users(id,email,password,created,verified,admin) VALUES(?,?,?,?,?,1)", user, email, "argon2id-v1$"+salt+"$"+hash, now, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO preferences(user_id) VALUES(?)", user); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(created,user_id,event,target) VALUES(?,?,'admin_initialized',?)", now, user, user); err != nil {
		return err
	}
	return tx.Commit()
}

// WorkerIdentity reads an assignment without allocating a generation or a volume.
func (s *Store) WorkerIdentity(ctx context.Context, user string) (string, bool, error) {
	var generation string
	var initialized bool
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT generation FROM worker_generations WHERE user_id=u.id),''),COALESCE((SELECT initialized FROM worker_volumes WHERE user_id=u.id),0) FROM users u WHERE id=?`, user).Scan(&generation, &initialized)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, ErrCredentials
	}
	return generation, initialized, err
}
