package tui

import (
	"github.com/charmbracelet/lipgloss"
)

var (
	subtle    = lipgloss.AdaptiveColor{Light: "#D9DCCF", Dark: "#383838"}
	highlight = lipgloss.AdaptiveColor{Light: "#874BFD", Dark: "#7B56DB"}
	special   = lipgloss.AdaptiveColor{Light: "#43BF6D", Dark: "#73F59F"}

	appStyle = lipgloss.NewStyle().
			Padding(0, 0).
			Align(lipgloss.Top)

	titleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFF")).
			Background(highlight).
			Padding(0, 0).
			Bold(true)

	codeStyle = lipgloss.NewStyle().
			Foreground(special).
			Bold(true).
			Border(lipgloss.RoundedBorder()).
			Padding(0, 0)

	infoStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#555", Dark: "#BBB"}).
			Italic(true)

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF4444")).
			Bold(true)

	buttonStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFF")).
			Background(highlight).
			Padding(0, 0).
			Bold(true)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#999", Dark: "#666"}).
			Italic(true)

	progressStyle = lipgloss.NewStyle().
			Width(0)

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(subtle).
			Padding(0, 1)

	headingStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFF")).
			Bold(true)

	badgeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#000")).
			Background(special).
			Padding(0, 1).
			Bold(true)

	railStyle = lipgloss.NewStyle().
			Foreground(highlight).
			Bold(true)

)
