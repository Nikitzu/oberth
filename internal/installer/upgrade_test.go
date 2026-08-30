package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestValidateUpgradeRejectsDevBuild(t *testing.T) {
	t.Parallel()
	cfg := UpgradeConfig{BinaryVersion: "dev"}
	if err := cfg.ValidateUpgrade(); err == nil {
		t.Fatal("expected error for dev build")
	}
	cfg = UpgradeConfig{BinaryVersion: ""}
	if err := cfg.ValidateUpgrade(); err == nil {
		t.Fatal("expected error for empty version")
	}
}

func TestValidateUpgradeAcceptsReleaseVersion(t *testing.T) {
	t.Parallel()
	cfg := UpgradeConfig{BinaryVersion: "0.12.35"}
	if err := cfg.ValidateUpgrade(); err != nil {
		t.Fatal(err)
	}
	if cfg.Namespace != DefaultNamespace {
		t.Fatalf("namespace = %q, want %q", cfg.Namespace, DefaultNamespace)
	}
	if cfg.Timeout != DefaultTimeout {
		t.Fatalf("timeout = %v, want %v", cfg.Timeout, DefaultTimeout)
	}
}

func TestRunUpgradeAlreadyUpToDate(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	helmCalls := 0
	deps := Deps{
		Output: &output,
		RunHelm: func(_ context.Context, args []string) ([]byte, error) {
			helmCalls++
			if len(args) >= 2 && args[0] == "list" {
				releases := []helmRelease{{
					Name:      "oberth",
					Namespace: "oberth",
					Status:    "deployed",
					Chart:     "oberth-0.12.35",
				}}
				data, _ := json.Marshal(releases)
				return data, nil
			}
			return nil, fmt.Errorf("unexpected helm call: %v", args)
		},
		KubeClient: fake.NewClientset(),
		RestConfig: &rest.Config{Host: "https://127.0.0.1:6443"},
	}

	result, err := RunUpgrade(context.Background(), UpgradeConfig{
		BinaryVersion: "0.12.35",
		Namespace:     "oberth",
	}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if !result.AlreadyUpToDate {
		t.Fatal("expected AlreadyUpToDate")
	}
	if result.Upgraded {
		t.Fatal("should not be upgraded")
	}
	if !strings.Contains(output.String(), "Already running v0.12.35") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRunUpgradeNoRelease(t *testing.T) {
	t.Parallel()
	deps := Deps{
		Output: io.Discard,
		RunHelm: func(_ context.Context, args []string) ([]byte, error) {
			if len(args) >= 2 && args[0] == "list" {
				return []byte("[]"), nil
			}
			return nil, fmt.Errorf("unexpected helm call: %v", args)
		},
		KubeClient: fake.NewClientset(),
		RestConfig: &rest.Config{Host: "https://127.0.0.1:6443"},
	}

	_, err := RunUpgrade(context.Background(), UpgradeConfig{
		BinaryVersion: "0.12.35",
		Namespace:     "oberth",
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "no Oberth Helm release found") {
		t.Fatalf("error = %v, want 'no Oberth Helm release found'", err)
	}
}

func TestRunUpgradeRejectsDowngrade(t *testing.T) {
	t.Parallel()
	deps := Deps{
		Output: io.Discard,
		RunHelm: func(_ context.Context, args []string) ([]byte, error) {
			if len(args) >= 2 && args[0] == "list" {
				releases := []helmRelease{{
					Name:      "oberth",
					Namespace: "oberth",
					Status:    "deployed",
					Chart:     "oberth-0.12.35",
				}}
				data, _ := json.Marshal(releases)
				return data, nil
			}
			return nil, fmt.Errorf("unexpected helm call: %v", args)
		},
		KubeClient: fake.NewClientset(),
		RestConfig: &rest.Config{Host: "https://127.0.0.1:6443"},
	}

	_, err := RunUpgrade(context.Background(), UpgradeConfig{
		BinaryVersion: "0.12.34",
		Namespace:     "oberth",
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "newer than CLI version") {
		t.Fatalf("downgrade error = %v", err)
	}
}

func TestRunUpgradeRejectsUnparseableInstalledVersion(t *testing.T) {
	t.Parallel()
	deps := Deps{
		Output: io.Discard,
		RunHelm: func(_ context.Context, args []string) ([]byte, error) {
			if len(args) >= 2 && args[0] == "list" {
				releases := []helmRelease{{
					Name:      "oberth",
					Namespace: "oberth",
					Status:    "deployed",
					Chart:     "oberth-garbage",
				}}
				data, _ := json.Marshal(releases)
				return data, nil
			}
			return nil, fmt.Errorf("unexpected helm call: %v", args)
		},
		KubeClient: fake.NewClientset(),
		RestConfig: &rest.Config{Host: "https://127.0.0.1:6443"},
	}

	_, err := RunUpgrade(context.Background(), UpgradeConfig{
		BinaryVersion: "0.12.35",
		Namespace:     "oberth",
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "could not be parsed") {
		t.Fatalf("unparseable error = %v", err)
	}
}

func TestRunUpgradeDryRun(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	deps := Deps{
		Output: &output,
		RunHelm: func(_ context.Context, args []string) ([]byte, error) {
			if len(args) >= 2 && args[0] == "list" {
				releases := []helmRelease{{
					Name:      "oberth",
					Namespace: "oberth",
					Status:    "deployed",
					Chart:     "oberth-0.12.34",
				}}
				data, _ := json.Marshal(releases)
				return data, nil
			}
			return nil, fmt.Errorf("unexpected helm call: %v", args)
		},
		KubeClient: fake.NewClientset(),
		RestConfig: &rest.Config{Host: "https://127.0.0.1:6443"},
	}

	result, err := RunUpgrade(context.Background(), UpgradeConfig{
		BinaryVersion: "0.12.35",
		Namespace:     "oberth",
		DryRun:        true,
	}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if result.PreviousVersion != "0.12.34" || result.TargetVersion != "0.12.35" {
		t.Fatalf("result = %+v", result)
	}
	text := output.String()
	if !strings.Contains(text, "Dry-run plan") {
		t.Fatalf("output = %q", text)
	}
	if !strings.Contains(text, "v0.12.34") || !strings.Contains(text, "v0.12.35") {
		t.Fatalf("output = %q", text)
	}
	if !strings.Contains(text, "No cluster changes were made") {
		t.Fatalf("output = %q", text)
	}
}

func TestUpgradeHelmArgs(t *testing.T) {
	t.Parallel()
	cfg := UpgradeConfig{
		Namespace:     "oberth",
		BinaryVersion: "0.12.35",
		Timeout:       5 * time.Minute,
	}
	args := upgradeHelmArgs(cfg, "oberth-charts/oberth", "europe-west4-docker.pkg.dev/skipopsmain/cloudtaser/oberth:v0.12.35@sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "upgrade oberth oberth-charts/oberth") {
		t.Fatalf("args = %q", joined)
	}
	if !strings.Contains(joined, "--reuse-values") {
		t.Fatalf("missing --reuse-values: %q", joined)
	}
	if !strings.Contains(joined, "--set-string image.ref=") {
		t.Fatalf("missing --set-string image.ref: %q", joined)
	}
	if !strings.Contains(joined, "--version 0.12.35") {
		t.Fatalf("missing --version: %q", joined)
	}
	if !strings.Contains(joined, "--timeout 5m0s") {
		t.Fatalf("missing --timeout: %q", joined)
	}
	if !strings.Contains(joined, "--wait") {
		t.Fatalf("missing --wait: %q", joined)
	}
}

func TestUpgradeHelmArgsLocalChart(t *testing.T) {
	t.Parallel()
	cfg := UpgradeConfig{
		Namespace:     "oberth",
		BinaryVersion: "0.12.35",
		ChartOverride: "/tmp/chart",
	}
	args := upgradeHelmArgs(cfg, "/tmp/chart", "oberth:dev")
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--version") {
		t.Fatalf("local chart should not have --version: %q", joined)
	}
}

func TestDeploymentRolledOut(t *testing.T) {
	t.Parallel()
	replicas := int32(1)
	targetImage := "europe-west4-docker.pkg.dev/skipopsmain/cloudtaser/oberth:v0.12.35"

	tests := []struct {
		name       string
		deployment *appsv1.Deployment
		want       bool
	}{
		{
			name: "fully rolled out",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Generation: 2},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "oberth", Image: targetImage}},
						},
					},
				},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration:  2,
					UpdatedReplicas:     1,
					AvailableReplicas:   1,
					UnavailableReplicas: 0,
				},
			},
			want: true,
		},
		{
			name: "wrong image",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Generation: 2},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "oberth", Image: "old-image"}},
						},
					},
				},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration:  2,
					UpdatedReplicas:     1,
					AvailableReplicas:   1,
					UnavailableReplicas: 0,
				},
			},
			want: false,
		},
		{
			name: "generation not observed",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Generation: 3},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "oberth", Image: targetImage}},
						},
					},
				},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration:  2,
					UpdatedReplicas:     1,
					AvailableReplicas:   1,
					UnavailableReplicas: 0,
				},
			},
			want: false,
		},
		{
			name: "unavailable replicas",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Generation: 2},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "oberth", Image: targetImage}},
						},
					},
				},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration:  2,
					UpdatedReplicas:     1,
					AvailableReplicas:   0,
					UnavailableReplicas: 1,
				},
			},
			want: false,
		},
		{
			name: "nil replicas",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Generation: 2},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "oberth", Image: targetImage}},
						},
					},
				},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 2,
				},
			},
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := deploymentRolledOut(test.deployment, targetImage)
			if got != test.want {
				t.Fatalf("deploymentRolledOut = %v, want %v", got, test.want)
			}
		})
	}
}

