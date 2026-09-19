package setuptui

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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

// --- S2: token ceremony lifecycle ---

func TestCeremonySetTokenZerosSource(t *testing.T) {
	p := newCeremonyPage()
	src := []byte("bearer-token-value")
	p.setToken(src)
	if !bytes.Equal(p.token, []byte("bearer-token-value")) {
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
	p.setToken([]byte("tok"))

	// Enter before reveal/copy must not complete and must not zero.
	_, cmd := p.update(enterKey(), nil)
	if cmd != nil {
		t.Fatal("enter before reveal must not complete the ceremony")
	}
	if p.acknowledged || len(p.token) == 0 {
		t.Fatal("token must survive an unacknowledged enter")
	}

	// Reveal, then enter: acknowledged, token zeroed, page completes.
	_, _ = p.update(keyPress('r', "r"), nil)
	if !p.revealed {
		t.Fatal("r must reveal")
	}
	held := p.token
	_, cmd = p.update(enterKey(), nil)
	if cmd == nil {
		t.Fatal("acknowledged ceremony must emit a completion cmd")
	}
	if msg := cmd(); msg != (pageCompleteMsg{}) {
		t.Fatalf("expected pageCompleteMsg, got %T", msg)
	}
	if p.token != nil {
		t.Fatal("token must be nil after acknowledgment")
	}
	for i, b := range held {
		if b != 0 {
			t.Fatalf("token bytes not zeroed at %d", i)
		}
	}
}

func TestCeremonyReplacementZerosPreviousToken(t *testing.T) {
	p := newCeremonyPage()
	p.setToken([]byte("first-token"))
	first := p.token
	p.setToken([]byte("second-token"))
	for i, b := range first {
		if b != 0 {
			t.Fatalf("previous token not zeroed at %d", i)
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
	if !bytes.Equal(p.ceremony.token, []byte("mint-token-9f8e")) {
		t.Fatal("ceremony copy missing or wrong")
	}

	p.wipeSecrets()
	if p.ceremony.token != nil {
		t.Fatal("wipeSecrets must zero the ceremony token")
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
	if ap.ceremony.token != nil {
		t.Fatal("confirmed abort must zero the ceremony token")
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
