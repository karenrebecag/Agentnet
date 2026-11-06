//go:build unix

package tui

import (
	"context"
	"time"

	"github.com/karenrebecag/agentnet/internal/guests"
	"github.com/karenrebecag/agentnet/internal/relay"
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
}

// Local runs actions against this machine's files and its relay.
type Local struct {
	Home       string
	GuestsPath string
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
