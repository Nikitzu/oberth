package api

import (
	"strings"
	"testing"
)

func TestRunDetailRefreshKeepsResolvedBreadcrumb(t *testing.T) {
	t.Parallel()
	script, ok := staticAssets["app.js"]
	if !ok {
		t.Fatal("app.js is not embedded")
	}
	source := string(script.body)
	start := strings.Index(source, "async function renderRunDetail(runID, seq, background)")
	view := strings.Index(source, "async function renderRunDetailView(")
	if start < 0 || view <= start {
		t.Fatal("app.js is missing the run-detail renderers")
	}
	refresh := source[start:view]
	if !strings.Contains(refresh, `if (!background) setChrome("runs");`) {
		t.Fatal("run-detail chrome is not limited to foreground navigation")
	}
	if strings.Count(refresh, "setChrome(") != 1 {
		t.Fatal("run-detail refresh contains another chrome update")
	}
	routeStart := strings.Index(source, "async function route(background)")
	routeEnd := strings.Index(source, "/* ---------- events ---------- */")
	if routeStart < 0 || routeEnd <= routeStart {
		t.Fatal("app.js is missing the router")
	}
	router := source[routeStart:routeEnd]
	if !strings.Contains(router, "renderRunDetail(decodeURIComponent(match[1]), seq, background)") {
		t.Fatal("run-detail route does not pass the background refresh state")
	}
	if strings.Contains(router, "setChrome(") {
		t.Fatal("router resets chrome during background refresh")
	}
	viewEnd := strings.Index(source[view:], "\nasync function renderRepos(")
	if viewEnd < 0 {
		t.Fatal("app.js is missing the renderer after run detail")
	}
	if !strings.Contains(source[view:view+viewEnd], `setChrome("runs");`) {
		t.Fatal("run-detail view no longer marks runs as the active section")
	}
}

func TestRunDetailKeepsQueuedBurnsPending(t *testing.T) {
	t.Parallel()
	script, ok := staticAssets["app.js"]
	if !ok {
		t.Fatal("app.js is not embedded")
	}
	source := string(script.body)
	if !strings.Contains(source, `kinds.includes("pending") ? "pending"`) {
		t.Fatal("queued steps can repaint their burn as passed")
	}
	if !strings.Contains(source, `burn.kind === "pending" ? "queued"`) {
		t.Fatal("queued burn headers do not render the queued state")
	}
}
