//go:build unix

package tui

import "github.com/charmbracelet/lipgloss"

// A service-manager palette: color carries state. Teal names things, green is
// up, red is down, amber needs a decision, magenta titles a section. Every
// state is also spelled out, so nothing depends on seeing the color.
var (
	textColor    = lipgloss.AdaptiveColor{Light: "#1E1E1E", Dark: "#E6E6E6"}
	nameColor    = lipgloss.AdaptiveColor{Light: "#0B6E8E", Dark: "#2AA8CC"}
	upColor      = lipgloss.AdaptiveColor{Light: "#1E7F4F", Dark: "#3FBF7F"}
	downColor    = lipgloss.AdaptiveColor{Light: "#B3243A", Dark: "#E0475B"}
	warnColor    = lipgloss.AdaptiveColor{Light: "#9A6700", Dark: "#E5A93B"}
	sectionColor = lipgloss.AdaptiveColor{Light: "#8E2F99", Dark: "#C061CB"}
	tabColor     = lipgloss.AdaptiveColor{Light: "#9A5A2C", Dark: "#C8875A"}
	mutedColor   = lipgloss.AdaptiveColor{Light: "#6E6E6E", Dark: "#8C8C8C"}
	borderColor  = lipgloss.AdaptiveColor{Light: "#9A9A9A", Dark: "#B0B0B0"}
	selectBg     = lipgloss.AdaptiveColor{Light: "#C9DBF5", Dark: "#1F4E99"}

	text        = lipgloss.NewStyle().Foreground(textColor)
	bold        = lipgloss.NewStyle().Foreground(textColor).Bold(true)
	name        = lipgloss.NewStyle().Foreground(nameColor).Bold(true)
	up          = lipgloss.NewStyle().Foreground(upColor)
	down        = lipgloss.NewStyle().Foreground(downColor)
	warn        = lipgloss.NewStyle().Foreground(warnColor)
	section     = lipgloss.NewStyle().Foreground(sectionColor).Bold(true)
	tab_        = lipgloss.NewStyle().Foreground(tabColor)
	dim         = lipgloss.NewStyle().Foreground(mutedColor)
	border      = lipgloss.NewStyle().Foreground(borderColor)
	keycap      = lipgloss.NewStyle().Reverse(true).Foreground(textColor)
	selectedRow = lipgloss.NewStyle().Background(selectBg).Foreground(lipgloss.AdaptiveColor{Light: "#000000", Dark: "#FFFFFF"}).Bold(true)
)
