//go:build unix

package tui

import (
	"context"
	"time"

	"github.com/karenrebecag/agentnet/internal/core"
	"github.com/karenrebecag/agentnet/internal/guests"
	"github.com/karenrebecag/agentnet/internal/relay"
	"github.com/karenrebecag/agentnet/internal/sessions"
)

const actionTimeout = 15 * time.Second

// Actions is everything watch can change. Relay calls read the admin token
// from disk inside each call, so the model never holds it.
type Actions interface {
	Nodes(ctx context.Context) ([]relay.Node, error)
	ExposeGuests() ([]guests.Guest, error)
	AddMachine(ctx context.Context, name string) (string, error)
	AddRelayGuest(ctx context.Context, name string, targets []string) (string, error)
	RotateNode(ctx context.Context, name string) (string, error)
	RevokeNode(ctx context.Context, name string) error
	AddExposeGuest(name string, targets []string) (string, error)
	ApproveExposeGuest(name string) error
	RevokeExposeGuest(name string) error
	// AddWebGuest creates a relay guest with an inbox and an unbound pairing
	// code: the person approves the client's host when it signs in.
	AddWebGuest(ctx context.Context, name string) (string, error)
	// NewGuestURL replaces a web guest's link; the old one stops working.
	NewGuestURL(ctx context.Context, name string) (string, error)
	Contacts(ctx context.Context) ([]relay.Contact, error)
	DecideContact(ctx context.Context, id string, approve bool) error
	PairGuest(ctx context.Context, name string) (relay.Pairing, error)
	Signins(ctx context.Context) ([]relay.Signin, error)
	DecideSignin(ctx context.Context, id string, approve bool) error
	Sessions(ctx context.Context, joined map[int]string) ([]sessions.Session, error)
	Copy(text string) error
	// OpenToGuests lets web guests reach a live agent on this machine until
	// its session ends.
	OpenToGuests(agent string) error
}

// Local runs actions against this machine's files and its relay.
type Local struct {
	Home       string
	GuestsPath string
	Store      core.Store
}

func (l Local) Nodes(ctx context.Context) ([]relay.Node, error) { return relay.ListNodes(ctx, l.Home) }

func (l Local) ExposeGuests() ([]guests.Guest, error) { return guests.List(l.GuestsPath) }

func (l Local) AddMachine(ctx context.Context, name string) (string, error) {
	token, _, err := relay.AddNode(ctx, l.Home, name, "machine", nil)
	return token, err
}

func (l Local) AddRelayGuest(ctx context.Context, name string, targets []string) (string, error) {
	token, _, err := relay.AddNode(ctx, l.Home, name, "guest", targets)
	return token, err
}

func (l Local) RotateNode(ctx context.Context, name string) (string, error) {
	return relay.RotateNode(ctx, l.Home, name)
}

func (l Local) RevokeNode(ctx context.Context, name string) error {
	return relay.RevokeNode(ctx, l.Home, name)
}

func (l Local) AddExposeGuest(name string, targets []string) (string, error) {
	return guests.Add(l.GuestsPath, name, targets)
}

func (l Local) ApproveExposeGuest(name string) error { return guests.Approve(l.GuestsPath, name) }

func (l Local) RevokeExposeGuest(name string) error { return guests.Revoke(l.GuestsPath, name) }

// AddWebGuest creates an open guest with an inbox and returns its link, which
// carries the token. watch shows it once; nothing keeps a copy.
func (l Local) AddWebGuest(ctx context.Context, name string) (string, error) {
	token, _, err := relay.AddNodeOpts(ctx, l.Home, name, "guest", nil, true)
	if err != nil {
		return "", err
	}
	return relay.GuestURL(l.Home, token)
}

func (l Local) NewGuestURL(ctx context.Context, name string) (string, error) {
	token, err := relay.RotateNode(ctx, l.Home, name)
	if err != nil {
		return "", err
	}
	return relay.GuestURL(l.Home, token)
}

func (l Local) Contacts(ctx context.Context) ([]relay.Contact, error) { return relay.ListContacts(ctx, l.Home) }

func (l Local) DecideContact(ctx context.Context, id string, approve bool) error {
	return relay.DecideContact(ctx, l.Home, id, approve)
}

func (l Local) PairGuest(ctx context.Context, name string) (relay.Pairing, error) {
	return relay.PairNode(ctx, l.Home, name, "")
}

func (l Local) Signins(ctx context.Context) ([]relay.Signin, error) {
	return relay.ListSignins(ctx, l.Home)
}

func (l Local) DecideSignin(ctx context.Context, id string, approve bool) error {
	return relay.DecideSignin(ctx, l.Home, id, approve)
}

func (l Local) Sessions(ctx context.Context, joined map[int]string) ([]sessions.Session, error) {
	return sessions.Discover(ctx, joined)
}

func (l Local) Copy(text string) error { return sessions.Copy(text) }

func (l Local) OpenToGuests(agent string) error { return l.Store.OpenToGuests(agent) }
