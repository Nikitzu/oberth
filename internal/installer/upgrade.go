package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/mod/semver"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// canonicalGARPrefix is the canonical Google Artifact Registry prefix for
// CloudTaser images. Published chart refs must resolve to this registry.
const canonicalGARPrefix = "europe-west4-docker.pkg.dev/skipopsmain/cloudtaser/"

// imageRefIsPublished reports whether an image ref came from a release rather
// than from the operator's own build.
//
// Published means the ref lives in the same repository as the image this
// binary was built to deploy, or under the canonical GAR prefix upstream
// releases to. A prefix test alone was wrong for a fork: a fork publishes
// under its own registry, so its previous release looked like a hand-deployed
// image and every upgrade was silently kept at the old digest.
func imageRefIsPublished(ref, binaryDefaultRef string) bool {
	repo := imageRepository(strings.TrimSpace(ref))
	if repo == "" {
		return false
	}
	if strings.HasPrefix(repo, canonicalGARPrefix) {
		return true
	}
	binaryRepo := imageRepository(strings.TrimSpace(binaryDefaultRef))
	return binaryRepo != "" && repo == binaryRepo
}

// UpgradeConfig holds options for the upgrade command.
type UpgradeConfig struct {
	Namespace     string
	DryRun        bool
	Yes           bool   // --yes: proceed without confirmation for non-local targets
	ChartOverride string // --chart: override the chart reference
	BinaryVersion string
	// DefaultImageRef is the server image this binary was built to deploy.
	// A chart published by this build refers to that repository, which need
	// not be the canonical upstream registry when the build is a fork's.
	DefaultImageRef string
	Timeout         time.Duration
}

// UpgradeResult describes the outcome of an upgrade.
type UpgradeResult struct {
	PreviousVersion string
	TargetVersion   string
	AlreadyUpToDate bool
	Upgraded        bool
}

