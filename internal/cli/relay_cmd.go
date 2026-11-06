//go:build unix

package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"unicode"

	"github.com/karenrebecag/agentnet/internal/relay"
)

func (e env) relay(args []string) (int, error) {
	if len(args) == 0 {
		return ExitError, fmt.Errorf("relay needs a subcommand\n\n%s", usage)
	}
	switch args[0] {
	case "init":
		return e.relayInit(args[1:])
	case "admin-token":
		return e.relayAdminToken(args[1:])
	case "node":
		return e.relayNode(args[1:])
	case "signins":
		return e.relaySignins(args[1:])
	default:
		return ExitError, fmt.Errorf("unknown relay command %q", args[0])
	}
}

func (e env) relayInit(args []string) (int, error) {
	fs := flag.NewFlagSet("relay init", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	raw := fs.String("url", "", "https url of the relay")
	if _, err := parse(fs, args); err != nil {
		return ExitError, err
	}
	if *raw == "" {
		return ExitError, fmt.Errorf("relay init needs --url")
	}
	if err := relay.Init(e.store.Home, *raw); err != nil {
		return ExitError, err
	}
	fmt.Fprintln(e.stdout, "relay url saved")
	return ExitOK, nil
}

func (e env) relayAdminToken(args []string) (int, error) {
	fs := flag.NewFlagSet("relay admin-token", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	if _, err := parse(fs, args); err != nil {
		return ExitError, err
	}
	tok, err := relay.WriteAdminToken(e.store.Home)
	if err != nil {
		return ExitError, err
	}
	fmt.Fprintln(e.stdout, tok)
	return ExitOK, nil
}

func (e env) relayNode(args []string) (int, error) {
	if len(args) == 0 {
		return ExitError, fmt.Errorf("relay node needs add, list, pair, rotate or revoke")
	}
	switch args[0] {
	case "add":
		return e.relayNodeAdd(args[1:])
	case "pair":
		return e.relayNodePair(args[1:])
	case "list":
		return e.relayNodeList(args[1:])
	case "rotate":
		return e.relayNodeRotate(args[1:])
	case "revoke":
		return e.relayNodeRevoke(args[1:])
	default:
		return ExitError, fmt.Errorf("unknown relay node command %q", args[0])
	}
}

func (e env) relayNodeAdd(args []string) (int, error) {
	fs := flag.NewFlagSet("relay node add", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	kind := fs.String("kind", "", "machine or guest")
	targets := fs.String("targets", "", "comma-separated addresses a guest may ask")
	inbox := fs.Bool("inbox", false, "guest accepts text from its targets without an ask")
	pos, err := parse(fs, args)
	if err != nil {
		return ExitError, err
	}
	if len(pos) != 1 {
		return ExitError, fmt.Errorf("relay node add needs a name")
	}
	var list []string
	if *targets != "" {
		for _, part := range strings.Split(*targets, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				list = append(list, part)
			}
		}
	}
	tok, saved, err := relay.AddNodeOpts(context.Background(), e.store.Home, pos[0], *kind, list, *inbox)
	if tok != "" {
		fmt.Fprintln(e.stdout, tok)
	}
	if err != nil {
		return ExitError, err
	}
	if saved {
		fmt.Fprintf(e.stdout, "saved %s\n", pos[0])
	}
	return ExitOK, nil
}

func (e env) relayNodePair(args []string) (int, error) {
	fs := flag.NewFlagSet("relay node pair", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	host := fs.String("for", "", "hostname to bind the code to; without it you approve the host when the client signs in")
	pos, err := parse(fs, args)
	if err != nil {
		return ExitError, err
	}
	if len(pos) != 1 {
		return ExitError, fmt.Errorf("relay node pair needs a guest name")
	}
	p, err := relay.PairNode(context.Background(), e.store.Home, pos[0], *host)
	if err != nil {
		return ExitError, err
	}
	// The code is a one-time secret. Stdout is the only copy.
	fmt.Fprintln(e.stdout, p.Code)
	if p.ConnectorURL != "" {
		fmt.Fprintln(e.stdout, p.ConnectorURL)
	}
	if p.Expires != "" {
		fmt.Fprintln(e.stdout, p.Expires)
	}
	return ExitOK, nil
}

// relaySignins lists the clients waiting for approval, or answers one.
func (e env) relaySignins(args []string) (int, error) {
	ctx := context.Background()
	if len(args) == 0 || args[0] == "list" {
		list, err := relay.ListSignins(ctx, e.store.Home)
		if err != nil {
			return ExitError, err
		}
		for _, s := range list {
			fmt.Fprintf(e.stdout, "%s\t%s\tas=%s\tclient=%s\tip=%s\texpires=%s\n", s.ID, printable(s.Host), s.Guest, printable(s.ClientName), s.IP, s.Expires)
		}
		return ExitOK, nil
	}
	if len(args) != 2 || (args[0] != "approve" && args[0] != "deny") {
		return ExitError, fmt.Errorf("relay signins needs list, approve <id> or deny <id>")
	}
	// Approving by id alone would hide where the client returns; say it first.
	list, err := relay.ListSignins(ctx, e.store.Home)
	if err != nil {
		return ExitError, err
	}
	var host, guest string
	for _, s := range list {
		if s.ID == args[1] {
			host, guest = printable(s.Host), s.Guest
		}
	}
	if host == "" {
		return ExitError, fmt.Errorf("no sign-in is waiting with id %s", args[1])
	}
	if err := relay.DecideSignin(ctx, e.store.Home, args[1], args[0] == "approve"); err != nil {
		return ExitError, err
	}
	done := map[string]string{"approve": "approved", "deny": "denied"}[args[0]]
	fmt.Fprintf(e.stdout, "%s %s: %s as %s\n", done, args[1], host, guest)
	return ExitOK, nil
}

func (e env) relayNodeList(args []string) (int, error) {
	fs := flag.NewFlagSet("relay node list", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	if _, err := parse(fs, args); err != nil {
		return ExitError, err
	}
	nodes, err := relay.ListNodes(context.Background(), e.store.Home)
	if err != nil {
		return ExitError, err
	}
	for _, n := range nodes {
		var agents []string
		for _, a := range n.Agents {
			agents = append(agents, a.Name)
		}
		fmt.Fprintf(e.stdout, "%s\t%s\trevoked=%t\ttargets=%s\tcreated=%s\tlast_seen=%s\tagents=%s\n",
			n.Name, n.Kind, n.Revoked, strings.Join(n.Targets, ","), n.Created, n.LastSeen, strings.Join(agents, ","))
	}
	return ExitOK, nil
}

func (e env) relayNodeRotate(args []string) (int, error) {
	name, err := e.oneName("relay node rotate", args)
	if err != nil {
		return ExitError, err
	}
	tok, err := relay.RotateNode(context.Background(), e.store.Home, name)
	if tok != "" {
		fmt.Fprintln(e.stdout, tok)
	}
	if err != nil {
		return ExitError, err
	}
	return ExitOK, nil
}

func (e env) relayNodeRevoke(args []string) (int, error) {
	name, err := e.oneName("relay node revoke", args)
	if err != nil {
		return ExitError, err
	}
	if err := relay.RevokeNode(context.Background(), e.store.Home, name); err != nil {
		return ExitError, err
	}
	fmt.Fprintf(e.stdout, "revoked %s\n", name)
	return ExitOK, nil
}

func (e env) oneName(label string, args []string) (string, error) {
	fs := flag.NewFlagSet(label, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	pos, err := parse(fs, args)
	if err != nil {
		return "", err
	}
	if len(pos) != 1 {
		return "", fmt.Errorf("%s needs a name", label)
	}
	return pos[0], nil
}

// printable drops control characters from relay text a stranger chose.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}
