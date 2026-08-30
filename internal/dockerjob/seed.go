package dockerjob

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// seedTree streams the run volume's initial contents into a created container
// as a tar built here, rather than handing docker cp a host directory.
//
// The reason is ownership. docker cp from a host path preserves the host's
// numeric uid and gid, so on a laptop the whole /work tree arrives owned by
// uid 501. A step runs as root with every Linux capability dropped, which
// means no CAP_DAC_OVERRIDE, which means root does NOT bypass file
// permissions: a 0600 file owned by 501 is unreadable and a directory owned
// by 501 is unwritable. The Argo path never meets this because a PVC and its
// seeded contents are root-owned already.
//
// Building the archive here fixes ownership at 0:0 and normalises modes, so
// the tree the step sees is the tree the cluster would have given it. It also
// removes the host user from the picture entirely, which is one less thing
// that differs between two machines running the same pipeline.
func seedTree(ctx context.Context, binary, container, destination string, entries func(*tar.Writer) error) error {
	command := exec.CommandContext(ctx, binary, "cp", "-", container+":"+destination)
	stdin, err := command.StdinPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return err
	}
	writer := tar.NewWriter(stdin)
	writeErr := entries(writer)
	closeErr := writer.Close()
	stdinErr := stdin.Close()
	waitErr := command.Wait()
	if waitErr != nil {
		waitErr = fmt.Errorf("docker cp - %s:%s: %w: %s", container, destination, waitErr, strings.TrimSpace(stderr.String()))
	}
	return errors.Join(writeErr, closeErr, stdinErr, waitErr)
}

// rootOwned stamps an entry as owned by root with no host identity attached.
func rootOwned(header *tar.Header) {
	header.Uid, header.Gid = 0, 0
	header.Uname, header.Gname = "", ""
}

// tarDirectory adds one root-owned directory.
func tarDirectory(writer *tar.Writer, name string) error {
	header := &tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o755}
	rootOwned(header)
	return writer.WriteHeader(header)
}

// maxSeedDepth bounds how deep a checkout's directory tree may go
// before the walk gives up. A pathological tree is a resource question, not a
// correctness one, but an unbounded walk is still a way to hang a run.
const maxSeedDepth = 64

// tarSourceTree writes a checked-out working tree into the archive under
// prefix, root-owned, with modes normalised to 0755 for directories and
// executables and 0644 for everything else.
//
// Symlinks are carried as symlinks so a repository that uses them behaves as
// it does on a cluster; the extraction target is the run volume, which the
// step already owns, so a link escaping the tree grants nothing it could not
// reach by writing the same path itself. Anything that is not a regular file,
// directory, or symlink is skipped: a device node or a socket in a checkout is
// not something a pipeline can have meant.
func tarSourceTree(writer *tar.Writer, root, prefix string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if strings.Count(relative, string(filepath.Separator)) > maxSeedDepth {
			return fmt.Errorf("dockerjob: checkout nests deeper than %d directories at %s", maxSeedDepth, relative)
		}
		name := prefix + "/" + filepath.ToSlash(relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			return tarDirectory(writer, name)
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			header := &tar.Header{Typeflag: tar.TypeSymlink, Name: name, Linkname: target, Mode: 0o777}
			rootOwned(header)
			return writer.WriteHeader(header)
		case info.Mode().IsRegular():
			mode := int64(0o644)
			if info.Mode()&0o111 != 0 {
				mode = 0o755
			}
			header := &tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: mode, Size: info.Size()}
			rootOwned(header)
			if err := writer.WriteHeader(header); err != nil {
				return err
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer func() { _ = file.Close() }()
			copied, err := io.Copy(writer, file)
			if err != nil {
				return err
			}
			if copied != info.Size() {
				return fmt.Errorf("dockerjob: %s changed size while it was being copied into the run volume", relative)
			}
			return nil
		default:
			return nil
		}
	})
}
