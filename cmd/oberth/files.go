package main

import (
	"context"
	"fmt"
	"io"

	"github.com/oberthci/oberth/internal/gitcache"
	"github.com/oberthci/oberth/internal/store"
	"github.com/oberthci/oberth/pkg/argoworkflow"
)

// `files show` mirrors `fragments show`: in-pod, reading the git cache
// directly, needing no running server.
//
// There is deliberately no `files list`. Enumerating a tag's tree needs a
// `git ls-tree` the cache does not have, with its own bounds question, and a
// reference is written by someone who already knows the path.
func runFiles(ctx context.Context, arguments []string, output io.Writer) error {
	if len(arguments) == 0 {
		return fmt.Errorf("%w: files show <repository>@<version>:<path>", errUsage)
	}
	switch arguments[0] {
	case "show":
		return runFilesShow(ctx, arguments[1:], output)
	default:
		return fmt.Errorf("%w: files show <repository>@<version>:<path>", errUsage)
	}
}

func runFilesShow(ctx context.Context, arguments []string, output io.Writer) error {
	databasePath, dataRoot, rest, err := fragmentFlags("files show", arguments, output)
	if err != nil || databasePath == "" {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("%w: files show <repository>@<version>:<path>", errUsage)
	}
	ref, err := argoworkflow.ParseFileRef(rest[0])
	if err != nil {
		return err
	}
	database, err := store.OpenAdminClient(ctx, databasePath, store.Options{})
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()
	_, _, name, err := gitcache.ParseRepoPath(ref.Repo)
	if err != nil {
		return err
	}
	registered, err := database.RepositoryRegistered(ctx, name)
	if err != nil {
		return err
	}
	if !registered {
		return fmt.Errorf("file dependency %s names a repository this server does not host", ref)
	}
	cache, err := openFragmentCache(dataRoot)
	if err != nil {
		return err
	}
	sha, err := cache.TagSHA(ctx, ref.Repo, ref.Version)
	if err != nil {
		return err
	}
	body, err := cache.ReadBlob(ctx, ref.Repo, sha, ref.Path, argoworkflow.MaxFileBytes)
	if err != nil {
		return err
	}
	// The commit goes to stderr-adjacent commentary on stdout the way
	// `fragments show` does, so the bytes below are the file's own.
	if _, err := fmt.Fprintf(output, "# %s resolves to %s\n", ref, sha); err != nil {
		return err
	}
	_, err = output.Write(body)
	return err
}
