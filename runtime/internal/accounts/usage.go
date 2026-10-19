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
func (s *Store) Reserve(ctx context.Context, user, id string, amount, limit, globalLimit int64, model string) error {
	if amount <= 0 || limit < 0 || globalLimit < 0 {
		return ErrInput
	}
	now := time.Now().UTC()
	period := now.Format("2006-01-02")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var enabled int
	if err = tx.QueryRowContext(ctx, "SELECT enabled,COALESCE(daily_limit,?) FROM users WHERE id=?", limit, user).Scan(&enabled, &limit); err != nil {
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
	if globalLimit > 0 {
		var occupied int64
		if err = tx.QueryRowContext(ctx, "SELECT COALESCE(sum(COALESCE(charged,reserved)),0) FROM model_calls WHERE period=?", period).Scan(&occupied); err != nil {
			return err
		}
		if amount > globalLimit-occupied {
			return ErrLimit
		}
	}
	var existing int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM model_calls WHERE id=?", id).Scan(&existing); err != nil {
		return err
	}
	if existing != 0 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO model_calls(id,user_id,period,reserved,status,created,model) VALUES(?,?,?,?,?,?,?)", id, user, period, amount, "pending", now.Unix(), model); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Settle(ctx context.Context, user, id string, charged int64, prompt, completion *int64) error {
	if charged < 0 {
		return ErrInput
	}
	remaining := charged
	for _, value := range []*int64{prompt, completion} {
		if value != nil {
			if *value < 0 || *value > remaining {
				return ErrInput
			}
			remaining -= *value
		}
	}
	r, err := s.db.ExecContext(ctx, "UPDATE model_calls SET charged=?,prompt_tokens=?,completion_tokens=?,status='settled',error_code='' WHERE id=? AND user_id=? AND status='pending'", charged, prompt, completion, id, user)
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
	if user != "" {
		if err := s.db.QueryRowContext(ctx, "SELECT COALESCE(daily_limit,?) FROM users WHERE id=?", limit, user).Scan(&u.Limit); err != nil {
			return u, err
		}
	}
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(sum(CASE WHEN charged IS NOT NULL THEN charged ELSE 0 END),0),COALESCE(sum(CASE WHEN charged IS NULL THEN reserved ELSE 0 END),0) FROM model_calls WHERE (user_id=? OR ?='') AND period=?`, user, user, u.Period).Scan(&u.Used, &u.Pending)
	u.Remaining = max(int64(0), u.Limit-u.Used-u.Pending)
	return u, err
}

func (s *Store) MarkCallOutcome(ctx context.Context, user, id, status, code string) error {
	if status != "uncertain" && status != "failed" {
		return ErrInput
	}
	_, err := s.db.ExecContext(ctx, "UPDATE model_calls SET status=?,error_code=? WHERE id=? AND user_id=? AND charged IS NULL", status, code, id, user)
	return err
}

func (s *Store) RecoverPendingCalls(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "UPDATE model_calls SET status='uncertain',error_code='service_restarted' WHERE status='pending'")
	return err
}
