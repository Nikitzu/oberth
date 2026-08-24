package api

import (
	"regexp"
	"strings"
	"testing"
)

var stepButtonPattern = regexp.MustCompile(`<button class="bs \$\{esc\(sk\)\}"[^>]*>`)

func TestAStepButtonCarriesTheStepItRepresents(t *testing.T) {
	t.Parallel()
	script, ok := staticAssets["app.js"]
	if !ok {
		t.Fatal("app.js is not embedded")
	}
	body := string(script.body)

	matches := stepButtonPattern.FindAllString(body, -1)
	if len(matches) == 0 {
		t.Fatal("no step button found; this guard would pass vacuously")
	}
	for _, match := range matches {
		if !strings.Contains(match, "data-step-key") {
			t.Fatalf("a step button carries only a run ID, so clicking a step opens the run with the "+
				"step discarded and every step leads to the same view: %s", match)
		}
	}
}

func TestTheRunLinkHandlerHonoursAStepKey(t *testing.T) {
	t.Parallel()
	script, ok := staticAssets["app.js"]
	if !ok {
		t.Fatal("app.js is not embedded")
	}
	body := string(script.body)
	if !strings.Contains(body, "target.dataset.stepKey") {
		t.Fatal("the click handler never reads data-step-key, so setting it on a button changes nothing")
	}
}
