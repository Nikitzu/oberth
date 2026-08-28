package installer

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// --- ConfigMap fallback path (run == nil) ---

func TestProducePerRepoIdentitiesFromQualifiedGrants(t *testing.T) {
	t.Parallel()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: secretAccessConfigMapName, Namespace: "oberth"},
		Data: map[string]string{
			secretAccessConfigMapKey: `
- repo: codeberg/oberthci/oberth
  step: "*"
  secret: oberth/data/release/cosign-secret
- repo: codeberg/oberthci/oberth
  step: "*"
  secret: oberth/data/release/r2-token
- repo: github/skipops/terraform
  step: "*"
  secret: oberth/upstream/skipops/terraform/plan/gcp-sa
`,
		},
	}
	kube := fake.NewSimpleClientset(cm)

	result, err := ProducePerRepoIdentities(context.Background(), kube, nil, "", "oberth")
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 per-repo identities, got %d: %+v", len(result), result)
	}

	// Sort for deterministic assertion.
	sort.Slice(result, func(i, j int) bool {
		return result[i].Repo < result[j].Repo
	})

	// First: oberth (codeberg/oberthci)
	if result[0].Upstream != "codeberg" || result[0].Org != "oberthci" || result[0].Repo != "oberth" {
		t.Fatalf("unexpected identity[0]: %+v", result[0])
	}
	if len(result[0].Grants) != 2 {
		t.Fatalf("expected 2 grants for oberth, got %d", len(result[0].Grants))
	}

	// Second: terraform (github/skipops)
	if result[1].Upstream != "github" || result[1].Org != "skipops" || result[1].Repo != "terraform" {
		t.Fatalf("unexpected identity[1]: %+v", result[1])
	}
}

func TestProducePerRepoIdentitiesSkipsBareRepoNames(t *testing.T) {
	t.Parallel()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: secretAccessConfigMapName, Namespace: "oberth"},
		Data: map[string]string{
			secretAccessConfigMapKey: `
- repo: oberth
  step: "*"
  secret: oberth/data/release/cosign-secret
- repo: codeberg/oberthci/oberth
  step: "*"
  secret: oberth/data/release/r2-token
`,
		},
	}
	kube := fake.NewSimpleClientset(cm)

	result, err := ProducePerRepoIdentities(context.Background(), kube, nil, "", "oberth")
	if err != nil {
		t.Fatal(err)
	}

	// Only the qualified entry should produce an identity.
	if len(result) != 1 {
		t.Fatalf("expected 1 identity (bare name skipped), got %d: %+v", len(result), result)
	}
	if result[0].Repo != "oberth" || result[0].Upstream != "codeberg" {
		t.Fatalf("unexpected identity: %+v", result[0])
	}
}

func TestProducePerRepoIdentitiesReturnsNilWhenConfigMapAbsent(t *testing.T) {
	t.Parallel()
	kube := fake.NewSimpleClientset()

	result, err := ProducePerRepoIdentities(context.Background(), kube, nil, "", "oberth")
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatalf("expected nil, got %+v", result)
	}
}

func TestProducePerRepoIdentitiesReturnsNilOnEmptyData(t *testing.T) {
	t.Parallel()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: secretAccessConfigMapName, Namespace: "oberth"},
		Data:       map[string]string{secretAccessConfigMapKey: ""},
	}
	kube := fake.NewSimpleClientset(cm)

	result, err := ProducePerRepoIdentities(context.Background(), kube, nil, "", "oberth")
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatalf("expected nil, got %+v", result)
	}
}

// --- Exec path (access list parsing) ---

