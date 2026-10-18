// Package accounts owns hosted identities and credentials independently of Runtime.
package accounts

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
	_ "modernc.org/sqlite"
)

var (
	ErrCredentials = errors.New("invalid credentials")
	ErrInput       = errors.New("invalid account input")
	ErrConflict    = errors.New("account already exists")
	ErrLimit       = errors.New("account limit reached")
)

type User struct {
	ID         string `json:"user_id"`
	Email      string `json:"email"`
	CreatedAt  int64  `json:"created_at"`
	VerifiedAt int64  `json:"email_verified_at"`
	IsAdmin    bool   `json:"is_admin"`
}

type Session struct {
	User User   `json:"user"`
	CSRF string `json:"csrf"`
}

type Key struct {
	ID         string `json:"key_id"`
	CreatedAt  int64  `json:"created_at"`
	ExpiresAt  int64  `json:"expires_at"`
	LastUsedAt int64  `json:"last_used_at"`
	Revoked    bool   `json:"revoked"`
	DeviceID   string `json:"device_id"`
}

type Preferences struct {
	Version  int64  `json:"version"`
	Name     string `json:"name"`
	Language string `json:"language"`
	Style    string `json:"style"`
}

type Store struct {
	db            *sql.DB
	pepper        []byte
	passwordSlots chan struct{}
}

