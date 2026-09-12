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

	"github.com/oberthci/oberth/internal/clientprofile"
	"github.com/oberthci/oberth/internal/installer"
)

const profileUsage = "profile add <name> <base-url> --ca <file> [--token-command <cmd>] [--ssh-command <cmd>] | profile list"

func runProfile(_ context.Context, arguments []string, output io.Writer) error {
	if len(arguments) == 0 {
		return fmt.Errorf("%w: %s", errUsage, profileUsage)
	}
	switch arguments[0] {
	case "list":
		return printProfiles(output, "")
	case "add":
		return addProfile(arguments[1:], output)
	default:
		return fmt.Errorf("%w: %s", errUsage, profileUsage)
	}
}

func addProfile(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("profile add", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	ca := flags.String("ca", "", "the server's CA certificate file")
	tokenCommand := flags.String("token-command", "", "command whose output is the bearer token (default: this platform's secret store, entry named after the profile)")
	sshCommand := flags.String("ssh-command", "", "GIT_SSH_COMMAND for pushes to this server (default: none, the ssh agent's keys)")
	makeDefault := flags.Bool("default", false, "also make it the default profile")
	if err := flags.Parse(permuteFlagsFirst(arguments)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(output, "Usage: oberth "+profileUsage)
			return nil
		}
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if flags.NArg() != 2 {
		return fmt.Errorf("%w: %s", errUsage, profileUsage)
	}
	name, baseURL := flags.Arg(0), strings.TrimSpace(flags.Arg(1))
	if err := clientprofile.ValidateName(name); err != nil {
		return err
	}
	if !strings.HasPrefix(baseURL, "https://") {
		return fmt.Errorf("%w: the base URL must start with https://", errUsage)
	}
	if strings.TrimSpace(*ca) == "" {
		return fmt.Errorf("%w: --ca names the server's CA certificate; fetch it from the server's ~/.config/oberth/ca.crt", errUsage)
	}
	authority, err := os.ReadFile(*ca) // #nosec G304 -- the operator named the file.
	if err != nil {
		return fmt.Errorf("read %s: %w", *ca, err)
	}
	dir, err := clientprofile.Dir(name)
	if err != nil {
		return err
	}
	caPath := filepath.Join(dir, "ca.crt")
	read := strings.TrimSpace(*tokenCommand)
	storeHint := ""
	if read == "" {
		read, storeHint = installer.TokenCommandForHost(name)
	}
	env := installer.RenderClientEnv(baseURL, caPath, read)
	if strings.TrimSpace(*sshCommand) != "" {
		env += fmt.Sprintf("\nexport OBERTH_SSH_COMMAND=%q\n", strings.TrimSpace(*sshCommand))
	}
	mcp, err := installer.RenderMCPConfig(baseURL, read)
	if err != nil {
		mcp = nil
	}
	if _, err := clientprofile.Write(name, env, authority, mcp); err != nil {
		return err
	}
	fmt.Fprintf(output, "profile %s -> %s written to %s\n", name, baseURL, installer.DisplayPath(dir))
	if storeHint != "" {
		fmt.Fprintf(output, "store its bearer token where the profile reads it:\n    %s\n", storeHint)
	}
	if *makeDefault {
		if err := clientprofile.SetDefault(name); err != nil {
			return err
		}
		fmt.Fprintf(output, "%s is now the default profile\n", name)
	}
	fmt.Fprintf(output, "switch a checkout to it with: oberth use %s\n", name)
	return nil
}
