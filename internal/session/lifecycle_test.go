package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codegenbox/codegenbox/internal/agent"
	"github.com/codegenbox/codegenbox/internal/container"
)

type runnerFunc func(context.Context, container.Invocation) error

func (function runnerFunc) Run(ctx context.Context, invocation container.Invocation) error {
	return function(ctx, invocation)
}

func TestLifecycleCommitSurvivesCleanWorktreeRemoval(t *testing.T) {
	repository := newRepository(t)
	mainBefore := runGit(t, repository, "rev-parse", "main")
	dataRoot := filepath.Join(t.TempDir(), "codegenbox-state")
	adapter, err := agent.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}

	manager := testManager(dataRoot, runnerFunc(func(_ context.Context, invocation container.Invocation) error {
		worktree := invocationWorktree(t, invocation)
		writeFile(t, filepath.Join(worktree, "committed.txt"), "made in session\n")
		runGit(t, worktree, "add", "committed.txt")
		runGit(t, worktree, "commit", "-m", "session commit")
		return nil
	}))

	nested := filepath.Join(repository, "nested", "directory")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Start(context.Background(), nested, adapter, "node:22-bookworm", "docker")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if result.Metadata.State != StateCompleted || !result.WorkspaceRemoved {
		t.Fatalf("result = %#v, want completed and removed", result)
	}
	canonicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	if result.Metadata.SessionBranch != "codegenbox/project-20260903-193012-a82f" || result.Metadata.Agent != "codex" || result.Metadata.BaseBranch != "main" || result.Metadata.BaseCommit == "" || result.Metadata.Repository != canonicalRepository {
		t.Fatalf("session metadata is incomplete or incorrect: %#v", result.Metadata)
	}
	if isWithin(result.Metadata.Worktree, canonicalRepository) {
		t.Fatalf("worktree %q was created inside source repository %q", result.Metadata.Worktree, canonicalRepository)
	}
	if _, err := os.Stat(result.Metadata.Worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists or stat failed: %v", err)
	}

	branchCommit := runGit(t, repository, "rev-parse", result.Metadata.SessionBranch)
	if branchCommit == "" {
		t.Fatal("session branch has no commit")
	}
	if result.Metadata.ImportedCommit != branchCommit {
		t.Fatalf("ImportedCommit = %q, want %q", result.Metadata.ImportedCommit, branchCommit)
	}
	if got := runGit(t, repository, "for-each-ref", "--format=%(refname:short)", "refs/heads/codegenbox"); got != result.Metadata.SessionBranch {
		t.Fatalf("Codegenbox branches = %q, want only %q", got, result.Metadata.SessionBranch)
	}
	if got := runGit(t, repository, "show", "--format=%s", "--no-patch", result.Metadata.SessionBranch); got != "session commit" {
		t.Fatalf("session commit subject = %q, want session commit", got)
	}
	if got := runGit(t, repository, "branch", "--show-current"); got != "main" {
		t.Fatalf("original checkout changed branches: %q", got)
	}
	if got := runGit(t, repository, "rev-parse", "main"); got != mainBefore {
		t.Fatalf("main moved from %s to %s", mainBefore, got)
	}
	if got := readMetadata(t, dataRoot, result.Metadata.ID); got.State != StateCompleted {
		t.Fatalf("stored state = %q, want %q", got.State, StateCompleted)
	}
}