// ValidateUpgrade checks UpgradeConfig for invalid flag combinations and
// applies defaults.
func (cfg *UpgradeConfig) ValidateUpgrade() error {
	if cfg.Namespace == "" {
		cfg.Namespace = DefaultNamespace
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.BinaryVersion == "" || cfg.BinaryVersion == "dev" {
		return errors.New("cannot upgrade: this binary has no release version (dev build); use --chart with an explicit chart reference")
	}
	return nil
}

// RunUpgrade executes the upgrade flow: detect current version, compare with
// the binary's version, upgrade the Helm release, wait for rollout, and
// verify the new version.
func RunUpgrade(ctx context.Context, cfg UpgradeConfig, deps Deps) (UpgradeResult, error) {
	if err := cfg.ValidateUpgrade(); err != nil {
		return UpgradeResult{}, err
	}

	w := deps.Output
	if w == nil {
		w = io.Discard
	}
	color := isColor(deps)

	targetVersion := cfg.BinaryVersion
	_, _ = fmt.Fprintf(w, "oberth upgrade %s\n\n", displayVersion(targetVersion))

	// Print the target cluster context and server before any mutation.
	server := ""
	if deps.RestConfig != nil {
		server = deps.RestConfig.Host
	}
	_, _ = fmt.Fprintf(w, "Target: %s (%s)\n", deps.ContextName, server)

	// Non-local safety guard: match install's pattern (installer.go:468-475).
	// An upgrade against a remote cluster without explicit acknowledgement
	// risks applying to the wrong environment.
	if !IsLocalServer(server) && !cfg.Yes {
		return UpgradeResult{}, fmt.Errorf("target %q does not appear to be a local cluster (server: %s); "+
			"use --yes to proceed or switch to a local context", deps.ContextName, server)
	}
	if !IsLocalServer(server) && cfg.Yes {
		_, _ = fmt.Fprintf(w, "WARNING: %q does not appear to be a local cluster; proceeding because --yes was set\n", deps.ContextName)
	}

	// Detect current version from the Helm release.
	release, exists := findHelmRelease(ctx, deps, "oberth", cfg.Namespace)
	if !exists {
		return UpgradeResult{}, fmt.Errorf("no Oberth Helm release found in namespace %s; run oberth install first", cfg.Namespace)
	}
	currentVersion := chartVersionFromRelease(release.Chart, "oberth")

	result := UpgradeResult{
		PreviousVersion: currentVersion,
		TargetVersion:   targetVersion,
	}

	phase(w, "Current version", displayVersion(currentVersion), color)

	// Compare versions. Refuse to proceed when the installed version cannot
	// be parsed (unknown state) or is strictly newer than the CLI (silent
	// downgrade).
	currentCanonical := canonicalChartVersion(currentVersion)
	targetCanonical := canonicalChartVersion(targetVersion)
	if targetCanonical == "" {
		return result, fmt.Errorf("target version %q is not a valid semantic version", targetVersion)
	}
	if currentCanonical == "" {
		if currentVersion == "" {
			return result, errors.New("no version could be determined from the installed Helm release; " +
				"refusing to upgrade an unknown release — use helm upgrade directly or reinstall with oberth install")
		}
		return result, fmt.Errorf("installed version %q could not be parsed; refusing to upgrade an unknown release — "+
			"use helm upgrade directly or reinstall with oberth install", currentVersion)
	}
	cmp := semver.Compare(currentCanonical, targetCanonical)
	if cmp == 0 {
		result.AlreadyUpToDate = true
		_, _ = fmt.Fprintf(w, "\nAlready running %s\n", displayVersion(targetVersion))
		return result, nil
	}
	if cmp > 0 {
		return result, fmt.Errorf("installed version %s is newer than CLI version %s — upgrade the CLI first",
			displayVersion(currentVersion), displayVersion(targetVersion))
	}

	if cfg.DryRun {
		return printUpgradeDryRun(w, cfg, result)
	}

	// Resolve the chart reference. Use the existing Helm repo if no override.
	chart := cfg.ChartOverride
	if chart == "" {
		if _, err := deps.RunHelm(ctx, []string{"repo", "add", "--force-update", oberthRepoName, OberthHelmRepoURL}); err != nil {
			return result, fmt.Errorf("add Oberth helm repo: %w", err)
		}
		chart = oberthRepoName + "/oberth"
	}

	// Resolve the target chart's image ref so the upgrade pins the exact image.
	imageRef, err := resolveChartImageRef(ctx, deps, chart, targetVersion, cfg.ChartOverride != "", cfg.DefaultImageRef)
	if err != nil {
		return result, fmt.Errorf("resolve chart image: %w", err)
	}

	// Build and execute the helm upgrade.
	phase(w, "Chart", "updating", color)
	args := upgradeHelmArgs(cfg, chart, imageRef)
	if _, err := deps.RunHelm(ctx, args); err != nil {
		printRecoveryGuidance(w, cfg.Namespace)
		return result, fmt.Errorf("helm upgrade: %w", err)
	}
	phase(w, "Oberth", "upgrading", color)

	// Wait for rollout to complete by watching the deployment status.
	if err := waitForUpgradeRollout(ctx, deps, cfg, imageRef); err != nil {
		printRecoveryGuidance(w, cfg.Namespace)
		return result, fmt.Errorf("rollout: %w", err)
	}
	phase(w, "Rollout", "✓ ready", color)

	// Verify the running version matches the target.
	runningVersion, err := verifyRunningVersion(ctx, deps, cfg, targetVersion)
	if err != nil {
		printRecoveryGuidance(w, cfg.Namespace)
		return result, fmt.Errorf("version verification failed after upgrade: %w", err)
	}
	phase(w, "Oberth", "✓ "+displayVersion(runningVersion), color)

	result.Upgraded = true
	_, _ = fmt.Fprintf(w, "\nReady — %s\n", oberthWebUIURL)
	return result, nil
}

// resolveChartImageRef reads the chart's default image.ref value so the
// upgrade pins the exact image the chart was built against, rather than
// relying on an implicit default that --reuse-values might override with a
// stale value.
func resolveChartImageRef(ctx context.Context, deps Deps, chart, targetVersion string, isLocalChart bool, defaultImageRef string) (string, error) {
	args := []string{"show", "values", chart}
	if !isLocalChart && targetVersion != "" {
		args = append(args, "--version", targetVersion)
	}
	valuesYAML, err := deps.RunHelm(ctx, args)
	if err != nil {
		return "", fmt.Errorf("read chart values: %w", err)
	}
	var values oberthChartValues
	if err := yaml.Unmarshal(valuesYAML, &values); err != nil {
		return "", fmt.Errorf("parse chart values: %w", err)
	}
	ref := strings.TrimSpace(values.Image.Ref)
	if ref == "" {
		return "", errors.New("chart does not define image.ref")
	}

	// Validate the resolved ref is digest-pinned: <registry>/<repo>@sha256:<64hex>.
	// A tag-only ref could be silently replaced by --reuse-values or a
	// registry push, so the upgrade must pin an immutable digest.
	repo, digest, isDigest := splitImageDigestRef(ref)
	if !isDigest || !isSHA256Digest(digest) {
		return "", fmt.Errorf("chart image.ref %q is not in digest-pinned form (<registry>/<repo>@sha256:<64hex>)", ref)
	}

	// Validate the registry for published charts: the chart must point either
	// at this binary's own image repository, which is what a fork's release
	// publishes to, or at the canonical GAR prefix upstream releases to. The
	// --chart dev-loop override allows any registry (local builds, mirrors)
	// but still requires digest form above.
	if !isLocalChart && !imageRefIsPublished(repo, defaultImageRef) {
		return "", fmt.Errorf("chart image.ref %q is in neither this binary's image repository %q "+
			"nor under the canonical GAR prefix %s", ref, imageRepository(strings.TrimSpace(defaultImageRef)), canonicalGARPrefix)
	}

	return ref, nil
}

// upgradeHelmArgs returns the helm arguments for upgrading Oberth.
//
// DELIBERATE: --atomic is NOT used. Oberth's database migrations are
// forward-only; an automatic rollback to a pre-migration schema would leave
// the database in an inconsistent state (crashloop trap). On failure the
// operator must fix forward with a new 'oberth upgrade'.
func upgradeHelmArgs(cfg UpgradeConfig, chart, imageRef string) []string {
	args := []string{
		"upgrade", "oberth", chart,
		"-n", cfg.Namespace,
		"--reuse-values",
		"--set-string", "image.ref=" + imageRef,
	}
	// Only pin --version for published charts; a local path IS the version.
	if cfg.ChartOverride == "" && cfg.BinaryVersion != "" {
		args = append(args, "--version", cfg.BinaryVersion)
	}
	// Pass the user's timeout to helm so it respects the same deadline.
	if cfg.Timeout > 0 {
		args = append(args, "--timeout", cfg.Timeout.String())
	}
	args = append(args, "--wait")
	return args
}

// waitForUpgradeRollout watches the deployment until all replicas are updated
// and available with the target image, or the timeout expires.
func waitForUpgradeRollout(ctx context.Context, deps Deps, cfg UpgradeConfig, targetImageRef string) error {
	deadline, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	for {
		deployment, err := getOberthDeployment(deadline, deps, cfg.Namespace)
		if err != nil {
			return err
		}
		if deploymentRolledOut(deployment, targetImageRef) {
			return nil
		}
		select {
		case <-deadline.Done():
			return fmt.Errorf("timeout (%s) waiting for Oberth deployment rollout in namespace %s", cfg.Timeout, cfg.Namespace)
		case <-time.After(2 * time.Second):
		}
	}
}

// deploymentRolledOut reports whether the deployment has finished rolling out
// the target image: generation observed, all replicas updated and available,
// none unavailable, and the pod template specifies the expected image.
func deploymentRolledOut(deployment *appsv1.Deployment, targetImageRef string) bool {
	if deployment.Spec.Replicas == nil {
		return false
	}
	if deployment.Status.ObservedGeneration < deployment.Generation {
		return false
	}
	if deployment.Status.UpdatedReplicas != *deployment.Spec.Replicas {
		return false
	}
	if deployment.Status.AvailableReplicas != *deployment.Spec.Replicas {
		return false
	}
	if deployment.Status.UnavailableReplicas != 0 {
		return false
	}
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == "oberth" && container.Image == targetImageRef {
			return true
		}
	}
	return false
}

