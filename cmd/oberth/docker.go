package main

import (
	"context"
	"fmt"

	"github.com/oberthci/oberth/internal/app"
	"github.com/oberthci/oberth/internal/dockerjob"
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
	controller, err := dockerjob.NewController(dockerjob.Config{
		Docker:              options.dockerBinary,
		RunnerImagePrefixes: splitRunnerImagePrefixes(options.runnerImagePrefixes),
		ArtifactsLimitBytes: artifactLimit,
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
	return jobs, nil
}
