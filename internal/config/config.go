// Package config provides the small, explicit configuration surface for the CLI.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultImage is the immutable production image release compatible with the
// 0.2 Go CLI line. Users can select another compatible image with
// CODEGENBOX_IMAGE.
const DefaultImage = "docker.io/atacandur/codegenbox:0.2.1"

// Config contains only values that do not add host mounts or agent state.
type Config struct {
	DataRoot     string
	Image        string
	DockerBinary string
	Limits       ResourceLimits
}

// ResourceLimits are optional, conservative Docker limits. Zero means that
// Docker's/Colima's existing VM-wide limit remains in effect.
type ResourceLimits struct {
	PIDs   int
	Memory string
	CPUs   string
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

	limits, err := resourceLimitsFromEnv()
	if err != nil {
		return Config{}, err
	}
	return Config{
		DataRoot:     filepath.Clean(dataRoot),
		Image:        image,
		DockerBinary: "docker",
		Limits:       limits,
	}, nil
}

func resourceLimitsFromEnv() (ResourceLimits, error) {
	var limits ResourceLimits
	if value := strings.TrimSpace(os.Getenv("CODEGENBOX_PIDS_LIMIT")); value != "" {
		var err error
		if _, err = fmt.Sscanf(value, "%d", &limits.PIDs); err != nil || limits.PIDs < 1 || fmt.Sprintf("%d", limits.PIDs) != value {
			return ResourceLimits{}, fmt.Errorf("CODEGENBOX_PIDS_LIMIT must be a positive integer")
		}
	}
	limits.Memory, limits.CPUs = strings.TrimSpace(os.Getenv("CODEGENBOX_MEMORY_LIMIT")), strings.TrimSpace(os.Getenv("CODEGENBOX_CPUS_LIMIT"))
	for key, value := range map[string]string{"CODEGENBOX_MEMORY_LIMIT": limits.Memory, "CODEGENBOX_CPUS_LIMIT": limits.CPUs} {
		if value != "" && (strings.HasPrefix(value, "-") || strings.ContainsAny(value, "\x00 \t\n\r")) {
			return ResourceLimits{}, fmt.Errorf("%s contains an unsafe resource limit", key)
		}
	}
	return limits, nil
}
