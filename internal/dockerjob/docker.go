package dockerjob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// docker is a thin wrapper over the Docker CLI.
//
// The CLI rather than the Go SDK, deliberately. The SDK would add a large
// dependency tree to a module that currently has no Docker client at all, and
// this engine uses perhaps ten verbs. For a spike whose question is whether
// the execution seam holds, the CLI answers it with no supply-chain cost. A
// production version would want the SDK for stream handling and for not
// shelling out per step.
type docker struct {
	binary string
}

func newDocker(binary string) *docker {
	if strings.TrimSpace(binary) == "" {
		binary = "docker"
	}
	return &docker{binary: binary}
}

// available reports whether the daemon answers, so a misconfigured host fails
// at startup with a clear message rather than at the first push.
func (client *docker) available(ctx context.Context) error {
	if _, err := exec.LookPath(client.binary); err != nil {
		return fmt.Errorf("dockerjob: %q is not on PATH: %w", client.binary, err)
	}
	if _, err := client.run(ctx, "version", "--format", "{{.Server.Version}}"); err != nil {
		return fmt.Errorf("dockerjob: the Docker daemon is not reachable: %w", err)
	}
	return nil
}

// run executes a docker command and returns trimmed stdout.
func (client *docker) run(ctx context.Context, arguments ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(ctx, client.binary, arguments...)
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return strings.TrimSpace(stdout.String()), fmt.Errorf("docker %s: %w: %s",
			strings.Join(arguments, " "), err, detail)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// stream executes a docker command and copies its combined output into sink.
// Step logs come through here: Docker interleaves a container's stdout and
// stderr in the order they were written, which is the order a reader expects.
func (client *docker) stream(ctx context.Context, sink io.Writer, arguments ...string) error {
	command := exec.CommandContext(ctx, client.binary, arguments...)
	command.Stdout, command.Stderr = sink, sink
	return command.Run()
}

// pipe executes a docker command and hands its stdout to consume as a stream,
// which is how an artifact tar leaves a container without being buffered.
func (client *docker) pipe(ctx context.Context, consume func(io.Reader) error, arguments ...string) error {
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, client.binary, arguments...)
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return err
	}
	consumeErr := consume(stdout)
	// Drain whatever the consumer left, so the child never blocks on a full
	// pipe while Wait is waiting for it to exit.
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := command.Wait()
	if waitErr != nil {
		waitErr = fmt.Errorf("docker %s: %w: %s", strings.Join(arguments, " "), waitErr, strings.TrimSpace(stderr.String()))
	}
	return errors.Join(consumeErr, waitErr)
}
