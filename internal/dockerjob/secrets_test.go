package dockerjob

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oberthci/oberth/pkg/periapsis"
)

type recordingMinter struct {
	tier, org, repo, runID string
	token                  string
	err                    error
}

func (minter *recordingMinter) Mint(_ context.Context, tier, org, repo, runID string) (string, error) {
	minter.tier, minter.org, minter.repo, minter.runID = tier, org, repo, runID
	return minter.token, minter.err
}

func credentialedController(t *testing.T, minter IdentityMinter) *Controller {
	t.Helper()
	controller, err := NewController(Config{SecretStore: SecretStoreConfig{
		Address: "http://127.0.0.1:8200", KVMount: "oberth",
		CIRole: DefaultCIRole, ReleaseRole: DefaultReleaseRole, Minter: minter,
	}})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	return controller
}

// A credentialed step gets the same coordinates the Argo engine injects, so
// the pipeline's own `oberth secretstore exec` invocation needs no edit.
func TestCredentialedStepGetsTheStoreCoordinates(t *testing.T) {
	controller := credentialedController(t, &recordingMinter{token: "signed"})
	request := Request{Name: "job", RunID: "run", Repo: "acme/widget", Trigger: periapsis.TriggerCI, Credentialed: true}
	environment := controller.stepEnvironment(request, Step{})
	values := map[string]string{}
	for _, variable := range environment {
		name, value, _ := strings.Cut(variable, "=")
		values[name] = value
	}
	if values["VAULT_ADDR"] != "http://127.0.0.1:8200" {
		t.Fatalf("VAULT_ADDR: %q", values["VAULT_ADDR"])
	}
	if values["OBERTH_VAULT_ROLE"] != DefaultCIRole {
		t.Fatalf("OBERTH_VAULT_ROLE: %q", values["OBERTH_VAULT_ROLE"])
	}
	if values["OBERTH_SECRETSTORE_KV_MOUNT"] != "oberth" {
		t.Fatalf("OBERTH_SECRETSTORE_KV_MOUNT: %q", values["OBERTH_SECRETSTORE_KV_MOUNT"])
	}
}

// An uncredentialed run must reach no store at all. On a cluster it gets no
// token; here it must not even learn the address.
func TestUncredentialedStepGetsNoStoreCoordinates(t *testing.T) {
	controller := credentialedController(t, &recordingMinter{token: "signed"})
	request := Request{Name: "job", RunID: "run", Repo: "acme/widget", Trigger: periapsis.TriggerCI}
	for _, variable := range controller.stepEnvironment(request, Step{}) {
		if strings.HasPrefix(variable, "VAULT_ADDR=") || strings.HasPrefix(variable, "OBERTH_VAULT_ROLE=") {
			t.Fatalf("an uncredentialed run was handed store coordinates: %q", variable)
		}
	}
	arguments := controller.createArguments(request, Step{Image: "golang"}, 0)
	for index, value := range arguments {
		if value == "--tmpfs" && strings.HasPrefix(arguments[index+1], SecretsMountPath) {
			t.Fatalf("an uncredentialed run got the secrets mount: %v", arguments)
		}
		if value == "--volume" && strings.Contains(arguments[index+1], IdentityMountPath) {
			t.Fatalf("an uncredentialed run got the identity volume: %v", arguments)
		}
	}
}

// The identity is read-only, and the secrets directory is memory backed:
// `oberth secretstore exec` refuses to write credentials anywhere else.
func TestCredentialedStepMountsTheIdentityReadOnlyAndSecretsOnTmpfs(t *testing.T) {
	controller := credentialedController(t, &recordingMinter{token: "signed"})
	request := Request{Name: "job", RunID: "run", Repo: "acme/widget", Trigger: periapsis.TriggerCI, Credentialed: true}
	arguments := controller.createArguments(request, Step{Image: "golang"}, 0)
	joined := strings.Join(arguments, " ")
	if !strings.Contains(joined, "job-identity:"+IdentityMountPath+":ro") {
		t.Fatalf("identity volume is not mounted read-only: %v", arguments)
	}
	if !strings.Contains(joined, SecretsMountPath+":rw,noexec,nosuid,nodev,mode=0700") {
		t.Fatalf("secrets directory is not a private tmpfs: %v", arguments)
	}
}

// The tier comes from the durable run, and the org and repo travel with it so
// a per-repository subject is a change in one function rather than a change in
// the call chain.
func TestMintingUsesTheRunsOwnTierOrgAndRepo(t *testing.T) {
	minter := &recordingMinter{token: "signed"}
	config := SecretStoreConfig{Address: "http://127.0.0.1:8200", CIRole: DefaultCIRole, ReleaseRole: DefaultReleaseRole, Minter: minter}
	if _, err := config.mintIdentity(context.Background(), Request{
		RunID: "run-7", Org: "acme", Repo: "widget", Trigger: periapsis.TriggerRelease, Credentialed: true,
	}); err != nil {
		t.Fatalf("mintIdentity: %v", err)
	}
	if minter.tier != DefaultReleaseRole || minter.org != "acme" || minter.repo != "widget" || minter.runID != "run-7" {
		t.Fatalf("minter saw %+v", minter)
	}
}

func TestMintingRefusesWithNoStoreConfigured(t *testing.T) {
	var config SecretStoreConfig
	if _, err := config.mintIdentity(context.Background(), Request{Credentialed: true}); err == nil {
		t.Fatal("a credentialed run was minted with no store configured")
	}
}

// A minting failure must stop the run at submission, not partway through.
func TestCreateRefusesWhenTheIdentityCannotBeMinted(t *testing.T) {
	controller := credentialedController(t, &recordingMinter{err: errors.New("keychain locked")})
	_, err := controller.Create(context.Background(), Request{
		RunID: "run", Name: "job", Repo: "acme/widget", Trigger: periapsis.TriggerCI, Credentialed: true,
		Source: []byte("apiVersion: argoproj.io/v1alpha1\nkind: Workflow\nspec:\n  entrypoint: ci\n  activeDeadlineSeconds: 600\n" + `  templates:
    - name: ci
      dag:
        tasks:
          - name: unit
            template: unit
    - name: unit
      container:
        image: "golang:1.26` + digest + `"
        command: ["true"]
`),
	})
	if err == nil || !strings.Contains(err.Error(), "keychain locked") {
		t.Fatalf("expected the minting failure to refuse the submission, got %v", err)
	}
}
