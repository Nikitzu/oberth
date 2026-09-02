package pipelinegen

import (
	"sort"
	"strings"
)

// This file is the cookbook: the per-ecosystem invocation shapes that a naive
// translation gets wrong. Every rule here corresponds to a run that went red
// on a real repository, and each one carries the failure it kills, because a
// rule whose reason is not written down is a rule the next person deletes.

// Engine names the execution engine the server runs. It reaches the generator
// from /api/status, because the two engines do not accept the same document:
// the docker engine refuses repository-declared volumeMounts outright and
// mounts the /work tree itself.
type Engine string

const (
	// EngineArgo is the Kubernetes engine, which needs the claim template and
	// the mounts spelled out.
	EngineArgo Engine = "argo"
	// EngineDocker is the clusterless engine.
	EngineDocker Engine = "docker"
)

// --- Rule 1: per-manager invocation shapes ---------------------------------

// runScript is how one package manager is asked to run a repository script.
//
// The failure this kills: `pnpm run lint --if-present`. npm consumes
// --if-present itself; pnpm does not recognize it and forwards it to the
// script, so biome received --if-present and refused with a usage error that
// named a flag nobody wrote. The generator had emitted an npm-ism into a pnpm
// invocation because it built one command string for every manager.
//
// --if-present is not emitted for ANY manager now, not merely spelled
// differently per manager. It only ever guarded against naming a script that
// does not exist, and the generator emits steps only for scripts it has read
// out of package.json, so the guard protected against a case that cannot
// arise while costing a real failure in the case that does.
func runScript(project Project, script string) string {
	return packageRunner(project) + "run " + script
}

// registryAuthCommands injects a forge token into the package manager's own
// configuration, in the shape that manager actually honours.
//
// The failure this kills: appending `//<host>/:_authToken=${NPM_TOKEN}` to
// .npmrc in the build copy. npm expands ${VAR} in an .npmrc; pnpm merges
// several npmrc files and does not expand the placeholder reliably, so the
// literal string travelled to the registry as the token and the install
// failed with a 401 that named authentication rather than the placeholder.
// Writing the resolved value through the manager's own `config set` avoids
// the question entirely.
//
// The value comes from $GITHUB_TOKEN, which the secret preamble exported from
// the mounted secret directory. It is written into the build copy's project
// configuration, which no later step archives or uploads.
func registryAuthCommands(project Project) []string {
	host := strings.TrimSpace(project.Registry)
	if host == "" {
		return nil
	}
	key := `"//` + host + `/:_authToken"`
	switch project.PackageManager {
	case "pnpm":
		// pnpm spells the flag with a space and takes the location as its own
		// argument. This is the form proven against a GitHub Packages style
		// registry, and it is written with whatever host the checkout named.
		//
		// It goes through packageRunner for the same reason the install does:
		// pnpm is not on the image's PATH, it is fetched by npx, so a bare
		// `pnpm config set` is a command-not-found before the install has a
		// chance to fail on authentication.
		return []string{packageRunner(project) + `config set ` + key + ` "$GITHUB_TOKEN" --location project`}
	case "yarn":
		if project.PackageManagerMajor != "" && project.PackageManagerMajor != "1" {
			// Berry keeps credentials in .yarnrc.yml under npmScopes, which
			// needs the scope name rather than the host. The generator does
			// not have a proven form for it, and guessing here would produce
			// the same class of silent 401 this rule exists to kill.
			return nil
		}
		return []string{`npm config set ` + key + ` "$GITHUB_TOKEN" --location=project`}
	default:
		// npm spells the same flag with an equals sign.
		return []string{`npm config set ` + key + ` "$GITHUB_TOKEN" --location=project`}
	}
}

// berryRegistryUntranslatable reports the one credentialed case the cookbook
// declines to guess at, so the header says so rather than the build failing
// at resolution with a message about authentication.
func berryRegistryUntranslatable(project Project) bool {
	return project.PrivateRegistry && project.PackageManager == "yarn" &&
		project.PackageManagerMajor != "" && project.PackageManagerMajor != "1"
}

// --- Rule 2: a prepare script that needs git -------------------------------

