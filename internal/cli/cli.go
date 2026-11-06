//go:build unix

// Package cli implements the agentnet command line.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/karenrebecag/agentnet/internal/core"
	"github.com/karenrebecag/agentnet/internal/relay"
)

const (
	ExitOK        = 0
	ExitError     = 1
	ExitOffline   = 2
	ExitNameTaken = 3

	pollEvery   = 300 * time.Millisecond
	eventPrefix = "AGENTNET "
	usage       = `usage: agentnet <command> [args]

  join  [name] [--kind K] [--accept-external]
                                    register and stream incoming messages to stdout
  send  <to> [--from N] [--message T] [--reply-to ID]
                                    send a message (text from --message or stdin)
  read  [name]                      print unread messages once
  list                              show registered agents
  watch                             live, read-only view of agents and messages
  leave [name]                      drop a registration left behind
  init  [--install-claude-skill] [--force]
                                    create the data directory and print join commands

  guests add <name> --can-ask a,b   create a remote guest and print its token once
  guests list|approve|revoke <name> manage guests (approve only works here, locally)
  expose [--ttl 2h]                 publish an MCP endpoint through a Cloudflare quick tunnel
  expose stop                       kill switch for a running expose
  link                              relay session (reads relay.json, never the admin token)
  relay init --url https://...     save the relay url
  relay admin-token                 write an admin token and print it once
  relay node add|list|rotate|revoke manage relay nodes

AGENTNET_NAME sets the default name, AGENTNET_HOME the data directory.
Exit codes: 0 ok, 1 error, 2 recipient offline (queued), 3 name already live.
`
)

var Version = "dev"

type env struct {
	store  core.Store
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	home, err := core.DefaultHome()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitError
	}
	e := env{store: core.Store{Home: home}, stdin: stdin, stdout: stdout, stderr: stderr}
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return ExitError
	}
	cmd, rest := args[0], args[1:]
	var code int
	switch cmd {
	case "join":
		code, err = e.join(rest)
	case "send":
		code, err = e.send(rest)
	case "read":
		code, err = e.read(rest)
	case "list":
		code, err = e.list()
	case "leave":
		code, err = e.leave(rest)
	case "watch":
		code, err = e.watch()
	case "init":
		code, err = e.init(rest)
	case "guests":
		code, err = e.guests(rest)
	case "expose":
		code, err = e.expose(rest)
	case "link":
		code, err = e.link(rest)
	case "relay":
		code, err = e.relay(rest)
	case "version", "--version":
		fmt.Fprintln(stdout, Version)
		return ExitOK
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return ExitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
		return ExitError
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		if errors.Is(err, core.ErrNameTaken) {
			return ExitNameTaken
		}
		if code == ExitOK {
			code = ExitError
		}
	}
	return code
}

// parse accepts flags before or after positionals, as `join alice --kind claude`.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positionals, nil
		}
		positionals = append(positionals, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func nameArg(positionals []string) (string, error) {
	name := os.Getenv("AGENTNET_NAME")
	if len(positionals) > 0 {
		name = positionals[0]
	}
	if name == "" {
		return "", errors.New("missing agent name (argument or AGENTNET_NAME)")
	}
	return name, core.ValidateName(name)
}

func (e env) emit(v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(e.stdout, "%s%s\n", eventPrefix, line)
	return err
}

func (e env) deliver(name string) error {
	batch, err := e.store.Peek(name)
	if err != nil || len(batch.Messages) == 0 {
		return err
	}
	for _, msg := range batch.Messages {
		if err := e.emit(msg); err != nil {
			return err
		}
	}
	return e.store.Commit(name, batch)
}

func (e env) join(args []string) (int, error) {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	kind := fs.String("kind", "agent", "claude, grok, codex... informative only")
	acceptExternal := fs.Bool("accept-external", false, "let exposed guests message this agent")
	positionals, err := parse(fs, args)
	if err != nil {
		return ExitError, err
	}
	name, err := nameArg(positionals)
	if err != nil {
		return ExitError, err
	}
	if err := e.store.Register(name, *kind, *acceptExternal); err != nil {
		return ExitError, err
	}
	defer e.store.Unregister(name)
	if err := e.emit(map[string]string{"joined": name, "kind": *kind}); err != nil {
		return ExitError, err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	for {
		if err := e.deliver(name); err != nil {
			return ExitError, err
		}
		if err := e.store.Heartbeat(name); err != nil {
			return ExitError, err
		}
		select {
		case <-ctx.Done():
			return ExitOK, nil
		case <-ticker.C:
		}
	}
}

func (e env) send(args []string) (int, error) {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	from := fs.String("from", os.Getenv("AGENTNET_NAME"), "sender name")
	message := fs.String("message", "", "message text (default: stdin)")
	replyTo := fs.String("reply-to", "", "id of the message this answers")
	positionals, err := parse(fs, args)
	if err != nil {
		return ExitError, err
	}
	if len(positionals) != 1 {
		return ExitError, errors.New("send needs exactly one recipient")
	}
	text := *message
	if text == "" {
		raw, err := io.ReadAll(io.LimitReader(e.stdin, core.MaxTextBytes+1))
		if err != nil {
			return ExitError, err
		}
		text = string(raw)
	}
	if *from == "" {
		return ExitError, errors.New("missing --from (or AGENTNET_NAME)")
	}
	env, err := e.store.SendWith(positionals[0], *from, text, core.SendOptions{ReplyTo: *replyTo})
	if err != nil {
		return ExitError, err
	}
	if e.store.IsLive(env.To, time.Now()) {
		fmt.Fprintf(e.stdout, "delivered %s -> %s\n", env.ID, env.To)
		return ExitOK, nil
	}
	// A remote address is not a local agent that will join. link forwards it.
	note := "offline, delivered on next join/read"
	if _, _, err := relay.ParseAddress(env.To); err == nil {
		note = "via relay"
	}
	fmt.Fprintf(e.stderr, "queued %s -> %s (%s)\n", env.ID, env.To, note)
	return ExitOffline, nil
}

func (e env) read(args []string) (int, error) {
	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	positionals, err := parse(fs, args)
	if err != nil {
		return ExitError, err
	}
	name, err := nameArg(positionals)
	if err != nil {
		return ExitError, err
	}
	return ExitOK, e.deliver(name)
}

func (e env) list() (int, error) {
	agents, err := e.store.List(time.Now())
	if err != nil {
		return ExitError, err
	}
	for _, a := range agents {
		status := "offline"
		if a.Live {
			status = "live"
		}
		fmt.Fprintf(e.stdout, "%s\t%s\t%s\t%s\n", a.Name, a.Kind, status, a.CWD)
	}
	return ExitOK, nil
}

func (e env) leave(args []string) (int, error) {
	fs := flag.NewFlagSet("leave", flag.ContinueOnError)
	positionals, err := parse(fs, args)
	if err != nil {
		return ExitError, err
	}
	name, err := nameArg(positionals)
	if err != nil {
		return ExitError, err
	}
	return ExitOK, e.store.Leave(name)
}
