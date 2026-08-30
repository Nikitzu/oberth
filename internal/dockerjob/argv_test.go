package dockerjob

import (
	"strings"
	"testing"

	"github.com/oberthci/oberth/pkg/periapsis"
)

func newTestController(t *testing.T) *Controller {
	t.Helper()
	controller, err := NewController(Config{})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	return controller
}

func argumentValue(arguments []string, flag string) (string, bool) {
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == flag {
			return arguments[index+1], true
		}
	}
	return "", false
}

// The image and its argv are positional. Without a terminator the CLI reads a
// leading-dash image as one of its own flags, which is the one way a value
// carried by the repository document reaches docker's own option parser.
func TestCreateArgumentsTerminateFlagsBeforeTheImage(t *testing.T) {
	controller := newTestController(t)
	step := Step{Burn: "build", Step: "compile", Image: "--privileged", Command: []string{"sh"}, Args: []string{"-c", "true"}}
	arguments := controller.createArguments(Request{Name: "job", RunID: "run"}, step, 0)
	terminator := -1
	for index, value := range arguments {
		if value == "--" {
			terminator = index
		}
	}
	if terminator < 0 {
		t.Fatalf("no -- terminator in argv: %v", arguments)
	}
	if arguments[terminator+1] != "--privileged" {
		t.Fatalf("image is not the first positional after the terminator: %v", arguments[terminator:])
	}
	for _, later := range arguments[terminator+1:] {
		if later == "--volume" || later == "--label" {
			t.Fatalf("an engine flag appears after the terminator: %v", arguments)
		}
	}
}

// Declared limits must reach the container. Before this they were dropped, so
// a step bounded to two cores on a cluster ran unbounded here.
func TestCreateArgumentsCarryDeclaredLimits(t *testing.T) {
	controller := newTestController(t)
	step := Step{Burn: "build", Step: "compile", Image: "golang", CPULimit: 1.5, MemoryLimitBytes: 4 << 30}
	arguments := controller.createArguments(Request{Name: "job", RunID: "run"}, step, 0)
	if value, ok := argumentValue(arguments, "--cpus"); !ok || value != "1.5" {
		t.Fatalf("--cpus: got %q ok=%v", value, ok)
	}
	if value, ok := argumentValue(arguments, "--memory"); !ok || value != "4294967296" {
		t.Fatalf("--memory: got %q ok=%v", value, ok)
	}
}

func TestCreateArgumentsOmitLimitsThatWereNotDeclared(t *testing.T) {
	controller := newTestController(t)
	arguments := controller.createArguments(Request{Name: "job", RunID: "run"},
		Step{Burn: "build", Step: "compile", Image: "golang"}, 0)
	if _, ok := argumentValue(arguments, "--cpus"); ok {
		t.Fatalf("--cpus present without a declared limit: %v", arguments)
	}
	if _, ok := argumentValue(arguments, "--memory"); ok {
		t.Fatalf("--memory present without a declared limit: %v", arguments)
	}
}

// The server's own variables are appended last so a repository declaration of
// the same name cannot win: docker resolves a repeated --env to the last one.
func TestStepEnvironmentPutsServerValuesLast(t *testing.T) {
	controller := newTestController(t)
	request := Request{Name: "job", RunID: "run-1", Repo: "acme/widget", Ref: "main", SHA: "abc", Trigger: periapsis.TriggerCI}
	environment := controller.stepEnvironment(request, Step{Env: []string{"OBERTH_SHA=forged", "MY_VAR=mine"}})
	last := map[string]string{}
	for _, variable := range environment {
		name, value, _ := strings.Cut(variable, "=")
		last[name] = value
	}
	if last["OBERTH_SHA"] != "abc" {
		t.Fatalf("a repository declaration shadowed OBERTH_SHA: %q", last["OBERTH_SHA"])
	}
	if last["MY_VAR"] != "mine" {
		t.Fatalf("a repository declaration was dropped: %q", last["MY_VAR"])
	}
}
