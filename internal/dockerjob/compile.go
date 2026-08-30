// Package dockerjob executes an admitted Oberth pipeline as plain Docker
// containers against a local daemon, with no Kubernetes and no Argo
// controller anywhere in the path.
//
// It reads exactly the document the Argo engine reads, admitted by exactly the
// same gate, and interprets a documented subset of it. The subset is the whole
// design risk: a construct this engine does not implement is a construct that
// behaves differently here than on a cluster. So the rule is that every
// unsupported construct is refused by name at compile time, before a single
// container starts, and never silently ignored. A pipeline that runs here ran
// what it said it would run, or it did not start.
//
// Supported: a `dag` or `steps` entrypoint, one optional level of nesting into
// a per-burn dag or steps template, and `container` leaves carrying image,
// command, args, env, and workingDir. Dependencies order execution.
// activeDeadlineSeconds bounds the run. retryStrategy.limit re-runs a failed
// step. resources.limits.cpu and resources.limits.memory bound the container.
// The per-run shared volume replaces volumeClaimTemplates.
//
// Refused by name: synchronization, withItems, withParam, withSequence,
// onExit and lifecycle hooks at every level, continueOn, `when` guards,
// parameter interpolation, script/resource/plugin/http/data/containerSet/
// suspend templates, initContainers, sidecars, daemon templates, per-template
// activeDeadlineSeconds and timeout, dag failFast: false, every retryStrategy
// field other than limit, nesting beyond one level, and repository-declared
// volumeMounts.
package dockerjob

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	wfv1 "github.com/argoproj/argo-workflows/v4/pkg/apis/workflow/v1alpha1"
	corev1 "k8s.io/api/core/v1"

	"github.com/oberthci/oberth/pkg/argoworkflow"
)

// Mount topology. These are the same paths the Argo engine mounts, so a
// pipeline authored against one finds its tree at the same place under the
// other. Here they are directories inside a single per-run Docker volume
// rather than four separate Kubernetes volumes.
const (
	WorkMountPath   = "/work"
	SourceMountPath = "/work/src"
	// CacheMountPath is the cross-run, per-repository build cache. Unlike
	// everything else under /work it is not part of the run volume: a
	// separate named volume is mounted over it, so it survives the run.
	CacheMountPath     = "/work/cache"
	ArtifactsMountPath = "/work/artifacts"
	FilesMountPath     = "/work/files"
)

// ErrUnsupported is the class of every refusal this compiler makes. It is a
// distinct class so a caller can tell "this pipeline asks for something the
// docker engine does not implement" apart from "this pipeline is invalid".
var ErrUnsupported = errors.New("dockerjob: construct not supported by the docker engine")

func unsupported(construct, detail string) error {
	if detail != "" {
		detail = " (" + detail + ")"
	}
	return fmt.Errorf("%w: %s%s; this pipeline runs on the Argo engine but not on --engine=docker",
		ErrUnsupported, construct, detail)
}

// Step is one container this engine will run, named in Oberth's burn/step
// vocabulary so everything downstream (runlog, progress, step results) reads
// identically to an Argo run.
type Step struct {
	Burn       string
	Step       string
	Ordinal    int
	Image      string
	Command    []string
	Args       []string
	Env        []string // "NAME=value", ready for docker --env
	WorkingDir string
	// RetryLimit is the number of ADDITIONAL attempts after the first, taken
	// from retryStrategy.limit. Zero means one attempt.
	RetryLimit int
	// CPULimit is resources.limits.cpu expressed in whole cores, ready for
	// docker --cpus. Zero means the document declared none.
	CPULimit float64
	// MemoryLimitBytes is resources.limits.memory in bytes, ready for docker
	// --memory. Zero means the document declared none.
	MemoryLimitBytes int64
	// PlanSteps is how many steps the whole plan has. It travels on each step
	// so the engine can stamp it on every container it creates, which is what
	// lets a restarted server tell a finished run from a truncated one.
	PlanSteps int
}

