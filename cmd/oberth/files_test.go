package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestFilesRequiresASubcommand(t *testing.T) {
	var output bytes.Buffer
	if err := runFiles(context.Background(), nil, &output); err == nil {
		t.Fatal("files accepted no subcommand")
	} else if !strings.Contains(err.Error(), "files show") {
		t.Fatalf("error %v does not name the subcommand", err)
	}
}

func TestFilesRefusesAnUnknownSubcommand(t *testing.T) {
	var output bytes.Buffer
	if err := runFiles(context.Background(), []string{"list"}, &output); err == nil {
		t.Fatal("files list was accepted; it does not exist")
	}
}

func TestFilesShowRefusesAnUnpinnedReference(t *testing.T) {
	var output bytes.Buffer
	err := runFilesShow(context.Background(), []string{"--database", "/nonexistent", "tzmem:graph/repos.yml"}, &output)
	if err == nil {
		t.Fatal("files show accepted an unpinned reference")
	}
	// The reference is parsed before the database is opened, so this is the
	// grammar failing rather than the store being missing.
	if strings.Contains(err.Error(), "nonexistent") {
		t.Fatalf("the database was opened before the reference was validated: %v", err)
	}
}
