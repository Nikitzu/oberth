package installer

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func deploymentWithArgs(args ...string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "oberth", Namespace: DefaultNamespace},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "oberth", Args: args}},
				},
			},
		},
	}
}

// The advisory gate is the whole reason to run CI locally: a green run is a
// verdict, the developer pushes their own branch with their own credentials.
// A server that never reaches the forge must not ask anyone to arrange a
// deploy key for it.
func TestAdvisoryGateNeedsNoForgeCredential(t *testing.T) {
	deps := Deps{KubeClient: fake.NewClientset(deploymentWithArgs("serve", "--publish-on-green=false"))}
	if deploymentPublishes(context.Background(), Config{}, deps) {
		t.Fatal("a deployment that suppresses publication was reported as publishing")
	}
}

func TestPublishingDeploymentStillNeedsItsKey(t *testing.T) {
	deps := Deps{KubeClient: fake.NewClientset(deploymentWithArgs("serve", "--publish-on-green=true"))}
	if !deploymentPublishes(context.Background(), Config{}, deps) {
		t.Fatal("a publishing deployment must still be asked for a key")
	}
}

// Skipping a key that was needed costs a push that fails later with nothing on
// screen to explain it; asking for one that was not needed costs a prompt.
func TestUnreadableStateAsksForTheKey(t *testing.T) {
	for name, deps := range map[string]Deps{
		"no client":     {},
		"no deployment": {KubeClient: fake.NewClientset()},
		"no flag":       {KubeClient: fake.NewClientset(deploymentWithArgs("serve"))},
	} {
		if !deploymentPublishes(context.Background(), Config{}, deps) {
			t.Errorf("%s: must fall back to asking for a key", name)
		}
	}
}
