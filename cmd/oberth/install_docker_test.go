package main

import (
	"slices"
	"testing"

	"github.com/oberthci/oberth/internal/localinstall"
)

func TestLocalServeArgumentsCarryTestcontainers(t *testing.T) {
	layout := localinstall.Layout{Data: "/d", Database: "/db", TLSCert: "/c", TLSKey: "/k", SSHHostKey: "/h", Upstream: "/u", KnownHosts: "/kh"}
	with := localServeArguments(layout, 8443, 2222, true, "", nil, "docker", true)
	without := localServeArguments(layout, 8443, 2222, true, "", nil, "docker", false)
	if !slices.Contains(with, "--testcontainers") {
		t.Fatalf("flag not persisted: %v", with)
	}
	if slices.Contains(without, "--testcontainers") {
		t.Fatalf("flag persisted when not asked: %v", without)
	}
}
