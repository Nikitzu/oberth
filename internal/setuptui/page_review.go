package setuptui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type reviewPage struct {
	showDryRun bool
	dryRunText string
}

func newReviewPage() *reviewPage {
	return &reviewPage{}
}

func (p *reviewPage) title() string    { return "go/no-go" }
func (p *reviewPage) question() string { return "Review the plan." }
func (p *reviewPage) keys() string {
	return sKey.Render("1..8") + " revisit · " + sKey.Render("d") + " dry-run · " + sKey.Render("enter") + " apply · " + sKey.Render("esc") + " back"
}

func (p *reviewPage) init(_ *WizardState) tea.Cmd {
	p.showDryRun = false
	return nil
}

func (p *reviewPage) update(msg tea.Msg, state *WizardState) (page, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "enter":
			if !p.allGo(state) {
				return p, nil
			}
			return p, func() tea.Msg { return pageCompleteMsg{} }
		case "esc":
			return p, func() tea.Msg { return pageBackMsg{} }
		case "d":
			p.showDryRun = !p.showDryRun
			if p.showDryRun {
				p.dryRunText = BuildCommandLine(state)
			}
			return p, nil
		// Jump keys 1-8 → corresponding pages (1-indexed).
		case "1":
			return p, func() tea.Msg { return pageJumpMsg{page: 2} }
		case "2":
			return p, func() tea.Msg { return pageJumpMsg{page: 3} }
		case "3":
			return p, func() tea.Msg { return pageJumpMsg{page: 4} }
		case "4":
			return p, func() tea.Msg { return pageJumpMsg{page: 5} }
		case "5":
			return p, func() tea.Msg { return pageJumpMsg{page: 6} }
		case "6":
			return p, func() tea.Msg { return pageJumpMsg{page: 8} }
		case "7":
			return p, func() tea.Msg { return pageJumpMsg{page: 9} }
		case "8":
			return p, func() tea.Msg { return pageJumpMsg{page: 11} }
		}
	}
	return p, nil
}

func (p *reviewPage) allGo(state *WizardState) bool {
	rows := p.buildRows(state)
	for _, row := range rows {
		if row.status == "HOLD" {
			return false
		}
	}
	return true
}

type reviewRow struct {
	num     int
	label   string
	summary string
	status  string // GO, HOLD
	note    string
}

func (p *reviewPage) buildRows(state *WizardState) []reviewRow {
	rows := []reviewRow{
		{1, "cluster", formatClusterSummary(state), goStatus(state.pageValid[1]), ""},
		{2, "mode", formatModeSummary(state), goStatus(state.pageValid[2]), ""},
		{3, "namespaces", formatNamespacesSummary(state), goStatus(state.pageValid[3]), ""},
		{4, "network", formatNetworkSummary(state), goStatus(state.pageValid[4]), ""},
		{5, "store", formatStoreSummary(state), goStatus(state.pageValid[5] || state.pageValid[6]), ""},
		{6, "tls", formatTLSSummary(state), goStatus(state.pageValid[7]), ""},
		{7, "uplink", formatUplinkSummary(state), goStatus(state.pageValid[8]), ""},
		{8, "forge", formatForgeSummary(state), goStatus(state.pageValid[10]), ""},
	}
	return rows
}

func goStatus(valid bool) string {
	if valid {
		return "GO"
	}
	return "HOLD"
}

