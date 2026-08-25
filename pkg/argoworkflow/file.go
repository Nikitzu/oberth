package argoworkflow

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	wfv1 "github.com/argoproj/argo-workflows/v4/pkg/apis/workflow/v1alpha1"
)

// A pipeline's workspace holds exactly one repository: the one under test. A
// step that must read shared data -- a registry, a policy list, an identifier
// map -- has no way to reach it, and the three obvious answers are all worse
// than this one.
//
// Cloning it needs an uplink key, and sourcevolume.go states why that is
// unacceptable: an uplink key can promote to main, in every repository the
// server hosts. Vendoring it gives every consuming repository its own copy to
// drift. Fetching it at runtime reintroduces an unpinned, unauditable input to
// a build, which is the class of problem the admission gate exists to prevent.
//
// So the server reads it, exactly as it already reads fragments: one bounded
// blob from a registered repository at a pinned tag, on the server's side of
// the trust boundary, delivered to the pipeline as bytes on a read-only mount.
// The pipeline gains a file and no credential.
//
// A file dependency is pinned by tag for the same reason a fragment is. An
// input that can change under a build is an input that makes the build's
// result unattributable to its inputs.

const (
	// FilesAnnotation is where a repository declares its file dependencies,
	// and where the server writes the resolved lock on the submitted Workflow.
	//
	// One key rather than two, because Build refuses to submit a document
	// whose declaration it could not resolve. An unresolved declaration can
	// therefore never be mistaken for a lock -- there is no run in which one
	// exists.
	FilesAnnotation = "oberth.ci/files"

	// MaxFiles bounds how many dependencies one pipeline may declare. A
	// pipeline that needs more data than this wants a container image built
	// from it, not a build-time read of each piece.
	MaxFiles = 8

	// MaxFileBytes bounds one dependency, matching MaxSourceBytes. The git
	// cache's blob reader enforces the same ceiling on its side, so an
	// oversized file is refused before it is fully in memory.
	MaxFileBytes = MaxSourceBytes

	// MaxFilesTotalBytes bounds the set. The per-file limit alone would admit
	// eight megabytes, and the aggregate is the number that matters: it is
	// what crosses the exec stream into the seeding Pod.
	MaxFilesTotalBytes = 4 << 20
)

// ErrFileRefused is wrapped by every refusal in this file, so a caller can
// distinguish a rejected declaration from an I/O failure, and so a test can
// assert which guard fired rather than merely that something did.
var ErrFileRefused = errors.New("argoworkflow: file dependency refused")

// FileRef is a pinned reference to one file in another registered repository,
// written repository@version:path.
type FileRef struct {
	Repo    string `json:"repo"`
	Version string `json:"version"`
	Path    string `json:"path"`
}

func (ref FileRef) String() string { return ref.Repo + "@" + ref.Version + ":" + ref.Path }

// SeededFile is one resolved dependency: the commit it was read at, and the
// bytes read. The server fills these in; nothing a repository writes reaches
// this type.
type SeededFile struct {
	SHA   string
	Bytes []byte
}

// LockedFile is one entry in the audit record: what was asked for, which
// commit answered, and what the answer hashed to.
type LockedFile struct {
	Ref    FileRef `json:"ref"`
	SHA    string  `json:"sha"`
	Digest string  `json:"digest"`
}

// FileLock is the resolved set, in declaration order.
type FileLock []LockedFile

// ParseFileRef parses repository@version:path.
//
// The repository@version half is delegated to ParseFragmentRef rather than
// reimplemented: a file dependency and a fragment are pinned by the same rule,
// and two copies of that rule are two things to drift apart.
func ParseFileRef(reference string) (FileRef, error) {
	trimmed := strings.TrimSpace(reference)
	if trimmed == "" {
		return FileRef{}, fmt.Errorf("%w: empty reference", ErrFileRefused)
	}
	// Cut on the first colon, not the last. A version may not contain one and
	// a path may not either, so the first is the only separator -- and cutting
	// first is what makes "registry:graph/repos.yml" report a missing version
	// rather than a malformed path.
	pinned, path, found := strings.Cut(trimmed, ":")
	if !found {
		return FileRef{}, fmt.Errorf("%w: reference %q has no :path; a file dependency names one file",
			ErrFileRefused, reference)
	}
	key, err := ParseFragmentRef(pinned)
	if err != nil {
		return FileRef{}, fmt.Errorf("%w: reference %q: %s", ErrFileRefused, reference, err)
	}
	if err := ValidateFilePath(path); err != nil {
		return FileRef{}, fmt.Errorf("%w: reference %q: %s", ErrFileRefused, reference, err)
	}
	return FileRef{Repo: key.Repo, Version: key.Version, Path: path}, nil
}

