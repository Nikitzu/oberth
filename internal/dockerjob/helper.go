package dockerjob

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// The helper a credentialed step is started with.
//
// A credentialed step's entrypoint is `/run/oberth/bin/oberth secretstore
// exec`, which logs in to the store, materialises the declared secrets and
// execs the real command. On a cluster the server binary is Linux and rides
// the run's claim as a subPath. Here the server may be a macOS binary, so the
// helper is taken from the release image built alongside this binary, which
// is Linux for the daemon's architecture by construction, and copied once
// into a named volume that every credentialed step mounts read-only at the
// same path. A development build has no image and builds the helper from
// source instead, when a Go toolchain and the source are at hand.

const (
	// HelperMountPath is where credentialed steps find the helper. It is the
	// Argo engine's OberthBinMountPath, and it has to be, because the
	// pipeline document names it.
	HelperMountPath = "/run/oberth/bin"
	// helperImagePath is where the release image keeps the binary.
	helperImagePath = "/usr/local/bin/oberth"
	// helperVolumePrefix names the cache volumes; one per server version.
	helperVolumePrefix = "oberth-helper-"
)

// HelperConfig says where the helper comes from.
type HelperConfig struct {
	// ImageRef is the release image stamped into this binary, empty on a
	// development build.
	ImageRef string
	// Version is this server's version, which names the cache volume.
	Version string
	// SourceDir is the module root a development build compiles the helper
	// from. Empty means no fallback.
	SourceDir string
}

// HelperVolumeName is the cache volume for one server version. Docker volume
// names take [a-zA-Z0-9][a-zA-Z0-9_.-]*, and a version like v0.13.31-poc22
// already fits; anything else is folded to that alphabet.
func HelperVolumeName(version string) string {
	trimmed := strings.TrimSpace(version)
	if trimmed == "" {
		trimmed = "dev"
	}
	return helperVolumePrefix + helperVolumeCharacters.ReplaceAllString(trimmed, "-")
}

var helperVolumeCharacters = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

// helperSource is the decision: image, source build, or a refusal that says
// what is missing. goOnPath is looked up by the caller so the decision can be
// asserted without a toolchain.
type helperSource int

const (
	helperFromImage helperSource = iota
	helperFromSource
)

func decideHelperSource(config HelperConfig, goOnPath bool) (helperSource, error) {
	if strings.TrimSpace(config.ImageRef) != "" {
		return helperFromImage, nil
	}
	if strings.TrimSpace(config.SourceDir) == "" {
		return 0, errors.New("dockerjob: this is a development build with no release image, and no --helper-source names the oberth source to build the credentialed-step helper from")
	}
	if !goOnPath {
		return 0, errors.New("dockerjob: this is a development build with no release image, and no Go toolchain is on PATH to build the credentialed-step helper from " + config.SourceDir)
	}
	return helperFromSource, nil
}

// helperCached reports whether a release version's volume already holds the
// helper. A development volume is rebuilt once per server process, because
// the binary it mirrors changes with every build.
func (controller *Controller) helperCached(ctx context.Context, version string) bool {
	if strings.TrimSpace(version) == "" || version == "dev" {
		return false
	}
	_, err := controller.client.run(ctx, "volume", "inspect", HelperVolumeName(version))
	return err == nil
}

