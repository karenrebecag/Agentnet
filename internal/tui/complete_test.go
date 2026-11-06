//go:build unix

package tui

import (
	"reflect"
	"testing"
)

func values(s []suggestion) []string {
	out := []string{}
	for _, x := range s {
		out = append(out, x.Value)
	}
	return out
}

func TestCompletionFollowsTheCommandGrammar(t *testing.T) {
	c := completions{
		machines:     []string{"mac", "pc"},
		relayGuests:  []string{"perplexity"},
		exposeGuests: []string{"gemini"},
		pending:      []string{"gemini"},
		addresses:    []string{"mac.claude", "mac.grok", "pc.codex"},
		localAgents:  []string{"claude"},
	}
	cases := map[string][]string{
		"/r":                         {"/rotate", "/revoke"},
		"/revoke ":                   {"node", "guest"},
		"/revoke guest expose ":      {"gemini"},
		"/revoke guest relay p":      {"perplexity"},
		"/guest relay g mac.":        {"mac.claude", "mac.grok"},
		"/guest relay g mac.claude,": {"mac.claude,mac.grok", "mac.claude,pc.codex"},
		"/guest expose g ":           {"claude"},
		"/approve ":                  {"gemini"},
		"/node ":                     {},
		"hello":                      {},
	}
	for line, want := range cases {
		if got := values(c.suggest(line)); !reflect.DeepEqual(got, want) {
			t.Errorf("suggest(%q) = %v, want %v", line, got, want)
		}
	}
}

func TestAcceptReplacesOnlyTheLastWord(t *testing.T) {
	if got := accept("/revoke gu", suggestion{Value: "guest"}, false); got != "/revoke guest " {
		t.Fatalf("got %q", got)
	}
	if got := accept("/guest relay g mac.c", suggestion{Value: "mac.claude"}, true); got != "/guest relay g mac.claude" {
		t.Fatalf("a list stays open for another comma, got %q", got)
	}
}

func TestTypedRevokeStillAsksFirst(t *testing.T) {
	act := &fakeActions{}
	m := drive(t, newTestModel(act), "/")
	m = drive(t, m, splitKeys("revoke node pc")...)
	m = drive(t, m, "enter")
	if len(act.calls) != 0 || m.confirm == nil {
		t.Fatalf("a typed revoke must wait for y, calls = %v", act.calls)
	}
	drive(t, m, "y")
	if len(act.calls) != 1 || act.calls[0] != "revoke-node pc" {
		t.Fatalf("calls = %v", act.calls)
	}
}
