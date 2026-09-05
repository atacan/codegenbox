// Package agent defines the deliberately small Phase 1 agent surface.
package agent

import (
	"fmt"
	"strings"
)

const Codex = "codex"

// Adapter describes a command that is already available in the selected image.
// Phase 1 adapters intentionally have no host state, credential, or history
// mounts. Those mounts are a Phase 2 concern.
type Adapter struct {
	Name        string
	Command     []string
	Description string
}

// Lookup returns the sole proof-of-architecture adapter.
func Lookup(name string) (Adapter, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case Codex:
		return Adapter{
			Name:        Codex,
			Command:     []string{"npx", "--yes", "@openai/codex", "--yolo"},
			Description: "Runs Codex through npx with --yolo inside the isolated session-clone container; authentication and history remain ephemeral in Phase 1.",
		}, nil
	default:
		return Adapter{}, fmt.Errorf("unsupported agent %q (Phase 1 supports only %q)", name, Codex)
	}
}

// Supported returns adapter names accepted by this release.
func Supported() []string {
	return []string{Codex}
}
