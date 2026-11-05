package expose

import (
	"testing"
	"time"
)

func TestAdmitCaps(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	l := &Limiter{Now: func() time.Time { return now }}
	if err := l.Admit("alice", MaxBytes+1); err != ErrTooLarge {
		t.Fatalf("size: %v", err)
	}
	for i := 0; i < PerMinute; i++ {
		if err := l.Admit("alice", 10); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Admit("alice", 10); err != ErrRate {
		t.Fatalf("minute: %v", err)
	}
	snap := l.Export()
	l2 := &Limiter{Now: l.Now}
	l2.Restore(snap)
	if err := l2.Admit("alice", 10); err != ErrRate {
		t.Fatalf("restored minute: %v", err)
	}
}

func TestDailyCap(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	l := &Limiter{Now: func() time.Time { return now }}
	for i := 0; i < PerDay; i++ {
		// Spread hits so the minute window never fills.
		now = now.Add(time.Minute)
		if err := l.Admit("alice", 1); err != nil {
			t.Fatalf("hit %d: %v", i, err)
		}
	}
	now = now.Add(time.Minute)
	if err := l.Admit("alice", 1); err != ErrDaily {
		t.Fatalf("day: %v", err)
	}
}

func TestLockoutAfterFiveFailures(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	l := &Limiter{Now: func() time.Time { return now }}
	for i := 0; i < FailuresBeforeLock-1; i++ {
		l.NoteFailure("alice")
	}
	if err := l.Admit("alice", 1); err != nil {
		t.Fatal(err)
	}
	l.NoteFailure("alice")
	if err := l.Admit("alice", 1); err != ErrLocked {
		t.Fatalf("locked: %v", err)
	}
	now = now.Add(Lockout + time.Second)
	if err := l.Admit("alice", 1); err != nil {
		t.Fatalf("after lockout: %v", err)
	}
}
