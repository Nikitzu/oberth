package installer

import (
	"context"
	"sort"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

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

	result, err := ProducePerRepoIdentities(context.Background(), kube, "oberth")
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

	result, err := ProducePerRepoIdentities(context.Background(), kube, "oberth")
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

	result, err := ProducePerRepoIdentities(context.Background(), kube, "oberth")
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

	result, err := ProducePerRepoIdentities(context.Background(), kube, "oberth")
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatalf("expected nil, got %+v", result)
	}
}