// getOberthDeployment fetches the Oberth deployment by label selector.
func getOberthDeployment(ctx context.Context, deps Deps, namespace string) (*appsv1.Deployment, error) {
	if deps.KubeClient == nil {
		return nil, errors.New("no Kubernetes client available")
	}
	deployments, err := deps.KubeClient.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=oberth",
	})
	if err != nil {
		return nil, fmt.Errorf("list Oberth deployments: %w", err)
	}
	if len(deployments.Items) == 0 {
		return nil, fmt.Errorf("no Oberth deployment found in namespace %s", namespace)
	}
	return &deployments.Items[0], nil
}

// printRecoveryGuidance writes a short recovery block to w, covering release
// state, pod logs, and the forward-only rollback warning. Called on all three
// post-mutation failure paths (helm upgrade, rollout wait, version verify).
func printRecoveryGuidance(w io.Writer, namespace string) {
	_, _ = fmt.Fprintf(w, "\nRecovery:\n")
	_, _ = fmt.Fprintf(w, "  Release state:  helm status oberth -n %s\n", namespace)
	_, _ = fmt.Fprintf(w, "  Pod logs:       kubectl logs -n %s deploy/oberth\n", namespace)
	_, _ = fmt.Fprintf(w, "\nDO NOT run 'helm rollback' — database migrations are forward-only.\nFix the issue and run 'oberth upgrade' again.\n")
}

