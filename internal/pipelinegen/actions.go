package pipelinegen

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"
)

// Workflow is the part of a GitHub Actions document this translator reads.
// Everything else is deliberately absent: a field that is parsed but ignored
// reads like a field that was translated.
type Workflow struct {
	Path string
	Name string
	Jobs []Job
}

// Job is one Actions job. Either it has Steps, or it delegates the whole build
// to a reusable workflow through Uses, in which case the steps live in a
// repository that is not this one.
type Job struct {
	ID     string
	Name   string
	Uses   string
	With   map[string]string
	Steps  []Step
	Secret []string
}

// Step is one Actions step. Run holds a shell body; Uses names an action.
type Step struct {
	Name string
	Uses string
	Run  string
	With map[string]string
}

// skippedWorkflow matches the workflows that are not the build: releasing,
// rolling back, warming caches, load testing, and the review bots. Translating
// one of those would produce a branch pipeline that deploys.
var skippedWorkflow = []string{
	"release", "rollback", "deploy", "publish", "cache", "load-test",
	"copilot", "codeql", "dependabot", "stale", "label", "archived",
}

// FindBuildWorkflow picks the workflow that builds the repository.
//
// The order is: an exact build/ci filename first, then a workflow whose name
// says build or CI, and nothing otherwise. It never falls back to "the first
// file in the directory": a wrong guess here produces a pipeline that looks
// translated and runs the wrong thing, which is worse than admitting there was
// nothing to translate.
func FindBuildWorkflow(root string) (Workflow, bool) {
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Workflow{}, false
	}

	var candidates []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".yml") && !strings.HasSuffix(lower, ".yaml") {
			continue
		}
		if skipped(lower) {
			continue
		}
		candidates = append(candidates, filepath.Join(dir, name))
	}
	sortStrings(candidates)

	var best Workflow
	var bestRank int
	for _, path := range candidates {
		workflow, err := parseWorkflow(path)
		if err != nil {
			continue
		}
		rank := workflowRank(path, workflow.Name)
		if rank > bestRank {
			best, bestRank = workflow, rank
		}
	}
	return best, bestRank > 0
}

func skipped(lowerName string) bool {
	for _, marker := range skippedWorkflow {
		if strings.Contains(lowerName, marker) {
			return true
		}
	}
	return false
}

// workflowRank scores a candidate. Zero means "not the build workflow".
func workflowRank(path, name string) int {
	base := strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), ".yml"), ".yaml"))
	switch base {
	case "build":
		return 3
	case "ci", "test", "main":
		return 2
	}
	lowerName := strings.ToLower(name)
	if strings.Contains(lowerName, "build") || strings.Contains(lowerName, "ci") || strings.Contains(lowerName, "test") {
		return 1
	}
	return 0
}

// parseWorkflow decodes the subset above. `on:` is not read at all: the only
// trigger Oberth generates for is a branch push.
func parseWorkflow(path string) (Workflow, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- a path this command was pointed at
	if err != nil {
		return Workflow{}, err
	}
	var document struct {
		Name string `json:"name"`
		Jobs map[string]struct {
			Name    string            `json:"name"`
			Uses    string            `json:"uses"`
			With    map[string]any    `json:"with"`
			Secrets map[string]string `json:"secrets"`
			Steps   []struct {
				Name string         `json:"name"`
				Uses string         `json:"uses"`
				Run  string         `json:"run"`
				With map[string]any `json:"with"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return Workflow{}, err
	}

	workflow := Workflow{Path: path, Name: document.Name}
	ids := make([]string, 0, len(document.Jobs))
	for id := range document.Jobs {
		ids = append(ids, id)
	}
	sortStrings(ids)
	for _, id := range ids {
		raw := document.Jobs[id]
		job := Job{ID: id, Name: raw.Name, Uses: raw.Uses, With: flatten(raw.With)}
		for name := range raw.Secrets {
			job.Secret = append(job.Secret, name)
		}
		sortStrings(job.Secret)
		for _, step := range raw.Steps {
			job.Steps = append(job.Steps, Step{Name: step.Name, Uses: step.Uses, Run: step.Run, With: flatten(step.With)})
		}
		workflow.Jobs = append(workflow.Jobs, job)
	}
	return workflow, nil
}

// flatten renders `with:` values as strings. Actions accepts strings, numbers
// and booleans there, and every consumer below compares text.
func flatten(values map[string]any) map[string]string {
	if len(values) == 0 {
		return nil
	}
	flat := make(map[string]string, len(values))
	for key, value := range values {
		switch typed := value.(type) {
		case string:
			flat[key] = typed
		case bool:
			if typed {
				flat[key] = "true"
			} else {
				flat[key] = "false"
			}
		case float64:
			flat[key] = strconv.FormatFloat(typed, 'f', -1, 64)
		default:
			flat[key] = ""
		}
	}
	return flat
}

// WorkflowScripts returns every package manager script name the repository's
// own workflows invoke, in the order they are first seen. It reads the `run:`
// lines of every workflow that is not a release, so the answer is what the
// repository's CI actually runs rather than what its package.json could run.
//
// The names are not checked against package.json here: `pnpm install` yields
// "install", and it is the caller, which holds the scripts, that drops it.
func WorkflowScripts(root string) []string {
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".yml") && !strings.HasSuffix(lower, ".yaml") {
			continue
		}
		if skipped(lower) {
			continue
		}
		names = append(names, name)
	}
	sortStrings(names)
	var found []string
	seen := map[string]bool{}
	for _, name := range names {
		workflow, err := parseWorkflow(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		for _, job := range workflow.Jobs {
			for _, step := range job.Steps {
				for _, script := range scriptsInRun(step.Run) {
					if !seen[script] {
						seen[script] = true
						found = append(found, script)
					}
				}
			}
		}
	}
	return found
}

// runInvocation matches one package manager invocation of a script: `npm run
// X`, `npm test`, `pnpm X`, `pnpm run X`, `yarn X` and `yarn run X`. The
// leading group anchors on a command boundary so that a word inside an
// argument is not read as a manager.
var runInvocation = regexp.MustCompile(
	`(?:^|[\s;&|(])(?:npm|pnpm|yarn)(?:\s+run)?\s+([A-Za-z0-9_.:@/-]+)`)

// scriptsInRun extracts the script names one shell body invokes, in order and
// without repeats.
func scriptsInRun(body string) []string {
	var found []string
	seen := map[string]bool{}
	for _, match := range runInvocation.FindAllStringSubmatch(body, -1) {
		script := match[1]
		if strings.HasPrefix(script, "-") || seen[script] {
			continue
		}
		seen[script] = true
		found = append(found, script)
	}
	return found
}
