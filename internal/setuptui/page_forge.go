package setuptui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type forgePage struct {
	forgeOptions []string
	forgeCursor  int
	org          string
	authCursor   int // 0=deploy-key, 1=token
	focusField   int // 0=forge, 1=org, 2=auth
	errMsg       string

	// Discovery state.
	discovering     bool
	discoveryResult *forgeDiscoveryMsg
}

func newForgePage() *forgePage {
	return &forgePage{
		forgeOptions: []string{"codeberg", "github", "forgejo", "gitlab"},
	}
}

func (p *forgePage) title() string    { return "ground station" }
func (p *forgePage) question() string { return "Where does green code go?" }

func (p *forgePage) init(state *WizardState) tea.Cmd {
	for i, opt := range p.forgeOptions {
		if opt == state.ForgeType {
			p.forgeCursor = i
			break
		}
	}
	if state.ForgeOrg != "" {
		p.org = state.ForgeOrg
	}
	if state.ForgeAuth == "token" {
		p.authCursor = 1
	} else {
		p.authCursor = 0
	}
	p.focusField = 0
	p.errMsg = ""
	p.discoveryResult = nil
	return nil
}

func (p *forgePage) update(msg tea.Msg, state *WizardState) (page, tea.Cmd) {
	switch msg := msg.(type) {
	case forgeDiscoveryMsg:
		p.discovering = false
		p.discoveryResult = &msg
		if msg.err != nil {
			p.errMsg = msg.err.Error()
		}
		return p, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "tab":
			p.focusField = (p.focusField + 1) % 3
			p.errMsg = ""
		case "shift+tab":
			p.focusField = (p.focusField + 2) % 3
			p.errMsg = ""
		case "left":
			switch p.focusField {
			case 0:
				if p.forgeCursor > 0 {
					p.forgeCursor--
				}
			case 2:
				if p.authCursor > 0 {
					p.authCursor--
				}
			}
		case "right":
			switch p.focusField {
			case 0:
				if p.forgeCursor < len(p.forgeOptions)-1 {
					p.forgeCursor++
				}
			case 2:
				if p.authCursor < 1 {
					p.authCursor++
				}
			}
		case "d":
			if p.org == "" {
				p.errMsg = "org is required for discovery"
				return p, nil
			}
			p.discovering = true
			p.errMsg = ""
			return p, probeForge(p.forgeOptions[p.forgeCursor], p.org)
		case "enter":
			if p.org == "" {
				p.errMsg = "org is required"
				return p, nil
			}
			state.ForgeType = p.forgeOptions[p.forgeCursor]
			state.ForgeOrg = p.org
			if p.authCursor == 0 {
				state.ForgeAuth = "deploy-key"
			} else {
				state.ForgeAuth = "token"
			}
			return p, func() tea.Msg { return pageCompleteMsg{} }
		case "escape":
			return p, func() tea.Msg { return pageBackMsg{} }
		case "backspace":
			if p.focusField == 1 && len(p.org) > 0 {
				p.org = p.org[:len(p.org)-1]
			}
		default:
			text := msg.String()
			if p.focusField == 1 && len(text) == 1 {
				p.org += text
			}
		}
	}
	return p, nil
}

func (p *forgePage) view(_ *WizardState, _, _ int) string {
	var b strings.Builder

	b.WriteString("  " + sQuestion.Render(p.question()) + "\n\n")

	// Forge selection (inline radio).
	b.WriteString("  " + sMuted.Render("forge") + "        ")
	for i, opt := range p.forgeOptions {
		if i == p.forgeCursor {
			b.WriteString(lipgloss.NewStyle().Foreground(cPurple).Bold(true).Render("❯ " + opt))
		} else {
			b.WriteString("  " + sMuted.Render(opt))
		}
		if i < len(p.forgeOptions)-1 {
			b.WriteString("     ")
		}
	}
	b.WriteString("\n")

	// Org field.
	orgCursor := "  "
	orgLabelStyle := sMuted
	if p.focusField == 1 {
		orgCursor = lipgloss.NewStyle().Foreground(cPurple).Render("❯ ")
		orgLabelStyle = lipgloss.NewStyle().Foreground(cPurple)
	}
	orgInput := lipgloss.NewStyle().
		Background(cLine).
		Foreground(cFg).
		Padding(0, 1).
		Render(p.org)
	_, _ = fmt.Fprintf(&b, "  %s%-14s %s\n", orgCursor, orgLabelStyle.Render("owner/org"), orgInput)

	// Auth selection (inline radio).
	b.WriteString("  " + sMuted.Render("auth") + "         ")
	if p.authCursor == 0 {
		selected := lipgloss.NewStyle().Foreground(cPurple)
		b.WriteString(selected.Render("(•) deploy key per repo"))
	} else {
		b.WriteString(sMuted.Render("( ) deploy key per repo"))
	}
	b.WriteString("      ")
	if p.authCursor == 1 {
		selected := lipgloss.NewStyle().Foreground(cPurple)
		b.WriteString(selected.Render("(•) token → openbao only"))
	} else {
		b.WriteString(sMuted.Render("( ) token → openbao only"))
	}
	b.WriteString("\n\n")

	// Discovery section.
	b.WriteString("  " + sKey.Render("d") + sMuted.Render("iscovery") + "\n")
	if p.discovering {
		b.WriteString("    " + lipgloss.NewStyle().Foreground(cPurple).Render("⠸") + " discovering...\n")
	} else if p.discoveryResult != nil {
		if p.discoveryResult.err != nil {
			b.WriteString("    " + sFail.Render(p.discoveryResult.err.Error()) + "\n")
		} else {
			adopted := 0
			total := len(p.discoveryResult.repos)
			for _, r := range p.discoveryResult.repos {
				mark := sGo.Render("✓")
				if !r.adopted {
					mark = sMuted.Render("○")
				} else {
					adopted++
				}
				b.WriteString("    " + mark + " " + sText.Render(r.name))
				if r.errMsg != "" {
					b.WriteString(" " + sMuted.Render("("+r.errMsg+")"))
				}
			}
			_, _ = fmt.Fprintf(&b, "\n    %s — %d of %d adopted\n",
				sHighlight.Render(fmt.Sprintf("%d", adopted)),
				adopted, total)
		}
	}

	if p.errMsg != "" {
		b.WriteString("\n  " + sFail.Render(p.errMsg) + "\n")
	}

	return b.String()
}
