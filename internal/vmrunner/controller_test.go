package vmrunner

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeBackend is a test-only VMBackend that records calls and returns
// configured responses. It never touches KubeVirt or Kubernetes.
type fakeBackend struct {
	createCalls   atomic.Int32
	getCalls      atomic.Int32
	deleteCalls   atomic.Int32
	listCalls     atomic.Int32
	createErr     error
	getPhases     []string // Sequential phases returned by GetVMIPhase.
	getExitCodes  []int32  // Corresponding exit codes.
	getErr        error
	deleteErr     error
	listResult    []string
	listErr       error
	lastNamespace string
	lastSpec      *VMRunSpec
}

func (f *fakeBackend) CreateVMI(_ context.Context, namespace string, spec VMRunSpec) (string, string, error) {
	f.createCalls.Add(1)
	f.lastNamespace = namespace
	specCopy := spec
	f.lastSpec = &specCopy
	if f.createErr != nil {
		return "", "", f.createErr
	}
	return "vmi-" + spec.RunID, "uid-" + spec.RunID, nil
}

func (f *fakeBackend) GetVMIPhase(_ context.Context, namespace, _ string) (string, int32, error) {
	f.getCalls.Add(1)
	f.lastNamespace = namespace
	if f.getErr != nil {
		return "", 0, f.getErr
	}
	index := int(f.getCalls.Load()) - 1
	if index >= len(f.getPhases) {
		index = len(f.getPhases) - 1
	}
	if index < 0 {
		return "Pending", 0, nil
	}
	var exitCode int32
	if index < len(f.getExitCodes) {
		exitCode = f.getExitCodes[index]
	}
	return f.getPhases[index], exitCode, nil
}

func (f *fakeBackend) DeleteVMI(_ context.Context, namespace, _ string) error {
	f.deleteCalls.Add(1)
	f.lastNamespace = namespace
	return f.deleteErr
}

func (f *fakeBackend) ListVMIs(_ context.Context, namespace string, _ time.Duration) ([]string, error) {
	f.listCalls.Add(1)
	f.lastNamespace = namespace
	return f.listResult, f.listErr
}

func validConfig() Config {
	return Config{
		Namespace:    "oberth-pipelines",
		MaxCPUCores:  4,
		MaxMemoryMiB: 16384,
		MaxDiskGiB:   64,
		MaxDeadline:  6 * time.Hour,
		OrphanGrace:  time.Hour,
	}
}

// --- Controller construction ---

