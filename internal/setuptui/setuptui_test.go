package setuptui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/oberthci/oberth/internal/installer"
)

func keyPress(code rune, text string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Text: text})
}

func enterKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
}

// --- S7: https-only at the field level, shared by TUI and plain paths ---

func TestValidateStoreAddress(t *testing.T) {
	cases := []struct {
		addr    string
		wantErr bool
		wantMsg string
	}{
		{"https://openbao.skipops.internal:8200", false, ""},
		{"http://openbao.skipops.internal:8200", true, "https required"},
		{"HTTP://openbao.internal:8200", true, "https required"},
		{"ftp://openbao.internal", true, "https://"},
		{"openbao.internal:8200", true, ""},
		{"", true, "required"},
		{"https://", true, "host"},
		{"   ", true, "required"},
	}
	for _, tc := range cases {
		err := validateStoreAddress(tc.addr)
		if tc.wantErr && err == nil {
			t.Errorf("validateStoreAddress(%q): expected error, got nil", tc.addr)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("validateStoreAddress(%q): unexpected error %v", tc.addr, err)
		}
		if tc.wantMsg != "" && err != nil && !strings.Contains(err.Error(), tc.wantMsg) {
			t.Errorf("validateStoreAddress(%q): error %q does not contain %q", tc.addr, err, tc.wantMsg)
		}
	}
}

func TestValidateStoreAddressRejectsPlainHTTPWithInvariantMessage(t *testing.T) {
	err := validateStoreAddress("http://x.example")
	if !errors.Is(err, errHTTPRejected) {
		t.Fatalf("http:// must be rejected with the TLS invariant error, got: %v", err)
	}
}

// --- network policy vocabulary: display labels vs installer values ---

