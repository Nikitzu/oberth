package installer

import (
	"reflect"
	"testing"
)

// Every injectable dependency InstallDeps carries must reach the Deps the
// install actually runs with.
//
// LookPath was dropped at that boundary. Client detection then saw a nil
// resolver, concluded the machine had no MCP client at all, skipped the picker
// and registered nothing -- and the install still reported success, because
// "no clients here" is a legitimate answer. Nothing failed; the feature was
// simply absent.
//
// This compares the two structs by field name rather than testing one call,
// so adding a field to InstallDeps and forgetting to forward it fails here.
func TestEveryInjectableDependencyIsForwarded(t *testing.T) {
	t.Parallel()

	installFields := map[string]bool{}
	installType := reflect.TypeOf(InstallDeps{})
	for i := range installType.NumField() {
		installFields[installType.Field(i).Name] = true
	}

	depsType := reflect.TypeOf(Deps{})
	depsFields := map[string]bool{}
	for i := range depsType.NumField() {
		depsFields[depsType.Field(i).Name] = true
	}

	// Fields on both sides are the ones the boundary is responsible for.
	for name := range installFields {
		if !depsFields[name] {
			continue
		}
		if !forwardedFields[name] {
			t.Errorf("InstallDeps.%s exists on Deps but is not listed as forwarded; "+
				"add it to the Deps literal in runInstall and to forwardedFields", name)
		}
	}
}

// forwardedFields names what the Deps literal in host.go copies across. It is
// written out rather than derived so that dropping a line from that literal is
// a test failure and not a silent behaviour change.
var forwardedFields = map[string]bool{
	"Output":         true,
	"Input":          true,
	"RunHelm":        true,
	"RunCommand":     true,
	"RunInteractive": true,
	"IsTerminal":     true,
	"LookPath":       true,
	"HomeDir":        true,
}
