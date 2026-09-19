package setuptui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type storeConnectPage struct {
	fields       [3]storeField
	allowedPaths []string
	focus        int
	errMsg       string

	// Verification state.
	verifying    bool
	verifyResult *vaultVerifyMsg
}

type storeField struct {
	label string
	value string
}

func newStoreConnectPage() *storeConnectPage {
	return &storeConnectPage{
		fields: [3]storeField{
			{label: "address"},
			{label: "ca certificate"},
			{label: "allowed paths"},
		},
		allowedPaths: []string{"oberth/data/release/*", "oberth/upstream/*"},
	}
}

func (p *storeConnectPage) title() string    { return "propellant" }
func (p *storeConnectPage) question() string { return "Connect the store." }
func (p *storeConnectPage) keys() string {
	return sKey.Render("tab") + " fields · " + sKey.Render("enter") + " continue · " + sKey.Render("esc") + " back"
}

func (p *storeConnectPage) init(state *WizardState) tea.Cmd {
	if state.StoreAddress != "" {
		p.fields[0].value = state.StoreAddress
	}
	if state.StoreCACert != "" {
		p.fields[1].value = state.StoreCACert
	}
	if len(state.AllowedPaths) > 0 {
		p.allowedPaths = state.AllowedPaths
	}
	p.fields[2].value = strings.Join(p.allowedPaths, ", ")
	p.focus = 0
	p.errMsg = ""
	p.verifyResult = nil
	return nil
}

func (p *storeConnectPage) update(msg tea.Msg, state *WizardState) (page, tea.Cmd) {
	switch msg := msg.(type) {
	case vaultVerifyMsg:
		p.verifying = false
		p.verifyResult = &msg
		if msg.err != nil {
			p.errMsg = msg.err.Error()
		}
		return p, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "tab":
			p.focus = (p.focus + 1) % 4 // address(0), CA cert(1), paths(2), actions(3)
			p.errMsg = ""
		case "shift+tab":
			p.focus = (p.focus + 3) % 4
			p.errMsg = ""
		case "v":
			// Focus 0-2 are text fields: 'v' is text input.
			if p.focus == 0 || p.focus == 1 {
				p.fields[p.focus].value += "v"
				return p, nil
			}
			if p.focus == 2 {
				if len(p.allowedPaths) > 0 {
					last := len(p.allowedPaths) - 1
					p.allowedPaths[last] += "v"
					p.fields[2].value = strings.Join(p.allowedPaths, ", ")
				}
				return p, nil
			}
			// Focus 3 (actions row): trigger verify.
			if err := p.validateFields(); err != "" {
				p.errMsg = err
				return p, nil
			}
			p.verifying = true
			p.errMsg = ""
			return p, probeVault(p.fields[0].value, p.fields[1].value)
		case "n":
			// Focus 0-2 are text fields: 'n' is text input.
			if p.focus == 0 || p.focus == 1 {
				p.fields[p.focus].value += "n"
				return p, nil
			}
			if p.focus == 2 {
				if len(p.allowedPaths) > 0 {
					last := len(p.allowedPaths) - 1
					p.allowedPaths[last] += "n"
					p.fields[2].value = strings.Join(p.allowedPaths, ", ")
				}
				return p, nil
			}
			// Focus 3 (actions row): add a path.
			p.allowedPaths = append(p.allowedPaths, "")
			p.fields[2].value = strings.Join(p.allowedPaths, ", ")
			return p, nil
		case "enter":
			if err := p.validateFields(); err != "" {
				p.errMsg = err
				return p, nil
			}
			state.StoreAddress = p.fields[0].value
			state.StoreCACert = p.fields[1].value
			state.AllowedPaths = p.allowedPaths
			state.Config.ArgoVaultAddress = p.fields[0].value
			return p, func() tea.Msg { return pageCompleteMsg{} }
		case "esc":
			return p, func() tea.Msg { return pageBackMsg{} }
		case "backspace":
			if p.focus < 2 {
				v := p.fields[p.focus].value
				if len(v) > 0 {
					p.fields[p.focus].value = v[:len(v)-1]
				}
			} else if p.focus == 2 && len(p.allowedPaths) > 0 {
				last := len(p.allowedPaths) - 1
				if len(p.allowedPaths[last]) > 0 {
					p.allowedPaths[last] = p.allowedPaths[last][:len(p.allowedPaths[last])-1]
				}
				p.fields[2].value = strings.Join(p.allowedPaths, ", ")
			}
		default:
			text := msg.String()
			if p.focus < 2 && len(text) == 1 {
				p.fields[p.focus].value += text
			} else if p.focus == 2 && len(text) == 1 && len(p.allowedPaths) > 0 {
				last := len(p.allowedPaths) - 1
				p.allowedPaths[last] += text
				p.fields[2].value = strings.Join(p.allowedPaths, ", ")
			}
		}
	}
	return p, nil
}

