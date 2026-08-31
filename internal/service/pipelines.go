package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pmezard/go-difflib/difflib"

	"github.com/oberthci/oberth/internal/model"
	"github.com/oberthci/oberth/internal/pipelinegen"
	"github.com/oberthci/oberth/internal/store"
	"github.com/oberthci/oberth/pkg/argoworkflow"
	"github.com/oberthci/oberth/pkg/periapsis"
)

// PipelineStore is the durable home of server-held pipeline documents.
type PipelineStore interface {
	StoreRepoPipeline(context.Context, model.RepoPipelineSpec) (model.RepoPipeline, error)
	RepoPipeline(context.Context, int64, string) (model.RepoPipeline, error)
	RepoPipelineVersions(context.Context, int64, string) ([]model.RepoPipeline, error)
}

// PipelineGit materializes one commit of a repository so the generator inputs
// can be read from it. It is the git cache, which already knows how to do both
// of these for the promotion path.
type PipelineGit interface {
	Checkout(context.Context, string, string, string) error
	RefSHA(context.Context, string, string) (string, error)
}

// UpstreamReader resolves the upstream a repository belongs to, which is what
// scopes the secret path the generator writes.
type UpstreamReader interface {
	Upstream(context.Context, int64) (model.Upstream, error)
}

// PipelineResponse is the read view of a repository's server-held document.
type PipelineResponse struct {
	Repository     string    `json:"repository"`
	Trigger        string    `json:"trigger"`
	TriggerFile    string    `json:"trigger_file"`
	Held           bool      `json:"held"`
	Version        int64     `json:"version"`
	SHA256         string    `json:"sha256"`
	Document       string    `json:"document"`
	StoredBy       string    `json:"stored_by"`
	StoredAt       time.Time `json:"stored_at"`
	FingerprintRef string    `json:"fingerprint_ref"`
	Inputs         []string  `json:"inputs"`
	Versions       int       `json:"versions"`
}

// PipelineCheckResponse is the drift report for one named commit.
type PipelineCheckResponse struct {
	Repository string   `json:"repository"`
	Trigger    string   `json:"trigger"`
	Ref        string   `json:"ref"`
	Drifted    bool     `json:"drifted"`
	Changed    []string `json:"changed"`
	Diff       string   `json:"diff"`
	Stored     bool     `json:"stored"`
	Version    int64    `json:"version"`
}

// pipelineTriggers maps the word an operator types to the repository file the
// document would have occupied.
func pipelineTriggerFile(trigger string) (string, periapsis.Trigger, error) {
	switch strings.TrimSpace(strings.ToLower(trigger)) {
	case "", "build", "ci":
		return argoworkflow.BuildFile, periapsis.TriggerCI, nil
	case "release":
		return argoworkflow.ReleaseFile, periapsis.TriggerRelease, nil
	default:
		return "", "", fmt.Errorf("%w: unknown trigger %q; use build or release", ErrInvalidInput, trigger)
	}
}

func (service *API) pipelineRepository(ctx context.Context, name string) (model.Repository, error) {
	if service.pipelines == nil {
		return model.Repository{}, fmt.Errorf("%w: server-held pipelines", ErrUnavailable)
	}
	if service.repositories == nil {
		return model.Repository{}, fmt.Errorf("%w: repository catalog", ErrUnavailable)
	}
	if strings.TrimSpace(name) == "" {
		return model.Repository{}, fmt.Errorf("%w: repository name is required", ErrInvalidInput)
	}
	return service.repositories.RepositoryByName(ctx, name)
}

// PipelineShow returns the document the server currently holds for one
// repository and trigger, with the metadata that says who put it there.
func (service *API) pipelineShow(ctx context.Context, repoName, trigger string) (PipelineResponse, error) {
	file, _, err := pipelineTriggerFile(trigger)
	if err != nil {
		return PipelineResponse{}, err
	}
	repository, err := service.pipelineRepository(ctx, repoName)
	if err != nil {
		return PipelineResponse{}, err
	}
	response := PipelineResponse{Repository: repository.Name, Trigger: pipelineTriggerName(file), TriggerFile: file}
	versions, err := service.pipelines.RepoPipelineVersions(ctx, repository.ID, file)
	if err != nil {
		return PipelineResponse{}, err
	}
	response.Versions = len(versions)
	current, err := service.pipelines.RepoPipeline(ctx, repository.ID, file)
	if errors.Is(err, store.ErrNotFound) {
		return response, nil
	}
	if err != nil {
		return PipelineResponse{}, err
	}
	response.Version = current.Version
	response.StoredBy, response.StoredAt = current.StoredBy, current.StoredAt
	if current.Tombstone {
		return response, nil
	}
	response.Held = true
	response.SHA256 = current.SHA256
	response.Document = string(current.Document)
	response.FingerprintRef = current.FingerprintRef
	response.Inputs = sortedKeys(current.Fingerprint)
	return response, nil
}

