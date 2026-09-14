package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestInstallWizardDefaultsToDockerWithAServiceAndTheShellLine(t *testing.T) {
	var out bytes.Buffer
	arguments, docker, err := askInstallChoices(context.Background(), strings.NewReader("\n\n\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	if !docker {
		t.Fatal("an empty answer must pick docker")
	}
	want := "--engine=docker --secretstore --service --shell-profile=yes"
	if got := strings.Join(arguments, " "); got != want {
		t.Fatalf("arguments = %q, want %q", got, want)
	}
	if !strings.Contains(out.String(), "oberth install "+want) {
		t.Fatalf("the equivalent flag line must be printed:\n%s", out.String())
	}
}

func TestInstallWizardHonoursNoOnBothFollowUps(t *testing.T) {
	var out bytes.Buffer
	arguments, _, err := askInstallChoices(context.Background(), strings.NewReader("1\nn\nno\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(arguments, " "); got != "--engine=docker --secretstore --shell-profile=no" {
		t.Fatalf("arguments = %q", got)
	}
}

func TestInstallWizardKubeLeavesTheClusterPathAlone(t *testing.T) {
	var out bytes.Buffer
	arguments, docker, err := askInstallChoices(context.Background(), strings.NewReader("2\n"), &out)
	if err != nil || docker || arguments != nil {
		t.Fatalf("kube: arguments=%v docker=%v err=%v", arguments, docker, err)
	}
	if got := stripEngineKube([]string{"--engine=kube", "--install-secretstore", "--engine", "argo", "-f", "v.yaml"}); strings.Join(got, " ") != "--install-secretstore -f v.yaml" {
		t.Fatalf("stripEngineKube = %v", got)
	}
	if !engineArgumentPresent([]string{"--engine", "docker"}) || engineArgumentPresent([]string{"--install-secretstore"}) {
		t.Fatal("engineArgumentPresent misread the flags")
	}
}