// Plan is a whole compiled pipeline: a flat, dependency-ordered chain of
// containers plus the run's wall-clock bound.
//
// Flat and sequential is a deliberate reduction, not an oversight. Argo runs
// independent DAG branches concurrently; this engine serialises them in an
// order that respects every declared dependency. A pipeline therefore produces
// the same result here, and takes longer. The alternative, real concurrency,
// would also need the interleaved-log attribution problem solved, and the
// spike's question is whether the seam holds, not whether it is fast.
type Plan struct {
	Steps           []Step
	DeadlineSeconds int64
}

// Compile turns an admitted Workflow into an executable Plan, or refuses.
//
// The caller must have run argoworkflow.Admit first. Compile does not repeat
// admission and must never be treated as a substitute for it: it refuses
// constructs it cannot RUN, which is a different and much smaller set than the
// constructs admission refuses because they are unsafe.
func Compile(workflow *wfv1.Workflow) (Plan, error) {
	if workflow == nil {
		return Plan{}, errors.New("dockerjob: no Workflow to compile")
	}
	if err := refuseWorkflowLevel(workflow); err != nil {
		return Plan{}, err
	}
	deadline := int64(0)
	if workflow.Spec.ActiveDeadlineSeconds != nil {
		deadline = *workflow.Spec.ActiveDeadlineSeconds
	}
	if deadline <= 0 {
		// Admission requires this, so reaching here means Compile was handed
		// an unadmitted document.
		return Plan{}, errors.New("dockerjob: spec.activeDeadlineSeconds is required and must be positive")
	}
	templates := map[string]*wfv1.Template{}
	for index := range workflow.Spec.Templates {
		template := &workflow.Spec.Templates[index]
		if _, exists := templates[template.Name]; !exists {
			templates[template.Name] = template
		}
	}
	entrypoint := strings.TrimSpace(workflow.Spec.Entrypoint)
	root, ok := templates[entrypoint]
	if !ok {
		return Plan{}, fmt.Errorf("dockerjob: spec.entrypoint %q names no template", entrypoint)
	}
	for name := range templates {
		if err := refuseTemplateKind(templates[name]); err != nil {
			return Plan{}, err
		}
	}
	compiler := &compiler{templates: templates}
	ordered, err := orderedInvocations(root)
	if err != nil {
		return Plan{}, err
	}
	for _, called := range ordered {
		target := compiler.resolve(called)
		if target == nil {
			return Plan{}, fmt.Errorf("dockerjob: task %q references template %q, which the document does not define",
				called.name, called.template)
		}
		switch {
		case target.DAG != nil || len(target.Steps) != 0:
			// A nested dag or steps template is a burn. Its own invocations
			// must all be pod-producing: two levels is the shape Oberth's
			// burn/step model can name, and this engine does not pretend to
			// support a third.
			inner, err := orderedInvocations(target)
			if err != nil {
				return Plan{}, err
			}
			for _, leaf := range inner {
				leafTarget := compiler.resolve(leaf)
				if leafTarget == nil {
					return Plan{}, fmt.Errorf("dockerjob: task %q references template %q, which the document does not define",
						leaf.name, leaf.template)
				}
				if leafTarget.DAG != nil || len(leafTarget.Steps) != 0 {
					return Plan{}, unsupported("template nesting deeper than one level",
						fmt.Sprintf("task %s/%s invokes another dag or steps template", called.name, leaf.name))
				}
				if err := compiler.appendLeaf(called.name, leaf, leafTarget); err != nil {
					return Plan{}, err
				}
			}
		default:
			if err := compiler.appendLeaf("", called, target); err != nil {
				return Plan{}, err
			}
		}
	}
	if len(compiler.steps) == 0 {
		return Plan{}, errors.New("dockerjob: pipeline declares no runnable container steps")
	}
	for index := range compiler.steps {
		compiler.steps[index].PlanSteps = len(compiler.steps)
	}
	return Plan{Steps: compiler.steps, DeadlineSeconds: deadline}, nil
}

type compiler struct {
	templates map[string]*wfv1.Template
	steps     []Step
}

