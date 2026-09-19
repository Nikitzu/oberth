package setuptui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type executionPage struct {
	fields [5]execField
	focus  int
	errMsg string
}

type execField struct {
	label       string
	value       string
	fieldType   string // "number", "select", "port"
	options     []string
	optionIndex int
	description string
}

func newExecutionPage() *executionPage {
	return &executionPage{
		fields: [5]execField{
			{label: "concurrent jobs", value: "3", fieldType: "number"},
			{label: "network policy", value: "strict", fieldType: "select",
				options: []string{"strict", "auto", "off"}, optionIndex: 0},
			{label: "external anchoring", value: "off", fieldType: "select",
				options: []string{"off", "on"}, optionIndex: 0,
				description: "off — a default install contacts no external service"},
			{label: "git ssh nodeport", value: "30022", fieldType: "port"},
			{label: "https/mcp nodeport", value: "30443", fieldType: "port"},
		},
	}
}

func (p *executionPage) title() string    { return "flight plan" }
func (p *executionPage) question() string { return "Execution and network." }

func (p *executionPage) init(state *WizardState) tea.Cmd {
	if state.Config.NetworkPolicy != "" {
		for i, opt := range p.fields[1].options {
			if opt == state.Config.NetworkPolicy {
				p.fields[1].optionIndex = i
				p.fields[1].value = opt
			}
		}
	}
	if state.Config.InstallRekor {
		p.fields[2].optionIndex = 1
		p.fields[2].value = "on"
	}
	p.focus = 0
	p.errMsg = ""
	return nil
}

func (p *executionPage) update(msg tea.Msg, state *WizardState) (page, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "tab":
			p.focus = (p.focus + 1) % len(p.fields)
			p.errMsg = ""
		case "shift+tab":
			p.focus = (p.focus + len(p.fields) - 1) % len(p.fields)
			p.errMsg = ""
		case "left":
			f := &p.fields[p.focus]
			if f.fieldType == "select" && f.optionIndex > 0 {
				f.optionIndex--
				f.value = f.options[f.optionIndex]
			}
		case "right":
			f := &p.fields[p.focus]
			if f.fieldType == "select" && f.optionIndex < len(f.options)-1 {
				f.optionIndex++
				f.value = f.options[f.optionIndex]
			}
		case "enter":
			if err := p.validate(); err != "" {
				p.errMsg = err
				return p, nil
			}
			state.Config.NetworkPolicy = p.fields[1].value
			state.Config.InstallRekor = p.fields[2].value == "on"
			return p, func() tea.Msg { return pageCompleteMsg{} }
		case "escape":
			return p, func() tea.Msg { return pageBackMsg{} }
		case "backspace":
			f := &p.fields[p.focus]
			if (f.fieldType == "number" || f.fieldType == "port") && len(f.value) > 0 {
				f.value = f.value[:len(f.value)-1]
			}
		default:
			text := msg.String()
			f := &p.fields[p.focus]
			if (f.fieldType == "number" || f.fieldType == "port") && len(text) == 1 && text[0] >= '0' && text[0] <= '9' {
				f.value += text
			}
		}
	}
	return p, nil
}

func (p *executionPage) validate() string {
	// Validate concurrent jobs.
	jobs, err := strconv.Atoi(p.fields[0].value)
	if err != nil || jobs < 1 || jobs > 32 {
		return "concurrent jobs must be 1-32"
	}

	// Validate NodePorts.
	for _, idx := range []int{3, 4} {
		port, err := strconv.Atoi(p.fields[idx].value)
		if err != nil || port < 30000 || port > 32767 {
			return fmt.Sprintf("%s must be 30000-32767", p.fields[idx].label)
		}
	}

	// Ports must be distinct.
	if p.fields[3].value == p.fields[4].value {
		return "git ssh and https/mcp nodeports must differ"
	}

	return ""
}

func (p *executionPage) view(_ *WizardState, _, _ int) string {
	var b strings.Builder

	b.WriteString("  " + sQuestion.Render(p.question()) + "\n\n")

	for i, f := range p.fields {
		cursor := "  "
		labelStyle := sMuted
		if i == p.focus {
			cursor = lipgloss.NewStyle().Foreground(cPurple).Render("❯ ")
			labelStyle = lipgloss.NewStyle().Foreground(cPurple)
		}

		var input string
		switch f.fieldType {
		case "select":
			input = lipgloss.NewStyle().
				Background(cLine).
				Foreground(cFg).
				Padding(0, 1).
				Render(f.value + " ▾")
		default:
			input = lipgloss.NewStyle().
				Background(cLine).
				Foreground(cFg).
				Padding(0, 1).
				Render(f.value)
		}

		_, _ = fmt.Fprintf(&b, "  %s%-22s %s\n", cursor, labelStyle.Render(f.label), input)

		if i == p.focus && f.description != "" {
			b.WriteString("    " + sMuted.Render("· "+f.description) + "\n")
		}
		b.WriteString("\n")
	}

	if p.errMsg != "" {
		b.WriteString("  " + sFail.Render(p.errMsg) + "\n")
	}

	return b.String()
}