func TestCanonicalNetworkPolicy(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"strict", "true", true},
		{"auto", "auto", true},
		{"off", "false", true},
		{"true", "true", true},
		{"false", "false", true},
		{"lenient", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := canonicalNetworkPolicy(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("canonicalNetworkPolicy(%q) = %q,%v; want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestExecutionPageWritesInstallerVocabulary(t *testing.T) {
	p := newExecutionPage() // default network policy display value is "strict"
	state := &WizardState{}
	_, cmd := p.update(enterKey(), state)
	if cmd == nil {
		t.Fatal("expected pageCompleteMsg cmd from a valid execution page")
	}
	if state.Config.NetworkPolicy != "true" {
		t.Fatalf("NetworkPolicy = %q; the installer accepts only auto|true|false, display labels must never be written", state.Config.NetworkPolicy)
	}
}

// --- --yes consent gating in the dry-mode command ---

func TestBuildCommandLineYesRequiresConfirmedRemote(t *testing.T) {
	// Zero cluster info (probe never ran) must NOT recommend --yes.
	state := &WizardState{}
	if strings.Contains(BuildCommandLine(state), "--yes") {
		t.Fatal("--yes must not be recommended when no cluster probe succeeded")
	}

	// A failed probe must NOT recommend --yes.
	state.ClusterInfo = clusterInfoMsg{context: "gke-prod", err: errors.New("unreachable")}
	if strings.Contains(BuildCommandLine(state), "--yes") {
		t.Fatal("--yes must not be recommended when the cluster probe failed")
	}

	// A confirmed local cluster must NOT get --yes.
	state.ClusterInfo = clusterInfoMsg{context: "k3s-tuxbox", isLocal: true}
	if strings.Contains(BuildCommandLine(state), "--yes") {
		t.Fatal("--yes must not be recommended for a local cluster")
	}

	// Only a confirmed remote cluster gets --yes.
	state.ClusterInfo = clusterInfoMsg{context: "gke-prod", isLocal: false}
	if !strings.Contains(BuildCommandLine(state), "--yes") {
		t.Fatal("--yes expected for a positively identified remote cluster")
	}
}

func TestBuildCommandLineHoldsNoSecretsAndValidPolicy(t *testing.T) {
	state := &WizardState{}
	state.Config.NetworkPolicy = "true"
	state.Config.ArgoVaultAddress = "https://openbao.internal:8200"
	out := BuildCommandLine(state)
	if strings.Contains(out, "strict") || strings.Contains(out, "--network-policy=off") {
		t.Fatalf("dry-mode command contains a non-installer network-policy value: %s", out)
	}
	if !strings.Contains(out, "--network-policy=true") {
		t.Fatalf("expected --network-policy=true in: %s", out)
	}
}

// --- S2/S3: masker holds wipeable bytes, never immortal strings ---

func TestMaskerMaskAndWipe(t *testing.T) {
	m := newMasker()
	secret := []byte("tok-super-secret")
	m.register(secret)

	// The masker copied; mutating the caller's buffer must not affect it.
	secret[0] = 'X'
	masked := m.mask("prefix tok-super-secret suffix")
	if strings.Contains(masked, "tok-super-secret") {
		t.Fatalf("mask failed: %q", masked)
	}
	if !strings.Contains(masked, "********") {
		t.Fatalf("mask marker missing: %q", masked)
	}

	// Wipe must zero the internal buffer and stop masking.
	if len(m.secrets) != 1 {
		t.Fatalf("expected 1 registered secret, got %d", len(m.secrets))
	}
	held := m.secrets[0]
	m.wipe()
	for i, b := range held {
		if b != 0 {
			t.Fatalf("wipe left non-zero byte at %d", i)
		}
	}
	if got := m.mask("tok-super-secret"); got != "tok-super-secret" {
		t.Fatalf("masker still active after wipe: %q", got)
	}
}

func TestMaskerIgnoresEmpty(t *testing.T) {
	m := newMasker()
	m.register(nil)
	m.register([]byte{})
	if len(m.secrets) != 0 {
		t.Fatal("empty secrets must not be registered")
	}
}

// --- S2: credential ceremony lifecycle ---

func TestCeremonyAddCredentialZerosSource(t *testing.T) {
	p := newCeremonyPage()
	src := []byte("bearer-token-value")
	p.addCredential("Bearer token", src)
	if len(p.entries) != 1 || !bytes.Equal(p.entries[0].value, []byte("bearer-token-value")) {
		t.Fatal("ceremony did not take a faithful copy")
	}
	for i, b := range src {
		if b != 0 {
			t.Fatalf("source buffer not zeroed at %d", i)
		}
	}
}

func TestCeremonyRequiresRevealBeforeAck(t *testing.T) {
	p := newCeremonyPage()
	p.addCredential("Bearer token", []byte("tok"))

	// Enter before reveal/copy must not complete and must not zero.
	_, cmd := p.update(enterKey(), nil)
	if cmd != nil {
		t.Fatal("enter before reveal must not complete the ceremony")
	}
	if p.acknowledged || !p.hasCredentials() {
		t.Fatal("credentials must survive an unacknowledged enter")
	}

	// Reveal, then enter: acknowledged, all values zeroed, page completes.
	_, _ = p.update(keyPress('r', "r"), nil)
	if !p.revealed {
		t.Fatal("r must reveal")
	}
	held := p.entries[0].value
	_, cmd = p.update(enterKey(), nil)
	if cmd == nil {
		t.Fatal("acknowledged ceremony must emit a completion cmd")
	}
	if msg := cmd(); msg != (pageCompleteMsg{}) {
		t.Fatalf("expected pageCompleteMsg, got %T", msg)
	}
	if p.hasCredentials() {
		t.Fatal("no credential may remain after acknowledgment")
	}
	for i, b := range held {
		if b != 0 {
			t.Fatalf("credential bytes not zeroed at %d", i)
		}
	}
}

// A production install mints several once-only credentials (root token,
// unseal keys, bearer token); the ceremony must hold them ALL and zeroAll
// must leave no live byte from any of them (S2 across every entry, the
// property the single-token replacement path used to guarantee).
func TestCeremonyAccumulatesAndZeroAllWipesEverything(t *testing.T) {
	p := newCeremonyPage()
	p.addCredential("Root token", []byte("hvs-root-token"))
	p.addCredential("Unseal key", []byte("unseal-key-b64"))
	p.addCredential("Bearer token", []byte("oberth_bearer"))
	if len(p.entries) != 3 {
		t.Fatalf("expected 3 held credentials, got %d", len(p.entries))
	}
	held := make([][]byte, len(p.entries))
	for i := range p.entries {
		held[i] = p.entries[i].value
	}
	p.zeroAll()
	if p.hasCredentials() {
		t.Fatal("zeroAll must empty the ceremony")
	}
	for n, buf := range held {
		for i, b := range buf {
			if b != 0 {
				t.Fatalf("credential %d not zeroed at byte %d", n, i)
			}
		}
	}
}

// --- apply page owns registration order and teardown ---

func TestApplyPageTokenRegistrationAndWipe(t *testing.T) {
	p := newApplyPage()
	src := []byte("mint-token-9f8e")
	_, _ = p.update(ceremonyTokenMsg{token: src}, nil)

	// Source zeroed after delivery (S2).
	for i, b := range src {
		if b != 0 {
			t.Fatalf("message token buffer not zeroed at %d", i)
		}
	}
	// Masker learned the value before it was zeroed (S3).
	if got := p.masker.mask("log: mint-token-9f8e ok"); strings.Contains(got, "mint-token-9f8e") {
		t.Fatalf("masker did not learn the token: %q", got)
	}
	// Ceremony holds its own copy.
	if len(p.ceremony.entries) != 1 || !bytes.Equal(p.ceremony.entries[0].value, []byte("mint-token-9f8e")) {
		t.Fatal("ceremony copy missing or wrong")
	}

	p.wipeSecrets()
	if p.ceremony.hasCredentials() {
		t.Fatal("wipeSecrets must zero the ceremony credentials")
	}
	if got := p.masker.mask("mint-token-9f8e"); got != "mint-token-9f8e" {
		t.Fatal("wipeSecrets must wipe the masker")
	}
}

// --- unimplemented production profile must not be selectable ---

func TestModePageBlocksProduction(t *testing.T) {
	p := newModePage()
	state := &WizardState{}
	state.Config.Dev = true

	_, _ = p.update(keyPress('j', "j"), state) // cursor → production
	_, cmd := p.update(enterKey(), state)
	if cmd != nil {
		t.Fatal("selecting the unimplemented production profile must not advance")
	}
	if p.errMsg == "" {
		t.Fatal("expected an inline error for production")
	}
	if state.Config.Production {
		t.Fatal("Config.Production must stay false")
	}
}

// --- --dry-mode: the wizard must stop before the apply page ---

func TestWizardDryModeStopsBeforeApply(t *testing.T) {
	w := newWizard(Options{DryMode: true})
	w.page = totalPages - 2 // review page
	_, cmd := w.advance()
	if !w.dryDone || !w.quitting {
		t.Fatal("dry-mode advance from review must complete the wizard without applying")
	}
	if w.page != totalPages-2 {
		t.Fatal("dry-mode must never enter the apply page")
	}
	if cmd == nil {
		t.Fatal("expected a quit cmd")
	}
}

// --- S10: abort is confirmed, and confirmed abort wipes secrets ---

func TestWizardCtrlCConfirmedAbort(t *testing.T) {
	w := newWizard(Options{})
	ctrlC := tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl})

	_, _ = w.Update(ctrlC)
	if w.aborted {
		t.Fatal("first ctrl+c must arm, not abort")
	}
	if !w.confirmQuit {
		t.Fatal("first ctrl+c must arm the confirmation")
	}

	// Any other key disarms.
	_, _ = w.Update(keyPress('x', "x"))
	if w.confirmQuit {
		t.Fatal("a non-ctrl+c key must disarm the confirmation")
	}

	// Arm again, seed a token, confirm: aborted and wiped.
	ap, ok := w.pages[totalPages-1].(*applyPage)
	if !ok {
		t.Fatal("last page must be the apply page")
	}
	_, _ = ap.update(ceremonyTokenMsg{token: []byte("tok-abort")}, &w.state)
	_, _ = w.Update(ctrlC)
	_, _ = w.Update(ctrlC)
	if !w.aborted {
		t.Fatal("second ctrl+c must abort")
	}
	if ap.ceremony.hasCredentials() {
		t.Fatal("confirmed abort must zero the ceremony credentials")
	}
}