// PipelineSet stores a document for one repository and trigger.
//
// The document goes through the SAME admission a pushed document goes through
// before a single byte is written, and the refusal an operator sees here is
// the refusal a push would have produced. Storing an inadmissible document
// would move the failure from this command to the first push of a repository
// that no longer carries a pipeline to fall back to.
func (service *API) pipelineSet(ctx context.Context, actor, repoName, trigger string,
	document []byte, ref string) (PipelineResponse, error) {
	file, _, err := pipelineTriggerFile(trigger)
	if err != nil {
		return PipelineResponse{}, err
	}
	repository, err := service.pipelineRepository(ctx, repoName)
	if err != nil {
		return PipelineResponse{}, err
	}
	if len(document) == 0 {
		return PipelineResponse{}, fmt.Errorf("%w: the pipeline document is empty", ErrInvalidInput)
	}
	if int64(len(document)) > argoworkflow.MaxSourceBytes {
		return PipelineResponse{}, fmt.Errorf("%w: the pipeline document exceeds the source-size limit", ErrInvalidInput)
	}
	if err := service.admitPipelineDocument(document); err != nil {
		return PipelineResponse{}, err
	}

	// The fingerprint is read from a real checkout of a real commit, so what
	// drift is measured against is a revision anyone can name and inspect.
	fingerprint, resolvedRef, err := service.fingerprintAt(ctx, repository, ref)
	if err != nil {
		return PipelineResponse{}, err
	}
	stored, err := service.pipelines.StoreRepoPipeline(ctx, model.RepoPipelineSpec{
		RepoID: repository.ID, TriggerFile: file, Document: document,
		Fingerprint: fingerprint, FingerprintRef: resolvedRef, StoredBy: actor,
	})
	if err != nil {
		return PipelineResponse{}, err
	}
	service.auditPipeline(ctx, actor, "pipeline.set", repository, stored)
	return PipelineResponse{
		Repository: repository.Name, Trigger: pipelineTriggerName(file), TriggerFile: file,
		Held: true, Version: stored.Version, SHA256: stored.SHA256, Document: string(document),
		StoredBy: stored.StoredBy, StoredAt: stored.StoredAt, FingerprintRef: resolvedRef,
		Inputs: sortedKeys(fingerprint),
	}, nil
}

// PipelineUnset appends a tombstone version. The repository falls back to
// commit-only resolution, and every previously stored document stays readable.
func (service *API) pipelineUnset(ctx context.Context, actor, repoName, trigger string) (PipelineResponse, error) {
	file, _, err := pipelineTriggerFile(trigger)
	if err != nil {
		return PipelineResponse{}, err
	}
	repository, err := service.pipelineRepository(ctx, repoName)
	if err != nil {
		return PipelineResponse{}, err
	}
	current, err := service.pipelines.RepoPipeline(ctx, repository.ID, file)
	if errors.Is(err, store.ErrNotFound) {
		return PipelineResponse{}, fmt.Errorf("%w: %s holds no server-side pipeline for %s",
			ErrInvalidInput, repository.Name, file)
	}
	if err != nil {
		return PipelineResponse{}, err
	}
	if current.Tombstone {
		return PipelineResponse{}, fmt.Errorf("%w: %s already fell back to commit-only pipelines at version %d",
			ErrInvalidInput, repository.Name, current.Version)
	}
	stored, err := service.pipelines.StoreRepoPipeline(ctx, model.RepoPipelineSpec{
		RepoID: repository.ID, TriggerFile: file, Tombstone: true, StoredBy: actor,
	})
	if err != nil {
		return PipelineResponse{}, err
	}
	service.auditPipeline(ctx, actor, "pipeline.unset", repository, stored)
	return PipelineResponse{
		Repository: repository.Name, Trigger: pipelineTriggerName(file), TriggerFile: file,
		Held: false, Version: stored.Version, StoredBy: stored.StoredBy, StoredAt: stored.StoredAt,
	}, nil
}