func (c *compiler) resolve(called invocation) *wfv1.Template {
	if called.inline != nil {
		return called.inline
	}
	if called.template == "" {
		return nil
	}
	return c.templates[called.template]
}

func (c *compiler) appendLeaf(enclosing string, called invocation, target *wfv1.Template) error {
	burn, step := argoworkflow.BurnAndStep(enclosing, called.name)
	if burn == "" || step == "" {
		return fmt.Errorf("dockerjob: task %q does not yield a usable burn/step name", called.name)
	}
	container := target.Container
	if container == nil {
		return unsupported("a template that produces no container",
			fmt.Sprintf("template %q used by %s/%s", target.Name, burn, step))
	}
	compiled := Step{
		Burn: burn, Step: step, Ordinal: len(c.steps),
		Image: strings.TrimSpace(container.Image), WorkingDir: strings.TrimSpace(container.WorkingDir),
	}
	if compiled.Image == "" {
		return fmt.Errorf("dockerjob: step %s/%s declares no container image", burn, step)
	}
	if len(container.VolumeMounts) != 0 {
		return unsupported("repository-declared volumeMounts",
			fmt.Sprintf("step %s/%s; the docker engine mounts only the server-owned %s tree", burn, step, WorkMountPath))
	}
	for _, value := range container.Command {
		if err := refuseInterpolation(value, burn, step, "command"); err != nil {
			return err
		}
		compiled.Command = append(compiled.Command, value)
	}
	for _, value := range container.Args {
		if err := refuseInterpolation(value, burn, step, "args"); err != nil {
			return err
		}
		compiled.Args = append(compiled.Args, value)
	}
	for _, variable := range container.Env {
		if variable.ValueFrom != nil {
			return unsupported("env valueFrom",
				fmt.Sprintf("step %s/%s variable %s; the docker engine resolves no Kubernetes references", burn, step, variable.Name))
		}
		if err := refuseInterpolation(variable.Value, burn, step, "env "+variable.Name); err != nil {
			return err
		}
		compiled.Env = append(compiled.Env, variable.Name+"="+variable.Value)
	}
	if len(container.EnvFrom) != 0 {
		return unsupported("envFrom", fmt.Sprintf("step %s/%s", burn, step))
	}
	if err := applyResourceLimits(&compiled, container.Resources, burn, step); err != nil {
		return err
	}
	if target.RetryStrategy != nil {
		limit, err := retryLimit(target.RetryStrategy)
		if err != nil {
			return fmt.Errorf("dockerjob: step %s/%s: %w", burn, step, err)
		}
		compiled.RetryLimit = limit
	}
	c.steps = append(c.steps, compiled)
	return nil
}

func retryLimit(strategy *wfv1.RetryStrategy) (int, error) {
	if strategy.Limit == nil {
		return 0, nil
	}
	value, err := strconv.Atoi(strategy.Limit.String())
	if err != nil {
		return 0, fmt.Errorf("retryStrategy.limit is not an integer: %w", err)
	}
	if value < 0 || value > argoworkflow.MaxRetryLimit {
		return 0, fmt.Errorf("retryStrategy.limit %d is outside 0..%d", value, argoworkflow.MaxRetryLimit)
	}
	return value, nil
}

