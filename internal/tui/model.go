//go:build unix

package tui

import (
	"context"
	"fmt"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/karenrebecag/agentnet/internal/core"
	"github.com/karenrebecag/agentnet/internal/relay"
	"github.com/karenrebecag/agentnet/internal/sessions"
)

const pollEvery = 500 * time.Millisecond

// Info is what the screen shows about this machine. It is read once at start.
type Info struct {
	Version string
	Home    string
	Relay   string // relay host, "" when no relay is configured
	Node    string // this machine's node name on the relay
}

type inputMode int

const (
	inputOff inputMode = iota
	inputFilter
	inputCommand
)

// confirmation is a destructive action waiting for y.
type confirmation struct {
	prompt string
	cmd    tea.Cmd
}

type tickMsg time.Time

type pollMsg struct {
	snap Snapshot
	err  error
}

type Model struct {
	feed *Feed
	act  Actions
	info Info

	snap   Snapshot
	events []core.Envelope
	nodes  []relay.Node
	guests []GuestRow
	// relayErr explains an empty Nodes tab, for example a missing relay.
	relayErr error
	loaded   bool

	sessions    []sessions.Session
	sessionsErr error
	signins     []relay.Signin
	ticks       int
	wizard      *wizard

	tab    tabID
	cursor [tabCount]int
	filter [tabCount]string

	input   lineInput
	mode    inputMode
	pick    int
	confirm *confirmation
	secret  *secretNote
	detail  *core.Envelope

	status    string
	statusErr bool
	busy      bool
	now       time.Time
	width     int
	height    int
}

func New(feed *Feed, act Actions, info Info) Model {
	return Model{feed: feed, act: act, info: info, now: time.Now()}
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
	return tea.Batch(m.poll(), tick(), m.scan())
}

// scan refreshes what changes on its own: the open CLIs and, with a relay,
// the clients waiting for a yes.
func (m Model) scan() tea.Cmd {
	cmds := []tea.Cmd{scanSessions(m.act, m.snap.Agents)}
	if m.info.Relay != "" {
		cmds = append(cmds, scanSignins(m.act))
	}
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.onKey(msg)
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		m.now = time.Time(msg)
		m.ticks++
		if m.ticks%scanEvery == 0 {
			return m, tea.Batch(m.poll(), tick(), m.scan())
		}
		return m, tea.Batch(m.poll(), tick())
	case sessionsMsg:
		m.sessions, m.sessionsErr = msg.list, msg.err
		m.clampCursors()
	case signinsMsg:
		m.onSignins(msg)
	case pollMsg:
		m.onPoll(msg)
	case remoteMsg:
		m.nodes, m.guests, m.relayErr, m.loaded, m.busy = msg.nodes, msg.guests, msg.relayErr, true, false
		m.clampCursors()
	case actionMsg:
		m.busy = false
		if msg.err != nil {
			m.setStatus(msg.err.Error(), true)
			return m, nil
		}
		m.setStatus(msg.done, false)
		m.secret = msg.secret
		return m, tea.Batch(m.reload(), m.scan())
	}
	return m, nil
}

func (m *Model) onPoll(msg pollMsg) {
	if msg.err != nil {
		m.setStatus(msg.err.Error(), true)
	}
	m.snap.Agents, m.snap.Pending = msg.snap.Agents, msg.snap.Pending
	if len(msg.snap.New) == 0 {
		m.clampCursors()
		return
	}
	// The Messages cursor follows new messages only while it sits on the newest.
	following := m.cursor[tabMessages] >= len(m.events)-1
	m.events = append(m.events, msg.snap.New...)
	if len(m.events) > MaxEvents {
		m.events = m.events[len(m.events)-MaxEvents:]
	}
	if following {
		m.cursor[tabMessages] = len(m.events) - 1
	}
	m.clampCursors()
}

func (m *Model) reload() tea.Cmd {
	m.busy = true
	return refresh(m.act)
}

func (m *Model) setStatus(s string, isErr bool) {
	m.status, m.statusErr = s, isErr
}

func (m *Model) clampCursors() {
	for t := tabID(0); t < tabCount; t++ {
		n := len(m.table(t).rows)
		m.cursor[t] = max(0, min(m.cursor[t], n-1))
	}
}

// selectedKey is the key of the row under the cursor, or "" on an empty table.
func (m Model) selectedKey() string {
	t := m.table(m.tab)
	if m.cursor[m.tab] < len(t.keys) {
		return t.keys[m.cursor[m.tab]]
	}
	return ""
}

func (m Model) selectedGuest() (GuestRow, bool) {
	key := m.selectedKey()
	for _, g := range m.guests {
		if string(g.Via)+"/"+g.Name == key {
			return g, true
		}
	}
	return GuestRow{}, false
}

func (m Model) selectedMessage() (core.Envelope, bool) {
	i, err := strconv.Atoi(m.selectedKey())
	if err != nil || i < 0 || i >= len(m.events) {
		return core.Envelope{}, false
	}
	return m.events[i], true
}

func (m Model) nodeAction(verb string, local bool, name string) *confirmation {
	prompt := fmt.Sprintf("%s node %s?", verb, name)
	if local {
		prompt += " This is this machine: its link drops"
		if verb == "Rotate" {
			prompt += " and relay.json gets the new token; restart agentnet link."
		} else {
			prompt += " and relay.json loses its token."
		}
	}
	act := m.act
	var cmd tea.Cmd
	if verb == "Rotate" {
		cmd = run("rotated "+name, func(ctx context.Context) (*secretNote, error) {
			token, err := act.RotateNode(ctx, name)
			return &secretNote{title: "New token for " + name, token: token, hint: "The old token stopped working."}, err
		})
	} else {
		cmd = run("revoked "+name, func(ctx context.Context) (*secretNote, error) {
			return nil, act.RevokeNode(ctx, name)
		})
	}
	return &confirmation{prompt: prompt + " [y/N]", cmd: cmd}
}

func (m Model) guestAction(verb string, g GuestRow) *confirmation {
	prompt := fmt.Sprintf("%s %s guest %s? [y/N]", verb, g.Via, g.Name)
	act := m.act
	var cmd tea.Cmd
	switch {
	case verb == "Approve":
		cmd = run("approved "+g.Name, func(context.Context) (*secretNote, error) {
			return nil, act.ApproveExposeGuest(g.Name)
		})
	case g.Via == ViaRelay:
		cmd = run("revoked relay guest "+g.Name, func(ctx context.Context) (*secretNote, error) {
			return nil, act.RevokeNode(ctx, g.Name)
		})
	default:
		cmd = run("revoked expose guest "+g.Name, func(context.Context) (*secretNote, error) {
			return nil, act.RevokeExposeGuest(g.Name)
		})
	}
	return &confirmation{prompt: prompt, cmd: cmd}
}
