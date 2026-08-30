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
	if strings.Contains(without, "kubedock") || strings.Contains(without, "inNamespaceAllPorts") {
		t.Fatalf("an ordinary install carries the preset anyway:\n%s", without)
	}
}

// The tri-state --network-policy uses. An operator who named --testcontainers,
// either way, gets the value pinned through --reuse-values, which never merges
// a newer chart's defaults. One who said nothing gets no --set at all, so a
// kubedock.enabled they put in their own values file survives the re-run.
func TestKubedockIsPinnedOnlyWhenTheOperatorSaidSomething(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"explicitly on", Config{Testcontainers: true, TestcontainersExplicit: true}, "--set kubedock.enabled=true"},
		{"explicitly off", Config{TestcontainersExplicit: true}, "--set kubedock.enabled=false"},
	} {
		if args := strings.Join(OberthHelmArgs(test.cfg, OpenBaoResult{}, RekorResult{}), " "); !strings.Contains(args, test.want) {
			t.Errorf("%s: helm args missing %q:\n%s", test.name, test.want, args)
		}
	}
	silent := strings.Join(OberthHelmArgs(Config{}, OpenBaoResult{}, RekorResult{}), " ")
	if strings.Contains(silent, "kubedock") {
		t.Errorf("an install that never mentioned kubedock pins it anyway:\n%s", silent)
	}
}
