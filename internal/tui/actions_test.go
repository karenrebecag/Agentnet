//go:build unix

package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/karenrebecag/agentnet/internal/guests"
	"github.com/karenrebecag/agentnet/internal/relay"
	"github.com/karenrebecag/agentnet/internal/sessions"
)

const fakeToken = "an_SECRET_TOKEN_VALUE"

// waiting makes the relay report one client waiting for approval.
type fakeActions struct {
	calls   []string
	waiting bool
}

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

func (f *fakeActions) AddWebGuest(_ context.Context, n string, t []string) (relay.Pairing, error) {
	f.calls = append(f.calls, "add-web-guest "+n+" "+strings.Join(t, ","))
	return relay.Pairing{Code: "ABCD-EFGH", ConnectorURL: "https://relay.example/mcp"}, nil
}
func (f *fakeActions) PairGuest(_ context.Context, n string) (relay.Pairing, error) {
	f.calls = append(f.calls, "pair "+n)
	return relay.Pairing{Code: "ABCD-EFGH"}, nil
}
func (f *fakeActions) Signins(context.Context) ([]relay.Signin, error) {
	if !f.waiting {
		return nil, nil
	}
	return []relay.Signin{{ID: "0a1b2c3d", Guest: "claude-web", Host: "claude.ai", ClientName: "Claude"}}, nil
}
func (f *fakeActions) DecideSignin(_ context.Context, id string, approve bool) error {
	f.calls = append(f.calls, fmt.Sprintf("signin %s %t", id, approve))
	return nil
}
func (f *fakeActions) Sessions(context.Context, map[int]string) ([]sessions.Session, error) {
	return []sessions.Session{{PID: 101, Tool: "grok", Name: "grok-011", TTY: "ttys011", State: "open"}}, nil
}
func (f *fakeActions) Copy(text string) error {
	f.calls = append(f.calls, "copy "+text)
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
	if cmd == nil {
		return m
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = settle(m, c)
		}
		return m
	}
	if msg == nil {
		return m
	}
	next, more := m.Update(msg)
	return settle(next.(Model), more)
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
	m := drive(t, newTestModel(act), "/")
	m = drive(t, m, splitKeys("guest relay gemini mac.claude")...)
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

func TestAddAsksNameThenAgentsAndShowsTheCode(t *testing.T) {
	act := &fakeActions{}
	m := drive(t, newTestModel(act), "/")
	m = drive(t, m, append(append(splitKeys("add"), "enter"), splitKeys("claude-web")...)...)
	m = drive(t, m, "enter")
	m = drive(t, m, append(splitKeys("mac.claude"), "enter")...)
	if strings.Join(act.calls, ";") != "add-web-guest claude-web mac.claude" {
		t.Fatalf("calls = %v", act.calls)
	}
	if m.secret == nil || m.secret.token != "ABCD-EFGH" || !strings.Contains(m.secret.hint, "https://relay.example/mcp") {
		t.Fatalf("the code and connector URL must be shown: %+v", m.secret)
	}
}

func TestSigninWaitsForConfirmation(t *testing.T) {
	act := &fakeActions{waiting: true}
	m := settle(newTestModel(act), scanSignins(act))
	if len(act.calls) != 0 || m.confirm == nil || !strings.Contains(m.confirm.prompt, "claude.ai") || !strings.Contains(m.confirm.prompt, "0a1b2c3d") {
		t.Fatalf("approval must name the host and wait for y: %v %+v", act.calls, m.confirm)
	}
	drive(t, m, "y")
	if strings.Join(act.calls, ";") != "signin 0a1b2c3d true" {
		t.Fatalf("calls = %v", act.calls)
	}
}

func TestANewSigninAsksForApprovalAtOnce(t *testing.T) {
	act := &fakeActions{waiting: true}
	m := newTestModel(act)
	m.secret = &secretNote{title: "Connect a web AI as claude-web", token: "ABCD-EFGH"}
	m = settle(m, scanSignins(act))
	if m.secret != nil || m.confirm == nil || m.tab != tabGuests || !strings.Contains(m.confirm.prompt, "claude.ai") {
		t.Fatalf("a used code's screen must give way to the approval: secret=%v confirm=%v tab=%v", m.secret, m.confirm, m.tab)
	}
	if len(act.calls) != 0 {
		t.Fatalf("approval needs y: %v", act.calls)
	}
}

func TestEnterOnAnUnjoinedCLICopiesItsJoinLine(t *testing.T) {
	act := &fakeActions{}
	// Init's scan is what fills the list when watch opens.
	m := settle(newTestModel(act), newTestModel(act).scan())
	drive(t, m, "enter")
	if len(act.calls) != 1 || !strings.Contains(act.calls[0], "agentnet join grok-011 --kind grok") {
		t.Fatalf("calls = %v", act.calls)
	}
}

func TestAddWithAnExistingNameReconnectsOrRenames(t *testing.T) {
	act := &fakeActions{}
	m := drive(t, newTestModel(act), "right", "right", "n")
	drive(t, m, append(splitKeys("perplexity"), "enter")...)
	if strings.Join(act.calls, ";") != "pair perplexity" {
		t.Fatalf("a live guest of that name must get a new code, calls = %v", act.calls)
	}
	act = &fakeActions{}
	m = drive(t, newTestModel(act), "right", "right", "n")
	m = drive(t, m, append(splitKeys("mac"), "enter")...)
	if m.wizard == nil || m.wizard.name != "mac-2" {
		t.Fatalf("a taken name must move to the next free one: %+v", m.wizard)
	}
}
