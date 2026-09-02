package main

import "strings"

// A red run has two possible authors, and telling them apart is the whole
// difference between a tool that fixes its own mistakes and one that edits
// your code.
//
// generator-class: the pipeline asked for something the runner cannot do. A
// missing tool, a flag the package manager forwards instead of consuming, a
// credential that never arrived, a construct the engine refuses, a binary for
// the wrong architecture, an image without git. Oberth wrote that, so Oberth
// fixes it, re-stores the document, and pushes again.
//
// repository-class: the repository's own tests failed, its linter found
// something, its types do not check. That is the answer CI exists to give. It
// stops immediately and hands back the step and the log command. Nothing here
// ever weakens a repository's own checks, and no automatic repair is allowed
// to touch them.
//
// The default is repository-class. An unrecognized failure is far more likely
// to be real than to be the generator's, and guessing wrong in that direction
// costs one honest red run, while guessing wrong the other way retries a real
// failure twice and then reports it late.

type failureClass int

const (
	failureRepository failureClass = iota
	failureGenerator
)

// generatorSignatures are the log patterns that mean the pipeline, not the
// code. Each carries the sentence printed when it matches, because a retry
// that does not say why it is retrying is indistinguishable from a flake.
var generatorSignatures = []struct {
	patterns []string
	why      string
}{
	{
		// pnpm forwards an unrecognized flag to the script it runs, so an
		// npm-ism reaches the tool as an argument it has never heard of.
		patterns: []string{"unknown option", "unrecognized option", "unexpected argument", "--if-present"},
		why:      "the step passed a flag this tool does not accept; regenerating with the right invocation shape",
	},
	{
		// A prepare script shelling out to git on a -slim image.
		patterns: []string{"git: not found", "git: command not found", "husky", "spawn git"},
		why:      "the image has no git and this repository's install needs it; regenerating on the variant that ships it",
	},
	{
		// A per-architecture optional dependency the lockfile never pinned.
		patterns: []string{"cannot find module", "could not resolve", "failed to load", "no binary found", "is not supported on"},
		why:      "a tool's platform binary is missing on this runner; regenerating with the matching-platform fetch",
	},
	{
		// The credential never reached the step, or reached it unusable.
		patterns: []string{"401 unauthorized", "e401", "no credential was delivered", "authentication failed", "need auth", "enotoken"},
		why:      "the registry credential did not arrive in the form this package manager reads; regenerating the credential step",
	},
	{
		// The engine refused something the document declared.
		patterns: []string{"not supported by the docker engine", "construct not supported", "volumemounts"},
		why:      "this engine refuses a construct the document declared; regenerating for this engine",
	},
	{
		// The rootfs is read-only, so a step tried to install a system
		// package. That can never work and the generator must not emit it.
		patterns: []string{"read-only file system", "apt-get", "permission denied (os error 30)"},
		why:      "a step tried to write outside the build tree on a read-only root filesystem; regenerating without it",
	},
	{
		// The runner could not even start the command.
		patterns: []string{"executable file not found", "command not found", "no such file or directory: /bin"},
		why:      "the step named a command the image does not carry; regenerating with one it does",
	},
}

// repositorySignatures are the patterns that mean the repository's own checks
// spoke. They are consulted FIRST, because a failing test suite can easily
// print a line that looks like one of the generator patterns -- a test named
// "returns 401 unauthorized" is the obvious case -- and mistaking it for a
// generator fault would retry a real failure and report it two pushes late.
var repositorySignatures = []struct {
	patterns []string
	why      string
}{
	{
		patterns: []string{"tests failed", "test failed", "failed tests", "assertionerror", "expected", "✕", "✗"},
		why:      "its own test suite failed",
	},
	{
		patterns: []string{"found 1 error", "found 2 errors", "error ts", "type error", "typescript error"},
		why:      "its own typecheck failed",
	},
	{
		patterns: []string{"lint error", "checked ", "found 1 error.", "biome found", "eslint", "problems ("},
		why:      "its own lint found something",
	},
}

// classifyFailure reads the failed step's log and decides who is at fault.
func classifyFailure(run remoteRun, logBody string) (failureClass, string) {
	haystack := strings.ToLower(logBody + "\n" + run.Error)

	// The repository speaks first. See repositorySignatures.
	for _, signature := range repositorySignatures {
		if matchesAny(haystack, signature.patterns) {
			return failureRepository, "That is the repository's own gate, not the pipeline's: " + signature.why + "."
		}
	}
	for _, signature := range generatorSignatures {
		if matchesAny(haystack, signature.patterns) {
			return failureGenerator, signature.why
		}
	}
	return failureRepository, "The failure does not match anything Oberth knows how to repair, so it is treated as the repository's."
}

func matchesAny(haystack string, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.Contains(haystack, pattern) {
			return true
		}
	}
	return false
}