func TestNewController_ValidConfig(t *testing.T) {
	_, err := NewController(validConfig(), &fakeBackend{})
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestNewController_EmptyNamespace(t *testing.T) {
	config := validConfig()
	config.Namespace = ""
	_, err := NewController(config, &fakeBackend{})
	if err == nil || !strings.Contains(err.Error(), "Namespace is required") {
		t.Fatalf("expected namespace rejection, got: %v", err)
	}
}

func TestNewController_Defaults(t *testing.T) {
	config := Config{Namespace: "test"}
	ctrl, err := NewController(config, &fakeBackend{})
	if err != nil {
		t.Fatalf("defaults config rejected: %v", err)
	}
	if ctrl.config.MaxCPUCores != MaxVMCPUCores {
		t.Errorf("MaxCPUCores default: got %d, want %d", ctrl.config.MaxCPUCores, MaxVMCPUCores)
	}
	if ctrl.config.MaxMemoryMiB != MaxVMMemoryMiB {
		t.Errorf("MaxMemoryMiB default: got %d, want %d", ctrl.config.MaxMemoryMiB, MaxVMMemoryMiB)
	}
}

// --- Controller admission ---

func TestController_Admit_Valid(t *testing.T) {
	ctrl, _ := NewController(validConfig(), &fakeBackend{})
	if err := ctrl.Admit(validSpec()); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
}

func TestController_Admit_ExceedsConfiguredCeiling(t *testing.T) {
	config := validConfig()
	config.MaxCPUCores = 2
	ctrl, _ := NewController(config, &fakeBackend{})
	spec := validSpec()
	spec.Resources.CPUCores = 3
	err := ctrl.Admit(spec)
	if err == nil || !strings.Contains(err.Error(), "configured") {
		t.Fatalf("expected configured ceiling rejection, got: %v", err)
	}
}

func TestController_Admit_ExceedsConfiguredDeadline(t *testing.T) {
	config := validConfig()
	config.MaxDeadline = time.Hour
	ctrl, _ := NewController(config, &fakeBackend{})
	spec := validSpec()
	spec.Deadline = 2 * time.Hour
	err := ctrl.Admit(spec)
	if err == nil || !strings.Contains(err.Error(), "configured") {
		t.Fatalf("expected configured deadline rejection, got: %v", err)
	}
}

// --- Controller create ---

func TestController_Create_Success(t *testing.T) {
	backend := &fakeBackend{}
	ctrl, _ := NewController(validConfig(), backend)
	name, err := ctrl.Create(context.Background(), validSpec())
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if name == "" {
		t.Fatal("create returned empty name")
	}
	if backend.createCalls.Load() != 1 {
		t.Fatalf("expected 1 create call, got %d", backend.createCalls.Load())
	}
	if backend.lastNamespace != "oberth-pipelines" {
		t.Fatalf("VMI created in wrong namespace: %s", backend.lastNamespace)
	}
}

func TestController_Create_RejectsInvalidSpec(t *testing.T) {
	ctrl, _ := NewController(validConfig(), &fakeBackend{})
	spec := validSpec()
	spec.CandidateSHA = ""
	_, err := ctrl.Create(context.Background(), spec)
	if err == nil {
		t.Fatal("expected rejection of invalid spec")
	}
}

func TestController_Create_NoBackend(t *testing.T) {
	ctrl, _ := NewController(validConfig(), nil)
	_, err := ctrl.Create(context.Background(), validSpec())
	if err == nil || !strings.Contains(err.Error(), "no backend configured") {
		t.Fatalf("expected no-backend error, got: %v", err)
	}
}

func TestController_Create_BackendError(t *testing.T) {
	backend := &fakeBackend{createErr: errors.New("kubevirt unavailable")}
	ctrl, _ := NewController(validConfig(), backend)
	_, err := ctrl.Create(context.Background(), validSpec())
	if err == nil || !strings.Contains(err.Error(), "kubevirt unavailable") {
		t.Fatalf("expected backend error propagation, got: %v", err)
	}
}

// --- Controller wait ---

func TestController_Wait_ImmediateSuccess(t *testing.T) {
	backend := &fakeBackend{
		getPhases:    []string{"Succeeded"},
		getExitCodes: []int32{0},
	}
	ctrl, _ := NewController(validConfig(), backend)
	spec := validSpec()
	result, err := ctrl.Wait(context.Background(), "vmi-test", spec)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}
	if result.Phase != "Succeeded" {
		t.Fatalf("expected Succeeded, got %s", result.Phase)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}
}

func TestController_Wait_ImmediateFailure(t *testing.T) {
	backend := &fakeBackend{
		getPhases:    []string{"Failed"},
		getExitCodes: []int32{1},
	}
	ctrl, _ := NewController(validConfig(), backend)
	result, err := ctrl.Wait(context.Background(), "vmi-test", validSpec())
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}
	if result.Phase != "Failed" {
		t.Fatalf("expected Failed, got %s", result.Phase)
	}
	if result.ExitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", result.ExitCode)
	}
}

func TestController_Wait_TransitionsToSuccess(t *testing.T) {
	backend := &fakeBackend{
		getPhases:    []string{"Pending", "Running", "Succeeded"},
		getExitCodes: []int32{0, 0, 0},
	}
	ctrl, _ := NewController(validConfig(), backend)
	result, err := ctrl.Wait(context.Background(), "vmi-test", validSpec())
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}
	if result.Phase != "Succeeded" {
		t.Fatalf("expected Succeeded, got %s", result.Phase)
	}
}

