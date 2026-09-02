package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/oberthci/oberth/internal/gitcache"
	"github.com/oberthci/oberth/internal/model"
	"github.com/oberthci/oberth/internal/store"
)

// Registering a repository used to be an in-pod administrative verb: an
// operator ran `kubectl exec` into the server pod so a CLI in there could open
// the SQLite file directly. That is the single largest reason onboarding was
// not one line, and it is the reason `oberth onboard` exists at all.
//
// This is the same operation over the API, authorized the way the pipeline
// endpoints are. The in-pod path stays: a deployment whose API is unreachable
// still has a way in.

// RepositoryRegistrar is the durable half of registration. The store
// satisfies it directly.
type RepositoryRegistrar interface {
	RepositoryByName(context.Context, string) (model.Repository, error)
	CreateRepository(context.Context, model.RepositorySpec) (model.Repository, error)
	SetRepositoryDefaultBranch(context.Context, int64, string) (model.Repository, error)
}

// UpstreamCatalog lists the upstreams a repository can be mapped onto.
type UpstreamCatalog interface {
	ListUpstreams(context.Context) ([]model.Upstream, error)
}

// UpstreamProbe asks a forge which branch it advertises as HEAD, without a
// mirror. The git cache satisfies it.
type UpstreamProbe interface {
	LsRemoteDefaultBranch(context.Context, string) (string, error)
}

// RepoRegisterResponse is what a caller gets back. It reports what it found as
// well as what it did, because the whole point of an idempotent verb is that
// the second call can say "already so" rather than fail.
type RepoRegisterResponse struct {
	Repository    string `json:"repository"`
	Upstream      string `json:"upstream"`
	UpstreamURL   string `json:"upstream_url"`
	Org           string `json:"org"`
	DefaultBranch string `json:"default_branch"`
	// Created is false when the repository was already registered.
	Created bool `json:"created"`
	// BranchCorrected reports that an existing registration named a branch the
	// upstream does not have, and that it was updated to the advertised one.
	BranchCorrected bool `json:"branch_corrected"`
	// PreviousBranch is what it used to say, when it was corrected.
	PreviousBranch string `json:"previous_branch"`
	// BranchSource says where the default branch came from, so a reader can
	// tell an advertisement from a fallback.
	BranchSource string `json:"branch_source"`
}

