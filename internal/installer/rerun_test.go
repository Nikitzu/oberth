package installer

import (
	"bytes"
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func oberthDeploymentWithImage(namespace, image string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "oberth", Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "oberth", Image: image}},
				},
			},
		},
	}
}

// A re-run without --image used to deploy this binary's default over the image
// the first run was told to use. On a kind machine that image often exists
// only in the node, so the replacement is not even recoverable by re-running.
func TestARerunKeepsAnImageTheOperatorDeployed(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	deps := Deps{
		Output:     &out,
		KubeClient: fake.NewClientset(oberthDeploymentWithImage(DefaultNamespace, "oberth:local-dev")),
	}
	cfg := Config{ImageRef: canonicalGARPrefix + "oberth@sha256:" + strings.Repeat("a", 64)}

	keepCustomDeployedImage(context.Background(), &cfg, deps, DefaultNamespace)

	if cfg.ImageRef != "oberth:local-dev" {
		t.Fatalf("image = %q, want the deployed one kept", cfg.ImageRef)
	}
	if !strings.Contains(out.String(), "oberth:local-dev") || !strings.Contains(out.String(), "--image") {
		t.Fatalf("the substitution was silent:\n%s", out.String())
	}
}

// Naming --image is the operator asking for the change, so nothing is kept.
func TestAnExplicitImageFlagOverridesWhatIsDeployed(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	deps := Deps{
		Output:     &out,
		KubeClient: fake.NewClientset(oberthDeploymentWithImage(DefaultNamespace, "oberth:local-dev")),
	}
	cfg := Config{ImageRef: "oberth:next", ImageRefExplicit: true}

	keepCustomDeployedImage(context.Background(), &cfg, deps, DefaultNamespace)

	if cfg.ImageRef != "oberth:next" {
		t.Fatalf("image = %q, want the flag honoured", cfg.ImageRef)
	}
	if out.Len() != 0 {
		t.Fatalf("an explicit flag produced a warning:\n%s", out.String())
	}
}

// Moving one release's published image to the next is what a re-run with a
// newer binary is for, so a published image is never preserved.
func TestAPublishedImageIsNotTreatedAsCustom(t *testing.T) {
	t.Parallel()
	previous := canonicalGARPrefix + "oberth@sha256:" + strings.Repeat("b", 64)
	next := canonicalGARPrefix + "oberth@sha256:" + strings.Repeat("c", 64)
	var out bytes.Buffer
	deps := Deps{
		Output:     &out,
		KubeClient: fake.NewClientset(oberthDeploymentWithImage(DefaultNamespace, previous)),
	}
	cfg := Config{ImageRef: next}

	keepCustomDeployedImage(context.Background(), &cfg, deps, DefaultNamespace)

	if cfg.ImageRef != next {
		t.Fatalf("image = %q, want the upgrade to proceed", cfg.ImageRef)
	}
}

// A fork publishes its releases to its own registry, so its previous release
// is not under the canonical GAR prefix. Treating that as a hand-deployed
// image kept every fork install pinned to the digest it already ran: the
// install reported the new version while the deployment stayed on the old
// image. Sharing the repository with this binary's own default image is what
// makes a deployed image a release rather than the operator's own.
func TestAForksOwnPreviousReleaseIsUpgradedNotKept(t *testing.T) {
	t.Parallel()
	previous := "ghcr.io/fork-owner/oberth@sha256:" + strings.Repeat("d", 64)
	next := "ghcr.io/fork-owner/oberth@sha256:" + strings.Repeat("e", 64)
	var out bytes.Buffer
	deps := Deps{
		Output:     &out,
		KubeClient: fake.NewClientset(oberthDeploymentWithImage(DefaultNamespace, previous)),
	}
	cfg := Config{ImageRef: next}

	keepCustomDeployedImage(context.Background(), &cfg, deps, DefaultNamespace)

	if cfg.ImageRef != next {
		t.Fatalf("image = %q, want the upgrade to proceed", cfg.ImageRef)
	}
	if out.Len() != 0 {
		t.Fatalf("a release-to-release move warned about a custom image:\n%s", out.String())
	}
}

// An image from a registry that is neither this binary's own nor the canonical
// one is still the operator's: a digest is no evidence that a release put it
// there.
func TestADigestFromAnotherRegistryIsStillKept(t *testing.T) {
	t.Parallel()
	deployed := "oberth-registry:5000/oberth@sha256:" + strings.Repeat("f", 64)
	var out bytes.Buffer
	deps := Deps{
		Output:     &out,
		KubeClient: fake.NewClientset(oberthDeploymentWithImage(DefaultNamespace, deployed)),
	}
	cfg := Config{ImageRef: "ghcr.io/fork-owner/oberth@sha256:" + strings.Repeat("e", 64)}

	keepCustomDeployedImage(context.Background(), &cfg, deps, DefaultNamespace)

	if cfg.ImageRef != deployed {
		t.Fatalf("image = %q, want the deployed one kept", cfg.ImageRef)
	}
	if !strings.Contains(out.String(), deployed) || !strings.Contains(out.String(), "--image") {
		t.Fatalf("the substitution was silent:\n%s", out.String())
	}
}

// A fresh install has nothing to preserve and must not say anything.
func TestAFreshInstallHasNoImageToKeep(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	deps := Deps{Output: &out, KubeClient: fake.NewClientset()}
	cfg := Config{ImageRef: "oberth:next"}

	keepCustomDeployedImage(context.Background(), &cfg, deps, DefaultNamespace)

	if cfg.ImageRef != "oberth:next" || out.Len() != 0 {
		t.Fatalf("image = %q, output = %q", cfg.ImageRef, out.String())
	}
}
