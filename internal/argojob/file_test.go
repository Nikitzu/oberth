package argojob

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	wfv1 "github.com/argoproj/argo-workflows/v4/pkg/apis/workflow/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/oberthci/oberth/pkg/argoworkflow"
	"github.com/oberthci/oberth/pkg/periapsis"
)

const fileDependencyDocument = `
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  annotations:
    oberth.ci/files: tzmem@v1:graph/repos.yml
spec:
  entrypoint: main
  activeDeadlineSeconds: 999999
  templates:
    - name: main
      container:
        image: golang:1.26-alpine@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
        command: [/bin/true]
`

var registryRef = argoworkflow.FileRef{Repo: "tzmem", Version: "v1", Path: "graph/repos.yml"}

func requestWithFiles(source string) Request {
	request := testRequest(periapsis.TriggerCI, source)
	request.Files = map[argoworkflow.FileRef]argoworkflow.SeededFile{
		registryRef: {SHA: strings.Repeat("a", 40), Bytes: []byte("repos: []\n")},
	}
	return request
}

func TestBuildRefusesADeclarationItCouldNotResolve(t *testing.T) {
	// The single-annotation design is only safe because of this: there is no
	// run in which an unresolved declaration reaches the cluster, so a reader
	// of oberth.ci/files on a submitted Workflow is always reading a lock.
	request := testRequest(periapsis.TriggerCI, fileDependencyDocument)
	_, err := Build(testConfig(), request)
	if err == nil {
		t.Fatal("Build submitted a document whose file dependency was never loaded")
	}
	if !strings.Contains(err.Error(), "tzmem@v1:graph/repos.yml") {
		t.Fatalf("error %v does not name the reference", err)
	}
}

func TestBuildStampsTheResolvedLock(t *testing.T) {
	workflow, err := Build(testConfig(), requestWithFiles(fileDependencyDocument))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	raw := workflow.Annotations[argoworkflow.FilesAnnotation]
	if raw == "tzmem@v1:graph/repos.yml" {
		t.Fatal("the annotation still holds the declaration; the lock never replaced it")
	}
	var lock argoworkflow.FileLock
	if err := json.Unmarshal([]byte(raw), &lock); err != nil {
		t.Fatalf("annotation %q is not a lock: %v", raw, err)
	}
	if len(lock) != 1 || lock[0].Ref != registryRef {
		t.Fatalf("lock %+v", lock)
	}
	// The digest must be of the delivered bytes, not of the reference. That is
	// what makes "this run read the file that hashed to X" checkable against a
	// sha256sum taken inside the step.
	digest := sha256.Sum256([]byte("repos: []\n"))
	if lock[0].Digest != hex.EncodeToString(digest[:]) {
		t.Fatalf("digest %q is not the digest of the delivered bytes", lock[0].Digest)
	}
	if lock[0].SHA != strings.Repeat("a", 40) {
		t.Fatalf("lock entry %+v carries no commit provenance", lock[0])
	}
}

func TestBuildMountsFilesOutsideTheCheckout(t *testing.T) {
	request := requestWithFiles(fileDependencyDocument)
	request.SourceVolume = SourceVolume{ClaimName: "claim", SubPath: "src", FilesSubPath: "files"}
	workflow, err := Build(testConfig(), request)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var mount *corev1.VolumeMount
	for index := range workflow.Spec.Templates {
		container := workflow.Spec.Templates[index].Container
		if container == nil {
			continue
		}
		for position := range container.VolumeMounts {
			if container.VolumeMounts[position].MountPath == FilesMountPath {
				mount = &container.VolumeMounts[position]
			}
		}
	}
	if mount == nil {
		t.Fatalf("no container mounts %s", FilesMountPath)
	}
	if !mount.ReadOnly {
		t.Fatal("the files mount is writable; a pipeline could rewrite the data it was pinned to")
	}
	if mount.SubPath != "files" {
		t.Fatalf("files mount subPath = %q", mount.SubPath)
	}
	// A repository must not be able to shadow a delivered file with one of its
	// own, which is why this lives outside the checkout rather than inside it.
	if strings.HasPrefix(FilesMountPath, SourceMountPath) {
		t.Fatalf("%s is inside %s, so a repository can shadow a delivered file",
			FilesMountPath, SourceMountPath)
	}
}

// TestFilesEnvironmentDoesNotDependOnSeeding is the regression test for the
// trap this sub-project was planned around.
//
// sameSubmission compares a Workflow built with the real seeded volume against
// one built with a zero volume. Anything keyed off the seeded volume differs
// structurally between those paths, which is why VAULT_CACERT has to be
// stripped by name in normalizeSourceVolume. OBERTH_FILES is gated on
// request.Files instead, which is carried on the Request and identical on both
// paths.
//
// Gating it on the volume passes every other test in this file and then fails
// every retry of every run that declares a file.
func TestFilesEnvironmentDoesNotDependOnSeeding(t *testing.T) {
	seeded := requestWithFiles(fileDependencyDocument)
	seeded.SourceVolume = SourceVolume{ClaimName: "claim", SubPath: "src", FilesSubPath: "files"}

	withVolume, err := Build(testConfig(), seeded)
	if err != nil {
		t.Fatalf("build with a seeded volume: %v", err)
	}
	withoutVolume, err := Build(testConfig(), requestWithFiles(fileDependencyDocument))
	if err != nil {
		t.Fatalf("build without a seeded volume: %v", err)
	}

	if got := filesEnvValue(withoutVolume); got != FilesMountPath {
		t.Fatalf("OBERTH_FILES on the retry path = %q, want %q; it is gated on the seeded volume",
			got, FilesMountPath)
	}
	if got := filesEnvValue(withVolume); got != FilesMountPath {
		t.Fatalf("OBERTH_FILES on the create path = %q", got)
	}

	_, seededIdentity, _ := WorkflowMeta(withVolume)
	_, retryIdentity, _ := WorkflowMeta(withoutVolume)
	if seededIdentity != retryIdentity {
		t.Fatalf("spec identity differs between the create and retry paths (%s vs %s); "+
			"every retry of a run declaring a file would be refused as a different spec",
			seededIdentity, retryIdentity)
	}
}

