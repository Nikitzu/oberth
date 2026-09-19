package setuptui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/oberthci/oberth/internal/installer"
)

// applyStep tracks one step in the apply process.
type applyStep struct {
	name     string
	status   string // "pending", "running", "done", "failed"
	duration time.Duration
}

// applyLogMsg delivers a single masked log line to the TUI.
type applyLogMsg struct {
	line string
}

type applyPage struct {
	steps        []applyStep
	currentStep  int
	holdState    bool
	holdError    string
	holdDetail   string
	logTail      string
	logLines     []string
	showFullLog  bool
	startTime    time.Time
	masker       *masker
	ceremony     *ceremonyPage
	showCeremony bool
	applyDone    bool

	// Channel-based message passing from the installer goroutine.
	msgCh chan tea.Msg
}

func newApplyPage() *applyPage {
	return &applyPage{
		masker:   newMasker(),
		ceremony: newCeremonyPage(),
		msgCh:    make(chan tea.Msg, 64),
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

// wipeSecrets zeros every secret this page or its ceremony still holds.
// Called on every teardown path — acknowledged, aborted, or interrupted —
// so no exit route leaves token bytes live on the heap (S2/S10).
func (p *applyPage) wipeSecrets() {
	p.ceremony.zeroToken()
	p.masker.wipe()
}

func (p *applyPage) title() string {
	if p.holdState {
		return "ignition — hold"
	}
	return "ignition"
}

func (p *applyPage) question() string {
	if p.holdState {
		return "Holding."
	}
	return "Applying."
}

func (p *applyPage) keys() string {
	if p.showCeremony {
		return sKey.Render("r") + " reveal · " + sKey.Render("c") + " copy · " + sKey.Render("enter") + " acknowledge"
	}
	if p.holdState {
		return sKey.Render("r") + " retry · " + sKey.Render("1..8") + " revisit page · " + sKey.Render("l") + " full log · " + sKey.Render("ctrl+c") + " abort"
	}
	return sKey.Render("l") + " full log · " + sKey.Render("ctrl+c") + " abort"
}

// bandPercent returns the apply step progress as a fraction (0.0-1.0) for the
// progress band. During apply, the band tracks step completion, not page count.
func (p *applyPage) bandPercent() float64 {
	done := 0
	for _, s := range p.steps {
		if s.status == "done" {
			done++
		}
	}
	if len(p.steps) == 0 {
		return 0
	}
	return float64(done) / float64(len(p.steps))
}

func (p *applyPage) init(state *WizardState) tea.Cmd {
	p.startTime = time.Now()

	// In dry-mode, we don't actually apply.
	if state.Config.DryRun {
		return nil
	}

	// Start the real apply by running installer.Execute in a goroutine and
	// listening on the channel for progress messages.
	return p.startApply(state)
}

// listenForMsg returns a tea.Cmd that blocks on the message channel and
// delivers one message. Chained in update() to consume the full stream.
func (p *applyPage) listenForMsg() tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-p.msgCh
		if !ok {
			return applyDoneMsg{}
		}
		return msg
	}
}

// startApply launches installer.Execute in a goroutine and returns a tea.Cmd
// that begins listening for progress messages.
func (p *applyPage) startApply(state *WizardState) tea.Cmd {
	cfg := state.Config
	cfg.BinaryVersion = "dev" // will be overridden from opts if set

	return func() tea.Msg {
		go p.runInstaller(cfg)
		// Return the first message from the channel.
		msg, ok := <-p.msgCh
		if !ok {
			return applyDoneMsg{}
		}
		return msg
	}
}

// stepPatterns maps installer output substrings to step indices. The installer
// writes progress to deps.Output; the applyWriter matches these patterns to
// advance the step tracker.
var stepPatterns = []struct {
	step    int
	pattern string
}{
	{0, "helm"},
	{1, "namespace"},
	{2, "Installing Oberth"},
	{2, "Upgrading Oberth"},
	{3, "Waiting for"},
	{3, "rollout"},
	{4, "Installing OpenBao"},
	{4, "Upgrading OpenBao"},
	{5, "secretstore"},
	{5, "secret store"},
	{6, "release leg"},
	{6, "release-tier"},
	{7, "tls"},
	{7, "fingerprint"},
	{7, "SSH host key"},
	{8, "uplink"},
	{8, "Uplink"},
	{9, "upstream"},
	{9, "Upstream"},
	{9, "discovery"},
	{9, "deploy key"},
	{10, "audit"},
	{10, "genesis"},
	{10, "Ready"},
	{10, "readyz"},
}

