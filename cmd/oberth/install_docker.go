package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/oberthci/oberth/internal/installer"
	"github.com/oberthci/oberth/internal/localbao"
	"github.com/oberthci/oberth/internal/localinstall"
)

// Default ports for a local install. They are not the chart's NodePorts,
// because a machine may well be running a cluster install at the same time and
// two servers fighting over one port is the first thing that would go wrong.
const (
	defaultLocalHTTPSPort = 8443
	defaultLocalSSHPort   = 8022
)

// runInstallDocker installs and starts a clusterless Oberth server.
//
// It is the whole ceremony: a data directory, a TLS identity for localhost,
// the SSH host and upstream keys, the developer's own push identity, the
// server itself (in the foreground or as a launchd agent), an admin uplink
// whose bearer token goes into the platform secret store, the CLI and MCP
// client configuration in the place the cluster install writes it, and the
// push banner the cluster install ends with.
//
// Every step is create-if-absent, so a second run finds what the first made.
// The one thing it will not do twice is mint a second uplink for a key that
// already has one: a re-run reuses the existing identity and says so rather
// than accumulating credentials.
func runInstallDocker(ctx context.Context, arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("install --engine=docker", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	_ = flags.String("engine", engineDocker, "execution engine to install")
	root := flags.String("root", "", "install root holding the data directory, TLS material and SSH keys (default ~/.oberth/local)")
	httpsPort := flags.Int("https-port", defaultLocalHTTPSPort, "loopback port for the dashboard and the API")
	sshPort := flags.Int("ssh-port", defaultLocalSSHPort, "loopback port for the Git ingest")
	launchd := flags.Bool("launchd", false, "install a launchd agent that keeps the server running across logins, instead of running it in the foreground")
	shellProfile := flags.String("shell-profile", "", "shell profile to add the client environment line to (for example ~/.zshrc); empty prints the line instead")
	secretStore := flags.Bool("secretstore", false, "also run `secretstore init --engine=docker`, so credentialed pipelines work from the first push")
	publishOnGreen := flags.Bool("publish-on-green", false, "publish a green run to the upstream automatically")
	var releaseSecretPaths stringList
	flags.Var(&releaseSecretPaths, "secretstore-path",
		"system-namespace secret path a release pipeline may declare (repeatable, exactly as it appears in oberth.ci/secret-paths); "+
			"this is the clusterless stand-in for the approval table, and only the release tier can reach these")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(output)
			flags.Usage()
			return nil
		}
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("%w: install accepts flags only, no positional arguments", errUsage)
	}
	if *httpsPort == *sshPort {
		return fmt.Errorf("%w: --https-port and --ssh-port must differ", errUsage)
	}

	installRoot, err := resolveInstallRoot(*root)
	if err != nil {
		return err
	}
	layout := localinstall.NewLayout(installRoot)
	created, err := localinstall.EnsureMaterial(layout, time.Now())
	if err != nil {
		return err
	}
	if len(created) == 0 {
		say(output, "material   reusing the existing TLS and SSH material under %s", installer.DisplayPath(installRoot))
	}
	for _, path := range created {
		say(output, "material   created %s", installer.DisplayPath(path))
	}

	// Docker first. Everything below assumes a daemon, and a message about it
	// here is much cheaper than a server that starts and refuses every push.
	if err := requireDockerDaemon(ctx); err != nil {
		return err
	}

	storeOptions := localbao.Options{SigningKeyPath: layout.SigningKey, Output: output}
	if *secretStore {
		if err := ensureStoreTLS(&storeOptions, ""); err != nil {
			return err
		}
		if err := localbao.Init(ctx, storeOptions); err != nil {
			return fmt.Errorf("secret store setup: %w", err)
		}
	}

	binary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve this binary's path: %w", err)
	}
	storeCA := ""
	if *secretStore {
		storeCA = storeOptions.TLSCertPath
	}
	serveArguments := localServeArguments(layout, *httpsPort, *sshPort, *publishOnGreen, storeCA, releaseSecretPaths)

	// The server has to be running before an uplink can be minted: the admin
	// path talks to the live process's audit gate, which is exactly the point
	// of that gate.
	stop, err := startLocalServer(ctx, output, binary, serveArguments, layout, *launchd)
	if err != nil {
		return err
	}
	if stop != nil {
		defer stop()
	}
	baseURL := fmt.Sprintf("https://localhost:%d", *httpsPort)
	if err := waitForLocalServer(ctx, fmt.Sprintf("127.0.0.1:%d", *httpsPort)); err != nil {
		return err
	}
	say(output, "server     listening on %s and ssh://127.0.0.1:%d", baseURL, *sshPort)

	token, minted, err := ensureAdminUplink(ctx, output, layout)
	if err != nil {
		return err
	}
	if err := writeClientAccess(ctx, output, baseURL, layout, token, minted, *shellProfile); err != nil {
		return err
	}
	_, err = io.WriteString(output, localinstall.PushBanner(baseURL, "127.0.0.1", *sshPort, layout.ClientKey, nil))
	if err != nil {
		return err
	}
	if *launchd {
		say(output, "The server runs under launchd as %s. Its log is %s.",
			localinstall.LaunchdLabel, installer.DisplayPath(layout.Logs))
		return nil
	}
	say(output, "Running in the foreground. Press Ctrl-C to stop, or re-run with --launchd to keep it running.")
	<-ctx.Done()
	return nil
}

