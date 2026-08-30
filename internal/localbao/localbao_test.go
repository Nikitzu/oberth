package localbao

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeStash is an in-memory secret store, so no test touches the real keychain.
type fakeStash struct {
	mu     sync.Mutex
	values map[string]string
}

func newFakeStash() *fakeStash { return &fakeStash{values: map[string]string{}} }

func (stash *fakeStash) Get(_ context.Context, service string) (string, error) {
	stash.mu.Lock()
	defer stash.mu.Unlock()
	value, ok := stash.values[service]
	if !ok {
		return "", ErrNoSecret
	}
	return value, nil
}

func (stash *fakeStash) Put(_ context.Context, service, value string) error {
	stash.mu.Lock()
	defer stash.mu.Unlock()
	stash.values[service] = value
	return nil
}

// fakeBao is enough of the OpenBao API to drive the ceremony.
type fakeBao struct {
	mu          sync.Mutex
	initialized bool
	sealed      bool
	requests    []string
	bodies      map[string]map[string]any
	authMounts  map[string]json.RawMessage
	kvMounts    map[string]json.RawMessage
}

func newFakeBao() *fakeBao {
	return &fakeBao{
		sealed: true, bodies: map[string]map[string]any{},
		authMounts: map[string]json.RawMessage{}, kvMounts: map[string]json.RawMessage{},
	}
}

func (bao *fakeBao) handler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		bao.mu.Lock()
		defer bao.mu.Unlock()
		bao.requests = append(bao.requests, request.Method+" "+request.URL.Path)
		var body map[string]any
		if raw, _ := io.ReadAll(request.Body); len(raw) != 0 {
			_ = json.Unmarshal(raw, &body)
			bao.bodies[request.URL.Path] = body
		}
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(request.URL.Path, "/v1/sys/health"):
			_ = json.NewEncoder(writer).Encode(map[string]any{"initialized": bao.initialized, "sealed": bao.sealed})
		case request.URL.Path == "/v1/sys/init":
			if bao.initialized {
				writer.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(writer).Encode(map[string]any{"errors": []string{"already initialized"}})
				return
			}
			bao.initialized = true
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"keys_base64": []string{"unseal-key"}, "root_token": "root-token",
			})
		case request.URL.Path == "/v1/sys/unseal":
			if body["key"] != "unseal-key" {
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			bao.sealed = false
			_ = json.NewEncoder(writer).Encode(map[string]any{"sealed": false})
		case request.URL.Path == "/v1/sys/auth" && request.Method == http.MethodGet:
			_ = json.NewEncoder(writer).Encode(bao.authMounts)
		case strings.HasPrefix(request.URL.Path, "/v1/sys/auth/"):
			bao.authMounts[strings.TrimPrefix(request.URL.Path, "/v1/sys/auth/")+"/"] = json.RawMessage(`{}`)
			writer.WriteHeader(http.StatusNoContent)
		case request.URL.Path == "/v1/sys/mounts" && request.Method == http.MethodGet:
			_ = json.NewEncoder(writer).Encode(bao.kvMounts)
		case strings.HasPrefix(request.URL.Path, "/v1/sys/mounts/"):
			bao.kvMounts[strings.TrimPrefix(request.URL.Path, "/v1/sys/mounts/")+"/"] = json.RawMessage(`{}`)
			writer.WriteHeader(http.StatusNoContent)
		default:
			writer.WriteHeader(http.StatusNoContent)
		}
	})
}

func runInit(t *testing.T, bao *fakeBao, stash SecretStash, keyPath string) (*httptest.Server, string) {
	t.Helper()
	server := httptest.NewServer(bao.handler())
	t.Cleanup(server.Close)
	var output strings.Builder
	options := Options{
		Address: server.URL, Stash: stash, SigningKeyPath: keyPath, Output: &output,
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			// Pretend the container is already running, so the ceremony under
			// test is the configuration and not the docker plumbing.
			if len(args) > 0 && args[0] == "inspect" {
				return []byte("true\n"), nil
			}
			return nil, nil
		},
	}
	if err := Init(context.Background(), options); err != nil {
		t.Fatalf("Init: %v\n%s", err, output.String())
	}
	return server, output.String()
}

