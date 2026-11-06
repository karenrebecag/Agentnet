//go:build unix

package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const minWidth = 40

// hotkey is one entry of the bottom bar: the key drawn inverted, then its name.
type hotkey struct{ key, label string }

var tabKeys = [tabCount][]hotkey{
	{},
	{{"↵", "Copy join line"}, {"e", "Join open to guests"}},
	{{"a", "Add"}, {"r", "Rotate"}, {"x", "Revoke"}},
	{{"n", "Add web AI"}, {"a", "Approve"}, {"x", "Revoke/Deny"}},
	{{"↵", "Read"}},
}

var (
	navKeys     = []hotkey{{"↑↓", "Move"}, {"←→", "Tab"}}
	toolKeys    = []hotkey{{"/", "Command"}, {"i", "Filter"}, {"u", "Refresh"}, {"q", "Quit"}}
	commandKeys = []hotkey{{"tab", "Complete"}, {"↑↓", "Choose"}, {"↵", "Run"}, {"esc", "Cancel"}}
	filterKeys  = []hotkey{{"↵", "Keep filter"}, {"esc", "Leave"}}
	overlayKeys = []hotkey{{"↵", "Close"}}
	confirmKeys = []hotkey{{"y", "Yes"}, {"n", "No"}}
)

const menuRows = 6

func (m Model) View() string {
	width := max(m.width, minWidth)
	top := []string{m.tabs(width), ""}
	bottom := []string{m.prompt(width), m.keybar(width)}
	if menu := m.menu(width); menu != "" {
		bottom = append([]string{menu}, bottom...)
	}
	used := lipgloss.Height(strings.Join(top, "\n")) + lipgloss.Height(strings.Join(bottom, "\n"))
	height := max(3, m.height-used)
	body := m.body(width-2, height)
	// Pad so the prompt and shortcuts stay on the bottom edge however short the table is.
	if pad := height - lipgloss.Height(body); pad > 0 {
		body += strings.Repeat("\n", pad)
	}
	return strings.Join(append(append(top, body), bottom...), "\n")
}

// prompt is a two-line box in the shape of a powerlevel10k prompt: where you
// are and what happened on top, the input line below.
func (m Model) prompt(width int) string {
	where := name.Render(orDefault(m.info.Node, "local")) + dim.Render(" › ") + tab_.Render(tabNames[m.tab])
	state := m.state()
	gap := max(1, width-lipgloss.Width(where)-lipgloss.Width(state)-8)
	first := border.Render("╭─ ") + where + " " + border.Render(strings.Repeat("─", gap)) + " " + state + border.Render(" ─╮")

	char := name.Render("❯ ")
	if m.statusErr {
		char = down.Render("❯ ")
	}
	var line string
	switch {
	case m.wizard != nil:
		line = section.Render(wizardQuestions[m.wizard.step]) + m.input.View(m.ghost())
	case m.mode == inputCommand:
		line = m.input.View(m.ghost())
	case m.mode == inputFilter:
		line = dim.Render("filter: ") + m.input.View("")
	case m.filter[m.tab] != "":
		line = dim.Render("filter: ") + text.Render(m.filter[m.tab]) + dim.Render("  (i to edit)")
	default:
		line = dim.Render("/ for commands · i to filter")
	}
	inner := width - 8
	line = lipgloss.NewStyle().MaxWidth(inner).Render(line)
	second := border.Render("╰─ ") + char + line + strings.Repeat(" ", max(0, inner-lipgloss.Width(line))) + border.Render(" ─╯")
	return first + "\n" + second
}

func (m Model) state() string {
	switch {
	case m.busy:
		return section.Render("working…")
	case m.status != "" && m.statusErr:
		return down.Render(truncate("✗ "+m.status, 48))
	case m.status != "":
		return up.Render(truncate("✔ "+m.status, 48))
	}
	live := 0
	for _, a := range m.snap.Agents {
		if a.Live {
			live++
		}
	}
	return dim.Render(fmt.Sprintf("%d agents · %d live · %d messages", len(m.snap.Agents), live, len(m.events)))
}

// ghost is the rest of the highlighted suggestion, drawn faint after the
// cursor the way zsh-autosuggestions does.
func (m Model) ghost() string {
	options := m.suggestions()
	if len(options) == 0 {
		return ""
	}
	line := m.input.Value()
	word := line[strings.LastIndex(line, " ")+1:]
	pick := options[min(m.pick, len(options)-1)].Value
	if strings.HasPrefix(pick, word) {
		return pick[len(word):]
	}
	return ""
}

