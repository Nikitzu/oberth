package installer

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// upstreamTokenSecretName is where the token lives. The chart mounts it when
// upstream.tokenSecret names it, and the server re-reads the file per push so
// rotating the Secret needs no restart.
// #nosec G101 -- the name of a Kubernetes Secret, not a credential.
const upstreamTokenSecretName = "oberth-upstream-token"

// configureUpstreamToken settles the personal access token an HTTPS upstream
// pushes with, and seeds the same token into the secret store so a pipeline
// can install the org's private packages.
//
// This is the credential that replaces a deploy key. The difference is whose
// authority it is: a deploy key is installed on the forge by an administrator
// and gives this server standing write access afterwards, while a token
// reaches exactly the repositories its owner already reaches and can be
// revoked by them alone. For a tool whose only write is "push the branch I
// just asked you to push", the second is the right one.
//
// A non-interactive install is not a reason to configure nothing. The token
// is almost always already exported, and an install that reads it in a
// terminal but ignores it in a script produces a deployment that differs by
// how it was started — which is the difference that made the operator the
// integration test.
//
// Declining is fine and says so: everything except that push works without it,
// and the dashboard reports the failure with the forge's own words if it is
// ever needed and missing.
func configureUpstreamToken(ctx context.Context, cfg Config, deps Deps, tw *tableWriter, store SecretStoreResult, baseURL string) error {
	w := deps.Output
	color := isColor(deps)

	// Prefer somewhere the token already is.
	//
	// Asking someone to paste a credential trains them to paste credentials,
	// and a pasted one lands in scrollback and in the terminal's own history
	// of the session. Most people already keep this in an environment variable
	// or a password manager, so look there first and say where it was found.
	token, source := existingUpstreamToken()

	if deps.IsTerminal == nil || !deps.IsTerminal() || deps.Input == nil {
		if token == "" {
			tw.AppendRow("Upstream token", "not configured", "⚠ manual", false)
			return nil
		}
		return applyUpstreamToken(ctx, cfg, deps, tw, store, baseURL, token, source)
	}

	_, _ = fmt.Fprintln(w, "\nA token lets the dashboard push a green branch and open its pull request,")
	_, _ = fmt.Fprintln(w, "and lets a pipeline install this org's private packages.")
	_, _ = fmt.Fprintln(w, "It is yours, not this server's: it reaches the repositories you reach, and")
	_, _ = fmt.Fprintln(w, "no administrator has to install anything on the forge.")

	if token != "" {
		_, _ = fmt.Fprintf(w, "Found one in %s. Enter to use it, or paste a different one.\n", source)
	} else {
		_, _ = fmt.Fprintf(w, "Set %s in your shell profile and re-run to avoid pasting it.\n",
			strings.Join(upstreamTokenEnvVars, " or "))
		_, _ = fmt.Fprintln(w, "Enter to skip; input is not echoed.")
	}

	typed, err := readSecret(ctx, deps, w, color, "Upstream token", "personal access token: ")
	if err != nil {
		return fmt.Errorf("read upstream token: %w", err)
	}
	if typed != "" {
		token, source = typed, "entered"
	}
	if token == "" {
		tw.AppendRow("Upstream token", "skipped; push & open PR will not push", "— skipped", false)
		return nil
	}
	return applyUpstreamToken(ctx, cfg, deps, tw, store, baseURL, token, source)
}

// applyUpstreamToken puts one token in both places it is needed: the Secret
// the server pushes with, and the upstream org's subtree of the secret store
// that a pipeline reads.
//
// Neither failure aborts the install. A deployment without the push credential
// still builds, and a deployment without the stored copy still pushes; the row
// says which one is missing rather than the install ending on it.
func applyUpstreamToken(ctx context.Context, cfg Config, deps Deps, tw *tableWriter, store SecretStoreResult, baseURL, token, source string) error {
	if err := storeUpstreamToken(ctx, cfg, deps, token); err != nil {
		tw.AppendRow("Upstream token", err.Error(), "✗ error", false)
		return nil
	}
	tw.AppendRow("Upstream token", "from "+source, "✓ stored", false)

	org := orgFromBaseURL(baseURL)
	switch {
	case org == "":
		// Nothing to scope the secret to; the hierarchical namespace is keyed
		// by org and there is no other place this could go safely.
	case !storeReachable(store):
		tw.AppendRow("Package token", "no secret store; private packages will not install", "⚠ manual", false)
	default:
		if err := seedUpstreamToken(ctx, store, org, token); err != nil {
			tw.AppendRow("Package token", terseInstallError(err), "✗ error", false)
		} else {
			tw.AppendRow("Package token", declaredUpstreamTokenPath(org), "✓ stored", false)
		}
	}
	return nil
}

// storeUpstreamToken writes the token into its own Secret.
//
// Its own, rather than a data key of the SSH Secret, so that revoking it is
// deleting one object and so the chart can mount it only when it exists.
func storeUpstreamToken(ctx context.Context, cfg Config, deps Deps, token string) error {
	if deps.KubeClient == nil {
		return fmt.Errorf("no cluster client")
	}
	ns := cfg.Namespace
	if ns == "" {
		ns = DefaultNamespace
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: upstreamTokenSecretName, Namespace: ns},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"token": []byte(token)},
	}
	secrets := deps.KubeClient.CoreV1().Secrets(ns)
	if _, err := secrets.Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		if _, err := secrets.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	return nil
}