func TestInitStoresTheKeysBeforeAnythingElseCanFail(t *testing.T) {
	stash := newFakeStash()
	keyPath := filepath.Join(t.TempDir(), "signing.pem")
	runInit(t, newFakeBao(), stash, keyPath)
	if value, err := stash.Get(context.Background(), UnsealKeychainService); err != nil || value != "unseal-key" {
		t.Fatalf("unseal key: %q %v", value, err)
	}
	if value, err := stash.Get(context.Background(), RootKeychainService); err != nil || value != "root-token" {
		t.Fatalf("root token: %q %v", value, err)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("signing key: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("signing key mode %o; it mints run identities and must not be readable by anyone else", info.Mode().Perm())
	}
}

// The two policies are the tier boundary, so their text is worth pinning: a CI
// role that reached the whole mount would be a silent tier collapse.
func TestPoliciesScopeTheTiers(t *testing.T) {
	ci := ciPolicy("oberth")
	if !strings.Contains(ci, `"oberth/data/upstream/*"`) {
		t.Fatalf("ci policy does not scope to the upstream subtree: %s", ci)
	}
	if strings.Contains(ci, `"oberth/data/*"`) {
		t.Fatalf("the ci policy reaches the whole mount: %s", ci)
	}
	release := releasePolicy("oberth")
	if !strings.Contains(release, `"oberth/data/*"`) {
		t.Fatalf("release policy: %s", release)
	}
}

func TestInitBindsEachRoleToItsOwnSubjectAndAudience(t *testing.T) {
	bao := newFakeBao()
	runInit(t, bao, newFakeStash(), filepath.Join(t.TempDir(), "signing.pem"))
	for _, role := range []string{DefaultCIRole, DefaultReleaseRole} {
		body := bao.bodies["/v1/auth/jwt/role/"+role]
		if body == nil {
			t.Fatalf("role %s was never written", role)
		}
		if body["bound_subject"] != role {
			t.Fatalf("role %s binds subject %v", role, body["bound_subject"])
		}
		audiences, _ := body["bound_audiences"].([]any)
		if len(audiences) != 1 || audiences[0] != Audience {
			t.Fatalf("role %s binds audiences %v", role, body["bound_audiences"])
		}
		policies, _ := body["token_policies"].([]any)
		if len(policies) != 1 || policies[0] != role {
			t.Fatalf("role %s carries policies %v", role, body["token_policies"])
		}
	}
	config := bao.bodies["/v1/auth/jwt/config"]
	if config == nil || config["bound_issuer"] != Issuer {
		t.Fatalf("the jwt mount does not bind the issuer: %v", config)
	}
	keys, _ := config["jwt_validation_pubkeys"].([]any)
	if len(keys) != 1 || !strings.Contains(keys[0].(string), "BEGIN PUBLIC KEY") {
		t.Fatalf("the jwt mount was not given a public key: %v", config["jwt_validation_pubkeys"])
	}
}

// Re-running the ceremony has to be safe, or an operator will not run it when
// something looks wrong, which is exactly when it is needed.
func TestInitIsIdempotent(t *testing.T) {
	bao := newFakeBao()
	stash := newFakeStash()
	keyPath := filepath.Join(t.TempDir(), "signing.pem")
	runInit(t, bao, stash, keyPath)
	first, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}

	server := httptest.NewServer(bao.handler())
	defer server.Close()
	var output strings.Builder
	options := Options{
		Address: server.URL, Stash: stash, SigningKeyPath: keyPath, Output: &output,
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "inspect" {
				return []byte("true\n"), nil
			}
			return nil, nil
		},
	}
	if err := Init(context.Background(), options); err != nil {
		t.Fatalf("second Init: %v\n%s", err, output.String())
	}
	second, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("a re-run replaced the signing key, which would invalidate every existing configuration")
	}
	if !strings.Contains(output.String(), "already initialised") {
		t.Fatalf("a re-run did not recognise an initialised store:\n%s", output.String())
	}
}

// A sealed store with no key in the keychain must say so rather than hang or
// report a connection problem.
func TestUnsealSaysWhenTheKeychainHoldsNoKey(t *testing.T) {
	bao := newFakeBao()
	bao.initialized = true
	server := httptest.NewServer(bao.handler())
	defer server.Close()
	err := Unseal(context.Background(), Options{
		Address: server.URL, Stash: newFakeStash(), Output: io.Discard,
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "inspect" {
				return []byte("true\n"), nil
			}
			return nil, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), UnsealKeychainService) {
		t.Fatalf("expected a message naming the keychain service, got %v", err)
	}
}
