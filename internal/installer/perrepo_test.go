package installer

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPerRepoNameIsDeterministic(t *testing.T) {
	t.Parallel()
	a := PerRepoName("codeberg", "oberthci", "oberth")
	b := PerRepoName("codeberg", "oberthci", "oberth")
	if a != b {
		t.Fatalf("PerRepoName not deterministic: %q != %q", a, b)
	}
}

func TestPerRepoNameDiffersForDifferentRepos(t *testing.T) {
	t.Parallel()
	a := PerRepoName("codeberg", "oberthci", "oberth")
	b := PerRepoName("codeberg", "oberthci", "cloudtaser-operator")
	if a == b {
		t.Fatalf("different repos produced the same name: %q", a)
	}
}

func TestPerRepoNameDiffersForDifferentUpstreams(t *testing.T) {
	t.Parallel()
	a := PerRepoName("codeberg", "oberthci", "oberth")
	b := PerRepoName("github", "oberthci", "oberth")
	if a == b {
		t.Fatalf("different upstreams produced the same name: %q", a)
	}
}

func TestPerRepoNameDiffersForDifferentOrgs(t *testing.T) {
	t.Parallel()
	a := PerRepoName("codeberg", "oberthci", "oberth")
	b := PerRepoName("codeberg", "skipops", "oberth")
	if a == b {
		t.Fatalf("different orgs produced the same name: %q", a)
	}
}

func TestPerRepoNameIsDNS1123Safe(t *testing.T) {
	t.Parallel()
	cases := []struct {
		upstream, org, repo string
	}{
		{"codeberg", "oberthci", "oberth"},
		{"github", "skipops", "cloudtaser-operator"},
		{"my.upstream", "my.org", "my.repo"},
		{"UPPER", "CASE", "NAMES"},
		{"a", "b", "c"},
	}
	for _, tc := range cases {
		name := PerRepoName(tc.upstream, tc.org, tc.repo)
		if len(name) > maxPerRepoNameLength {
			t.Errorf("PerRepoName(%q,%q,%q) = %q (%d chars), exceeds limit %d",
				tc.upstream, tc.org, tc.repo, name, len(name), maxPerRepoNameLength)
		}
		// Must start and end with alphanumeric
		if name[0] < 'a' || name[0] > 'z' {
			t.Errorf("PerRepoName(%q,%q,%q) = %q, does not start with lowercase letter",
				tc.upstream, tc.org, tc.repo, name)
		}
		lastChar := name[len(name)-1]
		if (lastChar < 'a' || lastChar > 'z') && (lastChar < '0' || lastChar > '9') {
			t.Errorf("PerRepoName(%q,%q,%q) = %q, does not end with alphanumeric",
				tc.upstream, tc.org, tc.repo, name)
		}
		// Must contain only lowercase alphanumeric and hyphens
		for _, c := range name {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				t.Errorf("PerRepoName(%q,%q,%q) = %q, contains invalid character %q",
					tc.upstream, tc.org, tc.repo, name, string(c))
			}
		}
		// Must have the prefix
		if !strings.HasPrefix(name, perRepoNamePrefix) {
			t.Errorf("PerRepoName(%q,%q,%q) = %q, missing prefix %q",
				tc.upstream, tc.org, tc.repo, name, perRepoNamePrefix)
		}
	}
}

func TestPerRepoNameTruncatesLongNames(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 100)
	name := PerRepoName(long, long, long)
	if len(name) > maxPerRepoNameLength {
		t.Fatalf("PerRepoName with long inputs = %d chars, exceeds limit %d", len(name), maxPerRepoNameLength)
	}
}

func TestPerRepoPolicyContainsRepoScopedPath(t *testing.T) {
	t.Parallel()
	policy := PerRepoPolicy("oberth", "oberthci", "oberth", nil)
	if !strings.Contains(policy, `path "oberth/data/upstream/oberthci/oberth/*"`) {
		t.Fatalf("policy missing repo-scoped path:\n%s", policy)
	}
}

