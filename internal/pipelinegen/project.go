package pipelinegen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Kind is what the repository builds with. It is deliberately coarse: the
// generator only needs to know which toolchain image to run and which command
// installs dependencies.
type Kind string

const (
	KindNode    Kind = "node"
	KindMaven   Kind = "maven"
	KindGo      Kind = "go"
	KindUnknown Kind = "unknown"
)

// Project is everything the generator learned about a checkout.
//
// Every field records where it came from, because the generated header prints
// the provenance. A user who can see that the Node major came from .nvmrc and
// the test command came from package.json can check both in seconds; a user
// handed an unattributed pipeline has to read all of it.
type Project struct {
	Kind Kind

	// NodeMajor / JavaMajor are the major versions the repository asked for,
	// from the Actions workflow inputs or from a version file.
	NodeMajor string
	JavaMajor string

	// Scripts are the package.json scripts, by name.
	Scripts map[string]string

	// LegacyPeerDeps mirrors the Actions input of the same name: npm 7+
	// refuses a peer-dependency conflict that npm 6 accepted, and a repository
	// that built on Actions with the flag will not install without it.
	LegacyPeerDeps bool

	// PrivateRegistry reports that installing dependencies needs a credential,
	// and Registry names the host that wants it.
	PrivateRegistry bool
	Registry        string

	// Org is the upstream organization, which is what scopes the secret the
	// private registry needs.
	Org string

	// Provenance and honesty.
	Sources      []string
	Untranslated []string
}

// note records a provenance line.
func (p *Project) note(line string) { p.Sources = append(p.Sources, line) }

// cannot records something the generator saw and did not translate. A step
// that exists on Actions and not here has to be visible, or the pipeline
// quietly tests less than the repository thinks it does.
func (p *Project) cannot(line string) { p.Untranslated = append(p.Untranslated, line) }

// script returns a package.json script body and whether it exists.
func (p Project) script(name string) (string, bool) {
	body, ok := p.Scripts[name]
	return strings.TrimSpace(body), ok && strings.TrimSpace(body) != ""
}

// DetectProject reads the checkout at root. It never fails: an unreadable or
// absent file means one less thing known, and the caller turns "nothing known"
// into a scaffold that says so.
func DetectProject(root string) Project {
	project := Project{Kind: KindUnknown, Scripts: map[string]string{}}

	if raw, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var manifest struct {
			Scripts map[string]string `json:"scripts"`
			Engines struct {
				Node string `json:"node"`
			} `json:"engines"`
		}
		if json.Unmarshal(raw, &manifest) == nil {
			project.Kind = KindNode
			project.Scripts = manifest.Scripts
			project.note("package.json: " + strings.Join(scriptNames(manifest.Scripts), ", "))
			if major := majorVersion(manifest.Engines.Node); major != "" {
				project.NodeMajor = major
				project.note("package.json engines.node: " + major)
			}
		}
	}

	if raw, err := os.ReadFile(filepath.Join(root, ".nvmrc")); err == nil {
		if major := majorVersion(string(raw)); major != "" {
			project.NodeMajor = major
			project.note(".nvmrc: Node " + major)
		}
	}

	if raw, err := os.ReadFile(filepath.Join(root, ".npmrc")); err == nil {
		if host := authenticatedRegistryHost(string(raw)); host != "" {
			project.PrivateRegistry = true
			project.Registry = host
			project.note(".npmrc: authenticated registry " + host)
		}
	}

	if raw, err := os.ReadFile(filepath.Join(root, "pom.xml")); err == nil {
		project.Kind = KindMaven
		project.note("pom.xml found")
		if major := pomJavaVersion(string(raw)); major != "" {
			project.JavaMajor = major
			project.note("pom.xml: Java " + major)
		}
		// A parent that is not resolvable from Maven Central needs a
		// credentialed repository, which on this fork is the same upstream
		// token the private npm registry uses.
		if parent := pomParentGroup(string(raw)); parent != "" && !publicMavenGroup(parent) {
			project.PrivateRegistry = true
			project.Registry = "maven.pkg.github.com"
			project.note("pom.xml: parent " + parent + " is not a public group, so the build needs a credentialed Maven repository")
		}
	}

	if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil && project.Kind == KindUnknown {
		project.Kind = KindGo
		project.note("go.mod found")
	}

	project.Org = originOrg(root)
	return project
}

func scriptNames(scripts map[string]string) []string {
	names := make([]string, 0, len(scripts))
	for name := range scripts {
		names = append(names, name)
	}
	sortStrings(names)
	return names
}

var majorPattern = regexp.MustCompile(`(\d+)`)

// majorVersion pulls the leading major out of "20.19", "v20", ">=20 <21" or
// "^20.1.0".
func majorVersion(raw string) string {
	match := majorPattern.FindString(strings.TrimSpace(raw))
	return match
}

// authenticatedRegistryHost finds the host an .npmrc supplies a token for.
// An .npmrc that only sets a public registry needs no credential and must not
// cause a secret to be declared.
func authenticatedRegistryHost(npmrc string) string {
	for _, line := range strings.Split(npmrc, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		index := strings.Index(line, ":_authToken")
		if index < 0 {
			continue
		}
		host := strings.Trim(line[:index], "/")
		if host != "" && host != "registry.npmjs.org" {
			return host
		}
	}
	return ""
}

var (
	pomJavaPattern   = regexp.MustCompile(`<(?:java\.version|maven\.compiler\.release|maven\.compiler\.source)>\s*(\d+)`)
	pomParentPattern = regexp.MustCompile(`(?s)<parent>.*?<groupId>\s*([^<\s]+)\s*</groupId>`)
)

func pomJavaVersion(pom string) string {
	if match := pomJavaPattern.FindStringSubmatch(pom); len(match) == 2 {
		return match[1]
	}
	return ""
}

func pomParentGroup(pom string) string {
	if match := pomParentPattern.FindStringSubmatch(pom); len(match) == 2 {
		return match[1]
	}
	return ""
}

// publicMavenGroup lists the parent groups that resolve from Maven Central
// without a credential. Anything else is assumed to need one, which is the
// safe direction: a declared-but-unused credential is a comment to delete,
// while a missing one is a build that fails at dependency resolution.
func publicMavenGroup(group string) bool {
	switch group {
	case "org.springframework.boot", "org.apache.maven", "org.sonatype.oss", "org.jboss", "io.quarkus":
		return true
	}
	return false
}

var originPattern = regexp.MustCompile(`url\s*=\s*\S*?[/:]([^/\s]+)/[^/\s]+?(?:\.git)?\s*$`)

// originOrg reads the org out of the origin remote in .git/config.
//
// The org is what scopes a secret-store path, so it is read from the same
// place the push will come from rather than guessed from the directory name.
func originOrg(root string) string {
	raw, err := os.ReadFile(filepath.Join(root, ".git", "config"))
	if err != nil {
		return ""
	}
	inOrigin := false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inOrigin = trimmed == `[remote "origin"]`
			continue
		}
		if !inOrigin {
			continue
		}
		if match := originPattern.FindStringSubmatch(trimmed); len(match) == 2 {
			return match[1]
		}
	}
	return ""
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
