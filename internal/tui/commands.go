//go:build unix

package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/karenrebecag/agentnet/internal/guests"
	"github.com/karenrebecag/agentnet/internal/relay"
)

// Via says which store a guest lives in. The two are never merged: the same
// name can exist in both, and an action must reach the one the row came from.
type Via string

const (
	ViaRelay  Via = "relay"
	ViaExpose Via = "expose"
)

// GuestRow is one guest as the Guests tab shows it.
type GuestRow struct {
	Name    string
	Via     Via
	State   string
	Targets []string
}

// remoteMsg carries a refresh of the relay nodes and every guest.
type remoteMsg struct {
	nodes    []relay.Node
	guests   []GuestRow
	relayErr error
}

// actionMsg is the outcome of one action. The token, when there is one, only
// ever travels in this message and the overlay it opens.
type actionMsg struct {
	done   string
	err    error
	secret *secretNote
}

type secretNote struct {
	title string
	token string
	hint  string
}

func refresh(act Actions) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		var msg remoteMsg
		nodes, err := act.Nodes(ctx)
		msg.relayErr = err
		for _, n := range nodes {
			if n.Kind == "guest" {
				msg.guests = append(msg.guests, GuestRow{Name: n.Name, Via: ViaRelay, State: relayState(n), Targets: n.Targets})
			} else {
				msg.nodes = append(msg.nodes, n)
			}
		}
		if local, err := act.ExposeGuests(); err == nil {
			for _, g := range local {
				msg.guests = append(msg.guests, GuestRow{Name: g.Name, Via: ViaExpose, State: exposeState(g), Targets: g.Targets})
			}
		}
		return msg
	}
}

func relayState(n relay.Node) string {
	if n.Revoked {
		return "revoked"
	}
	return "active"
}

func exposeState(g guests.Guest) string {
	switch {
	case g.Revoked:
		return "revoked"
	case g.Approved:
		return "approved"
	case g.Pending:
		return "pending"
	default:
		return "created"
	}
}

var errUsage = errors.New("unknown command: type / to see them")

// command turns the input line into an action, or into a confirmation for the
// destructive ones. Nothing typed into the input is ever sent as a message: it
// is a command or it is refused.
func (m Model) command(line string) (tea.Cmd, *confirmation, error) {
	f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "/"))
	act, info := m.act, m.info
	switch {
	case len(f) == 2 && f[0] == "node":
		name := f[1]
		return run(fmt.Sprintf("machine %s added", name), func(ctx context.Context) (*secretNote, error) {
			token, err := act.AddMachine(ctx, name)
			return &secretNote{title: "Token for machine " + name, token: token,
				hint: "On that machine: agentnet relay init --url " + relayURL(info) + ", then put node and token in relay.json and run agentnet link."}, err
		}), nil, nil
	case len(f) == 4 && f[0] == "guest" && f[1] == string(ViaRelay):
		name, targets := f[2], splitTargets(f[3])
		return run(fmt.Sprintf("relay guest %s added", name), func(ctx context.Context) (*secretNote, error) {
			token, err := act.AddRelayGuest(ctx, name, targets)
			return &secretNote{title: "Token for relay guest " + name, token: token,
				hint: "Connector URL: " + relayURL(info) + "/mcp with header Authorization: Bearer <token>."}, err
		}), nil, nil
	case len(f) == 4 && f[0] == "guest" && f[1] == string(ViaExpose):
		name, targets := f[2], splitTargets(f[3])
		return run(fmt.Sprintf("expose guest %s added", name), func(context.Context) (*secretNote, error) {
			token, err := act.AddExposeGuest(name, targets)
			return &secretNote{title: "Token for expose guest " + name, token: token,
				hint: "Valid while agentnet expose runs. Approve the guest here when it first connects."}, err
		}), nil, nil
	case len(f) == 2 && f[0] == "pair":
		name := f[1]
		return run("new code for "+name, func(ctx context.Context) (*secretNote, error) {
			p, err := act.PairGuest(ctx, name)
			return pairingNote(name, p), err
		}), nil, nil
	case len(f) == 2 && f[0] == "approve":
		return nil, m.guestAction("Approve", GuestRow{Name: f[1], Via: ViaExpose}), nil
	case len(f) == 2 && f[0] == "rotate":
		return nil, m.nodeAction("Rotate", f[1] == info.Node, f[1]), nil
	case len(f) == 3 && f[0] == "revoke" && f[1] == "node":
		return nil, m.nodeAction("Revoke", f[2] == info.Node, f[2]), nil
	case len(f) == 4 && f[0] == "revoke" && f[1] == "guest" && (f[2] == string(ViaRelay) || f[2] == string(ViaExpose)):
		return nil, m.guestAction("Revoke", GuestRow{Name: f[3], Via: Via(f[2])}), nil
	}
	return nil, nil, errUsage
}

// run executes fn off the UI goroutine and reports in one actionMsg.
func run(done string, fn func(ctx context.Context) (*secretNote, error)) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		note, err := fn(ctx)
		if err != nil || note == nil || note.token == "" {
			return actionMsg{done: done, err: err}
		}
		return actionMsg{done: done, secret: note}
	}
}

func splitTargets(raw string) []string {
	var out []string
	for _, t := range strings.Split(raw, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func relayURL(info Info) string {
	if info.Relay == "" {
		return "<relay url>"
	}
	return "https://" + info.Relay
}
