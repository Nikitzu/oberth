package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// onboardServer is a whole Oberth deployment, in a handler. It records what it
// was asked to do so a test can assert the ORDER and the IDEMPOTENCE of the
// steps, which is what onboarding actually has to get right.
type onboardServer struct {
	mutex      sync.Mutex
	calls      []string
	registered int
	stored     []string
	runStatus  string
	failedBurn string
	failedStep string
	logBody    string
	runError   string
	engine     string
}

func newOnboardServer(t *testing.T, engine string) (*onboardServer, *httptest.Server) {
	t.Helper()
	state := &onboardServer{runStatus: "passed", engine: engine}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mutex.Lock()
		defer state.mutex.Unlock()
		state.calls = append(state.calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == "/api/status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"engine":       state.engine,
				"ssh_endpoint": "oberth.invalid:2222",
				"secret_store": map[string]any{"sealed": false},
				"upstream_info": []map[string]string{
					{"name": "github", "base_url": "ssh://git@github.com/transferz"},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/repos":
			state.registered++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"repository": "gateway", "upstream": "github", "org": "transferz",
				"default_branch": "master", "created": state.registered == 1,
				"branch_source": "the upstream's own HEAD advertisement",
			})
		case r.Method == http.MethodPut && r.URL.Path == "/api/repos/pipeline":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			state.stored = append(state.stored, body["document"])
			_ = json.NewEncoder(w).Encode(map[string]any{
				"repository": "gateway", "held": true, "version": len(state.stored),
			})
		case r.URL.Path == "/api/runs":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"ID": "aaaabbbbccccddddeeeeffff00001111", "Ref": "master",
				"SHA": "0000000000000000000000000000000000000000", "Status": state.runStatus, "Trigger": "push",
				"FailedBurn": state.failedBurn, "FailedStep": state.failedStep,
				"Error": state.runError,
			}})
		case strings.HasSuffix(r.URL.Path, "/logs"):
			_ = json.NewEncoder(w).Encode(map[string]any{"output": state.logBody})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	configure(t, server)
	return state, server
}

func (state *onboardServer) sawCall(want string) bool {
	state.mutex.Lock()
	defer state.mutex.Unlock()
	for _, call := range state.calls {
		if call == want {
			return true
		}
	}
	return false
}

