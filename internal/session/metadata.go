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

// Metadata is the persistent record for one temporary session clone. A dirty
// state means the clone is retained for a future implementation, but Phase 1
// does not yet provide a resume command.
type Metadata struct {
	ID             string     `json:"id"`
	Repository     string     `json:"repository"`
	Worktree       string     `json:"worktree"`
	Agent          string     `json:"agent"`
	BaseBranch     string     `json:"base_branch"`
	BaseCommit     string     `json:"base_commit"`
	SessionBranch  string     `json:"session_branch"`
	ImportedCommit string     `json:"imported_commit,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	State          State      `json:"state"`
	LastError      string     `json:"last_error,omitempty"`
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
	directory := filepath.Dir(MetadataPath(dataRoot, metadata.ID))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create metadata directory: %w", err)
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
	if err := os.Rename(temporaryName, MetadataPath(dataRoot, metadata.ID)); err != nil {
		return fmt.Errorf("replace session metadata: %w", err)
	}
	return nil
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
