//go:build unix

package sessions

import "testing"

const table = `
  1     0 ??       /sbin/launchd
 100    1 ttys000  -zsh
 101  100 ttys000  claude
 102  101 ttys000  /bin/zsh -c agentnet join orq --kind claude
 103  102 ttys000  agentnet join orq --kind claude
 200    1 ttys011  /opt/homebrew/bin/grok
 300    1 ttys004  node /usr/local/bin/gemini --yolo
 400    1 ??       claude -p --allowedTools SendMessage
 500    1 ttys007  vim notes.md
`

func TestFindMarksTheSessionAJoinCameFrom(t *testing.T) {
	got := Find(ParsePS(table), map[int]ClaudeInfo{101: {Name: "Nueva cotización PDF", Status: "busy", PID: 101}}, map[int]string{103: "orq"})
	want := []Session{
		{PID: 101, Tool: "claude", Name: "Nueva cotización PDF", TTY: "ttys000", State: "busy", Agent: "orq"},
		{PID: 300, Tool: "gemini", Name: "gemini-004", TTY: "ttys004", State: "open"},
		{PID: 200, Tool: "grok", Name: "grok-011", TTY: "ttys011", State: "open"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if n := AgentName(got[0]); n != "nueva-cotizacion-pdf" {
		t.Fatalf("agent name %q", n)
	}
}
