package setuptui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ceremonyPage handles the bearer token shown-once ceremony (S1/S2/S8).
// The token lives in one []byte owned by this model. It is never
// fmt-formatted, never logged, never placed in WizardState, and is zeroed
// when this page is dismissed.
type ceremonyPage struct {
	token        []byte
	revealed     bool
	copied       bool
	acknowledged bool
}

func newCeremonyPage() *ceremonyPage {
	return &ceremonyPage{}
}

func (p *ceremonyPage) title() string    { return "crew manifest" }
func (p *ceremonyPage) question() string { return "" }

func (p *ceremonyPage) init(_ *WizardState) tea.Cmd {
	return nil
}

// setToken takes ownership of the token: it copies the bytes into the
// ceremony's own buffer and ZEROS THE SOURCE (S2 — the token is held once;
// the message buffer that delivered it must not linger on the heap).
func (p *ceremonyPage) setToken(token []byte) {
	p.zeroToken() // drop any previous token first
	p.token = make([]byte, len(token))
	copy(p.token, token)
	for i := range token {
		token[i] = 0
	}
	p.revealed = false
	p.copied = false
	p.acknowledged = false
}

func (p *ceremonyPage) update(msg tea.Msg, _ *WizardState) (page, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "r":
			p.revealed = true
			return p, nil
		case "c":
			if len(p.token) > 0 {
				p.copied = true
				p.revealed = true
				// OSC 52 clipboard copy (S8: opt-in only, explicitly warned).
				return p, tea.SetClipboard(string(p.token))
			}
		case "enter":
			if p.revealed || p.copied {
				p.acknowledged = true
				// Zero the token (S2).
				p.zeroToken()
				return p, func() tea.Msg { return pageCompleteMsg{} }
			}
			// Cannot acknowledge without reveal or copy.
			return p, nil
		}
		// No esc — explicit ack required (design doc 5.14).
	}
	return p, nil
}

func (p *ceremonyPage) view(_ *WizardState, width, _ int) string {
	var b strings.Builder

	b.WriteString("  " + sText.Render("Store this token now — it will not exist again.") + "\n\n")

	// Token ceremony box (double border, Red — the one heavy border).
	var boxContent strings.Builder
	boxContent.WriteString("\n")
	boxContent.WriteString(sMuted.Render("stored server-side as a digest only. this screen is the only copy that will exist.") + "\n\n")

	// Token display.
	tokenDisplay := sHighlight.Render(strings.Repeat("█", 40))
	if p.revealed && len(p.token) > 0 {
		tokenDisplay = lipgloss.NewStyle().
			Background(cLine).
			Foreground(cFg).
			Render(string(p.token))
	}
	boxContent.WriteString("  " + sMuted.Render("token") + "   " + tokenDisplay)
	boxContent.WriteString("            " + sKey.Render("r") + " reveal · " + sKey.Render("c") + " copy\n\n")

	boxContent.WriteString("  " + sMuted.Render("destination: ") +
		sInfo.Render(".claude/settings.local.json") +
		sMuted.Render(" — you paste it; oberth never writes it") + "\n")
	boxContent.WriteString("  " + sMuted.Render("clipboard (osc 52) is outside oberth's control — prefer reveal-and-type") + "\n\n")

	// Acknowledge button.
	if p.revealed || p.copied {
		boxContent.WriteString("  " + sKey.Render("enter") +
			sMuted.Render(" — i have stored the token") + "\n")
	} else {
		boxContent.WriteString("  " + sMuted.Render("enter — i have stored the token") +
			sMuted.Render("             (enabled after reveal or copy)") + "\n")
	}

	boxWidth := min(78, width-8)
	box := sCeremonyBox.Width(boxWidth).Render(boxContent.String())

	// Center the box.
	pad := (width - lipgloss.Width(box)) / 2
	if pad < 0 {
		pad = 0
	}
	for _, line := range strings.Split(box, "\n") {
		b.WriteString(strings.Repeat(" ", pad) + line + "\n")
	}

	b.WriteString("\n  " + sGo.Render("leaving this screen discards the token — alt buffer: no scrollback, no logs") + "\n")

	return b.String()
}

// zeroToken securely zeros the token bytes (S2).
func (p *ceremonyPage) zeroToken() {
	for i := range p.token {
		p.token[i] = 0
	}
	p.token = nil
}