func say(output io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(output, format+"\n", args...)
}

func resolveInstallRoot(root string) (string, error) {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve the home directory: %w", err)
		}
		trimmed = filepath.Join(home, ".oberth", "local")
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("resolve the install root: %w", err)
	}
	return absolute, nil
}

func requireDockerDaemon(ctx context.Context) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return errors.New("install --engine=docker needs the docker CLI on PATH; install Docker Desktop and start it")
	}
	probe := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}")
	if err := probe.Run(); err != nil {
		return errors.New("the Docker daemon is not answering; start Docker Desktop and run this again")
	}
	return nil
}

// localServeArguments is the serve command line this install produces. It is
// built here rather than in a template so the launchd agent and the foreground
// run cannot drift.
func localServeArguments(layout localinstall.Layout, httpsPort, sshPort int, publishOnGreen bool,
	storeCACert string, releaseSecretPaths []string) []string {
	arguments := []string{
		"serve", "--engine=docker",
		"--data=" + layout.Data,
		"--database=" + layout.Database,
		fmt.Sprintf("--https-listen=127.0.0.1:%d", httpsPort),
		fmt.Sprintf("--ssh-listen=127.0.0.1:%d", sshPort),
		"--tls-cert=" + layout.TLSCert,
		"--tls-key=" + layout.TLSKey,
		"--ssh-host-key=" + layout.SSHHostKey,
		"--upstream-key=" + layout.Upstream,
		"--known-hosts=" + layout.KnownHosts,
		fmt.Sprintf("--publish-on-green=%t", publishOnGreen),
	}
	if storeCACert != "" {
		arguments = append(arguments,
			"--secretstore-address="+localbao.DefaultAddress,
			"--secretstore-ca-cert="+storeCACert,
			"--secretstore-jwt-signing-key="+layout.SigningKey)
		for _, path := range releaseSecretPaths {
			arguments = append(arguments, "--secretstore-path="+path)
		}
	}
	return arguments
}

// startLocalServer runs the server, either as a launchd agent or as a child of
// this process. It returns a stop function for the foreground case, and nil
// when the agent owns the lifetime.
func startLocalServer(ctx context.Context, output io.Writer, binary string, arguments []string,
	layout localinstall.Layout, launchd bool) (func(), error) {
	if launchd {
		return nil, installLaunchdAgent(ctx, output, binary, arguments, layout)
	}
	command := exec.CommandContext(ctx, binary, arguments...)
	logFile, err := os.OpenFile(layout.Logs, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open the server log: %w", err)
	}
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("start the server: %w", err)
	}
	say(output, "server     started, logging to %s", installer.DisplayPath(layout.Logs))
	return func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
		_ = logFile.Close()
	}, nil
}

func installLaunchdAgent(ctx context.Context, output io.Writer, binary string, arguments []string,
	layout localinstall.Layout) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve the home directory: %w", err)
	}
	agents := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agents, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", agents, err)
	}
	plist, err := localinstall.RenderLaunchdAgent(binary, arguments, layout)
	if err != nil {
		return err
	}
	path := filepath.Join(agents, localinstall.LaunchdLabel+".plist")
	if err := installer.AtomicWriteFile(path, plist, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// Unload first so a re-run replaces the running agent rather than failing
	// on an already-loaded label. A missing agent makes this a no-op, which is
	// why its failure is ignored and the load's is not.
	_ = exec.CommandContext(ctx, "launchctl", "unload", path).Run()
	if err := exec.CommandContext(ctx, "launchctl", "load", path).Run(); err != nil {
		return fmt.Errorf("load the launchd agent at %s: %w", path, err)
	}
	say(output, "server     launchd agent %s installed at %s", localinstall.LaunchdLabel, installer.DisplayPath(path))
	return nil
}

