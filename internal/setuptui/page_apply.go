package setuptui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// applyStep tracks one step in the apply process.
type applyStep struct {
	name     string
	status   string // "pending", "running", "done", "failed"
	duration time.Duration
}

type applyPage struct {
	steps       []applyStep
	currentStep int
	holdState   bool
	holdError   string
	holdDetail  string
	logTail     string
	showFullLog bool
	startTime   time.Time
	masker      *masker
	ceremony    *ceremonyPage
}

func newApplyPage() *applyPage {
	return &applyPage{
		masker:   newMasker(),
		ceremony: newCeremonyPage(),
		steps: []applyStep{
			{name: "render chart", status: "pending"},
			{name: "apply namespace · rbac · networkpolicy", status: "pending"},
			{name: "deploy oberth (digest-pinned)", status: "pending"},
			{name: "rollout ready", status: "pending"},
			{name: "install openbao", status: "pending"},
			{name: "secretstore verify — server leg", status: "pending"},
			{name: "secretstore probe — release leg", status: "pending"},
			{name: "tls + ssh host keys · fingerprints", status: "pending"},
			{name: "mint uplink — ceremony pauses here", status: "pending"},
			{name: "upstream discovery + deploy keys", status: "pending"},
			{name: "audit chain genesis · verify · readyz", status: "pending"},
		},
	}
}

func (p *applyPage) title() string {
	if p.holdState {
		return "ignition"
	}
	return "ignition"
}

func (p *applyPage) question() string {
	if p.holdState {
		return "Holding."
	}
	return "Applying."
}

func (p *applyPage) init(state *WizardState) tea.Cmd {
	p.startTime = time.Now()

	// In dry-mode, we don't actually apply.
	if state.Config.DryRun {
		return nil
	}

	// Start the apply simulation — in reality this would call
	// installer.Execute, but the real implementation needs the
	// installer to emit progress messages via tea.Cmd.
	return p.simulateApply(state)
}

func (p *applyPage) simulateApply(_ *WizardState) tea.Cmd {
	// Start by marking the first step as running.
	return func() tea.Msg {
		return applyStepMsg{
			step:   0,
			total:  len(p.steps),
			name:   p.steps[0].name,
			status: "running",
		}
	}
}

func (p *applyPage) update(msg tea.Msg, _ *WizardState) (page, tea.Cmd) {
	switch msg := msg.(type) {
	case applyStepMsg:
		if msg.step < len(p.steps) {
			p.steps[msg.step].status = msg.status
			p.steps[msg.step].duration = msg.duration
			p.currentStep = msg.step

			if msg.logLine != "" {
				p.logTail = msg.logLine
			}

			if msg.status == "failed" {
				p.holdState = true
				p.holdError = msg.name
				if msg.err != nil {
					p.holdDetail = msg.err.Error()
				}
				return p, nil
			}
		}
		return p, nil

	case applyDoneMsg:
		if msg.err != nil {
			p.holdState = true
			p.holdError = msg.err.Error()
			return p, nil
		}
		// All steps done — transition to done page.
		return p, func() tea.Msg { return pageCompleteMsg{} }

	case ceremonyTokenMsg:
		// The token ceremony interrupts the apply after the mint step.
		// Register with the masker for log-tail safety (S3), then hand
		// the token to the ceremony page for display.
		p.masker.register(string(msg.token))
		p.ceremony.setToken(msg.token)
		return p, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "l":
			p.showFullLog = !p.showFullLog
			return p, nil
		case "r":
			if p.holdState {
				// Retry the failed step.
				p.holdState = false
				p.holdError = ""
				p.holdDetail = ""
				if p.currentStep < len(p.steps) {
					p.steps[p.currentStep].status = "running"
				}
				return p, nil
			}
		case "escape":
			if p.holdState {
				return p, func() tea.Msg { return pageBackMsg{} }
			}
		// Number keys in HOLD state to jump back to a page.
		case "1", "2", "3", "4", "5", "6", "7", "8":
			if p.holdState {
				pageNum := int(msg.String()[0] - '0')
				return p, func() tea.Msg { return pageJumpMsg{page: pageNum + 1} }
			}
		}
	}

	return p, nil
}

func (p *applyPage) view(_ *WizardState, width, _ int) string {
	if p.holdState {
		return p.viewHold(width)
	}

	var b strings.Builder

	b.WriteString("  " + sQuestion.Render(p.question()) + "\n\n")

	for _, step := range p.steps {
		var mark string
		switch step.status {
		case "done":
			mark = sGo.Render("✓")
		case "running":
			mark = lipgloss.NewStyle().Foreground(cPurple).Render("⠹")
		case "failed":
			mark = sFail.Render("✗")
		default:
			mark = sMuted.Render("○")
		}

		name := sText.Render(step.name)
		dur := ""
		if step.duration > 0 {
			dur = sMuted.Render(fmt.Sprintf("%6.1fs", step.duration.Seconds()))
		}

		_, _ = fmt.Fprintf(&b, "  %s %-52s %s\n", mark, name, dur)
	}

	if p.logTail != "" {
		b.WriteString("\n  " + sMuted.Render(p.masker.mask(p.logTail)))
		b.WriteString(strings.Repeat(" ", max(0, width-lipgloss.Width(p.logTail)-20)))
		b.WriteString(sKey.Render("l") + " full log\n")
	}

	return b.String()
}

func (p *applyPage) viewHold(_ int) string {
	var b strings.Builder

	b.WriteString("  " + sQuestion.Render("Holding.") + "\n\n")

	// Summary line.
	doneCount := 0
	failedName := ""
	pendingCount := 0
	for _, step := range p.steps {
		switch step.status {
		case "done":
			doneCount++
		case "failed":
			failedName = step.name
		default:
			if step.status == "pending" {
				pendingCount++
			}
		}
	}

	b.WriteString("  " + sGo.Render(fmt.Sprintf("✓ %d steps green", doneCount)) +
		" · " + sFail.Render("✗ "+failedName) +
		" · " + sMuted.Render(fmt.Sprintf("○ %d waiting", pendingCount)) + "\n\n")

	// HOLD gutter.
	holdContent := fmt.Sprintf("HOLD — %s", p.holdError)
	if p.holdDetail != "" {
		holdContent += "\n\n" + p.holdDetail
	}
	holdContent += "\n\n" + sKey.Render("r") + " retry step · " +
		sKey.Render("esc") + " back · " +
		sKey.Render("l") + " full log · " +
		sKey.Render("ctrl+c") + " abort"

	gutter := sGutter.Render(holdContent)
	b.WriteString("  " + gutter + "\n\n")

	b.WriteString("  " + sMuted.Render("no silent retries · no skipped steps") + "\n")

	return b.String()
}
