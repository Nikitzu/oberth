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
	"github.com/charmbracelet/x/ansi"

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
	err      error
}

// applyDoneMsg reports the apply completed.
type applyDoneMsg struct {
	err error
}

// ceremonyTokenMsg delivers one once-only credential to the ceremony page.
// label identifies it ("Bearer token", "Root token", "Unseal key"); an empty
// label means the bare-line backstop detected an oberth_ token in the
// output stream and defaults to "Bearer token".
type ceremonyTokenMsg struct {
	label string
	token []byte
}

// reviewSectionPages maps the review page's section numbers (1..8) to
// 1-based wizard pages for pageJumpMsg. The apply page's HOLD state uses
// the SAME numbering — one table, no drift. Section 5 (store) targets the
// store-mode page; store-connect is reached from it when the mode is
// "connect", matching forward navigation.
var reviewSectionPages = map[int]int{
	1: 2,  // cluster
	2: 3,  // mode
	3: 4,  // namespaces
	4: 5,  // network / execution
	5: 6,  // store
	6: 8,  // tls
	7: 9,  // uplink
	8: 11, // forge
}

// Options holds the CLI-level options for the setup wizard.
type Options struct {
	DryMode    bool // wizard only, print the command, no apply
	Plain      bool // no TUI, sequential prompts
	Accessible bool // screen reader mode
	// BinaryVersion is the oberth binary's version, threaded from main so
	// an apply resolves the same chart version `oberth install` would.
	BinaryVersion string
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

	// Defensive teardown regardless of how the program ended: no exit
	// route may leave secret bytes live on the heap (S2/S10).
	wiz, isWizard := finalModel.(*wizard)
	if isWizard {
		wiz.teardownSecrets()
	}

	if err != nil {
		return fmt.Errorf("setup wizard: %w", err)
	}

	// Check if the wizard was aborted.
	if isWizard && wiz.aborted {
		return installer.ErrInterrupted
	}

	// --dry-mode: print the equivalent non-interactive command on the
	// primary buffer (the alt screen is gone, so this lands in normal
	// scrollback — it contains no secret by construction: BuildCommandLine
	// reads only non-secret installer.Config fields).
	if isWizard && wiz.dryDone && opts.DryMode {
		_, _ = fmt.Fprintln(output, "--dry-mode: no changes applied. Equivalent command:")
		_, _ = fmt.Fprintln(output)
		_, _ = fmt.Fprintln(output, "  "+BuildCommandLine(&wiz.state))
		return nil
	}

	return nil
}

// wizard is the root tea.Model that routes between pages.
type wizard struct {
	opts        Options
	state       WizardState
	band        progress.Model
	page        int // 0-indexed current page
	width       int
	height      int
	pages       []page
	showHelp    bool
	aborted     bool
	quitting    bool
	confirmQuit bool // first ctrl+c arms, second aborts (S10: confirmed abort)
	dryDone     bool // --dry-mode: wizard completed through review
	started     time.Time
}

// page is implemented by each wizard page.
type page interface {
	title() string    // stage name for the top bar
	question() string // the page question
	keys() string     // page-specific key hints for the bottom key line
	init(state *WizardState) tea.Cmd
	update(msg tea.Msg, state *WizardState) (page, tea.Cmd)
	view(state *WizardState, width, height int) string
}

