package gitcache

import (
	"testing"

	"github.com/oberthci/oberth/pkg/argoworkflow"
)

// The blob path grammar is enforced twice: here, where a path becomes an
// argument to `git cat-file`, and in pkg/argoworkflow, where a file dependency
// is parsed out of a repository's annotation. pkg may not import internal, so
// the two are separate implementations of one rule.
//
// This test is the only place both are visible, and it asserts the direction
// that matters: pkg must never accept a path this package refuses. A path pkg
// accepts becomes an argument to `git cat-file` here, so a gap in that
// direction is a gap in the git command's input.
//
// The reverse is allowed and real. pkg refuses a comma because a comma
// separates declarations in its annotation format, a constraint this package
// does not have. Asserting exact equality would therefore force one of the two
// to adopt a rule it has no reason for.
func TestBlobPathRulesAgreeAcrossTheTrustBoundary(t *testing.T) {
	corpus := []string{
		"graph/repos.yml",
		"a",
		"a/b/c/d.txt",
		"",
		" ",
		"/etc/passwd",
		"../etc/passwd",
		"graph/../../etc/passwd",
		"./repos.yml",
		"graph//repos.yml",
		"graph/",
		"--upload-pack=x",
		"-x",
		`graph\repos.yml`,
		"graph/repos\n.yml",
		"graph/repos\x00.yml",
		"graph,repos.yml",
		"graph repos.yml",
		"graph\trepos.yml",
		" leading.txt",
		"trailing.txt ",
		"a/./b",
		"a/../b",
	}
	for _, path := range corpus {
		t.Run(path, func(t *testing.T) {
			internalErr := validateBlobPath(path)
			publicErr := argoworkflow.ValidateFilePath(path)
			if internalErr != nil && publicErr == nil {
				t.Fatalf("path %q reaches git: gitcache refuses it (%v) but argoworkflow accepts it",
					path, internalErr)
			}
		})
	}
}
