package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oberthci/oberth/internal/localbao"
	"github.com/oberthci/oberth/internal/localinstall"
)

// storeMaterial resolves the signing key and the store certificate the way
// `oberth install --engine=docker` laid them out: under the install root,
// ~/.oberth/local unless --root says otherwise. An explicit --signing-key
// still wins, and the certificate is looked for beside it.
//
// Every docker store verb resolves through here, and it has to be the same
// resolution as the install's: the verbs used to default to ~/.oberth while
// the install wrote ~/.oberth/local, so a bare `secretstore put` looked for
// the store certificate in a directory the install never touched, and then
// minted one there, leaving two certificates and a put that could not
// verify the store it was talking to.
func storeMaterial(options *localbao.Options, root, signingKey string) error {
	installRoot, err := resolveInstallRoot(root)
	if err != nil {
		return err
	}
	options.SigningKeyPath = strings.TrimSpace(signingKey)
	if options.SigningKeyPath == "" {
		options.SigningKeyPath = localinstall.NewLayout(installRoot).SigningKey
	}
	return nil
}

// locateStoreTLS names the store's certificate for a verb that only talks to
// the store. It creates nothing: a verb that reads has no business minting
// a certificate, and one that did would silently start a second trust
// anchor whenever it was pointed at the wrong root.
func locateStoreTLS(options *localbao.Options) error {
	directory := filepath.Join(filepath.Dir(options.SigningKeyPath), "openbao-tls")
	options.TLSCertPath = filepath.Join(directory, "tls.crt")
	options.TLSKeyPath = filepath.Join(directory, "tls.key")
	if _, err := os.Stat(options.TLSCertPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no store certificate at %s; run `oberth secretstore init --engine=docker` "+
				"(or `oberth install --engine=docker --secretstore`) first, or name the install root with --root",
				options.TLSCertPath)
		}
		return fmt.Errorf("read the store certificate: %w", err)
	}
	return nil
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
	root := flags.String("root", "", "install root the signing key and the store certificate live under (default ~/.oberth/local)")
	signingKey := flags.String("signing-key", "", "run-identity signing key path (default <root>/jwt-signing.pem)")
	tlsDir := flags.String("tls-dir", "", "directory holding the store's own server certificate (default beside the signing key)")
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
	if err := storeMaterial(&options, *root, *signingKey); err != nil {
		return err
	}
	if err := ensureStoreTLS(&options, *tlsDir); err != nil {
		return err
	}
	options.Output = output
	if err := localbao.Init(ctx, options); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(output, "\nStart the server with:\n"+
		"  oberth serve --engine=docker ... \\\n"+
		"    --secretstore-address=%s \\\n"+
		"    --secretstore-ca-cert=%s \\\n"+
		"    --secretstore-jwt-signing-key=%s\n", options.Address, options.TLSCertPath, options.SigningKeyPath)
	return nil
}

// ensureStoreTLS issues the store's own server certificate if it has none.
//
// The certificate names every spelling of the store a consumer uses: the
// loopback the server talks to, and the daemon's host gateway a step container
// talks to. A certificate that names one and not the other produces a
// handshake failure that reads as an unreachable store.
func ensureStoreTLS(options *localbao.Options, directory string) error {
	trimmed := strings.TrimSpace(directory)
	if trimmed == "" {
		trimmed = filepath.Join(filepath.Dir(options.SigningKeyPath), "openbao-tls")
	}
	options.TLSCertPath = filepath.Join(trimmed, "tls.crt")
	options.TLSKeyPath = filepath.Join(trimmed, "tls.key")
	_, err := localinstall.EnsureSelfSignedCertificate(options.TLSCertPath, options.TLSKeyPath,
		"oberth-openbao",
		[]string{"localhost", localbao.ContainerHostName, localbao.DefaultContainer},
		[]net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, time.Now())
	return err
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
	root := flags.String("root", "", "install root the store certificate lives under (default ~/.oberth/local)")
	signingKey := flags.String("signing-key", "", "run-identity signing key path (default <root>/jwt-signing.pem), used to locate the store certificate")
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
	if err := storeMaterial(&options, *root, *signingKey); err != nil {
		return err
	}
	if err := locateStoreTLS(&options); err != nil {
		return err
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
	root := flags.String("root", "", "install root the store certificate lives under (default ~/.oberth/local)")
	signingKey := flags.String("signing-key", "", "run-identity signing key path (default <root>/jwt-signing.pem), used to locate the store certificate")
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
	if err := storeMaterial(&options, *root, *signingKey); err != nil {
		return err
	}
	// The store's certificate is this machine's own, so the anchor has to be
	// named explicitly: without it the platform verifier is consulted and it
	// has never seen this signer.
	if err := locateStoreTLS(&options); err != nil {
		return err
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
