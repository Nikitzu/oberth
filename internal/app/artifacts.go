package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
)

type ArtifactCollector interface {
	Collect(ctx context.Context, workflowName string) ([]byte, error)
}

type ArtifactStore interface {
	Extract(runID string, stream io.Reader, limit int64) error
}

func (jobs *ArgoJobs) SetArtifacts(collector ArtifactCollector, store ArtifactStore, limit int64) {
	jobs.collector = collector
	jobs.artifacts = store
	jobs.artifactLimit = limit
}

func (jobs *ArgoJobs) collectArtifacts(ctx context.Context, workflowName, runID string) string {
	if jobs.collector == nil || jobs.artifacts == nil {
		return ""
	}
	archive, err := jobs.collector.Collect(ctx, workflowName)
	if err != nil {
		return fmt.Sprintf("collect: %v", err)
	}
	if len(archive) == 0 {
		return ""
	}
	if err := jobs.artifacts.Extract(runID, bytes.NewReader(archive), jobs.artifactLimit); err != nil {
		return fmt.Sprintf("store: %v", err)
	}
	return ""
}

func (jobs *ArgoJobs) reportArtifactFailure(runID, reason string) {
	jobs.mu.Lock()
	if jobs.artifactFailures == nil {
		jobs.artifactFailures = map[string]string{}
	}
	jobs.artifactFailures[runID] = reason
	jobs.mu.Unlock()
}

func (jobs *ArgoJobs) ArtifactFailure(runID string) string {
	jobs.mu.Lock()
	defer jobs.mu.Unlock()
	return jobs.artifactFailures[runID]
}
