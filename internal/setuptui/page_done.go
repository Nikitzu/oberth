package setuptui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type donePage struct {
	totalSteps     int
	greenCount     int
	fingerprint    string
	sshFingerprint string
	context        string
	identity       string
}

func (p *donePage) title() string    { return "orbit" }
func (p *donePage) question() string { return "" }

func (p *donePage) init(state *WizardState) tea.Cmd {
	p.context = state.SelectedContext
	p.identity = state.UplinkIdentity
	return nil
}

func (p *donePage) update(msg tea.Msg, _ *WizardState) (page, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "enter", "q":
			return p, tea.Quit
		}
	}
	return p, nil
}

func (p *donePage) view(state *WizardState, width, _ int) string {
	_ = width
	var b strings.Builder

	// Completion line.
	greenStr := fmt.Sprintf("%d/%d", p.greenCount, p.totalSteps)
	if p.greenCount == 0 {
		greenStr = "all"
	}
	b.WriteString("  " + sText.Render("Setup complete — ") +
		sGo.Render(greenStr+" steps green") + sText.Render(".") + "\n\n")

	// Running now.
	ns := state.Config.Namespace
	if ns == "" {
		ns = "oberth"
	}
	openbaoNs := state.Config.OpenBaoNamespace
	if openbaoNs == "" {
		openbaoNs = "openbao"
	}
	b.WriteString("  " + sMuted.Render("running now") + "    " +
		sText.Render("deployment oberth (ns "+ns+")") +
		sMuted.Render(" · openbao (ns "+openbaoNs+")") + "\n\n")

	// Fingerprints section.
	b.WriteString("  " + sMuted.Render("verify out of band on every workstation") + "\n")

	if p.fingerprint != "" {
		b.WriteString("    " + sMuted.Render("tls 30443") + "    " +
			sHighlight.Render(p.fingerprint) + "\n")
	} else {
		b.WriteString("    " + sMuted.Render("tls 30443") + "    " +
			sHighlight.Render("sha-256 (generated at apply time)") + "\n")
	}

	b.WriteString("      " + sInfo.Render("kubectl get secret -n "+ns+" oberth-tls -o jsonpath='{.data.tls\\.crt}' | base64 -d") + "\n")

	if p.sshFingerprint != "" {
		b.WriteString("    " + sMuted.Render("ssh 30022") + "    " +
			sHighlight.Render(p.sshFingerprint) + "\n")
	} else {
		b.WriteString("    " + sMuted.Render("ssh 30022") + "    " +
			sHighlight.Render("ed25519 (generated at apply time)") + "\n")
	}

	b.WriteString("      " + sInfo.Render("ssh-keyscan -p 30022 localhost") + "\n\n")

	// Next steps.
	b.WriteString("  " + sMuted.Render("next") + "\n")
	b.WriteString("    " + sInfo.Render("git clone ssh://git@localhost:30022/oberth.git") + "\n")
	b.WriteString("    " + sMuted.Render(".claude/settings.local.json → ") +
		sInfo.Render("https://localhost:30443/mcp") +
		sMuted.Render("   (token from the ceremony)") + "\n")
	b.WriteString("    " + sMuted.Render("push — ") +
		sGo.Render("green") + sMuted.Render(" publishes upstream · ") +
		sFail.Render("red") + sMuted.Render(" opens exactly one ci issue") + "\n\n")

	// Dashboard and docs.
	b.WriteString("  " + sMuted.Render("dashboard ") +
		sInfo.Render("https://localhost:30443/runs") +
		sMuted.Render(" · docs ") +
		sInfo.Render("https://oberth.ci/docs") + "\n")

	return b.String()
}
