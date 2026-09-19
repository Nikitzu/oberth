package setuptui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/bubbles/v2/progress"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/oberthci/oberth/internal/installer"
)

const totalPages = 13

// ---------- typed messages for page transitions ----------

type pageCompleteMsg struct{}
type pageBackMsg struct{}

// pageJumpMsg requests a jump to a specific page (1-based, from review).
type pageJumpMsg struct{ page int }

// applyStepMsg reports progress from the apply phase.
type applyStepMsg struct {
	step     int
	total    int
	name     string
	status   string // "running", "done", "failed"
	duration time.Duration
	logLine  string
	err      error
}

// applyDoneMsg reports the apply completed.
type applyDoneMsg struct {
	err error
}

// ceremonyTokenMsg delivers the bearer token to the ceremony page.
type ceremonyTokenMsg struct {
	token []byte
}

// Options holds the CLI-level options for the setup wizard.
type Options struct {
	DryMode    bool // wizard only, print the command, no apply
	Plain      bool // no TUI, sequential prompts
	Accessible bool // screen reader mode
}

// WizardState holds the wizard's configuration state. It wraps
// installer.Config and adds wizard-specific metadata. The bearer token
// is deliberately excluded — it lives only in the ceremony model's
// local state (S2/S4).
type WizardState struct {
	Config installer.Config

	// Wizard-specific metadata (not in installer.Config).
	SelectedContext string
	ClusterInfo     clusterInfoMsg
	SSHKeyPath      string
	UplinkIdentity  string
	ForgeType       string // codeberg, github, forgejo, gitlab
	ForgeOrg        string
	ForgeAuth       string // deploy-key, token
	TLSMode         string // self-signed, byo
	ProxyEnabled    bool
	StoreMode       string // install-dev, install-prod, connect
	StoreAddress    string
	StoreCACert     string
	AllowedPaths    []string

	// Track page validation state for the review.
	pageValid [totalPages]bool
}

// Run is the main entry point for the setup wizard. It creates a Bubble Tea
// program with the wizard model and runs it.
func Run(ctx context.Context, opts Options, output io.Writer) error {
	if opts.Plain || opts.Accessible {
		return runPlain(ctx, opts, output)
	}

	w := newWizard(opts)
	p := tea.NewProgram(
		w,
		tea.WithContext(ctx),
		tea.WithOutput(output),
	)

	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("setup wizard: %w", err)
	}

	// Check if the wizard was aborted.
	if wiz, ok := finalModel.(*wizard); ok && wiz.aborted {
		return installer.ErrInterrupted
	}

	return nil
}

// wizard is the root tea.Model that routes between pages.
type wizard struct {
	opts     Options
	state    WizardState
	masker   *masker
	band     progress.Model
	page     int // 0-indexed current page
	width    int
	height   int
	pages    []page
	showHelp bool
	aborted  bool
	quitting bool
	started  time.Time
}

// page is implemented by each wizard page.
type page interface {
	title() string    // stage name for the top bar
	question() string // the page question
	init(state *WizardState) tea.Cmd
	update(msg tea.Msg, state *WizardState) (page, tea.Cmd)
	view(state *WizardState, width, height int) string
}

func newWizard(opts Options) *wizard {
	w := &wizard{
		opts:    opts,
		masker:  newMasker(),
		band:    newBand(),
		started: time.Now(),
		state: WizardState{
			Config: installer.Config{
				Dev:       true,
				Namespace: "oberth",
			},
			TLSMode:   "self-signed",
			StoreMode: "install-prod",
			ForgeType: "codeberg",
			ForgeAuth: "deploy-key",
		},
	}

	w.pages = []page{
		newWelcomePage(),      // 1
		newClusterPage(),      // 2
		newModePage(),         // 3
		newNamespacesPage(),   // 4
		newExecutionPage(),    // 5
		newStorePage(),        // 6
		newStoreConnectPage(), // 7
		newTLSPage(),          // 8
		newUplinkPage(),       // 9
		newGitPage(),          // 10
		newForgePage(),        // 11
		newReviewPage(),       // 12
		newApplyPage(),        // 13
	}

	return w
}

func (w *wizard) Init() tea.Cmd {
	cmds := []tea.Cmd{
		tea.RequestWindowSize,
	}
	if len(w.pages) > 0 {
		cmds = append(cmds, w.pages[0].init(&w.state))
	}
	return tea.Batch(cmds...)
}

