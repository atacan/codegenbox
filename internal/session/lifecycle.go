package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/codegenbox/codegenbox/internal/agent"
	"github.com/codegenbox/codegenbox/internal/container"
	"github.com/codegenbox/codegenbox/internal/dependency"
	gitops "github.com/codegenbox/codegenbox/internal/git"
)

// Manager coordinates isolated clone setup, Docker execution, and post-exit
// import. It performs no source-repository import while Runner.Run is active.
type Manager struct {
	DataRoot                  string
	Runner                    container.Runner
	Now                       func() time.Time
	NewID                     func(repository string, now time.Time) (string, error)
	StateResolver             func(agent.Adapter) ([]container.StateMount, error)
	PrivateDependencyResolver func() (container.PrivateDependencyAuthorization, error)
	Limits                    container.ResourceLimits
	ImageChecker              func(context.Context, string, string) error
	ContainerStatus           func(context.Context, string, string) (present, running bool, err error)
	// BeforeCleanup is a test-only synchronization hook. Production leaves it nil.
	BeforeCleanup func(workspace string)
}

// Result is returned even when Docker exits unsuccessfully, so callers can
// tell users whether the session clone was retained.
type Result struct {
	Metadata         Metadata
	WorkspaceRemoved bool
}

// StartOptions contains the small set of explicit user choices that become
// durable session metadata. Callers may omit it to preserve the manual-only
// default behavior.
type StartOptions struct {
	PostExitAction PostExitAction
}

// Start creates an independent session clone and runs its disposable
// container. Once Runner.Run returns for any reason, clone status is inspected
// and the expected session branch is imported before cleanup is considered.
func (m Manager) Start(ctx context.Context, workingDirectory string, adapter agent.Adapter, image, dockerBinary string, options ...StartOptions) (Result, error) {
	startOptions, err := parseStartOptions(options)
	if err != nil {
		return Result{}, err
	}
	if m.Runner == nil {
		return Result{}, fmt.Errorf("Docker runner is required")
	}
	if m.ImageChecker != nil {
		if err := m.ImageChecker(ctx, dockerBinary, image); err != nil {
			return Result{}, err
		}
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
		ID:             id,
		Repository:     repository.Root,
		Worktree:       workspace,
		Agent:          adapter.Name,
		BaseBranch:     repository.BaseBranch,
		BaseCommit:     repository.BaseCommit,
		SessionBranch:  branch,
		PostExitAction: startOptions.PostExitAction,
		StartedAt:      now(),
		State:          StateRunning,
		ProcessID:      os.Getpid(),
		ContainerName:  "codegenbox-" + id,
		DockerBinary:   dockerBinary,
	}
	result := Result{Metadata: metadata}
	if err := WriteMetadata(dataRoot, metadata); err != nil {
		// The clone is deliberately preserved: Codegenbox may not be able to
		// prove later writes or agent setup did not create meaningful work.
		return result, err
	}

	privateDependencies, privateDependencyErr := m.resolvePrivateDependencies()
	if privateDependencyErr != nil {
		return m.finish(ctx, dataRoot, repository.Root, result, privateDependencyErr)
	}
	stateMounts, stateErr := m.resolveState(adapter)
	if stateErr != nil {
		return m.finish(ctx, dataRoot, repository.Root, result, stateErr)
	}
	invocation, invocationErr := container.BuildRunInvocation(dockerBinary, image, workspace, adapter.Command, agent.EnvironmentPairs(adapter), adapter.Name, []string{repository.Root, filepath.Dir(repository.Root)}, stateMounts, m.Limits, container.ContainerName(metadata.ContainerName), privateDependencies)
	if invocationErr != nil {
		return m.finish(ctx, dataRoot, repository.Root, result, invocationErr)
	}
	runErr := m.Runner.Run(ctx, invocation)
	return m.finish(ctx, dataRoot, repository.Root, result, runErr)
}

func parseStartOptions(options []StartOptions) (StartOptions, error) {
	if len(options) == 0 {
		return StartOptions{}, nil
	}
	if len(options) != 1 || !validPostExitAction(options[0].PostExitAction) {
		return StartOptions{}, fmt.Errorf("invalid session start options")
	}
	return options[0], nil
}

