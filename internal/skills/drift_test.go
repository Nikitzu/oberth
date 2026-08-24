package skills

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	fencePattern       = regexp.MustCompile("(?s)```.*?```")
	backtickPattern    = regexp.MustCompile("`([^`\n]+)`")
	placeholderPattern = regexp.MustCompile(`<[^>]*>`)
)

var driftIgnoredWords = map[string]bool{"and": true, "the": true, "git": true}

func productionSource(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var builder strings.Builder
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil //nolint:nilerr
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "website", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path) // #nosec G304 -- walking this repository's own source.
		if readErr != nil {
			return nil //nolint:nilerr
		}
		builder.Write(body)
		builder.WriteString("\n")
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	if builder.Len() == 0 {
		t.Fatal("no production source found; the drift guard would pass vacuously")
	}
	return builder.String()
}

func driftTokens(body string) []string {
	stripped := fencePattern.ReplaceAllString(body, "")
	var out []string
	for _, match := range backtickPattern.FindAllStringSubmatch(stripped, -1) {
		out = append(out, match[1])
	}
	return out
}

func driftParts(token string) []string {
	token = placeholderPattern.ReplaceAllString(token, "")
	var out []string
	for _, word := range strings.Fields(token) {
		word = strings.Trim(word, "$:,.")
		if len(word) >= 3 && !driftIgnoredWords[word] {
			out = append(out, word)
		}
	}
	return out
}

func TestNoSkillDescribesSomethingThisBinaryDoesNotHave(t *testing.T) {
	t.Parallel()
	source := productionSource(t)
	checked := 0
	for _, skill := range List() {
		for _, token := range driftTokens(skill.Body) {
			checked++
			if strings.Contains(source, strings.TrimPrefix(token, "$")) {
				continue
			}
			for _, part := range driftParts(token) {
				if !strings.Contains(source, part) {
					t.Errorf("%s references %q, and %q appears nowhere in this binary's source: "+
						"either the feature was removed or the skill is wrong",
						skill.Name, token, part)
				}
			}
		}
	}
	if checked < 30 {
		t.Fatalf("only %d tokens checked; the extraction is not finding them", checked)
	}
}

func TestTheDriftGuardFailsOnAFabricatedFlag(t *testing.T) {
	t.Parallel()
	source := productionSource(t)
	fabricated := "--this-flag-does-not-exist"
	if strings.Contains(source, fabricated) {
		t.Fatalf("%q exists after all; pick another", fabricated)
	}
	for _, part := range driftParts(fabricated) {
		if strings.Contains(source, part) {
			t.Fatalf("the guard would not have caught %q, because %q is present", fabricated, part)
		}
	}
}