// applyResourceLimits carries the document's declared limits onto the
// container, so a step is bounded here by the same numbers the kubelet would
// enforce on a cluster. Admission has already capped them; this only has to
// translate them.
//
// Requests are deliberately dropped. On a cluster a request is a scheduling
// input, and this engine has one host and Oberth's own concurrency limiter, so
// a request describes nothing it could act on. A limit, by contrast, is a
// ceiling the step can actually hit, and ignoring it meant a step declaring
// 2 CPUs and 4 GiB could take the whole laptop.
func applyResourceLimits(compiled *Step, resources corev1.ResourceRequirements, burn, step string) error {
	if quantity, declared := resources.Limits[corev1.ResourceEphemeralStorage]; declared && !quantity.IsZero() {
		// A cluster evicts a Pod that exceeds this. Docker has no equivalent
		// bound on a volume, so honouring it is not possible and ignoring it
		// would be the silent divergence this compiler exists to prevent.
		return unsupported("resources.limits.ephemeral-storage",
			fmt.Sprintf("step %s/%s; the docker engine cannot bound the run volume", burn, step))
	}
	if quantity, declared := resources.Limits[corev1.ResourceCPU]; declared && !quantity.IsZero() {
		cores := quantity.AsApproximateFloat64()
		if cores <= 0 {
			return fmt.Errorf("dockerjob: step %s/%s declares an unusable resources.limits.cpu %q", burn, step, quantity.String())
		}
		compiled.CPULimit = cores
	}
	if quantity, declared := resources.Limits[corev1.ResourceMemory]; declared && !quantity.IsZero() {
		bytes, ok := quantity.AsInt64()
		if !ok || bytes <= 0 {
			return fmt.Errorf("dockerjob: step %s/%s declares an unusable resources.limits.memory %q", burn, step, quantity.String())
		}
		compiled.MemoryLimitBytes = bytes
	}
	return nil
}

// refuseInterpolation refuses any {{...}} placeholder. This engine substitutes
// no parameters, and a container that received "{{inputs.parameters.tag}}"
// literally would do something the author did not ask for. Refusing is the
// only honest option.
func refuseInterpolation(value, burn, step, field string) error {
	if strings.Contains(value, "{{") {
		return unsupported("parameter interpolation",
			fmt.Sprintf("step %s/%s %s contains a {{...}} placeholder the docker engine does not substitute", burn, step, field))
	}
	return nil
}

func refuseWorkflowLevel(workflow *wfv1.Workflow) error {
	if workflow.Spec.Synchronization != nil {
		return unsupported("spec.synchronization", "the docker engine holds no cross-run mutex")
	}
	if strings.TrimSpace(workflow.Spec.OnExit) != "" {
		return unsupported("spec.onExit", "exit handlers do not run under the docker engine")
	}
	if len(workflow.Spec.Hooks) != 0 {
		return unsupported("spec.hooks", "lifecycle hooks do not run under the docker engine")
	}
	if workflow.Spec.WorkflowTemplateRef != nil {
		return unsupported("spec.workflowTemplateRef", "the docker engine resolves no cluster-side templates")
	}
	if strings.TrimSpace(workflow.Spec.Entrypoint) == "" {
		return errors.New("dockerjob: spec.entrypoint is required")
	}
	return nil
}

func refuseTemplateKind(template *wfv1.Template) error {
	if template == nil {
		return nil
	}
	named := fmt.Sprintf("template %q", template.Name)
	switch {
	case template.Script != nil:
		return unsupported("script templates", named+"; use a container template with command and args")
	case template.Resource != nil:
		return unsupported("resource templates", named+"; the docker engine creates no Kubernetes objects")
	case template.Plugin != nil:
		return unsupported("plugin templates", named)
	case template.HTTP != nil:
		return unsupported("http templates", named)
	case template.Data != nil:
		return unsupported("data templates", named)
	case template.ContainerSet != nil:
		return unsupported("containerSet templates", named+"; the docker engine runs one container per step")
	case template.Suspend != nil:
		return unsupported("suspend templates", named)
	}
	if template.Synchronization != nil {
		return unsupported("template synchronization", named+"; the docker engine holds no cross-run mutex")
	}
	if len(template.Sidecars) != 0 {
		return unsupported("sidecars", named+"; the docker engine runs one container per step")
	}
	if len(template.InitContainers) != 0 {
		return unsupported("initContainers", named+"; the docker engine runs one container per step")
	}
	if template.Daemon != nil && *template.Daemon {
		return unsupported("daemon templates", named+"; the docker engine runs every step to completion")
	}
	if template.ActiveDeadlineSeconds != nil {
		return unsupported("template activeDeadlineSeconds",
			named+"; the docker engine bounds the whole run, not a single step")
	}
	if strings.TrimSpace(template.Timeout) != "" {
		return unsupported("template timeout",
			named+"; the docker engine bounds the whole run, not a single step")
	}
	if err := refuseRetryStrategy(template.RetryStrategy, named); err != nil {
		return err
	}
	if template.DAG != nil {
		// failFast defaults to true. An explicit false asks Argo to run every
		// remaining branch after one fails, which this engine cannot do: it
		// stops at the first failed step.
		if template.DAG.FailFast != nil && !*template.DAG.FailFast {
			return unsupported("dag failFast: false",
				named+"; the docker engine stops at the first failed step")
		}
		for index := range template.DAG.Tasks {
			task := &template.DAG.Tasks[index]
			where := fmt.Sprintf("%s task %q", named, task.Name)
			if err := refuseCallSiteHooks(task.OnExit, task.Hooks, task.ContinueOn, where); err != nil {
				return err
			}
		}
	}
	for group := range template.Steps {
		for index := range template.Steps[group].Steps {
			step := &template.Steps[group].Steps[index]
			where := fmt.Sprintf("%s step %q", named, step.Name)
			if err := refuseCallSiteHooks(step.OnExit, step.Hooks, step.ContinueOn, where); err != nil {
				return err
			}
		}
	}
	return nil
}

