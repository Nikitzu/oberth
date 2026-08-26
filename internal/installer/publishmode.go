package installer

import (
	"context"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// deploymentPublishes reports whether this deployment ever pushes to the forge.
//
// It reads the live Deployment rather than the chart or the flags, because the
// chart, a values file and the installer's own --set can each set it and only
// the running container's argv settles which one won.
//
// The answer decides whether a deploy key is worth asking for at all. Under the
// advisory gate a green run publishes nothing: the developer pushes their own
// branch with their own credentials and the dashboard offers a compare link
// that opens in their browser. There is no connection from this server to the
// forge, so a deploy key would be a credential arranged for a connection that
// is never opened.
//
// Unreadable state answers true. Asking for a key that turns out to be
// unnecessary costs a prompt; skipping one that was needed costs a push that
// fails later, with nothing on screen to explain it.
func deploymentPublishes(ctx context.Context, cfg Config, deps Deps) bool {
	if deps.KubeClient == nil {
		return true
	}
	ns := cfg.Namespace
	if ns == "" {
		ns = DefaultNamespace
	}
	deployment, err := deps.KubeClient.AppsV1().Deployments(ns).Get(ctx, "oberth", metav1.GetOptions{})
	if err != nil {
		return true
	}
	for _, container := range deployment.Spec.Template.Spec.Containers {
		for _, arg := range container.Args {
			if strings.HasPrefix(arg, "--publish-on-green=") {
				return !strings.EqualFold(strings.TrimPrefix(arg, "--publish-on-green="), "false")
			}
		}
	}
	return true
}
