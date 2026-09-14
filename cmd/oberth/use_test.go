package main

import (
	"strings"
	"testing"

	"github.com/oberthci/oberth/internal/clientprofile"
)

func TestProfileSSHCommandFallsBackToThePinnedHostKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := clientprofile.Write("srv", "export OBERTH_BASE_URL=\"https://srv\"\n", []byte("ca"), nil); err != nil {
		t.Fatal(err)
	}
	profile, err := clientprofile.Load("srv")
	if err != nil {
		t.Fatal(err)
	}
	if got := profileSSHCommand(profile); got != "" {
		t.Fatalf("no known_hosts yet, expected no command, got %q", got)
	}
	known, err := clientprofile.WriteKnownHosts("srv", "srv:30022", "ssh-ed25519 AAAA")
	if err != nil {
		t.Fatal(err)
	}
	if got := profileSSHCommand(profile); !strings.Contains(got, known) {
		t.Fatalf("expected the pinned known_hosts in %q", got)
	}
}
