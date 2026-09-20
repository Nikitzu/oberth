package setuptui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type tlsPage struct {
	tlsCursor   int // 0=self-signed, 1=byo
	proxyCursor int // 0=yes, 1=no
	sans        []string
	errMsg      string
}

func newTLSPage() *tlsPage {
	return &tlsPage{
		proxyCursor: 0,
	}
}

func (p *tlsPage) title() string    { return "heat shield" }
func (p *tlsPage) question() string { return "How should oberth serve TLS?" }
func (p *tlsPage) keys() string {
	return sKey.Render("↑/↓") + " choose · " + sKey.Render("←/→") + " proxy · " + sKey.Render("enter") + " continue · " + sKey.Render("esc") + " back"
}

func (p *tlsPage) init(state *WizardState) tea.Cmd {
	switch state.TLSMode {
	case "byo":
		p.tlsCursor = 1
	default:
		p.tlsCursor = 0
	}
	if !state.ProxyEnabled {
		p.proxyCursor = 1
	} else {
		p.proxyCursor = 0
	}

	// Pre-fill SANs from cluster info: service DNS, node name, node IP.
	// The context name ("default", "k3s-tuxbox") is not a useful SAN —
	// the node name and IP are what clients actually connect to.
	p.sans = []string{}
	ns := state.Config.Namespace
	if ns == "" {
		ns = "oberth"
	}
	p.sans = append(p.sans, "oberth."+ns+".svc")
	if state.ClusterInfo.nodeName != "" {
		p.sans = append(p.sans, state.ClusterInfo.nodeName)
	}
	// The node IP is a SAN too. init runs again on every revisit (esc back,
	// review jump), so only add it once — a duplicate would show up as an
	// extra --tls-extra-ip in the dry-mode command and in the review count.
	if ip := state.ClusterInfo.nodeIP; ip != "" && !slices.Contains(state.Config.TLSExtraIPs, ip) {
		state.Config.TLSExtraIPs = append(state.Config.TLSExtraIPs, ip)
	}
	if state.ProxyEnabled {
		p.sans = append(p.sans, "watch.oberth.ci")
	}

	p.errMsg = ""
	return nil
}

func (p *tlsPage) update(msg tea.Msg, state *WizardState) (page, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "up", "k":
			if p.tlsCursor > 0 {
				p.tlsCursor--
			}
		case "down", "j":
			if p.tlsCursor < 1 {
				p.tlsCursor++
			}
		case "left":
			if p.proxyCursor > 0 {
				p.proxyCursor--
			}
		case "right":
			if p.proxyCursor < 1 {
				p.proxyCursor++
			}
		case "enter":
			switch p.tlsCursor {
			case 0:
				state.TLSMode = "self-signed"
			case 1:
				state.TLSMode = "byo"
			}
			state.ProxyEnabled = p.proxyCursor == 0
			state.Config.TLSExtraDNSNames = p.sans
			return p, func() tea.Msg { return pageCompleteMsg{} }
		case "esc":
			return p, func() tea.Msg { return pageBackMsg{} }
		}
	}
	return p, nil
}

func (p *tlsPage) view(state *WizardState, _, _ int) string {
	var b strings.Builder

	b.WriteString("  " + sQuestion.Render(p.question()) + "\n\n")

	// Every name and address the certificate will be valid for.
	validFor := append(slices.Clone(p.sans), state.Config.TLSExtraIPs...)

	// TLS mode selection.
	options := []struct {
		label string
		desc  string
	}{
		{"generate a self-signed certificate (ed25519)",
			"valid for: " + strings.Join(validFor, " · ")},
		{"bring your own certificate + key",
			"you point at the files — their contents are never shown"},
	}

	for i, opt := range options {
		cursor := "      "
		radio := "( )"
		if i == p.tlsCursor {
			cursor = "    " + lipgloss.NewStyle().Foreground(cPurple).Render("❯") + " "
			radio = "(•)"
		}

		radioStyled := lipgloss.NewStyle().Foreground(cPurple).Render(radio)
		label := sText.Render(opt.label)
		descStyle := sMuted
		if i == 1 {
			descStyle = sHold // "contents never displayed" in Orange
		}
		desc := descStyle.Render(opt.desc)

		b.WriteString(cursor + radioStyled + " " + label + "\n")
		b.WriteString("          " + desc + "\n\n")
	}

	// Proxy toggle.
	proxyYes := "( )"
	proxyNo := "( )"
	if p.proxyCursor == 0 {
		proxyYes = lipgloss.NewStyle().Foreground(cPurple).Render("(•)")
	} else {
		proxyNo = lipgloss.NewStyle().Foreground(cPurple).Render("(•)")
	}
	b.WriteString("  " + sMuted.Render("Also serve through the watch.oberth.ci proxy?") + "\n")
	b.WriteString("    " + proxyYes + " " + sText.Render("yes") + sMuted.Render(" — publicly trusted certificate") + "      " +
		proxyNo + " " + sText.Render("no") + "\n\n")

	// Fingerprint note.
	b.WriteString("  " + sMuted.Render("the fingerprint appears on the final screen — verify it before you trust it") + "\n")

	if p.errMsg != "" {
		b.WriteString("\n  " + sFail.Render(p.errMsg) + "\n")
	}

	return b.String()
}