// repoRegister maps a repository name onto a configured upstream, reading the
// default branch from the upstream itself.
//
// Idempotent at every step, because `oberth onboard` runs it on every
// invocation and the second invocation has to be a report rather than a
// failure.
func (service *API) repoRegister(ctx context.Context, actor, repoName, upstreamName string) (RepoRegisterResponse, error) {
	if service.repositoryRegistrar == nil || service.upstreamCatalog == nil {
		return RepoRegisterResponse{}, fmt.Errorf("%w: repository registration", ErrUnavailable)
	}
	name, err := gitcache.NormalizeRepo(strings.TrimSpace(repoName))
	if err != nil {
		return RepoRegisterResponse{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	upstream, err := service.selectUpstream(ctx, upstreamName)
	if err != nil {
		return RepoRegisterResponse{}, err
	}

	response := RepoRegisterResponse{
		Repository: name, Upstream: upstream.Name, UpstreamURL: upstream.BaseURL, Org: upstream.Org(),
	}

	existing, lookupErr := service.repositoryRegistrar.RepositoryByName(ctx, name)
	switch {
	case lookupErr == nil:
		if existing.UpstreamID != upstream.ID {
			// Remapping is not supported, and quietly accepting it would move
			// a repository's identity under a different org's secret subtree.
			return RepoRegisterResponse{}, fmt.Errorf(
				"%w: %s is already registered against a different upstream (id %d); remapping is not supported",
				ErrInvalidInput, name, existing.UpstreamID)
		}
		response.DefaultBranch = existing.DefaultBranch
		response.BranchSource = "the existing registration"
		return service.reconcileDefaultBranch(ctx, actor, existing, response)
	case errors.Is(lookupErr, store.ErrNotFound):
	default:
		return RepoRegisterResponse{}, lookupErr
	}

	branch, source := service.advertisedDefaultBranch(ctx, name)
	created, err := service.repositoryRegistrar.CreateRepository(ctx, model.RepositorySpec{
		Name: name, UpstreamID: upstream.ID, DefaultBranch: branch,
	})
	if err != nil {
		return RepoRegisterResponse{}, err
	}
	response.Created = true
	response.DefaultBranch = created.DefaultBranch
	response.BranchSource = source
	service.auditRepo(ctx, actor, "repository.register", created,
		fmt.Sprintf(`{"upstream":%q,"default_branch":%q,"branch_source":%q}`,
			upstream.Name, created.DefaultBranch, source))
	return response, nil
}

// reconcileDefaultBranch corrects a registration that names a branch the
// upstream does not have.
//
// This is what makes the fix reach repositories that were registered before
// the probe existed. Without it, a repository seeded with "main" against a
// "master" upstream stays broken forever, and the branch-mismatch error
// promises that re-running onboard fixes it.
func (service *API) reconcileDefaultBranch(ctx context.Context, actor string,
	existing model.Repository, response RepoRegisterResponse) (RepoRegisterResponse, error) {
	advertised, source := service.advertisedDefaultBranch(ctx, existing.Name)
	if advertised == "" || advertised == existing.DefaultBranch {
		return response, nil
	}
	updated, err := service.repositoryRegistrar.SetRepositoryDefaultBranch(ctx, existing.ID, advertised)
	if err != nil {
		// A correction that cannot be written is not a reason to fail a verb
		// whose main job already succeeded. The response still reports the
		// branch the registration carries, which is the truth.
		return response, nil
	}
	response.BranchCorrected = true
	response.PreviousBranch = existing.DefaultBranch
	response.DefaultBranch = updated.DefaultBranch
	response.BranchSource = source
	service.auditRepo(ctx, actor, "repository.default_branch", updated,
		fmt.Sprintf(`{"from":%q,"to":%q,"branch_source":%q}`,
			existing.DefaultBranch, updated.DefaultBranch, source))
	return response, nil
}

// advertisedDefaultBranch asks the upstream, and falls back to main only when
// there is nothing to ask.
//
// The fallback is reported as a fallback. The old behaviour was this fallback
// with nothing said about it, which is how a repository on master came to be
// registered as being on main.
func (service *API) advertisedDefaultBranch(ctx context.Context, name string) (branch, source string) {
	if service.upstreamProbe == nil {
		return "main", "a fallback: this deployment cannot probe upstreams"
	}
	advertised, err := service.upstreamProbe.LsRemoteDefaultBranch(ctx, name)
	if err != nil || strings.TrimSpace(advertised) == "" {
		return "main", "a fallback: the upstream advertised no default branch"
	}
	return advertised, "the upstream's own HEAD advertisement"
}

// selectUpstream resolves the upstream by name, or picks the only one when the
// caller named none. A deployment with one upstream is the common case and
// making the caller name it is a step that carries no decision.
func (service *API) selectUpstream(ctx context.Context, named string) (model.Upstream, error) {
	upstreams, err := service.upstreamCatalog.ListUpstreams(ctx)
	if err != nil {
		return model.Upstream{}, err
	}
	if len(upstreams) == 0 {
		return model.Upstream{}, fmt.Errorf("%w: no upstream is registered on this server", ErrInvalidInput)
	}
	named = strings.TrimSpace(named)
	if named == "" {
		if len(upstreams) > 1 {
			names := make([]string, 0, len(upstreams))
			for _, candidate := range upstreams {
				names = append(names, candidate.Name)
			}
			return model.Upstream{}, fmt.Errorf(
				"%w: this server has %d upstreams registered (%s); say which one",
				ErrInvalidInput, len(upstreams), strings.Join(names, ", "))
		}
		return upstreams[0], nil
	}
	names := make([]string, 0, len(upstreams))
	for _, candidate := range upstreams {
		names = append(names, candidate.Name)
		if candidate.Name == named {
			return candidate, nil
		}
	}
	return model.Upstream{}, fmt.Errorf("%w: upstream %q is not registered (configured: %s)",
		ErrInvalidInput, named, strings.Join(names, ", "))
}

func (service *API) auditRepo(ctx context.Context, actor, action string, repository model.Repository, details string) {
	if service.auditor == nil {
		return
	}
	_, _ = service.auditor.AppendAuditAction(ctx, model.AuditActionSpec{
		Actor: actor, Action: action, ResourceType: "repository",
		ResourceID: fmt.Sprint(repository.ID), Details: details,
	})
}

// RepoRegister is what the HTTP layer calls. Registration is a durable change,
// so it meets the same mutation gate every other mutation meets.
func (service *API) RepoRegister(ctx context.Context, actor, repoName, upstreamName string) (any, error) {
	if err := service.requireMutation(ctx); err != nil {
		return nil, err
	}
	return service.repoRegister(ctx, actor, repoName, upstreamName)
}
