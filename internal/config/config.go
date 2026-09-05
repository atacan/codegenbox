// Package config provides the small, explicit configuration surface for the CLI.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultImage is the immutable production image release compatible with the
// 0.1 Go CLI line. Users can select another compatible image with
// CODEGENBOX_IMAGE.
const DefaultImage = "docker.io/atacandur/codegenbox:0.1.0"

// Config contains only values that do not add host mounts or agent state.
type Config struct {
	DataRoot     string
	Image        string
	DockerBinary string
}

// DefaultDataRoot selects an XDG-style, Codegenbox-owned host directory. It is
// never mounted into a container.
func DefaultDataRoot() (string, error) {
	if xdgData := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdgData != "" {
		return filepath.Abs(filepath.Join(xdgData, "codegenbox"))
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for Codegenbox data: %w", err)
	}
	return filepath.Abs(filepath.Join(home, ".local", "share", "codegenbox"))
}

// LoadFromEnv reads the intentionally narrow configuration overrides. The image is
// not built or pulled by the CLI; Docker handles image availability when run.
func LoadFromEnv() (Config, error) {
	dataRoot := strings.TrimSpace(os.Getenv("CODEGENBOX_DATA_DIR"))
	if dataRoot == "" {
		var err error
		dataRoot, err = DefaultDataRoot()
		if err != nil {
			return Config{}, err
		}
	} else {
		var err error
		dataRoot, err = filepath.Abs(dataRoot)
		if err != nil {
			return Config{}, fmt.Errorf("resolve CODEGENBOX_DATA_DIR: %w", err)
		}
	}

	image := strings.TrimSpace(os.Getenv("CODEGENBOX_IMAGE"))
	if image == "" {
		image = DefaultImage
	}

	return Config{
		DataRoot:     filepath.Clean(dataRoot),
		Image:        image,
		DockerBinary: "docker",
	}, nil
}