// A release run may declare an org-scoped secret, oberth/upstream/<org>/<secret>,
// and this fork's admission authorizes it structurally without an
// approval-table row (ce51495, PR #27). The per-repo policy has to serve what
// admission admits: without the org-level rule the run passes admission and
// then fails the Vault read with a 403.
func TestPerRepoPolicyServesAnOrgScopedPath(t *testing.T) {
	t.Parallel()
	policy := PerRepoPolicy("oberth", "oberthci", "oberth", nil)
	if !strings.Contains(policy, `path "oberth/data/upstream/oberthci/*"`) {
		t.Fatalf("policy missing org-scoped path, an org-scoped declaration would 403:\n%s", policy)
	}
}

// The org wildcard must stop at this repository's own org. Reading another
// org's subtree is what the shared credentialed policy allowed and what the
// per-repo identity exists to prevent.
func TestPerRepoPolicyDoesNotReachAnotherOrg(t *testing.T) {
	t.Parallel()
	policy := PerRepoPolicy("oberth", "oberthci", "oberth", nil)
	if strings.Contains(policy, `path "oberth/data/upstream/other-org`) {
		t.Fatalf("per-repo policy reaches another org:\n%s", policy)
	}
	if !strings.HasPrefix(strings.TrimSpace(orgRuleOf(t, policy)), `path "oberth/data/upstream/oberthci/*"`) {
		t.Fatalf("org rule is not scoped to this repository's org:\n%s", policy)
	}
}

// orgRuleOf returns the first path rule in the policy, which is the org rule.
func orgRuleOf(t *testing.T, policy string) string {
	t.Helper()
	index := strings.Index(policy, `path "`)
	if index < 0 {
		t.Fatalf("policy declares no path rule:\n%s", policy)
	}
	return policy[index:]
}

func TestPerRepoPolicyDoesNotContainAllUpstreamsWildcard(t *testing.T) {
	t.Parallel()
	policy := PerRepoPolicy("oberth", "oberthci", "oberth", nil)
	if strings.Contains(policy, `path "oberth/data/upstream/*"`) {
		t.Fatalf("per-repo policy must not contain the all-upstreams wildcard:\n%s", policy)
	}
}

func TestPerRepoPolicyContainsGrantPaths(t *testing.T) {
	t.Parallel()
	policy := PerRepoPolicy("oberth", "oberthci", "oberth", []string{"release/cosign-secret", "release/r2-token"})
	if !strings.Contains(policy, `path "oberth/data/release/cosign-secret"`) {
		t.Fatalf("policy missing grant path for cosign-secret:\n%s", policy)
	}
	if !strings.Contains(policy, `path "oberth/data/release/r2-token"`) {
		t.Fatalf("policy missing grant path for r2-token:\n%s", policy)
	}
}

func TestPerRepoPolicyDeduplicatesGrants(t *testing.T) {
	t.Parallel()
	policy := PerRepoPolicy("oberth", "oberthci", "oberth", []string{"release/cosign-secret", "release/cosign-secret"})
	count := strings.Count(policy, `path "oberth/data/release/cosign-secret"`)
	if count != 1 {
		t.Fatalf("duplicate grant path appeared %d times, want 1:\n%s", count, policy)
	}
}

func TestPerRepoPolicyContainsRevokeSelf(t *testing.T) {
	t.Parallel()
	policy := PerRepoPolicy("oberth", "oberthci", "oberth", nil)
	if !strings.Contains(policy, `path "auth/token/revoke-self"`) {
		t.Fatalf("policy missing revoke-self:\n%s", policy)
	}
}

// Despite the name, every assertion here is about cross-ORG reach, and that is
// what it still guarantees. Cross-repo reach WITHIN the repository's own org is
// deliberately allowed on this fork, because admission authorizes org-scoped
// paths structurally; see PerRepoPolicy's doc comment and
// TestPerRepoPolicyServesAnOrgScopedPath.
func TestPerRepoPolicyCrossRepoReadRefused(t *testing.T) {
	t.Parallel()
	// A per-repo policy for org "oberthci" must not grant access to org "skipops"
	policy := PerRepoPolicy("oberth", "oberthci", "oberth", nil)
	if strings.Contains(policy, "skipops") {
		t.Fatalf("per-repo policy for oberthci must not reference skipops:\n%s", policy)
	}
	// The policy must not have the all-orgs wildcard
	if strings.Contains(policy, `"oberth/data/upstream/*"`) {
		t.Fatalf("per-repo policy must not contain cross-org wildcard:\n%s", policy)
	}
}

