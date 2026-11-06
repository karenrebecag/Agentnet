//go:build unix

package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/karenrebecag/agentnet/internal/relay"
	"github.com/karenrebecag/agentnet/internal/sessions"
)

// Sessions and contacts change without anyone pressing a key, so they are
// rescanned every scanEvery ticks: a few seconds, cheap enough for ps and one GET.
const scanEvery = 10

type sessionsMsg struct {
	list []sessions.Session
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

func (m Model) selectedSession() (sessions.Session, bool) {
	key := m.selectedKey()
	for _, s := range m.sessions {
		if fmt.Sprintf("session/%d", s.PID) == key {
			return s, true
		}
	}
	return sessions.Session{}, false
}

// copyJoin puts the session's join line on the clipboard. The person pastes it
// into the session, the same way for every CLI.
func (m Model) copyJoin(external bool) (tea.Model, tea.Cmd) {
	s, ok := m.selectedSession()
	if !ok {
		agent, isAgent := strings.CutPrefix(m.selectedKey(), "agent/")
		switch {
		case isAgent && external:
			if err := m.act.OpenToGuests(agent); err != nil {
				m.setStatus(err.Error(), true)
			} else {
				m.setStatus(agent+" takes web guests until its session ends", false)
			}
		case isAgent:
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

// wizard asks for a new web guest one question at a time.
type wizard struct {
	step int
	name string
}

var wizardQuestions = [...]string{"name for the web AI: "}

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

// wizardEnter takes the name and creates an open guest: it may ask any agent,
// and the person allows each agent the first time it writes.
func (m Model) wizardEnter() (tea.Model, tea.Cmd) {
	answer := strings.TrimSpace(m.input.Value())
	m.input.SetValue("")
	m.pick = 0
	if answer == "" {
		m.setStatus("type a name, or esc to cancel", true)
		return m, nil
	}
	m.wizard, m.mode = nil, inputOff
	m.input.Blur()
	name, existing := m.freeName(answer)
	act := m.act
	if existing {
		// A new link replaces the old one, so the AI that used it must be updated.
		m.confirm = &confirmation{
			prompt: name + " exists. Make a new link for it? The old link stops working. [y/N]",
			cmd: run("new link for "+name, func(ctx context.Context) (*secretNote, error) {
				u, err := act.NewGuestURL(ctx, name)
				return linkNote(name, u), err
			}),
		}
		return m, nil
	}
	if name != answer {
		m.setStatus(fmt.Sprintf("%s was used before and names are not reused: using %s", answer, name), false)
	}
	m.busy = true
	return m, run("web guest "+name+" added", func(ctx context.Context) (*secretNote, error) {
		u, err := act.AddWebGuest(ctx, name)
		return linkNote(name, u), err
	})
}

// freeName says whether name is a live web guest already, or else gives the
// first name the relay will hand out: it never reuses one.
func (m Model) freeName(name string) (string, bool) {
	used := map[string]bool{}
	for _, n := range m.nodes {
		used[n.Name] = true
	}
	for _, g := range m.guests {
		if g.Via != ViaRelay {
			continue
		}
		if g.Name == name && g.State != "revoked" {
			return name, true
		}
		used[g.Name] = true
	}
	free := name
	for i := 2; used[free]; i++ {
		free = fmt.Sprintf("%s-%d", name, i)
	}
	return free, false
}

func linkNote(guest, link string) *secretNote {
	return &secretNote{
		title: "Connect a web AI as " + guest,
		token: link,
		hint: strings.Join([]string{
			"1. In the AI, add a custom connector (MCP) and paste this link.",
			"2. Ask it to list the agents and talk to one.",
			"3. The first time it writes to an agent, watch asks you to allow it.",
			"The link is the password: keep it out of shared chats. x on its row in Guests cuts it.",
		}, "\n"),
	}
}

func (m *Model) copyText(text, what string) {
	if err := m.act.Copy(text); err != nil {
		m.setStatus(err.Error(), true)
		return
	}
	m.setStatus(what+" copied", false)
}

type contactsMsg struct {
	list []relay.Contact
	err  error
}

func scanContacts(act Actions) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		list, err := act.Contacts(ctx)
		return contactsMsg{list: list, err: err}
	}
}

// onContacts asks at once when a web AI writes to an agent for the first time.
func (m *Model) onContacts(msg contactsMsg) {
	if msg.err != nil {
		return
	}
	known := map[string]bool{}
	for _, c := range m.contacts {
		known[c.ID] = true
	}
	m.contacts = msg.list
	for i, c := range msg.list {
		if known[c.ID] {
			continue
		}
		m.setStatus(fmt.Sprintf("%s wants to talk to %s", c.Guest, c.Address), false)
		if m.confirm == nil && m.secret == nil && m.detail == nil && m.mode == inputOff {
			m.tab, m.cursor[tabGuests] = tabGuests, i
			m.confirm = m.contactAction(true, c)
		}
	}
	m.clampCursors()
}

func (m Model) selectedContact() (relay.Contact, bool) {
	key := strings.TrimPrefix(m.selectedKey(), "contact/")
	for _, c := range m.contacts {
		if c.ID == key {
			return c, true
		}
	}
	return relay.Contact{}, false
}

func (m Model) contactAction(approve bool, c relay.Contact) *confirmation {
	act := m.act
	if !approve {
		return &confirmation{prompt: fmt.Sprintf("Refuse %s talking to %s? [y/N]", c.Guest, c.Address), cmd: run("refused "+c.Guest, func(ctx context.Context) (*secretNote, error) {
			return nil, act.DecideContact(ctx, c.ID, false)
		})}
	}
	return &confirmation{
		prompt: fmt.Sprintf("%s wants to talk to %s. Allow while that session lasts? [y/N]", c.Guest, c.Address),
		cmd: run(c.Guest+" may talk to "+c.Address, func(ctx context.Context) (*secretNote, error) {
			return nil, act.DecideContact(ctx, c.ID, true)
		}),
	}
}
