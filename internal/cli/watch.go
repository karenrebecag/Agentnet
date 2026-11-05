//go:build unix

package cli

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/karenrebecag/agentnet/internal/tui"
)

func (e env) watch() (int, error) {
	guestsPath, _, _ := e.paths()
	program := tea.NewProgram(tui.New(tui.NewFeed(e.store, guestsPath)), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := program.Run(); err != nil {
		return ExitError, err
	}
	return ExitOK, nil
}
