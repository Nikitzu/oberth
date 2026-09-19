package setuptui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/oberthci/oberth/internal/installer"
)

// runPlain implements the accessible/plain sequential fallback (S12).
// Same pages, same order, same validation, same security properties.
// No TUI, no color, no cursor addressing.
func runPlain(ctx context.Context, opts Options, output io.Writer) error {
	w := func(format string, a ...any) { _, _ = fmt.Fprintf(output, format, a...) }
	wln := func(a ...any) { _, _ = fmt.Fprintln(output, a...) }

	input := bufio.NewReader(os.Stdin)
	state := &WizardState{
		Config: installer.Config{
			Dev:       true,
			Namespace: "oberth",
		},
		StoreMode: "install-prod",
		ForgeType: "codeberg",
		ForgeAuth: "deploy-key",
		TLSMode:   "self-signed",
	}

	// Page 1: Welcome.
	wln("")
	wln("  OBERTH SETUP")
	wln("  Machine-speed code. Human-grade control.")
	wln("")
	wln("  step 1/13 — mission briefing")
	wln("")

	// Page 2: Cluster.
	wln("  step 2/13 — launch site")
	w("  Kubeconfig context (empty for current): ")
	ctxLine, _ := input.ReadString('\n')
	state.SelectedContext = strings.TrimSpace(ctxLine)

	// Page 3: Mode.
	wln("  step 3/13 — flight plan")
	w("  Mode [dev]: ")
	modeLine, _ := input.ReadString('\n')
	modeTrimmed := strings.TrimSpace(modeLine)
	if modeTrimmed == "production" {
		state.Config.Dev = false
		state.Config.Production = true
	}

	// Page 4: Namespaces.
	wln("  step 4/13 — flight plan")
	w("  Oberth namespace [oberth]: ")
	nsLine, _ := input.ReadString('\n')
	if ns := strings.TrimSpace(nsLine); ns != "" {
		state.Config.Namespace = ns
	}
	w("  Pipeline namespace [oberth-pipelines]: ")
	argoLine, _ := input.ReadString('\n')
	if ns := strings.TrimSpace(argoLine); ns != "" {
		state.Config.ArgoNamespace = ns
	} else {
		state.Config.ArgoNamespace = "oberth-pipelines"
	}
	w("  OpenBao namespace [openbao]: ")
	baoLine, _ := input.ReadString('\n')
	if ns := strings.TrimSpace(baoLine); ns != "" {
		state.Config.OpenBaoNamespace = ns
	} else {
		state.Config.OpenBaoNamespace = "openbao"
	}

	// Page 5: Execution.
	wln("  step 5/13 — flight plan")
	w("  Network policy [auto]: ")
	npLine, _ := input.ReadString('\n')
	if np := strings.TrimSpace(npLine); np != "" {
		state.Config.NetworkPolicy = np
	}
	w("  External anchoring [off]: ")
	anchorLine, _ := input.ReadString('\n')
	if strings.TrimSpace(anchorLine) == "on" {
		state.Config.InstallRekor = true
	}

	// Page 6: Secret store.
	wln("  step 6/13 — propellant")
	wln("  [1] Install OpenBao — dev")
	wln("  [2] Install OpenBao — production")
	wln("  [3] Connect existing")
	w("  Choice [2]: ")
	storeLine, _ := input.ReadString('\n')
	switch strings.TrimSpace(storeLine) {
	case "1":
		state.Config.InstallSecretStoreDev = true
		state.StoreMode = "install-dev"
	case "3":
		state.StoreMode = "connect"
		// Page 7: Store connect.
		wln("  step 7/13 — propellant")
		w("  Vault address (https://): ")
		addrLine, _ := input.ReadString('\n')
		addr := strings.TrimSpace(addrLine)
		if strings.HasPrefix(addr, "http://") {
			wln("  ERROR: https required — tls 1.3 is a product invariant")
			return fmt.Errorf("http:// rejected")
		}
		state.Config.ArgoVaultAddress = addr
		state.StoreAddress = addr
	default:
		state.Config.InstallSecretStore = true
		state.StoreMode = "install-prod"
	}

	// Page 8: TLS.
	wln("  step 8/13 — heat shield")
	w("  TLS mode [self-signed]: ")
	tlsLine, _ := input.ReadString('\n')
	if strings.TrimSpace(tlsLine) == "byo" {
		state.TLSMode = "byo"
	}

	// Page 9: Uplink.
	wln("  step 9/13 — crew manifest")
	user := os.Getenv("USER")
	if user == "" {
		user = "admin"
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "localhost"
	}
	defaultIdentity := user + "@" + host
	w("  Identity [%s]: ", defaultIdentity)
	idLine, _ := input.ReadString('\n')
	if id := strings.TrimSpace(idLine); id != "" {
		state.UplinkIdentity = id
	} else {
		state.UplinkIdentity = defaultIdentity
	}

	// Page 10: Git (informational).
	wln("  step 10/13 — comms check")
	wln("  Push over SSH to NodePort 30022 — smart protocol only.")
	wln("  clone: ssh://git@localhost:30022/<repo>.git")

	// Page 11: Forge.
	wln("  step 11/13 — ground station")
	w("  Forge [codeberg]: ")
	forgeLine, _ := input.ReadString('\n')
	if f := strings.TrimSpace(forgeLine); f != "" {
		state.ForgeType = f
	}
	w("  Owner/org: ")
	orgLine, _ := input.ReadString('\n')
	state.ForgeOrg = strings.TrimSpace(orgLine)

	// Page 12: Review.
	wln("  step 12/13 — go/no-go")
	wln("  Review the plan:")
	wln("")
	w("  cluster:    %s\n", state.SelectedContext)
	w("  mode:       %s\n", formatModeSummary(state))
	w("  namespaces: %s\n", formatNamespacesSummary(state))
	w("  store:      %s\n", formatStoreSummary(state))
	w("  tls:        %s\n", state.TLSMode)
	w("  uplink:     %s\n", state.UplinkIdentity)
	w("  forge:      %s %s\n", state.ForgeType, state.ForgeOrg)
	wln("")

	if opts.DryMode {
		wln("  --dry-mode: equivalent command:")
		wln("")
		wln("  " + BuildCommandLine(state))
		return nil
	}

	w("  Apply? [yes]: ")
	applyLine, _ := input.ReadString('\n')
	if ans := strings.TrimSpace(applyLine); ans != "" && ans != "yes" && ans != "y" {
		return installer.ErrInterrupted
	}

	// Page 13: Apply.
	wln("  step 13/13 — ignition")
	wln("  Applying...")

	state.Config.BinaryVersion = "dev"
	state.Config.SecretStoreUndecided = !state.Config.InstallSecretStore && !state.Config.InstallSecretStoreDev
	return installer.Execute(ctx, state.Config, installer.InstallDeps{
		Output: output,
		Input:  os.Stdin,
	})
}
