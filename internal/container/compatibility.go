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
	output, err := exec.CommandContext(ctx, binary, args...).Output()
	return string(output), err
}

func CheckImageCompatibility(ctx context.Context, inspector Inspector, dockerBinary, image string) error {
	if inspector == nil {
		return fmt.Errorf("image inspector is required")
	}
	if strings.TrimSpace(image) == "" || strings.HasPrefix(image, "-") {
		return fmt.Errorf("invalid image reference")
	}
	value, err := inspector.Output(ctx, dockerBinary, "image", "inspect", "--format", "{{ index .Config.Labels \""+CompatibilityLabel+"\" }}", "--", image)
	if err != nil {
		return fmt.Errorf("inspect Codegenbox image: %w", err)
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