func (w *wizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width = msg.Width
		w.height = msg.Height
		w.band.SetWidth(msg.Width)
		return w, nil

	case tea.KeyPressMsg:
		// Global keys handled before page delegation.
		switch msg.String() {
		case "ctrl+c":
			w.aborted = true
			w.quitting = true
			return w, tea.Quit
		case "?":
			if !w.showHelp {
				w.showHelp = true
				return w, nil
			}
			w.showHelp = false
			return w, nil
		}

		// Dismiss help overlay.
		if w.showHelp {
			switch msg.String() {
			case "escape", "?":
				w.showHelp = false
			}
			return w, nil
		}

	case pageCompleteMsg:
		return w.advance()

	case pageBackMsg:
		return w.goBack()

	case pageJumpMsg:
		if msg.page >= 1 && msg.page <= len(w.pages) {
			w.page = msg.page - 1
			cmd := w.pages[w.page].init(&w.state)
			return w, cmd
		}
		return w, nil
	}

	// Delegate to current page.
	if w.page < len(w.pages) {
		newPage, cmd := w.pages[w.page].update(msg, &w.state)
		w.pages[w.page] = newPage
		return w, cmd
	}

	return w, nil
}

func (w *wizard) advance() (*wizard, tea.Cmd) {
	// Mark current page as valid.
	if w.page < totalPages {
		w.state.pageValid[w.page] = true
	}

	// Skip store-connect page (7, index 6) when installing OpenBao.
	nextPage := w.page + 1
	if nextPage == 6 && w.state.StoreMode != "connect" {
		nextPage = 7 // skip to TLS page
	}

	if nextPage >= len(w.pages) {
		// Past the last page — check for done page.
		w.quitting = true
		return w, tea.Quit
	}

	w.page = nextPage
	cmd := w.pages[w.page].init(&w.state)
	return w, cmd
}

func (w *wizard) goBack() (*wizard, tea.Cmd) {
	if w.page <= 0 {
		return w, nil
	}
	prevPage := w.page - 1

	// Skip store-connect page going backwards too.
	if prevPage == 6 && w.state.StoreMode != "connect" {
		prevPage = 5
	}

	w.page = prevPage
	cmd := w.pages[w.page].init(&w.state)
	return w, cmd
}

func (w *wizard) View() tea.View {
	v := tea.NewView(w.renderContent())
	v.AltScreen = true
	return v
}

func (w *wizard) renderContent() string {
	if w.width == 0 || w.height == 0 {
		return ""
	}

	var b strings.Builder

	// The welcome page has no chrome.
	if w.page == 0 {
		pageView := w.pages[0].view(&w.state, w.width, w.height)
		b.WriteString(pageView)

		// Pad to fill the screen (no band on welcome).
		lines := strings.Count(b.String(), "\n") + 1
		for lines < w.height {
			b.WriteByte('\n')
			lines++
		}
		if w.showHelp {
			return w.overlayHelp(b.String())
		}
		return b.String()
	}

	// Top bar: ▲ oberth setup — <stage>                         step n/13
	topLeft := fmt.Sprintf(" %s %s",
		lipgloss.NewStyle().Foreground(cPurple).Render(brandMark),
		sTopBar.Render(fmt.Sprintf("oberth setup — %s", w.pages[w.page].title())),
	)
	topRight := sTopBar.Render(fmt.Sprintf("step %d/%d ", w.page+1, totalPages))
	padLen := w.width - lipgloss.Width(topLeft) - lipgloss.Width(topRight)
	if padLen < 1 {
		padLen = 1
	}
	b.WriteString(topLeft + strings.Repeat(" ", padLen) + topRight + "\n")
	b.WriteByte('\n')

	// Page content.
	contentHeight := w.height - 4 // top bar(1) + blank(1) + key line(1) + band(1)
	pageView := w.pages[w.page].view(&w.state, w.width, contentHeight)
	b.WriteString(pageView)

	// Pad content to fill available space.
	lines := strings.Count(pageView, "\n") + 1
	for lines < contentHeight {
		b.WriteByte('\n')
		lines++
	}

	// Key line (dim, page-specific keys described in page views).
	keyLine := w.keyLine()
	b.WriteString(keyLine + "\n")

	// The band — last line, full width.
	w.band.SetWidth(w.width)
	var bandPercent float64
	if w.page < totalPages {
		bandPercent = float64(w.page+1) / float64(totalPages)
	} else {
		bandPercent = 1.0
	}
	b.WriteString(w.band.ViewAs(bandPercent))

	content := b.String()
	if w.showHelp {
		return w.overlayHelp(content)
	}
	return content
}

func (w *wizard) keyLine() string {
	keys := []string{}

	switch w.page {
	case 0:
		keys = append(keys, sKey.Render("enter")+" begin")
	case totalPages - 1:
		// Apply page has its own keys.
		keys = append(keys, sKey.Render("l")+" full log")
		keys = append(keys, sKey.Render("ctrl+c")+" abort")
	case totalPages - 2:
		// Review page.
		keys = append(keys, sKey.Render("1..8")+" revisit")
		keys = append(keys, sKey.Render("d")+" dry-run")
		keys = append(keys, sKey.Render("enter")+" apply")
		keys = append(keys, sKey.Render("esc")+" back")
	default:
		keys = append(keys, sKey.Render("enter")+" continue")
		keys = append(keys, sKey.Render("esc")+" back")
	}
	keys = append(keys, sKey.Render("?")+" help")

	return " " + sMuted.Render(strings.Join(keys, " "+sMuted.Render("·")+" "))
}

