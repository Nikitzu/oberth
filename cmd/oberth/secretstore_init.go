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

	"github.com/oberthci/oberth/internal/localbao"
)

// DefaultSigningKeyPath is where the run-identity signing key lives when the
// operator names none. Under the user's own home, mode 0600, because it mints
// the identity every credentialed run logs in with.
func defaultSigningKeyPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve the home directory: %w", err)
	}
	return filepath.Join(home, ".oberth", "jwt-signing.pem"), nil
}

// runSecretStoreInit is the one-time ceremony for a clusterless server:
// OpenBao in a container with file storage, initialised and unsealed with its
// keys in the keychain, a jwt auth mount trusting this server's signing key, a
// KV v2 mount, and one role and policy per tier.
//
// It is idempotent. Running it again on a configured store re-asserts the
// configuration and changes nothing else, which is what makes it safe to run
// when something looks wrong.
func runSecretStoreInit(ctx context.Context, arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("secretstore init", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	engine := flags.String("engine", "",
		"execution engine to set the store up for; only \"docker\" is supported, because the argo engine's store is provisioned by `oberth install`")
	options := localbao.Options{}
	flags.StringVar(&options.Docker, "docker-binary", "docker", "Docker CLI to drive")
	flags.StringVar(&options.Image, "image", localbao.DefaultImage, "OpenBao image, digest pinned")
	flags.StringVar(&options.Container, "container", localbao.DefaultContainer, "container name")
	flags.StringVar(&options.Volume, "volume", localbao.DefaultVolume, "data volume name")
	flags.StringVar(&options.Listen, "listen", localbao.DefaultListen, "host address to publish the store on; keep it on the loopback")
	flags.StringVar(&options.Address, "address", localbao.DefaultAddress, "API address to configure the store through")
	flags.StringVar(&options.KVMount, "kv-mount", localbao.DefaultKVMount, "KV v2 mount to create")
	signingKey := flags.String("signing-key", "", "run-identity signing key path (default ~/.oberth/jwt-signing.pem)")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(output)
			flags.Usage()
			return nil
		}
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("%w: secretstore init accepts flags only", errUsage)
	}
	if strings.TrimSpace(*engine) != engineDocker {
		return fmt.Errorf("%w: secretstore init requires --engine=docker; the argo engine's store is provisioned by `oberth install`", errUsage)
	}
	options.SigningKeyPath = strings.TrimSpace(*signingKey)
	if options.SigningKeyPath == "" {
		path, err := defaultSigningKeyPath()
		if err != nil {
			return err
		}
		options.SigningKeyPath = path
	}
	options.Output = output
	if err := localbao.Init(ctx, options); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(output, "\nStart the server with:\n"+
		"  oberth serve --engine=docker ... \\\n"+
		"    --secretstore-address=%s \\\n"+
		"    --secretstore-jwt-signing-key=%s\n", options.Address, options.SigningKeyPath)
	return nil
}

// runSecretStoreUnseal is the reboot path. A sealed store presents to a
// pipeline as a connection failure rather than as anything mentioning a seal,
// which is the single most reported piece of local friction, so it gets its
// own verb rather than being a side effect of init.
func runSecretStoreUnseal(ctx context.Context, arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("secretstore unseal", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	engine := flags.String("engine", "", "only \"docker\" is supported")
	options := localbao.Options{}
	flags.StringVar(&options.Docker, "docker-binary", "docker", "Docker CLI to drive")
	flags.StringVar(&options.Container, "container", localbao.DefaultContainer, "container name")
	flags.StringVar(&options.Address, "address", localbao.DefaultAddress, "API address")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(output)
			flags.Usage()
			return nil
		}
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if strings.TrimSpace(*engine) != engineDocker {
		return fmt.Errorf("%w: secretstore unseal requires --engine=docker", errUsage)
	}
	options.Output = output
	return localbao.Unseal(ctx, options)
}

// runSecretStorePut writes one secret, which is the only step an operator
// repeats after the setup.
func runSecretStorePut(ctx context.Context, arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("secretstore put", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	engine := flags.String("engine", "", "only \"docker\" is supported")
	options := localbao.Options{}
	flags.StringVar(&options.Address, "address", localbao.DefaultAddress, "API address")
	flags.StringVar(&options.KVMount, "kv-mount", localbao.DefaultKVMount, "KV v2 mount")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(output)
			flags.Usage()
			return nil
		}
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if strings.TrimSpace(*engine) != engineDocker {
		return fmt.Errorf("%w: secretstore put requires --engine=docker", errUsage)
	}
	if flags.NArg() < 2 {
		return fmt.Errorf("%w: secretstore put --engine=docker <path> <field>=<value> [<field>=<value>...]", errUsage)
	}
	fields := map[string]string{}
	for _, pair := range flags.Args()[1:] {
		name, value, found := strings.Cut(pair, "=")
		if !found || strings.TrimSpace(name) == "" {
			return fmt.Errorf("%w: %q is not <field>=<value>", errUsage, pair)
		}
		fields[name] = value
	}
	options.Output = output
	return localbao.PutSecret(ctx, options, flags.Arg(0), fields)
}