func TestPerRepoRoleMatchesCorrectShape(t *testing.T) {
	t.Parallel()
	role := map[string]any{
		"bound_service_account_names":      []any{"oberth-argo-codeberg-oberthci-oberth-abcdef012345"},
		"bound_service_account_namespaces": []any{"oberth-argo"},
		"token_policies":                   []any{"oberth-argo-codeberg-oberthci-oberth-abcdef012345"},
		"token_no_default_policy":          true,
		"token_ttl":                        float64(1200),
		"token_max_ttl":                    float64(1800),
	}
	if !perRepoRoleMatches(role, "oberth-argo-codeberg-oberthci-oberth-abcdef012345", "oberth-argo-codeberg-oberthci-oberth-abcdef012345", "oberth-argo") {
		t.Fatal("expected role to match")
	}
}

func TestPerRepoRoleRejectsWrongSA(t *testing.T) {
	t.Parallel()
	role := map[string]any{
		"bound_service_account_names":      []any{"wrong-sa"},
		"bound_service_account_namespaces": []any{"oberth-argo"},
		"token_policies":                   []any{"oberth-argo-codeberg-oberthci-oberth-abcdef012345"},
		"token_no_default_policy":          true,
		"token_ttl":                        float64(1200),
		"token_max_ttl":                    float64(1800),
	}
	if perRepoRoleMatches(role, "oberth-argo-codeberg-oberthci-oberth-abcdef012345", "oberth-argo-codeberg-oberthci-oberth-abcdef012345", "oberth-argo") {
		t.Fatal("role with wrong SA should not match")
	}
}

func TestConfigurePerRepoIdentitiesCreatesNewPolicyAndRole(t *testing.T) {
	t.Parallel()

	name := PerRepoName("codeberg", "oberthci", "oberth")

	responses := map[string]fakeBaoResponse{
		"policy read " + name:                            {out: "No policy named: " + name, err: errors.New("exit status 2")},
		"policy write " + name + " -":                    {out: "Success!"},
		"read -format=json auth/kubernetes/role/" + name: {out: "No value found at auth/kubernetes/role/" + name, err: errors.New("exit status 2")},
		"write auth/kubernetes/role/" + name + " -":      {out: "Success!"},
	}
	runner := &fakeBaoRunner{t: t, responses: responses}
	store := openBaoExec{run: runner.run, namespace: "openbao", pod: "openbao-0"}

	identities := []PerRepoIdentity{{
		Upstream: "codeberg",
		Org:      "oberthci",
		Repo:     "oberth",
	}}

	items, err := ConfigurePerRepoIdentities(context.Background(), store, "root", identities, "oberth-argo")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items (policy + role), got %d", len(items))
	}

	// Verify the policy write was called
	byCommand := runner.callsByCommand()
	if _, ok := byCommand["policy write "+name+" -"]; !ok {
		t.Fatal("expected policy write call")
	}
	if _, ok := byCommand["write auth/kubernetes/role/"+name+" -"]; !ok {
		t.Fatal("expected role write call")
	}
}

func TestConfigurePerRepoIdentitiesSkipsExistingMatchingRole(t *testing.T) {
	t.Parallel()

	name := PerRepoName("codeberg", "oberthci", "oberth")
	wantPolicy := PerRepoPolicy(defaultKVPrefix, "oberthci", "oberth", nil)

	responses := map[string]fakeBaoResponse{
		"policy read " + name: {out: wantPolicy},
		"read -format=json auth/kubernetes/role/" + name: {out: `{"request_id":"1","data":{` +
			`"bound_service_account_names":["` + name + `"],` +
			`"bound_service_account_namespaces":["oberth-argo"],` +
			`"token_policies":["` + name + `"],` +
			`"token_no_default_policy":true,` +
			`"token_ttl":1200,` +
			`"token_max_ttl":1800}}`},
	}
	runner := &fakeBaoRunner{t: t, responses: responses}
	store := openBaoExec{run: runner.run, namespace: "openbao", pod: "openbao-0"}

	identities := []PerRepoIdentity{{
		Upstream: "codeberg",
		Org:      "oberthci",
		Repo:     "oberth",
	}}

	items, err := ConfigurePerRepoIdentities(context.Background(), store, "root", identities, "oberth-argo")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	// No write calls should have been made
	byCommand := runner.callsByCommand()
	for cmd := range byCommand {
		if strings.HasPrefix(cmd, "policy write") || strings.HasPrefix(cmd, "write auth/") {
			t.Fatalf("unexpected write call: %s", cmd)
		}
	}
}

