package session

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	continued := started.Add(time.Hour)
	metadata := Metadata{
		ID:              "project-20260903-193012-a82f",
		Repository:      "/source/project",
		Worktree:        "",
		Agent:           "codex",
		BaseBranch:      "main",
		BaseCommit:      "abcdef",
		SessionBranch:   "codegenbox/project-20260903-193012-a82f",
		ImportedCommit:  "123456",
		PostExitAction:  PostExitActionOpenCompare,
		StartedAt:       started,
		LastContinuedAt: &continued,
		ContinueCount:   2,
		State:           StateRunning,
	}
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	metadata.Worktree = filepath.Join(root, "sessions", metadata.ID)
	if err := os.MkdirAll(metadata.Worktree, 0o700); err != nil {
		t.Fatal(err)
	}
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
	if decoded.ID != metadata.ID || decoded.Repository != metadata.Repository || decoded.Worktree != metadata.Worktree || decoded.Agent != metadata.Agent || decoded.BaseBranch != metadata.BaseBranch || decoded.BaseCommit != metadata.BaseCommit || decoded.SessionBranch != metadata.SessionBranch || decoded.ImportedCommit != metadata.ImportedCommit || decoded.PostExitAction != metadata.PostExitAction || decoded.State != metadata.State {
		t.Fatalf("metadata = %#v, want %#v", decoded, metadata)
	}
	if !decoded.StartedAt.Equal(metadata.StartedAt) {
		t.Fatalf("StartedAt = %s, want %s", decoded.StartedAt, metadata.StartedAt)
	}
	if decoded.LastContinuedAt == nil || !decoded.LastContinuedAt.Equal(continued) || decoded.ContinueCount != 2 {
		t.Fatalf("continuation metadata = %#v", decoded)
	}
}

func TestOlderMetadataWithoutOptionalFieldsDecodesToZeroValues(t *testing.T) {
	var metadata Metadata
	if err := json.Unmarshal([]byte(`{"id":"project-20260903-193012-a82f","state":"completed"}`), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.LastContinuedAt != nil || metadata.ContinueCount != 0 || metadata.PostExitAction != PostExitActionNone {
		t.Fatalf("older metadata optional fields = %#v", metadata)
	}
}

func TestWriteMetadataRejectsUnknownPostExitAction(t *testing.T) {
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	id := "project-20260903-193012-a82f"
	workspace := filepath.Join(root, "sessions", id)
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	metadata := Metadata{ID: id, Repository: "/source", Worktree: workspace, Agent: "codex", SessionBranch: "codegenbox/" + id, PostExitAction: "anything", State: StateDirty}
	if err := WriteMetadata(root, metadata); err == nil {
		t.Fatal("WriteMetadata accepted an unknown post-exit action")
	}
}

func TestWriteMetadataRejectsPathTraversalID(t *testing.T) {
	if err := WriteMetadata(t.TempDir(), Metadata{ID: "../escape"}); err == nil {
		t.Fatal("WriteMetadata accepted a path-traversal ID")
	}
}

func TestLoadAndListMetadataAreStableAndRejectCorruption(t *testing.T) {
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"alpha-20260903-193012-a82f", "beta-20260903-193012-a82f"} {
		if err := os.MkdirAll(filepath.Join(root, "sessions", id), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := WriteMetadata(root, Metadata{ID: id, Repository: "/source", Worktree: filepath.Join(root, "sessions", id), Agent: "codex", BaseBranch: "main", BaseCommit: "abcdef", SessionBranch: "codegenbox/" + id, State: StateDirty}); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := ListMetadata(root)
	if err != nil || len(listed) != 2 || listed[0].ID != "alpha-20260903-193012-a82f" {
		t.Fatalf("ListMetadata = %#v, %v", listed, err)
	}
	if _, err := LoadMetadata(root, "../escape"); err == nil {
		t.Fatal("path traversal load accepted")
	}
	if err := os.WriteFile(MetadataPath(root, "beta-20260903-193012-a82f"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListMetadata(root); err == nil {
		t.Fatal("corrupt metadata was silently accepted")
	}
}

func TestMetadataDirectoryAndListingRejectEscapesBeforeWorkspaceProbe(t *testing.T) {
	root := t.TempDir()
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	id := "project-20260903-193012-a82f"
	if err := os.MkdirAll(filepath.Join(canonicalRoot, "sessions", id), 0o700); err != nil {
		t.Fatal(err)
	}
	valid := Metadata{ID: id, Repository: "/source", Worktree: filepath.Join(canonicalRoot, "sessions", id), Agent: "codex", BaseBranch: "main", BaseCommit: "abcdef", SessionBranch: "codegenbox/" + id, State: StateDirty}
	if err := WriteMetadata(canonicalRoot, valid); err != nil {
		t.Fatal(err)
	}
	if _, err := ListMetadata(canonicalRoot); err != nil {
		t.Fatalf("valid listing: %v", err)
	}
	metadataDir := filepath.Join(canonicalRoot, "metadata")
	if err := os.RemoveAll(metadataDir); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "external-metadata")
	if err := os.MkdirAll(external, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, metadataDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteMetadata(canonicalRoot, valid); err == nil {
		t.Fatal("metadata symlink escape accepted for write")
	}
	if _, err := ListMetadata(canonicalRoot); err == nil {
		t.Fatal("metadata symlink escape accepted for listing")
	}
	if _, err := LoadMetadata(canonicalRoot, id); err == nil {
		t.Fatal("metadata symlink escape accepted for load")
	}

	root = t.TempDir()
	canonicalRoot, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(canonicalRoot, "sessions", id), 0o700); err != nil {
		t.Fatal(err)
	}
	malicious := valid
	malicious.Worktree = filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(filepath.Join(canonicalRoot, "metadata"), 0o700); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(malicious)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(canonicalRoot, "metadata", id+".json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListMetadata(canonicalRoot); err == nil {
		t.Fatal("external workspace metadata was listed")
	}
}
