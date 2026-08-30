package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeEnginePipeline(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".oberth"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".oberth", "build.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return root
}

const validateDigest = "@sha256:66bb8d36ae1ddd72199ed235a089904874ca4079ee517936ca3adb80506a75c1"

func supportedPipeline() string {
	return `apiVersion: argoproj.io/v1alpha1
kind: Workflow
spec:
  entrypoint: ci
  activeDeadlineSeconds: 600
  templates:
    - name: ci
      dag:
        tasks:
          - name: unit
            template: unit
    - name: unit
      container:
        image: "golang:1.26` + validateDigest + `"
        command: ["sh", "-c"]
        args: ["go test ./..."]
`
}

func TestValidateDockerEngineAcceptsASupportedPipeline(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	root := writeEnginePipeline(t, supportedPipeline())
	if err := runValidate(context.Background(), []string{"--engine=docker", "--runner-image-prefixes=golang:", root}, &output); err != nil {
		t.Fatalf("validate: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "ok  docker engine execution subset") {
		t.Fatalf("the docker subset check did not run:\n%s", output.String())
	}
}

// The point of the flag: an author finds out here rather than from a run that
// never started a container, and the message is the compiler's own.
func TestValidateDockerEngineRefusesAnUnsupportedConstruct(t *testing.T) {
	t.Parallel()
	document := strings.Replace(supportedPipeline(),
		"            template: unit\n",
		"            template: unit\n            withItems: [\"a\", \"b\"]\n", 1)
	root := writeEnginePipeline(t, document)

	// The default engine admits it, because admission is not the question.
	var admitted bytes.Buffer
	if err := runValidate(context.Background(), []string{"--runner-image-prefixes=golang:", root}, &admitted); err != nil {
		t.Fatalf("the argo engine refused a document it admits: %v\n%s", err, admitted.String())
	}

	var output bytes.Buffer
	err := runValidate(context.Background(), []string{"--engine=docker", "--runner-image-prefixes=golang:", root}, &output)
	if err == nil {
		t.Fatalf("an unsupported construct passed --engine=docker:\n%s", output.String())
	}
	text := output.String()
	for _, expected := range []string{"engine docker", "withItems", "runs on the Argo engine but not on --engine=docker"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("output missing %q:\n%s", expected, text)
		}
	}
	if !strings.Contains(text, "result: FAIL") {
		t.Fatalf("a refused document did not fail:\n%s", text)
	}
}

func TestValidateRejectsAnUnknownEngine(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	err := runValidate(context.Background(), []string{"--engine=podman", writeEnginePipeline(t, supportedPipeline())}, &output)
	if !errors.Is(err, errUsage) {
		t.Fatalf("unknown engine accepted: %v", err)
	}
}

// Sequential execution is the one difference this engine does not refuse, so
// it has to be visible without running anything.
func TestValidateDockerEngineStatesTheExecutionOrder(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	root := writeEnginePipeline(t, supportedPipeline())
	if err := runValidate(context.Background(), []string{"--engine=docker", "--runner-image-prefixes=golang:", root}, &output); err != nil {
		t.Fatalf("validate: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "do not run concurrently") {
		t.Fatalf("the execution order is not stated:\n%s", output.String())
	}
}
