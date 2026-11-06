//go:build unix

package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/karenrebecag/agentnet/internal/guests"
	"github.com/karenrebecag/agentnet/internal/relay"
)

const fakeToken = "an_SECRET_TOKEN_VALUE"

type fakeActions struct{ calls []string }

func (f *fakeActions) Nodes(context.Context) ([]relay.Node, error) {
	return []relay.Node{{Name: "mac", Kind: "machine", Agents: []relay.NodeAgent{{Name: "claude"}, {Name: "grok"}}}, {Name: "pc", Kind: "machine", Agents: []relay.NodeAgent{{Name: "codex"}}}, {Name: "perplexity", Kind: "guest", Targets: []string{"mac.claude"}}}, nil
}
func (f *fakeActions) ExposeGuests() ([]guests.Guest, error) {
	return []guests.Guest{{Name: "perplexity", Pending: true, Targets: []string{"claude"}}}, nil
}
func (f *fakeActions) AddMachine(_ context.Context, n string) (string, error) {
	f.calls = append(f.calls, "add-machine "+n)
	return fakeToken, nil
}
func (f *fakeActions) AddRelayGuest(_ context.Context, n string, t []string) (string, error) {
	f.calls = append(f.calls, "add-relay-guest "+n+" "+strings.Join(t, ","))
	return fakeToken, nil
}
func (f *fakeActions) RotateNode(_ context.Context, n string) (string, error) {
	f.calls = append(f.calls, "rotate "+n)
	return fakeToken, nil
}
func (f *fakeActions) RevokeNode(_ context.Context, n string) error {
	f.calls = append(f.calls, "revoke-node "+n)
	return nil
}
func (f *fakeActions) AddExposeGuest(n string, t []string) (string, error) {
	f.calls = append(f.calls, "add-expose-guest "+n)
	return fakeToken, nil
}
func (f *fakeActions) ApproveExposeGuest(n string) error {
	f.calls = append(f.calls, "approve-expose "+n)
	return nil
}
func (f *fakeActions) RevokeExposeGuest(n string) error {
	f.calls = append(f.calls, "revoke-expose "+n)
	return nil
}

// drive feeds keys to the model and runs every command it returns, the way
// the Bubble Tea runtime would, except the polling tick.
func drive(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "right":
			msg = tea.KeyMsg{Type: tea.KeyRight}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case " ":
			msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		next, cmd := m.Update(msg)
		m = settle(next.(Model), cmd)
	}
	return m
}

func settle(m Model, cmd tea.Cmd) Model {
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			return m
		}
		next, more := m.Update(msg)
		m, cmd = next.(Model), more
	}
	return m
}

func newTestModel(act Actions) Model {
	m := New(nil, act, Info{Version: "test", Relay: "relay.example", Node: "mac"})
	m.width, m.height = 100, 40
	return m
}

func TestRevokeWaitsForConfirmation(t *testing.T) {
	act := &fakeActions{}
	m := drive(t, newTestModel(act), "right", "x")
	if len(act.calls) != 0 || m.confirm == nil {
		t.Fatalf("revoke ran before confirmation: %v", act.calls)
	}
	if !strings.Contains(m.confirm.prompt, "This is this machine") {
		t.Fatalf("revoking the local node must say so: %q", m.confirm.prompt)
	}
	m = drive(t, m, "n")
	if len(act.calls) != 0 {
		t.Fatalf("n must cancel, got %v", act.calls)
	}
	drive(t, m, "down", "x", "y")
	if strings.Join(act.calls, ";") != "revoke-node pc" {
		t.Fatalf("calls = %v, want revoke of the selected node pc", act.calls)
	}
}

func TestGuestActionsReachTheStoreTheRowCameFrom(t *testing.T) {
	act := &fakeActions{}
	// Same name in both stores: rows sort relay first, then expose.
	m := drive(t, newTestModel(act), "right", "right")
	m = drive(t, m, "a")
	if m.confirm != nil || len(act.calls) != 0 {
		t.Fatal("a relay guest has no approve step")
	}
	drive(t, m, "down", "x", "y")
	if strings.Join(act.calls, ";") != "revoke-expose perplexity" {
		t.Fatalf("calls = %v, want only the expose store touched", act.calls)
	}
}

func TestNewTokenStaysOutOfStatusAndLog(t *testing.T) {
	act := &fakeActions{}
	m := drive(t, newTestModel(act), "right", "right", "n")
	m = drive(t, m, splitKeys("gemini mac.claude")...)
	m = drive(t, m, "enter")
	if strings.Join(act.calls, ";") != "add-relay-guest gemini mac.claude" {
		t.Fatalf("calls = %v", act.calls)
	}
	if m.secret == nil || m.secret.token != fakeToken {
		t.Fatal("the new token must be shown once")
	}
	if strings.Contains(m.status, fakeToken) {
		t.Fatal("token leaked into the status line")
	}
	m = drive(t, m, "enter")
	if m.secret != nil || strings.Contains(m.View(), fakeToken) {
		t.Fatal("closing the overlay must drop the token")
	}
}

func TestInputIsNeverSentAsAMessage(t *testing.T) {
	act := &fakeActions{}
	m := drive(t, newTestModel(act), "/")
	m = drive(t, m, splitKeys("hello there")...)
	m = drive(t, m, "enter")
	if len(act.calls) != 0 || !m.statusErr {
		t.Fatalf("free text must be refused, calls = %v status = %q", act.calls, m.status)
	}
}

func splitKeys(s string) []string { return strings.Split(s, "") }
