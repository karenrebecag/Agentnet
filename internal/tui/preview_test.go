//go:build unix

package tui

import (
	"os"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/karenrebecag/agentnet/internal/core"
)

func TestPreview(t *testing.T) {
	if os.Getenv("AGENTNET_PREVIEW") == "" {
		t.Skip("set AGENTNET_PREVIEW=1 to print a rendered frame")
	}
	store := core.Store{Home: t.TempDir()}
	store.Register("claude-api", "claude", true)
	store.Register("grok-review", "grok", false)
	store.Send("grok-review", "claude-api", "can you review internal/expose/server.go before the release?")
	store.Send("claude-api", "grok-review", "done: 2 nits, no blockers")
	store.SendWith("claude-api", "ext.perplexity", "summarize the agentnet threat model", core.SendOptions{Origin: core.OriginExternal})
	m := New(NewFeed(store, ""))
	snap, _ := m.feed.Poll(time.Now())
	next, _ := m.Update(pollMsg{snap: snap})
	next, _ = next.Update(tea.WindowSizeMsg{Width: 90, Height: 22})
	os.Stdout.WriteString(next.View() + "\n")
}
