// Package session owns session identity, metadata, and lifecycle orchestration.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type State string

const (
	StateRunning     State = "running"
	StateClean       State = "clean"
	StateDirty       State = "dirty"
	StateCompleted   State = "completed"
	StateInterrupted State = "interrupted"
)

// PostExitAction is a user-selected, host-side action that may run after a
// session has completed safely. It is persisted so resumed and continued
// sessions keep the original request without relying on process state.
type PostExitAction string

const (
	PostExitActionNone        PostExitAction = ""
	PostExitActionOpenCompare PostExitAction = "open_compare"
)

// Metadata is the persistent record for one temporary session clone. A dirty
// state means the clone is retained for a future implementation, but Phase 1
// does not yet provide a resume command.
type Metadata struct {
	ID              string         `json:"id"`
	Repository      string         `json:"repository"`
	Worktree        string         `json:"worktree"`
	Agent           string         `json:"agent"`
	BaseBranch      string         `json:"base_branch"`
	BaseCommit      string         `json:"base_commit"`
	SessionBranch   string         `json:"session_branch"`
	ImportedCommit  string         `json:"imported_commit,omitempty"`
	PostExitAction  PostExitAction `json:"post_exit_action,omitempty"`
	StartedAt       time.Time      `json:"started_at"`
	FinishedAt      *time.Time     `json:"finished_at,omitempty"`
	LastResumedAt   *time.Time     `json:"last_resumed_at,omitempty"`
	ResumeCount     int            `json:"resume_count,omitempty"`
	LastContinuedAt *time.Time     `json:"last_continued_at,omitempty"`
	ContinueCount   int            `json:"continue_count,omitempty"`
	ProcessID       int            `json:"process_id,omitempty"`
	ContainerName   string         `json:"container_name,omitempty"`
	DockerBinary    string         `json:"docker_binary,omitempty"`
	State           State          `json:"state"`
	LastError       string         `json:"last_error,omitempty"`
}

// LoadMetadata reads a single, validated metadata record without following an
// ID-controlled path outside Codegenbox storage.
func LoadMetadata(dataRoot, id string) (Metadata, error) {
	if err := validateID(id); err != nil {
		return Metadata{}, fmt.Errorf("invalid session ID: %w", err)
	}
	directory, err := metadataDirectory(dataRoot, false)
	if err != nil {
		return Metadata{}, err
	}
	path := filepath.Join(directory, id+".json")
	info, err := os.Lstat(path)
	if err != nil {
		return Metadata{}, fmt.Errorf("inspect session metadata %q: %w", id, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Metadata{}, fmt.Errorf("session metadata %q is not an ordinary file", id)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return Metadata{}, fmt.Errorf("read session metadata %q: %w", id, err)
	}
	var metadata Metadata
	if err := json.Unmarshal(payload, &metadata); err != nil {
		return Metadata{}, fmt.Errorf("decode session metadata %q: %w", id, err)
	}
	if metadata.ID != id || validateID(metadata.ID) != nil {
		return Metadata{}, fmt.Errorf("session metadata %q has an invalid ID", id)
	}
	return metadata, nil
}

// ListMetadata returns valid records in ID order. A malformed record is an
// error rather than silently ignored: callers must never resume ambiguous data.
func ListMetadata(dataRoot string) ([]Metadata, error) {
	directory, err := metadataDirectory(dataRoot, false)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read metadata directory: %w", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read metadata directory: %w", err)
	}
	result := make([]Metadata, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		metadata, err := LoadMetadata(dataRoot, id)
		if err != nil {
			return nil, err
		}
		if err := validateMetadataForListing(dataRoot, metadata); err != nil {
			return nil, fmt.Errorf("unsafe session metadata %q: %w", id, err)
		}
		result = append(result, metadata)
	}
	return result, nil
}

func MetadataPath(dataRoot, id string) string {
	return filepath.Join(dataRoot, "metadata", id+".json")
}