// --- Bug 1: esc key must trigger back navigation ---

func escKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})
}

func TestEscKeyNavigatesBack(t *testing.T) {
	w := newWizard(Options{})
	// Start on page 2 (cluster page, index 1).
	w.page = 2
	state := &w.state

	// Esc on the mode page (index 2) must produce pageBackMsg.
	p := w.pages[2]
	_, cmd := p.update(escKey(), state)
	if cmd == nil {
		t.Fatal("esc on the mode page must produce a command")
	}
	msg := cmd()
	if _, ok := msg.(pageBackMsg); !ok {
		t.Fatalf("esc must produce pageBackMsg, got %T", msg)
	}
}

func TestEscKeyWorksOnAllConfigPages(t *testing.T) {
	w := newWizard(Options{})
	// Pages that must respond to esc with pageBackMsg (all config pages
	// except welcome and apply/done).
	escPages := []int{
		1,  // cluster
		2,  // mode
		3,  // namespaces
		4,  // execution
		5,  // store
		6,  // store-connect
		7,  // tls
		8,  // uplink
		9,  // git
		10, // forge
		11, // review
	}
	for _, idx := range escPages {
		p := w.pages[idx]
		_, cmd := p.update(escKey(), &w.state)
		if cmd == nil {
			t.Errorf("page %d (%s): esc must produce a command", idx, p.title())
			continue
		}
		msg := cmd()
		if _, ok := msg.(pageBackMsg); !ok {
			t.Errorf("page %d (%s): esc must produce pageBackMsg, got %T", idx, p.title(), msg)
		}
	}
}