func (p *storeConnectPage) validateFields() string {
	// S7: https only at the field level — shared with the plain path (S12).
	if err := validateStoreAddress(p.fields[0].value); err != nil {
		return err.Error()
	}
	return ""
}

func (p *storeConnectPage) view(_ *WizardState, _, _ int) string {
	var b strings.Builder

	b.WriteString("  " + sQuestion.Render(p.question()) + "\n\n")

	// Address and CA certificate fields.
	for i := 0; i < 2; i++ {
		f := p.fields[i]
		cursor := "  "
		labelStyle := sMuted
		if i == p.focus {
			cursor = lipgloss.NewStyle().Foreground(cPurple).Render("❯ ")
			labelStyle = lipgloss.NewStyle().Foreground(cPurple)
		}

		input := lipgloss.NewStyle().
			Background(cLine).
			Foreground(cFg).
			Padding(0, 1).
			Render(f.value)

		_, _ = fmt.Fprintf(&b, "  %s%-16s %s\n", cursor, labelStyle.Render(f.label), input)
		b.WriteString("\n")
	}

	// Allowed paths (editable when focused).
	pathCursor := "  "
	pathLabelStyle := sMuted
	if p.focus == 2 {
		pathCursor = lipgloss.NewStyle().Foreground(cPurple).Render("❯ ")
		pathLabelStyle = lipgloss.NewStyle().Foreground(cPurple)
	}
	_, _ = fmt.Fprintf(&b, "  %s%-16s ", pathCursor, pathLabelStyle.Render("allowed paths"))
	for i, path := range p.allowedPaths {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(lipgloss.NewStyle().Background(cLine).Foreground(cFg).Padding(0, 1).Render(path))
	}
	b.WriteString("\n")

	// Auth note.
	b.WriteString("  " + sMuted.Render("auth is kubernetes (role oberth-secretstore) — oberth ") +
		sText.Render("never") + sMuted.Render(" asks for a store") + "\n")
	b.WriteString("  " + sMuted.Render("admin token; store-side policy is setup-secretstore.sh in your own session") + "\n\n")

	// Actions row (focus index 3): v verify, n add path.
	actionStyle := sMuted
	if p.focus == 3 {
		actionStyle = lipgloss.NewStyle().Foreground(cPurple)
	}
	b.WriteString("  " + actionStyle.Render("v") + " " + actionStyle.Render("verify") +
		"  " + actionStyle.Render("n") + " " + actionStyle.Render("add path") + "\n")

	if p.verifying {
		b.WriteString("    " + lipgloss.NewStyle().Foreground(cPurple).Render("⠸") + " verifying...\n")
	} else if p.verifyResult != nil {
		vr := p.verifyResult
		if vr.reachable {
			b.WriteString("    " + sGo.Render("✓") + " reachable      " +
				sMuted.Render(fmt.Sprintf("tls %s · %d ms", vr.tlsVersion, vr.latency.Milliseconds())) + "\n")
		} else if vr.err != nil {
			b.WriteString("    " + sFail.Render("✗") + " " + sFail.Render(vr.err.Error()) + "\n")
		}
		if vr.serverLegOK {
			b.WriteString("    " + sGo.Render("✓") + " server leg     " +
				sMuted.Render(fmt.Sprintf("%d/%d allowed paths readable", vr.pathsOK, vr.pathsTotal)) + "\n")
		}
		switch vr.releaseLeg {
		case "ok":
			b.WriteString("    " + sGo.Render("✓") + " release leg\n")
		case "pending":
			b.WriteString("    " + lipgloss.NewStyle().Foreground(cPurple).Render("⠸") + " release leg    " +
				sMuted.Render("probe pod · release sa") + "\n")
		case "failed":
			b.WriteString("    " + sFail.Render("✗") + " release leg\n")
		}
	}

	if p.errMsg != "" {
		b.WriteString("\n  " + sFail.Render(p.errMsg) + "\n")
	}

	return b.String()
}
