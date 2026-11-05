//go:build unix

package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/karenrebecag/agentnet/internal/core"
)

const (
	pollEvery   = 500 * time.Millisecond
	agentRowsUI = 6
)

var (
	title    = lipgloss.NewStyle().Bold(true)
	dim      = lipgloss.NewStyle().Faint(true)
	external = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	alert    = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	panel    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
)

type keyMap struct {
	Up, Down, Top, Bottom, Follow, Clear, Help, Quit key.Binding
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Follow, k.Help, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Up, k.Down, k.Top, k.Bottom}, {k.Follow, k.Clear, k.Help, k.Quit}}
}

var keys = keyMap{
	Up:     key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "scroll up")),
	Down:   key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "scroll down")),
	Top:    key.NewBinding(key.WithKeys("home", "g"), key.WithHelp("g", "oldest")),
	Bottom: key.NewBinding(key.WithKeys("end", "G"), key.WithHelp("G", "newest")),
	Follow: key.NewBinding(key.WithKeys("f", " "), key.WithHelp("f", "follow on/off")),
	Clear:  key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "clear")),
	Help:   key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "more keys")),
	Quit:   key.NewBinding(key.WithKeys("q", "esc", "ctrl+c"), key.WithHelp("q", "quit")),
}

type tickMsg time.Time

type pollMsg struct {
	snap Snapshot
	err  error
}

type Model struct {
	feed   *Feed
	snap   Snapshot
	events []core.Envelope
	agents table.Model
	log    viewport.Model
	help   help.Model
	follow bool
	err    error
	width  int
	height int
	ready  bool
}

func New(feed *Feed) Model {
	agents := table.New(
		table.WithColumns(agentColumns(60)),
		table.WithHeight(agentRowsUI),
		table.WithFocused(false),
	)
	styles := table.DefaultStyles()
	styles.Header = styles.Header.Bold(true).BorderStyle(lipgloss.NormalBorder()).BorderBottom(true)
	styles.Selected = lipgloss.NewStyle()
	agents.SetStyles(styles)
	return Model{feed: feed, agents: agents, help: help.New(), follow: true}
}

func agentColumns(width int) []table.Column {
	name := max(14, width-44)
	return []table.Column{
		{Title: "", Width: 1},
		{Title: "agent", Width: name},
		{Title: "kind", Width: 8},
		{Title: "unread", Width: 7},
		{Title: "guests", Width: 7},
	}
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
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, keys.Follow):
			m.follow = !m.follow
			if m.follow {
				m.log.GotoBottom()
			}
			return m, nil
		case key.Matches(msg, keys.Clear):
			m.events = nil
			m.refreshLog()
			return m, nil
		case key.Matches(msg, keys.Help):
			m.help.ShowAll = !m.help.ShowAll
			m.layout()
			return m, nil
		case key.Matches(msg, keys.Top):
			m.follow = false
			m.log.GotoTop()
			return m, nil
		case key.Matches(msg, keys.Bottom):
			m.follow = true
			m.log.GotoBottom()
			return m, nil
		case key.Matches(msg, keys.Up):
			m.follow = false
		}
		m.log, cmd = m.log.Update(msg)
		if m.log.AtBottom() {
			m.follow = true
		}
		return m, cmd
	case tea.MouseMsg:
		m.log, cmd = m.log.Update(msg)
		m.follow = m.log.AtBottom()
		return m, cmd
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
	case tickMsg:
		return m, tea.Batch(m.poll(), tick())
	case pollMsg:
		m.err = msg.err
		m.snap.Agents, m.snap.Pending = msg.snap.Agents, msg.snap.Pending
		m.agents.SetRows(agentRows(m.snap.Agents))
		if len(msg.snap.New) > 0 {
			m.events = append(m.events, msg.snap.New...)
			if len(m.events) > MaxEvents {
				m.events = m.events[len(m.events)-MaxEvents:]
			}
			m.refreshLog()
		}
	}
	return m, nil
}

func agentRows(agents []AgentRow) []table.Row {
	rows := make([]table.Row, 0, len(agents))
	for _, a := range agents {
		dot := "○"
		if a.Live {
			dot = "●"
		}
		unread := ""
		if a.Unread > 0 {
			unread = fmt.Sprint(a.Unread)
		}
		ext := ""
		if a.External {
			ext = "open"
		}
		rows = append(rows, table.Row{dot, a.Name, a.Kind, unread, ext})
	}
	return rows
}

func (m *Model) layout() {
	width := max(m.width, 60)
	m.agents.SetColumns(agentColumns(width - 4))
	m.agents.SetWidth(width - 4)
	m.help.Width = width
	fixed := lipgloss.Height(m.header()) + agentRowsUI + 4 + 2 + lipgloss.Height(m.footer())
	height := max(3, m.height-fixed)
	if !m.ready {
		m.log = viewport.New(width-4, height)
		m.ready = true
	} else {
		m.log.Width, m.log.Height = width-4, height
	}
	m.refreshLog()
}

func (m *Model) refreshLog() {
	if !m.ready {
		return
	}
	m.log.SetContent(m.renderEvents(m.log.Width))
	if m.follow {
		m.log.GotoBottom()
	}
}

func (m Model) renderEvents(width int) string {
	if len(m.events) == 0 {
		return dim.Render("no messages yet")
	}
	lines := make([]string, 0, len(m.events))
	for _, e := range m.events {
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
		lines = append(lines, head+tag+oneLine(e.Text, width-used))
	}
	return strings.Join(lines, "\n")
}

func oneLine(s string, width int) string {
	s = strings.Join(strings.Fields(s), " ")
	if width > 1 && len([]rune(s)) > width {
		return string([]rune(s)[:width-1]) + "…"
	}
	return s
}

func (m Model) header() string {
	live := 0
	for _, a := range m.snap.Agents {
		if a.Live {
			live++
		}
	}
	line := title.Render("agentnet watch") + dim.Render(fmt.Sprintf("  %d agents, %d live", len(m.snap.Agents), live))
	if len(m.snap.Pending) > 0 {
		line += "  " + alert.Render("pending guests: "+strings.Join(m.snap.Pending, ", ")+" (agentnet guests approve <name>)")
	}
	return line
}

func (m Model) footer() string {
	state := dim.Render("following")
	if !m.follow {
		state = dim.Render("paused, scrolled back")
	}
	out := state + "  " + m.help.View(keys)
	if m.err != nil {
		out = alert.Render(m.err.Error()) + "  " + out
	}
	return out
}

func (m Model) View() string {
	if !m.ready {
		return "loading…"
	}
	width := max(m.width, 60)
	agents := panel.Width(width - 2).Render(m.agents.View())
	activity := panel.Width(width - 2).Render(title.Render("activity") + "\n" + m.log.View())
	return lipgloss.JoinVertical(lipgloss.Left, m.header(), agents, activity, m.footer())
}