func Open(path string, pepper []byte) (*Store, error) {
	if len(pepper) < 32 {
		return nil, errors.New("account secret requires at least 32 bytes")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	info, statErr := os.Stat(path)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	existing := statErr == nil && info.Size() > 0
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, pepper: append([]byte(nil), pepper...), passwordSlots: make(chan struct{}, 2)}
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,password TEXT NOT NULL,created INTEGER NOT NULL,enabled INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS invitations(digest TEXT PRIMARY KEY,expires INTEGER NOT NULL,used_by TEXT REFERENCES users(id));
CREATE TABLE IF NOT EXISTS sessions(digest TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id),expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS device_keys(id TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id),digest TEXT NOT NULL,created INTEGER NOT NULL,expires INTEGER NOT NULL,last_used INTEGER NOT NULL DEFAULT 0,revoked INTEGER NOT NULL DEFAULT 0,device TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS device_key_owner ON device_keys(user_id);
CREATE TABLE IF NOT EXISTS preferences(user_id TEXT PRIMARY KEY REFERENCES users(id),version INTEGER NOT NULL DEFAULT 0,name TEXT NOT NULL DEFAULT '',language TEXT NOT NULL DEFAULT 'zh-CN',style TEXT NOT NULL DEFAULT 'natural');
CREATE TABLE IF NOT EXISTS rate_windows(subject TEXT NOT NULL,window INTEGER NOT NULL,count INTEGER NOT NULL,PRIMARY KEY(subject,window));
CREATE TABLE IF NOT EXISTS model_calls(id TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id),period TEXT NOT NULL,reserved INTEGER NOT NULL,charged INTEGER,status TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS model_owner_period ON model_calls(user_id,period);
CREATE TABLE IF NOT EXISTS worker_generations(user_id TEXT PRIMARY KEY REFERENCES users(id),generation TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS worker_volumes(user_id TEXT PRIMARY KEY REFERENCES users(id),initialized INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY,created INTEGER NOT NULL,user_id TEXT NOT NULL,event TEXT NOT NULL);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	if _, err = db.Exec("CREATE TABLE IF NOT EXISTS service_meta(name TEXT PRIMARY KEY,value TEXT NOT NULL); INSERT INTO service_meta(name,value) VALUES('secret_check',?) ON CONFLICT(name) DO NOTHING", s.Digest("wia.account-store.v1")); err != nil {
		db.Close()
		return nil, err
	}
	var check string
	if err = db.QueryRow("SELECT value FROM service_meta WHERE name='secret_check'").Scan(&check); err != nil {
		db.Close()
		return nil, err
	}
	if !hmac.Equal([]byte(check), []byte(s.Digest("wia.account-store.v1"))) {
		db.Close()
		return nil, errors.New("account secret does not match the existing account store")
	}
	if err = s.migrate(path, existing); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func NewID(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}
func NewSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func (s *Store) Digest(value string) string {
	h := hmac.New(sha256.New, s.pepper)
	h.Write([]byte(value))
	return hex.EncodeToString(h.Sum(nil))
}

func normalizeEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	a, err := mail.ParseAddress(value)
	if err != nil || a.Address != value || len(value) > 254 || !strings.Contains(value, ".") {
		return "", ErrInput
	}
	return value, nil
}

func (s *Store) password(ctx context.Context, value, salt string) (string, error) {
	select {
	case s.passwordSlots <- struct{}{}:
		defer func() { <-s.passwordSlots }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return base64.RawStdEncoding.EncodeToString(argon2.IDKey([]byte(value), []byte(salt), 3, 64*1024, 1, 32)), nil
}

func (s *Store) Invite(ctx context.Context, lifetime time.Duration) (string, error) {
	_, secret, err := s.NewInvitation(ctx, "operator", lifetime)
	return secret, err
}

func (s *Store) Register(ctx context.Context, email, password, invitation string, proof Proof) (User, error) {
	email, err := normalizeEmail(email)
	if err != nil || utf8.RuneCountInString(password) < 12 || utf8.RuneCountInString(password) > 128 || len(password) > 512 {
		return User{}, ErrInput
	}
	// Validate the invitation before spending the password hashing budget.
	var valid int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM invitations WHERE digest=? AND used_by IS NULL AND revoked=0 AND expires>?", s.Digest(invitation), time.Now().Unix()).Scan(&valid); err != nil {
		return User{}, err
	}
	if valid != 1 {
		return User{}, ErrCredentials
	}
	salt := NewSecret()
	hash, err := s.password(ctx, password, salt)
	if err != nil {
		return User{}, err
	}
	u := User{ID: NewID("usr_"), Email: email, CreatedAt: time.Now().Unix(), VerifiedAt: time.Now().Unix()}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	var found int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE email=?", email).Scan(&found); err != nil {
		return User{}, err
	}
	if found != 0 {
		return User{}, ErrConflict
	}
	if err = s.consumeProof(ctx, tx, email, "register", proof); err != nil {
		return User{}, finishProofError(tx, err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO users(id,email,password,created,verified) VALUES(?,?,?,?,?)", u.ID, email, "argon2id-v1$"+salt+"$"+hash, u.CreatedAt, u.VerifiedAt); err != nil {
		return User{}, err
	}
	result, err := tx.ExecContext(ctx, "UPDATE invitations SET used_by=? WHERE digest=? AND used_by IS NULL AND revoked=0 AND expires>?", u.ID, s.Digest(invitation), time.Now().Unix())
	if err != nil {
		return User{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return User{}, err
	}
	if n != 1 {
		return User{}, ErrCredentials
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO preferences(user_id) VALUES(?)", u.ID); err != nil {
		return User{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(created,user_id,event) VALUES(?,?,?)", u.CreatedAt, u.ID, "account_registered"); err != nil {
		return User{}, err
	}
	return u, tx.Commit()
}

func (s *Store) Login(ctx context.Context, email, password string) (User, error) {
	email, err := normalizeEmail(email)
	if err != nil || len(password) > 512 {
		return User{}, ErrCredentials
	}
	var u User
	var encoded string
	err = s.db.QueryRowContext(ctx, "SELECT id,email,created,verified,admin,password FROM users WHERE email=? AND enabled=1", email).Scan(&u.ID, &u.Email, &u.CreatedAt, &u.VerifiedAt, &u.IsAdmin, &encoded)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return User{}, err
	}
	// Unknown accounts consume the same hashing work and return the same error.
	if errors.Is(err, sql.ErrNoRows) {
		encoded = "argon2id-v1$missing-account$invalid"
	}
	p := strings.Split(encoded, "$")
	if len(p) != 3 || p[0] != "argon2id-v1" {
		return User{}, ErrCredentials
	}
	hash, err := s.password(ctx, password, p[1])
	if err != nil {
		return User{}, err
	}
	if subtle.ConstantTimeCompare([]byte(hash), []byte(p[2])) != 1 || u.ID == "" {
		return User{}, ErrCredentials
	}
	return u, nil
}

func (s *Store) NewSession(ctx context.Context, user string, lifetime time.Duration) (string, error) {
	secret := NewSecret()
	_, err := s.db.ExecContext(ctx, "INSERT INTO sessions(digest,user_id,expires) VALUES(?,?,?)", s.Digest(secret), user, time.Now().Add(lifetime).Unix())
	return secret, err
}
func (s *Store) Session(ctx context.Context, secret string) (Session, error) {
	var result Session
	err := s.db.QueryRowContext(ctx, "SELECT u.id,u.email,u.created,u.verified,u.admin FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.digest=? AND s.expires>? AND u.enabled=1", s.Digest(secret), time.Now().Unix()).Scan(&result.User.ID, &result.User.Email, &result.User.CreatedAt, &result.User.VerifiedAt, &result.User.IsAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrCredentials
	}
	result.CSRF = s.Digest("csrf:" + secret)
	return result, err
}
func (s *Store) Logout(ctx context.Context, secret string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE digest=?", s.Digest(secret))
	return err
}

func (s *Store) CreateKey(ctx context.Context, user string, lifetime time.Duration) (Key, string, error) {
	k := Key{ID: NewID("key_"), CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(lifetime).Unix()}
	secret := "wia_" + k.ID + "." + NewSecret()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Key{}, "", err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE id=? AND enabled=1 AND verified>0", user).Scan(&count); err != nil {
		return Key{}, "", err
	}
	if count != 1 {
		return Key{}, "", ErrCredentials
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM device_keys WHERE user_id=? AND revoked=0 AND expires>?", user, k.CreatedAt).Scan(&count); err != nil {
		return Key{}, "", err
	}
	if count >= 5 {
		return Key{}, "", ErrLimit
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO device_keys(id,user_id,digest,created,expires) VALUES(?,?,?,?,?)", k.ID, user, s.Digest(secret), k.CreatedAt, k.ExpiresAt); err != nil {
		return Key{}, "", err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(created,user_id,event) VALUES(?,?,?)", k.CreatedAt, user, "key_created:"+k.ID); err != nil {
		return Key{}, "", err
	}
	return k, secret, tx.Commit()
}
func (s *Store) Keys(ctx context.Context, user string) ([]Key, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,created,expires,last_used,revoked,device FROM device_keys WHERE user_id=? ORDER BY created DESC LIMIT 100", user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Key{}
	for rows.Next() {
		var k Key
		if err = rows.Scan(&k.ID, &k.CreatedAt, &k.ExpiresAt, &k.LastUsedAt, &k.Revoked, &k.DeviceID); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
func (s *Store) RevokeKey(ctx context.Context, user, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE device_keys SET revoked=1 WHERE user_id=? AND id=?", user, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(created,user_id,event) VALUES(?,?,?)", time.Now().Unix(), user, "key_revoked:"+id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) AuthenticateKey(ctx context.Context, secret, device string, bind bool) (string, string, error) {
	if len(secret) > 160 || len(device) < 8 || len(device) > 80 {
		return "", "", ErrCredentials
	}
	var user, id, bound string
	err := s.db.QueryRowContext(ctx, `SELECT k.user_id,k.id,k.device FROM device_keys k JOIN users u ON u.id=k.user_id WHERE k.digest=? AND k.revoked=0 AND k.expires>? AND u.enabled=1`, s.Digest(secret), time.Now().Unix()).Scan(&user, &id, &bound)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrCredentials
	}
	if err != nil {
		return "", "", err
	}
	if bound != "" && bound != device {
		return "", "", ErrCredentials
	}
	if bind {
		r, e := s.db.ExecContext(ctx, "UPDATE device_keys SET device=?,last_used=? WHERE id=? AND (device='' OR device=?) AND revoked=0 AND expires>?", device, time.Now().Unix(), id, device, time.Now().Unix())
		if e != nil {
			return "", "", e
		}
		n, e := r.RowsAffected()
		if e != nil {
			return "", "", e
		}
		if n != 1 {
			return "", "", ErrCredentials
		}
	}
	return user, id, nil
}
func (s *Store) KeyActive(ctx context.Context, id string) error {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM device_keys k JOIN users u ON k.user_id=u.id WHERE k.id=? AND k.revoked=0 AND k.expires>? AND u.enabled=1", id, time.Now().Unix()).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrCredentials
	}
	return nil
}

func (s *Store) ReadPreferences(ctx context.Context, user string) (Preferences, error) {
	var p Preferences
	err := s.db.QueryRowContext(ctx, "SELECT version,name,language,style FROM preferences WHERE user_id=?", user).Scan(&p.Version, &p.Name, &p.Language, &p.Style)
	return p, err
}
func (s *Store) SavePreferences(ctx context.Context, user string, p Preferences) (Preferences, error) {
	if len([]rune(p.Name)) > 40 || strings.ContainsAny(p.Name, "\r\n\x00") || (p.Language != "zh-CN" && p.Language != "en") || (p.Style != "natural" && p.Style != "brief" && p.Style != "detailed") {
		return Preferences{}, ErrInput
	}
	r, err := s.db.ExecContext(ctx, "UPDATE preferences SET version=version+1,name=?,language=?,style=? WHERE user_id=? AND version=?", strings.TrimSpace(p.Name), p.Language, p.Style, user, p.Version)
	if err != nil {
		return Preferences{}, err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return Preferences{}, err
	}
	if n != 1 {
		return Preferences{}, ErrConflict
	}
	return s.ReadPreferences(ctx, user)
}

// RateLimit persists a bounded-minute authentication budget across restarts.
func (s *Store) RateLimit(ctx context.Context, subject string, limit int) error {
	minute := time.Now().Unix() / 60
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM rate_windows WHERE window<?", minute-2); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO rate_windows(subject,window,count) VALUES(?,?,1) ON CONFLICT(subject,window) DO UPDATE SET count=count+1", s.Digest(subject), minute); err != nil {
		return err
	}
	var n int
	if err = tx.QueryRowContext(ctx, "SELECT count FROM rate_windows WHERE subject=? AND window=?", s.Digest(subject), minute).Scan(&n); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if n > limit {
		return ErrLimit
	}
	return nil
}

