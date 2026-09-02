package pipelinegen

import (
	"strings"
	"testing"
)

// Every test in this file names the run that made the rule necessary. A rule
// whose failure is not written down is a rule the next person deletes.

// --- gateway-like ----------------------------------------------------------

func TestGatewayLikeIsAdmitted(t *testing.T) {
	t.Parallel()
	admitGenerated(t, generateFor(t, "gateway-like").YAML)
}

// gateway's lockfile carries @biomejs/cli-linux-x64 and nothing else, so
// `npm ci && npm run lint` cannot work on an arm64 runner. Actions never hit
// it because it lints through setup-biome rather than through the package.
func TestGatewayLikeRepairsTheSingleArchitectureLockfile(t *testing.T) {
	t.Parallel()
	result := generateFor(t, "gateway-like")

	if !containsStep(result.Steps, "platform-biome") {
		t.Fatalf("no platform repair step for a lockfile pinning one architecture: %v", result.Steps)
	}
	for _, want := range []string{
		`process.platform + '-' + process.arch`,
		`require('@biomejs/biome/package.json').version`,
		`@biomejs/cli-$platform@$version`,
	} {
		if !strings.Contains(result.YAML, want) {
			t.Errorf("the platform repair is missing %q", want)
		}
	}
	// The repair must be a run-time probe, not a guess at the runner's
	// architecture baked in at generation time.
	if strings.Contains(result.YAML, "@biomejs/cli-linux-x64@") {
		t.Error("the repair hardcodes an architecture instead of asking the runner")
	}
	// And it must never try to install a system package: the root filesystem
	// is read-only, so an apt-get is a step that always fails.
	if strings.Contains(result.YAML, "apt-get") {
		t.Error("generated an apt-get, which can never work on a read-only root filesystem")
	}
}

// gateway declares lint:arch. The generator matched four exact script names,
// so a repository-specific gate was dropped in silence.
func TestGatewayLikeKeepsEveryGateAndSaysWhatItSkipped(t *testing.T) {
	t.Parallel()
	result := generateFor(t, "gateway-like")

	for _, want := range []string{"lint", "lint-arch", "typecheck", "test", "build"} {
		if !containsStep(result.Steps, want) {
			t.Errorf("gate %q was dropped: %v", want, result.Steps)
		}
	}
	// A colon is legal in package.json and is not a legal template name, so
	// the gate is renamed rather than lost.
	if containsStep(result.Steps, "lint:arch") {
		t.Error("a template name still carries a colon")
	}
	// The gates that must NOT run, each with its reason printed.
	for script, reason := range map[string]string{
		"lint:fix":   "rewrites the tree",
		"test:watch": "never exits",
	} {
		if !strings.Contains(result.YAML, "script "+script+" is gate-shaped but is not run") {
			t.Errorf("skipping %q is not explained in the header", script)
		}
		if !strings.Contains(result.YAML, reason) {
			t.Errorf("the reason for skipping %q (%s) is not printed", script, reason)
		}
		if containsStep(result.Steps, templateName(script)) {
			t.Errorf("%q must not be a step", script)
		}
	}
}

// --- rides-like ------------------------------------------------------------

func TestRidesLikeIsAdmitted(t *testing.T) {
	t.Parallel()
	admitGenerated(t, generateFor(t, "rides-like").YAML)
}

// `pnpm run lint --if-present` forwarded the flag to biome, which refused.
// npm consumes --if-present; pnpm does not. No npm-ism may reach a pnpm
// invocation, and since the generator only emits scripts it has read, the
// flag is not needed for any manager.
func TestNoInvocationCarriesIfPresent(t *testing.T) {
	t.Parallel()
	for _, fixture := range []string{"gateway-like", "rides-like", "node"} {
		if body := generateFor(t, fixture).YAML; strings.Contains(body, "--if-present") {
			t.Errorf("%s: an --if-present survived into the generated pipeline", fixture)
		}
	}
}

// rides resolves @transferz from npm.pkg.github.com. Appending an .npmrc line
// with a ${NPM_TOKEN} placeholder does not work under pnpm, which merges
// npmrc files and does not expand the placeholder reliably, so the literal
// travelled to the registry as the token.
func TestRidesLikeInjectsTheRegistryCredentialTheWayPnpmReadsIt(t *testing.T) {
	t.Parallel()
	result := generateFor(t, "rides-like")

	if result.SecretPath != "oberth/upstream/transferz/github-token" {
		t.Fatalf("declared secret path = %q", result.SecretPath)
	}
	want := `config set "//npm.pkg.github.com/:_authToken" "$GITHUB_TOKEN" --location project`
	if !strings.Contains(result.YAML, want) {
		t.Errorf("the pnpm credential form is missing:\nwant %s", want)
	}
	// pnpm is not on the image's PATH; it arrives through npx. A bare `pnpm`
	// is a command-not-found before the install can fail on authentication.
	if strings.Contains(result.YAML, "\npnpm config set") || strings.Contains(result.YAML, " pnpm config set\n") {
		t.Error("the credential line calls pnpm without the runner that fetches it")
	}
	if strings.Contains(result.YAML, "${NPM_TOKEN}") {
		t.Error("an unexpanded npmrc placeholder reached the pipeline")
	}
}