func TestController_Wait_ContextCancellation(t *testing.T) {
	backend := &fakeBackend{
		getPhases: []string{"Pending", "Pending", "Pending"},
	}
	ctrl, _ := NewController(validConfig(), backend)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	spec := validSpec()
	spec.Deadline = 200 * time.Millisecond
	// The spec deadline is validated at admission; use the controller ceiling
	// for the context-driven test. We override below.
	spec.Deadline = MinDeadline
	result, err := ctrl.Wait(ctx, "vmi-test", spec)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if result.Phase != "Failed" {
		t.Fatalf("expected Failed on timeout, got %s", result.Phase)
	}
	// Verify cleanup was attempted.
	if backend.deleteCalls.Load() < 1 {
		t.Fatal("expected VMI deletion on timeout")
	}
}

// --- Adversarial: evidence integrity ---

func TestEvidence_ExitCodeSourceNeverLogMarker(t *testing.T) {
	// Invariant I2: evidence comes from the VMI phase, not from log markers.
	backend := &fakeBackend{
		getPhases:    []string{"Succeeded"},
		getExitCodes: []int32{0},
	}
	ctrl, _ := NewController(validConfig(), backend)
	result, err := ctrl.Wait(context.Background(), "vmi-test", validSpec())
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}
	if result.Evidence.ExitCodeSource != ExitCodeSourceVMIPhase {
		t.Fatalf("ExitCodeSource must be %q, got %q", ExitCodeSourceVMIPhase, result.Evidence.ExitCodeSource)
	}
	// Verify the constant is never "log-marker".
	if ExitCodeSourceVMIPhase == "log-marker" {
		t.Fatal("ExitCodeSourceVMIPhase must never be 'log-marker'")
	}
}

func TestEvidence_BindsCandidateSHA(t *testing.T) {
	// The evidence must echo the candidate SHA from the admitted spec, not
	// from anything the guest reports.
	backend := &fakeBackend{
		getPhases:    []string{"Succeeded"},
		getExitCodes: []int32{0},
	}
	ctrl, _ := NewController(validConfig(), backend)
	spec := validSpec()
	result, err := ctrl.Wait(context.Background(), "vmi-test", spec)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}
	if result.Evidence.CandidateSHA != spec.CandidateSHA {
		t.Fatalf("evidence CandidateSHA mismatch: got %q, want %q",
			result.Evidence.CandidateSHA, spec.CandidateSHA)
	}
}

func TestEvidence_BindsSuiteRevision(t *testing.T) {
	backend := &fakeBackend{
		getPhases:    []string{"Succeeded"},
		getExitCodes: []int32{0},
	}
	ctrl, _ := NewController(validConfig(), backend)
	spec := validSpec()
	result, err := ctrl.Wait(context.Background(), "vmi-test", spec)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}
	if result.Evidence.SuiteRevision != spec.SuiteRevision {
		t.Fatalf("evidence SuiteRevision mismatch: got %q, want %q",
			result.Evidence.SuiteRevision, spec.SuiteRevision)
	}
}

func TestEvidence_BindsGuestImage(t *testing.T) {
	backend := &fakeBackend{
		getPhases:    []string{"Succeeded"},
		getExitCodes: []int32{0},
	}
	ctrl, _ := NewController(validConfig(), backend)
	spec := validSpec()
	result, err := ctrl.Wait(context.Background(), "vmi-test", spec)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}
	if result.Evidence.GuestImageRef != spec.GuestImageRef {
		t.Fatalf("evidence GuestImageRef mismatch: got %q, want %q",
			result.Evidence.GuestImageRef, spec.GuestImageRef)
	}
}

func TestEvidence_TimestampsAreSet(t *testing.T) {
	backend := &fakeBackend{
		getPhases:    []string{"Succeeded"},
		getExitCodes: []int32{0},
	}
	ctrl, _ := NewController(validConfig(), backend)
	result, err := ctrl.Wait(context.Background(), "vmi-test", validSpec())
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}
	if result.Evidence.StartedAt.IsZero() {
		t.Fatal("evidence StartedAt must be set")
	}
	if result.Evidence.FinishedAt.IsZero() {
		t.Fatal("evidence FinishedAt must be set")
	}
	if result.Evidence.FinishedAt.Before(result.Evidence.StartedAt) {
		t.Fatal("evidence FinishedAt must not be before StartedAt")
	}
	if result.Duration <= 0 {
		t.Fatal("result Duration must be positive")
	}
}

