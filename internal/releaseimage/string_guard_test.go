package releaseimage

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestKeyJSONStringConversionGuard walks the non-test source files of this
// package with go/parser and asserts that exactly one string(…KeyJSON…)
// conversion exists: the documented site inside garAuthenticator. A new
// conversion site fails the test — review it for security impact and update
// garAuthenticator's residual comment before adding it to the allowed set.
func TestKeyJSONStringConversionGuard(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	var sites []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok || ident.Name != "string" {
				return true
			}
			var buf bytes.Buffer
			_ = printer.Fprint(&buf, fset, call.Args[0])
			arg := buf.String()
			// Match any reference to KeyJSON regardless of qualifier
			// (keyJSON, config.KeyJSON, c.KeyJSON, etc.).
			if strings.Contains(strings.ToLower(arg), "keyjson") {
				pos := fset.Position(call.Pos())
				sites = append(sites, pos.String())
			}
			return true
		})
	}
	if len(sites) != 1 {
		t.Fatalf("expected exactly 1 documented string(…KeyJSON…) conversion (garAuthenticator), found %d: %v", len(sites), sites)
	}
	if !strings.Contains(sites[0], "image.go") {
		t.Fatalf("documented conversion expected in image.go, found at %s", sites[0])
	}
}

// TestGarAuthenticatorSourceIsZeroable verifies that after garAuthenticator
// returns, the source []byte can be zeroed without affecting the authenticator.
// This is the security property: the caller zeros the key material after
// handoff; the accepted-residual immutable string inside the authenticator
// survives (it must — Go strings are immutable), but the source is clean.
func TestGarAuthenticatorSourceIsZeroable(t *testing.T) {
	keyJSON := []byte(`{"type":"service_account","project_id":"test"}`)
	original := string(keyJSON) // keep a reference for comparison
	auth := garAuthenticator(keyJSON)

	// Zero the source buffer.
	clear(keyJSON)
	for _, b := range keyJSON {
		if b != 0 {
			t.Fatal("source keyJSON buffer was not zeroed by clear()")
		}
	}

	// The authenticator must still function with its internal string copy.
	config, err := auth.Authorization()
	if err != nil {
		t.Fatalf("authenticator.Authorization() after source zeroing: %v", err)
	}
	if config.Username != "_json_key" {
		t.Fatalf("authenticator username = %q, want _json_key", config.Username)
	}
	if config.Password != original {
		t.Fatalf("authenticator password lost after source zeroing")
	}
}
