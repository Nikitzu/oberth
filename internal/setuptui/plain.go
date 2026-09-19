package setuptui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"k8s.io/client-go/tools/clientcmd"

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

	// ask prompts until validate accepts the answer (S12: the sequential
	// path keeps the same field-level validation the TUI enforces). An
	// empty answer selects def. Three rejected answers abort the wizard —
	// the same register as the installer's own prompts.
	ask := func(label, def string, validate func(string) error) (string, error) {
		for attempt := 0; attempt < 3; attempt++ {
			if def != "" {
				w("  %s [%s]: ", label, def)
			} else {
				w("  %s: ", label)
			}
			line, err := input.ReadString('\n')
			if err != nil && line == "" {
				return "", fmt.Errorf("read %s: %w", label, err)
			}
			answer := strings.TrimSpace(line)
			if answer == "" {
				answer = def
			}
			if validate == nil {
				return answer, nil
			}
			if err := validate(answer); err != nil {
				wln("  ERROR: " + err.Error())
				continue
			}
			return answer, nil
		}
		return "", fmt.Errorf("%s: three invalid answers", label)
	}

	validNamespace := func(v string) error {
		if !dns1123LabelRegexp.MatchString(v) {
			return fmt.Errorf("%q must be a valid DNS-1123 label", v)
		}
		return nil
	}

	// Page 1: Welcome.
	wln("")
	wln("  OBERTH SETUP")
	wln("  Machine-speed code. Human-grade control.")
	wln("")
	wln("  step 1/13 — mission briefing")
	wln("")

	// Page 2: Cluster. The installer targets the CURRENT kubeconfig
	// context — there is no --context flag — so naming a different context
	// here must stop the wizard rather than silently install into whatever
	// context happens to be current (wrong-cluster hazard).
	wln("  step 2/13 — launch site")
	currentContext := ""
	if raw, err := clientcmd.NewDefaultClientConfigLoadingRules().Load(); err == nil {
		currentContext = raw.CurrentContext
	}
	ctxAnswer, err := ask("Kubeconfig context (empty for current)", currentContext, func(v string) error {
		if v != "" && currentContext != "" && v != currentContext {
			return fmt.Errorf("installing into a non-current context is not supported yet — "+
				"run `kubectl config use-context %s` first, then re-run setup", v)
		}
		return nil
	})
	if err != nil {
		return err
	}
	state.SelectedContext = ctxAnswer

	// Page 3: Mode.
	wln("  step 3/13 — flight plan")
	if _, err := ask("Mode", "dev", func(v string) error {
		if v == "production" {
			return fmt.Errorf("production profile is not implemented yet — choose dev")
		}
		if v != "dev" {
			return fmt.Errorf("mode must be dev")
		}
		return nil
	}); err != nil {
		return err
	}
	state.Config.Dev = true
	state.Config.Production = false

	// Page 4: Namespaces — DNS-1123 validated and pairwise distinct, the
	// same rules the TUI page enforces.
	wln("  step 4/13 — flight plan")
	ns, err := ask("Oberth namespace", "oberth", validNamespace)
	if err != nil {
		return err
	}
	state.Config.Namespace = ns
	argoNS, err := ask("Pipeline namespace", "oberth-pipelines", func(v string) error {
		if err := validNamespace(v); err != nil {
			return err
		}
		if v == ns {
			return fmt.Errorf("pipeline namespace must differ from the oberth namespace")
		}
		return nil
	})
	if err != nil {
		return err
	}
	state.Config.ArgoNamespace = argoNS
	baoNS, err := ask("OpenBao namespace", "openbao", func(v string) error {
		if err := validNamespace(v); err != nil {
			return err
		}
		if v == ns || v == argoNS {
			return fmt.Errorf("openbao namespace must differ from the other namespaces")
		}
		return nil
	})
	if err != nil {
		return err
	}
	state.Config.OpenBaoNamespace = baoNS

	// Page 5: Execution.
	wln("  step 5/13 — flight plan")
	np, err := ask("Network policy (auto/strict/off)", "auto", func(v string) error {
		if _, ok := canonicalNetworkPolicy(v); !ok {
			return fmt.Errorf("network policy must be auto, strict, or off")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if canonical, ok := canonicalNetworkPolicy(np); ok {
		state.Config.NetworkPolicy = canonical
	}
	anchor, err := ask("External anchoring (on/off)", "off", func(v string) error {
		if v != "on" && v != "off" {
			return fmt.Errorf("answer on or off")
		}
		return nil
	})
	if err != nil {
		return err
	}
	state.Config.InstallRekor = anchor == "on"

	// Page 6: Secret store.
	wln("  step 6/13 — propellant")
	wln("  [1] Install OpenBao — dev")
	wln("  [2] Install OpenBao — production")
	wln("  [3] Connect existing")
	choice, err := ask("Choice", "2", func(v string) error {
		if v != "1" && v != "2" && v != "3" {
			return fmt.Errorf("answer 1, 2, or 3")
		}
		return nil
	})
	if err != nil {
		return err
	}
	switch choice {
	case "1":
		state.Config.InstallSecretStoreDev = true
		state.StoreMode = "install-dev"
	case "3":
		state.StoreMode = "connect"
		// Page 7: Store connect — same S7 validator as the TUI field.
		wln("  step 7/13 — propellant")
		addr, err := ask("Vault address (https://…)", "", func(v string) error {
			return validateStoreAddress(v)
		})
		if err != nil {
			return err
		}
		state.Config.ArgoVaultAddress = addr
		state.StoreAddress = addr
	default:
		state.Config.InstallSecretStore = true
		state.StoreMode = "install-prod"
	}

	// Page 8: TLS.
	wln("  step 8/13 — heat shield")
	tlsMode, err := ask("TLS mode (self-signed/byo)", "self-signed", func(v string) error {
		if v != "self-signed" && v != "byo" {
			return fmt.Errorf("answer self-signed or byo")
		}
		return nil
	})
	if err != nil {
		return err
	}
	state.TLSMode = tlsMode

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
	identity, err := ask("Identity", user+"@"+host, nil)
	if err != nil {
		return err
	}
	state.UplinkIdentity = identity

	// Page 10: Git (informational).
	wln("  step 10/13 — comms check")
	wln("  Push over SSH to NodePort 30022 — smart protocol only.")
	wln("  clone: ssh://git@localhost:30022/<repo>.git")

	// Page 11: Forge.
	wln("  step 11/13 — ground station")
	forge, err := ask("Forge (codeberg/github/forgejo/gitlab)", "codeberg", func(v string) error {
		switch v {
		case "codeberg", "github", "forgejo", "gitlab":
			return nil
		}
		return fmt.Errorf("forge must be codeberg, github, forgejo, or gitlab")
	})
	if err != nil {
		return err
	}
	state.ForgeType = forge
	org, err := ask("Owner/org", "", func(v string) error {
		if v == "" {
			return fmt.Errorf("org is required")
		}
		return nil
	})
	if err != nil {
		return err
	}
	state.ForgeOrg = org

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

	confirm, err := ask("Apply? (yes/no)", "no", func(v string) error {
		if v != "yes" && v != "y" && v != "no" && v != "n" {
			return fmt.Errorf("answer yes or no")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if confirm != "yes" && confirm != "y" {
		return installer.ErrInterrupted
	}

	// Page 13: Apply.
	wln("  step 13/13 — ignition")
	wln("  Applying...")

	state.Config.BinaryVersion = opts.BinaryVersion
	if state.Config.BinaryVersion == "" {
		state.Config.BinaryVersion = "dev"
	}
	state.Config.SecretStoreUndecided = !state.Config.InstallSecretStore && !state.Config.InstallSecretStoreDev
	return installer.Execute(ctx, state.Config, installer.InstallDeps{
		Output: output,
		Input:  os.Stdin,
	})
}
