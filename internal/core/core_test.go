//go:build unix

package core

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func newStore(t *testing.T) Store {
	t.Helper()
	return Store{Home: t.TempDir()}
}

func drain(t *testing.T, s Store, name string) []Envelope {
	t.Helper()
	batch, err := s.Peek(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(name, batch); err != nil {
		t.Fatal(err)
	}
	return batch.Messages
}

func TestOfflineMessageIsDeliveredOnceAfterCommit(t *testing.T) {
	s := newStore(t)
	if _, err := s.Send("bob", "alice", "hola"); err != nil {
		t.Fatal(err)
	}
	if got := drain(t, s, "bob"); len(got) != 1 || got[0].Text != "hola" {
		t.Fatalf("first drain = %+v", got)
	}
	if got := drain(t, s, "bob"); len(got) != 0 {
		t.Fatalf("second drain = %+v", got)
	}
}

func TestPeekWithoutCommitRedelivers(t *testing.T) {
	s := newStore(t)
	s.Send("bob", "alice", "hola")
	if _, err := s.Peek("bob"); err != nil {
		t.Fatal(err)
	}
	if got := drain(t, s, "bob"); len(got) != 1 {
		t.Fatalf("message lost when the reader died before commit: %+v", got)
	}
}

func TestPartialLineStaysUnreadAndPythonFormatParses(t *testing.T) {
	s := newStore(t)
	path, _ := s.inboxPath("bob")
	os.WriteFile(path, []byte(`{"id": "x", "from": "alice", "text": "par`), 0o600)
	if got := drain(t, s, "bob"); len(got) != 0 {
		t.Fatalf("partial line delivered: %+v", got)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("te\"}\n")
	f.Close()
	if got := drain(t, s, "bob"); len(got) != 1 || got[0].Text != "parte" {
		t.Fatalf("completed line = %+v", got)
	}
}

func TestConcurrentLargeSendsNeverInterleave(t *testing.T) {
	s := newStore(t)
	big := strings.Repeat("x", 200<<10)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); s.Send("bob", "alice", big) }()
	}
	wg.Wait()
	got := drain(t, s, "bob")
	if len(got) != 8 {
		t.Fatalf("got %d messages, want 8", len(got))
	}
	for _, m := range got {
		if m.Corrupt || len(m.Text) != len(big) {
			t.Fatal("interleaved or truncated line")
		}
	}
}

func TestFullyReadInboxIsTruncatedPastRotation(t *testing.T) {
	s := newStore(t)
	for range 5 {
		s.Send("bob", "alice", strings.Repeat("y", 250<<10))
	}
	drain(t, s, "bob")
	path, _ := s.inboxPath("bob")
	if info, _ := os.Stat(path); info.Size() != 0 {
		t.Fatalf("inbox not truncated: %d bytes", info.Size())
	}
	s.Send("bob", "alice", "despues")
	if got := drain(t, s, "bob"); len(got) != 1 || got[0].Text != "despues" {
		t.Fatalf("after rotation = %+v", got)
	}
}

func TestStaleHeartbeatCountsOffline(t *testing.T) {
	s := newStore(t)
	if err := s.Register("alice", "test", false); err != nil {
		t.Fatal(err)
	}
	if !s.IsLive("alice", time.Now()) {
		t.Fatal("fresh registration should be live")
	}
	if s.IsLive("alice", time.Now().Add(StaleAfter+time.Second)) {
		t.Fatal("stale registration should be offline")
	}
}

func TestNameLiveInAnotherProcessIsRefused(t *testing.T) {
	s := newStore(t)
	path, _ := s.agentPath("alice")
	other := `{"name":"alice","kind":"test","pid":` + strconv.Itoa(os.Getppid()) + `,"joined":"x"}`
	os.WriteFile(path, []byte(other), 0o600)
	if err := s.Register("alice", "test", false); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("err = %v, want ErrNameTaken", err)
	}
}

func TestPathLikeNamesAreRejected(t *testing.T) {
	s := newStore(t)
	for _, bad := range []string{"../etc", "a/b", "", ".hidden"} {
		if _, err := s.Send(bad, "alice", "x"); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestOversizedMessageIsRejected(t *testing.T) {
	s := newStore(t)
	if _, err := s.Send("bob", "alice", strings.Repeat("z", MaxTextBytes+1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
}
