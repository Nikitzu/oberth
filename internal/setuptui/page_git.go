package setuptui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

type gitPage struct{}

func newGitPage() *gitPage {
	return &gitPage{}
}

func (p *gitPage) title() string    { return "comms check" }
func (p *gitPage) question() string { return "How code arrives." }
func (p *gitPage) keys() string {
	return sKey.Render("enter") + " continue · " + sKey.Render("esc") + " back"
}

func (p *gitPage) init(_ *WizardState) tea.Cmd { return nil }

func (p *gitPage) update(msg tea.Msg, _ *WizardState) (page, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "enter":
			return p, func() tea.Msg { return pageCompleteMsg{} }
		case "esc":
			return p, func() tea.Msg { return pageBackMsg{} }
		}
	}
	return p, nil
}

func (p *gitPage) view(state *WizardState, _, _ int) string {
	var b strings.Builder

	b.WriteString("  " + sQuestion.Render(p.question()) + "\n\n")

	b.WriteString("  " + sMuted.Render("push over ssh to nodeport 30022 — ") +
		sText.Render("smart protocol only") + sMuted.Render(", attributed to your uplink") + "\n\n")

	b.WriteString("  " + sMuted.Render("clone") + "    " +
		sInfo.Render("ssh://git@localhost:30022/") + sMuted.Render("<repo>") + sInfo.Render(".git") + "\n")
	b.WriteString("           " +
		sInfo.Render("ssh://git@ssh.oberth.ci/") + sMuted.Render("<repo>") + sInfo.Render(".git") + "\n\n")

	identity := "admin@host"
	if state.UplinkIdentity != "" {
		identity = state.UplinkIdentity
	}

	b.WriteString("  " + sMuted.Render("every push answers on stderr:") + "\n")
	b.WriteString("    " + sMuted.Render("remote: oberth: uplink ") +
		sText.Render(identity) + sMuted.Render(" · run ") +
		sHighlight.Render("r_01J8LK2M") + sMuted.Render(" queued") + "\n")
	b.WriteString("    " + sMuted.Render("remote: oberth: watch: ") +
		sInfo.Render("https://192.168.20.7:30443/runs/r_01J8LK2M") + "\n\n")

	b.WriteString("  " + sMuted.Render("tags are creation-only · only ") +
		sGo.Render("green") + sMuted.Render(" publishes upstream") + "\n")

	return b.String()
}