// PipelineCheck regenerates the pipeline from a named commit with the same
// generator `oberth init` runs, and reports how it differs from the stored
// document.
func (service *API) pipelineCheck(ctx context.Context, actor, repoName, trigger, ref string,
	restore bool) (PipelineCheckResponse, error) {
	file, _, err := pipelineTriggerFile(trigger)
	if err != nil {
		return PipelineCheckResponse{}, err
	}
	if file != argoworkflow.BuildFile {
		return PipelineCheckResponse{}, fmt.Errorf(
			"%w: the generator writes %s only, so there is nothing to regenerate for the release trigger",
			ErrInvalidInput, argoworkflow.BuildFile)
	}
	repository, err := service.pipelineRepository(ctx, repoName)
	if err != nil {
		return PipelineCheckResponse{}, err
	}
	current, err := service.pipelines.RepoPipeline(ctx, repository.ID, file)
	if errors.Is(err, store.ErrNotFound) || (err == nil && current.Tombstone) {
		return PipelineCheckResponse{}, fmt.Errorf("%w: %s holds no server-side pipeline for %s",
			ErrInvalidInput, repository.Name, file)
	}
	if err != nil {
		return PipelineCheckResponse{}, err
	}

	workspace, resolvedRef, err := service.checkoutForPipeline(ctx, repository, ref)
	if err != nil {
		return PipelineCheckResponse{}, err
	}
	defer func() { _ = os.RemoveAll(filepath.Dir(workspace)) }()

	regenerated, err := service.regenerate(ctx, repository, workspace)
	if err != nil {
		return PipelineCheckResponse{}, err
	}
	fingerprint := pipelinegen.FingerprintInputs(workspace)
	changed := pipelinegen.DriftedInputs(current.Fingerprint, fingerprint)

	unified, err := unifiedDiff(file+" (server-held v"+fmt.Sprint(current.Version)+")",
		file+" (generated from "+shortRef(resolvedRef)+")", string(current.Document), regenerated)
	if err != nil {
		return PipelineCheckResponse{}, err
	}
	response := PipelineCheckResponse{
		Repository: repository.Name, Trigger: pipelineTriggerName(file), Ref: resolvedRef,
		Drifted: len(changed) > 0 || unified != "", Changed: changed, Diff: unified,
		Version: current.Version,
	}
	if restore && response.Drifted {
		if err := service.admitPipelineDocument([]byte(regenerated)); err != nil {
			return PipelineCheckResponse{}, err
		}
		stored, storeErr := service.pipelines.StoreRepoPipeline(ctx, model.RepoPipelineSpec{
			RepoID: repository.ID, TriggerFile: file, Document: []byte(regenerated),
			Fingerprint: fingerprint, FingerprintRef: resolvedRef, StoredBy: actor,
		})
		if storeErr != nil {
			return PipelineCheckResponse{}, storeErr
		}
		service.auditPipeline(ctx, actor, "pipeline.set", repository, stored)
		response.Stored, response.Version = true, stored.Version
	}
	return response, nil
}

// admitPipelineDocument is the whole gate on stored bytes: the same strict
// decode and the same admission policy a pushed document meets.
func (service *API) admitPipelineDocument(document []byte) error {
	workflow, err := argoworkflow.Decode(document)
	if err != nil {
		return fmt.Errorf("%w: decode pipeline document: %w", ErrInvalidInput, err)
	}
	if err := argoworkflow.Admit(workflow, argoworkflow.Policy{
		RunnerImagePrefixes: service.pipelineImagePrefixes,
	}); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	return nil
}

// fingerprintAt reads the generator inputs at one commit. The commit is named
// by the operator, or is the repository's default-branch head as Oberth knows
// it, which is what a push would have carried.
func (service *API) fingerprintAt(ctx context.Context, repository model.Repository,
	ref string) (pipelinegen.Fingerprint, string, error) {
	if service.pipelineGit == nil {
		// A deployment with no git cache wired can still hold a document; it
		// simply has no revision to measure drift against, and says so by
		// storing no fingerprint rather than by refusing.
		return nil, "", nil
	}
	workspace, resolved, err := service.checkoutForPipeline(ctx, repository, ref)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = os.RemoveAll(filepath.Dir(workspace)) }()
	return pipelinegen.FingerprintInputs(workspace), resolved, nil
}

