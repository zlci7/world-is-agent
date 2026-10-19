package accounts

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPersonalAndGlobalLimitsShareAtomicAdmission(t *testing.T) {
	s, _ := accountFixture(t)
	ctx := t.Context()
	admin := registerFixture(t, s, "admin@example.test")
	a := registerFixture(t, s, "a@example.test")
	b := registerFixture(t, s, "b@example.test")
	if err := s.SetAdmin(ctx, admin.Email, true); err != nil {
		t.Fatal(err)
	}
	limit := int64(300)
	if err := s.SetDailyLimit(ctx, a.ID, b.ID, &limit, "ordinary user"); !errors.Is(err, ErrCredentials) {
		t.Fatal(err)
	}
	if err := s.SetDailyLimit(ctx, admin.ID, a.ID, &limit, ""); !errors.Is(err, ErrInput) {
		t.Fatal(err)
	}
	if err := s.SetDailyLimit(ctx, admin.ID, a.ID, &limit, "内测配额"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 10)
	for i := 0; i < 10; i++ {
		user := a.ID
		if i%2 == 0 {
			user = b.ID
		}
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.Reserve(ctx, user, NewID("call_"), 100, 500, 500, "test-model") }()
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrLimit) {
			t.Fatal(err)
		}
	}
	if accepted != 5 {
		t.Fatalf("global admission accepted %d", accepted)
	}
	usage, err := s.Usage(ctx, "", 500)
	if err != nil || usage.Pending != 500 || usage.Remaining != 0 {
		t.Fatal(usage, err)
	}
	usage, err = s.Usage(ctx, a.ID, 500)
	if err != nil || usage.Limit != 300 || usage.Pending > 300 {
		t.Fatal(usage, err)
	}
	limit = 0
	if err = s.SetDailyLimit(ctx, admin.ID, a.ID, &limit, "暂停新增调用"); err != nil {
		t.Fatal(err)
	}
	if err = s.Reserve(ctx, a.ID, "after-lowering", 1, 500, 1000, "test-model"); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	if err = s.SetDailyLimit(ctx, admin.ID, a.ID, nil, "恢复默认额度"); err != nil {
		t.Fatal(err)
	}
	users, err := s.Users(ctx, 0, a.Email, 500)
	if err != nil || len(users) != 1 || users[0].DailyLimit != nil || users[0].EffectiveLimit != 500 || users[0].InvitationID == "" {
		t.Fatal(users, err)
	}
	events, err := s.Audit(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Event != "daily_limit_changed" || !strings.Contains(events[0].Detail, `"after":null`) {
		t.Fatal(events[0])
	}
}

func TestUsageSnapshotsRetainUnknownsAndReconcileWithDetails(t *testing.T) {
	s, path := accountFixture(t)
	ctx := t.Context()
	user := registerFixture(t, s, "usage@example.test")
	for _, id := range []string{"settled", "failed", "interrupted"} {
		if err := s.Reserve(ctx, user.ID, id, 100, 1000, 1000, "model-a"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Settle(ctx, user.ID, "settled", 25, tokenPointer(20), tokenPointer(5)); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkCallOutcome(ctx, user.ID, "failed", "failed", "upstream_failed"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverPendingCalls(ctx); err != nil {
		t.Fatal(err)
	}
	day := time.Now().UTC().Format("2006-01-02")
	filter := UsageFilter{From: day, To: day}
	report, err := s.UsageReport(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if report.Totals.Requests != 3 || report.Totals.Used != 25 || report.Totals.Pending != 200 || report.Totals.Failed != 1 || report.Totals.Uncertain != 1 {
		t.Fatal(report.Totals)
	}
	calls, err := s.CallsForExport(ctx, filter)
	if err != nil || len(calls) != 3 {
		t.Fatal(calls, err)
	}
	filter.Status = "settled"
	report, err = s.UsageReport(ctx, filter)
	if err != nil || len(report.Calls) != 1 || report.Calls[0].Prompt == nil || *report.Calls[0].Prompt != 20 {
		t.Fatal(report, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, []byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	usage, err := reopened.Usage(ctx, user.ID, 1000)
	if err != nil || usage.Used != 25 || usage.Pending != 200 {
		t.Fatal(usage, err)
	}
}

func TestBootstrapAndScopedSession(t *testing.T) {
	s, _ := accountFixture(t)
	ctx := t.Context()
	if err := s.BootstrapAdmin(ctx, "owner@example.test", "a long test password"); err != nil {
		t.Fatal(err)
	}
	user, err := s.Login(ctx, "owner@example.test", "a long test password")
	if err != nil || !user.IsAdmin || user.VerifiedAt == 0 {
		t.Fatal(user, err)
	}
	if err = s.BootstrapAdmin(ctx, user.Email, "replacement password"); err == nil {
		t.Fatal("bootstrap overwrote existing identity")
	}
	key, err := s.NewAdminSession(ctx, user.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.Session(ctx, key)
	if err != nil || session.Scope != "admin" {
		t.Fatal(session, err)
	}
	if err = s.SetAdmin(ctx, user.Email, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.NewAdminSession(ctx, user.ID, time.Hour); !errors.Is(err, ErrCredentials) {
		t.Fatal(err)
	}
}

func tokenPointer(v int64) *int64 { return &v }

func TestSettledTotalPreservesMissingBreakdown(t *testing.T) {
	s, _ := accountFixture(t)
	user := registerFixture(t, s, "parts@example.test")
	ctx := t.Context()
	if err := s.Reserve(ctx, user.ID, "parts", 100, 1000, 1000, "model"); err != nil {
		t.Fatal(err)
	}
	if err := s.Settle(ctx, user.ID, "parts", 25, tokenPointer(30), nil); !errors.Is(err, ErrInput) {
		t.Fatal("accepted inconsistent usage", err)
	}
	if err := s.Settle(ctx, user.ID, "parts", 25, nil, tokenPointer(5)); err != nil {
		t.Fatal(err)
	}
	day := time.Now().UTC().Format("2006-01-02")
	r, err := s.UsageReport(ctx, UsageFilter{From: day, To: day})
	if err != nil || r.Calls[0].Prompt != nil || r.Calls[0].Completion == nil || *r.Calls[0].Completion != 5 {
		t.Fatal(r, err)
	}
}
