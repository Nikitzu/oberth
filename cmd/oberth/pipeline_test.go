package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordingPipelineServer answers the four pipeline endpoints and records what
// the CLI actually sent, so the tests assert the wire and not a mock's memory.
type recordingPipelineServer struct {
	method string
	path   string
	query  string
	body   map[string]any
}

func pipelineServer(t *testing.T, payload string) (*httptest.Server, *recordingPipelineServer) {
	t.Helper()
	recorded := &recordingPipelineServer{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		recorded.method, recorded.path = request.Method, request.URL.Path
		recorded.query = request.URL.RawQuery
		if request.Body != nil {
			raw, _ := io.ReadAll(request.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &recorded.body)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(server.Close)
	return server, recorded
}

const heldPayload = `{"repository":"codeberg/acme/demo","trigger":"build","trigger_file":".oberth/build.yaml",` +
	`"held":true,"version":3,"sha256":"abc123","document":"apiVersion: argoproj.io/v1alpha1\n",` +
	`"stored_by":"SHA256:operator","stored_at":"2026-08-31T09:00:00Z","fingerprint_ref":"1111111111111111111111111111111111111111",` +
	`"inputs":[".github/workflows/build.yml","go.mod"],"versions":3}`

func TestPipelineShowPrintsTheDocumentAndItsMetadata(t *testing.T) {
	server, recorded := pipelineServer(t, heldPayload)
	configure(t, server)
	var out bytes.Buffer
	if err := runRepoPipeline(context.Background(), []string{"show", "codeberg/acme/demo"}, &out); err != nil {
		t.Fatal(err)
	}
	if recorded.method != http.MethodGet || recorded.path != "/api/repos/pipeline" {
		t.Fatalf("show sent %s %s", recorded.method, recorded.path)
	}
	if !strings.Contains(recorded.query, "repo=codeberg%2Facme%2Fdemo") || !strings.Contains(recorded.query, "trigger=build") {
		t.Fatalf("show query = %q", recorded.query)
	}
	body := out.String()
	for _, want := range []string{"version 3 of 3", "SHA256:operator", "abc123", ".github/workflows/build.yml", "apiVersion:"} {
		if !strings.Contains(body, want) {
			t.Fatalf("output missing %q:\n%s", want, body)
		}
	}
}

func TestPipelineShowDistinguishesNeverStoredFromWithdrawn(t *testing.T) {
	server, _ := pipelineServer(t, `{"repository":"demo","trigger_file":".oberth/build.yaml","held":false,"version":0}`)
	configure(t, server)
	var out bytes.Buffer
	if err := runRepoPipeline(context.Background(), []string{"show", "demo"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "holds no server-side") {
		t.Fatalf("never-stored output:\n%s", out.String())
	}

	server2, _ := pipelineServer(t, `{"repository":"demo","trigger_file":".oberth/build.yaml","held":false,"version":4,`+
		`"stored_by":"SHA256:operator","stored_at":"2026-08-31T09:00:00Z"}`)
	configure(t, server2)
	out.Reset()
	if err := runRepoPipeline(context.Background(), []string{"show", "demo"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "withdrew its server-side") {
		t.Fatalf("withdrawn output:\n%s", out.String())
	}
}

func TestPipelineSetUploadsTheFileContents(t *testing.T) {
	server, recorded := pipelineServer(t, heldPayload)
	configure(t, server)
	path := filepath.Join(t.TempDir(), "build.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: argoproj.io/v1alpha1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runRepoPipeline(context.Background(),
		[]string{"set", "codeberg/acme/demo", path, "--trigger", "release", "--ref", "deadbeef"}, &out); err != nil {
		t.Fatal(err)
	}
	if recorded.method != http.MethodPut || recorded.path != "/api/repos/pipeline" {
		t.Fatalf("set sent %s %s", recorded.method, recorded.path)
	}
	if recorded.body["repo"] != "codeberg/acme/demo" || recorded.body["trigger"] != "release" ||
		recorded.body["ref"] != "deadbeef" || recorded.body["document"] != "apiVersion: argoproj.io/v1alpha1\n" {
		t.Fatalf("set body = %#v", recorded.body)
	}
	if !strings.Contains(out.String(), "as version 3") {
		t.Fatalf("set output:\n%s", out.String())
	}
}

func TestPipelineSetSurfacesTheServersAdmissionRefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"argoworkflow: template build: image not allowed"}`))
	}))
	t.Cleanup(server.Close)
	configure(t, server)
	path := filepath.Join(t.TempDir(), "build.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runRepoPipeline(context.Background(), []string{"set", "demo", path}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "image not allowed") {
		t.Fatalf("set error = %v, want the server's admission message", err)
	}
}

func TestPipelineUnsetSendsADelete(t *testing.T) {
	server, recorded := pipelineServer(t,
		`{"repository":"demo","trigger_file":".oberth/build.yaml","held":false,"version":5}`)
	configure(t, server)
	var out bytes.Buffer
	if err := runRepoPipeline(context.Background(), []string{"unset", "demo"}, &out); err != nil {
		t.Fatal(err)
	}
	if recorded.method != http.MethodDelete || !strings.Contains(recorded.query, "repo=demo") {
		t.Fatalf("unset sent %s %q", recorded.method, recorded.query)
	}
	if !strings.Contains(out.String(), "tombstone version 5") {
		t.Fatalf("unset output:\n%s", out.String())
	}
}

func TestPipelineCheckExitsNonZeroOnDriftAndPrintsTheDiff(t *testing.T) {
	server, recorded := pipelineServer(t, `{"repository":"demo","trigger":"build","ref":"1111111111111111",`+
		`"drifted":true,"changed":[".github/workflows/build.yml"],"diff":"--- a\n+++ b\n-old\n+new\n"}`)
	configure(t, server)
	var out bytes.Buffer
	err := runRepoPipeline(context.Background(), []string{"check", "demo"}, &out)
	if err == nil {
		t.Fatal("check must exit non-zero when the pipeline has drifted")
	}
	if !errors.Is(err, errPipelineDrift) {
		t.Fatalf("check error = %v, want the drift sentinel", err)
	}
	if recorded.method != http.MethodPost || recorded.path != "/api/repos/pipeline/check" {
		t.Fatalf("check sent %s %s", recorded.method, recorded.path)
	}
	if recorded.body["store"] != false {
		t.Fatalf("check body = %#v, want store false by default", recorded.body)
	}
	body := out.String()
	for _, want := range []string{"changed inputs:", ".github/workflows/build.yml", "+new", "--store"} {
		if !strings.Contains(body, want) {
			t.Fatalf("output missing %q:\n%s", want, body)
		}
	}
}

func TestPipelineCheckIsQuietAndGreenWithoutDrift(t *testing.T) {
	server, _ := pipelineServer(t, `{"repository":"demo","trigger":"build","ref":"1111111111111111","drifted":false}`)
	configure(t, server)
	var out bytes.Buffer
	if err := runRepoPipeline(context.Background(), []string{"check", "demo"}, &out); err != nil {
		t.Fatalf("check without drift = %v, want success", err)
	}
	if !strings.Contains(out.String(), "no drift") {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestPipelineCheckStoreFlagTravelsAndIsReported(t *testing.T) {
	server, recorded := pipelineServer(t, `{"repository":"demo","trigger":"build","ref":"1111111111111111",`+
		`"drifted":true,"changed":["go.mod"],"diff":"--- a\n+++ b\n","stored":true,"version":7}`)
	configure(t, server)
	var out bytes.Buffer
	if err := runRepoPipeline(context.Background(), []string{"check", "demo", "--store"}, &out); err != nil {
		t.Fatalf("check --store = %v, want success once the new version is stored", err)
	}
	if recorded.body["store"] != true {
		t.Fatalf("check body = %#v, want store true", recorded.body)
	}
	if !strings.Contains(out.String(), "as version 7") {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestPipelineUsageErrors(t *testing.T) {
	for _, arguments := range [][]string{nil, {"nonsense"}, {"show"}, {"set", "only-one"}} {
		if err := runRepoPipeline(context.Background(), arguments, io.Discard); !errors.Is(err, errUsage) {
			t.Fatalf("repo pipeline %v = %v, want a usage error", arguments, err)
		}
	}
}

func TestRunDetailSaysWhichDocumentRan(t *testing.T) {
	payload := `{"Run":{"ID":"run-abc","Status":"passed","Ref":"main","SHA":"0123456789abcdef","Actor":"miki",` +
		`"Trigger":"branch","PipelineSource":"server","PipelineVersion":3,"PipelineSHA256":"abcdef0123456789",` +
		`"PipelineDrift":[".github/workflows/build.yml"]},"Steps":[],"Repository":{"Name":"demo"}}`
	configure(t, remoteServer(t, payload))
	var out bytes.Buffer
	if err := runRunDetail(context.Background(), []string{"run-abc"}, &out); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, want := range []string{"pipeline: server-held version 3", "abcdef0", "drifted:", ".github/workflows/build.yml"} {
		if !strings.Contains(body, want) {
			t.Fatalf("output missing %q:\n%s", want, body)
		}
	}
}

func TestRunDetailSaysNothingAboutAPipelineItWasNeverTold(t *testing.T) {
	payload := `{"Run":{"ID":"run-old","Status":"passed","Ref":"main","SHA":"0123456789abcdef","Actor":"miki",` +
		`"Trigger":"branch"},"Steps":[],"Repository":{"Name":"demo"}}`
	configure(t, remoteServer(t, payload))
	var out bytes.Buffer
	if err := runRunDetail(context.Background(), []string{"run-old"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "pipeline:") {
		t.Fatalf("a run recorded before the field existed must claim nothing:\n%s", out.String())
	}
}

func TestRunsListingMarksADriftedServerHeldRun(t *testing.T) {
	payload := `[{"ID":"run-a","Ref":"main","SHA":"0123456789abcdef","Status":"passed","Trigger":"branch",` +
		`"PipelineSource":"server","PipelineDrift":["go.mod"]},` +
		`{"ID":"run-b","Ref":"main","SHA":"0123456789abcdef","Status":"passed","Trigger":"branch",` +
		`"PipelineSource":"commit"}]`
	configure(t, remoteServer(t, payload))
	var out bytes.Buffer
	if err := runRuns(context.Background(), nil, &out); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	if !strings.Contains(body, "PIPELINE") || !strings.Contains(body, "server!") || !strings.Contains(body, "commit") {
		t.Fatalf("runs listing:\n%s", body)
	}
}

func TestStatusWarnsAboutADriftedRepository(t *testing.T) {
	payload := `{"database":"ready","vcs":"ready","cluster":"ready","audit":"ready","version":"dev",` +
		`"pipeline_drift":[{"repository":"codeberg/acme/demo","run_id":"run-abcdefghijklmnop","ref":"main",` +
		`"inputs":[".github/workflows/build.yml"]}]}`
	configure(t, remoteServer(t, payload))
	var out bytes.Buffer
	if err := runRemoteStatus(context.Background(), nil, &out); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, want := range []string{"warning:", "codeberg/acme/demo", ".github/workflows/build.yml", "run-abcdefgh"} {
		if !strings.Contains(body, want) {
			t.Fatalf("status output missing %q:\n%s", want, body)
		}
	}
}
