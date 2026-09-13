package task

import (
	"context"
	"errors"
	"time"
)

type DispatcherConfig struct {
	ScanInterval       time.Duration
	BatchSize          int
	RetryMin, RetryMax time.Duration
}
type DispatcherCallbacks struct {
	Updates     <-chan struct{}
	ReadyWorlds func() []Head
	Guard       func(Binding, func(Head) error) error
	EnqueueWake func(context.Context, Binding, Wake) error
	PauseWorld  func(context.Context, Binding, Wake, error)
}
type Dispatcher struct {
	cancel context.CancelFunc
	done   chan struct{}
	notify chan struct{}
}

func NewDispatcher(parent context.Context, s *Service, c DispatcherConfig, cb DispatcherCallbacks) (*Dispatcher, error) {
	if parent == nil || s == nil || c.ScanInterval <= 0 || c.BatchSize <= 0 || c.RetryMin <= 0 || c.RetryMax < c.RetryMin || cb.ReadyWorlds == nil || cb.Guard == nil || cb.EnqueueWake == nil || cb.PauseWorld == nil {
		return nil, ErrInvalidTaskSpec
	}
	ctx, cancel := context.WithCancel(parent)
	d := &Dispatcher{cancel: cancel, done: make(chan struct{}), notify: make(chan struct{}, 1)}
	go func() {
		defer close(d.done)
		ticker := time.NewTicker(c.ScanInterval)
		defer ticker.Stop()
		scan := func() {
			for _, head := range cb.ReadyWorlds() {
				if ctx.Err() != nil {
					return
				}
				var wakes []Wake
				var err error
				for failures := 1; failures <= 3; failures++ {
					err = cb.Guard(head.Binding, func(current Head) error {
						var claimErr error
						wakes, claimErr = s.ClaimDue(ctx, current.Binding, current.Clock, c.BatchSize)
						return claimErr
					})
					if err == nil || !TechnicalFailure(err) || failures == 3 {
						break
					}
					if !WaitTechnicalRetry(ctx, c, failures) {
						return
					}
				}
				if ctx.Err() != nil {
					return
				}
				if err != nil {
					if !BindingRecoveryError(err) {
						cb.PauseWorld(ctx, head.Binding, Wake{}, err)
					}
					continue
				}
				for _, wake := range wakes {
					if ctx.Err() != nil {
						return
					}
					// Admission owns this claim once called, including queue failure recovery.
					err := cb.EnqueueWake(ctx, head.Binding, wake)
					if ctx.Err() != nil {
						return
					}
					if err != nil && !BindingRecoveryError(err) {
						cb.PauseWorld(ctx, head.Binding, wake, err)
					}
				}
			}
		}
		scan()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				scan()
			case <-d.notify:
				scan()
			case <-cb.Updates:
				scan()
			}
		}
	}()
	return d, nil
}
func (d *Dispatcher) Stop() {
	if d != nil {
		d.cancel()
		<-d.done
	}
}
func (d *Dispatcher) Notify() {
	if d != nil {
		select {
		case <-d.done:
			return
		default:
		}
		select {
		case d.notify <- struct{}{}:
		default:
		}
	}
}

func TechnicalFailure(err error) bool { var e *Error; return !errors.As(err, &e) || e.Technical() }
func BindingRecoveryError(err error) bool {
	return errors.Is(err, ErrGenerationStale) || errors.Is(err, ErrWorldNotReady) || errors.Is(err, ErrSaveInProgress)
}
func RetryDelay(c DispatcherConfig, failures int) time.Duration {
	delay := c.RetryMin
	for i := 1; i < failures && delay < c.RetryMax; i++ {
		if delay > c.RetryMax/2 {
			return c.RetryMax
		}
		delay *= 2
	}
	if delay > c.RetryMax {
		return c.RetryMax
	}
	return delay
}
func WaitTechnicalRetry(ctx context.Context, c DispatcherConfig, failures int) bool {
	timer := time.NewTimer(RetryDelay(c, failures))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