// menu lists the completions above the prompt, like zsh's menu select.
func (m Model) menu(width int) string {
	options := m.suggestions()
	if len(options) == 0 {
		return ""
	}
	pick := min(m.pick, len(options)-1)
	start := max(0, pick-menuRows+1)
	valueW := 0
	for _, o := range options {
		valueW = max(valueW, lipgloss.Width(o.Value))
	}
	valueW = min(valueW, width/2)
	var lines []string
	for i := start; i < len(options) && i < start+menuRows; i++ {
		v := fmt.Sprintf(" %-*s ", valueW, truncate(options[i].Value, valueW))
		if i == pick {
			v = selectedRow.Render(v)
		} else {
			v = name.Render(v)
		}
		lines = append(lines, "   "+v+" "+dim.Render(truncate(options[i].Desc, max(10, width-valueW-8))))
	}
	if more := len(options) - (start + menuRows); more > 0 {
		lines = append(lines, "   "+dim.Render(fmt.Sprintf("… +%d more", more)))
	}
	return strings.Join(lines, "\n")
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func (m Model) tabs(width int) string {
	parts := make([]string, tabCount)
	for t := tabID(0); t < tabCount; t++ {
		label := tabNames[t]
		if waiting := len(m.snap.Pending) + len(m.signins); t == tabGuests && waiting > 0 {
			label += fmt.Sprintf(" (%d waiting)", waiting)
		}
		if t == m.tab {
			parts[t] = tab_.Render(label)
		} else {
			parts[t] = text.Render(label)
		}
	}
	left := " " + strings.Join(parts, border.Render(" │ "))
	right := dim.Render("agentnet " + m.info.Version)
	if m.info.Node != "" {
		right = name.Render(m.info.Node) + dim.Render(" · agentnet "+m.info.Version)
	}
	gap := width - lipgloss.Width(left) - lipgloss.Width(right) - 1
	if gap < 2 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m Model) body(width, height int) string {
	switch {
	case m.secret != nil:
		return m.secretView(width)
	case m.detail != nil:
		return m.detailView(width)
	case (m.tab == tabNodes || m.tab == tabGuests) && !m.loaded:
		return " " + dim.Render("loading…")
	case m.tab == tabSessions && m.sessionsErr != nil:
		return " " + down.Render(m.sessionsErr.Error())
	case m.tab == tabNodes && m.relayErr != nil:
		return " " + down.Render("relay: "+m.relayErr.Error()) + "\n " + dim.Render("set one up with agentnet relay init --url <url> and agentnet relay admin-token")
	}
	return " " + strings.ReplaceAll(m.table(m.tab).render(width, height, m.cursor[m.tab]), "\n", "\n ")
}

// secretView shows a new token once. Closing it drops the only copy watch has.
func (m Model) secretView(width int) string {
	lines := []string{
		warn.Render("Shown once. It is not saved anywhere by watch; copy it now."),
		"",
		bold.Render(m.secret.token),
		"",
		text.Render(m.secret.hint),
		"",
		dim.Render("Enter or Esc closes this."),
	}
	return box(m.secret.title, wrapAll(lines, width-2), width)
}

func (m Model) detailView(width int) string {
	e := m.detail
	head := name.Render(e.From) + dim.Render(" → ") + name.Render(e.To) + dim.Render("  "+clock(e.TS))
	if e.Origin == "external" {
		head += warn.Render("  external")
	}
	lines := append([]string{head, ""}, wrapAll(strings.Split(e.Text, "\n"), width-2)...)
	return box("Message", append(lines, "", dim.Render("Enter or Esc closes this.")), width)
}

// keybar is a nano-style line at the very bottom: only the keys that work
// right now, so it changes with the tab and with what is open.
func (m Model) keybar(width int) string {
	var keys []hotkey
	switch {
	case m.confirm != nil:
		return warn.Render(truncate(" "+m.confirm.prompt, width)) + "\n" + renderKeys(confirmKeys, width)
	case m.secret != nil || m.detail != nil:
		keys = overlayKeys
	case m.mode == inputCommand:
		keys = commandKeys
	case m.mode == inputFilter:
		keys = filterKeys
	default:
		keys = append(append(append([]hotkey{}, navKeys...), tabKeys[m.tab]...), toolKeys...)
	}
	return renderKeys(keys, width)
}

func renderKeys(keys []hotkey, width int) string {
	var lines []string
	cur := ""
	for _, k := range keys {
		item := keycap.Render(k.key) + " " + dim.Render(k.label)
		if cur != "" && lipgloss.Width(cur)+2+lipgloss.Width(item) > width {
			lines = append(lines, cur)
			cur = ""
		}
		if cur != "" {
			cur += "  "
		}
		cur += item
	}
	return strings.Join(append(lines, cur), "\n")
}

// box draws a square border with its title set into the top edge.
func box(title string, lines []string, width int) string {
	inner := width - 2
	top := border.Render("┌") + text.Render(title) + border.Render(strings.Repeat("─", max(0, inner-lipgloss.Width(title)))+"┐")
	out := []string{top}
	for _, l := range lines {
		l = lipgloss.NewStyle().MaxWidth(inner).Render(l)
		out = append(out, border.Render("│")+l+strings.Repeat(" ", max(0, inner-lipgloss.Width(l)))+border.Render("│"))
	}
	return strings.Join(append(out, border.Render("└"+strings.Repeat("─", inner)+"┘")), "\n")
}

func wrapAll(lines []string, width int) []string {
	var out []string
	for _, l := range lines {
		out = append(out, strings.Split(lipgloss.NewStyle().Width(width).Render(l), "\n")...)
	}
	return out
}