func TestVerifyRunningVersionParsesOutput(t *testing.T) {
	t.Parallel()
	deps := Deps{
		Output: io.Discard,
		RunCommand: func(_ context.Context, _ []byte, _ string, _ ...string) ([]byte, error) {
			return []byte("oberth v0.12.35 commit=abc123 date=2026-08-18T00:00:00Z\n"), nil
		},
	}
	version, err := verifyRunningVersion(context.Background(), deps, UpgradeConfig{Namespace: "oberth"}, "0.12.35")
	if err != nil {
		t.Fatal(err)
	}
	if version != "v0.12.35" {
		t.Fatalf("version = %q, want v0.12.35", version)
	}
}

func TestVerifyRunningVersionHandlesKubectlNoise(t *testing.T) {
	t.Parallel()
	deps := Deps{
		Output: io.Discard,
		RunCommand: func(_ context.Context, _ []byte, _ string, _ ...string) ([]byte, error) {
			return []byte("Defaulted container \"oberth\" out of: oberth, init\noberth v0.12.35 commit=abc123 date=2026-08-18T00:00:00Z\n"), nil
		},
	}
	version, err := verifyRunningVersion(context.Background(), deps, UpgradeConfig{Namespace: "oberth"}, "0.12.35")
	if err != nil {
		t.Fatal(err)
	}
	if version != "v0.12.35" {
		t.Fatalf("version = %q, want v0.12.35", version)
	}
}

