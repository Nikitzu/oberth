package client

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const secret = "tok-DO-NOT-LEAK-9f3a"

func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"OBERTH_BASE_URL", "OBERTH_TOKEN", "OBERTH_TOKEN_COMMAND", "OBERTH_CA_CERT"} {
		t.Setenv(name, "")
	}
}

func TestConfiguredReportsWhetherAServerIsSet(t *testing.T) {
	clearEnv(t)
	if FromEnv().Configured() {
		t.Fatal("configured with no base URL")
	}
	t.Setenv("OBERTH_BASE_URL", "https://oberth.example")
	if !FromEnv().Configured() {
		t.Fatal("not configured with a base URL set")
	}
}

func TestTokenComesFromTheEnvironment(t *testing.T) {
	clearEnv(t)
	t.Setenv("OBERTH_TOKEN", secret)
	got, err := FromEnv().resolveToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != secret {
		t.Fatalf("token = %q", got)
	}
}

func TestTokenCommandStdoutIsTrimmed(t *testing.T) {
	clearEnv(t)
	t.Setenv("OBERTH_TOKEN_COMMAND", "printf '"+secret+"\\n\\n'")
	got, err := FromEnv().resolveToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != secret {
		t.Fatalf("token = %q, want the trimmed value", got)
	}
}

func TestTokenCommandFailureSurfacesItsOwnStderr(t *testing.T) {
	clearEnv(t)
	t.Setenv("OBERTH_TOKEN_COMMAND", "echo 'vault is locked' >&2; exit 3")
	_, err := FromEnv().resolveToken(t.Context())
	if err == nil {
		t.Fatal("a failing token command succeeded")
	}
	if !strings.Contains(err.Error(), "vault is locked") {
		t.Fatalf("error does not carry the command's own message: %v", err)
	}
}

func TestAMissingTokenNamesTheEnvironmentVariable(t *testing.T) {
	clearEnv(t)
	_, err := FromEnv().resolveToken(t.Context())
	if err == nil {
		t.Fatal("no token was accepted")
	}
	if !strings.Contains(err.Error(), "OBERTH_TOKEN") {
		t.Fatalf("error does not name the variable to set: %v", err)
	}
}

func newClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	clearEnv(t)
	t.Setenv("OBERTH_BASE_URL", server.URL)
	t.Setenv("OBERTH_TOKEN", secret)
	client, err := New(t.Context(), FromEnv())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestGetSendsTheBearerTokenAndDecodes(t *testing.T) {
	var seenAuth, seenPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth, seenPath = r.Header.Get("Authorization"), r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ID":"run-1","Status":"passed"}`))
	}))
	defer server.Close()

	var run struct{ ID, Status string }
	if err := newClient(t, server).Get(context.Background(), "/api/runs/run-1", nil, &run); err != nil {
		t.Fatal(err)
	}
	if seenAuth != "Bearer "+secret {
		t.Fatalf("Authorization = %q", seenAuth)
	}
	if seenPath != "/api/runs/run-1" {
		t.Fatalf("path = %q", seenPath)
	}
	if run.ID == "" || run.Status == "" {
		t.Fatalf("decoded to zero values, which is what a snake_case struct would do: %+v", run)
	}
}

func TestGetPassesQueryParameters(t *testing.T) {
	var seenQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	var out struct{}
	query := map[string]string{"pattern": "FAIL", "context": "3"}
	if err := newClient(t, server).Get(context.Background(), "/api/runs/x/logs", query, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pattern=FAIL", "context=3"} {
		if !strings.Contains(seenQuery, want) {
			t.Fatalf("query %q missing %q", seenQuery, want)
		}
	}
}

func TestStatusCodesMapToDistinctMessages(t *testing.T) {
	cases := map[int]string{
		http.StatusUnauthorized:        "token",
		http.StatusForbidden:           "not permitted",
		http.StatusNotFound:            "not found",
		http.StatusInternalServerError: "server",
	}
	seen := map[string]bool{}
	for code, fragment := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":"upstream said no"}`))
		}))
		var out struct{}
		err := newClient(t, server).Get(context.Background(), "/api/runs", nil, &out)
		server.Close()
		if err == nil {
			t.Fatalf("%d returned no error", code)
		}
		if !strings.Contains(strings.ToLower(err.Error()), fragment) {
			t.Fatalf("%d gave %q, want something mentioning %q", code, err, fragment)
		}
		if seen[err.Error()] {
			t.Fatalf("%d produced a message already used by another status: %q", code, err)
		}
		seen[err.Error()] = true
	}
}

func TestANonJSONBodyDoesNotPanic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>gateway timeout</html>"))
	}))
	defer server.Close()

	var out struct{}
	err := newClient(t, server).Get(context.Background(), "/api/runs", nil, &out)
	if err == nil {
		t.Fatal("an HTML error body was accepted")
	}
}

func TestNoFailurePathEverEchoesTheToken(t *testing.T) {
	bodies := []struct {
		code int
		body string
	}{
		{http.StatusUnauthorized, `{"error":"invalid bearer token"}`},
		{http.StatusNotFound, `{"error":"no such run"}`},
		{http.StatusInternalServerError, `{"error":"boom"}`},
		{http.StatusBadGateway, "<html>nope</html>"},
		{http.StatusOK, "not json at all"},
	}
	for _, tc := range bodies {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.code)
			_, _ = w.Write([]byte(tc.body))
		}))
		var out struct{}
		err := newClient(t, server).Get(context.Background(), "/api/runs", nil, &out)
		server.Close()
		if err == nil {
			continue
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("status %d leaked the token: %v", tc.code, err)
		}
	}

	clearEnv(t)
	t.Setenv("OBERTH_BASE_URL", "https://127.0.0.1:1")
	t.Setenv("OBERTH_TOKEN", secret)
	client, err := New(t.Context(), FromEnv())
	if err != nil {
		t.Fatal(err)
	}
	var out struct{}
	if err := client.Get(context.Background(), "/api/runs", nil, &out); err != nil {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("a transport error leaked the token: %v", err)
		}
	}
}

