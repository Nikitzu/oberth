package installer

import (
	"context"
	"fmt"
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

// ProducePerRepoIdentities reads the secret-access ConfigMap from the
// server namespace and builds the per-repo identity slice for the
// installer. Only grants whose repo field is in qualified
// "upstream/org/repo" format (3 segments) produce identities; bare-name
// entries are skipped because the installer has no database access to
// resolve them.
//
// This is the installer-path producer for issue #246 phase 2. The serve
// path has its own producer that reads from the database directly.
func ProducePerRepoIdentities(ctx context.Context, kube kubernetes.Interface, namespace string) ([]PerRepoIdentity, error) {
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
