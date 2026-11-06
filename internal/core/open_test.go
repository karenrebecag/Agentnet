//go:build unix

package core

import (
	"testing"
	"time"
)

func TestOpenToGuestsLastsOnlyForTheLiveSession(t *testing.T) {
	s := Store{Home: t.TempDir()}
	if err := s.Register("grok", "grok", false); err != nil {
		t.Fatal(err)
	}
	if err := s.OpenToGuests("grok"); err != nil {
		t.Fatal(err)
	}
	if a, ok := s.Lookup("grok", time.Now()); !ok || !a.AcceptExternal {
		t.Fatalf("the live session must be open: %+v", a)
	}
	// A later join under the same name is a new session and starts closed.
	if err := s.Register("grok", "grok", false); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Lookup("grok", time.Now()); a.AcceptExternal {
		t.Fatal("a new session must not inherit the opening")
	}
	if err := s.OpenToGuests("nobody"); err == nil {
		t.Fatal("only a live agent can be opened")
	}
}
