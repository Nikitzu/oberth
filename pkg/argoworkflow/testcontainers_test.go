package argoworkflow

import (
	"testing"

	wfv1 "github.com/argoproj/argo-workflows/v4/pkg/apis/workflow/v1alpha1"
)

func decodeForTest(t *testing.T, source string) *wfv1.Workflow {
	t.Helper()
	workflow, err := Decode([]byte(source))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return workflow
}

func testcontainersDocument(value string) string {
	return `
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  annotations:
    oberth.ci/testcontainers: "` + value + `"
spec:
  entrypoint: ci
  templates:
    - name: ci
      container:
        image: alpine
`
}

func TestDeclaresTestcontainersReadsTheAnnotation(t *testing.T) {
	if !DeclaresTestcontainers(decodeForTest(t, testcontainersDocument("true"))) {
		t.Fatal("annotation set to \"true\" was not read as declared")
	}
}

func TestDeclaresTestcontainersIsFalseOtherwise(t *testing.T) {
	for _, value := range []string{"", "false", "yes", "1"} {
		if DeclaresTestcontainers(decodeForTest(t, testcontainersDocument(value))) {
			t.Fatalf("value %q was read as declared; only \"true\" declares", value)
		}
	}
	if DeclaresTestcontainers(nil) {
		t.Fatal("nil workflow declared testcontainers")
	}
}
