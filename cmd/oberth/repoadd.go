package main

import (
	"context"
	"fmt"
	"io"

	"github.com/oberthci/oberth/internal/client"
)

// remoteRepoRegistration is the server's answer to POST /api/repos.
type remoteRepoRegistration struct {
	Repository      string `json:"repository"`
	Upstream        string `json:"upstream"`
	UpstreamURL     string `json:"upstream_url"`
	Org             string `json:"org"`
	DefaultBranch   string `json:"default_branch"`
	Created         bool   `json:"created"`
	BranchCorrected bool   `json:"branch_corrected"`
	PreviousBranch  string `json:"previous_branch"`
	BranchSource    string `json:"branch_source"`
}

// registerRepository maps a repository onto an upstream over the API.
//
// The in-pod verb opens the SQLite file directly and therefore only works
// inside the server pod. This is the same operation for everyone else, which
// is what removes the `kubectl exec` step from onboarding.
func registerRepository(ctx context.Context, api *client.Client,
	repo, upstream string) (remoteRepoRegistration, error) {
	var registered remoteRepoRegistration
	body := map[string]string{"repo": repo, "upstream": upstream}
	if err := api.Send(ctx, "POST", "/api/repos", nil, body, &registered); err != nil {
		return remoteRepoRegistration{}, err
	}
	return registered, nil
}

// printRegistration reports what the server did, distinguishing a fresh
// registration from a repeat so a second run reads as "already so".
func printRegistration(output io.Writer, registered remoteRepoRegistration) error {
	switch {
	case registered.Created:
		if _, err := fmt.Fprintf(output, "registered %s -> upstream %s (%s)\n",
			registered.Repository, registered.Upstream, registered.UpstreamURL); err != nil {
			return err
		}
	case registered.BranchCorrected:
		if _, err := fmt.Fprintf(output, "%s was already registered; its default branch was %s and is now %s\n",
			registered.Repository, registered.PreviousBranch, registered.DefaultBranch); err != nil {
			return err
		}
	default:
		if _, err := fmt.Fprintf(output, "%s is already registered against upstream %s\n",
			registered.Repository, registered.Upstream); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(output, "  default branch %s, from %s\n",
		registered.DefaultBranch, registered.BranchSource)
	return err
}

// runRepoAddRemote is `oberth repo add` for everyone outside the server pod.
func runRepoAddRemote(ctx context.Context, repo, upstream string, output io.Writer) error {
	api, err := remoteClient(ctx)
	if err != nil {
		return err
	}
	reportMode("server")
	registered, err := registerRepository(ctx, api, repo, upstream)
	if err != nil {
		return err
	}
	return printRegistration(output, registered)
}

// waitTimeoutForOnboard keeps the onboard default and the wait default in one
// place, so they cannot drift apart.
const waitTimeoutForOnboard = defaultWaitTimeout

