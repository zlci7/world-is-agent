package accounts

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"time"
	"unicode/utf8"
)

var ErrVerification = errors.New("verification invalid or expired")

type Proof struct {
	ChallengeID string `json:"challenge_id"`
	Code        string `json:"code"`
}

type Challenge struct {
	ID        string `json:"challenge_id"`
	Email     string `json:"-"`
	Purpose   string `json:"-"`
	ExpiresAt int64  `json:"expires_at"`
	RetryAt   int64  `json:"retry_at"`
}

func (s *Store) User(ctx context.Context, id string) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, "SELECT id,email,created,verified,admin FROM users WHERE id=? AND enabled=1", id).Scan(&u.ID, &u.Email, &u.CreatedAt, &u.VerifiedAt, &u.IsAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrCredentials
	}
	return u, err
}

// BeginChallenge reserves a send attempt. Only SMTP acceptance can activate it.
func (s *Store) BeginChallenge(ctx context.Context, email, purpose, invitation string) (Challenge, string, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return Challenge{}, "", err
	}
	now := time.Now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Challenge{}, "", err
	}
	defer tx.Rollback()
	var valid int
	switch purpose {
	case "register":
		err = tx.QueryRowContext(ctx, "SELECT count(*) FROM invitations WHERE digest=? AND used_by IS NULL AND revoked=0 AND expires>?", s.Digest(invitation), now).Scan(&valid)
	case "verify":
		err = tx.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE email=? AND enabled=1 AND verified=0", email).Scan(&valid)
	case "reset":
		err = tx.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE email=? AND enabled=1 AND verified>0", email).Scan(&valid)
	default:
		return Challenge{}, "", ErrInput
	}
	if err != nil {
		return Challenge{}, "", err
	}
	if valid != 1 {
		return Challenge{}, "", ErrCredentials
	}
	var last int64
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(max(created),0) FROM mail_challenges WHERE email=? AND purpose=?", email, purpose).Scan(&last); err != nil {
		return Challenge{}, "", err
	}
	if last > now-60 {
		return Challenge{}, "", ErrLimit
	}
	secret := NewSecret()
	lifetime := int64(30 * 60)
	if purpose != "reset" {
		n, e := rand.Int(rand.Reader, big.NewInt(1000000))
		if e != nil {
			return Challenge{}, "", e
		}
		secret = fmt.Sprintf("%06d", n.Int64())
		lifetime = 10 * 60
	}
	c := Challenge{ID: NewID("mail_"), Email: email, Purpose: purpose, ExpiresAt: now + lifetime, RetryAt: now + 60}
	if _, err = tx.ExecContext(ctx, "DELETE FROM mail_challenges WHERE expires<?", now-86400); err != nil {
		return Challenge{}, "", err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO mail_challenges(id,email,purpose,digest,created,expires,state) VALUES(?,?,?,?,?,?,'pending')", c.ID, email, purpose, s.proofDigest(c.ID, email, purpose, secret), now, c.ExpiresAt)
	if err != nil {
		return Challenge{}, "", err
	}
	return c, secret, tx.Commit()
}

func (s *Store) ActivateChallenge(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var email, purpose string
	var created int64
	err = tx.QueryRowContext(ctx, "SELECT email,purpose,created FROM mail_challenges WHERE id=? AND state='pending' AND expires>?", id, time.Now().Unix()).Scan(&email, &purpose, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrVerification
	}
	if err != nil {
		return err
	}
	var newer int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM mail_challenges WHERE email=? AND purpose=? AND state IN ('ready','consumed','superseded') AND created>?", email, purpose, created).Scan(&newer); err != nil {
		return err
	}
	if newer != 0 {
		return ErrVerification
	}
	if _, err = tx.ExecContext(ctx, "UPDATE mail_challenges SET state='superseded' WHERE email=? AND purpose=? AND state='ready'", email, purpose); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE mail_challenges SET state='ready' WHERE id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FailChallenge(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE mail_challenges SET state='failed' WHERE id=? AND state='pending'", id)
	return err
}

