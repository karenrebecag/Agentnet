//go:build unix

// Package sessions finds the agent CLIs open on this machine, so watch can
// offer them a line to join the bus. It only reads the process table; it never
// writes into another program.
package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Tools are the CLIs watch recognizes, by the name of their executable.
var Tools = map[string]bool{
	"claude": true, "grok": true, "codex": true, "gemini": true, "aider": true,
	"opencode": true, "cursor-agent": true, "qwen": true, "amp": true, "goose": true,
}

// Runtimes run a CLI written as a script; the tool is then the script's name.
var runtimes = map[string]bool{"node": true, "bun": true, "deno": true, "python": true, "python3": true}

type Proc struct {
	PID, PPID int
	TTY       string
	Args      string
}

// Session is one open CLI. Agent is the agentnet name it joined as, or "".
type Session struct {
	PID   int
	Tool  string
	Name  string
	TTY   string
	State string
	Agent string
}

// ParsePS reads `ps -axo pid=,ppid=,tty=,args=`.
func ParsePS(out string) []Proc {
	var procs []Proc
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		procs = append(procs, Proc{PID: pid, PPID: ppid, TTY: f[2], Args: strings.Join(f[3:], " ")})
	}
	return procs
}

// ToolOf names the CLI a process runs, or "".
func ToolOf(args string) string {
	f := strings.Fields(args)
	if len(f) == 0 {
		return ""
	}
	base := strings.TrimSuffix(filepath.Base(f[0]), ".js")
	if runtimes[base] && len(f) > 1 {
		base = strings.TrimSuffix(filepath.Base(f[1]), ".js")
	}
	if Tools[base] {
		return base
	}
	return ""
}

// ClaudeInfo is what `claude agents --json` knows about an open window.
type ClaudeInfo struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	PID    int    `json:"pid"`
}

// Find lists the CLIs with a terminal. joined maps the pid of each live
// `agentnet join` to its agent name; a join belongs to the session it was
// started from, which is one of its ancestors.
func Find(procs []Proc, claude map[int]ClaudeInfo, joined map[int]string) []Session {
	parent := map[int]int{}
	for _, p := range procs {
		parent[p.PID] = p.PPID
	}
	owner := map[int]string{}
	for pid, agent := range joined {
		// The chain is short; the cap only guards against a cycle in a racy table.
		for cur, hops := pid, 0; cur > 1 && hops < 32; cur, hops = parent[cur], hops+1 {
			owner[cur] = agent
		}
	}
	var out []Session
	for _, p := range procs {
		tool := ToolOf(p.Args)
		// No terminal means a headless run, such as claude -p: nobody to paste into.
		if tool == "" || p.TTY == "??" || p.TTY == "?" || p.TTY == "-" {
			continue
		}
		// A CLI started by another CLI of the same tool is a helper, not a session.
		if ToolOf(argsOf(procs, p.PPID)) == tool {
			continue
		}
		s := Session{PID: p.PID, Tool: tool, TTY: p.TTY, State: "open", Agent: owner[p.PID]}
		if c, ok := claude[p.PID]; ok {
			s.Name, s.State = c.Name, c.Status
		}
		if s.Name == "" {
			s.Name = tool + "-" + strings.TrimPrefix(p.TTY, "ttys")
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Agent == "") != (out[j].Agent == "") {
			return out[i].Agent != ""
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func argsOf(procs []Proc, pid int) string {
	for _, p := range procs {
		if p.PID == pid {
			return p.Args
		}
	}
	return ""
}

// Discover reads the process table and, when Claude Code is installed, the
// names of its open windows.
func Discover(ctx context.Context, joined map[int]string) ([]Session, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,tty=,args=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	return Find(ParsePS(string(out)), claudeWindows(ctx), joined), nil
}

func claudeWindows(ctx context.Context) map[int]ClaudeInfo {
	out := map[int]ClaudeInfo{}
	raw, err := exec.CommandContext(ctx, "claude", "agents", "--json").Output()
	if err != nil {
		return out
	}
	var list []ClaudeInfo
	if json.Unmarshal(raw, &list) == nil {
		for _, c := range list {
			out[c.PID] = c
		}
	}
	return out
}

// AgentName turns a session's name into a valid agentnet name.
func AgentName(s Session) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s.Name) {
		r = fold(r)
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.'):
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	name := strings.Trim(b.String(), "-.")
	if len(name) > 40 {
		name = strings.TrimRight(name[:40], "-.")
	}
	if name == "" {
		return s.Tool
	}
	return name
}

func fold(r rune) rune {
	if f, ok := map[rune]rune{'á': 'a', 'é': 'e', 'í': 'i', 'ó': 'o', 'ú': 'u', 'ü': 'u', 'ñ': 'n', 'à': 'a', 'è': 'e', 'ç': 'c'}[r]; ok {
		return f
	}
	return r
}

// JoinLine is what the person pastes into the session. It reads the same for
// every CLI: the session runs the command itself, nothing is typed into it.
func JoinLine(s Session) string {
	return fmt.Sprintf("Join agentnet: run `agentnet join %s --kind %s` in the background and keep reading what it prints; each line is a message for you.",
		AgentName(s), s.Tool)
}

// Copy puts text on the system clipboard.
func Copy(text string) error {
	for _, c := range [][]string{{"pbcopy"}, {"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}} {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin = strings.NewReader(text)
		return cmd.Run()
	}
	return errors.New("no clipboard tool found (pbcopy, wl-copy, xclip or xsel)")
}
