// Package app contains the small adapters that compose Oberth's server
// primitives into one process.
package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/oberthci/oberth/internal/gitcache"
	"github.com/oberthci/oberth/internal/model"
	"github.com/oberthci/oberth/internal/store"
)

const defaultCatalogTimeout = 5 * time.Second

type UpstreamCatalog interface {
	RepositoryByName(context.Context, string) (model.Repository, error)
	Upstream(context.Context, int64) (model.Upstream, error)
	ListUpstreams(context.Context) ([]model.Upstream, error)
}

// Upstreams maps client-owned repository names onto an operator-owned Forge
// base URL. An unknown repository can be discovered only when one upstream is
// configured; with multiple upstreams the mapping must already be durable.
type Upstreams struct {
	Catalog UpstreamCatalog
	Timeout time.Duration
}

// Remote resolves a repository input (bare name or org-qualified "org/repo")
// to the full upstream Git remote URL. When an org prefix is provided, it is
// matched against the last path component of each upstream's base URL.
func (upstreams Upstreams) Remote(input string) (string, error) {
	if upstreams.Catalog == nil {
		return "", errors.New("app: upstream catalog is required")
	}
	org, repositoryName, err := gitcache.ParseRepoPath(input)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), upstreams.timeout())
	defer cancel()
	upstream, err := upstreams.selectUpstream(ctx, org, repositoryName)
	if err != nil {
		return "", err
	}
	remote := joinUpstream(upstream.BaseURL, repositoryName)
	if err := gitcache.ValidateUpstream(remote); err != nil {
		return "", fmt.Errorf("app: resolve upstream %s: %w", upstream.Name, err)
	}
	return remote, nil
}

// DiscoverRepository resolves a repository input (bare name or org-qualified)
// to a RepositorySpec suitable for initial catalog registration.
func (upstreams Upstreams) DiscoverRepository(ctx context.Context, input string) (model.RepositorySpec, error) {
	if upstreams.Catalog == nil {
		return model.RepositorySpec{}, errors.New("app: upstream catalog is required")
	}
	org, repositoryName, err := gitcache.ParseRepoPath(input)
	if err != nil {
		return model.RepositorySpec{}, err
	}
	upstream, err := upstreams.selectUpstream(ctx, org, repositoryName)
	if err != nil {
		return model.RepositorySpec{}, err
	}
	return model.RepositorySpec{Name: repositoryName, UpstreamID: upstream.ID}, nil
}

func (upstreams Upstreams) selectUpstream(ctx context.Context, org, repositoryName string) (model.Upstream, error) {
	repository, err := upstreams.Catalog.RepositoryByName(ctx, repositoryName)
	if err == nil {
		upstream, lookupErr := upstreams.Catalog.Upstream(ctx, repository.UpstreamID)
		if lookupErr != nil {
			return model.Upstream{}, fmt.Errorf("app: load repository upstream: %w", lookupErr)
		}
		if org != "" && !upstreamMatchesOrg(upstream, org) {
			return model.Upstream{}, fmt.Errorf("app: repository %s is registered under upstream %q, not %q", repositoryName, upstream.Name, org)
		}
		return upstream, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return model.Upstream{}, fmt.Errorf("app: look up repository mapping: %w", err)
	}
	values, err := upstreams.Catalog.ListUpstreams(ctx)
	if err != nil {
		return model.Upstream{}, fmt.Errorf("app: list upstreams: %w", err)
	}
	if len(values) == 0 {
		return model.Upstream{}, errors.New("app: no upstream is configured")
	}

	// When an org prefix is provided, match it against upstream base URLs.
	// Collect all matches; more than one is an ambiguity error.
	if org != "" {
		var matched []model.Upstream
		for _, u := range values {
			if upstreamMatchesOrg(u, org) {
				matched = append(matched, u)
			}
		}
		switch len(matched) {
		case 0:
			return model.Upstream{}, fmt.Errorf("app: no upstream registered for %q; available: %s", org, formatUpstreams(values))
		case 1:
			return matched[0], nil
		default:
			names := make([]string, len(matched))
			for i, u := range matched {
				names[i] = u.Name
			}
			return model.Upstream{}, fmt.Errorf("app: org %q matches multiple upstreams: %s", org, strings.Join(names, ", "))
		}
	}

	// No org — require exactly one upstream for implicit discovery.
	if len(values) != 1 {
		return model.Upstream{}, fmt.Errorf("app: repository %s has no mapping and %d upstreams are configured; use org/repo format (available: %s)", repositoryName, len(values), formatUpstreams(values))
	}
	return values[0], nil
}

// upstreamMatchesOrg checks whether an upstream's org identity matches the
// given org name. For example, a base URL of "ssh://git@github.com/oberthci"
// matches org "oberthci". Clone-path matching stays case-insensitive for
// operator convenience; the registered spelling from model.Upstream.Org is
// still the single canonical identity (secret path scoping matches it
// exactly).
func upstreamMatchesOrg(upstream model.Upstream, org string) bool {
	registered := upstream.Org()
	return registered != "" && strings.EqualFold(registered, org)
}

// formatUpstreams produces a human-readable list of available upstreams with
// their org identifiers for error messages.
func formatUpstreams(values []model.Upstream) string {
	parts := make([]string, len(values))
	for i, u := range values {
		org := extractOrgFromBase(u.BaseURL)
		if u.Name != "" {
			parts[i] = fmt.Sprintf("%s (%s)", org, u.Name)
		} else {
			parts[i] = org
		}
	}
	return strings.Join(parts, ", ")
}

func extractOrgFromBase(baseURL string) string {
	org := model.Upstream{BaseURL: baseURL}.Org()
	if org == "" {
		return "unknown"
	}
	return org
}

func (upstreams Upstreams) timeout() time.Duration {
	if upstreams.Timeout <= 0 {
		return defaultCatalogTimeout
	}
	return upstreams.Timeout
}

func joinUpstream(baseURL, repositoryName string) string {
	baseURL = strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if filepath.IsAbs(baseURL) {
		return filepath.Join(baseURL, repositoryName+".git")
	}
	return baseURL + "/" + repositoryName + ".git"
}

func ValidateUpstreamBase(baseURL string) error {
	baseURL = strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if baseURL == "" || strings.HasSuffix(strings.ToLower(baseURL), ".git") {
		return errors.New("app: upstream base must identify a repository namespace, not one repository")
	}
	if err := gitcache.ValidateUpstream(joinUpstream(baseURL, "oberth-probe")); err != nil {
		return fmt.Errorf("app: invalid upstream base: %w", err)
	}
	return nil
}

func UpstreamKind(baseURL string) (string, error) {
	if err := ValidateUpstreamBase(baseURL); err != nil {
		return "", err
	}
	if filepath.IsAbs(strings.TrimSpace(baseURL)) {
		return "local", nil
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("app: parse upstream base: %w", err)
	}
	return parsed.Scheme, nil
}
