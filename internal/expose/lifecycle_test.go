package expose

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeProc struct {
	killed chan struct{}
	done   chan error
}

func (p *fakeProc) Kill() error {
	select {
	case <-p.killed:
	default:
		close(p.killed)
	}
	return nil
}

func (p *fakeProc) Done() <-chan error { return p.done }

func TestKillSwitch(t *testing.T) {
	proc := &fakeProc{killed: make(chan struct{}), done: make(chan error)}
	started := make(chan struct{})
	s := &Supervisor{Start: func(context.Context) (Process, error) {
		close(started)
		return proc, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	<-started
	cancel()
	select {
	case <-proc.killed:
	case <-time.After(time.Second):
		t.Fatal("kill was not called")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("run: %v", err)
	}
}

func TestTTLKills(t *testing.T) {
	proc := &fakeProc{killed: make(chan struct{}), done: make(chan error)}
	s := &Supervisor{
		TTL:   20 * time.Millisecond,
		Start: func(context.Context) (Process, error) { return proc, nil },
	}
	err := s.Run(context.Background())
	if !errors.Is(err, ErrTTL) {
		t.Fatalf("run: %v", err)
	}
	select {
	case <-proc.killed:
	default:
		t.Fatal("ttl did not kill")
	}
}

func TestStopsAfterTwentyFailures(t *testing.T) {
	starts := 0
	s := &Supervisor{Start: func(context.Context) (Process, error) {
		starts++
		return nil, errors.New("boom")
	}}
	err := s.Run(context.Background())
	if !errors.Is(err, ErrTooManyFailures) {
		t.Fatalf("run: %v", err)
	}
	if starts != DefaultMaxFailures {
		t.Fatalf("starts %d", starts)
	}
}
