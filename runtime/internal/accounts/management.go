package accounts

import (
	"context"
	"database/sql"
	"time"
)

type UsageFilter struct {
	From, To, User, Status string
	Page                   int
}
type UsageTotals struct {
	Requests  int64 `json:"requests"`
	Used      int64 `json:"used"`
	Pending   int64 `json:"pending"`
	Failed    int64 `json:"failed"`
	Uncertain int64 `json:"uncertain"`
}
type UsageGroup struct {
	Key   string `json:"key"`
	Email string `json:"email"`
	UsageTotals
}
type ModelCall struct {
	ID         string `json:"request_id"`
	User       string `json:"user_id"`
	Email      string `json:"email"`
	Period     string `json:"period"`
	Created    int64  `json:"created_at"`
	Model      string `json:"model"`
	Status     string `json:"status"`
	Reserved   int64  `json:"reserved"`
	Charged    *int64 `json:"charged"`
	Prompt     *int64 `json:"prompt_tokens"`
	Completion *int64 `json:"completion_tokens"`
	ErrorCode  string `json:"error_code"`
}
type UsageReport struct {
	Totals         UsageTotals  `json:"totals"`
	Days           []UsageGroup `json:"days"`
	Users          []UsageGroup `json:"users"`
	Calls          []ModelCall  `json:"calls"`
	HasMore        bool         `json:"has_more"`
	UsersTruncated bool         `json:"users_truncated"`
}

const modelWhere = ` WHERE m.period>=? AND m.period<=? AND (?='' OR m.user_id=?) AND (?='' OR m.status=?)`
const modelTotals = `COUNT(*),COALESCE(SUM(m.charged),0),COALESCE(SUM(CASE WHEN m.charged IS NULL THEN m.reserved ELSE 0 END),0),COALESCE(SUM(CASE WHEN m.status='failed' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN m.status='uncertain' THEN 1 ELSE 0 END),0)`

func filterArgs(f UsageFilter) []any { return []any{f.From, f.To, f.User, f.User, f.Status, f.Status} }

func (s *Store) UsageReport(ctx context.Context, f UsageFilter) (UsageReport, error) {
	r := UsageReport{Days: []UsageGroup{}, Users: []UsageGroup{}, Calls: []ModelCall{}}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	a := filterArgs(f)
	if err = tx.QueryRowContext(ctx, "SELECT "+modelTotals+" FROM model_calls m"+modelWhere, a...).Scan(&r.Totals.Requests, &r.Totals.Used, &r.Totals.Pending, &r.Totals.Failed, &r.Totals.Uncertain); err != nil {
		return r, err
	}
	for _, group := range []string{"m.period", "m.user_id"} {
		order := group + " DESC"
		if group == "m.user_id" {
			order = "SUM(COALESCE(m.charged,m.reserved)) DESC,m.user_id"
		}
		q := "SELECT " + group + ",COALESCE(MAX(u.email),'')," + modelTotals + " FROM model_calls m JOIN users u ON u.id=m.user_id" + modelWhere + " GROUP BY " + group + " ORDER BY " + order + " LIMIT 101"
		rows, e := tx.QueryContext(ctx, q, a...)
		if e != nil {
			return r, e
		}
		values := []UsageGroup{}
		for rows.Next() {
			var v UsageGroup
			if e = rows.Scan(&v.Key, &v.Email, &v.Requests, &v.Used, &v.Pending, &v.Failed, &v.Uncertain); e != nil {
				rows.Close()
				return r, e
			}
			values = append(values, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return r, e
		}
		if group == "m.period" {
			r.Days = values
		} else {
			r.UsersTruncated = len(values) > 100
			if r.UsersTruncated {
				values = values[:100]
			}
			r.Users = values
		}
	}
	rows, err := tx.QueryContext(ctx, callQuery+modelWhere+" ORDER BY m.period DESC,m.created DESC,m.id DESC LIMIT 51 OFFSET ?", append(a, f.Page*50)...)
	if err != nil {
		return r, err
	}
	r.Calls, err = scanCalls(rows)
	if err != nil {
		return r, err
	}
	r.HasMore = len(r.Calls) > 50
	if r.HasMore {
		r.Calls = r.Calls[:50]
	}
	return r, tx.Commit()
}

const callQuery = `SELECT m.id,m.user_id,u.email,m.period,m.created,m.model,m.status,m.reserved,m.charged,m.prompt_tokens,m.completion_tokens,m.error_code FROM model_calls m JOIN users u ON u.id=m.user_id`

func scanCalls(rows *sql.Rows) ([]ModelCall, error) {
	defer rows.Close()
	values := []ModelCall{}
	for rows.Next() {
		var v ModelCall
		if err := rows.Scan(&v.ID, &v.User, &v.Email, &v.Period, &v.Created, &v.Model, &v.Status, &v.Reserved, &v.Charged, &v.Prompt, &v.Completion, &v.ErrorCode); err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

// CallsForExport reads one bounded snapshot; callers reject results beyond 5000 rows.
func (s *Store) CallsForExport(ctx context.Context, f UsageFilter) ([]ModelCall, error) {
	rows, err := s.db.QueryContext(ctx, callQuery+modelWhere+" ORDER BY m.period DESC,m.created DESC,m.id DESC LIMIT 5001", filterArgs(f)...)
	if err != nil {
		return nil, err
	}
	return scanCalls(rows)
}

type Overview struct {
	Users        int64        `json:"users"`
	Enabled      int64        `json:"enabled"`
	Verified     int64        `json:"verified"`
	Connected    int          `json:"connected"`
	Daily        Usage        `json:"daily"`
	Trend        []UsageGroup `json:"trend"`
	Today        UsageTotals  `json:"today"`
	DefaultLimit int64        `json:"default_limit"`
}

func (s *Store) Overview(ctx context.Context, defaultLimit, globalLimit int64) (Overview, error) {
	o := Overview{DefaultLimit: defaultLimit}
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(enabled),0),COALESCE(SUM(CASE WHEN verified>0 THEN 1 ELSE 0 END),0) FROM users").Scan(&o.Users, &o.Enabled, &o.Verified)
	if err != nil {
		return o, err
	}
	o.Daily, err = s.Usage(ctx, "", globalLimit)
	if err != nil {
		return o, err
	}
	today := time.Now().UTC()
	report, err := s.UsageReport(ctx, UsageFilter{From: today.Format("2006-01-02"), To: today.Format("2006-01-02")})
	if err != nil {
		return o, err
	}
	o.Today = report.Totals
	report, err = s.UsageReport(ctx, UsageFilter{From: today.AddDate(0, 0, -6).Format("2006-01-02"), To: today.Format("2006-01-02")})
	o.Trend = report.Days
	return o, err
}
