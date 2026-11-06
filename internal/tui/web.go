//go:build unix

package tui

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/karenrebecag/agentnet/internal/relay"
	"github.com/karenrebecag/agentnet/internal/sessions"
)

// Sessions and sign-ins change without anyone pressing a key, so they are
// rescanned every scanEvery ticks: a few seconds, cheap enough for ps and one GET.
const scanEvery = 10

type sessionsMsg struct {
	list []sessions.Session
	err  error
}

type signinsMsg struct {
	list []relay.Signin
	err  error
}

func scanSessions(act Actions, agents []AgentRow) tea.Cmd {
	joined := map[int]string{}
	for _, a := range agents {
		if a.Live && a.PID > 0 {
			joined[a.PID] = a.Name
		}
	}
	return func() tea.Msg {
		list, err := act.Sessions(context.Background(), joined)
		return sessionsMsg{list: list, err: err}
	}
}

func scanSignins(act Actions) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		list, err := act.Signins(ctx)
		return signinsMsg{list: list, err: err}
	}
}

// onSignins tells the person when a client starts waiting, wherever they are.
func (m *Model) onSignins(msg signinsMsg) {
	if msg.err != nil {
		return
	}
	known := map[string]bool{}
	for _, s := range m.signins {
		known[s.ID] = true
	}
	for i := range msg.list {
		msg.list[i].ClientName, msg.list[i].Host = printable(msg.list[i].ClientName), printable(msg.list[i].Host)
	}
	m.signins = msg.list
	for i, s := range msg.list {
		if known[s.ID] {
			continue
		}
		m.setStatus(fmt.Sprintf("%s wants in as %s", s.Host, s.Guest), false)
		// The code screen for this guest has done its job once a client used the code.
		if m.secret != nil && strings.HasSuffix(m.secret.title, " as "+s.Guest) {
			m.secret = nil
		}
		// Ask right away instead of leaving a row to find, unless the person is
		// in the middle of something else.
		if m.confirm == nil && m.secret == nil && m.detail == nil && m.mode == inputOff {
			m.tab, m.cursor[tabGuests] = tabGuests, i
			m.confirm = m.signinAction(true, s)
		}
	}
	m.clampCursors()
}

func (m Model) selectedSession() (sessions.Session, bool) {
	key := m.selectedKey()
	for _, s := range m.sessions {
		if fmt.Sprintf("session/%d", s.PID) == key {
			return s, true
		}
	}
	return sessions.Session{}, false
}

func (m Model) selectedSignin() (relay.Signin, bool) {
	key := strings.TrimPrefix(m.selectedKey(), "signin/")
	for _, s := range m.signins {
		if s.ID == key {
			return s, true
		}
	}
	return relay.Signin{}, false
}

// copyJoin puts the session's join line on the clipboard. The person pastes it
// into the session, the same way for every CLI.
func (m Model) copyJoin(external bool) (tea.Model, tea.Cmd) {
	s, ok := m.selectedSession()
	if !ok {
		if strings.HasPrefix(m.selectedKey(), "agent/") {
			m.setStatus("already in the network; Enter joins a row marked not joined", false)
		}
		return m, nil
	}
	if s.Agent != "" && !external {
		m.setStatus(fmt.Sprintf("%s is already in the network as %s", s.Name, s.Agent), false)
		return m, nil
	}
	line := sessions.JoinLine(s)
	if external {
		line = strings.Replace(line, "--kind "+s.Tool, "--kind "+s.Tool+" --accept-external", 1)
	}
	if err := m.act.Copy(line); err != nil {
		m.secret = &secretNote{title: "Paste this into " + s.Name, token: line, hint: err.Error()}
		return m, nil
	}
	m.setStatus("copied: paste it into "+s.Name+" ("+s.TTY+")", false)
	return m, nil
}

func (m Model) signinAction(approve bool, s relay.Signin) *confirmation {
	act := m.act
	if !approve {
		return &confirmation{prompt: fmt.Sprintf("Deny the sign-in from %s? [y/N]", s.Host), cmd: run("denied "+s.Host, func(ctx context.Context) (*secretNote, error) {
			return nil, act.DecideSignin(ctx, s.ID, false)
		})}
	}
	prompt := fmt.Sprintf("Sign-in %s: let %s in as %s? Only if your browser shows %s and you are signing in on %s now.", s.ID, s.Host, s.Guest, s.ID, s.Host)
	if loopbackHost(s.Host) {
		prompt += " It returns to a program on this computer: any local program can claim that."
	}
	return &confirmation{
		prompt: prompt + " [y/N]",
		cmd: run(s.Host+" signed in as "+s.Guest, func(ctx context.Context) (*secretNote, error) {
			return nil, act.DecideSignin(ctx, s.ID, true)
		}),
	}
}

func loopbackHost(host string) bool {
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i]
	}
	return h == "localhost" || h == "127.0.0.1"
}

// printable drops control characters, so a name a stranger chose cannot
// redraw the screen the person approves from.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

// wizard asks for a new web guest one question at a time.
type wizard struct {
	step int // 0 name, 1 targets
	name string
}

var wizardQuestions = [...]string{"name for the web guest: ", "agents it can talk to: "}

func (m Model) startWizard() (tea.Model, tea.Cmd) {
	m.wizard = &wizard{}
	m.mode = inputCommand
	m.input.SetValue("")
	m.input.Focus()
	m.pick = 0
	if !m.loaded && !m.busy {
		return m, m.reload()
	}
	return m, nil
}

// wizardEnter takes one answer; the last one creates the guest.
func (m Model) wizardEnter() (tea.Model, tea.Cmd) {
	answer := strings.TrimSpace(m.input.Value())
	m.input.SetValue("")
	m.pick = 0
	if answer == "" {
		m.setStatus("type an answer, or esc to cancel", true)
		return m, nil
	}
	if m.wizard.step == 0 {
		m.wizard = &wizard{step: 1, name: answer}
		return m, nil
	}
	name, targets := m.wizard.name, splitTargets(answer)
	m.wizard, m.mode = nil, inputOff
	m.input.Blur()
	m.busy = true
	act := m.act
	return m, run("web guest "+name+" added", func(ctx context.Context) (*secretNote, error) {
		p, err := act.AddWebGuest(ctx, name, targets)
		return pairingNote(name, p), err
	})
}

func pairingNote(guest string, p relay.Pairing) *secretNote {
	return &secretNote{
		title: "Connect a web AI as " + guest,
		token: p.Code,
		hint: strings.Join([]string{
			"1. In the AI, add a custom connector (MCP) with this URL:",
			"   " + orDefault(p.ConnectorURL, "<relay>/mcp"),
			"2. Connect. On the relay's page, type the code above.",
			"3. Come back here: watch asks you to approve the sign-in.",
			"The code works once, for 10 minutes. /pair " + guest + " makes a new one.",
		}, "\n"),
	}
}

// wizardSuggestions completes the targets question from the relay's agents.
func (m Model) wizardSuggestions() []suggestion {
	if m.wizard == nil || m.wizard.step != 1 {
		return nil
	}
	var open, closed []string
	for _, n := range m.nodes {
		if n.Revoked {
			continue
		}
		for _, a := range n.Agents {
			if a.AcceptExternal {
				open = append(open, n.Name+"."+a.Name)
			} else {
				closed = append(closed, n.Name+"."+a.Name)
			}
		}
	}
	word := m.input.Value()
	return append(filterList(open, word, "agent on the relay"),
		filterList(closed, word, "does not take guests yet: rejoin it from Agents with e")...)
}