func (s *Store) Generation(ctx context.Context, user string) (string, error) {
	_, err := s.db.ExecContext(ctx, "INSERT INTO worker_generations(user_id,generation) VALUES(?,?) ON CONFLICT(user_id) DO NOTHING", user, NewID("gen_"))
	if err != nil {
		return "", err
	}
	var generation string
	err = s.db.QueryRowContext(ctx, "SELECT generation FROM worker_generations WHERE user_id=?", user).Scan(&generation)
	return generation, err
}

func (s *Store) ReplaceGeneration(ctx context.Context, user string) (string, error) {
	generation := NewID("gen_")
	_, err := s.db.ExecContext(ctx, "UPDATE worker_generations SET generation=? WHERE user_id=?", generation, user)
	return generation, err
}

func (s *Store) Generations(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT w.user_id,w.generation FROM worker_generations w JOIN users u ON u.id=w.user_id WHERE u.enabled=1")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var user, generation string
		if err = rows.Scan(&user, &generation); err != nil {
			return nil, err
		}
		out[user] = generation
	}
	return out, rows.Err()
}

func (s *Store) VolumeInitialized(ctx context.Context, user string) (bool, error) {
	var initialized bool
	err := s.db.QueryRowContext(ctx, "SELECT initialized FROM worker_volumes WHERE user_id=?", user).Scan(&initialized)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return initialized, err
}
func (s *Store) MarkVolumeInitialized(ctx context.Context, user string) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO worker_volumes(user_id,initialized) VALUES(?,1) ON CONFLICT(user_id) DO UPDATE SET initialized=1", user)
	return err
}

func (s *Store) Disable(ctx context.Context, email string) error {
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
	if err = tx.QueryRowContext(ctx, "SELECT id FROM users WHERE email=?", email).Scan(&user); err != nil {
		return err
	}
	if err = setEnabledTx(ctx, tx, "operator", user, false); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ResetPassword(ctx context.Context, email, password string) error {
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
	var user string
	if err = tx.QueryRowContext(ctx, "SELECT id FROM users WHERE email=?", email).Scan(&user); err != nil {
		return err
	}
	if err = replacePassword(ctx, tx, user, "argon2id-v1$"+salt+"$"+hash, "operator"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Backup(ctx context.Context, path string) error {
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("backup target must be new")
	}
	_, err := s.db.ExecContext(ctx, "VACUUM INTO ?", path)
	return err
}