// Resume runs the adapter recorded in retained, validated metadata. An omitted
// ID is intentionally rejected: selecting an arbitrary path or "latest" could
// resume the wrong repository state.
func (m Manager) Resume(ctx context.Context, id, image, dockerBinary string) (Result, error) {
	if m.Runner == nil {
		return Result{}, fmt.Errorf("Docker runner is required")
	}
	if m.ImageChecker != nil {
		if err := m.ImageChecker(ctx, dockerBinary, image); err != nil {
			return Result{}, err
		}
	}
	if err := validateID(id); err != nil {
		return Result{}, err
	}
	dataRoot, err := prepareDataRootWithoutRepository(m.DataRoot)
	if err != nil {
		return Result{}, err
	}
	release, err := acquireSessionLock(dataRoot, id)
	if err != nil {
		return Result{}, err
	}
	defer release()
	metadata, err := LoadMetadata(dataRoot, id)
	if err != nil {
		return Result{}, err
	}
	if metadata.State == StateRunning {
		return Result{Metadata: metadata}, fmt.Errorf("session %q is still running; recover it only after its recorded container has stopped", id)
	}
	if err := validateRetainedMetadata(dataRoot, metadata); err != nil {
		return Result{Metadata: metadata}, err
	}
	adapter, err := agent.Lookup(metadata.Agent)
	if err != nil {
		return Result{Metadata: metadata}, fmt.Errorf("recorded session adapter: %w", err)
	}
	repository, err := gitops.Discover(context.WithoutCancel(ctx), metadata.Repository)
	if err != nil {
		return Result{Metadata: metadata}, fmt.Errorf("validate recorded repository: %w", err)
	}
	if repository.Root != metadata.Repository {
		return Result{Metadata: metadata}, fmt.Errorf("recorded repository identity no longer matches metadata")
	}
	if err := gitops.ValidateSessionClone(metadata.Worktree); err != nil {
		return Result{Metadata: metadata}, fmt.Errorf("validate retained session clone: %w", err)
	}
	now := m.Now
	if now == nil {
		now = time.Now
	}
	resumed := now()
	metadata.State, metadata.LastError, metadata.FinishedAt, metadata.ProcessID = StateRunning, "", nil, os.Getpid()
	metadata.ContainerName, metadata.DockerBinary = "codegenbox-"+metadata.ID, dockerBinary
	metadata.LastResumedAt, metadata.ResumeCount = &resumed, metadata.ResumeCount+1
	result := Result{Metadata: metadata}
	if err := WriteMetadata(dataRoot, metadata); err != nil {
		return result, err
	}
	privateDependencies, err := m.resolvePrivateDependencies()
	if err != nil {
		return m.finish(ctx, dataRoot, repository.Root, result, err)
	}
	stateMounts, err := m.resolveState(adapter)
	if err != nil {
		return m.finish(ctx, dataRoot, repository.Root, result, err)
	}
	invocation, err := container.BuildRunInvocation(dockerBinary, image, metadata.Worktree, adapter.Command, agent.EnvironmentPairs(adapter), adapter.Name, []string{repository.Root, filepath.Dir(repository.Root)}, stateMounts, m.Limits, container.ContainerName(metadata.ContainerName), privateDependencies)
	if err != nil {
		return m.finish(ctx, dataRoot, repository.Root, result, err)
	}
	return m.finish(ctx, dataRoot, repository.Root, result, m.Runner.Run(ctx, invocation))
}

