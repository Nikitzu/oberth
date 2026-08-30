package installer

import (
	"strings"
	"testing"
)

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
	if strings.Contains(without, "inNamespaceAllPorts") {
		t.Fatalf("an ordinary install carries the preset anyway:\n%s", without)
	}
}

// A re-run without --testcontainers still has to say kubedock.enabled=false
// out loud. helm upgrade --reuse-values reuses the previous release's values
// and never merges the new chart's defaults, so leaving the value unset left
// a release created before the key existed with no .Values.kubedock at all,
// and every upgrade of one failed rendering the template.
func TestOrdinaryInstallPinsKubedockOffForReuseValues(t *testing.T) {
	t.Parallel()
	args := strings.Join(OberthHelmArgs(Config{}, OpenBaoResult{}, RekorResult{}), " ")
	if !strings.Contains(args, "--set kubedock.enabled=false") {
		t.Fatalf("helm args do not pin kubedock off:\n%s", args)
	}
}
