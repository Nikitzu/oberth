package dockerjob

import (
	"errors"
	"strings"
	"testing"

	"github.com/oberthci/oberth/pkg/argoworkflow"
)

const digest = "@sha256:66bb8d36ae1ddd72199ed235a089904874ca4079ee517936ca3adb80506a75c1"

func compileYAML(t *testing.T, body string) (Plan, error) {
	t.Helper()
	workflow, err := argoworkflow.Decode([]byte(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return Compile(workflow)
}

func mustCompile(t *testing.T, body string) Plan {
	t.Helper()
	plan, err := compileYAML(t, body)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return plan
}

// refusal asserts that a document is refused as unsupported, and that the
// message names the construct. A refusal nobody can act on is not much better
// than a silent no-op, so the naming is part of the contract.
func refusal(t *testing.T, body, mustMention string) {
	t.Helper()
	_, err := compileYAML(t, body)
	if err == nil {
		t.Fatalf("expected a refusal mentioning %q, got a successful compile", mustMention)
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
	if !strings.Contains(err.Error(), mustMention) {
		t.Fatalf("refusal does not name %q: %v", mustMention, err)
	}
}

func header(body string) string {
	return "apiVersion: argoproj.io/v1alpha1\nkind: Workflow\nspec:\n  entrypoint: ci\n  activeDeadlineSeconds: 600\n" + body
}

func TestCompileOrdersBurnsByDependency(t *testing.T) {
	plan := mustCompile(t, header(`  templates:
    - name: ci
      dag:
        tasks:
          - name: second
            template: unit
            dependencies: [first]
          - name: first
            template: lint
    - name: lint
      container:
        image: "golang:1.26`+digest+`"
        command: ["sh", "-c"]
        args: ["gofmt -l ."]
    - name: unit
      container:
        image: "golang:1.26`+digest+`"
        command: ["sh", "-c"]
        args: ["go test ./..."]
`))
	if len(plan.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(plan.Steps))
	}
	// Declaration order puts "second" first; the dependency must reorder it.
	if plan.Steps[0].Burn != "first" || plan.Steps[1].Burn != "second" {
		t.Fatalf("dependency order not honoured: %s then %s", plan.Steps[0].Burn, plan.Steps[1].Burn)
	}
	if plan.DeadlineSeconds != 600 {
		t.Fatalf("deadline: got %d", plan.DeadlineSeconds)
	}
}

func TestCompileNestsOneLevelIntoBurnAndStep(t *testing.T) {
	plan := mustCompile(t, header(`  templates:
    - name: ci
      dag:
        tasks:
          - name: verify
            template: verify-burn
    - name: verify-burn
      dag:
        tasks:
          - name: unit
            template: unit
          - name: lint
            template: lint
            dependencies: [unit]
    - name: unit
      container:
        image: "golang:1.26`+digest+`"
        command: ["go"]
        args: ["test", "./..."]
        workingDir: /work/src
        env:
          - name: CGO_ENABLED
            value: "0"
    - name: lint
      container:
        image: "golang:1.26`+digest+`"
        command: ["gofmt"]
`))
	if len(plan.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(plan.Steps))
	}
	first := plan.Steps[0]
	if first.Burn != "verify" || first.Step != "unit" {
		t.Fatalf("burn/step naming: got %s/%s", first.Burn, first.Step)
	}
	if first.WorkingDir != "/work/src" {
		t.Fatalf("workingDir: got %q", first.WorkingDir)
	}
	if len(first.Env) != 1 || first.Env[0] != "CGO_ENABLED=0" {
		t.Fatalf("env: got %v", first.Env)
	}
	if plan.Steps[1].Step != "lint" {
		t.Fatalf("second step: got %s", plan.Steps[1].Step)
	}
	if plan.Steps[0].Ordinal != 0 || plan.Steps[1].Ordinal != 1 {
		t.Fatalf("ordinals not sequential: %d, %d", plan.Steps[0].Ordinal, plan.Steps[1].Ordinal)
	}
}

func TestCompileHonoursRetryLimit(t *testing.T) {
	plan := mustCompile(t, header(`  templates:
    - name: ci
      dag:
        tasks:
          - name: flaky
            template: flaky
    - name: flaky
      retryStrategy:
        limit: "3"
      container:
        image: "golang:1.26`+digest+`"
        command: ["true"]
`))
	if plan.Steps[0].RetryLimit != 3 {
		t.Fatalf("retry limit: got %d", plan.Steps[0].RetryLimit)
	}
}

func TestCompileFlattensStepsTemplatesInDeclarationOrder(t *testing.T) {
	plan := mustCompile(t, header(`  templates:
    - name: ci
      steps:
        - - name: build
            template: run
        - - name: test
            template: run
    - name: run
      container:
        image: "golang:1.26`+digest+`"
        command: ["true"]
`))
	if len(plan.Steps) != 2 || plan.Steps[0].Burn != "build" || plan.Steps[1].Burn != "test" {
		t.Fatalf("steps flattening: got %+v", plan.Steps)
	}
}

// Every construct below runs correctly on the Argo engine and would run
// INCORRECTLY here if it were ignored. Each must be refused by name.
func TestCompileRefusesUnsupportedConstructs(t *testing.T) {
	container := `    - name: run
      container:
        image: "golang:1.26` + digest + `"
        command: ["true"]
`
	cases := []struct{ name, body, mention string }{
		{"withItems", header(`  templates:
    - name: ci
      dag:
        tasks:
          - name: fan
            template: run
            withItems: [a, b]
` + container), "withItems"},
		{"withSequence", header(`  templates:
    - name: ci
      dag:
        tasks:
          - name: fan
            template: run
            withSequence:
              count: "3"
` + container), "withSequence"},
		{"when", header(`  templates:
    - name: ci
      dag:
        tasks:
          - name: maybe
            template: run
            when: "{{workflow.parameters.go}} == yes"
` + container), "when guards"},
		{"onExit", header(`  onExit: run
  templates:
    - name: ci
      dag:
        tasks:
          - name: only
            template: run
` + container), "onExit"},
		{"synchronization", header(`  synchronization:
    mutexes:
      - name: build
  templates:
    - name: ci
      dag:
        tasks:
          - name: only
            template: run
` + container), "synchronization"},
		{"script template", header(`  templates:
    - name: ci
      dag:
        tasks:
          - name: only
            template: run
    - name: run
      script:
        image: "golang:1.26` + digest + `"
        command: ["sh"]
        source: "echo hi"
`), "script templates"},
		{"parameter interpolation", header(`  templates:
    - name: ci
      dag:
        tasks:
          - name: only
            template: run
    - name: run
      container:
        image: "golang:1.26` + digest + `"
        command: ["sh", "-c"]
        args: ["echo {{workflow.name}}"]
`), "parameter interpolation"},
		{"deep nesting", header(`  templates:
    - name: ci
      dag:
        tasks:
          - name: outer
            template: middle
    - name: middle
      dag:
        tasks:
          - name: inner
            template: deeper
    - name: deeper
      dag:
        tasks:
          - name: leaf
            template: run
` + container), "nesting deeper than one level"},
		{"volumeMounts", header(`  templates:
    - name: ci
      dag:
        tasks:
          - name: only
            template: run
    - name: run
      container:
        image: "golang:1.26` + digest + `"
        command: ["true"]
        volumeMounts:
          - name: extra
            mountPath: /extra
`), "volumeMounts"},
		{"sidecars", header(`  templates:
    - name: ci
      dag:
        tasks:
          - name: only
            template: run
    - name: run
      sidecars:
        - name: db
          image: "golang:1.26` + digest + `"
      container:
        image: "golang:1.26` + digest + `"
        command: ["true"]
`), "sidecars"},
		{"depends conditions", header(`  templates:
    - name: ci
      dag:
        tasks:
          - name: first
            template: run
          - name: second
            template: run
            depends: "first.Succeeded"
` + container), "depends status conditions"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			refusal(t, testCase.body, testCase.mention)
		})
	}
}

func TestCompileRefusesDependencyCycle(t *testing.T) {
	_, err := compileYAML(t, header(`  templates:
    - name: ci
      dag:
        tasks:
          - name: a
            template: run
            dependencies: [b]
          - name: b
            template: run
            dependencies: [a]
    - name: run
      container:
        image: "golang:1.26`+digest+`"
        command: ["true"]
`))
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected a cycle refusal, got %v", err)
	}
}

func TestCompileRefusesPipelineWithNoSteps(t *testing.T) {
	_, err := compileYAML(t, header(`  templates:
    - name: ci
      dag:
        tasks: []
`))
	if err == nil {
		t.Fatal("expected an empty pipeline to be refused")
	}
}
