package service

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestPipelineSetOnABranchMismatchSaysWhatIsWrong is the gateway failure.
//
// The repository was registered with default branch "main" while its upstream
// is on "master". `repo pipeline set` then resolved a ref that does not exist,
// and the resolution error fell through the classifier's default arm as a
// bare 500 reading "internal error". Three sessions read that as a server
// fault. It is a registration that assumed a branch, and the message has to
// say so and say what corrects it.
func TestPipelineSetOnABranchMismatchSaysWhatIsWrong(t *testing.T) {
	t.Parallel()
	fixture := newPipelineFixture(t)
	// An upstream that cannot resolve the registered default branch, which is
	// exactly what a "main" registration meets on a "master" repository.
	fixture.git.head = ""

	document := generatedDocument(t, fixture, firstCommit)
	_, err := fixture.api.pipelineSet(context.Background(), "SHA256:operator", "oberth", "build",
		[]byte(document), "")
	if err == nil {
		t.Fatal("expected a refusal when the registered default branch cannot be resolved")
	}
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("branch mismatch error = %v, want ErrInvalidInput so the API answers 400, not 500", err)
	}
	for _, want := range []string{"default branch", `"main"`, "oberth onboard", "--ref"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("branch mismatch message is missing %q:\n%s", want, err)
		}
	}
}

// A caller who names the commit does not depend on the registration at all,
// so it must still work while the default branch is wrong.
func TestPipelineSetWithAnExplicitRefIgnoresTheRegisteredBranch(t *testing.T) {
	t.Parallel()
	fixture := newPipelineFixture(t)
	fixture.git.head = ""

	document := generatedDocument(t, fixture, firstCommit)
	stored, err := fixture.api.pipelineSet(context.Background(), "SHA256:operator", "oberth", "build",
		[]byte(document), firstCommit)
	if err != nil {
		t.Fatalf("pipelineSet with an explicit ref: %v", err)
	}
	if stored.FingerprintRef != firstCommit {
		t.Fatalf("fingerprint ref = %q, want %q", stored.FingerprintRef, firstCommit)
	}
}