func TestVerifyRunningVersionRejectsMismatch(t *testing.T) {
	t.Parallel()
	deps := Deps{
		Output: io.Discard,
		RunCommand: func(_ context.Context, _ []byte, _ string, _ ...string) ([]byte, error) {
			return []byte("oberth v0.12.20 commit=abc123 date=2026-08-18T00:00:00Z\n"), nil
		},
	}
	_, err := verifyRunningVersion(context.Background(), deps, UpgradeConfig{Namespace: "oberth"}, "0.12.35")
	if err == nil || !strings.Contains(err.Error(), "expected v0.12.35 but pod reports v0.12.20") {
		t.Fatalf("mismatch error = %v", err)
	}
}

func TestVerifyRunningVersionRejectsUnparseable(t *testing.T) {
	t.Parallel()
	deps := Deps{
		Output: io.Discard,
		RunCommand: func(_ context.Context, _ []byte, _ string, _ ...string) ([]byte, error) {
			return []byte("oberth dev commit=unknown date=unknown\n"), nil
		},
	}
	_, err := verifyRunningVersion(context.Background(), deps, UpgradeConfig{Namespace: "oberth"}, "0.12.35")
	if err == nil || !strings.Contains(err.Error(), "could not parse") {
		t.Fatalf("unparseable error = %v", err)
	}
}