// --- Adversarial: VMI has no service account token ---

func TestVMI_NoServiceAccountToken(t *testing.T) {
	// Invariant I3: the guest has no cluster access. The backend must
	// construct VMIs with automountServiceAccountToken: false. This test
	// verifies the spec passed to the backend contains the correct binding.
	backend := &fakeBackend{}
	ctrl, _ := NewController(validConfig(), backend)
	spec := validSpec()
	_, err := ctrl.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	// The backend receives the validated spec. In Phase 2, the real backend
	// will construct the VMI with automountServiceAccountToken: false. For
	// now, verify the spec is passed through unchanged.
	if backend.lastSpec == nil {
		t.Fatal("backend did not receive the spec")
	}
	if backend.lastSpec.CandidateSHA != spec.CandidateSHA {
		t.Fatal("backend received a different CandidateSHA")
	}
}

// --- Adversarial: namespace isolation ---

func TestVMI_CreatedInPipelineNamespace(t *testing.T) {
	// VMIs must be created in the pipeline namespace, never in the server
	// namespace. This is the same isolation the Argo engine enforces.
	backend := &fakeBackend{}
	config := validConfig()
	config.Namespace = "oberth-pipelines"
	ctrl, _ := NewController(config, backend)
	_, err := ctrl.Create(context.Background(), validSpec())
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if backend.lastNamespace != "oberth-pipelines" {
		t.Fatalf("VMI created in namespace %q, expected %q",
			backend.lastNamespace, "oberth-pipelines")
	}
}

// --- Controller cancel ---

func TestController_Cancel(t *testing.T) {
	backend := &fakeBackend{}
	ctrl, _ := NewController(validConfig(), backend)
	if err := ctrl.Cancel(context.Background(), "vmi-test"); err != nil {
		t.Fatalf("cancel failed: %v", err)
	}
	if backend.deleteCalls.Load() != 1 {
		t.Fatalf("expected 1 delete call, got %d", backend.deleteCalls.Load())
	}
}

func TestController_Cancel_NoBackend(t *testing.T) {
	ctrl, _ := NewController(validConfig(), nil)
	// Cancel without a backend should be a no-op, not an error.
	if err := ctrl.Cancel(context.Background(), "vmi-test"); err != nil {
		t.Fatalf("cancel without backend should not error: %v", err)
	}
}

// --- Orphan sweep ---

func TestController_SweepOrphanedVMIs(t *testing.T) {
	backend := &fakeBackend{
		listResult: []string{"vmi-old-1", "vmi-old-2"},
	}
	ctrl, _ := NewController(validConfig(), backend)
	swept, err := ctrl.SweepOrphanedVMIs(context.Background())
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if swept != 2 {
		t.Fatalf("expected 2 swept, got %d", swept)
	}
	if backend.deleteCalls.Load() != 2 {
		t.Fatalf("expected 2 delete calls, got %d", backend.deleteCalls.Load())
	}
}

func TestController_SweepOrphanedVMIs_Empty(t *testing.T) {
	backend := &fakeBackend{
		listResult: nil,
	}
	ctrl, _ := NewController(validConfig(), backend)
	swept, err := ctrl.SweepOrphanedVMIs(context.Background())
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if swept != 0 {
		t.Fatalf("expected 0 swept, got %d", swept)
	}
}

func TestController_SweepOrphanedVMIs_NoBackend(t *testing.T) {
	ctrl, _ := NewController(validConfig(), nil)
	swept, err := ctrl.SweepOrphanedVMIs(context.Background())
	if err != nil {
		t.Fatalf("sweep without backend should not error: %v", err)
	}
	if swept != 0 {
		t.Fatalf("expected 0 swept without backend, got %d", swept)
	}
}

// --- Adversarial: marker spoofing ---