// waitForLocalServer waits for the HTTPS listener to accept a connection. The
// listener is the readiness signal because it is the last thing serve() opens.
func waitForLocalServer(ctx context.Context, address string) error {
	deadline := time.Now().Add(60 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", address, 2*time.Second)
		if err == nil {
			_ = connection.Close()
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("the server did not start listening on %s within a minute: %w", address, lastErr)
}

// ensureAdminUplink mints the admin uplink on a first install and recognises
// an existing one on a re-run. It reports the token and whether it is fresh,
// because a token this process did not mint is one it cannot store.
func ensureAdminUplink(ctx context.Context, output io.Writer, layout localinstall.Layout) (string, bool, error) {
	var existing bytes.Buffer
	if err := runUplink(ctx, []string{"list", "--database=" + layout.Database}, nil, &existing); err == nil {
		if strings.Contains(existing.String(), localUplinkIdentity) {
			say(output, "uplink     %s already registered, keeping its existing token", localUplinkIdentity)
			return "", false, nil
		}
	}
	var minted bytes.Buffer
	err := runUplink(ctx, []string{"add", "--admin",
		"--database=" + layout.Database, "--tls-cert=" + layout.TLSCert,
		layout.ClientKey + ".pub", localUplinkIdentity}, nil, &minted)
	if err != nil {
		return "", false, fmt.Errorf("register the admin uplink: %w", err)
	}
	token := lastNonEmptyLine(minted.String())
	if token == "" {
		return "", false, errors.New("the uplink registration produced no token")
	}
	say(output, "uplink     %s registered with admin rights", localUplinkIdentity)
	return token, true, nil
}

// localUplinkIdentity is the identity a local install registers. One machine,
// one developer, one uplink.
const localUplinkIdentity = "local@localhost"

func lastNonEmptyLine(text string) string {
	scanner := bufio.NewScanner(strings.NewReader(text))
	last := ""
	for scanner.Scan() {
		if trimmed := strings.TrimSpace(scanner.Text()); trimmed != "" {
			last = trimmed
		}
	}
	return last
}

// writeClientAccess writes the same files the cluster install writes, in the
// same place, and stores the token in the same platform secret store. A
// machine that has had either install answers OBERTH_TOKEN_COMMAND.
func writeClientAccess(ctx context.Context, output io.Writer, baseURL string,
	layout localinstall.Layout, token string, minted bool, shellProfile string) error {
	root, err := installer.ClientConfigRoot()
	if err != nil {
		return fmt.Errorf("resolve the client configuration directory: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", root, err)
	}
	// The trust anchor is this deployment's own certificate. OBERTH_CA_CERT
	// replaces the system pool rather than adding to it, so the file is
	// required even though it is self-signed and local.
	authority, err := os.ReadFile(layout.TLSCert) // #nosec G304 -- the certificate this install just wrote.
	if err != nil {
		return fmt.Errorf("read the server certificate: %w", err)
	}
	caPath := filepath.Join(root, "ca.crt")
	if err := installer.AtomicWriteFile(caPath, authority, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", caPath, err)
	}
	tokenCommand, tokenHint := installer.TokenCommandForHost()
	if minted && strings.TrimSpace(token) != "" {
		if err := installer.StoreUplinkToken(ctx, token); err != nil {
			// Deliberately not the error: on macOS the secret store takes the
			// token as an argument, so a failure from it quotes the command.
			say(output, "token      could not be saved to your secret store; store it yourself with:\n    %s", tokenHint)
		} else {
			say(output, "token      saved to your secret store")
		}
	}
	envPath := filepath.Join(root, "env")
	if err := installer.AtomicWriteFile(envPath,
		[]byte(installer.RenderClientEnv(baseURL, caPath, tokenCommand)), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", envPath, err)
	}
	say(output, "client     %s", installer.DisplayPath(envPath))
	mcpBody, err := installer.RenderMCPConfig(baseURL, tokenCommand)
	if err == nil {
		mcpPath := filepath.Join(root, "mcp.json")
		if writeErr := installer.AtomicWriteFile(mcpPath, mcpBody, 0o600); writeErr == nil {
			say(output, "client     %s", installer.DisplayPath(mcpPath))
		}
	}
	line := localinstall.ShellProfileLine(envPath)
	if strings.TrimSpace(shellProfile) == "" {
		say(output, "\nAdd this to your shell profile:\n\n    %s\n", line)
		return nil
	}
	return appendShellProfileLine(output, shellProfile, line)
}

// appendShellProfileLine adds the line once. Appending it on every install is
// how a profile ends up sourcing the same file five times.
func appendShellProfileLine(output io.Writer, profile, line string) error {
	expanded, err := expandHome(profile)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(expanded) // #nosec G304 -- an operator-supplied path.
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", expanded, err)
	}
	if strings.Contains(string(body), line) {
		say(output, "profile    %s already sources the client environment", installer.DisplayPath(expanded))
		return nil
	}
	file, err := os.OpenFile(expanded, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- an operator-supplied path.
	if err != nil {
		return fmt.Errorf("open %s: %w", expanded, err)
	}
	defer func() { _ = file.Close() }()
	if _, err := fmt.Fprintf(file, "\n# Added by oberth install --engine=docker\n%s\n", line); err != nil {
		return fmt.Errorf("write %s: %w", expanded, err)
	}
	say(output, "profile    %s now sources the client environment", installer.DisplayPath(expanded))
	return nil
}
