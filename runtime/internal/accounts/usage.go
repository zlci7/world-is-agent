package accounts

import (
	"context"
	"errors"
	"time"
)

type Usage struct {
	Period    string `json:"period"`
	Timezone  string `json:"timezone"`
	Limit     int64  `json:"limit"`
	Used      int64  `json:"used"`
	Pending   int64  `json:"pending"`
	Remaining int64  `json:"remaining"`
}

// Reservations stay charged while the upstream outcome is unknown, including after restart.
func (s *Store) Reserve(ctx context.Context, user, id string, amount, limit int64) error {
	if amount <= 0 || limit <= 0 {
		return ErrInput
	}
	period := time.Now().UTC().Format("2006-01-02")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var enabled int
	if err = tx.QueryRowContext(ctx, "SELECT enabled FROM users WHERE id=?", user).Scan(&enabled); err != nil {
		return err
	}
	if enabled != 1 {
		return ErrCredentials
	}
	var used int64
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(sum(COALESCE(charged,reserved)),0) FROM model_calls WHERE user_id=? AND period=?", user, period).Scan(&used); err != nil {
		return err
	}
	if amount > limit-used {
		return ErrLimit
	}
	var existing int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM model_calls WHERE id=?", id).Scan(&existing); err != nil {
		return err
	}
	if existing != 0 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO model_calls(id,user_id,period,reserved,status) VALUES(?,?,?,?,?)", id, user, period, amount, "pending"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Settle(ctx context.Context, user, id string, charged int64) error {
	if charged < 0 {
		return ErrInput
	}
	r, err := s.db.ExecContext(ctx, "UPDATE model_calls SET charged=?,status='settled' WHERE id=? AND user_id=? AND status='pending'", charged, id, user)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("reservation is absent or already settled")
	}
	return nil
}

func (s *Store) Usage(ctx context.Context, user string, limit int64) (Usage, error) {
	u := Usage{Period: time.Now().UTC().Format("2006-01-02"), Timezone: "UTC", Limit: limit}
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(sum(CASE WHEN charged IS NOT NULL THEN charged ELSE 0 END),0),COALESCE(sum(CASE WHEN charged IS NULL THEN reserved ELSE 0 END),0) FROM model_calls WHERE user_id=? AND period=?`, user, u.Period).Scan(&u.Used, &u.Pending)
	u.Remaining = max(int64(0), limit-u.Used-u.Pending)
	return u, err
}
