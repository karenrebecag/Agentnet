//go:build unix

package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	// Overlays take every key until they close, so a stray key cannot act on
	// the table behind them.
	switch {
	case m.secret != nil:
		if key == "enter" || key == "esc" {
			m.secret = nil
		}
		return m, nil
	case m.confirm != nil:
		cmd := m.confirm.cmd
		m.confirm = nil
		if key == "y" || key == "Y" {
			m.busy = true
			return m, cmd
		}
		m.setStatus("cancelled", false)
		return m, nil
	case m.detail != nil:
		if key == "enter" || key == "esc" || key == "q" {
			m.detail = nil
		}
		return m, nil
	case m.mode != inputOff:
		return m.onInputKey(msg)
	}
	return m.onTableKey(key)
}

func (m Model) onInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	options := m.suggestions()
	switch msg.String() {
	case "esc":
		if m.mode == inputCommand {
			m.input.SetValue("")
		}
		m.wizard = nil
		m.mode = inputOff
		m.input.Blur()
		return m, nil
	case "up":
		m.pick = max(0, m.pick-1)
		return m, nil
	case "down":
		m.pick = min(max(0, len(options)-1), m.pick+1)
		return m, nil
	case "tab", "right":
		if len(options) > 0 {
			line := m.input.Value()
			m.input.SetValue(accept(line, options[min(m.pick, len(options)-1)], m.wizard != nil || completingList(line)))
			m.pick = 0
		}
		return m, nil
	case "enter":
		if m.mode == inputFilter {
			m.mode = inputOff
			m.input.Blur()
			return m, nil
		}
		if m.wizard != nil {
			return m.wizardEnter()
		}
		line := m.input.Value()
		if strings.TrimSpace(line) == "/add" {
			return m.startWizard()
		}
		m.mode = inputOff
		m.input.Blur()
		m.input.SetValue("")
		cmd, confirm, err := m.command(line)
		switch {
		case err != nil:
			m.setStatus(err.Error(), true)
			return m, nil
		case confirm != nil:
			m.confirm = confirm
			return m, nil
		}
		m.busy = true
		return m, cmd
	}
	m.input.Update(msg)
	m.pick = 0
	if m.mode == inputFilter {
		m.filter[m.tab] = m.input.Value()
		m.cursor[m.tab] = 0
	}
	return m, nil
}

// suggestions are the completions for the current command line, if any.
func (m Model) suggestions() []suggestion {
	if m.mode != inputCommand {
		return nil
	}
	if m.wizard != nil {
		return m.wizardSuggestions()
	}
	return m.completions().suggest(m.input.Value())
}

func (m Model) onTableKey(key string) (tea.Model, tea.Cmd) {
	// The last result stays until the next key, then the prompt shows the network again.
	m.status, m.statusErr = "", false
	rows := len(m.table(m.tab).rows)
	switch key {
	case "q":
		return m, tea.Quit
	case "left", "h", "shift+tab":
		return m.switchTab((m.tab + tabCount - 1) % tabCount)
	case "right", "l", "tab":
		return m.switchTab((m.tab + 1) % tabCount)
	case "up", "k":
		m.cursor[m.tab] = max(0, m.cursor[m.tab]-1)
	case "down", "j":
		m.cursor[m.tab] = max(0, min(rows-1, m.cursor[m.tab]+1))
	case "home", "g":
		m.cursor[m.tab] = 0
	case "end", "G":
		m.cursor[m.tab] = max(0, rows-1)
	case "i":
		m.mode = inputFilter
		m.input.SetValue(m.filter[m.tab])
		m.input.Focus()
		return m, nil
	case "/":
		return m.startCommand("/")
	case "u":
		return m, m.reload()
	case "enter":
		if m.tab == tabSessions {
			return m.copyJoin(false)
		}
		if e, ok := m.selectedMessage(); ok && m.tab == tabMessages {
			m.detail = &e
		}
	default:
		return m.onRowAction(key)
	}
	return m, nil
}

func (m Model) onRowAction(key string) (tea.Model, tea.Cmd) {
	switch m.tab {
	case tabSessions:
		if key == "e" {
			return m.copyJoin(true)
		}
	case tabNodes:
		if key == "a" {
			return m.startCommand("/node ")
		}
		name := m.selectedKey()
		if name == "" {
			return m, nil
		}
		switch key {
		case "r":
			m.confirm = m.nodeAction("Rotate", name == m.info.Node, name)
		case "x":
			m.confirm = m.nodeAction("Revoke", name == m.info.Node, name)
		}
	case tabGuests:
		if key == "n" {
			return m.startWizard()
		}
		if s, ok := m.selectedSignin(); ok {
			switch key {
			case "a":
				m.confirm = m.signinAction(true, s)
			case "x":
				m.confirm = m.signinAction(false, s)
			}
			return m, nil
		}
		g, ok := m.selectedGuest()
		if !ok {
			return m, nil
		}
		switch {
		case key == "x" && g.State != "revoked":
			m.confirm = m.guestAction("Revoke", g)
		case key == "a" && g.Via == ViaExpose && (g.State == "pending" || g.State == "created"):
			m.confirm = m.guestAction("Approve", g)
		case key == "a":
			m.setStatus("only a pending expose guest can be approved; relay guests are active when created", true)
		}
	}
	return m, nil
}

func (m Model) startCommand(prefix string) (tea.Model, tea.Cmd) {
	m.mode = inputCommand
	m.input.SetValue(prefix)
	m.input.Focus()
	m.pick = 0
	// Completion needs node and guest names; fetch them once if no tab did yet.
	if !m.loaded && !m.busy {
		return m, m.reload()
	}
	return m, nil
}

func (m Model) switchTab(t tabID) (tea.Model, tea.Cmd) {
	m.tab = t
	m.input.SetValue(m.filter[t])
	// Relay data is fetched on entering its tabs and on u, never on a timer.
	if (t == tabNodes || t == tabGuests) && !m.busy {
		return m, tea.Batch(m.reload(), m.scan())
	}
	if t == tabSessions {
		return m, m.scan()
	}
	return m, nil
}