// ValidateFilePath states the path grammar a file dependency may use.
//
// It is exported because internal/gitcache enforces the same grammar on its
// own side and pkg may not import internal. The agreement between the two is
// asserted by a test in internal/gitcache, which can see both.
func ValidateFilePath(path string) error {
	switch {
	case path == "":
		return errors.New("path is empty")
	case path != strings.TrimSpace(path):
		return fmt.Errorf("path %q has surrounding whitespace", path)
	case strings.ContainsAny(path, "\x00\r\n\t\\"):
		return fmt.Errorf("path %q contains forbidden characters", path)
	case strings.Contains(path, ","):
		// A comma separates declarations, so a path containing one would be
		// split before it was ever parsed and refused as two malformed
		// references. Refusing it here is what makes the error name the path
		// the author actually wrote.
		return fmt.Errorf("path %q contains a comma, which separates declarations", path)
	case strings.HasPrefix(path, "-"):
		return fmt.Errorf("path %q looks like an option", path)
	case strings.HasPrefix(path, "/"):
		return fmt.Errorf("path %q must be relative to the repository root", path)
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("path %q must not contain %q", path, segment)
		}
	}
	return nil
}

// FileRefs reads the declared dependencies from the document's annotation, in
// declaration order, with duplicates collapsed.
//
// Entries separate on newlines or commas because a YAML block scalar is the
// natural way to write several and a flow string is the natural way to write
// one, and refusing either would be a syntax trap with no security value.
func FileRefs(workflow *wfv1.Workflow) ([]FileRef, error) {
	if workflow == nil {
		return nil, nil
	}
	declared := strings.TrimSpace(workflow.Annotations[FilesAnnotation])
	if declared == "" {
		return nil, nil
	}
	var refs []FileRef
	seen := map[FileRef]bool{}
	for _, entry := range strings.FieldsFunc(declared, func(r rune) bool { return r == '\n' || r == ',' }) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		ref, err := ParseFileRef(entry)
		if err != nil {
			return nil, err
		}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	if len(refs) > MaxFiles {
		return nil, fmt.Errorf("%w: document declares %d file dependencies, the limit is %d",
			ErrFileRefused, len(refs), MaxFiles)
	}
	return refs, nil
}

// ResolveFiles pairs every declared reference with the bytes the server loaded
// for it and returns the lock.
//
// It judges the whole set before returning any of it, and it does not touch
// the workflow. Both properties matter: a partial answer would let a build
// proceed on some of the data it asked for, and stamping the annotation is
// Build's decision, made once the document is otherwise final.
func ResolveFiles(workflow *wfv1.Workflow, loaded map[FileRef]SeededFile) (FileLock, error) {
	refs, err := FileRefs(workflow)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, nil
	}
	total := 0
	lock := make(FileLock, 0, len(refs))
	for _, ref := range refs {
		file, ok := loaded[ref]
		if !ok {
			return nil, fmt.Errorf("%w: %s was not loaded", ErrFileRefused, ref)
		}
		if len(file.Bytes) > MaxFileBytes {
			return nil, fmt.Errorf("%w: %s is %d bytes, over the %d byte limit for one file",
				ErrFileRefused, ref, len(file.Bytes), MaxFileBytes)
		}
		total += len(file.Bytes)
		if total > MaxFilesTotalBytes {
			return nil, fmt.Errorf("%w: the declared files total more than the %d byte limit",
				ErrFileRefused, MaxFilesTotalBytes)
		}
		digest := sha256.Sum256(file.Bytes)
		lock = append(lock, LockedFile{Ref: ref, SHA: file.SHA, Digest: hex.EncodeToString(digest[:])})
	}
	return lock, nil
}
