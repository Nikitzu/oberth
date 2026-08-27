package installer

import (
	"bytes"
	"context"
	"io"
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

// The URL column is what a re-run needs to find the org whose subtree of the
// secret store the token belongs in.
func TestTheUpstreamListYieldsItsBaseURLs(t *testing.T) {
	t.Parallel()
	out := []byte("Defaulted container \"oberth\" out of: oberth, runner\n" +
		"NAME       KIND    URL                              KEY NAME   KEY FINGERPRINT\n" +
		"codeberg   ssh     ssh://git@codeberg.org/acme      -          SHA256:y\n" +
		"github     https   https://github.com/other-org     -          -\n")
	got := upstreamListBaseURLs(out)
	want := []string{"ssh://git@codeberg.org/acme", "https://github.com/other-org"}
	assertSliceEqual(t, got, want)
}

// Seeding happened only while registering an upstream, so a re-run against a
// deployment that already had one left a rotated token unapplied and a
// reinstalled store empty.
func TestARerunReseedsTheUpstreamTokenIntoEveryOrg(t *testing.T) {
	const token = "ghp_rerunvalue"
	t.Setenv("GITHUB_TOKEN", token)

	runner := &fakeBaoRunner{t: t, responses: map[string]fakeBaoResponse{
		"write oberth/data/upstream/acme/github-token -": {},
	}}
	deps := secretStoreTestDeps(runner, io.Discard)
	store := SecretStoreResult{}
	store.client = newOpenBaoExec(deps, DefaultOpenBaoNamespace, expectedOpenBaoPodName)
	store.rootToken = "s.roottoken"

	tw := newTableWriter(io.Discard, false)
	reseedUpstreamToken(context.Background(), Config{}, deps, tw,
		store, []string{"ssh://git@github.com/acme", "ssh://git@github.com/acme"})

	secret, err := deps.KubeClient.CoreV1().Secrets(DefaultNamespace).
		Get(context.Background(), upstreamTokenSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the push Secret was not written: %v", err)
	}
	if string(secret.Data["token"]) != token {
		t.Fatalf("push Secret holds %q", secret.Data["token"])
	}
	writes := 0
	for _, call := range runner.calls {
		if strings.Contains(call.command, "upstream/acme/github-token") {
			writes++
			if !strings.Contains(call.stdin, token) {
				t.Fatalf("the token did not reach the store: %q", call.stdin)
			}
		}
	}
	// The same org twice is one write, not two.
	if writes != 1 {
		t.Fatalf("store writes = %d, want exactly one per distinct org", writes)
	}
}

// Without a token in the environment a re-run changes nothing and says which
// variable to set. It must never start prompting on an unattended re-run.
func TestARerunWithoutATokenLeavesTheDeploymentAlone(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")

	runner := &fakeBaoRunner{t: t, responses: map[string]fakeBaoResponse{}}
	deps := secretStoreTestDeps(runner, io.Discard)
	var table bytes.Buffer
	tw := newTableWriter(&table, false)
	tw.WriteHeader()

	reseedUpstreamToken(context.Background(), Config{}, deps, tw, SecretStoreResult{}, []string{"ssh://git@github.com/acme"})
	tw.WriteFooter()

	if _, err := deps.KubeClient.CoreV1().Secrets(DefaultNamespace).
		Get(context.Background(), upstreamTokenSecretName, metav1.GetOptions{}); err == nil {
		t.Fatal("a re-run with no token rewrote the push Secret anyway")
	}
	if !strings.Contains(table.String(), "GITHUB_TOKEN") {
		t.Fatalf("the row does not say how to re-seed:\n%s", table.String())
	}
}

// The shim and the egress rule only work together. One --set without the other
// produces a deployment where Testcontainers hangs on its wait strategy, which
// is exactly what the two-manual-steps arrangement kept producing.
func TestTestcontainersDeploysTheShimAndOpensTheEgressTogether(t *testing.T) {
	t.Parallel()
	args := strings.Join(OberthHelmArgs(Config{Testcontainers: true}, OpenBaoResult{}, RekorResult{}), " ")
	for _, want := range []string{"kubedock.enabled=true", "networkPolicy.inNamespaceAllPorts=true"} {
		if !strings.Contains(args, want) {
			t.Fatalf("helm args missing %q:\n%s", want, args)
		}
	}

	without := strings.Join(OberthHelmArgs(Config{}, OpenBaoResult{}, RekorResult{}), " ")
	if strings.Contains(without, "kubedock") || strings.Contains(without, "inNamespaceAllPorts") {
		t.Fatalf("an ordinary install carries the preset anyway:\n%s", without)
	}
}
