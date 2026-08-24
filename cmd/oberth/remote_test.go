package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func remoteServer(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(server.Close)
	return server
}

func configure(t *testing.T, server *httptest.Server) {
	t.Helper()
	t.Setenv("OBERTH_BASE_URL", server.URL)
	t.Setenv("OBERTH_TOKEN", "test-token")
	t.Setenv("OBERTH_TOKEN_COMMAND", "")
	t.Setenv("OBERTH_CA_CERT", "")
}

const runsPayload = `[{"ID":"run-abc","Ref":"main","SHA":"0123456789abcdef","Status":"failed","Trigger":"schedule"}]`

func TestRunsRendersTheServersRows(t *testing.T) {
	configure(t, remoteServer(t, runsPayload))
	var out bytes.Buffer
	if err := runRuns(context.Background(), nil, &out); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, want := range []string{"run-abc", "failed", "schedule", "0123456", "main"} {
		if !strings.Contains(body, want) {
			t.Fatalf("output missing %q:\n%s", want, body)
		}
	}
}

func TestRunsJSONEmitsTheServerPayload(t *testing.T) {
	configure(t, remoteServer(t, runsPayload))
	var out bytes.Buffer
	if err := runRuns(context.Background(), []string{"--json"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"Trigger": "schedule"`) {
		t.Fatalf("json output is not the server's own shape:\n%s", out.String())
	}
	if strings.Contains(out.String(), "RUN ") {
		t.Fatalf("the rendered table contaminated --json output:\n%s", out.String())
	}
}

func TestRunDetailMarksTheFailingStep(t *testing.T) {
	payload := `{"Run":{"ID":"run-abc","Status":"failed","Ref":"main","SHA":"0123456789abcdef",` +
		`"FailedBurn":"ci","FailedStep":"test","Actor":"miki","Trigger":"branch"},` +
		`"Steps":[{"Burn":"ci","Step":"lint","Status":"passed"},{"Burn":"ci","Step":"test","Status":"failed"}],` +
		`"Repository":{"Name":"demo","DefaultBranch":"main"}}`
	configure(t, remoteServer(t, payload))
	var out bytes.Buffer
	if err := runRunDetail(context.Background(), []string{"run-abc"}, &out); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	if !strings.Contains(body, "* ci") {
		t.Fatalf("the failing step is not marked:\n%s", body)
	}
	if !strings.Contains(body, "demo") {
		t.Fatalf("repository missing:\n%s", body)
	}
}

func TestReposLists(t *testing.T) {
	configure(t, remoteServer(t, `[{"ID":1,"Name":"alpha","DefaultBranch":"main"}]`))
	var out bytes.Buffer
	if err := runRepos(context.Background(), nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "alpha") {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestARemoteCommandWithNoBaseURLNamesTheVariable(t *testing.T) {
	t.Setenv("OBERTH_BASE_URL", "")
	t.Setenv("OBERTH_TOKEN", "test-token")
	var out bytes.Buffer
	err := runRuns(context.Background(), nil, &out)
	if err == nil {
		t.Fatal("a remote command ran with no server configured")
	}
	if !strings.Contains(err.Error(), "OBERTH_BASE_URL") {
		t.Fatalf("error does not name the variable to set: %v", err)
	}
}

func TestRenderedOutputCarriesNoModeLine(t *testing.T) {
	configure(t, remoteServer(t, runsPayload))
	var out bytes.Buffer
	if err := runRuns(context.Background(), nil, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "reading:") {
		t.Fatalf("the mode line went to stdout, so it would contaminate a pipe:\n%s", out.String())
	}
}
