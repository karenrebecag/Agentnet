//go:build unix

package tui

import (
	"testing"
	"time"

	"github.com/karenrebecag/agentnet/internal/core"
)

func TestWatchingNeverConsumesMessages(t *testing.T) {
	store := core.Store{Home: t.TempDir()}
	store.Send("bob", "alice", "uno")
	feed := NewFeed(store, "")
	snap, err := feed.Poll(time.Now())
	if err != nil || len(snap.New) != 1 {
		t.Fatalf("first poll = %+v, %v", snap.New, err)
	}
	store.Send("bob", "alice", "dos")
	snap, _ = feed.Poll(time.Now())
	if len(snap.New) != 1 || snap.New[0].Text != "dos" {
		t.Fatalf("second poll should only show the new message: %+v", snap.New)
	}
	batch, _ := store.Peek("bob")
	if len(batch.Messages) != 2 {
		t.Fatalf("watch consumed bob's messages: %d left", len(batch.Messages))
	}
}

func TestFeedRestartsAfterInboxRotation(t *testing.T) {
	store := core.Store{Home: t.TempDir()}
	feed := NewFeed(store, "")
	store.Send("bob", "alice", "antes")
	feed.Poll(time.Now())
	batch, _ := store.Peek("bob")
	store.Commit("bob", batch)
	feed.offsets["bob"] = 1 << 30 // as if the file had been truncated under us
	store.Send("bob", "alice", "despues")
	snap, _ := feed.Poll(time.Now())
	if len(snap.New) == 0 || snap.New[len(snap.New)-1].Text != "despues" {
		t.Fatalf("after rotation = %+v", snap.New)
	}
}