// refuseCallSiteHooks refuses the per-call-site constructs that change what
// runs or what a failure means. All three are admitted by the gate, because
// none of them is a way to reach substrate Oberth did not grant; they are
// refused here because this engine does not implement them, and running a
// pipeline that declares one while ignoring it would produce a different
// verdict than the same document produces on a cluster.
func refuseCallSiteHooks(onExit string, hooks wfv1.LifecycleHooks, continueOn *wfv1.ContinueOn, where string) error {
	if strings.TrimSpace(onExit) != "" {
		return unsupported("onExit", where)
	}
	if len(hooks) != 0 {
		return unsupported("lifecycle hooks", where)
	}
	if continueOn != nil {
		return unsupported("continueOn", where+
			"; the docker engine stops the run at the first failed step")
	}
	return nil
}

// refuseRetryStrategy admits only retryStrategy.limit. Every other field asks
// for behaviour this engine does not have: a backoff it does not wait, a
// policy it does not evaluate, an expression it does not parse, and a node
// affinity that has no meaning with one host.
func refuseRetryStrategy(strategy *wfv1.RetryStrategy, named string) error {
	if strategy == nil {
		return nil
	}
	if strategy.Backoff != nil {
		return unsupported("retryStrategy.backoff", named+"; the docker engine retries immediately")
	}
	if strings.TrimSpace(string(strategy.RetryPolicy)) != "" {
		return unsupported("retryStrategy.retryPolicy",
			named+"; the docker engine retries every failed attempt up to the limit")
	}
	if strings.TrimSpace(strategy.Expression) != "" {
		return unsupported("retryStrategy.expression", named+"; the docker engine evaluates no expressions")
	}
	if strategy.Affinity != nil {
		return unsupported("retryStrategy.affinity", named+"; the docker engine runs every attempt on one host")
	}
	return nil
}

// invocation is one call site inside a dag or steps template, normalised so
// both shapes compile identically. It mirrors the same normalisation
// argoworkflow's plan walk makes, so the compiled chain and the recorded step
// plan cannot disagree about what a pipeline declares.
type invocation struct {
	name         string
	template     string
	inline       *wfv1.Template
	dependencies []string
}