func TestProduceFromAccessListParsesQualifiedOutput(t *testing.T) {
	t.Parallel()
	// Simulate the tabwriter-formatted output of `oberth access list`.
	accessListOutput := strings.Join([]string{
		"REPO                                        STEP   SECRET                                             APPROVED BY                            APPROVED AT        STATUS",
		"codeberg/cloudtaser/cloudtaser-beacon       *      oberth/data/release/cosign-secret                  admin@localhost+configmap@rv=225951    2026-08-21 16:40   active",
		"codeberg/cloudtaser/cloudtaser-beacon       *      oberth/data/release/gar-sa-key                     admin@localhost+configmap@rv=225949    2026-08-21 16:40   active",
		"codeberg/cloudtaser/terraform               *      oberth/upstream/cloudtaser/terraform/credentials   configmap@rv=699072                    2026-08-23 22:23   active",
		"github/oberthci/oberth                      *      oberth/data/release/cosign-secret                  configmap@rv=18991                     2026-08-18 08:54   active",
		"github/oberthci/oberth                      *      oberth/data/release/r2-upload-token                admin@localhost+configmap@rv=18990     2026-08-18 08:54   active",
	}, "\n")

	// Mock the CommandRunner to return the simulated output.
	mockRun := func(_ context.Context, _ []byte, _ string, _ ...string) ([]byte, error) {
		return []byte(accessListOutput), nil
	}

	result, err := ProducePerRepoIdentities(context.Background(), nil, mockRun, "", "oberth")
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != 3 {
		t.Fatalf("expected 3 per-repo identities, got %d: %+v", len(result), result)
	}

	sort.Slice(result, func(i, j int) bool {
		key := func(id PerRepoIdentity) string { return id.Upstream + "/" + id.Org + "/" + id.Repo }
		return key(result[i]) < key(result[j])
	})

	// cloudtaser-beacon: 2 grants
	if result[0].Upstream != "codeberg" || result[0].Org != "cloudtaser" || result[0].Repo != "cloudtaser-beacon" {
		t.Fatalf("unexpected identity[0]: %+v", result[0])
	}
	if len(result[0].Grants) != 2 {
		t.Fatalf("expected 2 grants for cloudtaser-beacon, got %d: %v", len(result[0].Grants), result[0].Grants)
	}

	// terraform: 1 upstream grant
	if result[1].Upstream != "codeberg" || result[1].Org != "cloudtaser" || result[1].Repo != "terraform" {
		t.Fatalf("unexpected identity[1]: %+v", result[1])
	}
	if len(result[1].Grants) != 1 || result[1].Grants[0] != "oberth/upstream/cloudtaser/terraform/credentials" {
		t.Fatalf("unexpected grants for terraform: %v", result[1].Grants)
	}

	// oberth: 2 grants
	if result[2].Upstream != "github" || result[2].Org != "oberthci" || result[2].Repo != "oberth" {
		t.Fatalf("unexpected identity[2]: %+v", result[2])
	}
	if len(result[2].Grants) != 2 {
		t.Fatalf("expected 2 grants for oberth, got %d", len(result[2].Grants))
	}
}

func TestProduceFromAccessListFallsBackToConfigMapOnExecFailure(t *testing.T) {
	t.Parallel()
	// Exec fails — simulate a pod that is not running.
	failRun := func(_ context.Context, _ []byte, _ string, _ ...string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	}

	// ConfigMap with a qualified entry should still be found via fallback.
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: secretAccessConfigMapName, Namespace: "oberth"},
		Data: map[string]string{
			secretAccessConfigMapKey: `
- repo: codeberg/oberthci/oberth
  step: "*"
  secret: oberth/data/release/cosign-secret
`,
		},
	}
	kube := fake.NewSimpleClientset(cm)

	result, err := ProducePerRepoIdentities(context.Background(), kube, failRun, "", "oberth")
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 identity from CM fallback, got %d: %+v", len(result), result)
	}
	if result[0].Repo != "oberth" {
		t.Fatalf("unexpected identity: %+v", result[0])
	}
}

func TestParseAccessListOutputSkipsBareRows(t *testing.T) {
	t.Parallel()
	output := strings.Join([]string{
		"REPO          STEP   SECRET                              APPROVED BY   APPROVED AT        STATUS",
		"terraform     *      oberth/data/release/cosign-secret   admin         2026-08-18 08:54   active",
	}, "\n")

	result, err := parseAccessListOutput([]byte(output))
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 0 {
		t.Fatalf("expected 0 identities (bare name), got %d: %+v", len(result), result)
	}
}