func TestConfigurePerRepoIdentitiesWithGrants(t *testing.T) {
	t.Parallel()

	name := PerRepoName("codeberg", "oberthci", "oberth")

	responses := map[string]fakeBaoResponse{
		"policy read " + name:                            {out: "No policy named: " + name, err: errors.New("exit status 2")},
		"policy write " + name + " -":                    {out: "Success!"},
		"read -format=json auth/kubernetes/role/" + name: {out: "No value found at auth/kubernetes/role/" + name, err: errors.New("exit status 2")},
		"write auth/kubernetes/role/" + name + " -":      {out: "Success!"},
	}
	runner := &fakeBaoRunner{t: t, responses: responses}
	store := openBaoExec{run: runner.run, namespace: "openbao", pod: "openbao-0"}

	identities := []PerRepoIdentity{{
		Upstream: "codeberg",
		Org:      "oberthci",
		Repo:     "oberth",
		Grants:   []string{"oberth/data/release/cosign-secret"},
	}}

	items, err := ConfigurePerRepoIdentities(context.Background(), store, "root", identities, "oberth-argo")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	// Verify the policy body contains the grant path
	for _, call := range runner.calls {
		if call.command == "policy write "+name+" -" {
			if !strings.Contains(call.stdin, "release/cosign-secret") {
				t.Fatalf("policy body missing grant path:\n%s", call.stdin)
			}
		}
	}
}

func TestPerRepoIdentityNamesReturnsSorted(t *testing.T) {
	t.Parallel()
	ids := []PerRepoIdentity{
		{Upstream: "github", Org: "skipops", Repo: "terraform"},
		{Upstream: "codeberg", Org: "oberthci", Repo: "oberth"},
		{Upstream: "codeberg", Org: "oberthci", Repo: "cloudtaser-operator"},
	}
	names := PerRepoIdentityNames(ids)
	if len(names) != 3 {
		t.Fatalf("expected 3 names, got %d", len(names))
	}
	for i := 1; i < len(names); i++ {
		if names[i] < names[i-1] {
			t.Fatalf("names not sorted: %v", names)
		}
	}
}

func TestPerRepoIdentityNamesDeduplicates(t *testing.T) {
	t.Parallel()
	ids := []PerRepoIdentity{
		{Upstream: "codeberg", Org: "oberthci", Repo: "oberth"},
		{Upstream: "codeberg", Org: "oberthci", Repo: "oberth"},
	}
	names := PerRepoIdentityNames(ids)
	if len(names) != 1 {
		t.Fatalf("expected 1 unique name, got %d: %v", len(names), names)
	}
}

func TestGrantlessRepoGetsNoPerRepoSA(t *testing.T) {
	t.Parallel()
	// A repo with no grants and no declared secret-store paths should not
	// appear in the per-repo identities at all. This test verifies the policy
	// generation still works (produces an org-scoped-only policy) but the
	// caller's responsibility is to not include grantless repos.
	policy := PerRepoPolicy("oberth", "oberthci", "oberth", nil)
	// Only the org rule, this repo's own rule, and revoke-self should be
	// present. The org rule is this fork's divergence (see PerRepoPolicy);
	// upstream emits two entries here rather than three.
	lines := strings.Split(policy, "\n")
	pathCount := 0
	for _, line := range lines {
		if strings.Contains(line, `path "`) {
			pathCount++
		}
	}
	if pathCount != 3 { // org rule + repo rule + revoke-self
		t.Fatalf("grantless policy should have exactly 3 path entries, got %d:\n%s", pathCount, policy)
	}
}
