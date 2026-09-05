package container

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// CompatibilityLabel is deliberately a small, explicit image/CLI contract.
// It prevents a locally retagged arbitrary image from silently receiving agent
// state mounts and the fixed adapter commands.
const CompatibilityLabel = "io.codegenbox.compatibility"
const CompatibilityVersion = "1"

type Inspector interface {
	Output(context.Context, string, ...string) (string, error)
}
type ExecInspector struct{}

func (ExecInspector) Output(ctx context.Context, binary string, args ...string) (string, error) {
	output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	return string(output), err
}

func CheckImageCompatibility(ctx context.Context, inspector Inspector, dockerBinary, image string) error {
	value, err := inspectImageCompatibility(ctx, inspector, dockerBinary, image)
	if err != nil {
		return err
	}
	// Phase 3 images predate this label. Keep them usable for the 0.1 CLI line;
	// once a label is present, however, it must match exactly.
	if trimmed := strings.TrimSpace(value); trimmed == "" || trimmed == "<no value>" {
		return nil
	} else if trimmed != CompatibilityVersion {
		return fmt.Errorf("image %q is not compatible with this Codegenbox CLI (want %s=%s)", image, CompatibilityLabel, CompatibilityVersion)
	}
	return nil
}

// EnsureImageCompatibility makes the first normal session work on a host that
// has not yet cached the configured image. Docker normally pulls on docker run,
// but the compatibility check must happen before a container receives any agent
// state mounts. Doctor deliberately uses CheckImageCompatibility instead, so it
// remains an inspection-only command.
func EnsureImageCompatibility(ctx context.Context, inspector Inspector, dockerBinary, image string) error {
	_, err := inspectImageCompatibility(ctx, inspector, dockerBinary, image)
	if err != nil {
		if !isMissingImageError(err) {
			return err
		}
		output, pullErr := inspector.Output(ctx, dockerBinary, "image", "pull", "--", image)
		if pullErr != nil {
			return commandError("pull Codegenbox image", pullErr, output)
		}
	}
	return CheckImageCompatibility(ctx, inspector, dockerBinary, image)
}

func inspectImageCompatibility(ctx context.Context, inspector Inspector, dockerBinary, image string) (string, error) {
	if inspector == nil {
		return "", fmt.Errorf("image inspector is required")
	}
	if strings.TrimSpace(image) == "" || strings.HasPrefix(image, "-") {
		return "", fmt.Errorf("invalid image reference")
	}
	value, err := inspector.Output(ctx, dockerBinary, "image", "inspect", "--format", "{{ index .Config.Labels \""+CompatibilityLabel+"\" }}", "--", image)
	if err != nil {
		return "", commandError("inspect Codegenbox image", err, value)
	}
	return value, nil
}

func isMissingImageError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such image") || strings.Contains(message, "no such object")
}

func commandError(action string, err error, output string) error {
	if message := strings.TrimSpace(output); message != "" {
		return fmt.Errorf("%s: %w: %s", action, err, message)
	}
	return fmt.Errorf("%s: %w", action, err)
}