// Continue reconstructs a removed clean session clone at its recorded path and
// runs the adapter originally selected for that session. Unlike Resume, it
// requires that no filesystem object remains at the workspace path.
func (m Manager) Continue(ctx context.Context, id, image, dockerBinary string) (Result, error) {
	if m.Runner == nil {
		return Result{}, fmt.Errorf("Docker runner is required")
	}
	if err := validateID(id); err != nil {
		return Result{}, err
	}
	dataRoot, err := prepareDataRootWithoutRepository(m.DataRoot)
	if err != nil {
		return Result{}, err
	}
	release, err := acquireSessionLock(dataRoot, id)
	if err != nil {
		return Result{}, err
	}
	defer release()
	if m.ImageChecker != nil {
		if err := m.ImageChecker(ctx, dockerBinary, image); err != nil {
			return Result{}, err
		}
	}

	metadata, err := LoadMetadata(dataRoot, id)
	if err != nil {
		return Result{}, err
	}
	result := Result{Metadata: metadata}
	if metadata.State == StateRunning {
		return result, fmt.Errorf("session %q is still running; recover it only after its recorded container has stopped", id)
	}
	if metadata.State == StateDirty {
		return result, fmt.Errorf("session %q has a retained dirty workspace; use codegenbox resume %s", id, id)
	}
	if err := validateContinuationMetadata(dataRoot, metadata); err != nil {
		return result, err
	}
	adapter, err := agent.Lookup(metadata.Agent)
	if err != nil {
		return result, fmt.Errorf("recorded session adapter: %w", err)
	}
	repository, err := gitops.Discover(context.WithoutCancel(ctx), metadata.Repository)
	if err != nil {
		return result, fmt.Errorf("validate recorded repository: %w", err)
	}
	if repository.Root != metadata.Repository {
		return result, fmt.Errorf("recorded repository identity no longer matches metadata")
	}
	if err := gitops.ValidateImportedSessionTip(context.WithoutCancel(ctx), repository.Root, metadata.SessionBranch, metadata.ImportedCommit); err != nil {
		return result, err
	}
	if _, err := os.Lstat(metadata.Worktree); err == nil {
		return result, fmt.Errorf("session workspace already exists: %s; use codegenbox resume %s for a retained workspace", metadata.Worktree, id)
	} else if !os.IsNotExist(err) {
		return result, fmt.Errorf("inspect session workspace path: %w", err)
	}

	now := m.Now
	if now == nil {
		now = time.Now
	}
	continued := now()
	metadata.State, metadata.LastError, metadata.FinishedAt, metadata.ProcessID = StateRunning, "", nil, os.Getpid()
	metadata.ContainerName, metadata.DockerBinary = "codegenbox-"+metadata.ID, dockerBinary
	metadata.LastContinuedAt, metadata.ContinueCount = &continued, metadata.ContinueCount+1
	result.Metadata = metadata
	if err := WriteMetadata(dataRoot, metadata); err != nil {
		return result, err
	}
	if err := gitops.CreateSessionClone(context.WithoutCancel(ctx), repository.Root, metadata.Worktree, metadata.SessionBranch, metadata.ImportedCommit); err != nil {
		return m.recordContinuationSetupFailure(dataRoot, result, err)
	}

	privateDependencies, err := m.resolvePrivateDependencies()
	if err != nil {
		return m.finish(ctx, dataRoot, repository.Root, result, err)
	}
	stateMounts, err := m.resolveState(adapter)
	if err != nil {
		return m.finish(ctx, dataRoot, repository.Root, result, err)
	}
	invocation, err := container.BuildRunInvocation(dockerBinary, image, metadata.Worktree, adapter.Command, agent.EnvironmentPairs(adapter), adapter.Name, []string{repository.Root, filepath.Dir(repository.Root)}, stateMounts, m.Limits, container.ContainerName(metadata.ContainerName), privateDependencies)
	if err != nil {
		return m.finish(ctx, dataRoot, repository.Root, result, err)
	}
	return m.finish(ctx, dataRoot, repository.Root, result, m.Runner.Run(ctx, invocation))
}

func (m Manager) recordContinuationSetupFailure(dataRoot string, result Result, setupErr error) (Result, error) {
	metadata := result.Metadata
	now := m.Now
	if now == nil {
		now = time.Now
	}
	finished := now()
	metadata.State, metadata.LastError, metadata.FinishedAt, metadata.ProcessID = StateInterrupted, errorText(setupErr), &finished, 0
	result.Metadata = metadata
	return result, errors.Join(setupErr, WriteMetadata(dataRoot, metadata))
}

// acquireSessionLock serializes transitions of one retained clone. Its file
// contains only the owner PID; a dead lock owner is safely replaced. A reused
// PID can only conservatively block a resume, never permit concurrent access.
func acquireSessionLock(dataRoot, id string) (func(), error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	directory := filepath.Join(dataRoot, "locks")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create session lock storage: %w", err)
	}
	path := filepath.Join(directory, id+".lock")
	for attempt := 0; attempt < 2; attempt++ {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, writeErr := fmt.Fprintf(file, "%d\n", os.Getpid())
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				os.Remove(path)
				return nil, errors.Join(writeErr, closeErr)
			}
			return func() { _ = os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("acquire session lock: %w", err)
		}
		payload, readErr := os.ReadFile(path)
		var owner int
		parsed, parseErr := fmt.Sscanf(strings.TrimSpace(string(payload)), "%d", &owner)
		if readErr != nil || parseErr != nil || parsed != 1 || processAlive(owner) {
			return nil, fmt.Errorf("session %q is already being resumed or recovered", id)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("remove stale session lock: %w", err)
		}
	}
	return nil, fmt.Errorf("acquire session lock: concurrent update")
}

