package pipelinegen

import (
	"strings"
	"testing"
)

// Every test in this file names the run that made the rule necessary. A rule
// whose failure is not written down is a rule the next person deletes.

// --- npm-arch-lockfile ----------------------------------------------------------

func TestGatewayLikeIsAdmitted(t *testing.T) {
	t.Parallel()
	admitGenerated(t, generateFor(t, "npm-arch-lockfile").YAML)
}

// gateway's lockfile carries @biomejs/cli-linux-x64 and nothing else, so
// `npm ci && npm run lint` cannot work on an arm64 runner. Actions never hit
// it because it lints through setup-biome rather than through the package.
func TestGatewayLikeRepairsTheSingleArchitectureLockfile(t *testing.T) {
	t.Parallel()
	result := generateFor(t, "npm-arch-lockfile")

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
	result := generateFor(t, "npm-arch-lockfile")

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

// --- pnpm-scoped-registry ------------------------------------------------------------

func TestRidesLikeIsAdmitted(t *testing.T) {
	t.Parallel()
	admitGenerated(t, generateFor(t, "pnpm-scoped-registry").YAML)
}

// `pnpm run lint --if-present` forwarded the flag to biome, which refused.
// npm consumes --if-present; pnpm does not. No npm-ism may reach a pnpm
// invocation, and since the generator only emits scripts it has read, the
// flag is not needed for any manager.
func TestNoInvocationCarriesIfPresent(t *testing.T) {
	t.Parallel()
	for _, fixture := range []string{"npm-arch-lockfile", "pnpm-scoped-registry", "node"} {
		if body := generateFor(t, fixture).YAML; strings.Contains(body, "--if-present") {
			t.Errorf("%s: an --if-present survived into the generated pipeline", fixture)
		}
	}
}

// rides resolves @acme from packages.forge.example. Appending an .npmrc line
// with a ${NPM_TOKEN} placeholder does not work under pnpm, which merges
// npmrc files and does not expand the placeholder reliably, so the literal
// travelled to the registry as the token.
func TestRidesLikeInjectsTheRegistryCredentialTheWayPnpmReadsIt(t *testing.T) {
	t.Parallel()
	result := generateFor(t, "pnpm-scoped-registry")

	if result.SecretPath != "oberth/upstream/acme/github-token" {
		t.Fatalf("declared secret path = %q", result.SecretPath)
	}
	want := `config set "//packages.forge.example/:_authToken" "$GITHUB_TOKEN" --location project`
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
	result := generateFor(t, "pnpm-scoped-registry")

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
	for _, name := range generateFor(t, "pnpm-scoped-registry").Steps {
		if strings.HasPrefix(name, "platform-") {
			t.Fatalf("a platform repair fired for a lockfile that covers both architectures: %v", name)
		}
	}
}

// rides declares validate:structure and validate:compile, which the old exact
// match dropped, and engines.node >=26, for which there is no pinned image.
func TestRidesLikeKeepsValidateGatesAndWarnsAboutTheNodeMajor(t *testing.T) {
	t.Parallel()
	result := generateFor(t, "pnpm-scoped-registry")

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
	for _, fixture := range []string{"npm-arch-lockfile", "pnpm-scoped-registry", "node", "maven"} {
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
	result := generateFor(t, "npm-arch-lockfile")

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
	root := materialize(t, "npm-arch-lockfile")
	project := DetectProject(root)
	project.Org = "acme"
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
	document := generateFor(t, "npm-arch-lockfile").YAML
	for _, want := range []string{"volumeClaimTemplates", "volumeMounts: &mounts"} {
		if !strings.Contains(document, want) {
			t.Errorf("an Argo pipeline is missing %s", want)
		}
	}
}

// The committed shape of a private scope usually carries NO token line: the
// token is a secret, and CI is expected to supply it. Reading only the token
// line meant such a repository was detected as needing no credential, so no
// credential step was emitted and the install failed at resolution with an
// authentication error.
func TestAScopedRegistryWithNoAuthLineStillNeedsACredential(t *testing.T) {
	t.Parallel()
	for name, npmrc := range map[string]string{
		"scope mapping only": "@acme:registry=https://packages.forge.example\n",
		"scope mapping plus a token": "@acme:registry=https://packages.forge.example\n" +
			"//packages.forge.example/:_authToken=${TOKEN}\n",
		"token line only": "//packages.forge.example/:_authToken=${TOKEN}\n",
	} {
		if host := authenticatedRegistryHost(npmrc); host != "packages.forge.example" {
			t.Errorf("%s: detected host %q, want packages.forge.example", name, host)
		}
	}
}

// A repository that only names the public registry, or a plain mirror, must
// not cause a secret to be declared: a pipeline that declares a credential it
// does not need fails admission for a path nobody meant to use.
func TestAPublicRegistryDeclaresNoCredential(t *testing.T) {
	t.Parallel()
	for name, npmrc := range map[string]string{
		"public registry":     "registry=https://registry.npmjs.org\n",
		"public scope":        "@acme:registry=https://registry.npmjs.org\n",
		"settings only":       "auto-install-peers=true\nsave-exact=true\n",
		"a commented mapping": "# @acme:registry=https://packages.forge.example\n",
		"a bare mirror":       "registry=https://mirror.internal.example\n",
	} {
		if host := authenticatedRegistryHost(npmrc); host != "" {
			t.Errorf("%s: declared a credential for %q, want none", name, host)
		}
	}
}

// The registry host and the org both come from the repository and the server,
// never from a constant. The fixture names neither of the organizations any
// transcript mentioned, and the generated document must carry its values.
func TestRegistryAndOrgAreTakenFromTheCheckoutNotFromAConstant(t *testing.T) {
	t.Parallel()
	result := generateFor(t, "pnpm-scoped-registry")

	if result.SecretPath != "oberth/upstream/acme/github-token" {
		t.Fatalf("secret path = %q, want it scoped to the org the checkout named", result.SecretPath)
	}
	if !strings.Contains(result.YAML, `"//packages.forge.example/:_authToken"`) {
		t.Error("the credential line does not name the registry the checkout declared")
	}
}
