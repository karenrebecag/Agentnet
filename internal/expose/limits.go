package expose

import (
	"errors"
	"time"
)

const (
	MaxBytes           = 16 * 1024
	PerMinute          = 10
	PerDay             = 100
	FailuresBeforeLock = 5
	Lockout            = 15 * time.Minute
)

var (
	ErrLocked   = errors.New("guest locked out")
	ErrTooLarge = errors.New("message exceeds 16 KiB")
	ErrRate     = errors.New("rate limit")
	ErrDaily    = errors.New("daily limit")
)

type hit struct {
	at    time.Time
	bytes int
}

type guestLimit struct {
	hits     []hit
	failures int
	lockedTo time.Time
}

// Limiter counts in memory. Export and Restore exist so a host can persist
// the counters. A restart without Restore resets the day cap.
type Limiter struct {
	Now    func() time.Time
	guests map[string]*guestLimit
}

func (l *Limiter) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Limiter) get(name string) *guestLimit {
	if l.guests == nil {
		l.guests = make(map[string]*guestLimit)
	}
	g, ok := l.guests[name]
	if !ok {
		g = &guestLimit{}
		l.guests[name] = g
	}
	return g
}

// Admit records one accepted message. Failures and lockouts are separate.
func (l *Limiter) Admit(guest string, nbytes int) error {
	g := l.get(guest)
	now := l.now()
	if now.Before(g.lockedTo) {
		return ErrLocked
	}
	if nbytes > MaxBytes {
		return ErrTooLarge
	}
	g.hits = prune(g.hits, now.Add(-24*time.Hour))
	minute := 0
	day := 0
	for _, h := range g.hits {
		day++
		if h.at.After(now.Add(-time.Minute)) {
			minute++
		}
	}
	if minute >= PerMinute {
		return ErrRate
	}
	if day >= PerDay {
		return ErrDaily
	}
	g.hits = append(g.hits, hit{at: now, bytes: nbytes})
	return nil
}

// NoteFailure locks the guest after five consecutive failures.
func (l *Limiter) NoteFailure(guest string) {
	g := l.get(guest)
	g.failures++
	if g.failures >= FailuresBeforeLock {
		g.lockedTo = l.now().Add(Lockout)
		g.failures = 0
	}
}

// NoteSuccess clears the consecutive-failure streak, not the lockout.
func (l *Limiter) NoteSuccess(guest string) {
	l.get(guest).failures = 0
}

func prune(hits []hit, cutoff time.Time) []hit {
	out := hits[:0]
	for _, h := range hits {
		if h.at.After(cutoff) {
			out = append(out, h)
		}
	}
	return out
}

// Snapshot is the persistable counter state.
type Snapshot struct {
	Guests map[string]GuestSnapshot
}

type GuestSnapshot struct {
	Hits     []HitSnapshot
	Failures int
	LockedTo time.Time
}

type HitSnapshot struct {
	At    time.Time
	Bytes int
}

func (l *Limiter) Export() Snapshot {
	out := Snapshot{Guests: make(map[string]GuestSnapshot, len(l.guests))}
	for name, g := range l.guests {
		gs := GuestSnapshot{Failures: g.failures, LockedTo: g.lockedTo}
		for _, h := range g.hits {
			gs.Hits = append(gs.Hits, HitSnapshot{At: h.at, Bytes: h.bytes})
		}
		out.Guests[name] = gs
	}
	return out
}

func (l *Limiter) Restore(s Snapshot) {
	l.guests = make(map[string]*guestLimit, len(s.Guests))
	for name, gs := range s.Guests {
		g := &guestLimit{failures: gs.Failures, lockedTo: gs.LockedTo}
		for _, h := range gs.Hits {
			g.hits = append(g.hits, hit{at: h.At, bytes: h.Bytes})
		}
		l.guests[name] = g
	}
}
