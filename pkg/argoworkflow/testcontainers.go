package argoworkflow

import (
	"strings"

	wfv1 "github.com/argoproj/argo-workflows/v4/pkg/apis/workflow/v1alpha1"
)

const TestcontainersAnnotation = "oberth.ci/testcontainers"

func DeclaresTestcontainers(workflow *wfv1.Workflow) bool {
	if workflow == nil {
		return false
	}
	return strings.TrimSpace(workflow.Annotations[TestcontainersAnnotation]) == "true"
}
