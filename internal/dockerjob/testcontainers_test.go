package dockerjob

import (
	"strings"
	"testing"
)

func TestProxyCreateArgumentsMountTheSocketReadOnlyAndAliasBothNames(t *testing.T) {
	controller := newTestController(t)
	arguments := controller.proxyCreateArguments(Request{Name: "job", RunID: "run"})
	joined := strings.Join(arguments, " ")
	for _, want := range []string{
		"--name job-testcontainers",
		"--network job-net",
		"--network-alias kubedock",
		"--network-alias docker",
		"--volume /var/run/docker.sock:/var/run/docker.sock:ro",
		"--label " + labelJob + "=job",
		"--label " + labelRun + "=run",
		"--cpus 0.25",
		"--memory 67108864",
		"--env CONTAINERS=1", "--env EXEC=1", "--env IMAGES=1", "--env NETWORKS=1", "--env POST=1",
		"--env INFO=1", "--env PING=1", "--env VERSION=1", "--env EVENTS=1",
		"--env BUILD=0", "--env COMMIT=0", "--env VOLUMES=0", "--env SECRETS=0", "--env SWARM=0",
		"--env SYSTEM=0", "--env PLUGINS=0", "--env AUTH=0",
		proxyImage,
		":2475,:2375",
		"timeout http-keep-alive 10m",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("proxy argv lacks %q: %v", want, arguments)
		}
	}
	for _, forbidden := range []string{"--privileged", "docker.sock:/var/run/docker.sock:rw", "docker.sock:/var/run/docker.sock "} {
		if strings.Contains(joined+" ", forbidden) {
			t.Fatalf("proxy argv contains %q: %v", forbidden, arguments)
		}
	}
}

func TestDeclaredStepsGetTheDockerHostAndGateway(t *testing.T) {
	controller := newTestController(t)
	step := Step{Burn: "test", Step: "test", Image: "maven"}
	arguments := controller.createArguments(Request{Name: "job", RunID: "run", Testcontainers: true}, step, 0)
	joined := strings.Join(arguments, " ")
	for _, want := range []string{
		"--env DOCKER_HOST=tcp://kubedock:2475",
		"--env TESTCONTAINERS_RYUK_DISABLED=true",
		"--env TESTCONTAINERS_HOST_OVERRIDE=host.docker.internal",
		"--env TESTCONTAINERS_CHECKS_DISABLE=true",
		"--add-host host.docker.internal:host-gateway",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("declared step argv lacks %q: %v", want, arguments)
		}
	}
	if strings.Contains(joined, "docker.sock") {
		t.Fatalf("a step container sees the socket: %v", arguments)
	}
}

func TestDeclaredStepsKeepADocumentSetDockerHost(t *testing.T) {
	controller := newTestController(t)
	step := Step{Burn: "test", Step: "test", Image: "maven", Env: []string{"DOCKER_HOST=tcp://kubedock:2475"}}
	arguments := controller.createArguments(Request{Name: "job", RunID: "run", Testcontainers: true}, step, 0)
	count := 0
	for _, value := range arguments {
		if strings.HasPrefix(value, "DOCKER_HOST=") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("DOCKER_HOST set %d times, want once (the document's): %v", count, arguments)
	}
}

func TestUndeclaredStepsGetNothing(t *testing.T) {
	controller := newTestController(t)
	step := Step{Burn: "test", Step: "test", Image: "maven"}
	arguments := controller.createArguments(Request{Name: "job", RunID: "run"}, step, 0)
	joined := strings.Join(arguments, " ")
	for _, forbidden := range []string{"DOCKER_HOST", "TESTCONTAINERS_", "--add-host"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("undeclared step argv carries %q: %v", forbidden, arguments)
		}
	}
}

func TestGatewayNameFollowsTheSecretStoreSetting(t *testing.T) {
	controller, err := NewController(Config{SecretStore: SecretStoreConfig{HostGatewayName: "gateway.local"}})
	if err != nil {
		t.Fatal(err)
	}
	step := Step{Burn: "test", Step: "test", Image: "maven"}
	joined := strings.Join(controller.createArguments(Request{Name: "job", RunID: "run", Testcontainers: true}, step, 0), " ")
	if !strings.Contains(joined, "TESTCONTAINERS_HOST_OVERRIDE=gateway.local") || !strings.Contains(joined, "--add-host gateway.local:host-gateway") {
		t.Fatalf("gateway name not taken from the secret store setting: %s", joined)
	}
}

func TestReapSetKeepsWhatExistedBefore(t *testing.T) {
	before := []string{"aaa", "bbb"}
	after := []string{"aaa", "bbb", "ccc", "ddd"}
	got := newSince(before, after)
	if strings.Join(got, ",") != "ccc,ddd" {
		t.Fatalf("newSince = %v, want [ccc ddd]", got)
	}
	if len(newSince(after, before)) != 0 {
		t.Fatal("nothing new must reap nothing")
	}
}

func TestSupportsHostGateway(t *testing.T) {
	for version, want := range map[string]bool{"20.10.0": true, "24.0.7": true, "28.3.2-orbstack": true, "19.03.15": false, "20.9.1": false, "": false} {
		if got := SupportsHostGateway(version); got != want {
			t.Fatalf("SupportsHostGateway(%q) = %v, want %v", version, got, want)
		}
	}
}