// onboardCheckout is a real git repository, because onboard reads the origin
// remote and runs git for the remote and the push.
func onboardCheckout(t *testing.T, origin string) string {
	t.Helper()
	root := t.TempDir()
	for _, arguments := range [][]string{
		{"init", "--initial-branch=master"},
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@example.invalid"},
		{"remote", "add", "origin", origin},
	} {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, out)
		}
	}
	manifest := `{"name":"gateway","scripts":{"lint":"biome check .","test":"vitest run"}}`
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package-lock.json"),
		[]byte(`{"name":"gateway","lockfileVersion":3,"packages":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"add", "."}, {"commit", "-m", "initial"}} {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, out)
		}
	}
	return root
}

// The acceptance case, minus the push: one invocation registers, stores a
// pipeline and sets the remote, with nothing typed but the verb.
func TestOnboardDoesEveryStepFromOneInvocation(t *testing.T) {
	state, _ := newOnboardServer(t, "argo")
	root := onboardCheckout(t, "git@github.com:transferz/gateway.git")

	var out bytes.Buffer
	if err := runOnboard(context.Background(), []string{root, "--dry-run"}, &out); err != nil {
		t.Fatalf("onboard: %v", err)
	}
	if state.registered != 1 {
		t.Errorf("registered %d times, want 1", state.registered)
	}
	if len(state.stored) != 1 {
		t.Errorf("stored %d pipelines, want 1", len(state.stored))
	}
	if !state.sawCall("POST /api/repos") || !state.sawCall("PUT /api/repos/pipeline") {
		t.Errorf("onboard did not register and store: %v", state.calls)
	}
	// The remote is set on the checkout, not merely reported.
	url, err := exec.Command("git", "-C", root, "remote", "get-url", "oberth").Output()
	if err != nil {
		t.Fatalf("the oberth remote was not created: %v", err)
	}
	if got := strings.TrimSpace(string(url)); got != "ssh://git@oberth.invalid:2222/gateway" {
		t.Errorf("remote url = %q", got)
	}
}

// The pipeline must never be written into the working tree. `oberth init`
// writes .oberth/build.yaml, which every onboarding session then had to
// remember to delete, and one of them committed it.
func TestOnboardNeverWritesIntoTheWorkingTree(t *testing.T) {
	_, _ = newOnboardServer(t, "argo")
	root := onboardCheckout(t, "git@github.com:transferz/gateway.git")

	before := treeListing(t, root)
	var out bytes.Buffer
	if err := runOnboard(context.Background(), []string{root, "--dry-run"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".oberth")); !os.IsNotExist(err) {
		t.Error("onboard created a .oberth directory in the checkout")
	}
	if after := treeListing(t, root); after != before {
		t.Errorf("onboard changed the working tree:\nbefore %v\nafter  %v", before, after)
	}
}

func treeListing(t *testing.T, root string) string {
	t.Helper()
	var names []string
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return strings.Join(names, ",")
}

// Running it twice is the normal case, because the reason to run it again is
// that the first run said something needed fixing.
func TestOnboardIsIdempotent(t *testing.T) {
	state, _ := newOnboardServer(t, "argo")
	root := onboardCheckout(t, "git@github.com:transferz/gateway.git")

	for attempt := 0; attempt < 2; attempt++ {
		var out bytes.Buffer
		if err := runOnboard(context.Background(), []string{root, "--dry-run"}, &out); err != nil {
			t.Fatalf("onboard attempt %d: %v", attempt, err)
		}
	}
	if state.registered != 2 {
		t.Errorf("registration was called %d times, want 2 (it is idempotent, not skipped)", state.registered)
	}
	// The same repository generates the same document, so the second store is
	// byte-identical. A differing document would report drift on every run.
	if len(state.stored) == 2 && state.stored[0] != state.stored[1] {
		t.Error("two onboardings of an unchanged repository produced different documents")
	}
	url, err := exec.Command("git", "-C", root, "remote", "get-url", "oberth").Output()
	if err != nil || strings.TrimSpace(string(url)) != "ssh://git@oberth.invalid:2222/gateway" {
		t.Errorf("the remote was not left correct on the second run: %q %v", url, err)
	}
}

// A checkout from another org cannot be onboarded here, and it has to be told
// so before a push produces an admission refusal naming a path nobody typed.
func TestOnboardRefusesACheckoutOutsideTheRegisteredOrg(t *testing.T) {
	_, _ = newOnboardServer(t, "argo")
	root := onboardCheckout(t, "git@github.com:someone-else/gateway.git")

	var out bytes.Buffer
	err := runOnboard(context.Background(), []string{root, "--dry-run"}, &out)
	if err == nil {
		t.Fatal("a checkout from another org must be refused")
	}
	for _, want := range []string{"someone-else", "transferz", "upstream add"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal is missing %q:\n%s", want, err)
		}
	}
}

// The document has to match the engine that will run it.
func TestOnboardGeneratesForTheEngineTheServerReports(t *testing.T) {
	state, _ := newOnboardServer(t, "docker")
	root := onboardCheckout(t, "git@github.com:transferz/gateway.git")

	var out bytes.Buffer
	if err := runOnboard(context.Background(), []string{root, "--dry-run"}, &out); err != nil {
		t.Fatal(err)
	}
	if len(state.stored) != 1 {
		t.Fatalf("stored %d pipelines", len(state.stored))
	}
	if strings.Contains(state.stored[0], "volumeMounts") {
		t.Error("a docker-engine deployment was sent a document declaring volumeMounts, which it refuses")
	}
}

func TestOnboardRejectsBadUsage(t *testing.T) {
	_, _ = newOnboardServer(t, "argo")
	var out bytes.Buffer
	if err := runOnboard(context.Background(), []string{"one", "two"}, &out); err == nil {
		t.Fatal("two paths must be a usage error")
	}
}
