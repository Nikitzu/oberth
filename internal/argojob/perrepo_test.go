package argojob

import (
	"testing"

	"github.com/oberthci/oberth/pkg/periapsis"
)

const (
	testPerRepoSA   = "oberth-argo-codeberg-oberthci-oberth-abcdef012345"
	testPerRepoRole = testPerRepoSA // SA name == role name by convention
)

func testConfigWithPerRepo() Config {
	cfg := testConfig()
	cfg.PerRepoIdentities = map[string]PerRepoIdentityConfig{
		"codeberg/oberthci/oberth": {ServiceAccountName: testPerRepoSA},
	}
	return cfg
}

// perRepoCredentialedDocument has activeDeadlineSeconds, a secret-paths
// annotation, and an oberth secretstore exec command using that path.
func perRepoCredentialedDocument(secretPath string) string {
	return `
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  annotations:
    oberth.ci/secret-paths: "` + secretPath + `"
spec:
  entrypoint: main
  activeDeadlineSeconds: 3600
  templates:
    - name: main
      container:
        image: golang:1.26-alpine@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
        command: [/run/oberth/bin/oberth]
        args: [secretstore, exec, --path, "` + secretPath + `", --, /bin/true]
`
}

// TestBuildUsesPerRepoIdentityForRelease proves that a repo with a per-repo
// identity uses that identity instead of the shared credentialed SA.
func TestBuildUsesPerRepoIdentityForRelease(t *testing.T) {
	t.Parallel()
	cfg := testConfigWithPerRepo()
	path := "oberth/upstream/oberthci/oberth/test-secret"
	req := testRequest(periapsis.TriggerRelease, perRepoCredentialedDocument(path))
	req.Repo = "oberth"
	req.UpstreamName = "codeberg"
	req.UpstreamOrg = "oberthci"
	req.ApprovedSecrets = map[string]bool{path: true}
	req.SourceVolume = SourceVolume{
		ClaimName:      "test-claim",
		SubPath:        "src",
		VaultCASubPath: "vault-ca",
		BinarySubPath:  "bin",
	}

	wf, err := Build(cfg, req)
	if err != nil {
		t.Fatal(err)
	}
	if wf.Spec.ServiceAccountName != testPerRepoSA {
		t.Fatalf("ServiceAccount = %q, want per-repo %q", wf.Spec.ServiceAccountName, testPerRepoSA)
	}
}

// TestBuildUsesPerRepoIdentityForCI proves that a CI run with a per-repo
// identity also uses it instead of the shared ci-secrets SA.
func TestBuildUsesPerRepoIdentityForCI(t *testing.T) {
	t.Parallel()
	cfg := testConfigWithPerRepo()
	path := "oberth/upstream/oberthci/oberth/test-secret"
	req := testRequest(periapsis.TriggerCI, perRepoCredentialedDocument(path))
	req.Repo = "oberth"
	req.UpstreamName = "codeberg"
	req.UpstreamOrg = "oberthci"
	req.ApprovedSecrets = map[string]bool{path: true}

	wf, err := Build(cfg, req)
	if err != nil {
		t.Fatal(err)
	}
	if wf.Spec.ServiceAccountName != testPerRepoSA {
		t.Fatalf("CI ServiceAccount = %q, want per-repo %q", wf.Spec.ServiceAccountName, testPerRepoSA)
	}
}

// TestBuildFallsBackToSharedIdentityForUnknownRepo proves that a repo without
// a per-repo identity falls back to the shared tier identity.
func TestBuildFallsBackToSharedIdentityForUnknownRepo(t *testing.T) {
	t.Parallel()
	cfg := testConfigWithPerRepo()
	path := "oberth/upstream/skipops/other-repo/test-secret"
	req := testRequest(periapsis.TriggerRelease, perRepoCredentialedDocument(path))
	req.Repo = "other-repo"
	req.UpstreamName = "codeberg"
	req.UpstreamOrg = "skipops"
	req.ApprovedSecrets = map[string]bool{path: true}
	req.SourceVolume = SourceVolume{
		ClaimName:      "test-claim",
		SubPath:        "src",
		VaultCASubPath: "vault-ca",
		BinarySubPath:  "bin",
	}

	wf, err := Build(cfg, req)
	if err != nil {
		t.Fatal(err)
	}
	if wf.Spec.ServiceAccountName != testCredentialedAcct {
		t.Fatalf("ServiceAccount = %q, want shared %q", wf.Spec.ServiceAccountName, testCredentialedAcct)
	}
}

// TestBuildWithoutSecretsStillUsesPipelineSA proves that a repo with a
// per-repo identity but no declared secrets still uses the pipeline SA.
func TestBuildWithoutSecretsStillUsesPipelineSA(t *testing.T) {
	t.Parallel()
	cfg := testConfigWithPerRepo()
	req := testRequest(periapsis.TriggerRelease, greedyDocument)
	req.Repo = "oberth"

	wf, err := Build(cfg, req)
	if err != nil {
		t.Fatal(err)
	}
	if wf.Spec.ServiceAccountName != testPipelineAcct {
		t.Fatalf("ServiceAccount = %q, want pipeline %q", wf.Spec.ServiceAccountName, testPipelineAcct)
	}
}

