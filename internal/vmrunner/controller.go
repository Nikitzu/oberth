package vmrunner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Controller manages VM lifecycle on behalf of pipeline steps. It is
// operator-owned: the pipeline declares what to test, the controller
// decides how to run it. The controller creates VMIs through its own
// Kubernetes identity, not the pipeline's.
//
// Phase 1: lifecycle state machine and interface only. KubeVirt API
// integration is Phase 2 (see docs/solutions/kubevirt-runner.md).
type Controller struct {
	config    Config
	backend   VMBackend
	namespace string
}

// Config is the administrator-owned half of the VM runner. Every field that
// decides trust is here, not in the repository document.
type Config struct {
	// Namespace is where VMIs are created. Must be the pipeline namespace
	// (same as Argo workflows), never the server namespace.
	Namespace string

	// MaxCPUCores caps the per-VM CPU allocation.
	// Zero selects the default MaxVMCPUCores.
	MaxCPUCores int

	// MaxMemoryMiB caps the per-VM memory allocation.
	// Zero selects the default MaxVMMemoryMiB.
	MaxMemoryMiB int

	// MaxDiskGiB caps the per-VM disk allocation.
	// Zero selects the default MaxVMDiskGiB.
	MaxDiskGiB int

	// MaxDeadline caps the per-VM execution deadline.
	// Zero selects the default MaxDeadline.
	MaxDeadline time.Duration

	// OrphanGrace is how long an unfinished VMI survives after its parent
	// run is interrupted before the sweep deletes it.
	// Zero selects one hour.
	OrphanGrace time.Duration
}

func (config *Config) applyDefaults() {
	if config.MaxCPUCores <= 0 {
		config.MaxCPUCores = MaxVMCPUCores
	}
	if config.MaxMemoryMiB <= 0 {
		config.MaxMemoryMiB = MaxVMMemoryMiB
	}
	if config.MaxDiskGiB <= 0 {
		config.MaxDiskGiB = MaxVMDiskGiB
	}
	if config.MaxDeadline <= 0 {
		config.MaxDeadline = MaxDeadline
	}
	if config.OrphanGrace <= 0 {
		config.OrphanGrace = time.Hour
	}
}

// Validate rejects a configuration that could not produce a safe submission.
func (config Config) Validate() error {
	var problems []error
	if strings.TrimSpace(config.Namespace) == "" {
		problems = append(problems, errors.New("vmrunner: Namespace is required"))
	}
	if config.MaxCPUCores < 1 || config.MaxCPUCores > 64 {
		problems = append(problems, fmt.Errorf("vmrunner: MaxCPUCores %d is outside [1, 64]", config.MaxCPUCores))
	}
	if config.MaxMemoryMiB < MinVMMemoryMiB || config.MaxMemoryMiB > 131072 {
		problems = append(problems, fmt.Errorf("vmrunner: MaxMemoryMiB %d is outside [%d, 131072]", config.MaxMemoryMiB, MinVMMemoryMiB))
	}
	if config.MaxDiskGiB < 0 || config.MaxDiskGiB > 1024 {
		problems = append(problems, fmt.Errorf("vmrunner: MaxDiskGiB %d is outside [0, 1024]", config.MaxDiskGiB))
	}
	if config.MaxDeadline < MinDeadline {
		problems = append(problems, fmt.Errorf("vmrunner: MaxDeadline %s is below the %s minimum", config.MaxDeadline, MinDeadline))
	}
	return errors.Join(problems...)
}

// VMBackend is the narrow interface through which the controller interacts
// with KubeVirt. It is separated from the Kubernetes client so tests can
// supply a fake without needing KubeVirt CRDs.
//
// Phase 1 defines the interface. Phase 2 provides the real implementation.
type VMBackend interface {
	// CreateVMI creates a VirtualMachineInstance from the given spec. It
	// returns the VMI name and UID on success. The backend is responsible
	// for constructing the VMI with the security baseline documented in
	// docs/solutions/kubevirt-runner.md.
	CreateVMI(ctx context.Context, namespace string, spec VMRunSpec) (name string, uid string, err error)

	// GetVMIPhase returns the current phase of a VMI. Terminal phases are
	// "Succeeded" and "Failed". Non-terminal phases include "Pending",
	// "Scheduling", "Scheduled", and "Running".
	GetVMIPhase(ctx context.Context, namespace, name string) (phase string, exitCode int32, err error)

	// DeleteVMI deletes a VMI by name. It is idempotent: deleting a
	// non-existent VMI returns nil.
	DeleteVMI(ctx context.Context, namespace, name string) error

	// ListVMIs lists VMI names created by this controller (identified by
	// the oberth.ci/tier=vm-runner label) that are older than the given age.
	ListVMIs(ctx context.Context, namespace string, olderThan time.Duration) ([]string, error)
}

// NewController builds the VM runner controller. The backend may be nil in
// tests that only exercise admission and lifecycle state transitions.
func NewController(config Config, backend VMBackend) (*Controller, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Controller{
		config:    config,
		backend:   backend,
		namespace: config.Namespace,
	}, nil
}

// Namespace reports where this controller creates VMIs.
func (controller *Controller) Namespace() string { return controller.namespace }

