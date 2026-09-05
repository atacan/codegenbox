package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/codegenbox/codegenbox/internal/agent"
	"github.com/codegenbox/codegenbox/internal/container"
	gitops "github.com/codegenbox/codegenbox/internal/git"
)

// Manager coordinates isolated clone setup, Docker execution, and post-exit
// import. It performs no source-repository import while Runner.Run is active.
type Manager struct {
	DataRoot string
	Runner   container.Runner
	Now      func() time.Time
	NewID    func(repository string, now time.Time) (string, error)
}

// Result is returned even when Docker exits unsuccessfully, so callers can
// tell users whether the session clone was retained.
type Result struct {
	Metadata         Metadata
	WorkspaceRemoved bool
}

// Start creates an independent session clone and runs its disposable
// container. Once Runner.Run returns for any reason, clone status is inspected
// and the expected session branch is imported before cleanup is considered.
func (m Manager) Start(ctx context.Context, workingDirectory string, adapter agent.Adapter, image, dockerBinary string) (Result, error) {
	if m.Runner == nil {
		return Result{}, fmt.Errorf("Docker runner is required")
	}
	now := m.Now
	if now == nil {
		now = time.Now
	}
	newID := m.NewID
	if newID == nil {
		newID = NewID
	}

	repository, err := gitops.Discover(ctx, workingDirectory)
	if err != nil {
		return Result{}, err
	}
	dataRoot, err := prepareDataRoot(m.DataRoot, repository.Root)
	if err != nil {
		return Result{}, err
	}

	id, err := newID(repository.Root, now())
	if err != nil {
		return Result{}, err
	}
	if err := validateID(id); err != nil {
		return Result{}, err
	}
	workspaceRoot, err := prepareWorkspaceRoot(dataRoot)
	if err != nil {
		return Result{}, err
	}
	workspace := filepath.Join(workspaceRoot, id)
	if !isWithin(workspace, workspaceRoot) {
		return Result{}, fmt.Errorf("refusing session workspace outside Codegenbox data root")
	}
	if _, err := os.Lstat(workspace); err == nil {
		return Result{}, fmt.Errorf("session workspace already exists: %s", workspace)
	} else if !os.IsNotExist(err) {
		return Result{}, fmt.Errorf("inspect session workspace path: %w", err)
	}
	if _, err := os.Lstat(MetadataPath(dataRoot, id)); err == nil {
		return Result{}, fmt.Errorf("session metadata already exists for %q", id)
	} else if !os.IsNotExist(err) {
		return Result{}, fmt.Errorf("inspect session metadata path: %w", err)
	}

	branch := "codegenbox/" + id
	if err := gitops.CreateSessionClone(ctx, repository.Root, workspace, branch, repository.BaseCommit); err != nil {
		// A partial clone is preserved rather than deleted. Host code cannot know
		// whether clone setup or a future implementation wrote useful state.
		return Result{}, err
	}

	metadata := Metadata{
		ID:            id,
		Repository:    repository.Root,
		Worktree:      workspace,
		Agent:         adapter.Name,
		BaseBranch:    repository.BaseBranch,
		BaseCommit:    repository.BaseCommit,
		SessionBranch: branch,
		StartedAt:     now(),
		State:         StateRunning,
	}
	result := Result{Metadata: metadata}
	if err := WriteMetadata(dataRoot, metadata); err != nil {
		// The clone is deliberately preserved: Codegenbox may not be able to
		// prove later writes or agent setup did not create meaningful work.
		return result, err
	}

	invocation, invocationErr := container.BuildRunInvocation(dockerBinary, image, workspace, adapter.Command)
	if invocationErr != nil {
		return m.finish(ctx, dataRoot, repository.Root, result, invocationErr)
	}
	runErr := m.Runner.Run(ctx, invocation)
	return m.finish(ctx, dataRoot, repository.Root, result, runErr)
}