func TestResolveChartImageRef(t *testing.T) {
	t.Parallel()
	deps := Deps{
		Output: io.Discard,
		RunHelm: func(_ context.Context, args []string) ([]byte, error) {
			if len(args) >= 2 && args[0] == "show" && args[1] == "values" {
				return []byte("image:\n  ref: europe-west4-docker.pkg.dev/skipopsmain/cloudtaser/oberth:v0.12.35@sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789\n"), nil
			}
			return nil, fmt.Errorf("unexpected: %v", args)
		},
	}
	ref, err := resolveChartImageRef(context.Background(), deps, "oberth-charts/oberth", "0.12.35", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if ref != "europe-west4-docker.pkg.dev/skipopsmain/cloudtaser/oberth:v0.12.35@sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789" {
		t.Fatalf("ref = %q", ref)
	}
}

func TestResolveChartImageRefEmpty(t *testing.T) {
	t.Parallel()
	deps := Deps{
		Output: io.Discard,
		RunHelm: func(_ context.Context, args []string) ([]byte, error) {
			if len(args) >= 2 && args[0] == "show" && args[1] == "values" {
				return []byte("image:\n  ref: \"\"\n"), nil
			}
			return nil, fmt.Errorf("unexpected: %v", args)
		},
	}
	_, err := resolveChartImageRef(context.Background(), deps, "oberth-charts/oberth", "0.12.35", false, "")
	if err == nil || !strings.Contains(err.Error(), "chart does not define image.ref") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunUpgradeFullFlow(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	targetImage := "europe-west4-docker.pkg.dev/skipopsmain/cloudtaser/oberth:v0.12.35@sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	replicas := int32(1)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "oberth",
			Namespace:  "oberth",
			Generation: 2,
			Labels:     map[string]string{"app.kubernetes.io/instance": "oberth"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "oberth", Image: targetImage}},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration:  2,
			UpdatedReplicas:     1,
			AvailableReplicas:   1,
			UnavailableReplicas: 0,
		},
	}

	kubeClient := fake.NewClientset(deployment)

	helmCallLog := make([]string, 0)
	deps := Deps{
		Output: &output,
		RunHelm: func(_ context.Context, args []string) ([]byte, error) {
			helmCallLog = append(helmCallLog, args[0])
			switch {
			case args[0] == "list":
				releases := []helmRelease{{
					Name:      "oberth",
					Namespace: "oberth",
					Status:    "deployed",
					Chart:     "oberth-0.12.34",
				}}
				data, _ := json.Marshal(releases)
				return data, nil
			case args[0] == "repo":
				return nil, nil
			case args[0] == "show" && args[1] == "values":
				return []byte(fmt.Sprintf("image:\n  ref: %s\n", targetImage)), nil
			case args[0] == "upgrade":
				return nil, nil
			default:
				return nil, fmt.Errorf("unexpected helm call: %v", args)
			}
		},
		RunCommand: func(_ context.Context, _ []byte, _ string, _ ...string) ([]byte, error) {
			return []byte("oberth v0.12.35 commit=abc123 date=2026-08-18T00:00:00Z\n"), nil
		},
		KubeClient: kubeClient,
		RestConfig: &rest.Config{Host: "https://127.0.0.1:6443"},
	}

	result, err := RunUpgrade(context.Background(), UpgradeConfig{
		BinaryVersion: "0.12.35",
		Namespace:     "oberth",
		Timeout:       10 * time.Second,
	}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Upgraded {
		t.Fatal("expected Upgraded")
	}
	if result.PreviousVersion != "0.12.34" {
		t.Fatalf("PreviousVersion = %q", result.PreviousVersion)
	}
	text := output.String()
	if !strings.Contains(text, "v0.12.34") || !strings.Contains(text, "v0.12.35") {
		t.Fatalf("output = %q", text)
	}
	if !strings.Contains(text, "ready") {
		t.Fatalf("output = %q", text)
	}
	if !strings.Contains(text, "Target:") {
		t.Fatalf("output should contain target disclosure: %q", text)
	}
}

// --- #70: digest-form validation ---

func TestResolveChartImageRefRejectsTagOnly(t *testing.T) {
	t.Parallel()
	deps := Deps{
		Output: io.Discard,
		RunHelm: func(_ context.Context, args []string) ([]byte, error) {
			if args[0] == "show" && args[1] == "values" {
				return []byte("image:\n  ref: europe-west4-docker.pkg.dev/skipopsmain/cloudtaser/oberth:v0.12.35\n"), nil
			}
			return nil, fmt.Errorf("unexpected: %v", args)
		},
	}
	_, err := resolveChartImageRef(context.Background(), deps, "oberth-charts/oberth", "0.12.35", false, "")
	if err == nil || !strings.Contains(err.Error(), "not in digest-pinned form") {
		t.Fatalf("tag-only ref error = %v", err)
	}
}

func TestResolveChartImageRefRejectsShortDigest(t *testing.T) {
	t.Parallel()
	deps := Deps{
		Output: io.Discard,
		RunHelm: func(_ context.Context, args []string) ([]byte, error) {
			if args[0] == "show" && args[1] == "values" {
				return []byte("image:\n  ref: europe-west4-docker.pkg.dev/skipopsmain/cloudtaser/oberth@sha256:abc123\n"), nil
			}
			return nil, fmt.Errorf("unexpected: %v", args)
		},
	}
	_, err := resolveChartImageRef(context.Background(), deps, "oberth-charts/oberth", "0.12.35", false, "")
	if err == nil || !strings.Contains(err.Error(), "not in digest-pinned form") {
		t.Fatalf("short digest ref error = %v", err)
	}
}

func TestResolveChartImageRefRejectsNonGARRegistry(t *testing.T) {
	t.Parallel()
	validDigest := "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	deps := Deps{
		Output: io.Discard,
		RunHelm: func(_ context.Context, args []string) ([]byte, error) {
			if args[0] == "show" && args[1] == "values" {
				return []byte(fmt.Sprintf("image:\n  ref: other-registry.example.com/repo/oberth@%s\n", validDigest)), nil
			}
			return nil, fmt.Errorf("unexpected: %v", args)
		},
	}
	_, err := resolveChartImageRef(context.Background(), deps, "oberth-charts/oberth", "0.12.35", false, "")
	if err == nil || !strings.Contains(err.Error(), "canonical GAR prefix") {
		t.Fatalf("non-GAR registry error = %v", err)
	}
}