func TestEscOnWelcomeQuits(t *testing.T) {
	w := newWizard(Options{})
	p := w.pages[0] // welcome page
	_, cmd := p.update(escKey(), &w.state)
	if cmd == nil {
		t.Fatal("esc on welcome must produce a quit command")
	}
}

func TestEscDismissesHelpOverlay(t *testing.T) {
	w := newWizard(Options{})
	w.showHelp = true
	_, _ = w.Update(escKey())
	if w.showHelp {
		t.Fatal("esc must dismiss the help overlay")
	}
}

// --- Bug 2: hotkeys must not steal from text fields ---

func TestStoreConnectHotkeysDoNotStealFromTextFields(t *testing.T) {
	p := newStoreConnectPage()
	state := &WizardState{}
	p.init(state)

	// Focus on address field (index 0) and type "v" — must append, not verify.
	p.focus = 0
	p.update(keyPress('v', "v"), state)
	if !strings.Contains(p.fields[0].value, "v") {
		t.Fatalf("'v' on address field must be text input, got %q", p.fields[0].value)
	}
	if p.verifying {
		t.Fatal("'v' on address field must not trigger verify")
	}

	// Type "n" on address field — must append, not add path.
	initialPaths := len(p.allowedPaths)
	p.update(keyPress('n', "n"), state)
	if !strings.Contains(p.fields[0].value, "n") {
		t.Fatalf("'n' on address field must be text input, got %q", p.fields[0].value)
	}
	if len(p.allowedPaths) != initialPaths {
		t.Fatal("'n' on address field must not add an allowed path")
	}

	// Focus on CA cert field (index 1) — same guard.
	p.focus = 1
	p.update(keyPress('v', "v"), state)
	if !strings.Contains(p.fields[1].value, "v") {
		t.Fatalf("'v' on CA cert field must be text input, got %q", p.fields[1].value)
	}
}

func TestForgeHotkeyDoesNotStealFromOrgField(t *testing.T) {
	p := newForgePage()
	state := &WizardState{}
	p.init(state)

	// Focus on org field (index 1) and type "d" — must append, not discover.
	p.focusField = 1
	p.update(keyPress('d', "d"), state)
	if !strings.Contains(p.org, "d") {
		t.Fatalf("'d' on org field must be text input, got %q", p.org)
	}
	if p.discovering {
		t.Fatal("'d' on org field must not trigger discovery")
	}
}

// --- Important-3: masker data race (must pass with -race) ---

func TestMaskerConcurrentAccess(t *testing.T) {
	m := newMasker()
	var wg sync.WaitGroup

	// register goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			m.register([]byte(fmt.Sprintf("secret-%d", i)))
		}
	}()

	// mask goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = m.mask(fmt.Sprintf("line with secret-%d in it", i))
		}
	}()

	// wipe goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			m.wipe()
		}
	}()

	wg.Wait()
}

// --- Minor-1: uplink j/k must type into the identity field when focused ---

func TestUplinkJKTypesIntoIdentityField(t *testing.T) {
	p := newUplinkPage()
	state := &WizardState{}
	p.init(state)

	// focusField starts at 0 (identity).
	p.identity = "admin"
	p.update(keyPress('j', "j"), state)
	if !strings.HasSuffix(p.identity, "j") {
		t.Fatalf("'j' on identity field must type text, got %q", p.identity)
	}
	p.update(keyPress('k', "k"), state)
	if !strings.HasSuffix(p.identity, "k") {
		t.Fatalf("'k' on identity field must type text, got %q", p.identity)
	}
}

