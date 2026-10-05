package claudemod

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteLaysOutTheMarketplace(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, Plugin, "stale"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir); err != nil {
		t.Fatal(err)
	}

	var market struct {
		Name    string
		Plugins []struct{ Name, Source string }
	}
	body, err := os.ReadFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &market); err != nil {
		t.Fatal(err)
	}
	if market.Name != Marketplace || len(market.Plugins) != 1 || market.Plugins[0].Source != "./"+Plugin {
		t.Fatalf("marketplace = %+v", market)
	}
	for _, want := range []string{".claude-plugin/plugin.json", "hooks/hooks.json", "hooks/register.tsx", "types/index.d.ts"} {
		if _, err := os.Stat(filepath.Join(dir, Plugin, want)); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, Plugin, "hooks", "register.test.ts")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the mod's test was installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, Plugin, "stale")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a previous install's file survived: %v", err)
	}
}

func TestInstallRegistersTheMarketplaceThenThePlugin(t *testing.T) {
	dir := t.TempDir()
	var calls []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	if err := Install(context.Background(), dir, run); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"claude plugin marketplace add " + dir + " --scope user",
		"claude plugin install oberth-watch@oberth --scope user",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %q, want %q", calls, want)
	}
}

func TestInstallReportsAFailedRegistration(t *testing.T) {
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[1] == "install" {
			return []byte("plugin not found"), errors.New("exit status 1")
		}
		return nil, nil
	}
	err := Install(context.Background(), t.TempDir(), run)
	if err == nil || !strings.Contains(err.Error(), "plugin not found") {
		t.Fatalf("err = %v", err)
	}
}
