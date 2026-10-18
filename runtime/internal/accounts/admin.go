package accounts

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Invitation struct {
	ID        string `json:"invitation_id"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
	UsedBy    string `json:"used_by"`
	Status    string `json:"status"`
}
type ManagedUser struct {
	User
	Enabled bool `json:"enabled"`
}
type AuditEntry struct {
	ID        int64  `json:"id"`
	CreatedAt int64  `json:"created_at"`
	Actor     string `json:"actor"`
	Target    string `json:"target"`
	Event     string `json:"event"`
	Result    string `json:"result"`
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

func (s *Store) NewInvitation(ctx context.Context, actor string, lifetime time.Duration) (Invitation, string, error) {
	if lifetime <= 0 || lifetime > 30*24*time.Hour {
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
	v := Invitation{ID: NewID("inv_"), CreatedAt: now.Unix(), ExpiresAt: now.Add(lifetime).Unix(), Status: "unused"}
	secret := "invite_" + NewSecret()
	if _, err = tx.ExecContext(ctx, "INSERT INTO invitations(digest,expires,id,created) VALUES(?,?,?,?)", s.Digest(secret), v.ExpiresAt, v.ID, v.CreatedAt); err != nil {
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
	rows, err := s.db.QueryContext(ctx, `SELECT id,created,expires,COALESCE(used_by,''),CASE WHEN used_by IS NOT NULL THEN 'used' WHEN revoked=1 THEN 'revoked' WHEN expires<=? THEN 'expired' ELSE 'unused' END FROM invitations ORDER BY created DESC,id DESC LIMIT 50 OFFSET ?`, time.Now().Unix(), page*50)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invitation{}
	for rows.Next() {
		var v Invitation
		if err = rows.Scan(&v.ID, &v.CreatedAt, &v.ExpiresAt, &v.UsedBy, &v.Status); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) Users(ctx context.Context, page int) ([]ManagedUser, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,email,created,verified,admin,enabled FROM users ORDER BY created DESC,id DESC LIMIT 50 OFFSET ?", page*50)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ManagedUser{}
	for rows.Next() {
		var u ManagedUser
		if err = rows.Scan(&u.ID, &u.Email, &u.CreatedAt, &u.VerifiedAt, &u.IsAdmin, &u.Enabled); err != nil {
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
	rows, err := s.db.QueryContext(ctx, "SELECT id,created,actor,CASE WHEN target='' THEN COALESCE(user_id,'') ELSE target END,event,result FROM audit ORDER BY id DESC LIMIT 50 OFFSET ?", page*50)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var v AuditEntry
		if err = rows.Scan(&v.ID, &v.CreatedAt, &v.Actor, &v.Target, &v.Event, &v.Result); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
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