func TestParseAccessListOutputEmptyInput(t *testing.T) {
	t.Parallel()
	result, err := parseAccessListOutput([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 0 {
		t.Fatalf("expected 0, got %d", len(result))
	}
}

// --- Integration: producer output through ConfigurePerRepoIdentities ---
// This proves that the producer's output format (including upstream-prefix
// grants) is accepted by the downstream consumer without error, closing
// the test gap identified in the review.

func TestProducerOutputAcceptedByConfigurePerRepoIdentities(t *testing.T) {
	t.Parallel()

	// Simulate access list output with both data-prefix and upstream-prefix grants.
	accessListOutput := strings.Join([]string{
		"REPO                                        STEP   SECRET                                             APPROVED BY       APPROVED AT        STATUS",
		"codeberg/cloudtaser/cloudtaser-beacon       *      oberth/data/release/cosign-secret                  admin             2026-08-21 16:40   active",
		"codeberg/cloudtaser/cloudtaser-beacon       *      oberth/data/release/gar-sa-key                     admin             2026-08-21 16:40   active",
		"codeberg/cloudtaser/terraform               *      oberth/upstream/cloudtaser/terraform/credentials   admin             2026-08-23 22:23   active",
	}, "\n")

	mockRun := func(_ context.Context, _ []byte, _ string, _ ...string) ([]byte, error) {
		return []byte(accessListOutput), nil
	}

	identities, err := ProducePerRepoIdentities(context.Background(), nil, mockRun, "", "oberth")
	if err != nil {
		t.Fatal(err)
	}

	if len(identities) != 2 {
		t.Fatalf("expected 2 identities, got %d: %+v", len(identities), identities)
	}

	// Script bao responses for both identities' policy + role operations.
	beaconName := PerRepoName("codeberg", "cloudtaser", "cloudtaser-beacon")
	terraformName := PerRepoName("codeberg", "cloudtaser", "terraform")

	responses := map[string]fakeBaoResponse{
		// beacon: policy and role not yet present
		"policy read " + beaconName:                            {out: "No policy named: " + beaconName, err: errors.New("exit status 2")},
		"policy write " + beaconName + " -":                    {out: "Success!"},
		"read -format=json auth/kubernetes/role/" + beaconName: {out: "No value found", err: errors.New("exit status 2")},
		"write auth/kubernetes/role/" + beaconName + " -":      {out: "Success!"},
		// terraform: policy and role not yet present
		"policy read " + terraformName:                            {out: "No policy named: " + terraformName, err: errors.New("exit status 2")},
		"policy write " + terraformName + " -":                    {out: "Success!"},
		"read -format=json auth/kubernetes/role/" + terraformName: {out: "No value found", err: errors.New("exit status 2")},
		"write auth/kubernetes/role/" + terraformName + " -":      {out: "Success!"},
	}
	runner := &fakeBaoRunner{t: t, responses: responses}
	store := openBaoExec{run: runner.run, namespace: "openbao", pod: "openbao-0"}

	items, err := ConfigurePerRepoIdentities(context.Background(), store, "root-token", identities, "oberth-pipelines")
	if err != nil {
		t.Fatalf("ConfigurePerRepoIdentities rejected producer output: %v", err)
	}

	// Each identity produces a policy + role = 2 items each.
	if len(items) != 4 {
		t.Fatalf("expected 4 config items (2 per identity), got %d: %+v", len(items), items)
	}

	// Verify that the terraform policy write included the upstream grant.
	for _, call := range runner.calls {
		if call.command == "policy write "+terraformName+" -" {
			if !strings.Contains(call.stdin, `path "oberth/data/upstream/cloudtaser/terraform/credentials"`) {
				t.Fatalf("terraform policy missing upstream grant path in written HCL:\n%s", call.stdin)
			}
			return
		}
	}
	t.Fatal("terraform policy write call not found")
}
