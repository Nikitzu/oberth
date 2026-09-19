package setuptui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type modePage struct {
	cursor int // 0 = dev, 1 = production
}

func newModePage() *modePage {
	return &modePage{}
}

func (p *modePage) title() string    { return "flight plan" }
func (p *modePage) question() string { return "Which mode?" }

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
		case "down", "j":
			if p.cursor < 1 {
				p.cursor++
			}
		case "enter":
			state.Config.Dev = p.cursor == 0
			state.Config.Production = p.cursor == 1
			return p, func() tea.Msg { return pageCompleteMsg{} }
		case "escape":
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
			description: "hardened profile — not implemented yet",
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

	return b.String()
}
