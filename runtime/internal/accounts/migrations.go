package accounts

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func (s *Store) migrate(path string, existing bool) error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == 2 {
		return nil
	}
	if version < 0 || version > 2 {
		return errors.New("unsupported account schema version")
	}
	if existing {
		if err := s.Backup(context.Background(), fmt.Sprintf("%s.pre-v%d-%d.sqlite", path, version+1, time.Now().UnixNano())); err != nil {
			return fmt.Errorf("backup before account migration: %w", err)
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if version == 0 {
		_, err = tx.Exec(`
ALTER TABLE users ADD COLUMN verified INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN admin INTEGER NOT NULL DEFAULT 0;
ALTER TABLE invitations ADD COLUMN id TEXT NOT NULL DEFAULT '';
ALTER TABLE invitations ADD COLUMN created INTEGER NOT NULL DEFAULT 0;
ALTER TABLE invitations ADD COLUMN revoked INTEGER NOT NULL DEFAULT 0;
UPDATE invitations SET id='inv_' || lower(hex(randomblob(16))) WHERE id='';
CREATE UNIQUE INDEX invitation_id ON invitations(id);
ALTER TABLE audit ADD COLUMN actor TEXT NOT NULL DEFAULT 'operator';
ALTER TABLE audit ADD COLUMN target TEXT NOT NULL DEFAULT '';
ALTER TABLE audit ADD COLUMN result TEXT NOT NULL DEFAULT 'success';
UPDATE audit SET actor=user_id WHERE event='account_registered' OR event LIKE 'key_created:%' OR event LIKE 'key_revoked:%';
UPDATE audit SET target=user_id WHERE target='';
CREATE TABLE mail_challenges(id TEXT PRIMARY KEY,email TEXT NOT NULL,purpose TEXT NOT NULL,digest TEXT NOT NULL,created INTEGER NOT NULL,expires INTEGER NOT NULL,attempts INTEGER NOT NULL DEFAULT 0,state TEXT NOT NULL);
CREATE INDEX mail_challenge_owner ON mail_challenges(email,purpose,created);
PRAGMA user_version=1;`)
		if err != nil {
			return fmt.Errorf("migrate account schema: %w", err)
		}
	}
	_, err = tx.Exec(`
ALTER TABLE users ADD COLUMN daily_limit INTEGER CHECK(daily_limit>=0);
ALTER TABLE sessions ADD COLUMN scope TEXT NOT NULL DEFAULT 'player';
ALTER TABLE invitations ADD COLUMN note TEXT NOT NULL DEFAULT '';
ALTER TABLE invitations ADD COLUMN redeemed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE audit ADD COLUMN detail TEXT NOT NULL DEFAULT '';
ALTER TABLE model_calls ADD COLUMN created INTEGER NOT NULL DEFAULT 0;
ALTER TABLE model_calls ADD COLUMN model TEXT NOT NULL DEFAULT '';
ALTER TABLE model_calls ADD COLUMN prompt_tokens INTEGER;
ALTER TABLE model_calls ADD COLUMN completion_tokens INTEGER;
ALTER TABLE model_calls ADD COLUMN error_code TEXT NOT NULL DEFAULT '';
CREATE INDEX model_period_created ON model_calls(period,created,id);
CREATE INDEX invitation_user ON invitations(used_by);
PRAGMA user_version=2;`)
	if err != nil {
		return fmt.Errorf("migrate management schema: %w", err)
	}
	return tx.Commit()
}
