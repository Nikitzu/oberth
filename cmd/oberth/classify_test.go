package main

import (
	"strings"
	"testing"
)

// The classifier decides whether Oberth fixes its own document or hands the
// failure back. Getting this wrong in the generous direction means a tool that
// retries a genuinely failing test suite and reports it two pushes late; in
// the strict direction it means one honest red run. The tests below pin the
// boundary in both directions.

func TestClassifyGeneratorFaults(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"pnpm forwards an npm flag": "biome: unknown option '--if-present'",
		"slim image has no git":     "sh: 1: git: not found\nhusky - install failed",
		"platform binary missing":   "Error: Cannot find module '@biomejs/cli-linux-arm64'",
		"credential never arrived":  "npm error code E401\nnpm error 401 Unauthorized",
		"engine refuses a construct": "dockerjob: construct not supported by the docker engine: " +
			"repository-declared volumeMounts",
		"read-only rootfs": "E: Could not open lock file - open (30: Read-only file system)",
		"command missing":  "exec: \"pnpm\": executable file not found in $PATH",
	} {
		class, why := classifyFailure(remoteRun{}, body)
		if class != failureGenerator {
			t.Errorf("%s: classified as repository-class, want generator-class", name)
		}
		if why == "" {
			t.Errorf("%s: a retry with no stated reason is indistinguishable from a flake", name)
		}
	}
}

func TestClassifyRepositoryFaults(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"its test suite failed": "Test Files  1 failed (12)\n  Tests  3 failed | 40 passed",
		"its types do not check": "src/index.ts(14,3): error TS2322: Type 'string' is not " +
			"assignable to type 'number'.\nFound 1 error.",
		"its linter found something": "Checked 214 files in 180ms. biome found 3 errors.",
		"an assertion failed":        "AssertionError: expected 3 to equal 4",
	} {
		class, why := classifyFailure(remoteRun{}, body)
		if class != failureRepository {
			t.Errorf("%s: classified as generator-class, want repository-class", name)
		}
		if why == "" {
			t.Errorf("%s: the reader must be told why it stopped", name)
		}
	}
}

// The boundary case that matters most: a test suite whose OUTPUT contains a
// phrase the generator patterns also match. A test named for a 401 must not
// make Oberth regenerate the credential step and push again.
func TestARepositoryTestThatMentionsAGeneratorPatternIsStillRepositoryClass(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"a test about unauthorized responses": "FAIL  src/auth.test.ts\n" +
			"  ✕ returns 401 Unauthorized for a missing token\n" +
			"  Tests  1 failed | 22 passed",
		"a test asserting a missing module": "AssertionError: expected [Function] to throw " +
			"'Cannot find module' but it did not\n  Tests  1 failed",
	} {
		if class, _ := classifyFailure(remoteRun{}, body); class != failureRepository {
			t.Errorf("%s: a failing test that mentions a generator pattern was treated as a pipeline fault", name)
		}
	}
}

// An unrecognized failure defaults to the repository, because guessing that
// direction costs one honest red run and guessing the other retries twice.
func TestAnUnknownFailureIsTreatedAsTheRepositorys(t *testing.T) {
	t.Parallel()
	class, why := classifyFailure(remoteRun{}, "the machine caught fire")
	if class != failureRepository {
		t.Fatal("an unrecognized failure must not trigger an automatic pipeline repair")
	}
	if why == "" {
		t.Fatal("the reader must be told that nothing matched")
	}
}

// The run's own error is read alongside the log, because a step that never
// started produces no log at all.
func TestTheRunsOwnErrorIsClassifiedWhenThereIsNoLog(t *testing.T) {
	t.Parallel()
	run := remoteRun{Error: "dockerjob: construct not supported by the docker engine: repository-declared volumeMounts"}
	if class, _ := classifyFailure(run, ""); class != failureGenerator {
		t.Fatal("a run that failed before any step ran was not classified from its own error")
	}
}

// Go, Maven and JUnit output, taken from real runs. The first version of this
// classifier only knew JavaScript output, so a Go test failure fell through to
// the default arm and was reported with no diagnosis.
func TestClassifyRecognisesNonJavaScriptToolchains(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"go test": "--- FAIL: TestAddIsDeliberatelyWrong (0.00s)\n" +
			"    adder_test.go:15: Add(2,2) = 4, want 5\nFAIL\nFAIL\texample.com/toy\t0.001s\nFAIL",
		"go build":       "./adder.go:7:2: declared and not used: unused",
		"go vet":         "./adder.go:12:9: undefined: missingHelper",
		"maven surefire": "[ERROR] Tests run: 14, Failures: 1, Errors: 0, Skipped: 0",
		"javac":          "[ERROR] COMPILATION ERROR :\n[ERROR] cannot find symbol",
	} {
		class, why := classifyFailure(remoteRun{}, body)
		if class != failureRepository {
			t.Errorf("%s: classified as generator-class, want repository-class", name)
		}
		if strings.Contains(why, "does not match anything") {
			t.Errorf("%s: fell through to the default arm with no diagnosis", name)
		}
	}
}

// Oberth ends every docker-engine step log with a paragraph about the sandbox
// that contains the words "read-only root filesystem". Those are the server's
// words about every step, not the step's words about itself, and classifying
// on them would read the same evidence into every run.
func TestOberthsOwnTrailerIsNotEvidence(t *testing.T) {
	t.Parallel()
	trailer := "\noberth: this step ran with a read-only root filesystem as UID 0, with all " +
		"Linux capabilities dropped and privilege escalation disabled. Only /work and /tmp are " +
		"writable; a step that writes anywhere else fails here.\n"

	// A genuine repository failure stays repository-class with the trailer on.
	body := "--- FAIL: TestAdd (0.00s)\nFAIL\texample.com/toy\t0.001s" + trailer
	if class, _ := classifyFailure(remoteRun{}, body); class != failureRepository {
		t.Error("the server's own sandbox trailer turned a failing test into a pipeline fault")
	}
	// And the trailer alone is not evidence of anything.
	if class, why := classifyFailure(remoteRun{}, trailer); class != failureRepository {
		t.Errorf("the trailer alone was classified as a generator fault: %s", why)
	}
}
