package argoworkflow

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	wfv1 "github.com/argoproj/argo-workflows/v4/pkg/apis/workflow/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func annotated(value string) *wfv1.Workflow {
	return &wfv1.Workflow{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{FilesAnnotation: value}},
	}
}

func TestParseFileRefAcceptsAPinnedReference(t *testing.T) {
	ref, err := ParseFileRef("depgraph@v1:graph/repos.yml")
	if err != nil {
		t.Fatalf("ParseFileRef: %v", err)
	}
	if ref.Repo != "depgraph" || ref.Version != "v1" || ref.Path != "graph/repos.yml" {
		t.Fatalf("parsed %+v", ref)
	}
	if got := ref.String(); got != "depgraph@v1:graph/repos.yml" {
		t.Fatalf("String() = %q", got)
	}
}

func TestParseFileRefRefusesMalformedReferences(t *testing.T) {
	// Every case must be refused with ErrFileRefused. Asserting err != nil is
	// how four of fifteen refusal cases in the artifacts work passed with
	// their guards deleted.
	cases := map[string]string{
		"empty":            "",
		"no version":       "depgraph:graph/repos.yml",
		"no path":          "depgraph@v1",
		"empty path":       "depgraph@v1:",
		"empty repo":       "@v1:graph/repos.yml",
		"empty version":    "depgraph@:graph/repos.yml",
		"two at signs":     "depgraph@v1@v2:graph/repos.yml",
		"parent escape":    "depgraph@v1:../etc/passwd",
		"parent mid path":  "depgraph@v1:graph/../../etc/passwd",
		"absolute path":    "depgraph@v1:/etc/passwd",
		"option shaped":    "depgraph@v1:--upload-pack=x",
		"backslash":        `depgraph@v1:graph\repos.yml`,
		"newline":          "depgraph@v1:graph/repos\n.yml",
		"nul byte":         "depgraph@v1:graph/repos\x00.yml",
		"comma in path":    "depgraph@v1:graph,repos.yml",
		"dot segment":      "depgraph@v1:./repos.yml",
		"double slash":     "depgraph@v1:graph//repos.yml",
		"trailing slash":   "depgraph@v1:graph/",
		"version is path":  "depgraph@v1/v2:graph/repos.yml",
		"repo is absolute": "/depgraph@v1:graph/repos.yml",
	}
	for name, ref := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseFileRef(ref)
			if err == nil {
				t.Fatalf("ParseFileRef(%q) accepted, giving %+v", ref, got)
			}
			if !errors.Is(err, ErrFileRefused) {
				t.Fatalf("ParseFileRef(%q) error %v does not wrap ErrFileRefused", ref, err)
			}
		})
	}
}

