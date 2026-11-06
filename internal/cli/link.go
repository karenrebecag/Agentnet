//go:build unix

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/karenrebecag/agentnet/internal/relay"
)

func (e env) link(args []string) (int, error) {
	fs := flag.NewFlagSet("link", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	if _, err := parse(fs, args); err != nil {
		return ExitError, err
	}
	f, err := relay.Load(e.store.Home)
	if err != nil {
		return ExitError, err
	}
	if f.URL == "" || f.Node == "" || f.Token == "" {
		return ExitError, errors.New("relay.json needs url, node and token")
	}
	if _, err := fmt.Fprintf(e.stdout, "link %s %s\n", f.Node, f.URL); err != nil {
		return ExitError, err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := relay.Run(ctx, relay.Options{Store: e.store, File: f}); err != nil {
		return ExitError, err
	}
	return ExitOK, nil
}