// prepareNeedsGit reports that the repository's install will run a script
// requiring a git binary.
//
// The failure this kills: a repository whose `prepare` script runs a git-hook
// installer, which shells out to `git config`. The node -slim images carry no
// git, so the install dies after every package has already downloaded, with an
// error from the hook installer rather than from the install. Observed on a
// repository using husky; the class is any prepare or postinstall step that
// needs a git binary. The fix is NOT to install git: pipeline containers run
// with a read-only root filesystem, so an apt-get in a step can never succeed,
// and a generator that emits one is emitting a step that is guaranteed to
// fail. The fix is to pick the image variant that already ships git.
func prepareNeedsGit(project Project) bool {
	for _, name := range []string{"prepare", "postinstall", "preinstall"} {
		body, ok := project.script(name)
		if !ok {
			continue
		}
		lowered := strings.ToLower(body)
		for _, needle := range []string{"husky", "git ", "git config", "simple-git-hooks", "lefthook"} {
			if strings.Contains(lowered, needle) {
				return true
			}
		}
	}
	return false
}

// --- Rule 3: per-architecture optional dependencies ------------------------

// platformTool is a package whose real binary ships in a per-architecture
// optional dependency, which a lockfile can pin for one architecture only.
type platformTool struct {
	// module is the package whose installed version names the platform
	// package's version. They are released in lockstep.
	module string
	// prefix and suffix bracket the platform triple in the optional
	// dependency's name, which each of these tools spells differently.
	prefix string
	suffix string
}

// platformTools is the curated list. Every one of these has bitten a real
// repository; the list is deliberately not "every package with optionalDeps",
// because emitting a runtime probe for a package that does not need one is
// noise in every log.
var platformTools = []platformTool{
	{module: "@biomejs/biome", prefix: "@biomejs/cli-"},
	{module: "esbuild", prefix: "@esbuild/"},
	{module: "sharp", prefix: "@img/sharp-"},
	{module: "@swc/core", prefix: "@swc/core-", suffix: "-gnu"},
}

// platformPackageStep builds the runtime repair for one tool.
//
// The failure this kills: a lockfile that pins one architecture's variant of a
// tool and no other, so a plain install on any other architecture leaves the
// tool with no binary. Observed with a linter whose lockfile carried the x64
// Linux package only, on an arm64 runner; hosted CI had never hit it because
// it installed that tool by another route entirely, so the lockfile had been
// wrong for as long as anyone had had an arm64 machine.
//
// The probe is at runtime rather than at generation time on purpose: the
// generator does not know the runner's architecture, and a step that asks node
// what platform it is actually on is right on every runner instead of right on
// the one the generator guessed.
func platformPackageStep(tool platformTool) string {
	name := tool.prefix + `$platform` + tool.suffix
	return strings.Join([]string{
		`platform="$(node -p "process.platform + '-' + process.arch")"`,
		`version="$(node -p "require('` + tool.module + `/package.json').version" 2>/dev/null || true)"`,
		`if [ -n "$version" ] && ! node -e "require.resolve('` + name + `/package.json')" 2>/dev/null; then`,
		`  echo "this lockfile carries no ` + name + `; fetching $version to match the runner" >&2`,
		`  npm install --no-save --no-audit --no-fund "` + name + `@$version"`,
		`fi`,
	}, "\n")
}

// missingPlatformTools returns the tools this repository depends on whose
// lockfile does not cover both Linux architectures.
//
// Covering both is the test rather than covering the runner's, because the
// generator cannot know the runner's architecture and because a lockfile that
// covers both is one no runner can break.
func missingPlatformTools(project Project) []platformTool {
	var found []platformTool
	for _, tool := range platformTools {
		if !project.Dependencies[tool.module] {
			continue
		}
		covered := true
		for _, arch := range []string{"linux-x64", "linux-arm64"} {
			if !strings.Contains(project.LockfileBody, tool.prefix+arch+tool.suffix) {
				covered = false
			}
		}
		if !covered {
			found = append(found, tool)
		}
	}
	return found
}

// --- Rule 4: gate coverage -------------------------------------------------

