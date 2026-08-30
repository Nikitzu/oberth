package localbao

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// DefaultImage is pinned by digest for the same reason every pipeline
	// image is: this container holds the credentials for every repository this
	// server builds, and a floating tag is a supply chain someone else owns.
	DefaultImage = "openbao/openbao:2.4.1@sha256:597f62847dd382382056a1d6704d50465908c2040038c4611832a23269a67112"
	// DefaultContainer and DefaultVolume are stable names so a re-run finds
	// what the last run made.
	DefaultContainer = "oberth-openbao"
	DefaultVolume    = "oberth-openbao-data"
	// DefaultListen binds the loopback only. Step containers reach it through
	// the daemon's host gateway, and nothing off this machine can.
	DefaultListen = "127.0.0.1:8200"
	// DefaultAddress is HTTPS, and not negotiable. This is the channel a run's
	// credentials come back over, it crosses the container boundary through
	// the daemon's gateway, and `oberth secretstore exec` refuses a plain HTTP
	// store for exactly that reason. The certificate is this machine's own and
	// is handed to both consumers as their trust anchor.
	DefaultAddress = "https://127.0.0.1:8200"
	// ContainerAddress is the same store as a step container sees it. Inside a
	// container 127.0.0.1 is the container, so the loopback address the server
	// uses would reach nothing.
	ContainerAddress = "https://host.docker.internal:8200"
	// ContainerHostName is the name the address above resolves through, mapped
	// to the daemon's host gateway on every credentialed step.
	ContainerHostName = "host.docker.internal"
	// DefaultKVMount is the KV v2 mount both engines use.
	DefaultKVMount = "oberth"
	// DefaultJWTMount is where the jwt auth method lives.
	DefaultJWTMount = "jwt"
	// DefaultCIRole and DefaultReleaseRole are the two tiers. Each binds its
	// own subject and carries its own policy; the boundary between them is
	// those two policies and nothing in Oberth.
	DefaultCIRole      = "oberth-ci"
	DefaultReleaseRole = "oberth-release"
	// RoleTokenTTLSeconds bounds a login. It matches the projected
	// ServiceAccount token's 600 seconds in-cluster.
	RoleTokenTTLSeconds = 600
	// Audience is the audience the roles bind and the server mints.
	Audience = "oberth"
	// Issuer is the issuer claim the mount binds.
	Issuer = "oberth"
)

// Options configures the ceremony. Every field has a working default.
type Options struct {
	Docker         string
	Image          string
	Container      string
	Volume         string
	Listen         string
	Address        string
	KVMount        string
	JWTMount       string
	CIRole         string
	ReleaseRole    string
	SigningKeyPath string
	// TLSCertPath and TLSKeyPath are the store's own server certificate, on
	// the host. Their directory is mounted into the container read-only.
	TLSCertPath string
	TLSKeyPath  string

	Stash  SecretStash
	HTTP   *http.Client
	Run    func(ctx context.Context, name string, args ...string) ([]byte, error)
	Now    func() time.Time
	Output io.Writer
}