func TestResolveChartImageRefLocalChartSkipsRegistryCheck(t *testing.T) {
	t.Parallel()
	validDigest := "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	deps := Deps{
		Output: io.Discard,
		RunHelm: func(_ context.Context, args []string) ([]byte, error) {
			if args[0] == "show" && args[1] == "values" {
				return []byte(fmt.Sprintf("image:\n  ref: my-local-registry.dev/oberth@%s\n", validDigest)), nil
			}
			return nil, fmt.Errorf("unexpected: %v", args)
		},
	}
	ref, err := resolveChartImageRef(context.Background(), deps, "/tmp/chart", "0.12.35", true, "")
	if err != nil {
		t.Fatalf("local chart with non-GAR registry should pass: %v", err)
	}
	if !strings.Contains(ref, validDigest) {
		t.Fatalf("ref = %q", ref)
	}
}

func TestResolveChartImageRefLocalChartStillRequiresDigest(t *testing.T) {
	t.Parallel()
	deps := Deps{
		Output: io.Discard,
		RunHelm: func(_ context.Context, args []string) ([]byte, error) {
			if args[0] == "show" && args[1] == "values" {
				return []byte("image:\n  ref: my-local-registry.dev/oberth:latest\n"), nil
			}
			return nil, fmt.Errorf("unexpected: %v", args)
		},
	}
	_, err := resolveChartImageRef(context.Background(), deps, "/tmp/chart", "0.12.35", true, "")
	if err == nil || !strings.Contains(err.Error(), "not in digest-pinned form") {
		t.Fatalf("local chart without digest error = %v", err)
	}
}

// --- #71: non-local guard ---

func TestRunUpgradeRejectsNonLocalWithoutYes(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	deps := Deps{
		Output:      &output,
		ContextName: "prod-cluster",
		RestConfig:  &rest.Config{Host: "https://example.com:6443"},
		KubeClient:  fake.NewClientset(),
	}
	_, err := RunUpgrade(context.Background(), UpgradeConfig{
		BinaryVersion: "0.12.35",
		Namespace:     "oberth",
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "does not appear to be a local cluster") {
		t.Fatalf("non-local guard error = %v", err)
	}
	if !strings.Contains(output.String(), "Target: prod-cluster") {
		t.Fatalf("output should contain target disclosure: %q", output.String())
	}
}

func TestRunUpgradeAllowsNonLocalWithYes(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	deps := Deps{
		Output:      &output,
		ContextName: "prod-cluster",
		RestConfig:  &rest.Config{Host: "https://example.com:6443"},
		RunHelm: func(_ context.Context, args []string) ([]byte, error) {
			if args[0] == "list" {
				return []byte("[]"), nil // no releases — proves we passed the guard
			}
			return nil, fmt.Errorf("unexpected: %v", args)
		},
		KubeClient: fake.NewClientset(),
	}
	_, err := RunUpgrade(context.Background(), UpgradeConfig{
		BinaryVersion: "0.12.35",
		Namespace:     "oberth",
		Yes:           true,
	}, deps)
	// Should get past the non-local guard and fail on "no release found".
	if err == nil || !strings.Contains(err.Error(), "no Oberth Helm release found") {
		t.Fatalf("expected 'no release found' (proving guard was bypassed), got: %v", err)
	}
	if !strings.Contains(output.String(), "WARNING:") {
		t.Fatalf("non-local --yes should print WARNING: %q", output.String())
	}
}

// --- #72: failure path gaps ---

func TestUpgradeHelmArgsNoAtomic(t *testing.T) {
	t.Parallel()
	cfg := UpgradeConfig{
		Namespace:     "oberth",
		BinaryVersion: "0.12.35",
		Timeout:       5 * time.Minute,
	}
	args := upgradeHelmArgs(cfg, "oberth-charts/oberth", "some-image@sha256:0000000000000000000000000000000000000000000000000000000000000000")
	for _, arg := range args {
		if arg == "--atomic" {
			t.Fatal("--atomic must not be present in upgrade helm args: auto-rollback + forward-only migrations = crashloop trap")
		}
	}
}

func TestUpgradeHelmArgsPassesTimeout(t *testing.T) {
	t.Parallel()
	cfg := UpgradeConfig{
		Namespace:     "oberth",
		BinaryVersion: "0.12.35",
		Timeout:       3 * time.Minute,
	}
	args := upgradeHelmArgs(cfg, "oberth-charts/oberth", "image@sha256:0000000000000000000000000000000000000000000000000000000000000000")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--timeout 3m0s") {
		t.Fatalf("missing --timeout 3m0s: %q", joined)
	}
}

