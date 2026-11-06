//go:build unix

package cli

import (
	"net/url"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/karenrebecag/agentnet/internal/relay"
	"github.com/karenrebecag/agentnet/internal/tui"
)

// No mouse capture: watch has no mouse actions, and capturing it would stop
// the terminal from selecting text such as a code or a URL.
func (e env) watch() (int, error) {
	guestsPath, _, _ := e.paths()
	program := tea.NewProgram(tui.New(tui.NewFeed(e.store, guestsPath), tui.Local{Home: e.store.Home, GuestsPath: guestsPath}, e.watchInfo()), tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		return ExitError, err
	}
	return ExitOK, nil
}

// watchInfo never shows the relay token: only the host and the node name.
func (e env) watchInfo() tui.Info {
	info := tui.Info{Version: Version, Home: e.store.Home}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(info.Home, home) {
		info.Home = "~" + strings.TrimPrefix(info.Home, home)
	}
	if cfg, err := relay.Load(e.store.Home); err == nil && cfg.URL != "" {
		host := cfg.URL
		if u, err := url.Parse(cfg.URL); err == nil && u.Host != "" {
			host = u.Host
		}
		info.Relay, info.Node = host, cfg.Node
	}
	return info
}
