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

	"github.com/oberthci/oberth/internal/pipelinegen"
)

// allProjectTypes are the values --type accepts. They are the toolchains the
// generator can write a real build for; anything else gets the scaffold,
// which is what "generic" asks for explicitly.
var allProjectTypes = []pipelinegen.Kind{
	pipelinegen.KindGo, pipelinegen.KindNode, pipelinegen.KindMaven, pipelinegen.KindUnknown,
}

func runInit(_ context.Context, arguments []string, output io.Writer) error {
	var typeOverride string
	var force bool
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&typeOverride, "type", "",
		"project type: go|node|maven|generic (default: read the repository's own build workflow and manifests)")
	flags.BoolVar(&force, "force", false, "overwrite existing .oberth/build.yaml")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(output)
			flags.Usage()
			return nil
		}
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("%w: init accepts flags only", errUsage)
	}
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}
	return executeInit(root, typeOverride, force, output)
}

// executeInit reads the repository, generates its pipeline, and writes it.
//
// The generator this replaces wrote the same demo DAG for every repository:
// it copied a file, checksummed it, and went green. That is worse than
// writing nothing, because a repository nobody has translated then looks
// exactly like one that works. Everything below either produces steps that
// really run this repository's build, or says in the file and on the terminal
// that it did not.
func executeInit(root, typeOverride string, force bool, output io.Writer) error {
	project := pipelinegen.DetectProject(root)
	if workflow, ok := pipelinegen.FindBuildWorkflow(root); ok {
		pipelinegen.Apply(workflow, &project)
	}

	reason := "the repository's own files"
	if typeOverride != "" {
		kind, err := parseProjectType(typeOverride)
		if err != nil {
			return err
		}
		project.Kind = kind
		reason = "--type " + typeOverride
	}

	result := pipelinegen.Generate(project)

	if err := writePipeline(root, result.YAML, force); err != nil {
		return err
	}

	return report(output, project, result, reason)
}

func parseProjectType(value string) (pipelinegen.Kind, error) {
	// "generic" is the word the flag has always used for "I know you cannot
	// work this out"; the generator calls that state unknown.
	if strings.EqualFold(value, "generic") {
		return pipelinegen.KindUnknown, nil
	}
	for _, kind := range allProjectTypes {
		if strings.EqualFold(value, string(kind)) {
			return kind, nil
		}
	}
	return "", fmt.Errorf("%w: unknown project type %q; use go, node, maven, or generic", errUsage, value)
}

// writePipeline writes the document atomically, refusing to follow a symlink
// at either the directory or the file.
func writePipeline(root, content string, force bool) error {
	oberthDir := filepath.Join(root, ".oberth")
	target := filepath.Join(oberthDir, "build.yaml")
	if info, err := os.Lstat(oberthDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New(".oberth is a symlink; refusing to follow")
		}
	}
	if !force {
		if _, err := os.Stat(target); err == nil {
			return errors.New(".oberth/build.yaml already exists; use --force to overwrite")
		}
	}
	if force {
		if info, err := os.Lstat(target); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New(".oberth/build.yaml is a symlink; refusing to follow")
			}
		}
	}
	if err := os.MkdirAll(oberthDir, 0o750); err != nil { // #nosec G301 -- project directory, not secrets
		return fmt.Errorf("create .oberth directory: %w", err)
	}
	tmpFile, err := os.CreateTemp(oberthDir, ".build.yaml.*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpName := tmpFile.Name()
	if _, writeErr := tmpFile.WriteString(content); writeErr != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
		return writeErr
	}
	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// report says what was written and what is still needed.
//
// The old summary claimed "5 steps, 3 dependencies, ~30 seconds to run" for
// every repository, which was true of the demo and of nothing else. These
// numbers are counted from the document that was actually written.
func report(output io.Writer, project pipelinegen.Project, result pipelinegen.Result, reason string) error {
	var out strings.Builder

	kind := string(project.Kind)
	if project.Kind == pipelinegen.KindUnknown {
		kind = "generic"
	}
	fmt.Fprintf(&out, "detected: %s (%s)\n", kind, reason)

	if len(project.Sources) > 0 {
		out.WriteString("read:\n")
		for _, line := range project.Sources {
			out.WriteString("  - " + line + "\n")
		}
	}
	out.WriteString("\nwrote: .oberth/build.yaml\n\n")
	out.WriteString("  " + strings.Join(result.Steps, " -> ") + "\n\n")
	fmt.Fprintf(&out, "  %d steps, run in order, stopping at the first red.\n", len(result.Steps))

	if !result.Complete {
		out.WriteString("\nTHIS PIPELINE IS NOT FINISHED.\n")
		out.WriteString("Nothing in this repository said how it is built, so rather than write\n")
		out.WriteString("steps that pass by doing nothing, oberth init wrote one step that fails.\n")
		out.WriteString("Edit .oberth/build.yaml and replace it with the real build.\n")
	}

	if len(project.Untranslated) > 0 {
		fmt.Fprintf(&out, "\n%d thing(s) could not be translated. They are listed at the top of the\n", len(project.Untranslated))
		out.WriteString("file; read them before trusting a green run.\n")
	}

	if result.GrantCommand != "" {
		out.WriteString("\nThis pipeline reads a secret, so it needs one grant before its first push.\n")
		out.WriteString("Without it the run is refused before any step starts:\n\n")
		out.WriteString("    " + result.GrantCommand + "\n")
	}

	if result.Complete {
		out.WriteString("\nnext: commit and push to Oberth, and watch the run in the dashboard.\n")
	}

	_, err := io.WriteString(output, out.String())
	return err
}
