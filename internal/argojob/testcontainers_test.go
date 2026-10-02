package argojob

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/oberthci/oberth/pkg/periapsis"
)

const testcontainersDocument = `
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  annotations:
    oberth.ci/testcontainers: "true"
spec:
  entrypoint: main
  activeDeadlineSeconds: 600
  templates:
    - name: main
      dag:
        tasks:
          - name: test
            template: test
          - name: pinned
            template: pinned
    - name: test
      container:
        image: maven:3.9-eclipse-temurin-21@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
        command: [mvn, verify]
    - name: pinned
      container:
        image: maven:3.9-eclipse-temurin-21@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
        command: [mvn, verify]
        env:
          - name: DOCKER_HOST
            value: tcp://elsewhere:2375
`

func testcontainersEnvironmentOf(t *testing.T, request Request, template string) map[string]string {
	t.Helper()
	workflow, err := Build(testConfig(), request)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, candidate := range workflow.Spec.Templates {
		if candidate.Name == template && candidate.Container != nil {
			values := map[string]string{}
			for _, variable := range candidate.Container.Env {
				values[variable.Name] = variable.Value
			}
			return values
		}
	}
	t.Fatalf("template %q not found", template)
	return nil
}

func TestTestcontainersRunPointsEveryStepAtKubedock(t *testing.T) {
	request := testRequest(periapsis.TriggerCI, testcontainersDocument)
	request.Testcontainers = true

	values := testcontainersEnvironmentOf(t, request, "test")
	for name, want := range map[string]string{
		"DOCKER_HOST":                   "tcp://kubedock:2475",
		"TESTCONTAINERS_RYUK_DISABLED":  "true",
		"TESTCONTAINERS_CHECKS_DISABLE": "true",
	} {
		if values[name] != want {
			t.Errorf("%s = %q, want %q", name, values[name], want)
		}
	}
	if _, set := values["TESTCONTAINERS_HOST_OVERRIDE"]; set {
		t.Error("kubedock runs with --reverse-proxy, so the host comes from DOCKER_HOST and must not be overridden")
	}
}

func TestStepSetDockerHostIsKept(t *testing.T) {
	request := testRequest(periapsis.TriggerCI, testcontainersDocument)
	request.Testcontainers = true

	if got := testcontainersEnvironmentOf(t, request, "pinned")["DOCKER_HOST"]; got != "tcp://elsewhere:2375" {
		t.Fatalf("DOCKER_HOST = %q, want the step's own value", got)
	}
}

func TestNoTestcontainersMeansNoDockerHost(t *testing.T) {
	values := testcontainersEnvironmentOf(t, testRequest(periapsis.TriggerCI, testcontainersDocument), "test")
	if _, set := values["DOCKER_HOST"]; set {
		t.Fatal("DOCKER_HOST injected into a run the caller did not mark as Testcontainers")
	}
}

func TestDefaultEnvironmentKeepsOrderAndExistingValues(t *testing.T) {
	merged := defaultEnvironment(
		[]corev1.EnvVar{{Name: "A", Value: "mine"}},
		[]corev1.EnvVar{{Name: "A", Value: "theirs"}, {Name: "B", Value: "added"}},
	)
	if len(merged) != 2 || merged[0].Value != "mine" || merged[1].Name != "B" {
		t.Fatalf("merged = %+v", merged)
	}
}