func newWizard(opts Options) *wizard {
	w := &wizard{
		opts:    opts,
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

	// Thread the binary version so TUI apply resolves the same chart
	// version the installer would (never a hardcoded "dev" placeholder).
	if opts.BinaryVersion != "" {
		w.state.Config.BinaryVersion = opts.BinaryVersion
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
		&donePage{},           // done (after apply)
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
		// Any key other than a second ctrl+c disarms the abort confirmation.
		if msg.String() != "ctrl+c" {
			w.confirmQuit = false
		}

		// Global keys handled before page delegation.
		switch msg.String() {
		case "ctrl+c":
			// S10: abort is confirmed, never instant. The first ctrl+c arms;
			// the second aborts, after zeroing every secret still held.
			if !w.confirmQuit {
				w.confirmQuit = true
				return w, nil
			}
			w.teardownSecrets()
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
			case "esc", "?":
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

	// --dry-mode: the review page is the last stop. The apply page must
	// never start; Run prints the equivalent `oberth install` command on
	// the primary buffer after the program exits.
	if nextPage == totalPages-1 && w.opts.DryMode {
		w.dryDone = true
		w.quitting = true
		return w, tea.Quit
	}

	if nextPage >= len(w.pages) {
		// Past the last page — check for done page.
		w.quitting = true
		return w, tea.Quit
	}

	// When advancing from apply to done, populate step results from the
	// apply page so the done page shows real counts instead of fabricated
	// "11/11 green" (P4-3).
	if nextPage == len(w.pages)-1 { // entering the done page
		if ap, ok := w.pages[nextPage-1].(*applyPage); ok {
			if dp, ok := w.pages[nextPage].(*donePage); ok {
				dp.totalSteps = len(ap.steps)
				dp.greenCount = 0
				for _, s := range ap.steps {
					if s.status == "done" {
						dp.greenCount++
					}
				}
			}
		}
	}

	w.page = nextPage
	cmd := w.pages[w.page].init(&w.state)
	return w, cmd
}

// teardownSecrets zeros every secret any page still holds. Idempotent;
// called on the confirmed-abort path and again defensively after the
// program exits (S2/S10: no exit route leaves secret bytes live).
func (w *wizard) teardownSecrets() {
	for _, p := range w.pages {
		if pg, ok := p.(*applyPage); ok {
			// Cancel the installer context so it stops mutating on abort.
			if pg.cancel != nil {
				pg.cancel()
			}
			pg.wipeSecrets()
		}
	}
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
	var topRight string
	if w.page+1 > totalPages {
		topRight = "" // done page — no step counter
	} else {
		topRight = sTopBar.Render(fmt.Sprintf("step %d/%d ", w.page+1, totalPages))
	}
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
	switch {
	case w.page < totalPages-1:
		// Config pages: band = page / 13.
		bandPercent = float64(w.page+1) / float64(totalPages)
	case w.page == totalPages-1:
		// Apply page: band tracks apply step progress.
		if ap, ok := w.pages[w.page].(*applyPage); ok {
			bandPercent = ap.bandPercent()
		} else {
			bandPercent = float64(w.page+1) / float64(totalPages)
		}
	default:
		// Done page: band reflects actual step completion, not a
		// hardcoded 100%. A partial completion after failures must not
		// show a full bar.
		if dp, ok := w.pages[w.page].(*donePage); ok && dp.totalSteps > 0 {
			bandPercent = float64(dp.greenCount) / float64(dp.totalSteps)
		} else {
			bandPercent = 1.0
		}
	}
	b.WriteString(w.band.ViewAs(bandPercent))

	content := b.String()
	if w.showHelp {
		return w.overlayHelp(content)
	}
	return content
}

func (w *wizard) keyLine() string {
	if w.confirmQuit {
		return " " + sFail.Render("ctrl+c again to abort") + " " +
			sMuted.Render("· any other key continues")
	}

	// M3: each page exposes its own key hints.
	pageKeys := ""
	if w.page < len(w.pages) {
		pageKeys = w.pages[w.page].keys()
	}
	if pageKeys != "" {
		return " " + sMuted.Render(pageKeys+" "+sMuted.Render("·")+" ") + sKey.Render("?") + sMuted.Render(" help")
	}

	// Fallback for pages that return empty keys.
	keys := []string{
		sKey.Render("enter") + " continue",
		sKey.Render("esc") + " back",
		sKey.Render("?") + " help",
	}
	return " " + sMuted.Render(strings.Join(keys, " "+sMuted.Render("·")+" "))
}

func (w *wizard) overlayHelp(backdrop string) string {
	helpContent := lipgloss.JoinVertical(lipgloss.Left,
		"",
		sKey.Render("navigate")+"   "+sMuted.Render("up/down or j/k · tab/shift+tab fields · left/right options · / filter"),
		sKey.Render("advance")+"    "+sMuted.Render("enter continue · esc back one page (answers kept)"),
		sKey.Render("pages")+"      "+sMuted.Render("1..8 jump (review) · d dry-mode / discovery · v verify"),
		sKey.Render("modes")+"      "+sMuted.Render("a accessible (screen reader) · p plain (no color/motion)"),
		sKey.Render("escape")+"     "+sMuted.Render("ctrl+c abort — confirmed; states what already exists"),
		"",
		sMuted.Render("the wizard is a skin over ")+sInfo.Render("oberth install")+sMuted.Render(" — every answer maps to"),
		sMuted.Render("a flag; --dry-mode prints the equivalent non-interactive command."),
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

// stripAnsi removes ANSI escape sequences from a string, using the
// x/ansi package already in the dependency tree.
func stripAnsi(s string) string {
	return ansi.Strip(s)
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
	// --yes suppresses the installer's own remote-cluster consent gate.
	// Recommend it ONLY when a probe positively identified a remote
	// cluster — never because detection failed or was skipped (the zero
	// value of ClusterInfo must not read as "remote, consent granted").
	if state.ClusterInfo.err == nil && state.ClusterInfo.context != "" && !state.ClusterInfo.isLocal {
		args = append(args, "--yes")
	}

	return strings.Join(args, " \\\n  ")
}