// rides' prepare script runs husky, which shells out to git. The -slim images
// carry no git, so the install died after every package had downloaded. The
// containers have a read-only root filesystem, so installing git is not an
// option; the image that already ships it is.
func TestRidesLikeUsesAnImageThatShipsGit(t *testing.T) {
	t.Parallel()
	result := generateFor(t, "rides-like")

	if strings.Contains(result.YAML, "-trixie-slim@") {
		t.Error("a repository whose prepare script needs git got a slim image")
	}
	if !strings.Contains(result.YAML, "node:22-trixie@sha256:") {
		t.Error("expected the full Debian node variant, which ships git")
	}
	if strings.Contains(result.YAML, "apt-get") {
		t.Error("generated an apt-get on a read-only root filesystem")
	}
}

// The negative half of the platform rule: rides pins biome for both Linux
// architectures, so no repair step belongs in its pipeline. A rule that fires
// on every repository is noise in every log.
func TestRidesLikeNeedsNoPlatformRepair(t *testing.T) {
	t.Parallel()
	for _, name := range generateFor(t, "rides-like").Steps {
		if strings.HasPrefix(name, "platform-") {
			t.Fatalf("a platform repair fired for a lockfile that covers both architectures: %v", name)
		}
	}
}

// rides declares validate:structure and validate:compile, which the old exact
// match dropped, and engines.node >=26, for which there is no pinned image.
func TestRidesLikeKeepsValidateGatesAndWarnsAboutTheNodeMajor(t *testing.T) {
	t.Parallel()
	result := generateFor(t, "rides-like")

	for _, want := range []string{"validate-structure", "validate-compile", "lint", "typecheck", "test", "build"} {
		if !containsStep(result.Steps, want) {
			t.Errorf("gate %q was dropped: %v", want, result.Steps)
		}
	}
	if !strings.Contains(result.YAML, "Node 26 has no pinned image here") {
		t.Error("an unpinnable Node major must be stated in the header, not discovered in a build log")
	}
	// `start` is a long-lived process and is not gate-shaped at all, so it
	// must simply not appear.
	if containsStep(result.Steps, "start") {
		t.Error("the start script became a step")
	}
}

// --- determinism -----------------------------------------------------------

// The stored document is diffed against a regeneration to detect drift, so any
// map iteration order leaking into the output reports drift on every check.
func TestGenerationIsByteStable(t *testing.T) {
	t.Parallel()
	for _, fixture := range []string{"gateway-like", "rides-like", "node", "maven"} {
		first := generateFor(t, fixture).YAML
		for attempt := 0; attempt < 8; attempt++ {
			if again := generateFor(t, fixture).YAML; again != first {
				t.Fatalf("%s: generation is not byte-stable on attempt %d", fixture, attempt)
			}
		}
	}
}

// --- template compression --------------------------------------------------

// The boilerplate every step shares used to be repeated verbatim, putting
// roughly forty-five lines behind each step, so adding one by hand meant
// copying all of them correctly.
func TestSharedBlocksAreWrittenOnceAndAliased(t *testing.T) {
	t.Parallel()
	result := generateFor(t, "gateway-like")

	for _, anchor := range []string{"env: &env", "volumeMounts: &mounts", "resources: &resources"} {
		if count := strings.Count(result.YAML, anchor); count != 1 {
			t.Errorf("anchor %q appears %d times, want exactly 1", anchor, count)
		}
	}
	steps := len(result.Steps)
	for _, alias := range []string{"env: *env", "volumeMounts: *mounts", "resources: *resources"} {
		if count := strings.Count(result.YAML, alias); count != steps-1 {
			t.Errorf("alias %q appears %d times, want %d (one per step after the first)", alias, count, steps-1)
		}
	}
	// The compression is only real if admission still sees the same
	// structure, which is the whole reason value anchors were chosen over
	// merge keys.
	admitGenerated(t, result.YAML)
}

// --- engine awareness ------------------------------------------------------

// The docker engine mounts /work itself and refuses a repository-declared
// volumeMount outright, so the shape the Argo engine requires is the shape the
// docker engine rejects.
func TestDockerEngineGetsNoMountsItWouldRefuse(t *testing.T) {
	t.Parallel()
	root := materialize(t, "gateway-like")
	project := DetectProject(root)
	project.Org = "transferz"
	project.Engine = EngineDocker

	document := Generate(project).YAML
	for _, forbidden := range []string{"volumeMounts", "volumeClaimTemplates"} {
		if strings.Contains(document, forbidden) {
			t.Errorf("a docker-engine pipeline declares %s, which that engine refuses", forbidden)
		}
	}
	admitGenerated(t, document)

	// The build tree is at the same path under both engines, so no step
	// script changes.
	if !strings.Contains(document, "workingDir: /work/build") {
		t.Error("the docker-engine pipeline lost its working directory")
	}
}

// The Argo engine still needs both, so the switch must not have removed them
// for everyone.
func TestArgoEngineKeepsTheClaimAndTheMounts(t *testing.T) {
	t.Parallel()
	document := generateFor(t, "gateway-like").YAML
	for _, want := range []string{"volumeClaimTemplates", "volumeMounts: &mounts"} {
		if !strings.Contains(document, want) {
			t.Errorf("an Argo pipeline is missing %s", want)
		}
	}
}
