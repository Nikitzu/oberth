package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func claudeModDeps(onPath bool, calls *[]string) Deps {
	return Deps{
		Output: &strings.Builder{},
		LookPath: func(name string) (string, error) {
			if onPath {
				return "/usr/local/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		RunCommand: func(_ context.Context, _ []byte, name string, args ...string) ([]byte, error) {
			*calls = append(*calls, name+" "+strings.Join(args, " "))
			return nil, nil
		},
	}
}

func TestClaudeModYesInstallsTheModForEverySession(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var calls []string
	var out strings.Builder
	offerClaudeMod(context.Background(), Config{ClaudeMod: "yes"}, claudeModDeps(true, &calls), newTableWriter(&out, false), root)

	if len(calls) != 2 || !strings.HasPrefix(calls[0], "claude plugin marketplace add ") || calls[1] != "claude plugin install oberth-watch@oberth --scope user" {
		t.Fatalf("calls = %q", calls)
	}
	if _, err := os.Stat(filepath.Join(root, "claude-mods", "oberth-watch", "hooks", "register.tsx")); err != nil {
		t.Fatalf("mod not written: %v", err)
	}
	if !strings.Contains(out.String(), "✓ installed") {
		t.Fatalf("table = %s", out.String())
	}
}

func TestClaudeModIsLeftAloneWhenDeclinedOrUnasked(t *testing.T) {
	t.Parallel()
	for _, choice := range []string{"no", ""} {
		var calls []string
		var out strings.Builder
		offerClaudeMod(context.Background(), Config{ClaudeMod: choice}, claudeModDeps(true, &calls), newTableWriter(&out, false), t.TempDir())
		if len(calls) != 0 || !strings.Contains(out.String(), "skipped") {
			t.Fatalf("--claude-mod %q: calls = %q, table = %s", choice, calls, out.String())
		}
	}
}

func TestClaudeModWithoutClaudeIsAManualStep(t *testing.T) {
	t.Parallel()
	var calls []string
	var out strings.Builder
	offerClaudeMod(context.Background(), Config{ClaudeMod: "yes"}, claudeModDeps(false, &calls), newTableWriter(&out, false), t.TempDir())
	if len(calls) != 0 || !strings.Contains(out.String(), "claude is not on PATH") {
		t.Fatalf("calls = %q, table = %s", calls, out.String())
	}
}

func TestAnUnknownClaudeModAnswerIsRefused(t *testing.T) {
	t.Parallel()
	err := (&Config{ClaudeMod: "true"}).Validate()
	if err == nil || !strings.Contains(err.Error(), "--claude-mod") {
		t.Fatalf("err = %v, want a usage error", err)
	}
	for _, accepted := range []string{"", "yes", "no", " YES "} {
		if err := (&Config{ClaudeMod: accepted}).Validate(); err != nil {
			t.Fatalf("--claude-mod %q was refused: %v", accepted, err)
		}
	}
}
