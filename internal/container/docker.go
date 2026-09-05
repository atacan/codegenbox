// Package container builds and runs the narrow Docker invocation used by Phase 1.
package container

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
)

const workspace = "/workspace"

// Invocation is a fully constructed Docker CLI invocation. Keeping it as data
// makes the security-sensitive argument list inspectable in tests.
type Invocation struct {
	Binary string
	Args   []string
}

// BuildRunInvocation constructs a disposable, interactive Docker command with
// exactly one host filesystem mount: the temporary session clone at /workspace.
func BuildRunInvocation(binary, image, workspacePath string, command []string) (Invocation, error) {
	if strings.TrimSpace(binary) == "" {
		return Invocation{}, fmt.Errorf("Docker binary is required")
	}
	if strings.TrimSpace(image) == "" || strings.HasPrefix(image, "-") {
		return Invocation{}, fmt.Errorf("a non-option Docker image is required")
	}
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return Invocation{}, fmt.Errorf("agent command is required")
	}

	absoluteWorkspace, err := filepath.Abs(workspacePath)
	if err != nil {
		return Invocation{}, fmt.Errorf("resolve session workspace path: %w", err)
	}
	absoluteWorkspace = filepath.Clean(absoluteWorkspace)
	// Docker's --mount key/value format uses commas as separators. Rejecting a
	// comma avoids turning a user-controlled filesystem path into new mount data.
	if strings.Contains(absoluteWorkspace, ",") {
		return Invocation{}, fmt.Errorf("session workspace paths containing commas are not supported")
	}

	args := []string{
		"run",
		"--rm",
		"--interactive",
		"--tty",
		"--workdir", workspace,
		"--mount", "type=bind,src=" + absoluteWorkspace + ",dst=" + workspace,
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges=true",
		image,
	}
	args = append(args, command...)
	return Invocation{Binary: binary, Args: args}, nil
}

// Runner is injectable so tests can verify lifecycle behavior without Docker.
type Runner interface {
	Run(context.Context, Invocation) error
}

// ExecRunner forwards the terminal streams directly to docker run.
type ExecRunner struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

func (r ExecRunner) Run(ctx context.Context, invocation Invocation) error {
	cmd := exec.CommandContext(ctx, invocation.Binary, invocation.Args...)
	cmd.Stdin = r.Stdin
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker run: %w", err)
	}
	return nil
}
