//go:build unix

package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// lineInput is a one-line editor. bubbles/textinput would pull a clipboard
// dependency into the module for a box that only needs typing and deleting.
type lineInput struct {
	value   []rune
	focused bool
}

const inputLimit = 512

func (in *lineInput) SetValue(s string) { in.value = []rune(s) }
func (in lineInput) Value() string      { return string(in.value) }
func (in *lineInput) Focus()            { in.focused = true }
func (in *lineInput) Blur()             { in.focused = false }

func (in *lineInput) Update(msg tea.KeyMsg) {
	switch msg.Type {
	case tea.KeyBackspace:
		if len(in.value) > 0 {
			in.value = in.value[:len(in.value)-1]
		}
	case tea.KeyCtrlU:
		in.value = nil
	case tea.KeyCtrlW:
		in.value = []rune(trimLastWord(string(in.value)))
	case tea.KeySpace:
		in.add([]rune{' '})
	case tea.KeyRunes:
		in.add(msg.Runes)
	}
}

func (in *lineInput) add(r []rune) {
	if len(in.value)+len(r) <= inputLimit {
		in.value = append(in.value, r...)
	}
}

// View draws the value, the cursor and, after it, a faint suggested ending.
func (in lineInput) View(ghost string) string {
	if !in.focused {
		return text.Render(string(in.value))
	}
	return text.Render(string(in.value)) + selectedRow.Render(" ") + dim.Render(ghost)
}

func trimLastWord(s string) string {
	r := []rune(s)
	i := len(r)
	for i > 0 && r[i-1] == ' ' {
		i--
	}
	for i > 0 && r[i-1] != ' ' {
		i--
	}
	return string(r[:i])
}
