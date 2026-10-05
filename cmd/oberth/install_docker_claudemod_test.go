package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDockerInstallClaudeMod(t *testing.T) {
	onPath := func(name string) (string, error) { return "/usr/local/bin/" + name, nil }
	notOnPath := func(string) (string, error) { return "", errors.New("not found") }
	cases := []struct {
		name     string
		choice   string
		lookPath func(string) (string, error)
		calls    int
		says     string
	}{
		{"yes installs", "yes", onPath, 2, "installed for every Claude Code session"},
		{"empty only tells how", "", onPath, 0, "--claude-mod=yes"},
		{"no says nothing", "no", onPath, 0, ""},
		{"yes without claude", "yes", notOnPath, 0, "claude is not on PATH"},
		{"empty without claude says nothing", "", notOnPath, 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out strings.Builder
			calls := 0
			run := func(context.Context, string, ...string) ([]byte, error) { calls++; return nil, nil }
			installClaudeModLocally(context.Background(), &out, c.choice, t.TempDir(), c.lookPath, run)
			if calls != c.calls {
				t.Fatalf("calls = %d, want %d", calls, c.calls)
			}
			if c.says == "" && out.Len() != 0 || c.says != "" && !strings.Contains(out.String(), c.says) {
				t.Fatalf("output = %q, want %q", out.String(), c.says)
			}
		})
	}
}

func TestDockerInstallRefusesAnUnknownClaudeModAnswer(t *testing.T) {
	err := runInstallDocker(context.Background(), []string{"--claude-mod", "true"}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "--claude-mod") {
		t.Fatalf("err = %v", err)
	}
}
