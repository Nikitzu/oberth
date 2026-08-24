package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oberthci/oberth/pkg/argoworkflow"
)

type stubBlobs struct {
	shas  map[string]string
	blobs map[string][]byte
	limit int
}

func (stub *stubBlobs) TagSHA(_ context.Context, input, tag string) (string, error) {
	sha, ok := stub.shas[input+"@"+tag]
	if !ok {
		return "", errors.New("tag " + tag + " not found in " + input)
	}
	return sha, nil
}

func (stub *stubBlobs) ReadBlob(_ context.Context, input, sha, file string, limit int) ([]byte, error) {
	stub.limit = limit
	body, ok := stub.blobs[input+":"+file]
	if !ok {
		return nil, errors.New("no such blob " + file)
	}
	if len(body) > limit {
		return nil, errors.New("blob exceeds the " + string(rune(limit)) + " byte limit")
	}
	return body, nil
}

type stubRegistry map[string]bool

func (stub stubRegistry) RepositoryRegistered(_ context.Context, name string) (bool, error) {
	return stub[name], nil
}

func testFileLoader(t *testing.T, allowlist ...string) (*GitFileLoader, *stubBlobs) {
	t.Helper()
	blobs := &stubBlobs{
		shas:  map[string]string{"tzmem@v1": strings.Repeat("a", 40)},
		blobs: map[string][]byte{"tzmem:graph/repos.yml": []byte("repos: []\n")},
	}
	loader, err := NewGitFileLoader(blobs, stubRegistry{"tzmem": true, "other": true}, allowlist)
	if err != nil {
		t.Fatalf("NewGitFileLoader: %v", err)
	}
	return loader, blobs
}

var testRef = argoworkflow.FileRef{Repo: "tzmem", Version: "v1", Path: "graph/repos.yml"}

func TestFileLoaderReadsAPinnedBlob(t *testing.T) {
	loader, blobs := testFileLoader(t)
	file, err := loader.Load(context.Background(), testRef)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if file.SHA != strings.Repeat("a", 40) {
		t.Fatalf("SHA = %q", file.SHA)
	}
	if string(file.Bytes) != "repos: []\n" {
		t.Fatalf("bytes = %q", file.Bytes)
	}
	if blobs.limit != argoworkflow.MaxFileBytes {
		t.Fatalf("read with limit %d, want MaxFileBytes %d", blobs.limit, argoworkflow.MaxFileBytes)
	}
}

func TestFileLoaderRefusesAnUnregisteredRepository(t *testing.T) {
	loader, _ := testFileLoader(t)
	_, err := loader.Load(context.Background(), argoworkflow.FileRef{
		Repo: "stranger", Version: "v1", Path: "graph/repos.yml",
	})
	if err == nil {
		t.Fatal("Load accepted a repository this server does not host")
	}
	if !strings.Contains(err.Error(), "stranger@v1:graph/repos.yml") {
		t.Fatalf("error %v does not name the reference", err)
	}
	if !strings.Contains(err.Error(), "does not host") {
		t.Fatalf("error %v does not say why", err)
	}
}

func TestFileLoaderRefusesARepositoryOutsideTheAllowlist(t *testing.T) {
	loader, _ := testFileLoader(t, "other")
	if _, err := loader.Load(context.Background(), testRef); err == nil {
		t.Fatal("Load ignored the allowlist")
	} else if !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("error %v does not name the allowlist", err)
	}
}

func TestFileLoaderRefusesAMissingTag(t *testing.T) {
	loader, _ := testFileLoader(t)
	_, err := loader.Load(context.Background(), argoworkflow.FileRef{
		Repo: "tzmem", Version: "v99", Path: "graph/repos.yml",
	})
	if err == nil || !strings.Contains(err.Error(), "v99") {
		t.Fatalf("Load on a missing tag = %v", err)
	}
}

const filesDocument = `
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

func TestLoadFilesResolvesTheDeclaration(t *testing.T) {
	loader, _ := testFileLoader(t)
	files, err := loadFiles(context.Background(), loader, []byte(filesDocument))
	if err != nil {
		t.Fatalf("loadFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("resolved %d files", len(files))
	}
	if string(files[testRef].Bytes) != "repos: []\n" {
		t.Fatalf("resolved %+v", files[testRef])
	}
}

// A server with no loader configured must refuse a document that declares a
// file, not run it without one. A pipeline reading a registry that is silently
// absent answers "no" to every entry it should answer "yes" to.
func TestLoadFilesRefusesWhenNoLoaderIsConfigured(t *testing.T) {
	_, err := loadFiles(context.Background(), nil, []byte(filesDocument))
	if err == nil {
		t.Fatal("loadFiles ran a declaration with no loader")
	}
	if !strings.Contains(err.Error(), "tzmem@v1:graph/repos.yml") {
		t.Fatalf("error %v does not name the reference", err)
	}
}

func TestLoadFilesIsEmptyWithoutADeclaration(t *testing.T) {
	files, err := loadFiles(context.Background(), nil, []byte(strings.Replace(
		filesDocument, "    oberth.ci/files: tzmem@v1:graph/repos.yml\n", "", 1)))
	if err != nil || files != nil {
		t.Fatalf("loadFiles on a document declaring none = %+v, %v", files, err)
	}
}