// --- Important-2: store-connect actions row ---

func TestStoreConnectActionsRowVerify(t *testing.T) {
	p := newStoreConnectPage()
	state := &WizardState{}
	p.init(state)

	// Set a valid address so verify doesn't reject on validation.
	p.fields[0].value = "https://openbao.internal:8200"

	// Focus on the actions row (index 3) and press 'v'.
	p.focus = 3
	p.update(keyPress('v', "v"), state)
	if !p.verifying {
		t.Fatal("'v' on actions row (focus 3) must trigger verify")
	}
}

func TestStoreConnectActionsRowAddPath(t *testing.T) {
	p := newStoreConnectPage()
	state := &WizardState{}
	p.init(state)

	initialPaths := len(p.allowedPaths)

	// Focus on the actions row (index 3) and press 'n'.
	p.focus = 3
	p.update(keyPress('n', "n"), state)
	if len(p.allowedPaths) != initialPaths+1 {
		t.Fatalf("'n' on actions row must add a path, got %d (was %d)", len(p.allowedPaths), initialPaths)
	}
}

// --- namespace validation shared shape ---

func TestDNS1123LabelRegexp(t *testing.T) {
	valid := []string{"oberth", "oberth-pipelines", "a", "a1", "x-2-y"}
	invalid := []string{"", "-a", "a-", "A", "under_score", "dot.dot", strings.Repeat("a", 64)}
	for _, v := range valid {
		if !dns1123LabelRegexp.MatchString(v) {
			t.Errorf("%q should be valid", v)
		}
	}
	for _, v := range invalid {
		if dns1123LabelRegexp.MatchString(v) {
			t.Errorf("%q should be invalid", v)
		}
	}
}

// --- Critical-1: credentials must reach the ceremony structurally, never
// --- through the log stream ---

// pumpApply drives the apply page's message loop the way the tea runtime
// would: execute the pending cmd, feed the message to update, repeat. It
// stops when no cmd is pending or when the page emits pageCompleteMsg.
func pumpApply(t *testing.T, p *applyPage, state *WizardState, cmd tea.Cmd, maxSteps int) tea.Msg {
	t.Helper()
	for i := 0; i < maxSteps && cmd != nil; i++ {
		msg := cmd()
		if _, ok := msg.(pageCompleteMsg); ok {
			return msg
		}
		var next page
		next, cmd = p.update(msg, state)
		p = next.(*applyPage)
	}
	return nil
}

func TestApplyCredentialSinkRoutesToCeremonyAndMasksLogs(t *testing.T) {
	p := newApplyPage()
	state := &WizardState{}

	const rootToken = "hvs-fake-root-token-value"
	const bearer = "oberth_fake_bearer_value"

	p.execInstaller = func(_ context.Context, _ installer.Config, deps installer.InstallDeps) error {
		// The real installer delivers once-only credentials through the
		// sink; the output stream carries only log-shaped text. A log line
		// that (defensively) echoes a credential value must come out masked.
		deps.CredentialSink("Root token", rootToken)
		deps.CredentialSink("Bearer token", bearer)
		_, _ = deps.Output.Write([]byte("audit chain verified\n"))
		_, _ = deps.Output.Write([]byte("echo " + rootToken + " should be masked\n"))
		return nil
	}

	cmd := p.startApply(state)
	done := pumpApply(t, p, state, cmd, 200)

	// The ceremony must hold BOTH credentials, unacknowledged, and the
	// apply must be waiting on it (not completed past it).
	if done != nil {
		t.Fatal("apply must pause on the ceremony, not complete past it")
	}
	if !p.showCeremony {
		t.Fatal("ceremony must be showing after credential delivery")
	}
	if len(p.ceremony.entries) != 2 {
		t.Fatalf("ceremony must hold 2 credentials, got %d", len(p.ceremony.entries))
	}
	if p.ceremony.entries[0].label != "Root token" || !bytes.Equal(p.ceremony.entries[0].value, []byte(rootToken)) {
		t.Fatal("root token entry missing or wrong")
	}
	if p.ceremony.entries[1].label != "Bearer token" || !bytes.Equal(p.ceremony.entries[1].value, []byte(bearer)) {
		t.Fatal("bearer token entry missing or wrong")
	}

	// No retained log line may carry a raw credential value (S3).
	for _, line := range p.logLines {
		if strings.Contains(line, rootToken) || strings.Contains(line, bearer) {
			t.Fatalf("raw credential leaked into the log lines: %q", line)
		}
	}
	if strings.Contains(p.logTail, rootToken) || strings.Contains(p.logTail, bearer) {
		t.Fatalf("raw credential leaked into the log tail: %q", p.logTail)
	}

	// Acknowledge the ceremony: reveal, enter — then the apply completes.
	_, _ = p.update(keyPress('r', "r"), state)
	_, cmd = p.update(enterKey(), state)
	if cmd == nil {
		t.Fatal("acknowledged ceremony with finished apply must complete the page")
	}
	if msg := cmd(); msg != (pageCompleteMsg{}) {
		t.Fatalf("expected pageCompleteMsg, got %T", msg)
	}
	if p.ceremony.hasCredentials() {
		t.Fatal("acknowledgment must zero all credentials")
	}
}

