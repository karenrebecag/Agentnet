//go:build unix

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/karenrebecag/agentnet/internal/expose"
	"github.com/karenrebecag/agentnet/internal/guests"
)

const maxTTL = 12 * time.Hour

func (e env) paths() (guestsPath, auditPath, stopPath string) {
	return filepath.Join(e.store.Home, "guests.json"),
		filepath.Join(e.store.Home, "audit.jsonl"),
		filepath.Join(e.store.Home, "expose.stop")
}

func (e env) guests(args []string) (int, error) {
	if len(args) == 0 {
		return ExitError, errors.New("usage: agentnet guests add|list|approve|revoke")
	}
	path, _, _ := e.paths()
	if err := os.MkdirAll(e.store.Home, 0o700); err != nil {
		return ExitError, err
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		fs := flag.NewFlagSet("guests add", flag.ContinueOnError)
		canAsk := fs.String("can-ask", "", "comma-separated local agents this guest may ask")
		positionals, err := parse(fs, rest)
		if err != nil || len(positionals) != 1 {
			return ExitError, errors.Join(err, errors.New("usage: agentnet guests add <name> --can-ask a,b"))
		}
		targets := splitList(*canAsk)
		if len(targets) == 0 {
			return ExitError, errors.New("--can-ask is required: a guest with no targets can do nothing")
		}
		token, err := guests.Add(path, positionals[0], targets)
		if err != nil {
			return ExitError, err
		}
		fmt.Fprintf(e.stdout, "guest %s created. Token, shown only once:\n\n  %s\n\nPaste it as the API key / Bearer token of the connector. The guest stays pending until you run:\n  agentnet guests approve %s\n", positionals[0], token, positionals[0])
		return ExitOK, nil
	case "list":
		all, err := guests.List(path)
		if err != nil {
			return ExitError, err
		}
		for _, g := range all {
			state := "pending-approval"
			switch {
			case g.Revoked:
				state = "revoked"
			case g.Approved:
				state = "approved"
			case g.Pending:
				state = "waiting (called, needs approve)"
			}
			fmt.Fprintf(e.stdout, "%s\t%s\tcan-ask=%s\n", g.Name, state, strings.Join(g.Targets, ","))
		}
		return ExitOK, nil
	case "approve":
		if len(rest) != 1 {
			return ExitError, errors.New("usage: agentnet guests approve <name>")
		}
		return ExitOK, guests.Approve(path, rest[0])
	case "revoke":
		if len(rest) != 1 {
			return ExitError, errors.New("usage: agentnet guests revoke <name>")
		}
		return ExitOK, guests.Revoke(path, rest[0])
	}
	return ExitError, fmt.Errorf("unknown guests command %q", sub)
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func (e env) expose(args []string) (int, error) {
	if len(args) > 0 && args[0] == "stop" {
		_, _, stopPath := e.paths()
		return ExitOK, os.WriteFile(stopPath, []byte("stop\n"), 0o600)
	}
	fs := flag.NewFlagSet("expose", flag.ContinueOnError)
	ttl := fs.Duration("ttl", 2*time.Hour, "session length, at most 12h")
	protocol := fs.String("protocol", "auto", "cloudflared edge protocol: auto, quic or http2 (http2 when UDP is blocked)")
	if _, err := parse(fs, args); err != nil {
		return ExitError, err
	}
	if *ttl <= 0 || *ttl > maxTTL {
		return ExitError, fmt.Errorf("--ttl must be between 1s and %s", maxTTL)
	}
	guestsPath, auditPath, stopPath := e.paths()
	_ = os.Remove(stopPath)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return ExitError, err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	pidPath := filepath.Join(e.store.Home, "cloudflared.pid")
	tunnel, host, err := expose.StartQuickTunnel(ctx, "http://"+ln.Addr().String(),
		filepath.Join(e.store.Home, "cloudflared.log"), pidPath, *protocol)
	if err != nil {
		ln.Close()
		return ExitError, err
	}
	defer func() { _ = tunnel.Kill(); _ = os.Remove(pidPath) }()

	stopReason := make(chan error, 1)
	srv := expose.NewServer(expose.ServerConfig{
		Store: e.store, GuestsPath: guestsPath, AuditPath: auditPath, StopPath: stopPath,
		Host: host, Expires: time.Now().Add(*ttl),
		Stop: func(reason error) {
			select {
			case stopReason <- reason:
			default:
			}
		},
		Warn: func(msg string) { fmt.Fprintf(e.stderr, "[%s] %s\n", time.Now().Format("15:04:05"), msg) },
	})
	go srv.Pump(ctx)
	// WriteTimeout leaves room for the 50 s long poll of ask and get_reply.
	httpServer := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      expose.MaxWait + 20*time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go httpServer.Serve(ln)

	fmt.Fprintf(e.stdout, "agentnet is reachable for %s at:\n\n  https://%s/\n\nAdd it as a remote MCP connector with the guest's token as API key.\nStop: Ctrl-C, or `agentnet expose stop`. Pending guests: `agentnet guests list`.\n", *ttl, host)

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	deadline := time.NewTimer(*ttl)
	defer deadline.Stop()
	var reason error
	for reason == nil {
		select {
		case <-ctx.Done():
			reason = errors.New("interrupted")
		case reason = <-stopReason:
		case err := <-tunnel.Done():
			reason = errors.Join(errors.New("tunnel exited"), err)
		case <-deadline.C:
			reason = expose.ErrTTL
		case <-ticker.C:
			if _, err := os.Stat(stopPath); err == nil {
				reason = errors.New("kill switch")
			}
		}
	}
	// Cut the public path first; draining in-flight requests can wait.
	_ = tunnel.Kill()
	shutdown, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	_ = httpServer.Shutdown(shutdown)
	fmt.Fprintf(e.stderr, "expose stopped: %v\n", reason)
	return ExitOK, nil
}