func (service *API) checkoutForPipeline(ctx context.Context, repository model.Repository,
	ref string) (string, string, error) {
	if service.pipelineGit == nil {
		return "", "", fmt.Errorf("%w: repository checkouts", ErrUnavailable)
	}
	resolved := strings.TrimSpace(ref)
	if resolved == "" {
		branch := repository.DefaultBranch
		if branch == "" {
			branch = "main"
		}
		sha, err := service.pipelineGit.RefSHA(ctx, repository.Name, branch)
		if err != nil {
			return "", "", fmt.Errorf("resolve %s of %s: %w", branch, repository.Name, err)
		}
		resolved = sha
	}
	if err := os.MkdirAll(service.promotionWorkspaceRoot, 0o700); err != nil {
		return "", "", fmt.Errorf("create the workspace root: %w", err)
	}
	workspace, err := os.MkdirTemp(service.promotionWorkspaceRoot, "pipeline-")
	if err != nil {
		return "", "", fmt.Errorf("create pipeline workspace: %w", err)
	}
	// The checkout goes into a path that does NOT yet exist: git refuses to
	// materialize a revision into an occupied directory, and MkdirTemp's whole
	// job is to have created one.
	source := filepath.Join(workspace, "src")
	if err := service.pipelineGit.Checkout(ctx, repository.Name, resolved, source); err != nil {
		_ = os.RemoveAll(workspace)
		return "", "", fmt.Errorf("check out %s of %s: %w", shortRef(resolved), repository.Name, err)
	}
	return source, resolved, nil
}

// regenerate runs the init generator over a checkout, with the org resolved
// the way `oberth init` resolves it: from the upstream the server registered,
// never from the checkout.
func (service *API) regenerate(ctx context.Context, repository model.Repository, root string) (string, error) {
	project := pipelinegen.DetectProject(root)
	if workflow, ok := pipelinegen.FindBuildWorkflow(root); ok {
		pipelinegen.Apply(workflow, &project)
	}
	project.Repo = repository.Name
	if project.PrivateRegistry && service.upstreams != nil {
		upstream, err := service.upstreams.Upstream(ctx, repository.UpstreamID)
		if err != nil {
			return "", fmt.Errorf("resolve the upstream organization for %s: %w", repository.Name, err)
		}
		project.Org = upstream.Org()
	}
	return pipelinegen.Generate(project).YAML, nil
}

func (service *API) auditPipeline(ctx context.Context, actor, action string,
	repository model.Repository, stored model.RepoPipeline) {
	if service.auditor == nil {
		return
	}
	details := fmt.Sprintf(`{"repo":%q,"trigger_file":%q,"version":%d,"sha256":%q,"tombstone":%t}`,
		repository.Name, stored.TriggerFile, stored.Version, stored.SHA256, stored.Tombstone)
	_, _ = service.auditor.AppendAuditAction(ctx, model.AuditActionSpec{
		Actor: actor, Action: action, ResourceType: "repository",
		ResourceID: fmt.Sprint(repository.ID), Details: details,
	})
}

func pipelineTriggerName(file string) string {
	if file == argoworkflow.ReleaseFile {
		return "release"
	}
	return "build"
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func shortRef(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// unifiedDiff renders the standard three-line-context unified diff, so the
// output is the same shape every other tool prints and can be piped to a
// pager, `git apply`, or a reviewer.
func unifiedDiff(fromName, toName, from, to string) (string, error) {
	if from == to {
		return "", nil
	}
	rendered, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A: difflib.SplitLines(from), FromFile: fromName,
		B: difflib.SplitLines(to), ToFile: toName,
		Context: 3,
	})
	if err != nil {
		return "", fmt.Errorf("render the pipeline diff: %w", err)
	}
	return rendered, nil
}

// The exported wrappers are what the HTTP layer calls. They erase the concrete
// response types because internal/api cannot import internal/service (service
// imports api), and they are where the mutation gate sits: storing or
// withdrawing a pipeline is a durable change and is refused while the audit
// chain is unanchored, exactly as every other mutation is.

func (service *API) PipelineShow(ctx context.Context, repoName, trigger string) (any, error) {
	return service.pipelineShow(ctx, repoName, trigger)
}

func (service *API) PipelineSet(ctx context.Context, actor, repoName, trigger string,
	document []byte, ref string) (any, error) {
	if err := service.requireMutation(ctx); err != nil {
		return nil, err
	}
	return service.pipelineSet(ctx, actor, repoName, trigger, document, ref)
}

func (service *API) PipelineUnset(ctx context.Context, actor, repoName, trigger string) (any, error) {
	if err := service.requireMutation(ctx); err != nil {
		return nil, err
	}
	return service.pipelineUnset(ctx, actor, repoName, trigger)
}

func (service *API) PipelineCheck(ctx context.Context, actor, repoName, trigger, ref string,
	restore bool) (any, error) {
	if restore {
		if err := service.requireMutation(ctx); err != nil {
			return nil, err
		}
	}
	return service.pipelineCheck(ctx, actor, repoName, trigger, ref, restore)
}
