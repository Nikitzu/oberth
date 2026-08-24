package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/oberthci/oberth/internal/skills"
)

func runSkills(_ context.Context, arguments []string, output io.Writer) error {
	if len(arguments) == 0 {
		return fmt.Errorf("%w: skills list|show|install", errUsage)
	}
	switch arguments[0] {
	case "list":
		return runSkillsList(arguments[1:], output)
	case "show":
		return runSkillsShow(arguments[1:], output)
	case "install":
		return runSkillsInstall(arguments[1:], output)
	default:
		return fmt.Errorf("%w: skills list|show|install", errUsage)
	}
}

func runSkillsList(arguments []string, output io.Writer) error {
	if len(arguments) != 0 {
		return fmt.Errorf("%w: skills list accepts no arguments", errUsage)
	}
	for _, skill := range skills.List() {
		if _, err := fmt.Fprintf(output, "%-18s %s\n", skill.Name, skill.Description); err != nil {
			return err
		}
	}
	return nil
}

func runSkillsShow(arguments []string, output io.Writer) error {
	if len(arguments) != 1 {
		return fmt.Errorf("%w: skills show <name>", errUsage)
	}
	skill, err := skills.Get(arguments[0])
	if err != nil {
		return err
	}
	_, err = io.WriteString(output, skill.Body)
	return err
}

func runSkillsInstall(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("skills install", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	target := flags.String("target", "", "agents|claude|replit (default: detect from the repository)")
	personal := flags.Bool("personal", false, "install into the home directory instead of this repository")
	force := flags.Bool("force", false, "replace a file oberth did not write")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(output)
			flags.Usage()
			return nil
		}
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if flags.NArg() > 1 {
		return fmt.Errorf("%w: skills install [name]", errUsage)
	}

	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if *personal {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return homeErr
		}
		root = home
	}

	chosen := skills.Target(strings.TrimSpace(*target))
	detected := false
	if chosen == "" {
		chosen = skills.Detect(root)
		detected = true
	}

	result, err := skills.Install(skills.InstallOptions{
		Root: root, Target: chosen, Only: flags.Arg(0), Force: *force, Personal: *personal,
	})
	if err != nil {
		return err
	}
	how := "target"
	if detected {
		how = "detected target"
	}
	if _, err := fmt.Fprintf(output, "%s: %s\n", how, result.Target); err != nil {
		return err
	}
	for _, path := range result.Written {
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			relative = path
		}
		if *personal {
			relative = "~/" + relative
		}
		if _, err := fmt.Fprintf(output, "wrote: %s\n", relative); err != nil {
			return err
		}
	}
	return nil
}