// verifyRunningVersion execs into the Oberth pod, runs `oberth version`,
// parses the reported version, and compares it against the expected target.
// Returns the running version on match, or an error on mismatch or parse
// failure.
//
// The exec subresource can lag right after rollout, so transient exec errors
// are retried up to verifyMaxAttempts times with pollInterval delays (the same
// pattern as upstreamConfigured in onboard.go). Version-mismatch and
// unparseable-output results are definitive and are NOT retried.
//
// verifyMaxAttempts is the number of exec attempts before giving up.
const verifyMaxAttempts = 3

func verifyRunningVersion(ctx context.Context, deps Deps, cfg UpgradeConfig, targetVersion string) (string, error) {
	run := deps.RunCommand
	if run == nil {
		run = DefaultRunCommand
	}
	args := []string{"exec", "-c", "oberth", "-n", cfg.Namespace, "deploy/oberth", "--", "oberth", "version"}
	if deps.ContextName != "" {
		args = append(args[:1], append([]string{"--context", deps.ContextName}, args[1:]...)...)
	}

	var lastErr error
	for attempt := 0; attempt < verifyMaxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(pollInterval(deps)):
			}
		}
		out, err := run(ctx, nil, "kubectl", args...)
		if err != nil {
			lastErr = fmt.Errorf("kubectl exec oberth version: %w", err)
			continue // transient exec error — retry
		}
		// Parse "oberth v0.12.35 commit=... date=..."
		// Filter out kubectl stderr noise (e.g. "Defaulted container...")
		var runningVersion string
		for _, field := range strings.Fields(strings.TrimSpace(string(out))) {
			if strings.HasPrefix(field, "v") && semver.IsValid(field) {
				runningVersion = field
				break
			}
		}
		if runningVersion == "" {
			// Unparseable output is a definitive failure — do not retry.
			return "", fmt.Errorf("could not parse a semantic version from pod output: %s", strings.TrimSpace(string(out)))
		}
		targetCanonical := canonicalChartVersion(targetVersion)
		if targetCanonical == "" {
			return "", fmt.Errorf("target version %q is not a valid semantic version", targetVersion)
		}
		if semver.Compare(runningVersion, targetCanonical) != 0 {
			// Version mismatch is a definitive failure — do not retry.
			return "", fmt.Errorf("expected %s but pod reports %s", displayVersion(targetVersion), runningVersion)
		}
		return runningVersion, nil
	}
	return "", fmt.Errorf("upgrade applied but unverified — verify manually: "+
		"kubectl exec -n %s deploy/oberth -- oberth version (%w)", cfg.Namespace, lastErr)
}

func printUpgradeDryRun(w io.Writer, cfg UpgradeConfig, result UpgradeResult) (UpgradeResult, error) {
	_, _ = fmt.Fprintf(w, "\nDry-run plan (no cluster changes will be made):\n\n")
	step := 1

	chart := cfg.ChartOverride
	if chart == "" {
		chart = oberthRepoName + "/oberth"
		_, _ = fmt.Fprintf(w, "  %d. Add/update Helm repo\n", step)
		_, _ = fmt.Fprintf(w, "     helm repo add --force-update %s %s\n\n", oberthRepoName, OberthHelmRepoURL)
		step++
	}

	_, _ = fmt.Fprintf(w, "  %d. Upgrade Oberth from %s to %s\n", step, displayVersion(result.PreviousVersion), displayVersion(result.TargetVersion))
	_, _ = fmt.Fprintf(w, "     helm upgrade oberth %s -n %s --reuse-values --set-string image.ref=<chart-default> --version %s --wait\n\n",
		chart, cfg.Namespace, result.TargetVersion)
	step++

	_, _ = fmt.Fprintf(w, "  %d. Wait for deployment rollout completion\n\n", step)
	step++

	_, _ = fmt.Fprintf(w, "  %d. Verify running version matches %s\n\n", step, displayVersion(result.TargetVersion))

	_, _ = fmt.Fprintln(w, "No cluster changes were made (--dry-run).")
	return result, nil
}