// --- Critical-2: retry after a failed run must re-run the installer on a
// --- fresh channel — the old code sent on the closed channel and panicked ---

func TestApplyRetryAfterFailureRestartsInstaller(t *testing.T) {
	p := newApplyPage()
	state := &WizardState{}

	calls := 0
	p.execInstaller = func(_ context.Context, _ installer.Config, deps installer.InstallDeps) error {
		calls++
		if calls == 1 {
			_, _ = deps.Output.Write([]byte("Installing Oberth\n"))
			return errors.New("helm timeout")
		}
		_, _ = deps.Output.Write([]byte("audit chain verified\n"))
		return nil
	}

	// First run: fails, enters HOLD.
	cmd := p.startApply(state)
	if done := pumpApply(t, p, state, cmd, 200); done != nil {
		t.Fatal("failed run must not complete the page")
	}
	if !p.holdState {
		t.Fatal("failed run must enter HOLD state")
	}

	// Give the failed run's goroutine time to close its channel — the old
	// implementation reused that closed channel and panicked on the first
	// send of the retry.
	firstCh := p.msgCh

	// Retry: must actually restart the installer and reach done.
	var next page
	next, cmd = p.update(keyPress('r', "r"), state)
	p = next.(*applyPage)
	if cmd == nil {
		t.Fatal("'r' in HOLD must return a restart cmd")
	}
	if p.msgCh == firstCh {
		t.Fatal("retry must run on a fresh channel — the old one is closed")
	}
	if p.holdState {
		t.Fatal("retry must clear HOLD state")
	}
	done := pumpApply(t, p, state, cmd, 200)
	if done == nil {
		t.Fatal("retried run must complete the page")
	}
	if calls != 2 {
		t.Fatalf("installer must have run twice, ran %d times", calls)
	}
	if p.holdState {
		t.Fatal("successful retry must not remain in HOLD")
	}
	// The first run's "failed" marker must not survive the retry. (Steps the
	// output stream skips over remain "pending" — a pre-existing display
	// quirk of the pattern tracker, not retry state.)
	for i, s := range p.steps {
		if s.status == "failed" {
			t.Fatalf("step %d still marked failed after successful retry", i)
		}
	}
	if p.steps[len(p.steps)-1].status != "done" {
		t.Fatal("final step must be done after successful retry")
	}
}

// --- HOLD jump keys must use the review page's section numbering ---

func TestApplyHoldJumpUsesReviewSectionMap(t *testing.T) {
	p := newApplyPage()
	state := &WizardState{}
	p.holdState = true

	// Section 8 is "forge" on the review page → 1-based page 11.
	_, cmd := p.update(keyPress('8', "8"), state)
	if cmd == nil {
		t.Fatal("'8' in HOLD must jump")
	}
	msg, ok := cmd().(pageJumpMsg)
	if !ok {
		t.Fatalf("expected pageJumpMsg, got %T", msg)
	}
	if msg.page != reviewSectionPages[8] {
		t.Fatalf("HOLD jump 8 → page %d, want %d (review's forge section)", msg.page, reviewSectionPages[8])
	}
}
