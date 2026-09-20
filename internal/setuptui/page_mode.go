package setuptui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type modePage struct {
	cursor int // 0 = dev, 1 = production
	errMsg string
}

func newModePage() *modePage {
	return &modePage{}
}

func (p *modePage) title() string    { return "flight plan" }
func (p *modePage) question() string { return "Which mode?" }
func (p *modePage) keys() string {
	return sKey.Render("↑/↓") + " choose · " + sKey.Render("enter") + " continue · " + sKey.Render("esc") + " back"
}

func (p *modePage) init(state *WizardState) tea.Cmd {
	if state.Config.Production {
		p.cursor = 1
	} else {
		p.cursor = 0
	}
	return nil
}

func (p *modePage) update(msg tea.Msg, state *WizardState) (page, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "up", "k":
			if p.cursor > 0 {
				p.cursor--
			}
			p.errMsg = ""
		case "down", "j":
			if p.cursor < 1 {
				p.cursor++
			}
			p.errMsg = ""
		case "enter":
			// The production profile is a disabled option, not a selectable
			// one — `oberth install --production` is not implemented and the
			// wizard must not build a config the installer cannot honor.
			if p.cursor == 1 {
				p.errMsg = "production is coming soon — choose dev / evaluation for now"
				return p, nil
			}
			state.Config.Dev = true
			state.Config.Production = false
			return p, func() tea.Msg { return pageCompleteMsg{} }
		case "esc":
			return p, func() tea.Msg { return pageBackMsg{} }
		}
	}
	return p, nil
}

func (p *modePage) view(state *WizardState, _, _ int) string {
	var b strings.Builder

	b.WriteString("  " + sQuestion.Render(p.question()) + "\n\n")

	options := []struct {
		label       string
		description string
		disabled    bool
	}{
		{
			label:       "dev / evaluation",
			description: "single node, fast defaults — recommended for " + state.ClusterInfo.context,
		},
		{
			label:       "production",
			description: "multi-replica, persistent storage, strict isolation — coming soon",
			disabled:    true,
		},
	}

	for i, opt := range options {
		cursor := "      "
		radio := "( )"
		if i == p.cursor {
			cursor = "    " + lipgloss.NewStyle().Foreground(cPurple).Render("❯") + " "
			radio = "(•)"
		}

		radioStyled := lipgloss.NewStyle().Foreground(cPurple).Render(radio)
		label := sText.Render(opt.label)
		descStyle := sMuted
		if opt.disabled {
			descStyle = sHold
		}
		desc := descStyle.Render(opt.description)

		b.WriteString(cursor + radioStyled + " " + label + "\n")
		b.WriteString("          " + desc + "\n\n")
	}

	if p.errMsg != "" {
		b.WriteString("  " + sFail.Render(p.errMsg) + "\n")
	}

	return b.String()
}
