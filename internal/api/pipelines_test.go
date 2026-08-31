package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakePipelines struct {
	actor    string
	repo     string
	trigger  string
	document string
	ref      string
	store    bool
	verb     string
}

func (f *fakePipelines) PipelineShow(_ context.Context, repo, trigger string) (any, error) {
	f.verb, f.repo, f.trigger = "show", repo, trigger
	return map[string]any{"repository": repo, "held": true}, nil
}

func (f *fakePipelines) PipelineSet(_ context.Context, actor, repo, trigger string, document []byte, ref string) (any, error) {
	f.verb, f.actor, f.repo, f.trigger = "set", actor, repo, trigger
	f.document, f.ref = string(document), ref
	return map[string]any{"repository": repo, "version": 1}, nil
}

func (f *fakePipelines) PipelineUnset(_ context.Context, actor, repo, trigger string) (any, error) {
	f.verb, f.actor, f.repo, f.trigger = "unset", actor, repo, trigger
	return map[string]any{"repository": repo, "version": 2}, nil
}

func (f *fakePipelines) PipelineCheck(_ context.Context, actor, repo, trigger, ref string, store bool) (any, error) {
	f.verb, f.actor, f.repo, f.trigger, f.ref, f.store = "check", actor, repo, trigger, ref, store
	return map[string]any{"repository": repo, "drifted": false}, nil
}

func pipelineTestServer(t *testing.T) (*Server, *fakePipelines) {
	t.Helper()
	backend := &fakeBackend{}
	pipelines := &fakePipelines{}
	server, err := New(backend, backend, backend, "test", WithPipelines(pipelines))
	if err != nil {
		t.Fatal(err)
	}
	return server, pipelines
}

func authed(method, target, body string) *http.Request {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	request.Header.Set("Authorization", "Bearer valid-token")
	return request
}

func TestPipelineEndpointsRequireABearerToken(t *testing.T) {
	server, _ := pipelineTestServer(t)
	for _, target := range []struct{ method, path string }{
		{http.MethodGet, "/api/repos/pipeline?repo=demo"},
		{http.MethodPut, "/api/repos/pipeline"},
		{http.MethodDelete, "/api/repos/pipeline?repo=demo"},
		{http.MethodPost, "/api/repos/pipeline/check"},
	} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(target.method, target.path, strings.NewReader("{}")))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401", target.method, target.path, response.Code)
		}
	}
}

func TestPipelineEndpointsAreAbsentWithoutTheService(t *testing.T) {
	server, _ := testServer(t)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, authed(http.MethodGet, "/api/repos/pipeline?repo=demo", ""))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when no pipeline service is wired", response.Code)
	}
}

func TestPipelineShowPassesTheRepoAndTrigger(t *testing.T) {
	server, pipelines := pipelineTestServer(t)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, authed(http.MethodGet, "/api/repos/pipeline?repo=up%2Forg%2Fdemo&trigger=release", ""))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", response.Code, response.Body)
	}
	if pipelines.repo != "up/org/demo" || pipelines.trigger != "release" {
		t.Fatalf("show received %q %q", pipelines.repo, pipelines.trigger)
	}
}

func TestPipelineShowRequiresARepo(t *testing.T) {
	server, _ := pipelineTestServer(t)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, authed(http.MethodGet, "/api/repos/pipeline", ""))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestPipelineSetCarriesTheAuthenticatedIdentity(t *testing.T) {
	server, pipelines := pipelineTestServer(t)
	body, err := json.Marshal(map[string]string{
		"repo": "demo", "trigger": "build", "document": "apiVersion: x\n", "ref": "deadbeef",
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, authed(http.MethodPut, "/api/repos/pipeline", string(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", response.Code, response.Body)
	}
	if pipelines.actor != "agent@host" {
		t.Fatalf("set stored by %q, want the authenticated identity", pipelines.actor)
	}
	if pipelines.document != "apiVersion: x\n" || pipelines.ref != "deadbeef" {
		t.Fatalf("set received %q %q", pipelines.document, pipelines.ref)
	}
}

func TestPipelineSetRefusesAnEmptyDocument(t *testing.T) {
	server, _ := pipelineTestServer(t)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, authed(http.MethodPut, "/api/repos/pipeline", `{"repo":"demo","document":""}`))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestPipelineSetRefusesAnUnknownField(t *testing.T) {
	server, _ := pipelineTestServer(t)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, authed(http.MethodPut, "/api/repos/pipeline",
		`{"repo":"demo","document":"x","surprise":true}`))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown body field", response.Code)
	}
}

func TestPipelineCheckPassesTheStoreFlag(t *testing.T) {
	server, pipelines := pipelineTestServer(t)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, authed(http.MethodPost, "/api/repos/pipeline/check",
		`{"repo":"demo","trigger":"build","ref":"abc","store":true}`))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", response.Code, response.Body)
	}
	if !pipelines.store || pipelines.ref != "abc" || pipelines.actor != "agent@host" {
		t.Fatalf("check received store=%v ref=%q actor=%q", pipelines.store, pipelines.ref, pipelines.actor)
	}
}

func TestPipelineUnsetPassesTheRepo(t *testing.T) {
	server, pipelines := pipelineTestServer(t)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, authed(http.MethodDelete, "/api/repos/pipeline?repo=demo&trigger=build", ""))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", response.Code, response.Body)
	}
	if pipelines.verb != "unset" || pipelines.repo != "demo" {
		t.Fatalf("unset received %q %q", pipelines.verb, pipelines.repo)
	}
}
