package installer

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ensureLocalImageInNode puts a digest-pinned image into the kind node when the
// node does not already have it.
//
// --chart with --image skipped image preparation entirely, reasoning that
// --image names an image the node already has. That holds when someone loaded
// it beforehand, and cannot hold on the path this installer creates: it is what
// makes the kind cluster, so at the only moment anyone could have loaded an
// image there was no node to load it into. The result was a Pod in
// ImagePullBackOff against a registry that was never meant to serve it, which
// reads as a broken image reference rather than as a missing step.
//
// Best-effort by design. When the image is not in the local daemon either, the
// operator meant a registry the node can reach, and the kubelet's pull is the
// right behaviour. Every failure here leaves that pull to happen.
func ensureLocalImageInNode(ctx context.Context, deps Deps, pinned string) error {
	cluster := strings.TrimSpace(deps.KindClusterName)
	if cluster == "" || strings.TrimSpace(pinned) == "" {
		return nil
	}
	run := deps.RunCommand
	if run == nil {
		run = DefaultRunCommand
	}

	nodes, err := kindNodeNames(ctx, run, cluster)
	if err != nil || len(nodes) == 0 {
		return nil
	}
	// Ask the node first. A repeat install must not pay for a load it does not
	// need, and on a rerun this is the common case.
	if _, checkErr := run(ctx, nil, "docker", "exec", nodes[0],
		"ctr", "--namespace=k8s.io", "images", "check", "name=="+pinned); checkErr == nil {
		return nil
	}

	local, err := localTagForDigest(ctx, run, pinned)
	if err != nil {
		return nil // not in this daemon: the operator meant a registry pull
	}

	if output, loadErr := run(ctx, nil, "kind", "load", "docker-image", "--name", cluster, local); loadErr != nil {
		return fmt.Errorf("load %s into kind cluster %q: %w%s", local, cluster, loadErr, commandOutputSuffix(output))
	}
	for _, node := range nodes {
		if output, tagErr := run(ctx, nil, "docker", "exec", node,
			"ctr", "--namespace=k8s.io", "images", "tag", "--force", local, pinned); tagErr != nil {
			return fmt.Errorf("alias %s on kind node %s: %w%s", pinned, node, tagErr, commandOutputSuffix(output))
		}
	}
	if deps.Output != nil {
		_, _ = fmt.Fprintf(deps.Output, "Loaded %s into the kind node from the local daemon.\n", pinned)
	}
	return nil
}

func kindNodeNames(ctx context.Context, run CommandRunner, cluster string) ([]string, error) {
	output, err := run(ctx, nil, "kind", "get", "nodes", "--name", cluster)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(output)), nil
}

// localTagForDigest finds a tag in the local daemon whose manifest digest is the
// pinned one. kind load takes a name the daemon knows, and a digest ref is not
// one of those.
func localTagForDigest(ctx context.Context, run CommandRunner, pinned string) (string, error) {
	_, digest, ok := splitImageDigestRef(pinned)
	if !ok {
		return "", errors.New("not a digest ref")
	}
	output, err := run(ctx, nil, "docker", "image", "inspect", pinned, "--format", "{{index .RepoTags 0}}")
	if err == nil {
		if tag := strings.TrimSpace(string(output)); tag != "" && tag != "<no value>" {
			return tag, nil
		}
	}
	// Fall back to the repository the digest names, which is how the image was
	// tagged when it was pushed to obtain that digest.
	repository := imageRepository(pinned)
	output, err = run(ctx, nil, "docker", "image", "inspect", repository+"@"+digest, "--format", "{{index .RepoTags 0}}")
	if err != nil {
		return "", err
	}
	tag := strings.TrimSpace(string(output))
	if tag == "" || tag == "<no value>" {
		return "", errors.New("no local tag for that digest")
	}
	return tag, nil
}