func TestAnUntrustedCertificateIsRefusedUntilItsAnchorIsSupplied(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ID":"run-1"}`))
	}))
	defer server.Close()

	clearEnv(t)
	t.Setenv("OBERTH_BASE_URL", server.URL)
	t.Setenv("OBERTH_TOKEN", secret)
	client, err := New(t.Context(), FromEnv())
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ ID string }
	err = client.Get(context.Background(), "/api/runs/run-1", nil, &out)
	if err == nil {
		t.Fatal("an untrusted certificate was accepted")
	}
	if !strings.Contains(err.Error(), "OBERTH_CA_CERT") {
		t.Fatalf("error does not name the variable that would fix it: %v", err)
	}

	anchor := filepath.Join(t.TempDir(), "ca.pem")
	certificate := server.Certificate()
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	if err := os.WriteFile(anchor, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OBERTH_CA_CERT", anchor)
	trusted, err := New(t.Context(), FromEnv())
	if err != nil {
		t.Fatal(err)
	}
	if err := trusted.Get(context.Background(), "/api/runs/run-1", nil, &out); err != nil {
		t.Fatalf("the supplied anchor did not make the certificate trusted: %v", err)
	}
	if out.ID != "run-1" {
		t.Fatalf("decoded %+v", out)
	}
}

func TestTheClientNeverDisablesCertificateVerification(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		body, readErr := os.ReadFile(entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		checked++
		if strings.Contains(string(body), "InsecureSkipVerify") {
			t.Fatalf("%s mentions InsecureSkipVerify; a CI client that can be told to trust anything will be",
				entry.Name())
		}
	}
	if checked == 0 {
		t.Fatal("no source inspected; the guard would pass vacuously")
	}
}

func TestNewRefusesAMalformedBaseURL(t *testing.T) {
	clearEnv(t)
	t.Setenv("OBERTH_TOKEN", secret)
	for _, base := range []string{"", "not a url", "ftp://oberth.example", "://broken"} {
		t.Setenv("OBERTH_BASE_URL", base)
		if _, err := New(t.Context(), FromEnv()); err == nil {
			t.Fatalf("base URL %q was accepted", base)
		}
	}
}

func TestAHostnameMismatchNamesTheAddressesTheCertificateCovers(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	anchor := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(anchor, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	clearEnv(t)
	address := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	t.Setenv("OBERTH_BASE_URL", address)
	t.Setenv("OBERTH_TOKEN", secret)
	t.Setenv("OBERTH_CA_CERT", anchor)
	api, err := New(t.Context(), FromEnv())
	if err != nil {
		t.Fatal(err)
	}
	var out struct{}
	err = api.Get(context.Background(), "/api/runs", nil, &out)
	if err == nil {
		t.Skip("this certificate covers localhost, so there is no mismatch to observe")
	}
	if strings.Contains(err.Error(), "OBERTH_CA_CERT") {
		t.Fatalf("a hostname mismatch was reported as an untrusted authority, "+
			"which sends the user to fix the wrong thing: %v", err)
	}
	if !strings.Contains(err.Error(), "does not cover") {
		t.Fatalf("error does not explain the mismatch: %v", err)
	}
}

// --- #235: reject non-loopback http:// ---

func TestPlainHTTPIsRejectedForNonLoopbackHosts(t *testing.T) {
	clearEnv(t)
	t.Setenv("OBERTH_TOKEN", secret)
	for _, base := range []string{
		"http://oberth.example",
		"http://10.0.0.1:8443",
		"http://192.168.1.1",
	} {
		t.Setenv("OBERTH_BASE_URL", base)
		_, err := New(t.Context(), FromEnv())
		if err == nil {
			t.Fatalf("http:// to %q was accepted; the bearer token would be in cleartext", base)
		}
		if !strings.Contains(err.Error(), "cleartext") {
			t.Fatalf("error does not say why http:// is refused: %v", err)
		}
	}
}

func TestPlainHTTPIsAllowedForLoopback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	clearEnv(t)
	t.Setenv("OBERTH_BASE_URL", server.URL) // http://127.0.0.1:<port>
	t.Setenv("OBERTH_TOKEN", secret)
	if _, err := New(t.Context(), FromEnv()); err != nil {
		t.Fatalf("http://127.0.0.1 should be allowed for port-forward use: %v", err)
	}
}

// --- #237: GetBytes reads raw binary, not JSON ---

func TestGetBytesReadsRawBinaryContent(t *testing.T) {
	binary := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a} // PNG header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(binary)
	}))
	defer server.Close()

	client := newClient(t, server)
	got, err := client.GetBytes(context.Background(), "/api/runs/r/artifacts/img.png", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(binary) {
		t.Fatalf("got %d bytes, want %d", len(got), len(binary))
	}
	for i := range binary {
		if got[i] != binary[i] {
			t.Fatalf("byte %d: got %02x, want %02x", i, got[i], binary[i])
		}
	}
}

func TestGetBytesDoesNotSetAcceptJSON(t *testing.T) {
	var seenAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAccept = r.Header.Get("Accept")
		_, _ = w.Write([]byte("data"))
	}))
	defer server.Close()

	client := newClient(t, server)
	_, _ = client.GetBytes(context.Background(), "/test", nil)
	if seenAccept == "application/json" {
		t.Fatal("GetBytes set Accept: application/json, but it is for binary downloads")
	}
}