func TestLifecycleSessionCloneIsSelfContainedAndGitUsable(t *testing.T) {
	repository := newRepository(t)
	dataRoot := filepath.Join(t.TempDir(), "codegenbox-state")
	adapter, _ := agent.Lookup("codex")
	canonicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}

	manager := testManager(dataRoot, runnerFunc(func(_ context.Context, invocation container.Invocation) error {
		workspace := invocationWorktree(t, invocation)
		gitDirectory := filepath.Join(workspace, ".git")
		info, err := os.Stat(gitDirectory)
		if err != nil || !info.IsDir() {
			t.Fatalf("session .git is not a usable directory: info=%v err=%v", info, err)
		}
		if _, err := os.Stat(filepath.Join(gitDirectory, "objects", "info", "alternates")); !os.IsNotExist(err) {
			t.Fatalf("session clone unexpectedly has Git alternates: %v", err)
		}
		config, err := os.ReadFile(filepath.Join(gitDirectory, "config"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(config), canonicalRepository) {
			t.Fatalf("clone config retains a source-repository reference: %s", config)
		}
		if remotes := runGit(t, workspace, "remote"); remotes != "" {
			t.Fatalf("clone retains source remotes: %q", remotes)
		}
		assertNoSharedGitObjectInodes(t, filepath.Join(canonicalRepository, ".git", "objects"), filepath.Join(gitDirectory, "objects"))

		// No host identity configuration is required for normal Git commands.
		writeFile(t, filepath.Join(workspace, "ordinary-git.txt"), "commit from clone\n")
		runGit(t, workspace, "add", "ordinary-git.txt")
		runGit(t, workspace, "commit", "-m", "ordinary Git works")
		// Host import must not parse agent-writable clone configuration. A normal
		// Git status/import would fail on this malformed config if it did.
		if err := os.WriteFile(filepath.Join(gitDirectory, "config"), []byte("[malformed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return nil
	}))

	result, err := manager.Start(context.Background(), repository, adapter, "node:22-bookworm", "docker")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !result.WorkspaceRemoved {
		t.Fatalf("clean self-contained clone was retained: %#v", result)
	}
	if got := runGit(t, repository, "show", "--format=%s", "--no-patch", result.Metadata.SessionBranch); got != "ordinary Git works" {
		t.Fatalf("imported commit subject = %q", got)
	}
}

func TestLifecyclePreservesDirtyWorktreeAfterDockerFailure(t *testing.T) {
	repository := newRepository(t)
	mainBefore := runGit(t, repository, "rev-parse", "main")
	dataRoot := filepath.Join(t.TempDir(), "codegenbox-state")
	adapter, _ := agent.Lookup("codex")

	manager := testManager(dataRoot, runnerFunc(func(_ context.Context, invocation container.Invocation) error {
		writeFile(t, filepath.Join(invocationWorktree(t, invocation), "uncommitted.txt"), "keep me\n")
		return errors.New("simulated Docker failure")
	}))
	result, err := manager.Start(context.Background(), repository, adapter, "node:22-bookworm", "docker")
	if err == nil || !strings.Contains(err.Error(), "simulated Docker failure") {
		t.Fatalf("Start error = %v, want Docker failure", err)
	}
	if result.Metadata.State != StateDirty || result.WorkspaceRemoved {
		t.Fatalf("result = %#v, want dirty retained worktree", result)
	}
	if contents, err := os.ReadFile(filepath.Join(result.Metadata.Worktree, "uncommitted.txt")); err != nil || string(contents) != "keep me\n" {
		t.Fatalf("dirty file was not preserved: contents=%q err=%v", contents, err)
	}
	if status := runGit(t, result.Metadata.Worktree, "status", "--porcelain"); !strings.Contains(status, "uncommitted.txt") {
		t.Fatalf("worktree not dirty: %q", status)
	}
	if got := runGit(t, repository, "rev-parse", "main"); got != mainBefore {
		t.Fatalf("runner failure changed main from %s to %s", mainBefore, got)
	}
	if got := runGit(t, repository, "branch", "--show-current"); got != "main" {
		t.Fatalf("runner failure changed normal checkout branch: %q", got)
	}
	metadata := readMetadata(t, dataRoot, result.Metadata.ID)
	if metadata.State != StateDirty || !strings.Contains(metadata.LastError, "simulated Docker failure") {
		t.Fatalf("stored metadata = %#v, want dirty state and error", metadata)
	}
}

func TestLifecycleImportsCommittedWorkAndRetainsDirtyClone(t *testing.T) {
	repository := newRepository(t)
	dataRoot := filepath.Join(t.TempDir(), "codegenbox-state")
	adapter, _ := agent.Lookup("codex")
	manager := testManager(dataRoot, runnerFunc(func(_ context.Context, invocation container.Invocation) error {
		workspace := invocationWorktree(t, invocation)
		writeFile(t, filepath.Join(workspace, "committed.txt"), "keep this commit\n")
		runGit(t, workspace, "add", "committed.txt")
		runGit(t, workspace, "commit", "-m", "commit before dirty file")
		writeFile(t, filepath.Join(workspace, "uncommitted.txt"), "keep this too\n")
		return nil
	}))

	result, err := manager.Start(context.Background(), repository, adapter, "node:22-bookworm", "docker")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if result.Metadata.State != StateDirty || result.WorkspaceRemoved {
		t.Fatalf("result = %#v, want imported dirty clone retained", result)
	}
	if got := runGit(t, repository, "show", "--format=%s", "--no-patch", result.Metadata.SessionBranch); got != "commit before dirty file" {
		t.Fatalf("committed work was not imported: %q", got)
	}
	if result.Metadata.ImportedCommit == "" {
		t.Fatalf("imported commit missing from metadata: %#v", result.Metadata)
	}
	if contents, err := os.ReadFile(filepath.Join(result.Metadata.Worktree, "uncommitted.txt")); err != nil || string(contents) != "keep this too\n" {
		t.Fatalf("dirty clone was not retained: contents=%q err=%v", contents, err)
	}
}

func TestLifecycleInspectsDirtyWorktreeAfterExecutionContextCancellation(t *testing.T) {
	repository := newRepository(t)
	mainBefore := runGit(t, repository, "rev-parse", "main")
	dataRoot := filepath.Join(t.TempDir(), "codegenbox-state")
	adapter, _ := agent.Lookup("codex")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := testManager(dataRoot, runnerFunc(func(_ context.Context, invocation container.Invocation) error {
		writeFile(t, filepath.Join(invocationWorktree(t, invocation), "interrupted.txt"), "preserve me\n")
		cancel()
		return context.Canceled
	}))

	result, err := manager.Start(ctx, repository, adapter, "node:22-bookworm", "docker")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start error = %v, want context cancellation", err)
	}
	if result.Metadata.State != StateDirty || result.WorkspaceRemoved {
		t.Fatalf("result = %#v, want dirty retained worktree after cancellation", result)
	}
	if _, err := os.Stat(filepath.Join(result.Metadata.Worktree, "interrupted.txt")); err != nil {
		t.Fatalf("interrupted work was not preserved: %v", err)
	}
	if got := runGit(t, repository, "rev-parse", "main"); got != mainBefore {
		t.Fatalf("cancellation changed main from %s to %s", mainBefore, got)
	}
}

func TestLifecycleRemovesCleanWorktreeAndRecordsInterruptedDockerRun(t *testing.T) {
	repository := newRepository(t)
	dataRoot := filepath.Join(t.TempDir(), "codegenbox-state")
	adapter, _ := agent.Lookup("codex")
	manager := testManager(dataRoot, runnerFunc(func(context.Context, container.Invocation) error {
		return errors.New("agent exited unsuccessfully")
	}))

	result, err := manager.Start(context.Background(), repository, adapter, "node:22-bookworm", "docker")
	if err == nil {
		t.Fatal("Start unexpectedly succeeded")
	}
	if result.Metadata.State != StateInterrupted || !result.WorkspaceRemoved {
		t.Fatalf("result = %#v, want interrupted clean cleanup", result)
	}
	if _, err := os.Stat(result.Metadata.Worktree); !os.IsNotExist(err) {
		t.Fatalf("clean worktree was not removed: %v", err)
	}
	if metadata := readMetadata(t, dataRoot, result.Metadata.ID); metadata.State != StateInterrupted {
		t.Fatalf("stored state = %q, want interrupted", metadata.State)
	}
}

func TestLifecycleFinalCleanupCheckPreservesLateDirtyClone(t *testing.T) {
	repository := newRepository(t)
	dataRoot := filepath.Join(t.TempDir(), "codegenbox-state")
	adapter, _ := agent.Lookup("codex")
	manager := testManager(dataRoot, runnerFunc(func(context.Context, container.Invocation) error { return nil }))
	manager.BeforeCleanup = func(workspace string) { writeFile(t, filepath.Join(workspace, "late-dirty.txt"), "preserve") }
	result, err := manager.Start(context.Background(), repository, adapter, "node:22-bookworm", "docker")
	if err == nil || result.WorkspaceRemoved || result.Metadata.State != StateDirty {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	if contents, readErr := os.ReadFile(filepath.Join(result.Metadata.Worktree, "late-dirty.txt")); readErr != nil || string(contents) != "preserve" {
		t.Fatalf("late dirty work was removed: %q %v", contents, readErr)
	}
}

func TestLifecycleRejectsNonDescendantExpectedBranchWithoutMutatingSourceRefs(t *testing.T) {
	repository := newRepository(t)
	dataRoot := filepath.Join(t.TempDir(), "codegenbox-state")
	adapter, _ := agent.Lookup("codex")
	mainBefore := runGit(t, repository, "rev-parse", "main")
	runGit(t, repository, "branch", "unrelated", "main")
	runGit(t, repository, "tag", "protect-me", "main")

	manager := testManager(dataRoot, runnerFunc(func(_ context.Context, invocation container.Invocation) error {
		workspace := invocationWorktree(t, invocation)
		base := runGit(t, workspace, "rev-parse", "HEAD")
		unrelated := runGit(t, workspace, "commit-tree", base+"^{tree}", "-m", "unrelated root")
		runGit(t, workspace, "update-ref", "refs/heads/codegenbox/project-20260903-193012-a82f", unrelated)
		runGit(t, workspace, "tag", "agent-created-tag", unrelated)
		runGit(t, workspace, "branch", "agent-created-branch", unrelated)
		return nil
	}))

	result, err := manager.Start(context.Background(), repository, adapter, "node:22-bookworm", "docker")
	if err == nil || !strings.Contains(err.Error(), "import policy rejected") {
		t.Fatalf("Start error = %v, want import policy failure", err)
	}
	if result.Metadata.State != StateInterrupted || result.WorkspaceRemoved {
		t.Fatalf("result = %#v, want retained interrupted clone", result)
	}
	if got := runGit(t, repository, "rev-parse", "main"); got != mainBefore {
		t.Fatalf("main moved from %s to %s", mainBefore, got)
	}
	if got := runGit(t, repository, "rev-parse", "unrelated"); got != mainBefore {
		t.Fatalf("unrelated branch moved from %s to %s", mainBefore, got)
	}
	if got := runGit(t, repository, "rev-parse", "protect-me"); got != mainBefore {
		t.Fatalf("tag moved from %s to %s", mainBefore, got)
	}
	assertGitRefMissing(t, repository, result.Metadata.SessionBranch)
	assertGitRefMissing(t, repository, "agent-created-branch")
	assertGitRefMissing(t, repository, "agent-created-tag")
}

func TestResumeUsesRecordedAdapterAndAtomicallyAdvancesImportedBranch(t *testing.T) {
	repository := newRepository(t)
	mainBefore := runGit(t, repository, "rev-parse", "main")
	dataRoot := filepath.Join(t.TempDir(), "codegenbox-state")
	adapter, _ := agent.Lookup("claude")
	first := testManager(dataRoot, runnerFunc(func(_ context.Context, invocation container.Invocation) error {
		workspace := invocationWorktree(t, invocation)
		assertOnlyAgentStateMounts(t, invocation, "claude")
		writeFile(t, filepath.Join(workspace, "first.txt"), "first\n")
		runGit(t, workspace, "add", "first.txt")
		runGit(t, workspace, "commit", "-m", "first")
		writeFile(t, filepath.Join(workspace, "dirty.txt"), "preserve\n")
		return nil
	}))
	initial, err := first.Start(context.Background(), repository, adapter, "node:22-bookworm", "docker")
	if err != nil || initial.Metadata.State != StateDirty || initial.Metadata.ImportedCommit == "" {
		t.Fatalf("initial = %#v, err = %v", initial, err)
	}
	firstImported := initial.Metadata.ImportedCommit
	resumed := testManager(dataRoot, runnerFunc(func(_ context.Context, invocation container.Invocation) error {
		assertOnlyAgentStateMounts(t, invocation, "claude")
		workspace := invocationWorktree(t, invocation)
		if err := os.Remove(filepath.Join(workspace, "dirty.txt")); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(workspace, "second.txt"), "second\n")
		runGit(t, workspace, "add", "second.txt")
		runGit(t, workspace, "commit", "-m", "second")
		return nil
	}))
	result, err := resumed.Resume(context.Background(), initial.Metadata.ID, "node:22-bookworm", "docker")
	if err != nil || !result.WorkspaceRemoved || result.Metadata.State != StateCompleted {
		t.Fatalf("resume = %#v, err = %v", result, err)
	}
	if result.Metadata.Agent != "claude" || result.Metadata.ResumeCount != 1 || result.Metadata.ImportedCommit == firstImported {
		t.Fatalf("resume metadata = %#v", result.Metadata)
	}
	if got := runGit(t, repository, "rev-parse", result.Metadata.SessionBranch); got != result.Metadata.ImportedCommit {
		t.Fatalf("branch = %q, imported = %q", got, result.Metadata.ImportedCommit)
	}
	if got := runGit(t, repository, "branch", "--show-current"); got != "main" {
		t.Fatalf("checkout changed to %q", got)
	}
	if got := runGit(t, repository, "rev-parse", "main"); got != mainBefore {
		t.Fatalf("main changed from %q to %q", mainBefore, got)
	}
}

func TestResumeRejectsTamperedMetadataAndPreservesClone(t *testing.T) {
	repository := newRepository(t)
	dataRoot := filepath.Join(t.TempDir(), "codegenbox-state")
	adapter, _ := agent.Lookup("codex")
	manager := testManager(dataRoot, runnerFunc(func(_ context.Context, invocation container.Invocation) error {
		writeFile(t, filepath.Join(invocationWorktree(t, invocation), "dirty.txt"), "keep\n")
		return nil
	}))
	result, err := manager.Start(context.Background(), repository, adapter, "node:22-bookworm", "docker")
	if err != nil || result.Metadata.State != StateDirty {
		t.Fatalf("Start = %#v, %v", result, err)
	}
	metadata := readMetadata(t, dataRoot, result.Metadata.ID)
	metadata.Worktree = filepath.Join(t.TempDir(), "outside")
	payload, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(MetadataPath(dataRoot, metadata.ID), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Resume(context.Background(), result.Metadata.ID, "node:22-bookworm", "docker"); err == nil {
		t.Fatal("tampered metadata resumed")
	}
	if _, err := os.Stat(result.Metadata.Worktree); err != nil {
		t.Fatalf("retained clone was removed: %v", err)
	}
}

func TestResumePreservesCloneWhenImportedCommitCompareAndSwapFails(t *testing.T) {
	repository := newRepository(t)
	dataRoot := filepath.Join(t.TempDir(), "codegenbox-state")
	adapter, _ := agent.Lookup("codex")
	first := testManager(dataRoot, runnerFunc(func(_ context.Context, invocation container.Invocation) error {
		workspace := invocationWorktree(t, invocation)
		writeFile(t, filepath.Join(workspace, "commit.txt"), "one")
		runGit(t, workspace, "add", "commit.txt")
		runGit(t, workspace, "commit", "-m", "first")
		writeFile(t, filepath.Join(workspace, "dirty.txt"), "keep")
		return nil
	}))
	initial, err := first.Start(context.Background(), repository, adapter, "node:22-bookworm", "docker")
	if err != nil || initial.Metadata.ImportedCommit == "" {
		t.Fatalf("start = %#v, %v", initial, err)
	}
	rogue := runGit(t, repository, "commit-tree", initial.Metadata.ImportedCommit+"^{tree}", "-p", initial.Metadata.ImportedCommit, "-m", "external change")
	runGit(t, repository, "update-ref", "refs/heads/"+initial.Metadata.SessionBranch, rogue)
	second := testManager(dataRoot, runnerFunc(func(_ context.Context, invocation container.Invocation) error {
		if err := os.Remove(filepath.Join(invocationWorktree(t, invocation), "dirty.txt")); err != nil {
			t.Fatal(err)
		}
		return nil
	}))
	result, err := second.Resume(context.Background(), initial.Metadata.ID, "node:22-bookworm", "docker")
	if err == nil || result.WorkspaceRemoved || result.Metadata.State != StateInterrupted {
		t.Fatalf("resume = %#v, err = %v", result, err)
	}
	if _, statErr := os.Stat(initial.Metadata.Worktree); statErr != nil {
		t.Fatalf("CAS-failed clone was removed: %v", statErr)
	}
	if got := runGit(t, repository, "rev-parse", initial.Metadata.SessionBranch); got != rogue {
		t.Fatalf("CAS failure overwrote source branch: %q", got)
	}
}

func TestSelectedAgentStateSurvivesReplacementWithoutCrossAgentMounts(t *testing.T) {
	for _, name := range agent.Supported() {
		t.Run(name, func(t *testing.T) {
			repository := newRepository(t)
			dataRoot := filepath.Join(t.TempDir(), "codegenbox-state")
			adapter, _ := agent.Lookup(name)
			first := testManager(dataRoot, runnerFunc(func(_ context.Context, invocation container.Invocation) error {
				state := selectedStateSource(t, invocation, name)
				writeFile(t, filepath.Join(state, "persistence-marker"), name)
				writeFile(t, filepath.Join(invocationWorktree(t, invocation), "dirty.txt"), "keep")
				return nil
			}))
			initial, err := first.Start(context.Background(), repository, adapter, "node:22-bookworm", "docker")
			if err != nil || initial.Metadata.State != StateDirty {
				t.Fatalf("start = %#v, %v", initial, err)
			}
			second := testManager(dataRoot, runnerFunc(func(_ context.Context, invocation container.Invocation) error {
				state := selectedStateSource(t, invocation, name)
				contents, err := os.ReadFile(filepath.Join(state, "persistence-marker"))
				if err != nil || string(contents) != name {
					t.Fatalf("state did not persist: %q %v", contents, err)
				}
				if err := os.Remove(filepath.Join(invocationWorktree(t, invocation), "dirty.txt")); err != nil {
					t.Fatal(err)
				}
				return nil
			}))
			result, err := second.Resume(context.Background(), initial.Metadata.ID, "node:22-bookworm", "docker")
			if err != nil || !result.WorkspaceRemoved {
				t.Fatalf("resume = %#v, %v", result, err)
			}
		})
	}
}

func selectedStateSource(t *testing.T, invocation container.Invocation, name string) string {
	t.Helper()
	mounts := valuesAfterInvocation(invocation.Args, "--mount")
	var selected string
	for _, mount := range mounts {
		if !strings.Contains(mount, "dst=/home/agent/") {
			continue
		}
		if name == "claude" && !strings.Contains(mount, "dst=/home/agent/.claude") {
			t.Fatalf("Claude received foreign state mount %q", mount)
		}
		if name == "codex" && !strings.Contains(mount, "dst=/home/agent/.codex") {
			t.Fatalf("Codex received foreign state mount %q", mount)
		}
		if name == "opencode" && !(strings.Contains(mount, "dst=/home/agent/.config/opencode") || strings.Contains(mount, "dst=/home/agent/.local/share/opencode")) {
			t.Fatalf("OpenCode received foreign state mount %q", mount)
		}
		if selected == "" {
			selected = strings.TrimSuffix(strings.TrimPrefix(mount, "type=bind,src="), ",dst="+strings.Split(strings.TrimPrefix(mount, "type=bind,src="), ",dst=")[1])
		}
	}
	if selected == "" {
		t.Fatal("selected adapter state mount missing")
	}
	return selected
}

func assertOnlyAgentStateMounts(t *testing.T, invocation container.Invocation, agentName string) {
	t.Helper()
	mounts := valuesAfterInvocation(invocation.Args, "--mount")
	for _, mount := range mounts {
		if strings.Contains(mount, "/home/agent/") && !strings.Contains(mount, "/home/agent/.claude") && agentName == "claude" {
			t.Fatalf("wrong Claude state mount %q", mount)
		}
	}
}

func valuesAfterInvocation(values []string, flag string) []string {
	var result []string
	for index := range values[:len(values)-1] {
		if values[index] == flag {
			result = append(result, values[index+1])
		}
	}
	return result
}

func TestStartRejectsStorageInsideSourceRepository(t *testing.T) {
	repository := newRepository(t)
	adapter, _ := agent.Lookup("codex")
	manager := testManager(filepath.Join(repository, ".codegenbox-data"), runnerFunc(func(context.Context, container.Invocation) error {
		t.Fatal("runner should not execute")
		return nil
	}))

	_, err := manager.Start(context.Background(), repository, adapter, "node:22-bookworm", "docker")
	if err == nil || !strings.Contains(err.Error(), "must not be inside") {
		t.Fatalf("Start error = %v, want storage-root rejection", err)
	}
}

func testManager(dataRoot string, runner container.Runner) Manager {
	return Manager{
		DataRoot: dataRoot,
		Runner:   runner,
		StateResolver: func(adapter agent.Adapter) ([]container.StateMount, error) {
			mounts := make([]container.StateMount, 0, len(adapter.State))
			for _, location := range adapter.State {
				path := filepath.Join(dataRoot, "agent-state", adapter.Name, location.Key)
				if err := os.MkdirAll(path, 0o700); err != nil {
					return nil, err
				}
				mounts = append(mounts, container.StateMount{Agent: adapter.Name, Source: path, Destination: location.Destination})
			}
			return mounts, nil
		},
		Now: func() time.Time {
			return time.Date(2026, 9, 3, 19, 30, 12, 0, time.UTC)
		},
		NewID: func(string, time.Time) (string, error) {
			return "project-20260903-193012-a82f", nil
		},
	}
}

func newRepository(t *testing.T) string {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "init", "-b", "main")
	runGit(t, repository, "config", "user.name", "Codegenbox Test")
	runGit(t, repository, "config", "user.email", "test@example.invalid")
	writeFile(t, filepath.Join(repository, "README.md"), "initial\n")
	runGit(t, repository, "add", "README.md")
	runGit(t, repository, "commit", "-m", "initial")
	return repository
}

func invocationWorktree(t *testing.T, invocation container.Invocation) string {
	t.Helper()
	for index, argument := range invocation.Args[:len(invocation.Args)-1] {
		if argument == "--mount" {
			const prefix = "type=bind,src="
			const suffix = ",dst=/workspace"
			mount := invocation.Args[index+1]
			if strings.HasPrefix(mount, prefix) && strings.HasSuffix(mount, suffix) {
				return strings.TrimSuffix(strings.TrimPrefix(mount, prefix), suffix)
			}
		}
	}
	t.Fatalf("worktree mount missing from %#v", invocation.Args)
	return ""
}

func readMetadata(t *testing.T, root, id string) Metadata {
	t.Helper()
	payload, err := os.ReadFile(MetadataPath(root, id))
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	var metadata Metadata
	if err := json.Unmarshal(payload, &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	return metadata
}

func runGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertNoSharedGitObjectInodes(t *testing.T, sourceObjects, cloneObjects string) {
	t.Helper()
	var sourceFiles []os.FileInfo
	if err := filepath.Walk(sourceObjects, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && info.Mode().IsRegular() {
			sourceFiles = append(sourceFiles, info)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk source objects: %v", err)
	}
	if len(sourceFiles) == 0 {
		t.Fatal("source repository unexpectedly has no object files")
	}
	cloneFileCount := 0
	if err := filepath.Walk(cloneObjects, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		cloneFileCount++
		for _, sourceFile := range sourceFiles {
			if os.SameFile(sourceFile, info) {
				return fmt.Errorf("clone object %q shares an inode with source metadata", path)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if cloneFileCount == 0 {
		t.Fatal("clone unexpectedly has no object files")
	}
}

func assertGitRefMissing(t *testing.T, directory, ref string) {
	t.Helper()
	command := exec.Command("git", "-C", directory, "rev-parse", "--verify", "--quiet", ref)
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("ref %q unexpectedly exists: %s", ref, output)
	}
}
