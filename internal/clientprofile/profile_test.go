package clientprofile

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestProfilesRoundTripAndTheDefaultIsACopy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	env := "export OBERTH_BASE_URL=\"https://one.example:8443\"\nexport OBERTH_CA_CERT=\"/p/ca.crt\"\nexport OBERTH_TOKEN_COMMAND=\"pass show oberth/one/token\"\nexport OBERTH_SSH_COMMAND=\"/p/git-ssh\"\n"
	if _, err := Write("one", env, []byte("CA"), []byte("{}")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := Write("two", "export OBERTH_BASE_URL=\"https://two.example:30443\"\n", []byte("CB"), nil); err != nil {
		t.Fatalf("Write two: %v", err)
	}
	names, err := List()
	if err != nil || len(names) != 2 || names[0] != "one" || names[1] != "two" {
		t.Fatalf("List = %v, %v", names, err)
	}
	one, err := Load("one")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if one.BaseURL != "https://one.example:8443" || one.TokenCommand != "pass show oberth/one/token" || one.SSHCommand != "/p/git-ssh" || one.CACert != "/p/ca.crt" {
		t.Fatalf("Load = %+v", one)
	}
	if err := SetDefault("two"); err != nil {
		t.Fatalf("SetDefault: %v", err)
	}
	root, _ := Root()
	body, _ := os.ReadFile(filepath.Join(root, "env"))
	if string(body) != "export OBERTH_BASE_URL=\"https://two.example:30443\"\n" || Default() != "two" {
		t.Fatalf("the root env is not a copy of the default profile: %q, default %q", body, Default())
	}
	if _, err := os.Stat(filepath.Join(root, "ca.crt")); err != nil {
		t.Fatal("the root ca.crt was not copied")
	}
	if _, err := Load("three"); err == nil {
		t.Fatal("loading a missing profile must fail")
	}
	if err := ValidateName("../etc"); err == nil {
		t.Fatal("a path-shaped name must be refused")
	}
}

func TestACheckoutPinsAProfile(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"}} {
		if err := exec.Command("git", append([]string{"-C", dir}, args...)...).Run(); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if got := ForCheckout(dir); got != "" {
		t.Fatalf("unpinned checkout reports %q", got)
	}
	if err := Pin(dir, "server"); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if got := ForCheckout(dir); got != "server" {
		t.Fatalf("ForCheckout = %q", got)
	}
}
