package argoworkflow

import (
	"strings"

	wfv1 "github.com/argoproj/argo-workflows/v4/pkg/apis/workflow/v1alpha1"
)

// TestcontainersAnnotation is where a repository says its tests start
// containers through the Docker API. Only the literal "true" declares it, so a
// stray value is a no rather than a surprise proxy.
const TestcontainersAnnotation = "oberth.ci/testcontainers"

func DeclaresTestcontainers(workflow *wfv1.Workflow) bool {
	if workflow == nil {
		return false
	}
	return strings.TrimSpace(workflow.Annotations[TestcontainersAnnotation]) == "true"
}
