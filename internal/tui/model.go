//go:build unix

package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/karenrebecag/agentnet/internal/core"
)

const pollEvery = 500 * time.Millisecond

var (
	title    = lipgloss.NewStyle().Bold(true)
	dim      = lipgloss.NewStyle().Faint(true)
	live     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	external = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	alert    = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	panel    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
)

type tickMsg time.Time

type pollMsg struct {
	snap Snapshot
	err  error
}

type Model struct {
	feed   *Feed
	snap   Snapshot
	events []core.Envelope
	paused bool
	frozen int
	err    error
	width  int
	height int
}

func New(feed *Feed) Model {
	return Model{feed: feed}
}

func (m Model) poll() tea.Cmd {
	return func() tea.Msg {
		snap, err := m.feed.Poll(time.Now())
		return pollMsg{snap: snap, err: err}
	}
}

func tick() tea.Cmd {
	return tea.Tick(pollEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.poll(), tick())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "p", " ":
			m.paused = !m.paused
			m.frozen = len(m.events)
		case "c":
			m.events, m.frozen = nil, 0
		}
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		return m, tea.Batch(m.poll(), tick())
	case pollMsg:
		m.err = msg.err
		m.snap.Agents, m.snap.Pending = msg.snap.Agents, msg.snap.Pending
		// While paused the feed keeps its offsets, so new messages are kept,
		// only the screen stops scrolling.
		m.events = append(m.events, msg.snap.New...)
		if len(m.events) > MaxEvents {
			drop := len(m.events) - MaxEvents
			m.events = m.events[drop:]
			m.frozen = max(0, m.frozen-drop)
		}
	}
	return m, nil
}

func (m Model) agentsView() string {
	var b strings.Builder
	b.WriteString(title.Render("agents") + "\n")
	if len(m.snap.Agents) == 0 {
		b.WriteString(dim.Render("none joined yet") + "\n")
	}
	for _, a := range m.snap.Agents {
		dot := dim.Render("○")
		if a.Live {
			dot = live.Render("●")
		}
		line := fmt.Sprintf("%s %-18s %-7s", dot, a.Name, a.Kind)
		if a.Unread > 0 {
			line += fmt.Sprintf(" %d unread", a.Unread)
		}
		if a.External {
			line += " " + external.Render("ext")
		}
		b.WriteString(line + "\n")
	}
	if len(m.snap.Pending) > 0 {
		b.WriteString("\n" + alert.Render("pending guests: "+strings.Join(m.snap.Pending, ", ")) + "\n")
		b.WriteString(dim.Render("agentnet guests approve <name>") + "\n")
	}
	return b.String()
}

func oneLine(s string, width int) string {
	s = strings.Join(strings.Fields(s), " ")
	if width > 1 && len([]rune(s)) > width {
		return string([]rune(s)[:width-1]) + "…"
	}
	return s
}

func (m Model) feedView(rows, width int) string {
	var b strings.Builder
	state := ""
	if m.paused {
		state = dim.Render("  (paused)")
	}
	b.WriteString(title.Render("activity") + state + "\n")
	shown := m.events
	if m.paused {
		shown = m.events[:min(m.frozen, len(m.events))]
	}
	for _, e := range shown[max(0, len(shown)-rows):] {
		ts := e.TS
		if t, err := time.Parse(time.RFC3339, e.TS); err == nil {
			ts = t.Local().Format("15:04:05")
		}
		head := fmt.Sprintf("%s %s → %s ", dim.Render(ts), e.From, e.To)
		tag := ""
		if e.Origin == core.OriginExternal {
			tag = external.Render("[ext] ")
		}
		used := lipgloss.Width(head) + lipgloss.Width(tag)
		b.WriteString(head + tag + oneLine(e.Text, width-used) + "\n")
	}
	if len(m.events) == 0 {
		b.WriteString(dim.Render("no messages yet") + "\n")
	}
	return b.String()
}

func (m Model) View() string {
	width := max(m.width, 60)
	height := max(m.height, 12)
	agents := panel.Render(m.agentsView())
	feedRows := max(3, height-lipgloss.Height(agents)-6)
	feed := panel.Width(width - 2).Render(m.feedView(feedRows, width-6))
	footer := dim.Render("q quit · p pause · c clear · read-only: cursors are never moved")
	if m.err != nil {
		footer = alert.Render(m.err.Error()) + "  " + footer
	}
	return lipgloss.JoinVertical(lipgloss.Left, title.Render("agentnet watch"), agents, feed, footer)
}
