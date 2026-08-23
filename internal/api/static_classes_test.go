package api

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	classAttrPattern  = regexp.MustCompile(`class="([a-z0-9 _-]+)"`)
	cssSelectorHunter = regexp.MustCompile(`\.([a-zA-Z][a-zA-Z0-9_-]*)`)
)

func TestEveryMarkupClassHasAStyle(t *testing.T) {
	t.Parallel()
	script, ok := staticAssets["app.js"]
	if !ok {
		t.Fatal("app.js is not embedded")
	}
	style, ok := staticAssets["app.css"]
	if !ok {
		t.Fatal("app.css is not embedded")
	}
	styled := map[string]bool{}
	for _, match := range cssSelectorHunter.FindAllStringSubmatch(string(style.body), -1) {
		styled[match[1]] = true
	}

	var missing []string
	seen := map[string]bool{}
	for _, match := range classAttrPattern.FindAllStringSubmatch(string(script.body), -1) {
		for _, name := range strings.Fields(match[1]) {
			if seen[name] || styled[name] {
				continue
			}
			seen[name] = true
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("app.js renders classes that app.css never styles, so they are invisible: %v", missing)
	}
}