// gateFamilies is the order gates run in, and the prefixes that put a script
// in each one. Cheap and specific first: a lint finding should not wait behind
// a five-minute test suite.
//
// The failure this kills: the generator recognized exactly four script names
// (lint, typecheck, test, build) by exact match, so every repository-specific
// gate was dropped in silence. Observed with an architecture-rule gate named
// `lint:arch` and with `validate:structure` and `validate:compile`. A pipeline
// that runs fewer gates than the repository believes it runs is the failure
// mode this whole package exists to prevent, and it had it.
var gateFamilies = []struct {
	name     string
	prefixes []string
}{
	{"lint", []string{"lint"}},
	{"typecheck", []string{"typecheck", "typecheck", "tsc", "check"}},
	{"validate", []string{"validate"}},
	{"test", []string{"test"}},
	{"build", []string{"build", "compile"}},
}

// nonGateMarkers are the words that make a gate-shaped script name something
// no CI should run: an editing mode, a watcher, or a long-lived server.
//
// A script that rewrites the tree cannot be a gate, because a gate that fixes
// its own finding always passes.
var nonGateMarkers = []struct{ marker, why string }{
	{"watch", "it never exits"},
	{"fix", "it rewrites the tree, so it would always pass"},
	{"write", "it rewrites the tree, so it would always pass"},
	{"dev", "it is a development server"},
	{"start", "it is a long-lived process"},
	{"serve", "it is a long-lived process"},
	{"debug", "it is an interactive mode"},
	{"clean", "it deletes build output rather than checking it"},
	{"ui", "it opens an interactive reporter"},
	{"e2e", "it needs a browser and a running application, which this pipeline does not start"},
}

// gate is one script the pipeline will run.
type gate struct {
	script string
	family string
}

// skippedGate is one gate-shaped script that will not run, and why not.
type skippedGate struct {
	script string
	why    string
}

// classifyGates maps every package.json script into the ordered list of gates
// to run and the list deliberately left out.
//
// Determinism matters: the document is stored server-side and diffed against a
// regeneration to detect drift, so a map iteration order leaking into the
// output would report drift on every check.
func classifyGates(project Project) ([]gate, []skippedGate) {
	names := scriptNames(project.Scripts)

	var gates []gate
	var left []skippedGate
	claimed := map[string]bool{}

	for _, family := range gateFamilies {
		var matched []string
		for _, name := range names {
			if claimed[name] {
				continue
			}
			if _, ok := project.script(name); !ok {
				continue
			}
			if !matchesFamily(name, family.prefixes) {
				continue
			}
			claimed[name] = true
			if why, isNot := nonGateReason(name); isNot {
				left = append(left, skippedGate{script: name, why: why})
				continue
			}
			matched = append(matched, name)
		}
		sort.Strings(matched)
		for _, name := range matched {
			gates = append(gates, gate{script: name, family: family.name})
		}
	}
	return gates, left
}

// matchesFamily reports whether a script name belongs to a family. The match
// is on the first colon-separated segment, so `lint:arch` is a lint and
// `pretest` is not.
func matchesFamily(name string, prefixes []string) bool {
	head := name
	if colon := strings.IndexByte(head, ':'); colon >= 0 {
		head = head[:colon]
	}
	for _, prefix := range prefixes {
		if head == prefix {
			return true
		}
	}
	return false
}

// nonGateReason matches markers against whole name segments, never against
// raw substrings.
//
// A substring match called `build` an interactive reporter, because "build"
// contains "ui". Script names are colon- and dash-separated words and the
// markers are words, so the comparison is between words.
func nonGateReason(name string) (string, bool) {
	for _, word := range scriptWords(name) {
		for _, marker := range nonGateMarkers {
			if word == marker.marker {
				return marker.why, true
			}
		}
	}
	return "", false
}

// scriptWords splits a script name into its lowercase segments, so
// "test:watch" is "test" and "watch" and "lint-fix" is "lint" and "fix".
func scriptWords(name string) []string {
	return strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
}

// templateName is the script name spelled the way a workflow template can be
// named. A colon is legal in package.json and is not legal here, and
// `lint:arch` -- a real repository's architecture gate -- is exactly the kind
// of script that must not be dropped for want of a rename.
func templateName(script string) string {
	return strings.Join(scriptWords(script), "-")
}
