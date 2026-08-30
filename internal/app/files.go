package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/oberthci/oberth/internal/gitcache"
	"github.com/oberthci/oberth/pkg/argoworkflow"
)

// FileLoader resolves one declared file dependency to the bytes at its pinned
// commit.
type FileLoader interface {
	Load(ctx context.Context, ref argoworkflow.FileRef) (argoworkflow.SeededFile, error)
}

// GitFileLoader reads file dependencies out of the server's own git cache.
//
// It reuses FragmentBlobs and FragmentRegistry rather than declaring
// interfaces of its own: a file dependency and a fragment are the same read --
// resolve a tag to a commit, read one bounded blob at it -- differing only in
// which path is read and what the bytes are used for.
type GitFileLoader struct {
	blobs     FragmentBlobs
	registry  FragmentRegistry
	allowlist map[string]bool
}

// NewGitFileLoader builds the loader. The allowlist is the fragment allowlist,
// deliberately: both mechanisms answer one question -- which repositories may
// a pipeline read content from at build time -- and two lists would drift,
// leaving an administrator to remember that widening one did not widen the
// other.
func NewGitFileLoader(blobs FragmentBlobs, registry FragmentRegistry, allowlist []string) (*GitFileLoader, error) {
	if blobs == nil || registry == nil {
		return nil, errors.New("app: file dependencies require a git cache and a repository registry")
	}
	loader := &GitFileLoader{blobs: blobs, registry: registry}
	if len(allowlist) > 0 {
		loader.allowlist = make(map[string]bool, len(allowlist))
		for _, entry := range allowlist {
			key, err := argoworkflow.ParseFragmentRef(entry + "@v0")
			if err != nil {
				return nil, fmt.Errorf("app: fragment allowlist entry %q: %w", entry, err)
			}
			loader.allowlist[key.Repo] = true
		}
	}
	return loader, nil
}

func (loader *GitFileLoader) Load(ctx context.Context, ref argoworkflow.FileRef) (argoworkflow.SeededFile, error) {
	if loader.allowlist != nil && !loader.allowlist[ref.Repo] {
		return argoworkflow.SeededFile{}, fmt.Errorf("app: file dependency %s is not in the fragment allowlist", ref)
	}
	_, _, name, err := gitcache.ParseRepoPath(ref.Repo)
	if err != nil {
		return argoworkflow.SeededFile{}, fmt.Errorf("app: file dependency %s: %w", ref, err)
	}
	registered, err := loader.registry.RepositoryRegistered(ctx, name)
	if err != nil {
		return argoworkflow.SeededFile{}, fmt.Errorf("app: file dependency %s: %w", ref, err)
	}
	if !registered {
		return argoworkflow.SeededFile{}, fmt.Errorf(
			"app: file dependency %s names a repository this server does not host", ref)
	}
	sha, err := loader.blobs.TagSHA(ctx, ref.Repo, ref.Version)
	if err != nil {
		return argoworkflow.SeededFile{}, fmt.Errorf("app: file dependency %s: %w", ref, err)
	}
	// Reachability gate, the same one #213 added for fragments and for the
	// same reason. A tag is creation-only but unconstrained in what it points
	// at, so without this a tag on a commit that never went through
	// green-and-publish could feed unreviewed content into a consuming
	// repository's pipeline. A registry that decides whether a claim is real
	// is exactly the file worth pointing a tag at.
	//
	// Enforced on every trigger tier: the read below serves another
	// repository's bytes into this run regardless of what triggered it.
	reachable, err := loader.blobs.ReachableFromUpstreamDefault(ctx, ref.Repo, sha)
	if err != nil {
		return argoworkflow.SeededFile{}, fmt.Errorf("app: file dependency %s: %w", ref, err)
	}
	if !reachable {
		return argoworkflow.SeededFile{}, fmt.Errorf(
			"app: file dependency %s resolves to commit %s, which is not reachable from its repository's upstream default branch; only published versions may be read",
			ref, sha)
	}
	bytes, err := loader.blobs.ReadBlob(ctx, ref.Repo, sha, ref.Path, argoworkflow.MaxFileBytes)
	if err != nil {
		return argoworkflow.SeededFile{}, fmt.Errorf("app: file dependency %s: %w", ref, err)
	}
	return argoworkflow.SeededFile{SHA: sha, Bytes: bytes}, nil
}

// loadFiles resolves every file dependency the document declares.
//
// Like loadFragments, this decodes the source a second time. The alternative
// is threading a decoded document through the scheduler, and the cost of one
// more decode of a bounded file is not worth that coupling.
func loadFiles(ctx context.Context, loader FileLoader, source []byte) (
	map[argoworkflow.FileRef]argoworkflow.SeededFile, error,
) {
	workflow, err := argoworkflow.Decode(source)
	if err != nil {
		return nil, err
	}
	refs, err := argoworkflow.FileRefs(workflow)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, nil
	}
	if loader == nil {
		return nil, fmt.Errorf(
			"app: the pipeline declares file dependency %s but this server has no file loader configured", refs[0])
	}
	files := make(map[argoworkflow.FileRef]argoworkflow.SeededFile, len(refs))
	for _, ref := range refs {
		file, err := loader.Load(ctx, ref)
		if err != nil {
			return nil, err
		}
		files[ref] = file
	}
	return files, nil
}