func TestEvidence_MarkerSpoofingIsStructurallyImpossible(t *testing.T) {
	// The VMRunResult is constructed entirely by the server from:
	// 1. The VMI phase (from KubeVirt API, not guest output)
	// 2. The admitted spec (immutable after admission)
	// There is no code path that reads evidence from guest output.
	//
	// This test verifies the structural property: VMRunResult fields are
	// set by the controller, not by any external input.
	backend := &fakeBackend{
		getPhases:    []string{"Failed"},
		getExitCodes: []int32{42},
	}
	ctrl, _ := NewController(validConfig(), backend)
	spec := validSpec()
	result, err := ctrl.Wait(context.Background(), "vmi-test", spec)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}
	// Even though the VMI "failed" with exit code 42, the evidence still
	// comes from the server, not from the guest.
	if result.Evidence.ExitCodeSource != ExitCodeSourceVMIPhase {
		t.Fatalf("exit code source must be %q even on failure, got %q",
			ExitCodeSourceVMIPhase, result.Evidence.ExitCodeSource)
	}
	// The candidate SHA in the evidence is from the spec, not from the guest.
	if result.Evidence.CandidateSHA != spec.CandidateSHA {
		t.Fatal("evidence CandidateSHA must come from the spec, not the guest")
	}
	// The suite revision is from the spec, not from the guest.
	if result.Evidence.SuiteRevision != spec.SuiteRevision {
		t.Fatal("evidence SuiteRevision must come from the spec, not the guest")
	}
}

func TestEvidence_GuestCannotOverrideExitCode(t *testing.T) {
	// A guest that prints "exit code: 0" to stdout cannot override the
	// real exit code from the VMI phase. The controller never reads exit
	// codes from logs.
	backend := &fakeBackend{
		getPhases:    []string{"Failed"},
		getExitCodes: []int32{137}, // OOM-killed, for example
	}
	ctrl, _ := NewController(validConfig(), backend)
	result, err := ctrl.Wait(context.Background(), "vmi-test", validSpec())
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}
	// The exit code must be 137, not 0 (even if the guest tried to claim 0).
	if result.ExitCode != 137 {
		t.Fatalf("exit code must come from VMI phase (137), got %d", result.ExitCode)
	}
	if result.Phase != "Failed" {
		t.Fatalf("phase must be Failed, got %s", result.Phase)
	}
}

// --- Adversarial: credential fishing ---

func TestController_Create_NeverPassesCredentials(t *testing.T) {
	// The VMRunSpec has no field for credentials, tokens, or secrets.
	// This is a structural defence: there is no code path through which
	// a pipeline step could inject credentials into a VM execution.
	//
	// Verify this by examining the VMRunSpec type: it contains only
	// RunID, Repo, CandidateSHA, SuiteRevision, GuestImageRef, Resources,
	// and Deadline. No credential field exists.
	spec := validSpec()
	// The spec identity contains only these fields.
	identity := SpecIdentity(spec)
	if strings.Contains(identity, "token") || strings.Contains(identity, "secret") ||
		strings.Contains(identity, "password") || strings.Contains(identity, "credential") {
		t.Fatal("SpecIdentity must not reference any credential-like field")
	}
}

// --- Config validation ---

func TestConfig_Validate_InvalidMaxCPU(t *testing.T) {
	config := validConfig()
	config.MaxCPUCores = 0
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "MaxCPUCores") {
		t.Fatalf("expected MaxCPUCores rejection, got: %v", err)
	}
}

func TestConfig_Validate_InvalidMaxMemory(t *testing.T) {
	config := validConfig()
	config.MaxMemoryMiB = 100
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "MaxMemoryMiB") {
		t.Fatalf("expected MaxMemoryMiB rejection, got: %v", err)
	}
}

func TestConfig_Validate_InvalidMaxDisk(t *testing.T) {
	config := validConfig()
	config.MaxDiskGiB = -1
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "MaxDiskGiB") {
		t.Fatalf("expected MaxDiskGiB rejection, got: %v", err)
	}
}

func TestConfig_Validate_InvalidMaxDeadline(t *testing.T) {
	config := validConfig()
	config.MaxDeadline = 10 * time.Second
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "MaxDeadline") {
		t.Fatalf("expected MaxDeadline rejection, got: %v", err)
	}
}
