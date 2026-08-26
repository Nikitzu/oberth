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

// promptUpstreamToken asks for the personal access token an HTTPS upstream
// pushes with.
//
// This is the credential that replaces a deploy key. The difference is whose
// authority it is: a deploy key is installed on the forge by an administrator
// and gives this server standing write access afterwards, while a token
// reaches exactly the repositories its owner already reaches and can be
// revoked by them alone. For a tool whose only write is "push the branch I
// just asked you to push", the second is the right one.
//
// Declining is fine and says so: everything except that push works without it,
// and the dashboard reports the failure with the forge's own words if it is
// ever needed and missing.
func promptUpstreamToken(ctx context.Context, cfg Config, deps Deps, tw *tableWriter) error {
	if deps.IsTerminal == nil || !deps.IsTerminal() || deps.Input == nil {
		tw.AppendRow("Upstream token", "not configured", "⚠ manual", false)
		return nil
	}
	w := deps.Output
	color := isColor(deps)

	_, _ = fmt.Fprintln(w, "\nA token lets the dashboard push a green branch and open its pull request.")
	_, _ = fmt.Fprintln(w, "It is yours, not this server's: it reaches the repositories you reach, and")
	_, _ = fmt.Fprintln(w, "no administrator has to install anything on the forge. Enter to skip.")

	startPrompt(w, color, "Upstream token", "personal access token: ")
	token, err := readLine(ctx, deps.Input)
	if err != nil {
		return fmt.Errorf("read upstream token: %w", err)
	}
	erasePromptLines(w, color, 1)
	token = strings.TrimSpace(token)
	if token == "" {
		tw.AppendRow("Upstream token", "skipped; push & open PR will not push", "— skipped", false)
		return nil
	}
	if err := storeUpstreamToken(ctx, cfg, deps, token); err != nil {
		tw.AppendRow("Upstream token", err.Error(), "✗ error", false)
		return nil
	}
	tw.AppendRow("Upstream token", upstreamTokenSecretName, "✓ stored", false)
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