// WriteMetadata atomically replaces a session record with restrictive host-file
// permissions. Metadata storage is never part of the Docker mount set.
func WriteMetadata(dataRoot string, metadata Metadata) error {
	if err := validateID(metadata.ID); err != nil {
		return fmt.Errorf("session metadata requires a valid ID: %w", err)
	}
	if err := validateMetadataForListing(dataRoot, metadata); err != nil {
		return fmt.Errorf("unsafe session metadata: %w", err)
	}
	directory, err := metadataDirectory(dataRoot, true)
	if err != nil {
		return err
	}

	payload, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session metadata: %w", err)
	}
	payload = append(payload, '\n')

	temporary, err := os.CreateTemp(directory, ".metadata-*.json")
	if err != nil {
		return fmt.Errorf("create temporary metadata file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("set metadata permissions: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return fmt.Errorf("write session metadata: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close session metadata: %w", err)
	}
	if err := os.Rename(temporaryName, filepath.Join(directory, metadata.ID+".json")); err != nil {
		return fmt.Errorf("replace session metadata: %w", err)
	}
	return nil
}

func metadataDirectory(dataRoot string, create bool) (string, error) {
	root, err := canonicalDataRoot(dataRoot, create)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(root, "metadata")
	info, err := os.Lstat(directory)
	if os.IsNotExist(err) {
		if !create {
			return "", err
		}
		if err := os.Mkdir(directory, 0o700); err != nil {
			return "", fmt.Errorf("create metadata directory: %w", err)
		}
		info, err = os.Lstat(directory)
	}
	if err != nil {
		return "", fmt.Errorf("inspect metadata directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("metadata directory must be an ordinary directory beneath Codegenbox storage")
	}
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", fmt.Errorf("resolve metadata directory: %w", err)
	}
	if filepath.Clean(canonical) != directory || !isWithin(filepath.Clean(canonical), root) {
		return "", fmt.Errorf("metadata directory resolves outside Codegenbox storage")
	}
	return directory, nil
}

func canonicalDataRoot(dataRoot string, create bool) (string, error) {
	if strings.TrimSpace(dataRoot) == "" {
		return "", fmt.Errorf("Codegenbox data root is required")
	}
	abs, err := filepath.Abs(dataRoot)
	if err != nil {
		return "", err
	}
	if create {
		if err := os.MkdirAll(abs, 0o700); err != nil {
			return "", fmt.Errorf("create Codegenbox data root: %w", err)
		}
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(canonical), nil
}

// validateMetadataForListing performs only lexical/canonical storage checks;
// callers may probe the workspace only after this returns successfully.
func validateMetadataForListing(dataRoot string, metadata Metadata) error {
	if err := validateID(metadata.ID); err != nil {
		return err
	}
	if !validPostExitAction(metadata.PostExitAction) {
		return fmt.Errorf("invalid post-exit action")
	}
	if metadata.Agent == "" || metadata.SessionBranch != "codegenbox/"+metadata.ID {
		return fmt.Errorf("missing agent or reserved branch")
	}
	root, err := canonicalDataRoot(dataRoot, false)
	if err != nil {
		return err
	}
	sessions := filepath.Join(root, "sessions")
	info, err := os.Lstat(sessions)
	if err != nil {
		return fmt.Errorf("inspect session storage: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("session storage is not an ordinary directory")
	}
	expected := filepath.Join(sessions, metadata.ID)
	workspace, err := filepath.Abs(metadata.Worktree)
	if err != nil {
		return err
	}
	if filepath.Clean(workspace) != expected {
		return fmt.Errorf("workspace is outside expected session storage")
	}
	return nil
}

func validPostExitAction(action PostExitAction) bool {
	return action == PostExitActionNone || action == PostExitActionOpenCompare
}

// NewID creates a readable, branch-safe session identifier.
func NewID(repository string, now time.Time) (string, error) {
	bytes := make([]byte, 2)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate session suffix: %w", err)
	}
	return fmt.Sprintf("%s-%s-%s", repositorySlug(repository), now.Format("20060102-150405"), hex.EncodeToString(bytes)), nil
}

func repositorySlug(repository string) string {
	base := strings.ToLower(filepath.Base(filepath.Clean(repository)))
	var result strings.Builder
	lastDash := false
	for _, character := range base {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			result.WriteRune(character)
			lastDash = false
			continue
		}
		if !lastDash {
			result.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(result.String(), "-")
	if slug == "" {
		return "repository"
	}
	return slug
}
