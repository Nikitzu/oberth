package dockerjob

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// proxyImage is tecnativa/docker-socket-proxy 0.3.0, pinned by digest: the
// only container in a run that sees the daemon socket, so which bytes it runs
// is not a detail.
const proxyImage = "tecnativa/docker-socket-proxy@sha256:9e4b9e7517a6b660f2cc903a19b257b1852d5b3344794e3ea334ff00ae677ac2"

// The same name a kubedock pipeline already uses, so one document runs on
// both engines. "docker" is for a document that names the plain default.
const (
	proxyAliasKubedock = "kubedock"
	proxyAliasDocker   = "docker"
	proxyPortKubedock  = "2475"
	proxyPortDocker    = "2375"
	dockerSocket       = "/var/run/docker.sock"
	defaultGatewayName = "host.docker.internal"
	proxyMemoryBytes   = 64 << 20
)

// proxyEndpoints is the haproxy allowlist. Everything a Testcontainers
// module needs to start, inspect, exec into, log and remove a container and
// its network; nothing that builds images, touches volumes, or reaches swarm.
var proxyEndpoints = []string{
	"CONTAINERS=1", "EXEC=1", "IMAGES=1", "NETWORKS=1", "POST=1",
	"INFO=1", "PING=1", "VERSION=1", "EVENTS=1",
	"BUILD=0", "COMMIT=0", "VOLUMES=0", "SECRETS=0", "SWARM=0",
	"SYSTEM=0", "PLUGINS=0", "AUTH=0", "ALLOW_START=1", "ALLOW_STOP=1", "ALLOW_RESTARTS=1",
}

func (controller *Controller) proxyName(name string) string { return name + "-testcontainers" }

func (controller *Controller) gatewayName() string {
	if gateway := strings.TrimSpace(controller.config.SecretStore.HostGatewayName); gateway != "" {
		return gateway
	}
	return defaultGatewayName
}

// proxyCreateArguments is the argv for the run's socket proxy. The image's
// entrypoint binds 2375 only, so the config template is rendered here with
// both ports and haproxy started directly.
func (controller *Controller) proxyCreateArguments(request Request) []string {
	arguments := []string{
		"create",
		"--name", controller.proxyName(request.Name),
		"--network", controller.networkName(request.Name),
		"--network-alias", proxyAliasKubedock,
		"--network-alias", proxyAliasDocker,
		"--volume", dockerSocket + ":" + dockerSocket + ":ro",
		"--label", labelJob + "=" + request.Name,
		"--label", labelRun + "=" + request.RunID,
		"--cpus", "0.25",
		"--memory", fmt.Sprintf("%d", proxyMemoryBytes),
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
	}
	for _, variable := range proxyEndpoints {
		arguments = append(arguments, "--env", variable)
	}
	arguments = append(arguments, "--entrypoint", "sh", "--", proxyImage, "-c",
		`sed "s/\${BIND_CONFIG}/:`+proxyPortKubedock+`,:`+proxyPortDocker+`/g" /usr/local/etc/haproxy/haproxy.cfg.template > /tmp/haproxy.cfg && exec haproxy -W -db -f /tmp/haproxy.cfg`)
	return arguments
}

// testcontainersEnvironment is what a declared step gets, minus anything the
// document set itself: a pipeline written for kubedock already names
// DOCKER_HOST and must keep its own value.
func (controller *Controller) testcontainersEnvironment(step Step) []string {
	defaults := []string{
		"DOCKER_HOST=tcp://" + proxyAliasKubedock + ":" + proxyPortKubedock,
		"TESTCONTAINERS_RYUK_DISABLED=true",
		"TESTCONTAINERS_HOST_OVERRIDE=" + controller.gatewayName(),
		"TESTCONTAINERS_CHECKS_DISABLE=true",
	}
	var out []string
	for _, candidate := range defaults {
		key := candidate[:strings.Index(candidate, "=")+1]
		set := false
		for _, existing := range step.Env {
			if strings.HasPrefix(existing, key) {
				set = true
				break
			}
		}
		if !set {
			out = append(out, candidate)
		}
	}
	return out
}

// Testcontainers labels every container and network it creates. Ryuk, its
// own reaper, needs a socket it will not get, so the engine reaps instead:
// everything with the label that was not there when the run started.
const testcontainersLabel = "org.testcontainers=true"

func newSince(before, after []string) []string {
	seen := make(map[string]bool, len(before))
	for _, id := range before {
		seen[id] = true
	}
	var out []string
	for _, id := range after {
		if !seen[id] {
			out = append(out, id)
		}
	}
	return out
}

type testcontainersSnapshot struct {
	containers []string
	networks   []string
}

func (controller *Controller) snapshotTestcontainers(ctx context.Context) testcontainersSnapshot {
	containers, _ := controller.client.run(ctx, "ps", "--all", "--no-trunc",
		"--filter", "label="+testcontainersLabel, "--format", "{{.ID}}")
	networks, _ := controller.client.run(ctx, "network", "ls",
		"--filter", "label="+testcontainersLabel, "--format", "{{.ID}}")
	return testcontainersSnapshot{containers: strings.Fields(containers), networks: strings.Fields(networks)}
}

// startProxy creates and starts the run's proxy. It runs after the network
// exists and before any step, so the alias resolves from the first step on.
func (controller *Controller) startProxy(ctx context.Context, request Request) error {
	if _, err := controller.client.run(ctx, controller.proxyCreateArguments(request)...); err != nil {
		return fmt.Errorf("dockerjob: create the testcontainers proxy: %w", err)
	}
	if _, err := controller.client.run(ctx, "start", controller.proxyName(request.Name)); err != nil {
		return fmt.Errorf("dockerjob: start the testcontainers proxy: %w", err)
	}
	return nil
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// reapTestcontainers removes what the run's tests left behind, then the
// proxy. Every failure is logged and swallowed: a leaked test container is a
// leaked process, not a wrong verdict.
func (controller *Controller) reapTestcontainers(ctx context.Context, name string,
	before testcontainersSnapshot, log io.Writer) {
	after := controller.snapshotTestcontainers(ctx)
	for _, id := range newSince(before.containers, after.containers) {
		if _, err := controller.client.run(ctx, "rm", "--force", "--volumes", id); err != nil {
			fmt.Fprintf(log, "testcontainers: could not remove container %s: %v\n", shortID(id), err)
			continue
		}
		fmt.Fprintf(log, "testcontainers: removed container %s\n", shortID(id))
	}
	for _, id := range newSince(before.networks, after.networks) {
		if _, err := controller.client.run(ctx, "network", "rm", id); err != nil {
			fmt.Fprintf(log, "testcontainers: could not remove network %s: %v\n", shortID(id), err)
			continue
		}
		fmt.Fprintf(log, "testcontainers: removed network %s\n", shortID(id))
	}
	_, _ = controller.client.run(ctx, "rm", "--force", controller.proxyName(name))
}

func (controller *Controller) DaemonVersion(ctx context.Context) (string, error) {
	return controller.client.run(ctx, "version", "--format", "{{.Server.Version}}")
}

// SupportsHostGateway is Docker 20.10 or newer, where --add-host host-gateway exists.
func SupportsHostGateway(version string) bool {
	parts := strings.SplitN(strings.TrimSpace(version), ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return major > 20 || (major == 20 && minor >= 10)
}
