//go:build unix

// Package tui is the read-only network monitor behind `agentnet watch`.
package tui

import (
	"sort"
	"time"

	"github.com/karenrebecag/agentnet/internal/core"
	"github.com/karenrebecag/agentnet/internal/guests"
)

// MaxEvents bounds the history kept on screen.
const MaxEvents = 500

type AgentRow struct {
	Name, Kind     string
	Live, External bool
	Unread         int
	PID            int
}

type Snapshot struct {
	Agents  []AgentRow
	New     []core.Envelope
	Pending []string
}

// Feed tracks one read offset per inbox, independent of the agents' cursors.
type Feed struct {
	Store      core.Store
	GuestsPath string
	offsets    map[string]int64
}

func NewFeed(store core.Store, guestsPath string) *Feed {
	return &Feed{Store: store, GuestsPath: guestsPath, offsets: map[string]int64{}}
}

// Poll returns the current agents and every message written since the last
// poll. The first poll replays what the inboxes still hold.
func (f *Feed) Poll(now time.Time) (Snapshot, error) {
	var snap Snapshot
	agents, err := f.Store.List(now)
	if err != nil {
		return snap, err
	}
	for _, a := range agents {
		snap.Agents = append(snap.Agents, AgentRow{
			Name: a.Name, Kind: a.Kind, Live: a.Live, External: a.AcceptExternal, Unread: f.Store.Unread(a.Name), PID: a.PID,
		})
	}
	names, err := f.Store.Inboxes()
	if err != nil {
		return snap, err
	}
	for _, name := range names {
		msgs, next, err := f.Store.Tail(name, f.offsets[name])
		if err != nil {
			continue
		}
		f.offsets[name] = next
		snap.New = append(snap.New, msgs...)
	}
	sort.SliceStable(snap.New, func(i, j int) bool { return snap.New[i].TS < snap.New[j].TS })
	if f.GuestsPath != "" {
		if all, err := guests.List(f.GuestsPath); err == nil {
			for _, g := range all {
				if g.Pending && !g.Approved && !g.Revoked {
					snap.Pending = append(snap.Pending, g.Name)
				}
			}
		}
	}
	return snap, nil
}