// Admit validates a VM run spec against the controller's configuration.
// This runs before any VMI exists.
func (controller *Controller) Admit(spec VMRunSpec) error {
	if err := ValidateVMRunSpec(spec); err != nil {
		return err
	}
	// Enforce controller-specific ceilings that may be tighter than the
	// package-level constants.
	var problems []error
	if spec.Resources.CPUCores > controller.config.MaxCPUCores {
		problems = append(problems, fmt.Errorf(
			"vmrunner: CPUCores %d exceeds the configured %d ceiling",
			spec.Resources.CPUCores, controller.config.MaxCPUCores))
	}
	if spec.Resources.MemoryMiB > controller.config.MaxMemoryMiB {
		problems = append(problems, fmt.Errorf(
			"vmrunner: MemoryMiB %d exceeds the configured %d ceiling",
			spec.Resources.MemoryMiB, controller.config.MaxMemoryMiB))
	}
	if spec.Resources.DiskGiB > controller.config.MaxDiskGiB {
		problems = append(problems, fmt.Errorf(
			"vmrunner: DiskGiB %d exceeds the configured %d ceiling",
			spec.Resources.DiskGiB, controller.config.MaxDiskGiB))
	}
	if spec.Deadline > controller.config.MaxDeadline {
		problems = append(problems, fmt.Errorf(
			"vmrunner: Deadline %s exceeds the configured %s ceiling",
			spec.Deadline, controller.config.MaxDeadline))
	}
	return errors.Join(problems...)
}

// Create validates and submits a VM execution. It returns the VMI name on
// success. The VMI is created with the security baseline documented in
// docs/solutions/kubevirt-runner.md.
func (controller *Controller) Create(ctx context.Context, spec VMRunSpec) (string, error) {
	if controller.backend == nil {
		return "", errors.New("vmrunner: no backend configured; KubeVirt integration is Phase 2")
	}
	if err := controller.Admit(spec); err != nil {
		return "", err
	}
	name, _, err := controller.backend.CreateVMI(ctx, controller.namespace, spec)
	if err != nil {
		return "", fmt.Errorf("vmrunner: create VMI: %w", err)
	}
	return name, nil
}

// Wait supervises a VMI to its terminal phase and returns the result.
// It polls the VMI phase until it reaches "Succeeded" or "Failed", or
// until the context is cancelled.
func (controller *Controller) Wait(ctx context.Context, name string, spec VMRunSpec) (VMRunResult, error) {
	if controller.backend == nil {
		return VMRunResult{}, errors.New("vmrunner: no backend configured; KubeVirt integration is Phase 2")
	}
	startedAt := time.Now()
	deadline := spec.Deadline
	if deadline <= 0 {
		deadline = controller.config.MaxDeadline
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		phase, exitCode, err := controller.backend.GetVMIPhase(ctx, controller.namespace, name)
		if err != nil {
			return VMRunResult{}, fmt.Errorf("vmrunner: read VMI %s: %w", name, err)
		}
		if isTerminal(phase) {
			finishedAt := time.Now()
			return VMRunResult{
				Phase:    phase,
				ExitCode: exitCode,
				Evidence: VMEvidence{
					CandidateSHA:   spec.CandidateSHA,
					SuiteRevision:  spec.SuiteRevision,
					GuestImageRef:  spec.GuestImageRef,
					VMInstanceName: name,
					StartedAt:      startedAt,
					FinishedAt:     finishedAt,
					ExitCodeSource: ExitCodeSourceVMIPhase,
				},
				Duration: finishedAt.Sub(startedAt),
			}, nil
		}
		select {
		case <-ctx.Done():
			// Deadline exceeded or context cancelled. Clean up the VMI.
			_ = controller.Cancel(context.WithoutCancel(ctx), name)
			return VMRunResult{
				Phase:    "Failed",
				ExitCode: -1,
				Evidence: VMEvidence{
					CandidateSHA:   spec.CandidateSHA,
					SuiteRevision:  spec.SuiteRevision,
					GuestImageRef:  spec.GuestImageRef,
					VMInstanceName: name,
					StartedAt:      startedAt,
					FinishedAt:     time.Now(),
					ExitCodeSource: ExitCodeSourceVMIPhase,
				},
				Duration: time.Since(startedAt),
			}, fmt.Errorf("vmrunner: VMI %s did not complete within %s", name, deadline)
		case <-ticker.C:
		}
	}
}

// Cancel deletes a VMI by name. It is idempotent.
func (controller *Controller) Cancel(ctx context.Context, name string) error {
	if controller.backend == nil {
		return nil
	}
	return controller.backend.DeleteVMI(ctx, controller.namespace, name)
}

// SweepOrphanedVMIs deletes VMIs that are older than the configured grace
// window. This catches VMIs that were orphaned by a server crash or restart.
func (controller *Controller) SweepOrphanedVMIs(ctx context.Context) (int, error) {
	if controller.backend == nil {
		return 0, nil
	}
	names, err := controller.backend.ListVMIs(ctx, controller.namespace, controller.config.OrphanGrace)
	if err != nil {
		return 0, fmt.Errorf("vmrunner: list orphaned VMIs: %w", err)
	}
	var swept int
	for _, name := range names {
		if deleteErr := controller.backend.DeleteVMI(ctx, controller.namespace, name); deleteErr != nil {
			err = errors.Join(err, deleteErr)
		} else {
			swept++
		}
	}
	return swept, err
}

// isTerminal reports whether a VMI phase is terminal.
func isTerminal(phase string) bool {
	return phase == "Succeeded" || phase == "Failed"
}
