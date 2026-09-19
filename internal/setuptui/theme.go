// Package setuptui implements the interactive TUI wizard for `oberth setup`.
// It builds an [installer.Config] from a multi-page Bubble Tea flow and hands
// it to [installer.Execute] — the same code path the flag-driven install uses.
package setuptui

import (
	"charm.land/bubbles/v2/progress"
	"charm.land/lipgloss/v2"
)

// Dracula canonical palette — draculatheme.com/contribute.
// Terminal cells are not subpixel-rendered text; canonical Dracula is designed
// for exactly this medium. Semantic assignments match the oberth.ci pipeline
// diagram (orange=preflight, purple=policy/brand, cyan=sync/info, green=live).
var (
	cBg      = lipgloss.Color("#282a36") // Background
	cLine    = lipgloss.Color("#44475a") // Current Line
	cFg      = lipgloss.Color("#f8f8f2") // Foreground
	cComment = lipgloss.Color("#6272a4") // Comment
	cCyan    = lipgloss.Color("#8be9fd") // Cyan
	cGreen   = lipgloss.Color("#50fa7b") // Green
	cOrange  = lipgloss.Color("#ffb86c") // Orange
	cPink    = lipgloss.Color("#ff79c6") // Pink
	cPurple  = lipgloss.Color("#bd93f9") // Purple
	cRed     = lipgloss.Color("#ff5555") // Red
	cYellow  = lipgloss.Color("#f1fa8c") // Yellow
)

// Semantic style tokens shared across all pages.
var (
	sQuestion  = lipgloss.NewStyle().Foreground(cPurple).Bold(true)
	sTopBar    = lipgloss.NewStyle().Foreground(cComment)
	sKey       = lipgloss.NewStyle().Foreground(cPink)
	sGo        = lipgloss.NewStyle().Foreground(cGreen).Bold(true)
	sHold      = lipgloss.NewStyle().Foreground(cOrange)
	sFail      = lipgloss.NewStyle().Foreground(cRed).Bold(true)
	sInfo      = lipgloss.NewStyle().Foreground(cCyan)
	sHighlight = lipgloss.NewStyle().Foreground(cYellow)
	sMuted     = lipgloss.NewStyle().Foreground(cComment)
	sText      = lipgloss.NewStyle().Foreground(cFg)
	sButton    = lipgloss.NewStyle().Background(cPurple).Foreground(cBg).Padding(0, 2)

	// sButtonDim is the review apply button when any HOLD exists.
	sButtonDim = lipgloss.NewStyle().Background(cLine).Foreground(cComment).Padding(0, 2)

	sListBox = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(cComment)
	sGutter = lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(cRed).
		PaddingLeft(1)

	// The token ceremony's distinctive double-line border.
	sCeremonyBox = lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(cRed).
			Padding(1, 2)

	// Help modal border.
	sHelpModal = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(cPurple).
			Padding(1, 2)
)

// brandMark is the triangle logomark used in the top bar.
const brandMark = "▲" // ▲

// newBand creates the apt-style progress band: solid Purple fill with full
// block characters, no gradient, no percentage text, no easing (design 4.4).
func newBand() progress.Model {
	return progress.New(
		progress.WithColors(cPurple),
		progress.WithFillCharacters(progress.DefaultFullCharFullBlock, progress.DefaultEmptyCharBlock),
		progress.WithoutPercentage(),
	)
}
