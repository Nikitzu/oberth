package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Target string

const (
	TargetAgents Target = "agents"
	TargetClaude Target = "claude"
	TargetReplit Target = "replit"
)

const (
	agentsFile = "AGENTS.md"
	replitFile = "custom_instruction/instructions.md"
	claudeDir  = ".claude/skills"
)

type InstallOptions struct {
	Root     string
	Target   Target
	Only     string
	Force    bool
	Personal bool
}

type Result struct {
	Target  Target
	Written []string
}

func Detect(root string) Target {
	if exists(filepath.Join(root, agentsFile)) {
		return TargetAgents
	}
	if exists(filepath.Join(root, filepath.FromSlash(claudeDir))) {
		return TargetClaude
	}
	if exists(filepath.Join(root, filepath.FromSlash(replitFile))) {
		return TargetReplit
	}
	return TargetAgents
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func Install(options InstallOptions) (Result, error) {
	if strings.TrimSpace(options.Root) == "" {
		return Result{}, errors.New("skills: an install root is required")
	}
	selected, err := selection(options.Only)
	if err != nil {
		return Result{}, err
	}
	switch options.Target {
	case TargetAgents:
		return installMarked(options, filepath.Join(options.Root, agentsFile), selected)
	case TargetReplit:
		return installMarked(options, filepath.Join(options.Root, filepath.FromSlash(replitFile)), selected)
	case TargetClaude:
		return installClaude(options, selected)
	default:
		return Result{}, fmt.Errorf("skills: unknown target %q, want agents, claude or replit", options.Target)
	}
}

func selection(only string) ([]Skill, error) {
	if strings.TrimSpace(only) == "" {
		return List(), nil
	}
	skill, err := Get(only)
	if err != nil {
		return nil, err
	}
	return []Skill{skill}, nil
}

func installMarked(options InstallOptions, path string, selected []Skill) (Result, error) {
	if err := refuseSymlink(options.Root, path); err != nil {
		return Result{}, err
	}
	existing, err := os.ReadFile(path) // #nosec G304 -- path is derived from the caller's root and a fixed name.
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Result{}, fmt.Errorf("skills: read %s: %w", path, err)
	}
	merged := MergeMarked(existing, EmitAgents(selected))
	if err := writeFile(path, merged); err != nil {
		return Result{}, err
	}
	return Result{Target: options.Target, Written: []string{path}}, nil
}

func installClaude(options InstallOptions, selected []Skill) (Result, error) {
	result := Result{Target: TargetClaude}
	for _, skill := range selected {
		path := filepath.Join(options.Root, filepath.FromSlash(claudeDir), skill.Name, "SKILL.md")
		if err := refuseSymlink(options.Root, path); err != nil {
			return Result{}, err
		}
		if !options.Force {
			if err := refuseForeign(path, skill.Name); err != nil {
				return Result{}, err
			}
		}
		if err := writeFile(path, EmitSKILL(skill)); err != nil {
			return Result{}, err
		}
		result.Written = append(result.Written, path)
	}
	return result, nil
}

func refuseSymlink(root, path string) error {
	for current := path; len(current) > len(root); current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("skills: %s is a symlink; refusing to follow", current)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("skills: inspect %s: %w", current, err)
		}
	}
	return nil
}

func refuseForeign(path, name string) error {
	body, err := os.ReadFile(path) // #nosec G304 -- path is derived from the caller's root and a validated skill name.
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("skills: read %s: %w", path, err)
	}
	if skill, parseErr := parse(body); parseErr == nil && skill.Name == name {
		return nil
	}
	return fmt.Errorf("skills: %s was not written by oberth; use --force to replace it", path)
}

func writeFile(path string, body []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil { // #nosec G301 -- project directory, not secrets
		return fmt.Errorf("skills: create %s: %w", directory, err)
	}
	temporary, err := os.CreateTemp(directory, ".skill.*.tmp")
	if err != nil {
		return fmt.Errorf("skills: create temporary file: %w", err)
	}
	name := temporary.Name()
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		_ = os.Remove(name)
		return err
	}
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		_ = os.Remove(name)
		return err
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