func (s *Store) proofDigest(id, email, purpose, code string) string {
	return s.Digest("mail:" + id + ":" + email + ":" + purpose + ":" + code)
}

func (s *Store) consumeProof(ctx context.Context, tx *sql.Tx, email, purpose string, proof Proof) error {
	var digest string
	err := tx.QueryRowContext(ctx, "SELECT digest FROM mail_challenges WHERE id=? AND email=? AND purpose=? AND state='ready' AND expires>? AND attempts<5", proof.ChallengeID, email, purpose, time.Now().Unix()).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrVerification
	}
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(digest), []byte(s.proofDigest(proof.ChallengeID, email, purpose, proof.Code))) != 1 {
		if _, err = tx.ExecContext(ctx, "UPDATE mail_challenges SET attempts=attempts+1 WHERE id=?", proof.ChallengeID); err != nil {
			return err
		}
		return ErrVerification
	}
	_, err = tx.ExecContext(ctx, "UPDATE mail_challenges SET state='consumed' WHERE id=?", proof.ChallengeID)
	return err
}

// Wrong attempts persist; successful consumption belongs to the caller's transaction.
func finishProofError(tx *sql.Tx, err error) error {
	if errors.Is(err, ErrVerification) {
		if e := tx.Commit(); e != nil {
			return e
		}
	}
	return err
}

func (s *Store) VerifyEmail(ctx context.Context, user string, proof Proof) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var email string
	if err = tx.QueryRowContext(ctx, "SELECT email FROM users WHERE id=? AND enabled=1 AND verified=0", user).Scan(&email); errors.Is(err, sql.ErrNoRows) {
		return ErrCredentials
	}
	if err != nil {
		return err
	}
	if err = s.consumeProof(ctx, tx, email, "verify", proof); err != nil {
		return finishProofError(tx, err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE users SET verified=? WHERE id=?", time.Now().Unix(), user); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(created,user_id,event,actor,target) VALUES(?,?,'email_verified',?,?)", time.Now().Unix(), user, user, user); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ResetWithProof(ctx context.Context, proof Proof, password string) (string, error) {
	if utf8.RuneCountInString(password) < 12 || utf8.RuneCountInString(password) > 128 || len(password) > 512 {
		return "", ErrInput
	}
	var email string
	err := s.db.QueryRowContext(ctx, "SELECT email FROM mail_challenges WHERE id=? AND purpose='reset' AND state='ready' AND expires>?", proof.ChallengeID, time.Now().Unix()).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrVerification
	}
	if err != nil {
		return "", err
	}
	salt := NewSecret()
	hash, err := s.password(ctx, password, salt)
	if err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var user string
	if err = tx.QueryRowContext(ctx, "SELECT id FROM users WHERE email=? AND enabled=1 AND verified>0", email).Scan(&user); errors.Is(err, sql.ErrNoRows) {
		return "", ErrVerification
	}
	if err != nil {
		return "", err
	}
	if err = s.consumeProof(ctx, tx, email, "reset", proof); err != nil {
		return "", finishProofError(tx, err)
	}
	if err = replacePassword(ctx, tx, user, "argon2id-v1$"+salt+"$"+hash, user); err != nil {
		return "", err
	}
	return user, tx.Commit()
}

func replacePassword(ctx context.Context, tx *sql.Tx, user, hash, actor string) error {
	if _, err := tx.ExecContext(ctx, "UPDATE users SET password=? WHERE id=?", hash, user); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id=?", user); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE device_keys SET revoked=1 WHERE user_id=?", user); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE mail_challenges SET state='consumed' WHERE email=(SELECT email FROM users WHERE id=?) AND purpose='reset'", user); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO audit(created,user_id,event,target,actor) VALUES(?,?,'password_reset',?,?)", time.Now().Unix(), user, user, actor)
	return err
}
