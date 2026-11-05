package expose

import (
	"context"
	"errors"
	"time"
)

const DefaultMaxFailures = 20

var (
	ErrTooManyFailures = errors.New("tunnel stopped after repeated failures")
	ErrTTL             = errors.New("tunnel stopped at ttl")
)

// Process is the tunnel child. Kill must end the whole process group.
type Process interface {
	Kill() error
	Done() <-chan error
}

// Supervisor restarts a process until the context is cancelled, the TTL
// fires, or it has failed DefaultMaxFailures times.
type Supervisor struct {
	TTL         time.Duration
	MaxFailures int
	Start       func(context.Context) (Process, error)
}

func (s *Supervisor) max() int {
	if s.MaxFailures <= 0 {
		return DefaultMaxFailures
	}
	return s.MaxFailures
}

func (s *Supervisor) Run(ctx context.Context) error {
	failures := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		proc, err := s.Start(ctx)
		if err != nil {
			failures++
			if failures >= s.max() {
				return ErrTooManyFailures
			}
			continue
		}
		var timer *time.Timer
		var timed <-chan time.Time
		if s.TTL > 0 {
			timer = time.NewTimer(s.TTL)
			timed = timer.C
		}
		select {
		case <-ctx.Done():
			stopTimer(timer)
			_ = proc.Kill()
			return ctx.Err()
		case <-timed:
			stopTimer(timer)
			_ = proc.Kill()
			return ErrTTL
		case err := <-proc.Done():
			stopTimer(timer)
			if err == nil {
				return nil
			}
			failures++
			if failures >= s.max() {
				return ErrTooManyFailures
			}
		}
	}
}

func stopTimer(t *time.Timer) {
	if t == nil {
		return
	}
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}