func TestRunUpgradeRejectsEmptyInstalledVersion(t *testing.T) {
	t.Parallel()
	deps := Deps{
		Output: io.Discard,
		RunHelm: func(_ context.Context, args []string) ([]byte, error) {
			if len(args) >= 2 && args[0] == "list" {
				releases := []helmRelease{{
					Name:      "oberth",
					Namespace: "oberth",
					Status:    "deployed",
					Chart:     "oberth", // no version suffix
				}}
				data, _ := json.Marshal(releases)
				return data, nil
			}
			return nil, fmt.Errorf("unexpected helm call: %v", args)
		},
		KubeClient: fake.NewClientset(),
		RestConfig: &rest.Config{Host: "https://127.0.0.1:6443"},
	}

	result, err := RunUpgrade(context.Background(), UpgradeConfig{
		BinaryVersion: "0.12.35",
		Namespace:     "oberth",
	}, deps)
	if err == nil {
		t.Fatal("expected error for empty installed version")
	}
	if !strings.Contains(err.Error(), "no version could be determined") {
		t.Fatalf("error = %v, want 'no version could be determined'", err)
	}
	if result.Upgraded {
		t.Fatal("should not be upgraded")
	}
}

func TestRunUpgradeRetriesTransientVerifyExecFailure(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	targetImage := "europe-west4-docker.pkg.dev/skipopsmain/cloudtaser/oberth:v0.12.35@sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	replicas := int32(1)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "oberth",
			Namespace:  "oberth",
			Generation: 2,
			Labels:     map[string]string{"app.kubernetes.io/instance": "oberth"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "oberth", Image: targetImage}},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration:  2,
			UpdatedReplicas:     1,
			AvailableReplicas:   1,
			UnavailableReplicas: 0,
		},
	}

	kubeClient := fake.NewClientset(deployment)

	execAttempts := 0
	deps := Deps{
		Output:       &output,
		PollInterval: time.Millisecond,
		RunHelm: func(_ context.Context, args []string) ([]byte, error) {
			switch {
			case args[0] == "list":
				releases := []helmRelease{{
					Name: "oberth", Namespace: "oberth",
					Status: "deployed", Chart: "oberth-0.12.34",
				}}
				data, _ := json.Marshal(releases)
				return data, nil
			case args[0] == "repo":
				return nil, nil
			case args[0] == "show" && args[1] == "values":
				return []byte(fmt.Sprintf("image:\n  ref: %s\n", targetImage)), nil
			case args[0] == "upgrade":
				return nil, nil
			default:
				return nil, fmt.Errorf("unexpected helm call: %v", args)
			}
		},
		RunCommand: func(_ context.Context, _ []byte, _ string, _ ...string) ([]byte, error) {
			execAttempts++
			if execAttempts < 2 {
				return nil, fmt.Errorf("error: unable to upgrade connection: container not found")
			}
			return []byte("oberth v0.12.35 commit=abc123 date=2026-08-18T00:00:00Z\n"), nil
		},
		KubeClient: kubeClient,
		RestConfig: &rest.Config{Host: "https://127.0.0.1:6443"},
	}

	result, err := RunUpgrade(context.Background(), UpgradeConfig{
		BinaryVersion: "0.12.35",
		Namespace:     "oberth",
		Timeout:       10 * time.Second,
	}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Upgraded {
		t.Fatal("expected Upgraded")
	}
	if execAttempts < 2 {
		t.Fatalf("expected at least 2 exec attempts, got %d", execAttempts)
	}
}

func TestRunUpgradeFailsWhenVersionUnverifiable(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	targetImage := "europe-west4-docker.pkg.dev/skipopsmain/cloudtaser/oberth:v0.12.35@sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	replicas := int32(1)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "oberth",
			Namespace:  "oberth",
			Generation: 2,
			Labels:     map[string]string{"app.kubernetes.io/instance": "oberth"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "oberth", Image: targetImage}},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration:  2,
			UpdatedReplicas:     1,
			AvailableReplicas:   1,
			UnavailableReplicas: 0,
		},
	}

	kubeClient := fake.NewClientset(deployment)

	execAttempts := 0
	deps := Deps{
		Output:       &output,
		PollInterval: time.Millisecond,
		RunHelm: func(_ context.Context, args []string) ([]byte, error) {
			switch {
			case args[0] == "list":
				releases := []helmRelease{{
					Name: "oberth", Namespace: "oberth",
					Status: "deployed", Chart: "oberth-0.12.34",
				}}
				data, _ := json.Marshal(releases)
				return data, nil
			case args[0] == "repo":
				return nil, nil
			case args[0] == "show" && args[1] == "values":
				return []byte(fmt.Sprintf("image:\n  ref: %s\n", targetImage)), nil
			case args[0] == "upgrade":
				return nil, nil
			default:
				return nil, fmt.Errorf("unexpected helm call: %v", args)
			}
		},
		RunCommand: func(_ context.Context, _ []byte, _ string, _ ...string) ([]byte, error) {
			execAttempts++
			return nil, fmt.Errorf("error: unable to upgrade connection: container not found")
		},
		KubeClient: kubeClient,
		RestConfig: &rest.Config{Host: "https://127.0.0.1:6443"},
	}

	_, err := RunUpgrade(context.Background(), UpgradeConfig{
		BinaryVersion: "0.12.35",
		Namespace:     "oberth",
		Timeout:       10 * time.Second,
	}, deps)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "verify manually") {
		t.Fatalf("error should contain 'verify manually': %v", err)
	}
	if execAttempts != verifyMaxAttempts {
		t.Fatalf("expected exactly %d exec attempts, got %d", verifyMaxAttempts, execAttempts)
	}
	// Recovery guidance should be printed on version-verify failure.
	text := output.String()
	if !strings.Contains(text, "DO NOT run 'helm rollback'") {
		t.Fatalf("missing recovery guidance in output: %q", text)
	}
}