func (p *reviewPage) view(state *WizardState, _, _ int) string {
	var b strings.Builder

	b.WriteString("  " + sQuestion.Render(p.question()) + "\n\n")

	if p.showDryRun {
		b.WriteString("  " + sInfo.Render(p.dryRunText) + "\n\n")
		b.WriteString("  " + sKey.Render("d") + sMuted.Render(" toggle dry-run") + "\n")
		return b.String()
	}

	rows := p.buildRows(state)

	for _, row := range rows {
		numStr := sKey.Render(fmt.Sprintf("  %d", row.num))
		labelStr := lipgloss.NewStyle().Foreground(cPurple).Render(fmt.Sprintf("  %-12s", row.label))
		summaryStr := sText.Render(row.summary)

		statusStyle := sGo
		if row.status == "HOLD" {
			statusStyle = sHold
		}
		statusStr := statusStyle.Render(fmt.Sprintf("%6s", row.status))

		b.WriteString(numStr + labelStr + summaryStr)
		// Right-align the status.
		b.WriteString("  " + statusStr + "\n")

		if row.note != "" {
			b.WriteString("     " + sHold.Render("! "+row.note) + "\n")
		}
	}

	b.WriteString("\n")

	// Secrets guarantee line.
	b.WriteString("  " + sMuted.Render("no secret leaves memory: ") +
		sGo.Render("none") + sMuted.Render(" on disk · ") +
		sGo.Render("none") + sMuted.Render(" in etcd · token shown once") + "\n\n")

	// Apply button.
	allGo := p.allGo(state)
	if allGo {
		centered := lipgloss.NewStyle().Width(80).AlignHorizontal(lipgloss.Center).
			Render(sButton.Render("▶ apply the plan — enter"))
		b.WriteString(centered + "\n")
	} else {
		centered := lipgloss.NewStyle().Width(80).AlignHorizontal(lipgloss.Center).
			Render(sButtonDim.Render("▶ apply the plan — enter"))
		b.WriteString(centered + "\n")
	}

	return b.String()
}

// Helper formatters for the review summary column.

func formatClusterSummary(state *WizardState) string {
	if state.ClusterInfo.context == "" {
		return "not selected"
	}
	parts := []string{state.ClusterInfo.context}
	if state.ClusterInfo.version != "" {
		parts = append(parts, state.ClusterInfo.version)
	}
	if state.ClusterInfo.isLocal {
		parts = append(parts, "local")
	} else {
		parts = append(parts, "remote")
	}
	return strings.Join(parts, " · ")
}

func formatModeSummary(state *WizardState) string {
	if state.Config.Dev {
		return "dev / evaluation"
	}
	return "production"
}

func formatNamespacesSummary(state *WizardState) string {
	ns := state.Config.Namespace
	if ns == "" {
		ns = "oberth"
	}
	argo := state.Config.ArgoNamespace
	if argo == "" {
		argo = "oberth-pipelines"
	}
	openbao := state.Config.OpenBaoNamespace
	if openbao == "" {
		openbao = "openbao"
	}
	return ns + " / " + argo + " / " + openbao
}

func formatNetworkSummary(state *WizardState) string {
	// Config holds the installer vocabulary (auto|true|false); show the
	// human-facing label the wizard collected instead of the raw value.
	np := state.Config.NetworkPolicy
	switch np {
	case "", "auto":
		np = "auto"
	case "true":
		np = "strict"
	case "false":
		np = "off"
	}
	anchoring := "off"
	if state.Config.InstallRekor {
		anchoring = "on"
	}
	return "30022 / 30443 · " + np + " · anchoring " + anchoring
}

func formatStoreSummary(state *WizardState) string {
	switch state.StoreMode {
	case "install-dev":
		return "install openbao — dev"
	case "install-prod":
		return "install openbao — production"
	case "connect":
		if state.StoreAddress != "" {
			return "connect existing · " + state.StoreAddress
		}
		return "connect existing"
	}
	return "undecided"
}

func formatTLSSummary(state *WizardState) string {
	mode := state.TLSMode
	if mode == "" {
		mode = "self-signed"
	}
	proxy := "off"
	if state.ProxyEnabled {
		proxy = "on"
	}
	sans := len(state.Config.TLSExtraDNSNames) + len(state.Config.TLSExtraIPs)
	return mode + " · " + fmt.Sprintf("%d sans", sans) + " · proxy " + proxy
}

func formatUplinkSummary(state *WizardState) string {
	if state.UplinkIdentity == "" {
		return "not configured"
	}
	return state.UplinkIdentity
}

func formatForgeSummary(state *WizardState) string {
	if state.ForgeOrg == "" {
		return "not configured"
	}
	summary := state.ForgeType + " · " + state.ForgeOrg + " · " + state.ForgeAuth
	// I6: show forge token destination class, never the value (S5).
	if state.ForgeAuth == "token" {
		summary += " · forge token → openbao oberth/upstream/" + state.ForgeOrg
	}
	return summary
}
