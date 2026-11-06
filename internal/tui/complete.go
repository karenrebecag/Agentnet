//go:build unix

package tui

import (
	"sort"
	"strings"
)

// suggestion replaces the word being typed. Desc explains it in the menu.
type suggestion struct {
	Value string
	Desc  string
}

// commandSpec is one command and how to complete each of its arguments.
type commandSpec struct {
	Name  string
	Usage string
	Desc  string
}

var commandSpecs = []commandSpec{
	{"node", "<name>", "add a machine"},
	{"guest", "relay|expose <name> <targets>", "invite a guest"},
	{"approve", "<guest>", "approve a pending expose guest"},
	{"rotate", "<node>", "new token for a node"},
	{"revoke", "node|guest …", "revoke a node or guest"},
}

// completions are what the screen already knows: nothing here makes a request.
type completions struct {
	machines     []string // relay machine nodes, not revoked
	relayGuests  []string
	exposeGuests []string
	pending      []string // expose guests that can be approved
	addresses    []string // node.agent on the relay
	localAgents  []string
}

func (m Model) completions() completions {
	var c completions
	for _, n := range m.nodes {
		if n.Revoked {
			continue
		}
		c.machines = append(c.machines, n.Name)
		for _, a := range n.Agents {
			c.addresses = append(c.addresses, n.Name+"."+a.Name)
		}
	}
	for _, g := range m.guests {
		if g.State == "revoked" {
			continue
		}
		if g.Via == ViaRelay {
			c.relayGuests = append(c.relayGuests, g.Name)
			continue
		}
		c.exposeGuests = append(c.exposeGuests, g.Name)
		if g.State == "pending" || g.State == "created" {
			c.pending = append(c.pending, g.Name)
		}
	}
	for _, a := range m.snap.Agents {
		c.localAgents = append(c.localAgents, a.Name)
	}
	return c
}

// suggest lists what can complete the last word of line. Free-form words, such
// as the name of something new, have no suggestions.
func (c completions) suggest(line string) []suggestion {
	if !strings.HasPrefix(line, "/") {
		return nil
	}
	words := strings.Split(strings.TrimPrefix(line, "/"), " ")
	last := words[len(words)-1]
	if len(words) == 1 {
		var out []suggestion
		for _, s := range commandSpecs {
			out = append(out, suggestion{"/" + s.Name, s.Usage + " · " + s.Desc})
		}
		return filter(out, "/"+last)
	}
	pos := len(words) - 1
	switch words[0] {
	case "guest":
		switch {
		case pos == 1:
			return filter(fixed("relay", "reaches agents on any linked machine", "expose", "reaches this machine through a quick tunnel"), last)
		case pos == 3 && words[1] == "relay":
			return filterList(c.addresses, last, "agent on the relay")
		case pos == 3 && words[1] == "expose":
			return filterList(c.localAgents, last, "local agent")
		}
	case "approve":
		if pos == 1 {
			return filter(plain(c.pending, "pending expose guest"), last)
		}
	case "rotate":
		if pos == 1 {
			return filter(plain(c.machines, "relay node"), last)
		}
	case "revoke":
		switch {
		case pos == 1:
			return filter(fixed("node", "a machine on the relay", "guest", "a relay or expose guest"), last)
		case pos == 2 && words[1] == "node":
			return filter(plain(c.machines, "relay node"), last)
		case pos == 2 && words[1] == "guest":
			return filter(fixed("relay", "guest of the relay", "expose", "guest of expose"), last)
		case pos == 3 && words[1] == "guest" && words[2] == "relay":
			return filter(plain(c.relayGuests, "relay guest"), last)
		case pos == 3 && words[1] == "guest" && words[2] == "expose":
			return filter(plain(c.exposeGuests, "expose guest"), last)
		}
	}
	return nil
}

// filterList completes the last entry of a comma-separated list, the way a
// shell completes the last path segment, and skips entries already listed.
func filterList(all []string, word, desc string) []suggestion {
	done := strings.Split(word, ",")
	head, tail := strings.Join(done[:len(done)-1], ","), done[len(done)-1]
	if head != "" {
		head += ","
	}
	taken := map[string]bool{}
	for _, d := range done[:len(done)-1] {
		taken[d] = true
	}
	var out []suggestion
	for _, v := range all {
		if !taken[v] && strings.HasPrefix(v, tail) {
			out = append(out, suggestion{head + v, desc})
		}
	}
	return out
}

func filter(all []suggestion, word string) []suggestion {
	var out []suggestion
	for _, s := range all {
		if strings.HasPrefix(s.Value, word) && s.Value != word {
			out = append(out, s)
		}
	}
	return out
}

func plain(values []string, desc string) []suggestion {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	out := make([]suggestion, len(sorted))
	for i, v := range sorted {
		out[i] = suggestion{v, desc}
	}
	return out
}

func fixed(pairs ...string) []suggestion {
	out := make([]suggestion, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, suggestion{pairs[i], pairs[i+1]})
	}
	return out
}

// accept puts s in place of the last word. A finished word gets a space so the
// next argument can start; a list stays open for another comma.
func accept(line string, s suggestion, list bool) string {
	i := strings.LastIndex(line, " ")
	out := line[:i+1] + s.Value
	if !list {
		out += " "
	}
	return out
}

// completingList says whether the word being typed is a comma-separated list.
func completingList(line string) bool {
	words := strings.Split(strings.TrimPrefix(line, "/"), " ")
	return len(words) == 4 && words[0] == "guest"
}
