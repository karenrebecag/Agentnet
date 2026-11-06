//go:build unix

package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

type tabID int

const (
	tabAgents tabID = iota
	tabNodes
	tabGuests
	tabMessages
	tabCount
)

var tabNames = [tabCount]string{"Agents", "Nodes", "Guests", "Messages"}

// cell is one table value: plain text for width and filtering, a style to draw it.
type cell struct {
	text  string
	style lipgloss.Style
}

type table struct {
	head []string
	rows [][]cell
	// keys identify each row, so the selection survives a refresh that reorders.
	keys []string
}

func c(text string, style lipgloss.Style) cell { return cell{text: text, style: style} }

func (m Model) table(tab tabID) table {
	var t table
	switch tab {
	case tabAgents:
		// One list: agents in the network, then CLIs open on this machine that
		// have not joined. An open CLI is an agent that has not said hello yet.
		t.head = []string{"Name", "State", "Kind", "Unread", "External", "Terminal"}
		terminal := map[string]string{}
		for _, s := range m.sessions {
			if s.Agent != "" {
				terminal[s.Agent] = s.TTY
			}
		}
		for _, a := range m.snap.Agents {
			state := c("offline", down)
			if a.Live {
				state = c("live", up)
			}
			unread := c("", dim)
			if a.Unread > 0 {
				unread = c(fmt.Sprint(a.Unread), tab_)
			}
			ext := c("no", dim)
			if a.External {
				ext = c("yes", warn)
			}
			t.add("agent/"+a.Name, c(a.Name, name), state, c(a.Kind, text), unread, ext, c(terminal[a.Name], dim))
		}
		for _, s := range m.sessions {
			if s.Agent == "" {
				t.add(fmt.Sprintf("session/%d", s.PID), c(s.Name, text), c("not joined", warn), c(s.Tool, text), c("", dim), c("", dim), c(s.TTY, dim))
			}
		}
	case tabNodes:
		t.head = []string{"Name", "State", "Last seen", "Agents"}
		for _, n := range m.nodes {
			label := n.Name
			if n.Name == m.info.Node {
				label += " (this machine)"
			}
			state := c("active", up)
			if n.Revoked {
				state = c("revoked", down)
			}
			agents := make([]string, 0, len(n.Agents))
			for _, a := range n.Agents {
				agents = append(agents, a.Name)
			}
			t.add(n.Name, c(label, name), state, c(ago(n.LastSeen, m.now), dim), c(strings.Join(agents, ", "), text))
		}
	case tabGuests:
		t.head = []string{"Name", "Via", "State", "Can ask"}
		for _, s := range m.signins {
			t.add("signin/"+s.ID, c(s.Guest, name), c("sign-in", text), c("waiting "+s.ID, warn), c("from "+s.Host+" · says it is "+s.ClientName, warn))
		}
		for _, g := range m.guests {
			t.add(string(g.Via)+"/"+g.Name, c(g.Name, name), c(string(g.Via), text), c(g.State, guestStyle(g.State)), c(strings.Join(g.Targets, ", "), text))
		}
	case tabMessages:
		t.head = []string{"Time", "From", "To", "Message"}
		for i, e := range m.events {
			from := c(e.From, name)
			if e.Origin == "external" {
				from = c(e.From, warn)
			}
			if e.Corrupt {
				from = c("unreadable", down)
			}
			t.add(fmt.Sprint(i), c(clock(e.TS), dim), from, c(e.To, name), c(oneLine(e.Text), text))
		}
	}
	return t.filtered(m.filter[tab])
}

func (t *table) add(key string, cells ...cell) {
	t.keys = append(t.keys, key)
	t.rows = append(t.rows, cells)
}

func (t table) filtered(query string) table {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return t
	}
	out := table{head: t.head}
	for i, row := range t.rows {
		var all []string
		for _, cl := range row {
			all = append(all, cl.text)
		}
		if strings.Contains(strings.ToLower(strings.Join(all, " ")), query) {
			out.rows = append(out.rows, row)
			out.keys = append(out.keys, t.keys[i])
		}
	}
	return out
}

// render draws the header and the rows that fit, keeping the cursor in view.
func (t table) render(width, height, cursor int) string {
	if len(t.head) == 0 {
		return ""
	}
	const marker = 3
	widths := make([]int, len(t.head))
	for i, h := range t.head {
		widths[i] = len(h)
	}
	for _, row := range t.rows {
		for i, cl := range row {
			widths[i] = max(widths[i], lipgloss.Width(cl.text))
		}
	}
	// Every column but the last is capped; the last one takes what is left.
	used := marker
	for i := range widths[:len(widths)-1] {
		widths[i] = min(widths[i], 24)
		used += widths[i] + 2
	}
	widths[len(widths)-1] = max(8, width-used)

	lines := []string{strings.Repeat(" ", marker) + joinCells(headCells(t.head), widths, false)}
	visible := max(1, height-1)
	start := 0
	if cursor >= visible {
		start = cursor - visible + 1
	}
	for i := start; i < len(t.rows) && i < start+visible; i++ {
		selected := i == cursor
		prefix := "   "
		if selected {
			prefix = selectedRow.Render(">> ")
		}
		lines = append(lines, prefix+joinCells(t.rows[i], widths, selected))
	}
	if len(t.rows) == 0 {
		lines = append(lines, strings.Repeat(" ", marker)+dim.Render("nothing here"))
	}
	return strings.Join(lines, "\n")
}

func headCells(head []string) []cell {
	out := make([]cell, len(head))
	for i, h := range head {
		out[i] = c(h, bold)
	}
	return out
}

func joinCells(cells []cell, widths []int, selected bool) string {
	var b strings.Builder
	for i, cl := range cells {
		txt := fmt.Sprintf("%-*s", widths[i], truncate(cl.text, widths[i]))
		style := cl.style
		if selected {
			style = selectedRow
		}
		b.WriteString(style.Render(txt))
		if i < len(cells)-1 {
			b.WriteString(sel(selected, "  "))
		}
	}
	return b.String()
}

func sel(selected bool, s string) string {
	if selected {
		return selectedRow.Render(s)
	}
	return s
}

func guestStyle(state string) lipgloss.Style {
	switch state {
	case "active", "approved":
		return up
	case "pending":
		return warn
	case "revoked":
		return down
	}
	return text
}

func ago(ts string, now time.Time) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return "never"
	}
	d := now.Sub(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func clock(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ""
	}
	return t.Local().Format("15:04:05")
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func truncate(s string, width int) string {
	r := []rune(s)
	if width > 1 && len(r) > width {
		return string(r[:width-1]) + "…"
	}
	return s
}