// orderedInvocations returns a template's call sites in an order that respects
// every declared dependency, refusing the constructs this engine cannot honour.
func orderedInvocations(template *wfv1.Template) ([]invocation, error) {
	var called []invocation
	if template.DAG != nil {
		for index := range template.DAG.Tasks {
			task := &template.DAG.Tasks[index]
			if len(task.WithItems) != 0 {
				return nil, unsupported("withItems", "task "+task.Name)
			}
			if strings.TrimSpace(task.WithParam) != "" {
				return nil, unsupported("withParam", "task "+task.Name)
			}
			if task.WithSequence != nil {
				return nil, unsupported("withSequence", "task "+task.Name)
			}
			if strings.TrimSpace(task.When) != "" {
				return nil, unsupported("when guards", "task "+task.Name+"; the docker engine evaluates no expressions")
			}
			dependencies := append([]string(nil), task.Dependencies...)
			if strings.TrimSpace(task.Depends) != "" {
				parsed, err := parseDepends(task.Depends, task.Name)
				if err != nil {
					return nil, err
				}
				dependencies = append(dependencies, parsed...)
			}
			called = append(called, invocation{
				name: task.Name, template: task.Template, inline: task.Inline, dependencies: dependencies,
			})
		}
		return topologicalOrder(called, template.Name)
	}
	// A steps template is already ordered: each group runs after the previous
	// one. Groups are flattened in declaration order, which respects the
	// sequencing the author wrote.
	for group := range template.Steps {
		for index := range template.Steps[group].Steps {
			step := &template.Steps[group].Steps[index]
			if len(step.WithItems) != 0 {
				return nil, unsupported("withItems", "step "+step.Name)
			}
			if strings.TrimSpace(step.WithParam) != "" {
				return nil, unsupported("withParam", "step "+step.Name)
			}
			if step.WithSequence != nil {
				return nil, unsupported("withSequence", "step "+step.Name)
			}
			if strings.TrimSpace(step.When) != "" {
				return nil, unsupported("when guards", "step "+step.Name+"; the docker engine evaluates no expressions")
			}
			called = append(called, invocation{name: step.Name, template: step.Template, inline: step.Inline})
		}
	}
	return called, nil
}

// parseDepends reads the simple conjunctive form of Argo's depends field.
// Anything richer than "a && b" carries per-branch status conditions this
// engine does not evaluate, so it is refused rather than approximated.
func parseDepends(expression, task string) ([]string, error) {
	if strings.ContainsAny(expression, "|()!") {
		return nil, unsupported("depends expressions with conditions or alternation",
			"task "+task+"; use dependencies, or a plain \"a && b\" depends")
	}
	var names []string
	for _, part := range strings.Split(expression, "&&") {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		if strings.Contains(trimmed, ".") {
			return nil, unsupported("depends status conditions", "task "+task+" depends on "+trimmed)
		}
		names = append(names, trimmed)
	}
	return names, nil
}

// topologicalOrder linearises a DAG. Ties are broken by declaration order so
// the same document always compiles to the same chain, which is what makes a
// run reproducible and a log diffable.
func topologicalOrder(called []invocation, templateName string) ([]invocation, error) {
	position := make(map[string]int, len(called))
	for index, item := range called {
		if _, duplicate := position[item.name]; duplicate {
			return nil, fmt.Errorf("dockerjob: template %q declares task %q twice", templateName, item.name)
		}
		position[item.name] = index
	}
	remaining := make(map[string][]string, len(called))
	for _, item := range called {
		for _, dependency := range item.dependencies {
			if _, known := position[dependency]; !known {
				return nil, fmt.Errorf("dockerjob: task %q depends on %q, which template %q does not declare",
					item.name, dependency, templateName)
			}
		}
		remaining[item.name] = append([]string(nil), item.dependencies...)
	}
	ordered := make([]invocation, 0, len(called))
	done := make(map[string]bool, len(called))
	for len(ordered) < len(called) {
		var ready []string
		for name, dependencies := range remaining {
			satisfied := true
			for _, dependency := range dependencies {
				if !done[dependency] {
					satisfied = false
					break
				}
			}
			if satisfied {
				ready = append(ready, name)
			}
		}
		if len(ready) == 0 {
			return nil, fmt.Errorf("dockerjob: template %q has a dependency cycle", templateName)
		}
		sort.Slice(ready, func(left, right int) bool { return position[ready[left]] < position[ready[right]] })
		next := ready[0]
		ordered = append(ordered, called[position[next]])
		done[next] = true
		delete(remaining, next)
	}
	return ordered, nil
}