func (m Manager) resolveState(adapter agent.Adapter) ([]container.StateMount, error) {
	resolver := m.StateResolver
	if resolver == nil {
		resolver = agent.ResolveState
	}
	return resolver(adapter)
}

func (m Manager) resolvePrivateDependencies() (container.PrivateDependencyAuthorization, error) {
	resolver := m.PrivateDependencyResolver
	if resolver == nil {
		resolver = dependency.Resolve
	}
	return resolver()
}

func (m Manager) finish(ctx context.Context, dataRoot, repositoryRoot string, result Result, runErr error) (Result, error) {
	metadata := result.Metadata
	now := m.Now
	if now == nil {
		now = time.Now
	}
	finishedAt := now()
	metadata.FinishedAt = &finishedAt
	metadata.ProcessID = 0

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
	importedCommit, importErr := gitops.ImportSessionBranch(postExitContext, repositoryRoot, metadata.Worktree, metadata.SessionBranch, metadata.BaseCommit, metadata.ImportedCommit, dataRoot)
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

	if m.BeforeCleanup != nil {
		m.BeforeCleanup(metadata.Worktree)
	}
	removeErr := gitops.RemoveSessionClone(dataRoot, metadata.Worktree)
	if removeErr != nil {
		if errors.Is(removeErr, gitops.ErrDirtyClone) {
			metadata.State = StateDirty
		} else {
			metadata.State = StateClean
		}
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

// RecoverOrphans reconciles only sessions recorded as running by a process
// that is no longer alive. It uses the same post-container import and cleanup
// path as a returned runner, so dirty work is preserved and only the reserved
// branch can be imported. A live PID is never touched.
func (m Manager) RecoverOrphans(ctx context.Context) ([]Result, error) {
	dataRoot, err := prepareDataRootWithoutRepository(m.DataRoot)
	if err != nil {
		return nil, err
	}
	items, err := ListMetadata(dataRoot)
	if err != nil {
		return nil, err
	}
	var results []Result
	var allErr error
	for _, metadata := range items {
		if metadata.State != StateRunning || processAlive(metadata.ProcessID) {
			continue
		}
		release, lockErr := acquireSessionLock(dataRoot, metadata.ID)
		if lockErr != nil {
			allErr = errors.Join(allErr, fmt.Errorf("recover %s: %w", metadata.ID, lockErr))
			continue
		}
		if metadata.ContainerName == "" || metadata.DockerBinary == "" {
			allErr = errors.Join(allErr, fmt.Errorf("recover %s: legacy running session cannot be safely verified; clone preserved", metadata.ID))
			release()
			continue
		}
		present, running, inspectErr := m.containerStatus(ctx, metadata.DockerBinary, metadata.ContainerName)
		if inspectErr != nil {
			allErr = errors.Join(allErr, fmt.Errorf("recover %s: inspect recorded container: %w", metadata.ID, inspectErr))
			release()
			continue
		}
		if present && running {
			allErr = errors.Join(allErr, fmt.Errorf("recover %s: recorded container is still running; clone preserved", metadata.ID))
			release()
			continue
		}
		if err := validateRetainedMetadata(dataRoot, metadata); err != nil {
			allErr = errors.Join(allErr, fmt.Errorf("recover %s: %w", metadata.ID, err))
			release()
			continue
		}
		result, finishErr := m.finish(ctx, dataRoot, metadata.Repository, Result{Metadata: metadata}, fmt.Errorf("previous Codegenbox process ended before Docker returned"))
		results = append(results, result)
		allErr = errors.Join(allErr, finishErr)
		release()
	}
	return results, allErr
}

func (m Manager) containerStatus(ctx context.Context, binary, name string) (bool, bool, error) {
	if m.ContainerStatus != nil {
		return m.ContainerStatus(ctx, binary, name)
	}
	output, err := exec.CommandContext(ctx, binary, "container", "inspect", "--format", "{{.State.Running}}", "--", name).CombinedOutput()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 && definiteContainerNotFound(string(output), name) {
			return false, false, nil
		}
		return false, false, err
	}
	value := strings.TrimSpace(string(output))
	if value == "true" {
		return true, true, nil
	}
	if value == "false" {
		return true, false, nil
	}
	return false, false, fmt.Errorf("unexpected container state %q", value)
}

func definiteContainerNotFound(output, name string) bool {
	// Docker's CLI emits one of these stable English diagnostics for a missing
	// object. Every other failure (including daemon/context failure) is unsafe
	// to interpret as absence and therefore blocks recovery.
	return strings.Contains(output, "No such container: "+name) || strings.Contains(output, "No such object: "+name)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
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

func prepareDataRootWithoutRepository(dataRoot string) (string, error) {
	if strings.TrimSpace(dataRoot) == "" {
		return "", fmt.Errorf("Codegenbox data root is required")
	}
	abs, err := filepath.Abs(dataRoot)
	if err != nil {
		return "", fmt.Errorf("resolve Codegenbox data root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", fmt.Errorf("create Codegenbox data root: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve Codegenbox data root: %w", err)
	}
	return filepath.Clean(canonical), nil
}

func validateRetainedMetadata(dataRoot string, metadata Metadata) error {
	if err := validateID(metadata.ID); err != nil {
		return err
	}
	if !validPostExitAction(metadata.PostExitAction) {
		return fmt.Errorf("session metadata has an invalid post-exit action")
	}
	if metadata.Agent == "" || metadata.Repository == "" || metadata.Worktree == "" || metadata.BaseBranch == "" || metadata.BaseCommit == "" {
		return fmt.Errorf("session metadata is missing required fields")
	}
	if metadata.SessionBranch != "codegenbox/"+metadata.ID {
		return fmt.Errorf("session metadata has an unexpected reserved branch")
	}
	workspaceRoot, err := prepareWorkspaceRoot(dataRoot)
	if err != nil {
		return err
	}
	workspace, err := filepath.Abs(metadata.Worktree)
	if err != nil {
		return fmt.Errorf("resolve recorded workspace: %w", err)
	}
	if filepath.Clean(workspace) != filepath.Join(workspaceRoot, metadata.ID) || !isWithin(workspace, workspaceRoot) {
		return fmt.Errorf("recorded workspace is outside Codegenbox session storage")
	}
	if err := gitops.ValidateSessionClone(workspace); err != nil {
		return fmt.Errorf("recorded workspace is not a self-contained clone: %w", err)
	}
	repository, err := canonicalExistingPath(metadata.Repository)
	if err != nil {
		return fmt.Errorf("resolve recorded repository: %w", err)
	}
	if repository != filepath.Clean(metadata.Repository) {
		return fmt.Errorf("recorded repository path is not canonical")
	}
	return nil
}

// validateContinuationMetadata validates durable fields without probing a
// retained clone. Continue intentionally reconstructs only an absent path;
// Resume owns validation of an existing clone.
func validateContinuationMetadata(dataRoot string, metadata Metadata) error {
	if err := validateID(metadata.ID); err != nil {
		return err
	}
	if !validPostExitAction(metadata.PostExitAction) {
		return fmt.Errorf("session metadata has an invalid post-exit action")
	}
	if metadata.Agent == "" || metadata.Repository == "" || metadata.Worktree == "" || metadata.BaseBranch == "" || metadata.BaseCommit == "" || metadata.ImportedCommit == "" {
		return fmt.Errorf("session metadata is missing required fields for continuation")
	}
	if metadata.SessionBranch != "codegenbox/"+metadata.ID {
		return fmt.Errorf("session metadata has an unexpected reserved branch")
	}
	if err := gitops.ValidateCommitOID(metadata.BaseCommit); err != nil {
		return fmt.Errorf("invalid recorded base commit: %w", err)
	}
	if err := gitops.ValidateCommitOID(metadata.ImportedCommit); err != nil {
		return fmt.Errorf("invalid recorded imported commit: %w", err)
	}
	workspaceRoot, err := prepareWorkspaceRoot(dataRoot)
	if err != nil {
		return err
	}
	workspace, err := filepath.Abs(metadata.Worktree)
	if err != nil {
		return fmt.Errorf("resolve recorded workspace: %w", err)
	}
	if filepath.Clean(workspace) != filepath.Join(workspaceRoot, metadata.ID) || !isWithin(workspace, workspaceRoot) {
		return fmt.Errorf("recorded workspace is outside Codegenbox session storage")
	}
	repository, err := canonicalExistingPath(metadata.Repository)
	if err != nil {
		return fmt.Errorf("resolve recorded repository: %w", err)
	}
	if repository != filepath.Clean(metadata.Repository) {
		return fmt.Errorf("recorded repository path is not canonical")
	}
	return nil
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