func TestRunUpgradeFailsOnVersionMismatchFullFlow(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	targetImage := "europe-west4-docker.pkg.dev/skipopsmain/cloudtaser/oberth:v0.12.35@sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	replicas := int32(1)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "oberth",
			Namespace:  "oberth",
			Generation: 2,
			Labels:     map[string]string{"app.kubernetes.io/instance": "oberth"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "oberth", Image: targetImage}},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration:  2,
			UpdatedReplicas:     1,
			AvailableReplicas:   1,
			UnavailableReplicas: 0,
		},
	}

	kubeClient := fake.NewClientset(deployment)

	execAttempts := 0
	deps := Deps{
		Output:       &output,
		PollInterval: time.Millisecond,
		RunHelm: func(_ context.Context, args []string) ([]byte, error) {
			switch {
			case args[0] == "list":
				releases := []helmRelease{{
					Name: "oberth", Namespace: "oberth",
					Status: "deployed", Chart: "oberth-0.12.34",
				}}
				data, _ := json.Marshal(releases)
				return data, nil
			case args[0] == "repo":
				return nil, nil
			case args[0] == "show" && args[1] == "values":
				return []byte(fmt.Sprintf("image:\n  ref: %s\n", targetImage)), nil
			case args[0] == "upgrade":
				return nil, nil
			default:
				return nil, fmt.Errorf("unexpected helm call: %v", args)
			}
		},
		RunCommand: func(_ context.Context, _ []byte, _ string, _ ...string) ([]byte, error) {
			execAttempts++
			return []byte("oberth v0.12.20 commit=abc123 date=2026-08-18T00:00:00Z\n"), nil
		},
		KubeClient: kubeClient,
		RestConfig: &rest.Config{Host: "https://127.0.0.1:6443"},
	}

	_, err := RunUpgrade(context.Background(), UpgradeConfig{
		BinaryVersion: "0.12.35",
		Namespace:     "oberth",
		Timeout:       10 * time.Second,
	}, deps)
	if err == nil {
		t.Fatal("expected error for version mismatch")
	}
	if !strings.Contains(err.Error(), "v0.12.35") || !strings.Contains(err.Error(), "v0.12.20") {
		t.Fatalf("error should name both versions: %v", err)
	}
	// Version mismatch is definitive — must NOT retry.
	if execAttempts != 1 {
		t.Fatalf("expected exactly 1 exec attempt (no retry on mismatch), got %d", execAttempts)
	}
}

func TestRunUpgradeRecoveryGuidanceOnHelmFailure(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	validDigest := "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	targetImage := "europe-west4-docker.pkg.dev/skipopsmain/cloudtaser/oberth@" + validDigest
	deps := Deps{
		Output: &output,
		RunHelm: func(_ context.Context, args []string) ([]byte, error) {
			switch args[0] {
			case "list":
				releases := []helmRelease{{
					Name:      "oberth",
					Namespace: "oberth",
					Status:    "deployed",
					Chart:     "oberth-0.12.34",
				}}
				data, _ := json.Marshal(releases)
				return data, nil
			case "repo":
				return nil, nil
			case "show":
				return []byte(fmt.Sprintf("image:\n  ref: %s\n", targetImage)), nil
			case "upgrade":
				return nil, fmt.Errorf("helm upgrade failed: release timed out")
			default:
				return nil, fmt.Errorf("unexpected: %v", args)
			}
		},
		KubeClient: fake.NewClientset(),
		RestConfig: &rest.Config{Host: "https://127.0.0.1:6443"},
	}

	_, err := RunUpgrade(context.Background(), UpgradeConfig{
		BinaryVersion: "0.12.35",
		Namespace:     "oberth",
		Timeout:       10 * time.Second,
	}, deps)
	if err == nil {
		t.Fatal("expected error from helm upgrade failure")
	}
	text := output.String()
	if !strings.Contains(text, "DO NOT run 'helm rollback'") {
		t.Fatalf("missing recovery guidance in output: %q", text)
	}
	if !strings.Contains(text, "forward-only") {
		t.Fatalf("missing forward-only mention in output: %q", text)
	}
	if !strings.Contains(text, "helm status oberth") {
		t.Fatalf("missing helm status pointer in output: %q", text)
	}
	if !strings.Contains(text, "kubectl logs") {
		t.Fatalf("missing kubectl logs pointer in output: %q", text)
	}
}

