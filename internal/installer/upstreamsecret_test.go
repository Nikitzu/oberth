package installer

import (
	"bytes"
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// storeForTest builds the in-process handle on a configured store that the
// install carries from the secret-store phase into onboarding.
func storeForTest(runner *fakeBaoRunner) SecretStoreResult {
	return SecretStoreResult{
		client:    openBaoExec{run: runner.run, contextName: "test-ctx", namespace: DefaultOpenBaoNamespace, pod: expectedOpenBaoPodName},
		rootToken: "s.roottoken",
	}
}

func TestDeclaredUpstreamTokenPathIsHierarchical(t *testing.T) {
	t.Parallel()
	// The annotation spelling is authorized structurally against the pushing
	// repository's upstream; the raw oberth/data/... spelling is rejected in
	// declarations, so the two must not be confused.
	declared := declaredUpstreamTokenPath("transferz")
	if declared != "oberth/upstream/transferz/github-token" {
		t.Fatalf("declared path = %q", declared)
	}
	if strings.Count(declared, "/") != 3 {
		t.Fatalf("declared org-scoped path must have exactly 4 segments, got %q", declared)
	}
	if api := upstreamTokenAPIPath("transferz"); api != "oberth/data/upstream/transferz/github-token" {
		t.Fatalf("API path = %q", api)
	}
}

func TestSeedUpstreamTokenWritesScopedPathWithoutExposingTheToken(t *testing.T) {
	t.Parallel()
	runner := &fakeBaoRunner{t: t, responses: map[string]fakeBaoResponse{
		"write oberth/data/upstream/transferz/github-token -": {out: "{}"},
	}}

	if err := seedUpstreamToken(context.Background(), storeForTest(runner), "transferz", "ghp_secretvalue"); err != nil {
		t.Fatal(err)
	}

	call, ok := runner.callsByCommand()["write oberth/data/upstream/transferz/github-token -"]
	if !ok {
		t.Fatalf("the token was never written to the store; calls: %v", runner.calls)
	}
	if !call.authenticated {
		t.Fatal("the write must go through the token-shell wrapper")
	}
	if !strings.Contains(call.stdin, `"token":"ghp_secretvalue"`) {
		t.Fatalf("payload missing the token, stdin = %q", call.stdin)
	}
	// The Kubernetes API server records exec arguments verbatim in its audit
	// log, so the value must never appear there.
	for _, argument := range call.argv {
		if strings.Contains(argument, "ghp_secretvalue") {
			t.Fatalf("token leaked into argv: %v", call.argv)
		}
	}
}

func TestSeedUpstreamTokenRefusesWithoutOrgOrStore(t *testing.T) {
	t.Parallel()
	runner := &fakeBaoRunner{t: t, responses: map[string]fakeBaoResponse{}}
	if err := seedUpstreamToken(context.Background(), storeForTest(runner), "", "tok"); err == nil {
		t.Fatal("an empty org must not be written to the shared subtree root")
	}
	if err := seedUpstreamToken(context.Background(), SecretStoreResult{}, "transferz", "tok"); err == nil {
		t.Fatal("no store must be an error, not a silent success")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("nothing should have been executed, got %v", runner.calls)
	}
}

// TestConfigureUpstreamTokenNonInteractiveSeedsBothPlaces is the effect test:
// a scripted install with the token already exported must end with the token
// in the push Secret AND in the org's subtree of the store. Before this, a
// non-interactive install configured neither and reported success.
func TestConfigureUpstreamTokenNonInteractiveSeedsBothPlaces(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_fromenvironment")
	t.Setenv("GH_TOKEN", "")

	runner := &fakeBaoRunner{t: t, responses: map[string]fakeBaoResponse{
		"write oberth/data/upstream/transferz/github-token -": {out: "{}"},
	}}
	var buf bytes.Buffer
	deps := secretStoreTestDeps(runner, &buf, runningOpenBaoPod())
	// No IsTerminal: a scripted install.
	cfg := Config{}
	_ = cfg.Validate()
	tw := newTableWriter(&buf, false)
	tw.WriteHeader()
	tw.WriteFooter()

	if err := configureUpstreamToken(context.Background(), cfg, deps, tw, storeForTest(runner), "https://github.com/transferz"); err != nil {
		t.Fatal(err)
	}

	secret, err := deps.KubeClient.CoreV1().Secrets(DefaultNamespace).Get(
		context.Background(), upstreamTokenSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("push Secret was not written: %v", err)
	}
	if got := string(secret.Data["token"]); got != "ghp_fromenvironment" {
		t.Fatalf("push Secret token = %q", got)
	}
	if _, ok := runner.callsByCommand()["write oberth/data/upstream/transferz/github-token -"]; !ok {
		t.Fatalf("the store was never seeded; calls: %v", runner.calls)
	}
	if !strings.Contains(buf.String(), "oberth/upstream/transferz/github-token") {
		t.Fatalf("the table must name the path a pipeline declares, got:\n%s", buf.String())
	}
}

// TestConfigureUpstreamTokenNonInteractiveWithoutStoreSaysSo guards the
// honesty of the no-op: with no store there is nowhere to put the package
// token, and the install must say that rather than report a clean table.
func TestConfigureUpstreamTokenNonInteractiveWithoutStoreSaysSo(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_fromenvironment")
	t.Setenv("GH_TOKEN", "")

	runner := &fakeBaoRunner{t: t, responses: map[string]fakeBaoResponse{}}
	var buf bytes.Buffer
	deps := secretStoreTestDeps(runner, &buf)
	cfg := Config{}
	_ = cfg.Validate()
	tw := newTableWriter(&buf, false)
	tw.WriteHeader()
	tw.WriteFooter()

	if err := configureUpstreamToken(context.Background(), cfg, deps, tw, SecretStoreResult{}, "https://github.com/transferz"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "private packages will not install") {
		t.Fatalf("a missing store must be reported, got:\n%s", buf.String())
	}
}