func TestNoFilesMeansNoEnvironmentVariable(t *testing.T) {
	workflow, err := Build(testConfig(), testRequest(periapsis.TriggerCI, greedyDocument))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := filesEnvValue(workflow); got != "" {
		t.Fatalf("OBERTH_FILES = %q on a run that declared none", got)
	}
}

func filesEnvValue(workflow *wfv1.Workflow) string {
	for index := range workflow.Spec.Templates {
		container := workflow.Spec.Templates[index].Container
		if container == nil {
			continue
		}
		for _, variable := range container.Env {
			if variable.Name == "OBERTH_FILES" {
				return variable.Value
			}
		}
	}
	return ""
}

// TestSeedWritesFileDependenciesIntoTheClaim proves the bytes reach the claim
// in the layout the mount expects, and that the archive carries no "./" member.
//
// That last clause is not hypothetical. `tar -czf - -C dir .` emits "./" as its
// first member, which broke every artifact collection in this programme until
// it was found. Building the archive in Go rather than shelling out is what
// removes the whole class, and this asserts it stayed removed.
func TestSeedWritesFileDependenciesIntoTheClaim(t *testing.T) {
	t.Parallel()
	client := fake.NewClientset()
	runningSeedPods(client)

	var calls []seedExecCall
	seeder := NewSourceSeeder(client, recordingExec(&calls), seedTestConfig())
	files := map[argoworkflow.FileRef]argoworkflow.SeededFile{
		registryRef: {SHA: strings.Repeat("a", 40), Bytes: []byte("repos: []\n")},
		{Repo: "policy", Version: "v3", Path: "ci/images.txt"}: {
			SHA: strings.Repeat("b", 40), Bytes: []byte("golang\n"),
		},
	}

	volume, err := seeder.Seed(context.Background(), "oberth-oberth-abc-def", writeCheckout(t), false, files)
	if err != nil {
		t.Fatal(err)
	}
	if volume.FilesSubPath != "files" {
		t.Fatalf("FilesSubPath = %q", volume.FilesSubPath)
	}

	var filesCall *seedExecCall
	for index := range calls {
		if strings.Contains(strings.Join(calls[index].command, " "), "/seed/files") {
			filesCall = &calls[index]
		}
	}
	if filesCall == nil {
		t.Fatalf("no exec targeted the files subPath; commands were %v", calls)
	}

	members := map[string]string{}
	stream, err := gzip.NewReader(strings.NewReader(filesCall.stdin))
	if err != nil {
		t.Fatalf("the files stream is not gzipped: %v", err)
	}
	archive := tar.NewReader(stream)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read the files archive: %v", err)
		}
		if header.Name == "./" || header.Name == "." {
			t.Fatal(`the archive carries a "./" member, the bug that broke artifact collection`)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		body, err := io.ReadAll(archive)
		if err != nil {
			t.Fatalf("read member %s: %v", header.Name, err)
		}
		members[header.Name] = string(body)
	}

	if got := members["tzmem/graph/repos.yml"]; got != "repos: []\n" {
		t.Fatalf("tzmem/graph/repos.yml = %q", got)
	}
	if got := members["policy/ci/images.txt"]; got != "golang\n" {
		t.Fatalf("policy/ci/images.txt = %q", got)
	}
	if len(members) != 2 {
		t.Fatalf("archive carries %d files, want 2: %v", len(members), members)
	}
}

func TestSeedWritesNoFilesDirectoryWhenNoneWereDeclared(t *testing.T) {
	t.Parallel()
	client := fake.NewClientset()
	runningSeedPods(client)

	var calls []seedExecCall
	seeder := NewSourceSeeder(client, recordingExec(&calls), seedTestConfig())
	volume, err := seeder.Seed(context.Background(), "oberth-oberth-abc-def", writeCheckout(t), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if volume.FilesSubPath != "" {
		t.Fatalf("FilesSubPath = %q on a run that declared no files", volume.FilesSubPath)
	}
}

// A run whose file dependencies could not be written must not proceed. A
// pipeline reading a registry that is silently absent answers "no" to every
// entry, which is worse than failing.
func TestSeedFailsWhenFileDependenciesCannotBeWritten(t *testing.T) {
	t.Parallel()
	client := fake.NewClientset()
	runningSeedPods(client)

	exec := func(_ context.Context, _, _, _ string, command []string,
		stdin io.Reader, _, _ io.Writer,
	) error {
		if stdin != nil {
			_, _ = io.Copy(io.Discard, stdin)
		}
		if strings.Contains(strings.Join(command, " "), "/seed/files") {
			return errors.New("no space left on device")
		}
		return nil
	}
	seeder := NewSourceSeeder(client, exec, seedTestConfig())
	files := map[argoworkflow.FileRef]argoworkflow.SeededFile{
		registryRef: {SHA: strings.Repeat("a", 40), Bytes: []byte("repos: []\n")},
	}
	if _, err := seeder.Seed(context.Background(), "oberth-oberth-abc-def", writeCheckout(t), false, files); err == nil {
		t.Fatal("Seed succeeded despite failing to write the declared files")
	}
	claims, err := client.CoreV1().PersistentVolumeClaims(seedTestConfig().Namespace).
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(claims.Items) != 0 {
		t.Fatalf("a failed seeding left %d claims behind", len(claims.Items))
	}
}
