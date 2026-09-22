package pipelinegen

import (
	"strings"
	"testing"
)

func TestComposeStepsPlacesAFragmentBeforeTheStepItNames(t *testing.T) {
	names := []string{"install", "test", "build"}
	fragments := []FragmentUse{{Ref: "org/jev@v1", Steps: []string{"jev"}, Before: "test"}}
	got := strings.Join(composeSteps(names, fragments), " ")
	if got != "install jev test build" {
		t.Fatalf("composeSteps = %q", got)
	}
}

func TestComposeStepsAppendsWhenBeforeIsEmptyOrUnknown(t *testing.T) {
	names := []string{"install", "test"}
	for _, before := range []string{"", "deploy"} {
		fragments := []FragmentUse{{Ref: "org/jev@v1", Steps: []string{"jev"}, Before: before}}
		got := strings.Join(composeSteps(names, fragments), " ")
		if got != "install test jev" {
			t.Fatalf("before %q: composeSteps = %q", before, got)
		}
	}
}

func TestComposeStepsKeepsFragmentOrderAtTheSameAnchor(t *testing.T) {
	names := []string{"test"}
	fragments := []FragmentUse{
		{Ref: "a", Steps: []string{"one"}, Before: "test"},
		{Ref: "b", Steps: []string{"two"}, Before: "test"},
	}
	got := strings.Join(composeSteps(names, fragments), " ")
	if got != "one two test" {
		t.Fatalf("composeSteps = %q", got)
	}
}
