package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/oberthci/oberth/internal/client"
	"github.com/oberthci/oberth/internal/clientprofile"
	"github.com/oberthci/oberth/internal/pipelinegen"
)

const useUsage = "use [<profile>]"

func runUse(ctx context.Context, arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("use", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(output, "Usage: oberth "+useUsage)
			return nil
		}
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if flags.NArg() > 1 {
		return fmt.Errorf("%w: %s", errUsage, useUsage)
	}
	working, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}
	root := gitRoot(working)
	if flags.NArg() == 0 {
		return printProfiles(output, root)
	}
	name := flags.Arg(0)
	profile, err := clientprofile.Load(name)
	if err != nil {
		return err
	}
	if root == "" {
		if err := clientprofile.SetDefault(name); err != nil {
			return err
		}
		fmt.Fprintf(output, "default profile is now %s (%s); source ~/.config/oberth/env in open shells\n", name, profile.BaseURL)
		return nil
	}
	return switchCheckout(ctx, output, root, profile)
}

func printProfiles(output io.Writer, root string) error {
	names, err := clientprofile.List()
	if err != nil {
		return err
	}
	if len(names) == 0 {
		fmt.Fprintln(output, "no profiles yet; an install writes one, or: oberth profile add <name> <base-url> --ca <file>")
		return nil
	}
	pinned := ""
	if root != "" {
		pinned = clientprofile.ForCheckout(root)
	}
	def := clientprofile.Default()
	for _, name := range names {
		profile, err := clientprofile.Load(name)
		if err != nil {
			continue
		}
		marks := ""
		if name == def {
			marks += " (default)"
		}
		if name == pinned {
			marks += " (this checkout)"
		}
		fmt.Fprintf(output, "%-12s %s%s\n", name, profile.BaseURL, marks)
	}
	return nil
}

func switchCheckout(ctx context.Context, output io.Writer, root string, profile clientprofile.Profile) error {
	config := client.Config{BaseURL: profile.BaseURL, CACert: profile.CACert, TokenCommand: profile.TokenCommand}
	api, err := client.New(ctx, config)
	if err != nil {
		return err
	}
	var status struct {
		SSHEndpoint string `json:"ssh_endpoint"`
	}
	if err := api.Get(ctx, "/api/status", nil, &status); err != nil {
		return sealedAdvice(fmt.Errorf("read %s: %w", profile.BaseURL, err))
	}
	endpoint := strings.TrimSpace(status.SSHEndpoint)
	if endpoint == "" || strings.HasPrefix(endpoint, ":") {
		return fmt.Errorf("%s does not advertise an address clients can push to (it reports %q); its install needs --ssh-advertise or a certificate name", profile.Name, endpoint)
	}
	name := repositoryName(root)
	if name == "" {
		return fmt.Errorf("%s has no origin remote to read a repository name from", root)
	}
	want := "ssh://git@" + endpoint + "/" + name
	existing, _ := gitIn(root, "remote", "get-url", "oberth")
	switch {
	case strings.TrimSpace(existing) == "":
		if _, err := gitIn(root, "remote", "add", "oberth", want); err != nil {
			return fmt.Errorf("add the oberth remote: %w", err)
		}
	case strings.TrimSpace(existing) != want:
		if _, err := gitIn(root, "remote", "set-url", "oberth", want); err != nil {
			return fmt.Errorf("update the oberth remote: %w", err)
		}
	}
	if err := ensureSSHCommandIn(root, profile.SSHCommand, func(string, ...any) {}); err != nil {
		return err
	}
	if err := clientprofile.Pin(root, profile.Name); err != nil {
		return err
	}
	fmt.Fprintf(output, "%s now pushes to %s (%s)\n", root, profile.Name, want)
	fmt.Fprintln(output, "next: git push oberth HEAD:refs/heads/<branch>, or oberth onboard if this server does not know the repository yet")
	return nil
}

func repositoryName(root string) string {
	if existing, err := gitIn(root, "remote", "get-url", "oberth"); err == nil {
		trimmed := strings.TrimSpace(existing)
		if index := strings.LastIndex(trimmed, "/"); index >= 0 && index < len(trimmed)-1 {
			return strings.TrimSuffix(trimmed[index+1:], ".git")
		}
	}
	_, name := pipelinegen.OriginIdentity(root)
	return name
}

func gitRoot(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output() // #nosec G204 -- fixed verbs.
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitIn(root string, arguments ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...) // #nosec G204 -- fixed verbs, repository path from the caller.
	out, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return "", fmt.Errorf("git %s: %s", strings.Join(arguments, " "), strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(arguments, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}
