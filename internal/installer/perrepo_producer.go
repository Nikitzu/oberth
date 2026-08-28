package installer

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

const (
	secretAccessConfigMapName = "oberth-secret-access" // #nosec G101 — Kubernetes ConfigMap NAME (an identifier), not credential material.
	secretAccessConfigMapKey  = "grants"
)

// grantEntry mirrors service.SecretAccessGrantEntry for ConfigMap parsing
// without importing the service package (which would pull in transitive
// dependencies the installer does not need).
type grantEntry struct {
	Repo   string `json:"repo"   yaml:"repo"`
	Step   string `json:"step"   yaml:"step"`
	Secret string `json:"secret" yaml:"secret"`
}

// ProducePerRepoIdentities reads per-repo identity data needed for Vault
// policy provisioning. It first tries to query the running Oberth server's
// database by exec'ing `oberth access list` in the Oberth deployment pod;
// this path returns qualified "upstream/org/repo" names that match the serve
// path's identity map. If exec is unavailable (fresh install, pod not yet
// running, incompatible binary), it falls back to reading the secret-access
// ConfigMap directly — that path only accepts qualified entries because the
// ConfigMap may carry bare-spelled names the installer cannot resolve without
// database access.
//
// The exec path is the primary producer for upgrades where the server is
// already running. The ConfigMap path remains as a fallback and as the
// original producer for deployments whose ConfigMap already carries qualified
// entries (e.g. after the reconciler canonicalizes them).
func ProducePerRepoIdentities(ctx context.Context, kube kubernetes.Interface, run CommandRunner, contextName, namespace string) ([]PerRepoIdentity, error) {
	// Primary: query the running server's database via exec. The database
	// carries qualified repo names (written by the v12 migration and the
	// access reconciler), so the result matches the serve path's
	// buildPerRepoIdentities exactly.
	if run != nil {
		identities, err := produceFromAccessList(ctx, run, contextName, namespace)
		if err == nil {
			return identities, nil
		}
		// exec failed — this is expected on fresh installs where the server
		// pod does not exist yet. Fall through to the ConfigMap path.
	}

	return produceFromConfigMap(ctx, kube, namespace)
}

// produceFromAccessList execs into the running Oberth deployment pod and
// runs `oberth access list` to read per-repo grant data from the server's
// database. The database rows carry qualified "upstream/org/repo" names
// thanks to the v12 schema migration and the access reconciler.
func produceFromAccessList(ctx context.Context, run CommandRunner, contextName, namespace string) ([]PerRepoIdentity, error) {
	args := []string{"exec", "-i", "-c", "oberth"}
	if contextName != "" {
		args = append(args, "--context", contextName)
	}
	args = append(args, "-n", namespace, "deploy/oberth", "--", "oberth", "access", "list")

	out, err := run(ctx, nil, "kubectl", args...)
	if err != nil {
		return nil, fmt.Errorf("exec access list in Oberth pod: %w", err)
	}

	return parseAccessListOutput(out)
}

// accessListSeparator matches the 3+ space padding tabwriter inserts between
// columns. Column values (repo, step, secret) never contain runs of 3+
// spaces, so this is a reliable field boundary.
var accessListSeparator = regexp.MustCompile(`\s{3,}`)

// parseAccessListOutput parses the tabwriter-formatted output of
// `oberth access list` into per-repo identities. Each line has:
//
//	REPO   STEP   SECRET   APPROVED_BY   APPROVED_AT   STATUS
//
// separated by 3+ spaces (tabwriter padding). Only qualified 3-segment
// repo names produce identities; unresolved bare rows in the database
// (possible for repos unregistered at both the v12 migration and the last
// reconcile) are skipped.
func parseAccessListOutput(data []byte) ([]PerRepoIdentity, error) {
	type repoKey struct {
		upstream, org, repo string
	}
	byRepo := make(map[repoKey][]string)

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "REPO") {
			continue // skip header and blank lines
		}

		fields := accessListSeparator.Split(line, -1)
		if len(fields) < 3 {
			continue
		}

		repo := strings.TrimSpace(fields[0])
		secret := strings.TrimSpace(fields[2])

		parts := strings.Split(repo, "/")
		if len(parts) != 3 {
			continue // skip bare names that the DB hasn't qualified yet
		}

		key := repoKey{upstream: parts[0], org: parts[1], repo: parts[2]}
		byRepo[key] = append(byRepo[key], secret)
	}

	result := make([]PerRepoIdentity, 0, len(byRepo))
	for key, grants := range byRepo {
		sort.Strings(grants)
		result = append(result, PerRepoIdentity{
			Upstream: key.upstream,
			Org:      key.org,
			Repo:     key.repo,
			Grants:   grants,
		})
	}
	return result, nil
}

// produceFromConfigMap reads the secret-access ConfigMap and builds per-repo
// identities from entries whose repo field is already in qualified
// "upstream/org/repo" format. Bare-name entries are skipped because the
// ConfigMap may not have been canonicalized yet and the installer has no
// database access to resolve them.
func produceFromConfigMap(ctx context.Context, kube kubernetes.Interface, namespace string) ([]PerRepoIdentity, error) {
	cm, err := kube.CoreV1().ConfigMaps(namespace).Get(ctx, secretAccessConfigMapName, metav1.GetOptions{})
	if err != nil {
		// ConfigMap not found or inaccessible: no per-repo identities.
		// This is normal for fresh installs where no grants exist yet.
		return nil, nil //nolint:nilerr // absent ConfigMap means zero grants
	}

	data := cm.Data[secretAccessConfigMapKey]
	if strings.TrimSpace(data) == "" {
		return nil, nil
	}

	var entries []grantEntry
	if err := yaml.Unmarshal([]byte(data), &entries); err != nil {
		return nil, fmt.Errorf("parse secret access ConfigMap: %w", err)
	}

	// Group grant secrets by qualified repo key.
	type repoKey struct {
		upstream, org, repo string
	}
	type repoGrants struct {
		key    repoKey
		grants []string
	}
	byRepo := make(map[string]*repoGrants)

	for _, entry := range entries {
		parts := strings.Split(entry.Repo, "/")
		if len(parts) != 3 {
			// Skip bare or org/repo entries — they can't be unambiguously
			// resolved without database access.
			continue
		}
		upstream, org, repo := parts[0], parts[1], parts[2]
		mapKey := entry.Repo

		rg, exists := byRepo[mapKey]
		if !exists {
			rg = &repoGrants{key: repoKey{upstream: upstream, org: org, repo: repo}}
			byRepo[mapKey] = rg
		}
		rg.grants = append(rg.grants, entry.Secret)
	}

	var result []PerRepoIdentity
	for _, rg := range byRepo {
		result = append(result, PerRepoIdentity{
			Upstream: rg.key.upstream,
			Org:      rg.key.org,
			Repo:     rg.key.repo,
			Grants:   rg.grants,
		})
	}
	return result, nil
}