func TestRunUpgradeRecoveryGuidanceOnRolloutFailure(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	validDigest := "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	targetImage := "europe-west4-docker.pkg.dev/skipopsmain/cloudtaser/oberth@" + validDigest
	replicas := int32(1)

	// Deployment with wrong image so rollout never completes.
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "oberth",
			Namespace:  "oberth",
			Generation: 2,
			Labels:     map[string]string{"app.kubernetes.io/instance": "oberth"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "oberth", Image: "old-image"}},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration:  2,
			UpdatedReplicas:     0,
			AvailableReplicas:   1,
			UnavailableReplicas: 0,
		},
	}

	kubeClient := fake.NewClientset(deployment)

	deps := Deps{
		Output: &output,
		RunHelm: func(_ context.Context, args []string) ([]byte, error) {
			switch {
			case args[0] == "list":
				releases := []helmRelease{{
					Name: "oberth", Namespace: "oberth",
					Status: "deployed", Chart: "oberth-0.12.34",
				}}
				data, _ := json.Marshal(releases)
				return data, nil
			case args[0] == "repo":
				return nil, nil
			case args[0] == "show" && args[1] == "values":
				return []byte(fmt.Sprintf("image:\n  ref: %s\n", targetImage)), nil
			case args[0] == "upgrade":
				return nil, nil
			default:
				return nil, fmt.Errorf("unexpected helm call: %v", args)
			}
		},
		KubeClient: kubeClient,
		RestConfig: &rest.Config{Host: "https://127.0.0.1:6443"},
	}

	// Use a very short timeout so the rollout times out quickly.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := RunUpgrade(ctx, UpgradeConfig{
		BinaryVersion: "0.12.35",
		Namespace:     "oberth",
		Timeout:       100 * time.Millisecond,
	}, deps)
	if err == nil {
		t.Fatal("expected error from rollout timeout")
	}
	text := output.String()
	if !strings.Contains(text, "DO NOT run 'helm rollback'") {
		t.Fatalf("missing recovery guidance in output: %q", text)
	}
	if !strings.Contains(text, "helm status oberth") {
		t.Fatalf("missing helm status pointer in output: %q", text)
	}
	if !strings.Contains(text, "kubectl logs") {
		t.Fatalf("missing kubectl logs pointer in output: %q", text)
	}
}

// A fork publishes its releases to its own registry, so the chart it ships
// names that repository rather than the canonical GAR one. Rejecting it would
// make `oberth upgrade` unusable on every build that is not upstream's.
func TestResolveChartImageRefAcceptsThisBinarysOwnRepository(t *testing.T) {
	t.Parallel()
	validDigest := "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	forkRef := "ghcr.io/nikitzu/oberth@" + validDigest
	deps := Deps{
		Output: io.Discard,
		RunHelm: func(_ context.Context, args []string) ([]byte, error) {
			if args[0] == "show" && args[1] == "values" {
				return []byte("image:\n  ref: " + forkRef + "\n"), nil
			}
			return nil, fmt.Errorf("unexpected: %v", args)
		},
	}

	ref, err := resolveChartImageRef(context.Background(), deps, "oberth-charts/oberth", "0.12.35", false,
		"ghcr.io/nikitzu/oberth@sha256:1111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("a chart in this binary's own image repository was rejected: %v", err)
	}
	if ref != forkRef {
		t.Fatalf("ref = %q, want %q", ref, forkRef)
	}
}

// A registry that is neither this binary's own nor the canonical one is still
// refused: the repository test replaces the prefix test, it does not drop it.
func TestResolveChartImageRefStillRejectsAThirdPartyRegistry(t *testing.T) {
	t.Parallel()
	validDigest := "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	deps := Deps{
		Output: io.Discard,
		RunHelm: func(_ context.Context, args []string) ([]byte, error) {
			if args[0] == "show" && args[1] == "values" {
				return []byte("image:\n  ref: other-registry.example.com/repo/oberth@" + validDigest + "\n"), nil
			}
			return nil, fmt.Errorf("unexpected: %v", args)
		},
	}

	_, err := resolveChartImageRef(context.Background(), deps, "oberth-charts/oberth", "0.12.35", false,
		"ghcr.io/nikitzu/oberth@sha256:1111111111111111111111111111111111111111111111111111111111111111")
	if err == nil || !strings.Contains(err.Error(), "canonical GAR prefix") {
		t.Fatalf("third-party registry error = %v", err)
	}
}
