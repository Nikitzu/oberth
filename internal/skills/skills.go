package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

//go:embed content
var content embed.FS

const maxBodyBytes = 8 << 10

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)

type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body,omitempty"`
}

type frontmatter struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

var catalogue = load()

func load() []Skill {
	var all []Skill
	_ = fs.WalkDir(content, "content", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil //nolint:nilerr
		}
		raw, readErr := content.ReadFile(path)
		if readErr != nil {
			return nil //nolint:nilerr
		}
		skill, parseErr := parse(raw)
		if parseErr != nil {
			return nil //nolint:nilerr
		}
		all = append(all, skill)
		return nil
	})
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return all
}

func parse(raw []byte) (Skill, error) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return Skill{}, fmt.Errorf("skills: body has no frontmatter")
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return Skill{}, fmt.Errorf("skills: frontmatter is not terminated")
	}
	var meta frontmatter
	if err := yaml.UnmarshalStrict([]byte(rest[:end+1]), &meta); err != nil {
		return Skill{}, fmt.Errorf("skills: decode frontmatter: %w", err)
	}
	if !namePattern.MatchString(meta.Name) {
		return Skill{}, fmt.Errorf("skills: %q is not a valid skill name", meta.Name)
	}
	if strings.TrimSpace(meta.Description) == "" {
		return Skill{}, fmt.Errorf("skills: %s has no description", meta.Name)
	}
	body := strings.TrimLeft(rest[end+len("\n---\n"):], "\n")
	return Skill{Name: meta.Name, Description: meta.Description, Body: body}, nil
}

func List() []Skill {
	out := make([]Skill, len(catalogue))
	copy(out, catalogue)
	return out
}

func Get(name string) (Skill, error) {
	if !namePattern.MatchString(name) {
		return Skill{}, fmt.Errorf("skills: %q is not a skill name", name)
	}
	for _, skill := range catalogue {
		if skill.Name == name {
			return skill, nil
		}
	}
	return Skill{}, fmt.Errorf("skills: no skill named %q", name)
}
