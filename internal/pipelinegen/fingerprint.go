package pipelinegen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Fingerprint is the content of everything the generator reads, keyed by
// repository-relative path.
//
// It exists so a server-held pipeline can say when it has gone stale. The
// document a repository stores on the server was generated from one revision's
// inputs; if a later push changes those inputs, the stored document may no
// longer describe how the repository is built. Comparing two fingerprints
// names exactly which inputs moved.
type Fingerprint map[string]string

// fingerprintFiles are the files whose whole content is an input. They are the
// files DetectProject reads, in the order it reads them, so a change here and
// a change there stay one edit apart.
var fingerprintFiles = []string{"pom.xml", "go.mod", ".nvmrc", ".npmrc"}

// fingerprintPresenceFiles are inputs the generator reads by NAME rather than
// by content: the lockfile decides which package manager runs, and a lockfile
// whose dependency graph moved does not change a single generated step. Only
// appearing or disappearing is drift.
var fingerprintPresenceFiles = []string{
	"package-lock.json", "pnpm-lock.yaml", "yarn.lock", ".yarnrc.yml", "pnpm-workspace.yaml",
}

// maxFingerprintFileBytes bounds one input read. A repository cannot make the
// server read an unbounded file by committing one.
const maxFingerprintFileBytes = 4 << 20

// FingerprintInputs reads the checkout at root and returns the fingerprint of
// its generator inputs.
//
// Like DetectProject it never fails: an absent or unreadable input is one less
// entry, and an input that disappears is itself the drift a caller wants to
// see.
func FingerprintInputs(root string) Fingerprint {
	fingerprint := Fingerprint{}

	// The Actions workflow tree, every candidate file the build-workflow
	// search would consider. Naming them individually means drift reports the
	// workflow that changed rather than "something under .github".
	workflowDir := filepath.Join(root, ".github", "workflows")
	if entries, err := os.ReadDir(workflowDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			lower := strings.ToLower(entry.Name())
			if !strings.HasSuffix(lower, ".yml") && !strings.HasSuffix(lower, ".yaml") {
				continue
			}
			relative := ".github/workflows/" + entry.Name()
			if sum, ok := hashFile(filepath.Join(workflowDir, entry.Name())); ok {
				fingerprint[relative] = sum
			}
		}
	}

	// package.json contributes only what the generator reads out of it. A
	// version bump or a new dependency changes no generated step, and
	// reporting it as drift would train the reader to ignore the warning.
	if sum, ok := hashPackageManifest(filepath.Join(root, "package.json")); ok {
		fingerprint["package.json"] = sum
	}

	for _, name := range fingerprintFiles {
		if sum, ok := hashFile(filepath.Join(root, name)); ok {
			fingerprint[name] = sum
		}
	}
	for _, name := range fingerprintPresenceFiles {
		if info, err := os.Stat(filepath.Join(root, name)); err == nil && info.Mode().IsRegular() {
			fingerprint[name] = "present"
		}
	}
	return fingerprint
}

// DriftedInputs lists the paths on which two fingerprints disagree, sorted.
// An input present in one and absent in the other counts, because a deleted
// workflow changes the generated pipeline exactly as an edited one does.
func DriftedInputs(stored, current Fingerprint) []string {
	seen := map[string]bool{}
	var drifted []string
	for path, sum := range stored {
		seen[path] = true
		if current[path] != sum {
			drifted = append(drifted, path)
		}
	}
	for path, sum := range current {
		if seen[path] {
			continue
		}
		if stored[path] != sum {
			drifted = append(drifted, path)
		}
	}
	sort.Strings(drifted)
	return drifted
}

func hashFile(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxFingerprintFileBytes {
		return "", false
	}
	body, err := os.ReadFile(path) // #nosec G304 -- a bounded regular file under the run's own checkout
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), true
}

// hashPackageManifest hashes the three package.json fields DetectProject reads:
// scripts, engines.node, and packageManager.
func hashPackageManifest(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxFingerprintFileBytes {
		return "", false
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- a bounded regular file under the run's own checkout
	if err != nil {
		return "", false
	}
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
		Engines struct {
			Node string `json:"node"`
		} `json:"engines"`
		PackageManager string `json:"packageManager"`
	}
	if json.Unmarshal(raw, &manifest) != nil {
		// An unparseable manifest is still an input: hash it whole so
		// repairing it registers as drift rather than as nothing.
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:]), true
	}
	// Marshal through a map so key order is Go's own sorted order rather than
	// the order the repository happened to write.
	canonical, err := json.Marshal(map[string]any{
		"scripts":        manifest.Scripts,
		"engines.node":   manifest.Engines.Node,
		"packageManager": manifest.PackageManager,
	})
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), true
}