// ensureHelper makes the helper volume exist and hold the binary, once per
// process, and records its name for createArguments. seedImage is an image
// the daemon already has, used only to create the never-started container
// the volume is written through.
func (controller *Controller) ensureHelper(ctx context.Context, seedImage string) error {
	controller.helperMu.Lock()
	defer controller.helperMu.Unlock()
	if controller.helperVolume != "" {
		return nil
	}
	config := controller.config.Helper
	volume := HelperVolumeName(config.Version)
	if controller.helperCached(ctx, config.Version) {
		controller.helperVolume = volume
		return nil
	}
	_, goErr := exec.LookPath("go")
	source, err := decideHelperSource(config, goErr == nil)
	if err != nil {
		return err
	}
	platform, err := controller.daemonPlatform(ctx)
	if err != nil {
		return err
	}
	scratch, err := os.MkdirTemp("", "oberth-helper-")
	if err != nil {
		return fmt.Errorf("dockerjob: scratch directory for the helper: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	binary := filepath.Join(scratch, "oberth")

	switch source {
	case helperFromImage:
		if err := controller.copyHelperFromImage(ctx, config.ImageRef, platform, binary); err != nil {
			return err
		}
		seedImage = config.ImageRef
	case helperFromSource:
		if err := buildHelperFromSource(ctx, config.SourceDir, platform, binary); err != nil {
			return err
		}
	}
	body, err := os.ReadFile(binary) // #nosec G304 -- the file this function just wrote.
	if err != nil {
		return fmt.Errorf("dockerjob: read the built helper: %w", err)
	}
	if err := controller.writeHelperVolume(ctx, volume, seedImage, body); err != nil {
		return err
	}
	controller.helperVolume = volume
	return nil
}

// daemonPlatform is what the image is pulled for and the helper built for:
// the daemon's, not the server's, because on a Mac they differ.
func (controller *Controller) daemonPlatform(ctx context.Context) (string, error) {
	out, err := controller.client.run(ctx, "version", "--format", "{{.Server.Os}}/{{.Server.Arch}}")
	if err != nil {
		return "", fmt.Errorf("dockerjob: read the daemon's platform: %w", err)
	}
	platform := strings.TrimSpace(out)
	if !strings.HasPrefix(platform, "linux/") {
		return "", fmt.Errorf("dockerjob: the daemon runs %q containers and the helper is a Linux binary", platform)
	}
	return platform, nil
}

// copyHelperFromImage pulls the release image for the daemon's platform,
// creates a container from it that is never started, and copies the binary
// out. A created container is enough for docker cp and runs nothing.
func (controller *Controller) copyHelperFromImage(ctx context.Context, ref, platform, destination string) error {
	if _, err := controller.client.run(ctx, "pull", "--platform", platform, ref); err != nil {
		return fmt.Errorf("dockerjob: pull the helper image: %w", err)
	}
	container, err := controller.client.run(ctx, "create", "--platform", platform, "--", ref)
	if err != nil {
		return fmt.Errorf("dockerjob: stage the helper image: %w", err)
	}
	defer func() { _, _ = controller.client.run(ctx, "rm", "--force", container) }()
	if _, err := controller.client.run(ctx, "cp", container+":"+helperImagePath, destination); err != nil {
		return fmt.Errorf("dockerjob: copy the helper out of the image: %w", err)
	}
	return nil
}

// buildHelperFromSource is the development fallback: the same package, cross
// compiled for the daemon.
func buildHelperFromSource(ctx context.Context, sourceDir, platform, destination string) error {
	goos, goarch, _ := strings.Cut(platform, "/")
	command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", destination, "./cmd/oberth") // #nosec G204 -- fixed verbs, the source directory is a serve flag.
	command.Dir = sourceDir
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
	if out, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("dockerjob: build the helper from %s for %s: %w: %s", sourceDir, platform, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// writeHelperVolume creates the volume and writes the binary into it through
// a never-started container, root-owned and executable, the way seedTree
// writes everything else.
func (controller *Controller) writeHelperVolume(ctx context.Context, volume, image string, body []byte) error {
	if _, err := controller.client.run(ctx, "volume", "create", volume); err != nil {
		return fmt.Errorf("dockerjob: create the helper volume: %w", err)
	}
	name := volume + "-seed"
	_, _ = controller.client.run(ctx, "rm", "--force", name)
	container, err := controller.client.run(ctx, "create", "--name", name,
		"--volume", volume+":"+HelperMountPath, "--", image, "true")
	if err != nil {
		return fmt.Errorf("dockerjob: stage the helper volume: %w", err)
	}
	defer func() { _, _ = controller.client.run(ctx, "rm", "--force", container) }()
	err = seedTree(ctx, controller.client.binary, container, HelperMountPath, func(writer *tar.Writer) error {
		header := &tar.Header{Typeflag: tar.TypeReg, Name: "oberth", Mode: 0o755, Size: int64(len(body))}
		rootOwned(header)
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		_, err := writer.Write(body)
		return err
	})
	if err != nil {
		return fmt.Errorf("dockerjob: write the helper into %s: %w", volume, err)
	}
	return nil
}