func (w *wizard) overlayHelp(backdrop string) string {
	helpContent := lipgloss.JoinVertical(lipgloss.Left,
		"",
		sKey.Render("navigate")+"   "+sMuted.Render("up/down or j/k · tab/shift+tab fields · left/right options · / filter"),
		sKey.Render("advance")+"    "+sMuted.Render("enter continue · esc back one page (answers kept)"),
		sKey.Render("pages")+"      "+sMuted.Render("1..8 jump (review) · d dry-run / discovery · v verify"),
		sKey.Render("modes")+"      "+sMuted.Render("a accessible (screen reader) · p plain (no color/motion)"),
		sKey.Render("escape")+"     "+sMuted.Render("ctrl+c abort — confirmed; states what already exists"),
		"",
		sMuted.Render("the wizard is a skin over ")+sInfo.Render("oberth install")+sMuted.Render(" — every answer maps to"),
		sMuted.Render("a flag; --dry-run prints the equivalent non-interactive command."),
		"",
		sMuted.Render("                                              ")+sKey.Render("?")+" or "+sKey.Render("esc")+" closes help",
	)

	modal := sHelpModal.Width(min(72, w.width-8)).Render(helpContent)

	// Center the modal over the backdrop.
	backdropLines := strings.Split(backdrop, "\n")
	modalLines := strings.Split(modal, "\n")
	startRow := (len(backdropLines) - len(modalLines)) / 2
	if startRow < 0 {
		startRow = 0
	}
	modalWidth := lipgloss.Width(modal)
	startCol := (w.width - modalWidth) / 2
	if startCol < 0 {
		startCol = 0
	}

	result := make([]string, len(backdropLines))
	for i, line := range backdropLines {
		mi := i - startRow
		if mi >= 0 && mi < len(modalLines) {
			// Dim the backdrop line and overlay the modal.
			dimLine := sMuted.Render(stripAnsi(line))
			if startCol > 0 && len(dimLine) > startCol {
				result[i] = dimLine[:startCol] + modalLines[mi]
			} else {
				result[i] = strings.Repeat(" ", startCol) + modalLines[mi]
			}
		} else {
			result[i] = sMuted.Render(stripAnsi(line))
		}
	}

	return strings.Join(result, "\n")
}

// stripAnsi removes ANSI escape sequences from a string.
func stripAnsi(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		if r == '\x1b' {
			inEsc = true
			continue
		}
		if inEsc {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// BuildCommandLine generates the equivalent `oberth install` command
// from the current wizard state, for --dry-mode output.
func BuildCommandLine(state *WizardState) string {
	var args []string
	args = append(args, "oberth install")

	if state.Config.Dev {
		args = append(args, "--dev")
	}
	if state.Config.Production {
		args = append(args, "--production")
	}
	if state.Config.Namespace != "" && state.Config.Namespace != "oberth" {
		args = append(args, fmt.Sprintf("--namespace=%s", state.Config.Namespace))
	}
	if state.Config.ArgoNamespace != "" {
		args = append(args, fmt.Sprintf("--argo-namespace=%s", state.Config.ArgoNamespace))
	}
	if state.Config.OpenBaoNamespace != "" && state.Config.OpenBaoNamespace != "openbao" {
		args = append(args, fmt.Sprintf("--openbao-namespace=%s", state.Config.OpenBaoNamespace))
	}
	if state.Config.InstallSecretStore {
		args = append(args, "--install-secretstore")
	}
	if state.Config.InstallSecretStoreDev {
		args = append(args, "--install-secretstore-dev")
	}
	if state.Config.InstallRekor {
		args = append(args, "--install-rekor")
	}
	if state.Config.NetworkPolicy != "" {
		args = append(args, fmt.Sprintf("--network-policy=%s", state.Config.NetworkPolicy))
	}
	if state.Config.ArgoVaultAddress != "" {
		args = append(args, fmt.Sprintf("--argo-vault-address=%s", state.Config.ArgoVaultAddress))
	}
	if state.Config.ArgoVaultCredentialedRole != "" {
		args = append(args, fmt.Sprintf("--argo-vault-credentialed-role=%s", state.Config.ArgoVaultCredentialedRole))
	}
	for _, dns := range state.Config.TLSExtraDNSNames {
		args = append(args, fmt.Sprintf("--tls-extra-dns-name=%s", dns))
	}
	for _, ip := range state.Config.TLSExtraIPs {
		args = append(args, fmt.Sprintf("--tls-extra-ip=%s", ip))
	}
	if !state.ClusterInfo.isLocal {
		args = append(args, "--yes")
	}

	return strings.Join(args, " \\\n  ")
}
