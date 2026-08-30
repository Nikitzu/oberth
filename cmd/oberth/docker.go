package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/oberthci/oberth/internal/app"
	"github.com/oberthci/oberth/internal/dockerjob"
	"github.com/oberthci/oberth/internal/secretstore"
	"github.com/oberthci/oberth/internal/service"
)

// buildDockerEngine wires the Docker execution engine from serve flags.
//
// It is reached only when --engine=docker. The whole point of this engine is
// that it needs no cluster, so its administrator surface is small: the image
// allowlist admission applies, the log and artifact budgets, and the daemon to
// talk to. Everything else a pipeline can reach comes from the admitted
// document, exactly as it does on the Argo path.
func buildDockerEngine(
	ctx context.Context,
	options serveOptions,
	auditor service.Auditor,
	artifactStore app.ArtifactStore,
	artifactLimit int64,
	artifactBudget int64,
) (*app.DockerJobs, error) {
	store, err := buildDockerSecretStore(options)
	if err != nil {
		return nil, err
	}
	controller, err := dockerjob.NewController(dockerjob.Config{
		Docker:              options.dockerBinary,
		RunnerImagePrefixes: splitRunnerImagePrefixes(options.runnerImagePrefixes),
		ArtifactsLimitBytes: artifactLimit,
		SecretStore:         store,
	})
	if err != nil {
		return nil, fmt.Errorf("configure docker execution engine: %w", err)
	}
	// Fail at startup rather than at the first push. A server that cannot
	// reach a daemon cannot run anything, and saying so now is much cheaper
	// than a repository discovering it through a red run.
	if err := controller.Available(ctx); err != nil {
		return nil, err
	}
	jobs, err := app.NewDockerJobs(controller, auditor)
	if err != nil {
		return nil, err
	}
	jobs.SetArtifacts(artifactStore, artifactLimit, artifactBudget)
	jobs.SetSecretStore(store.Enabled())
	return jobs, nil
}

// buildDockerSecretStore assembles the credentialed path's coordinates, or
// returns an empty configuration when no store is configured, in which case a
// pipeline declaring secret paths is refused at submission.
//
// The tier roles are the ones `oberth secretstore init --engine=docker`
// creates. Two roles, two policies, two subjects: OpenBao decides which one
// may read a release path, exactly as it does on a cluster where the role
// binds a ServiceAccount name instead of a subject claim.
func buildDockerSecretStore(options serveOptions) (dockerjob.SecretStoreConfig, error) {
	address := strings.TrimSpace(options.secretStoreAddress)
	if address == "" {
		return dockerjob.SecretStoreConfig{}, nil
	}
	minter, err := secretstore.NewJWTMinter(options.secretStoreJWTSigningKey)
	if err != nil {
		return dockerjob.SecretStoreConfig{}, err
	}
	ciRole := strings.TrimSpace(options.secretStoreRole)
	if ciRole == "" {
		ciRole = dockerjob.DefaultCIRole
	}
	return dockerjob.SecretStoreConfig{
		Address:     address,
		KVMount:     options.secretStoreKVMount,
		CIRole:      ciRole,
		ReleaseRole: dockerjob.DefaultReleaseRole,
		Minter:      minter,
	}, nil
}
