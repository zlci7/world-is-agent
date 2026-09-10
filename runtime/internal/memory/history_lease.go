package memory

import (
	"context"
	"time"
)

type historyLeaseMutex struct{ token chan struct{} }

func newHistoryLeaseMutex() historyLeaseMutex {
	m := historyLeaseMutex{token: make(chan struct{}, 1)}
	m.token <- struct{}{}
	return m
}
func (m historyLeaseMutex) Lock()   { <-m.token }
func (m historyLeaseMutex) Unlock() { m.token <- struct{}{} }
func (m historyLeaseMutex) LockContext(ctx context.Context, timeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.token:
		if err := ctx.Err(); err != nil {
			m.Unlock()
			return err
		}
		return nil
	}
}

func (s *SQLiteHistoryStore) validSnapshotContext(ctx context.Context, snapshot HistorySnapshot) (bool, error) {
	timeout := time.Duration(1<<63 - 1)
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
	}
	if err := s.mu.LockContext(ctx, timeout); err != nil {
		return false, err
	}
	defer s.mu.Unlock()
	saved, ok := s.leases[snapshot.LeaseID]
	return ok && saved == snapshot, nil
}