func TestFileRefsReadsTheAnnotationAndDeduplicates(t *testing.T) {
	refs, err := FileRefs(annotated("depgraph@v1:graph/repos.yml\npolicy@v3:ci/images.txt, depgraph@v1:graph/repos.yml\n\n"))
	if err != nil {
		t.Fatalf("FileRefs: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("got %d refs, want 2: %+v", len(refs), refs)
	}
	if refs[0].String() != "depgraph@v1:graph/repos.yml" || refs[1].String() != "policy@v3:ci/images.txt" {
		t.Fatalf("refs %+v are not in declaration order", refs)
	}
}

func TestFileRefsIsEmptyWithoutTheAnnotation(t *testing.T) {
	refs, err := FileRefs(&wfv1.Workflow{})
	if err != nil || len(refs) != 0 {
		t.Fatalf("FileRefs on a bare workflow = %+v, %v", refs, err)
	}
}

func TestFileRefsRefusesMoreThanTheLimit(t *testing.T) {
	var declared []string
	for index := 0; index <= MaxFiles; index++ {
		declared = append(declared, "repo@v1:file"+string(rune('a'+index))+".txt")
	}
	_, err := FileRefs(annotated(strings.Join(declared, "\n")))
	if err == nil {
		t.Fatal("FileRefs accepted more than MaxFiles")
	}
	if !errors.Is(err, ErrFileRefused) {
		t.Fatalf("error %v does not wrap ErrFileRefused", err)
	}
	if !strings.Contains(err.Error(), "8") {
		t.Fatalf("error %v does not name the limit", err)
	}
}

func TestResolveFilesRefusesADeclarationWithNoBytes(t *testing.T) {
	_, err := ResolveFiles(annotated("depgraph@v1:graph/repos.yml"), nil)
	if err == nil {
		t.Fatal("ResolveFiles accepted an unloaded declaration")
	}
	if !errors.Is(err, ErrFileRefused) {
		t.Fatalf("error %v does not wrap ErrFileRefused", err)
	}
	if !strings.Contains(err.Error(), "depgraph@v1:graph/repos.yml") {
		t.Fatalf("error %v does not name the reference", err)
	}
}

func TestResolveFilesRefusesAnOversizedAggregate(t *testing.T) {
	// Each file is within the per-file limit, so only the aggregate guard can
	// refuse this. That is the point: a test that trips the per-file limit
	// first proves nothing about the aggregate.
	var declared []string
	loaded := map[FileRef]SeededFile{}
	atLimit := bytes.Repeat([]byte{'x'}, MaxFileBytes)
	for index := 0; index < 5; index++ {
		ref := FileRef{Repo: "a", Version: "v1", Path: fmt.Sprintf("file%d.bin", index)}
		declared = append(declared, ref.String())
		loaded[ref] = SeededFile{SHA: strings.Repeat("a", 40), Bytes: atLimit}
	}
	workflow := annotated(strings.Join(declared, "\n"))
	_, err := ResolveFiles(workflow, loaded)
	if err == nil {
		t.Fatal("ResolveFiles accepted an aggregate over the limit")
	}
	if !errors.Is(err, ErrFileRefused) {
		t.Fatalf("error %v does not wrap ErrFileRefused", err)
	}
	if !strings.Contains(err.Error(), "4194304") {
		t.Fatalf("error %v does not name the aggregate limit", err)
	}
}

func TestResolveFilesRefusesAnOversizedSingleFile(t *testing.T) {
	workflow := annotated("a@v1:one.bin")
	loaded := map[FileRef]SeededFile{
		{Repo: "a", Version: "v1", Path: "one.bin"}: {
			SHA:   strings.Repeat("a", 40),
			Bytes: bytes.Repeat([]byte{'x'}, MaxFileBytes+1),
		},
	}
	if _, err := ResolveFiles(workflow, loaded); !errors.Is(err, ErrFileRefused) {
		t.Fatalf("ResolveFiles on an oversized file = %v", err)
	}
}

func TestResolveFilesProducesALockInDeclarationOrder(t *testing.T) {
	workflow := annotated("b@v2:two.txt\na@v1:one.txt")
	loaded := map[FileRef]SeededFile{
		{Repo: "a", Version: "v1", Path: "one.txt"}: {SHA: strings.Repeat("a", 40), Bytes: []byte("one")},
		{Repo: "b", Version: "v2", Path: "two.txt"}: {SHA: strings.Repeat("b", 40), Bytes: []byte("two")},
	}
	lock, err := ResolveFiles(workflow, loaded)
	if err != nil {
		t.Fatalf("ResolveFiles: %v", err)
	}
	if len(lock) != 2 {
		t.Fatalf("lock has %d entries", len(lock))
	}
	if lock[0].Ref.Repo != "b" || lock[1].Ref.Repo != "a" {
		t.Fatalf("lock %+v is not in declaration order", lock)
	}
	// sha256("one")
	const oneDigest = "7692c3ad3540bb803c020b3aee66cd8887123234ea0c6e7143c0add73ff431ed"
	if lock[1].Digest != oneDigest {
		t.Fatalf("digest %q, want %q", lock[1].Digest, oneDigest)
	}
}

func TestResolveFilesIsEmptyWithoutADeclaration(t *testing.T) {
	lock, err := ResolveFiles(&wfv1.Workflow{}, nil)
	if err != nil || lock != nil {
		t.Fatalf("ResolveFiles on a bare workflow = %+v, %v", lock, err)
	}
}

func TestResolveFilesDoesNotMutateTheWorkflow(t *testing.T) {
	workflow := annotated("a@v1:one.txt")
	loaded := map[FileRef]SeededFile{
		{Repo: "a", Version: "v1", Path: "one.txt"}: {SHA: strings.Repeat("a", 40), Bytes: []byte("one")},
	}
	if _, err := ResolveFiles(workflow, loaded); err != nil {
		t.Fatalf("ResolveFiles: %v", err)
	}
	if got := workflow.Annotations[FilesAnnotation]; got != "a@v1:one.txt" {
		t.Fatalf("ResolveFiles rewrote the annotation to %q; stamping is Build's job", got)
	}
}

func TestParseFileRefExplainsWhyItRefused(t *testing.T) {
	// Several guards overlap: "/etc/passwd" is caught by the empty-segment
	// rule even with the absolute-path branch deleted. Those branches survive
	// for their wording, so the wording is what the test pins. Without this,
	// deleting them changes nothing a test can see.
	cases := []struct{ ref, want string }{
		{"depgraph@v1:/etc/passwd", "must be relative to the repository root"},
		{"depgraph@v1:--upload-pack=x", "looks like an option"},
		{"depgraph@v1:../etc/passwd", `must not contain ".."`},
		{"depgraph@v1:graph,repos.yml", "separates declarations"},
		{`depgraph@v1:graph\repos.yml`, "contains forbidden characters"},
		{"depgraph:graph/repos.yml", "has no @version"},
		{"depgraph@v1", "has no :path"},
	}
	for _, testCase := range cases {
		t.Run(testCase.ref, func(t *testing.T) {
			_, err := ParseFileRef(testCase.ref)
			if err == nil {
				t.Fatalf("ParseFileRef(%q) accepted", testCase.ref)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("ParseFileRef(%q) said %q, want it to mention %q", testCase.ref, err, testCase.want)
			}
		})
	}
}