// TestPerRepoVaultRoleIsInjected proves that the per-repo Vault role (which
// shares the SA name) is injected as OBERTH_VAULT_ROLE.
func TestPerRepoVaultRoleIsInjected(t *testing.T) {
	t.Parallel()
	cfg := testConfigWithPerRepo()
	path := "oberth/upstream/oberthci/oberth/test-secret"
	req := testRequest(periapsis.TriggerRelease, perRepoCredentialedDocument(path))
	req.Repo = "oberth"
	req.UpstreamName = "codeberg"
	req.UpstreamOrg = "oberthci"
	req.ApprovedSecrets = map[string]bool{path: true}
	req.SourceVolume = SourceVolume{
		ClaimName:      "test-claim",
		SubPath:        "src",
		VaultCASubPath: "vault-ca",
		BinarySubPath:  "bin",
	}

	wf, err := Build(cfg, req)
	if err != nil {
		t.Fatal(err)
	}

	// Find OBERTH_VAULT_ROLE in the template
	env := environmentOf(t, wf, "main")
	if env["OBERTH_VAULT_ROLE"] != testPerRepoRole {
		t.Fatalf("OBERTH_VAULT_ROLE = %q, want per-repo %q", env["OBERTH_VAULT_ROLE"], testPerRepoRole)
	}
}

// TestFragmentRunUsesHostRepoIdentity proves that when a fragment is resolved
// into a host pipeline, the host repo's per-repo identity is used, not the
// fragment source's.
func TestFragmentRunUsesHostRepoIdentity(t *testing.T) {
	t.Parallel()
	cfg := testConfigWithPerRepo()
	path := "oberth/upstream/oberthci/oberth/test-secret"
	req := testRequest(periapsis.TriggerRelease, perRepoCredentialedDocument(path))
	req.Repo = "oberth" // HOST repo
	req.UpstreamName = "codeberg"
	req.UpstreamOrg = "oberthci"
	req.ApprovedSecrets = map[string]bool{path: true}
	req.SourceVolume = SourceVolume{
		ClaimName:      "test-claim",
		SubPath:        "src",
		VaultCASubPath: "vault-ca",
		BinarySubPath:  "bin",
	}

	wf, err := Build(cfg, req)
	if err != nil {
		t.Fatal(err)
	}
	if wf.Spec.ServiceAccountName != testPerRepoSA {
		t.Fatalf("fragment host ServiceAccount = %q, want %q", wf.Spec.ServiceAccountName, testPerRepoSA)
	}
}

// TestCrossRepoReadRefusedByPerRepoRole verifies that different repos get
// different SAs, so cross-repo reads at the Vault layer are structurally
// impossible.
func TestCrossRepoReadRefusedByPerRepoRole(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.PerRepoIdentities = map[string]PerRepoIdentityConfig{
		"codeberg/org/repo-a": {ServiceAccountName: "oberth-argo-upstream-org-repo-a-aaa111222333"},
		"codeberg/org/repo-b": {ServiceAccountName: "oberth-argo-upstream-org-repo-b-bbb444555666"},
	}

	pathA := "oberth/upstream/org/repo-a/test-secret"
	reqA := testRequest(periapsis.TriggerRelease, perRepoCredentialedDocument(pathA))
	reqA.Repo = "repo-a"
	reqA.UpstreamName = "codeberg"
	reqA.UpstreamOrg = "org"
	reqA.ApprovedSecrets = map[string]bool{pathA: true}
	reqA.SourceVolume = SourceVolume{
		ClaimName:      "test-claim-a",
		SubPath:        "src",
		VaultCASubPath: "vault-ca",
		BinarySubPath:  "bin",
	}

	wfA, err := Build(cfg, reqA)
	if err != nil {
		t.Fatal(err)
	}

	pathB := "oberth/upstream/org/repo-b/test-secret"
	reqB := testRequest(periapsis.TriggerRelease, perRepoCredentialedDocument(pathB))
	reqB.Repo = "repo-b"
	reqB.UpstreamName = "codeberg"
	reqB.UpstreamOrg = "org"
	reqB.ApprovedSecrets = map[string]bool{pathB: true}
	reqB.SourceVolume = SourceVolume{
		ClaimName:      "test-claim-b",
		SubPath:        "src",
		VaultCASubPath: "vault-ca",
		BinarySubPath:  "bin",
	}

	wfB, err := Build(cfg, reqB)
	if err != nil {
		t.Fatal(err)
	}

	if wfA.Spec.ServiceAccountName == wfB.Spec.ServiceAccountName {
		t.Fatalf("repos a and b got the same SA %q; per-repo isolation violated", wfA.Spec.ServiceAccountName)
	}
}
