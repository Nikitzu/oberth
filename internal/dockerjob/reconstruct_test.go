package dockerjob

import (
	"errors"
	"strings"
	"testing"
)

func passed(ordinal, plan int, burn, step string) containerState {
	return containerState{burn: burn, step: step, ordinal: ordinal, planSteps: plan}
}

func TestReconstructReportsAFinishedGreenRun(t *testing.T) {
	completion, err := reconstruct("job", []containerState{
		passed(0, 2, "build", "compile"), passed(1, 2, "test", "unit"),
	})
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	if !completion.Succeeded || completion.Phase != "passed" {
		t.Fatalf("a finished green run was not reported green: %+v", completion)
	}
}

// A server killed between step 1 and step 2 leaves one passing container and
// nothing else. Calling that green publishes a commit whose pipeline never
// finished, which is the worst outcome reconstruction can produce.
func TestReconstructRefusesToCallATruncatedRunGreen(t *testing.T) {
	completion, err := reconstruct("job", []containerState{passed(0, 3, "build", "compile")})
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	if completion.Succeeded {
		t.Fatalf("an interrupted run was reported green: %+v", completion)
	}
	if !strings.Contains(completion.Reason, "interrupted") {
		t.Fatalf("reason does not say the run was interrupted: %q", completion.Reason)
	}
}

// Containers that predate the step-count label cannot prove the run finished.
func TestReconstructRefusesContainersWithNoRecordedPlan(t *testing.T) {
	completion, err := reconstruct("job", []containerState{passed(0, 0, "build", "compile")})
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	if completion.Succeeded {
		t.Fatalf("an unreconstructable run was reported green: %+v", completion)
	}
}

func TestReconstructAttributesTheFailedStep(t *testing.T) {
	failing := passed(1, 2, "test", "unit")
	failing.exitCode = 3
	completion, err := reconstruct("job", []containerState{passed(0, 2, "build", "compile"), failing})
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	if completion.Succeeded || completion.FailedBurn != "test" || completion.FailedStep != "unit" {
		t.Fatalf("failed step not attributed: %+v", completion)
	}
}

// Only the last attempt of a retried step decides its verdict, and a retried
// step that eventually passed must not leave the run red.
func TestReconstructTakesTheLastAttemptOfARetriedStep(t *testing.T) {
	first := passed(0, 1, "test", "unit")
	first.exitCode, first.attempt = 1, 0
	second := passed(0, 1, "test", "unit")
	second.attempt = 1
	completion, err := reconstruct("job", []containerState{first, second})
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	if !completion.Succeeded {
		t.Fatalf("a step that passed on retry was reported failed: %+v", completion)
	}
}

func TestReconstructIsNotTerminalWhileAContainerRuns(t *testing.T) {
	running := passed(0, 2, "build", "compile")
	running.running = true
	if _, err := reconstruct("job", []containerState{running}); !errors.Is(err, ErrNotTerminal) {
		t.Fatalf("expected ErrNotTerminal, got %v", err)
	}
}
