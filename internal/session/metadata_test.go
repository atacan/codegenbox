package session

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
	"time"
)

func TestNewIDIsReadableAndBranchSafe(t *testing.T) {
	id, err := NewID("/tmp/My Project!", time.Date(2026, 9, 3, 19, 30, 12, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if !regexp.MustCompile(`^my-project-20260903-193012-[0-9a-f]{4}$`).MatchString(id) {
		t.Fatalf("unexpected ID %q", id)
	}
	if err := validateID(id); err != nil {
		t.Fatalf("validateID(%q): %v", id, err)
	}
	if err := validateID("../escape"); err == nil {
		t.Fatal("path-traversal-like ID was accepted")
	}
}

func TestWriteMetadataRoundTripsRequiredFields(t *testing.T) {
	started := time.Date(2026, 9, 3, 19, 30, 12, 0, time.FixedZone("CEST", 2*60*60))
	metadata := Metadata{
		ID:             "project-20260903-193012-a82f",
		Repository:     "/source/project",
		Worktree:       "/state/worktrees/project-20260903-193012-a82f",
		Agent:          "codex",
		BaseBranch:     "main",
		BaseCommit:     "abcdef",
		SessionBranch:  "codegenbox/project-20260903-193012-a82f",
		ImportedCommit: "123456",
		StartedAt:      started,
		State:          StateRunning,
	}
	root := t.TempDir()
	if err := WriteMetadata(root, metadata); err != nil {
		t.Fatalf("WriteMetadata: %v", err)
	}
	payload, err := os.ReadFile(MetadataPath(root, metadata.ID))
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	var decoded Metadata
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if decoded.ID != metadata.ID || decoded.Repository != metadata.Repository || decoded.Worktree != metadata.Worktree || decoded.Agent != metadata.Agent || decoded.BaseBranch != metadata.BaseBranch || decoded.BaseCommit != metadata.BaseCommit || decoded.SessionBranch != metadata.SessionBranch || decoded.ImportedCommit != metadata.ImportedCommit || decoded.State != metadata.State {
		t.Fatalf("metadata = %#v, want %#v", decoded, metadata)
	}
	if !decoded.StartedAt.Equal(metadata.StartedAt) {
		t.Fatalf("StartedAt = %s, want %s", decoded.StartedAt, metadata.StartedAt)
	}
}

func TestWriteMetadataRejectsPathTraversalID(t *testing.T) {
	if err := WriteMetadata(t.TempDir(), Metadata{ID: "../escape"}); err == nil {
		t.Fatal("WriteMetadata accepted a path-traversal ID")
	}
}