func (options *Options) applyDefaults() {
	if strings.TrimSpace(options.Docker) == "" {
		options.Docker = "docker"
	}
	if strings.TrimSpace(options.Image) == "" {
		options.Image = DefaultImage
	}
	if strings.TrimSpace(options.Container) == "" {
		options.Container = DefaultContainer
	}
	if strings.TrimSpace(options.Volume) == "" {
		options.Volume = DefaultVolume
	}
	if strings.TrimSpace(options.Listen) == "" {
		options.Listen = DefaultListen
	}
	if strings.TrimSpace(options.Address) == "" {
		options.Address = DefaultAddress
	}
	if strings.TrimSpace(options.KVMount) == "" {
		options.KVMount = DefaultKVMount
	}
	if strings.TrimSpace(options.JWTMount) == "" {
		options.JWTMount = DefaultJWTMount
	}
	if strings.TrimSpace(options.CIRole) == "" {
		options.CIRole = DefaultCIRole
	}
	if strings.TrimSpace(options.ReleaseRole) == "" {
		options.ReleaseRole = DefaultReleaseRole
	}
	if options.HTTP == nil {
		options.HTTP = &http.Client{Timeout: 15 * time.Second, Transport: options.transport()}
	}
	if options.Run == nil {
		options.Run = runCommand
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Output == nil {
		options.Output = io.Discard
	}
	if options.Stash == nil {
		options.Stash = KeychainStash{}
	}
}

// transport trusts exactly this store's own certificate and nothing else. The
// system pool is not consulted: the only correct answer for this address is
// the certificate this command issued, so widening the anchor set would only
// make a misconfiguration harder to see.
func (options Options) transport() http.RoundTripper {
	body, err := os.ReadFile(strings.TrimSpace(options.TLSCertPath)) // #nosec G304 -- an operator-supplied path.
	if err != nil {
		return http.DefaultTransport
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(body) {
		return http.DefaultTransport
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return transport
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return stdout.Bytes(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, detail)
	}
	return stdout.Bytes(), nil
}

func (options Options) say(format string, args ...any) {
	_, _ = fmt.Fprintf(options.Output, format+"\n", args...)
}

// Init performs the whole one-time setup, and is safe to re-run.
func Init(ctx context.Context, options Options) error {
	options.applyDefaults()
	if err := options.ensureContainer(ctx); err != nil {
		return err
	}
	if err := options.waitForAPI(ctx); err != nil {
		return err
	}
	root, err := options.initialiseAndUnseal(ctx)
	if err != nil {
		return err
	}
	publicKey, err := options.ensureSigningKey()
	if err != nil {
		return err
	}
	return options.configure(ctx, root, publicKey)
}

// ensureContainer starts OpenBao with file storage on a named volume.
//
// Not dev mode. Dev mode holds everything in memory, so the first restart
// loses every secret and every policy, and the failure presents as an empty
// store rather than as a missing one.
func (options Options) ensureContainer(ctx context.Context) error {
	state, err := options.Run(ctx, options.Docker, "inspect", "--format", "{{.State.Running}}", options.Container)
	if err == nil {
		if strings.TrimSpace(string(state)) == "true" {
			options.say("openbao: container %s is already running", options.Container)
			return nil
		}
		if _, err := options.Run(ctx, options.Docker, "start", options.Container); err != nil {
			return fmt.Errorf("localbao: start the existing %s container: %w", options.Container, err)
		}
		options.say("openbao: restarted the existing container %s", options.Container)
		return nil
	}
	if _, err := options.Run(ctx, options.Docker, "volume", "create", options.Volume); err != nil {
		return fmt.Errorf("localbao: create the openbao data volume: %w", err)
	}
	if strings.TrimSpace(options.TLSCertPath) == "" || strings.TrimSpace(options.TLSKeyPath) == "" {
		return errors.New("localbao: a server certificate and key are required; the credential channel is not run in the clear")
	}
	config := `{"storage":{"file":{"path":"/openbao/file"}},` +
		`"listener":{"tcp":{"address":"0.0.0.0:8200",` +
		`"tls_cert_file":"/openbao/tls/tls.crt","tls_key_file":"/openbao/tls/tls.key"}},` +
		`"disable_mlock":true,"ui":false}`
	_, err = options.Run(ctx, options.Docker, "run", "--detach",
		"--name", options.Container,
		"--restart", "unless-stopped",
		// The loopback only. A store bound to every interface on a laptop on
		// a shared network is a different security posture than the one this
		// design claims.
		"--publish", options.Listen+":8200",
		"--volume", options.Volume+":/openbao/file",
		"--volume", filepath.Dir(options.TLSCertPath)+":/openbao/tls:ro",
		"--cap-add", "IPC_LOCK",
		"--env", "BAO_LOCAL_CONFIG="+config,
		"--", options.Image, "server")
	if err != nil {
		return fmt.Errorf("localbao: start openbao: %w", err)
	}
	options.say("openbao: started %s with file storage on volume %s", options.Container, options.Volume)
	return nil
}

// health is the subset of /v1/sys/health this needs.
type health struct {
	Initialized bool `json:"initialized"`
	Sealed      bool `json:"sealed"`
}

func (options Options) waitForAPI(ctx context.Context) error {
	deadline := options.Now().Add(60 * time.Second)
	var lastErr error
	for options.Now().Before(deadline) {
		if _, err := options.health(ctx); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("localbao: openbao at %s did not answer within a minute: %w", options.Address, lastErr)
}

func (options Options) health(ctx context.Context) (health, error) {
	var status health
	// Every non-200 health code (501 uninitialised, 503 sealed) is a real
	// answer, so the body is decoded regardless of the status.
	_, err := options.call(ctx, http.MethodGet, "/v1/sys/health?standbyok=true&sealedcode=200&uninitcode=200",
		"", nil, &status)
	return status, err
}

// initialiseAndUnseal brings the store to a usable state and returns the root
// token, from the keychain when the store was already initialised.
func (options Options) initialiseAndUnseal(ctx context.Context) (string, error) {
	status, err := options.health(ctx)
	if err != nil {
		return "", err
	}
	if !status.Initialized {
		// keys_base64 is what the HTTP API returns. The CLI's own JSON output
		// calls the same field unseal_keys_b64, which is why the installer's
		// parser (which reads CLI output) uses that name and this one does not.
		var result struct {
			KeysBase64 []string `json:"keys_base64"`
			RootToken  string   `json:"root_token"`
		}
		if _, err := options.call(ctx, http.MethodPut, "/v1/sys/init", "",
			map[string]any{"secret_shares": 1, "secret_threshold": 1}, &result); err != nil {
			return "", fmt.Errorf("localbao: initialise openbao: %w", err)
		}
		if len(result.KeysBase64) == 0 || result.RootToken == "" {
			return "", errors.New("localbao: openbao reported an initialisation with no keys")
		}
		// Stored before anything else is attempted. After `operator init` the
		// keys exist nowhere but this process, so a failure in a later step
		// must not be the thing that loses them.
		if err := options.Stash.Put(ctx, UnsealKeychainService, result.KeysBase64[0]); err != nil {
			return "", err
		}
		if err := options.Stash.Put(ctx, RootKeychainService, result.RootToken); err != nil {
			return "", err
		}
		options.say("openbao: initialised; unseal key and root token stored in the keychain under %s and %s",
			UnsealKeychainService, RootKeychainService)
		status.Sealed = true
	} else {
		options.say("openbao: already initialised")
	}
	if status.Sealed {
		key, err := options.Stash.Get(ctx, UnsealKeychainService)
		if err != nil {
			return "", fmt.Errorf("localbao: the store is sealed and the keychain holds no unseal key under %s: %w",
				UnsealKeychainService, err)
		}
		if _, err := options.call(ctx, http.MethodPut, "/v1/sys/unseal", "", map[string]any{"key": key}, nil); err != nil {
			return "", fmt.Errorf("localbao: unseal: %w", err)
		}
		options.say("openbao: unsealed from the keychain")
	}
	root, err := options.Stash.Get(ctx, RootKeychainService)
	if err != nil {
		return "", fmt.Errorf("localbao: the keychain holds no root token under %s: %w", RootKeychainService, err)
	}
	return root, nil
}

// Unseal brings an already-initialised store back to usable, which is what a
// laptop reboot needs and nothing else does.
func Unseal(ctx context.Context, options Options) error {
	options.applyDefaults()
	if err := options.ensureContainer(ctx); err != nil {
		return err
	}
	if err := options.waitForAPI(ctx); err != nil {
		return err
	}
	_, err := options.initialiseAndUnseal(ctx)
	return err
}

// ensureSigningKey creates the server's run-identity signing key if it is
// absent, and returns the public half for the jwt mount.
func (options Options) ensureSigningKey() ([]byte, error) {
	path := strings.TrimSpace(options.SigningKeyPath)
	if path == "" {
		return nil, errors.New("localbao: a signing key path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("localbao: create the signing key directory: %w", err)
	}
	body, err := os.ReadFile(path) // #nosec G304 -- an operator-supplied path.
	if errors.Is(err, os.ErrNotExist) {
		key, genErr := rsa.GenerateKey(rand.Reader, 2048)
		if genErr != nil {
			return nil, fmt.Errorf("localbao: generate the signing key: %w", genErr)
		}
		body = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
		if err := os.WriteFile(path, body, 0o600); err != nil {
			return nil, fmt.Errorf("localbao: write the signing key: %w", err)
		}
		options.say("identity: generated the run-identity signing key at %s", path)
	} else if err != nil {
		return nil, fmt.Errorf("localbao: read the signing key: %w", err)
	} else {
		options.say("identity: reusing the run-identity signing key at %s", path)
	}
	block, _ := pem.Decode(body)
	if block == nil {
		return nil, fmt.Errorf("localbao: the signing key at %s is not PEM encoded", path)
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		parsed, pkcs8Err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if pkcs8Err != nil {
			return nil, fmt.Errorf("localbao: parse the signing key: %w", pkcs8Err)
		}
		rsaKey, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("localbao: the signing key is %T, and the jwt auth method needs RSA", parsed)
		}
		key = rsaKey
	}
	encoded, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: encoded}), nil
}