// runInstaller runs the installer in a goroutine and sends progress messages
// to the channel. It closes the channel when done.
func (p *applyPage) runInstaller(cfg installer.Config) {
	defer close(p.msgCh)

	// Mark the first step as running.
	p.msgCh <- applyStepMsg{
		step:   0,
		total:  len(p.steps),
		name:   p.steps[0].name,
		status: "running",
	}

	w := &applyWriter{
		ch:          p.msgCh,
		masker:      p.masker,
		currentStep: 0,
		totalSteps:  len(p.steps),
		stepStarts:  make(map[int]time.Time),
	}
	w.stepStarts[0] = time.Now()

	ctx := context.Background()
	err := installer.Execute(ctx, cfg, installer.InstallDeps{
		Output: w,
		Input:  os.Stdin,
	})

	// Mark the last running step as done (or failed).
	if err != nil {
		p.msgCh <- applyStepMsg{
			step:   w.currentStep,
			total:  len(p.steps),
			name:   p.steps[min(w.currentStep, len(p.steps)-1)].name,
			status: "failed",
			err:    err,
		}
		p.msgCh <- applyDoneMsg{err: err}
		return
	}

	// Complete any remaining running step.
	if w.currentStep < len(p.steps) {
		elapsed := time.Since(w.stepStarts[w.currentStep])
		p.msgCh <- applyStepMsg{
			step:     w.currentStep,
			total:    len(p.steps),
			name:     p.steps[w.currentStep].name,
			status:   "done",
			duration: elapsed,
		}
	}

	// Mark all remaining pending steps as done.
	for i := w.currentStep + 1; i < len(p.steps); i++ {
		p.msgCh <- applyStepMsg{
			step:   i,
			total:  len(p.steps),
			name:   p.steps[i].name,
			status: "done",
		}
	}

	p.msgCh <- applyDoneMsg{}
}

// applyWriter is an io.Writer that captures installer output, matches step
// patterns, detects the bearer token, and sends tea.Msg to the channel.
type applyWriter struct {
	ch          chan tea.Msg
	masker      *masker
	mu          sync.Mutex
	buf         bytes.Buffer
	currentStep int
	totalSteps  int
	stepStarts  map[int]time.Time
}

func (w *applyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.buf.Write(p)
	for {
		line, err := w.buf.ReadString('\n')
		if err != nil {
			// Incomplete line — put it back for next write.
			w.buf.WriteString(line)
			break
		}
		line = strings.TrimRight(line, "\n\r")
		if line == "" {
			continue
		}
		w.processLine(line)
	}
	return len(p), nil
}

func (w *applyWriter) processLine(line string) {
	// Check for bearer token: lines starting with "oberth_" or following
	// "Uplink token for" are token values (see extractUplinkToken).
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "oberth_") {
		w.ch <- ceremonyTokenMsg{token: []byte(trimmed)}
		return // Never send the token to the log tail.
	}

	// Match step patterns to advance the tracker.
	for _, sp := range stepPatterns {
		if sp.step > w.currentStep && strings.Contains(line, sp.pattern) {
			// Complete the current step.
			elapsed := time.Duration(0)
			if start, ok := w.stepStarts[w.currentStep]; ok {
				elapsed = time.Since(start)
			}
			w.ch <- applyStepMsg{
				step:     w.currentStep,
				total:    w.totalSteps,
				name:     line,
				status:   "done",
				duration: elapsed,
			}

			// Start the new step.
			w.currentStep = sp.step
			w.stepStarts[sp.step] = time.Now()
			w.ch <- applyStepMsg{
				step:   sp.step,
				total:  w.totalSteps,
				name:   line,
				status: "running",
			}
			break
		}
	}

	// Send as log tail (masked).
	masked := w.masker.mask(trimmed)
	select {
	case w.ch <- applyLogMsg{line: masked}:
	default:
		// Channel full — drop the log line rather than blocking the installer.
	}
}

func (p *applyPage) update(msg tea.Msg, _ *WizardState) (page, tea.Cmd) {
	switch msg := msg.(type) {
	case applyStepMsg:
		if msg.step < len(p.steps) {
			p.steps[msg.step].status = msg.status
			p.steps[msg.step].duration = msg.duration
			p.currentStep = msg.step

			if msg.status == "failed" {
				p.holdState = true
				p.holdError = p.steps[msg.step].name
				if msg.err != nil {
					p.holdDetail = msg.err.Error()
				}
				return p, p.listenForMsg()
			}
		}
		return p, p.listenForMsg()

	case applyLogMsg:
		p.logTail = msg.line
		if len(p.logLines) < 1000 {
			p.logLines = append(p.logLines, msg.line)
		}
		return p, p.listenForMsg()

	case applyDoneMsg:
		p.applyDone = true
		if msg.err != nil {
			p.holdState = true
			p.holdError = msg.err.Error()
			return p, nil
		}
		// If the ceremony has a token that hasn't been acknowledged,
		// show the ceremony first.
		if p.ceremony.token != nil && !p.ceremony.acknowledged {
			p.showCeremony = true
			return p, nil
		}
		// All steps done — transition to done page.
		return p, func() tea.Msg { return pageCompleteMsg{} }

	case ceremonyTokenMsg:
		// The token ceremony interrupts the apply after the mint step.
		// Register with the masker for log-tail safety (S3) first — the
		// masker copies — then hand the buffer to the ceremony page, which
		// copies and ZEROS the source (S2: no stray copy survives on the
		// message value).
		p.masker.register(msg.token)
		p.ceremony.setToken(msg.token)
		p.showCeremony = true
		return p, p.listenForMsg()

	case tea.KeyPressMsg:
		// If the ceremony is showing, delegate to it.
		if p.showCeremony {
			newCeremony, cmd := p.ceremony.update(msg, nil)
			p.ceremony = newCeremony.(*ceremonyPage)
			if p.ceremony.acknowledged {
				p.showCeremony = false
				// If the installer has already finished, advance to done.
				if p.applyDone {
					return p, func() tea.Msg { return pageCompleteMsg{} }
				}
			}
			return p, cmd
		}

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

func (p *applyPage) view(state *WizardState, width, height int) string {
	// Show the ceremony overlay if active.
	if p.showCeremony {
		return p.ceremony.view(state, width, height)
	}

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