func (m Manager) finish(ctx context.Context, dataRoot, repositoryRoot string, result Result, runErr error) (Result, error) {
	metadata := result.Metadata
	now := m.Now
	if now == nil {
		now = time.Now
	}
	finishedAt := now()
	metadata.FinishedAt = &finishedAt

	// A cancelled execution context must not skip post-container safety checks.
	// Runner.Run has returned; all remaining commands are local and bounded.
	postExitContext := context.WithoutCancel(ctx)
	dirty, statusErr := gitops.IsDirty(postExitContext, metadata.Worktree)
	if statusErr != nil {
		metadata.State = StateInterrupted
		metadata.LastError = joinErrors(runErr, statusErr)
		result.Metadata = metadata
		writeErr := WriteMetadata(dataRoot, metadata)
		return result, errors.Join(statusErr, runErr, writeErr)
	}

	// Import committed work even when the clone also has uncommitted changes.
	// Import is strictly post-exit and can update only the generated session ref.
	importedCommit, importErr := gitops.ImportSessionBranch(postExitContext, repositoryRoot, metadata.Worktree, metadata.SessionBranch, metadata.BaseCommit, dataRoot)
	if importErr != nil {
		if dirty {
			metadata.State = StateDirty
		} else {
			metadata.State = StateInterrupted
		}
		metadata.LastError = joinErrors(runErr, importErr)
		result.Metadata = metadata
		writeErr := WriteMetadata(dataRoot, metadata)
		return result, errors.Join(runErr, importErr, writeErr)
	}
	metadata.ImportedCommit = importedCommit

	if dirty {
		metadata.State = StateDirty
		metadata.LastError = errorText(runErr)
		result.Metadata = metadata
		writeErr := WriteMetadata(dataRoot, metadata)
		if runErr != nil || writeErr != nil {
			return result, errors.Join(runErr, writeErr)
		}
		return result, nil
	}
	// Check once more immediately before cleanup. The agent/container has
	// stopped, but this avoids deleting a clone if another local process changed
	// it during the import and metadata work above.
	dirtyAfterImport, statusErr := gitops.IsDirty(postExitContext, metadata.Worktree)
	if statusErr != nil {
		metadata.State = StateInterrupted
		metadata.LastError = joinErrors(runErr, statusErr)
		result.Metadata = metadata
		writeErr := WriteMetadata(dataRoot, metadata)
		return result, errors.Join(statusErr, runErr, writeErr)
	}
	if dirtyAfterImport {
		metadata.State = StateDirty
		metadata.LastError = errorText(runErr)
		result.Metadata = metadata
		writeErr := WriteMetadata(dataRoot, metadata)
		return result, errors.Join(runErr, writeErr)
	}

	// A confirmed-clean clone is eligible for deletion only after a verified,
	// atomic source-ref import and a durable metadata record of that import.
	if runErr != nil {
		metadata.State = StateInterrupted
		metadata.LastError = errorText(runErr)
	} else {
		metadata.State = StateCompleted
		metadata.LastError = ""
	}
	result.Metadata = metadata
	if writeErr := WriteMetadata(dataRoot, metadata); writeErr != nil {
		return result, errors.Join(runErr, writeErr)
	}

	removeErr := gitops.RemoveSessionClone(dataRoot, metadata.Worktree)
	if removeErr != nil {
		metadata.State = StateClean
		metadata.LastError = joinErrors(runErr, removeErr)
		result.Metadata = metadata
		writeErr := WriteMetadata(dataRoot, metadata)
		return result, errors.Join(runErr, removeErr, writeErr)
	}
	result.WorkspaceRemoved = true
	if runErr != nil {
		return result, runErr
	}
	return result, nil
}

func prepareWorkspaceRoot(dataRoot string) (string, error) {
	workspaceRoot := filepath.Join(dataRoot, "sessions")
	if err := os.MkdirAll(workspaceRoot, 0o700); err != nil {
		return "", fmt.Errorf("create session storage: %w", err)
	}
	canonicalWorkspaceRoot, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("resolve session storage: %w", err)
	}
	canonicalDataRoot, err := filepath.EvalSymlinks(dataRoot)
	if err != nil {
		return "", fmt.Errorf("resolve Codegenbox data root: %w", err)
	}
	if !isWithin(canonicalWorkspaceRoot, canonicalDataRoot) {
		return "", fmt.Errorf("session storage resolves outside Codegenbox data root")
	}
	return filepath.Clean(canonicalWorkspaceRoot), nil
}

func prepareDataRoot(dataRoot, repositoryRoot string) (string, error) {
	if strings.TrimSpace(dataRoot) == "" {
		return "", fmt.Errorf("Codegenbox data root is required")
	}
	absoluteDataRoot, err := filepath.Abs(dataRoot)
	if err != nil {
		return "", fmt.Errorf("resolve Codegenbox data root: %w", err)
	}
	canonicalRepository, err := canonicalExistingPath(repositoryRoot)
	if err != nil {
		return "", fmt.Errorf("resolve repository path: %w", err)
	}
	canonicalDataRoot, err := canonicalExistingPath(absoluteDataRoot)
	if err != nil {
		return "", fmt.Errorf("resolve Codegenbox data path: %w", err)
	}
	if isWithin(canonicalDataRoot, canonicalRepository) {
		return "", fmt.Errorf("Codegenbox data root must not be inside the source repository: %s", absoluteDataRoot)
	}
	if err := os.MkdirAll(absoluteDataRoot, 0o700); err != nil {
		return "", fmt.Errorf("create Codegenbox data root: %w", err)
	}
	canonicalDataRoot, err = filepath.EvalSymlinks(absoluteDataRoot)
	if err != nil {
		return "", fmt.Errorf("resolve created Codegenbox data root: %w", err)
	}
	if isWithin(canonicalDataRoot, canonicalRepository) {
		return "", fmt.Errorf("Codegenbox data root resolves inside the source repository: %s", absoluteDataRoot)
	}
	return filepath.Clean(canonicalDataRoot), nil
}

// canonicalExistingPath resolves the closest existing ancestor, so a not-yet-
// created data root cannot hide inside a symlinked source repository path.
func canonicalExistingPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var suffix []string
	current := filepath.Clean(absolute)
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func validateID(id string) error {
	if id == "" {
		return fmt.Errorf("invalid session ID %q", id)
	}
	if (id[0] < 'a' || id[0] > 'z') && (id[0] < '0' || id[0] > '9') {
		return fmt.Errorf("invalid session ID %q", id)
	}
	for _, character := range id {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return fmt.Errorf("invalid session ID %q", id)
		}
	}
	return nil
}

func isWithin(path, parent string) bool {
	relative, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func joinErrors(errorsToJoin ...error) string {
	parts := make([]string, 0, len(errorsToJoin))
	for _, err := range errorsToJoin {
		if err != nil {
			parts = append(parts, err.Error())
		}
	}
	return strings.Join(parts, "; ")
}
