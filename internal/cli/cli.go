// Package cli implements the intentionally small Phase 1 command shape.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/codegenbox/codegenbox/internal/agent"
	"github.com/codegenbox/codegenbox/internal/config"
	"github.com/codegenbox/codegenbox/internal/container"
	"github.com/codegenbox/codegenbox/internal/session"
)

type Environment struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getwd  func() (string, error)
	Config func() (config.Config, error)
	Runner container.Runner
}

// Run accepts `codegenbox codex` and `codegenbox run codex` only.
func Run(ctx context.Context, arguments []string, environment Environment) error {
	agentName, err := parseArguments(arguments)
	if err != nil {
		return err
	}
	adapter, err := agent.Lookup(agentName)
	if err != nil {
		return err
	}

	getwd := environment.Getwd
	if getwd == nil {
		getwd = os.Getwd
	}
	workingDirectory, err := getwd()
	if err != nil {
		return fmt.Errorf("read current directory: %w", err)
	}
	workingDirectory, err = filepath.Abs(workingDirectory)
	if err != nil {
		return fmt.Errorf("resolve current directory: %w", err)
	}

	loadConfig := environment.Config
	if loadConfig == nil {
		loadConfig = config.LoadFromEnv
	}
	configured, err := loadConfig()
	if err != nil {
		return err
	}
	runner := environment.Runner
	if runner == nil {
		runner = container.ExecRunner{
			Stdin:  chooseReader(environment.Stdin, os.Stdin),
			Stdout: chooseWriter(environment.Stdout, os.Stdout),
			Stderr: chooseWriter(environment.Stderr, os.Stderr),
		}
	}

	manager := session.Manager{DataRoot: configured.DataRoot, Runner: runner}
	result, runErr := manager.Start(ctx, workingDirectory, adapter, configured.Image, configured.DockerBinary)
	output := chooseWriter(environment.Stdout, os.Stdout)
	if result.Metadata.ID != "" {
		printResult(output, result)
	}
	if runErr != nil {
		return fmt.Errorf("Codegenbox session did not complete: %w", runErr)
	}
	return nil
}

func parseArguments(arguments []string) (string, error) {
	switch len(arguments) {
	case 1:
		return arguments[0], nil
	case 2:
		if arguments[0] == "run" {
			return arguments[1], nil
		}
	}
	return "", fmt.Errorf("usage: codegenbox <agent> | codegenbox run <agent>\nPhase 1 supports: codex")
}

func printResult(output io.Writer, result session.Result) {
	metadata := result.Metadata
	switch metadata.State {
	case session.StateCompleted:
		fmt.Fprintf(output, "Codegenbox session complete.\n\nBranch: %s\nTemporary workspace: removed\n", metadata.SessionBranch)
	case session.StateDirty:
		fmt.Fprintf(output, "Codegenbox session stopped with uncommitted changes.\n\nWorkspace preserved: %s\nSession metadata: %s\n", metadata.Worktree, session.MetadataPath(filepath.Dir(filepath.Dir(metadata.Worktree)), metadata.ID))
	case session.StateInterrupted:
		fmt.Fprintf(output, "Codegenbox session was interrupted.\n\nWorkspace: %s\n", workspaceMessage(result))
	case session.StateClean:
		fmt.Fprintf(output, "Codegenbox found a clean workspace but could not remove it.\n\nWorkspace preserved: %s\n", metadata.Worktree)
	}
}

func workspaceMessage(result session.Result) string {
	if result.WorkspaceRemoved {
		return "removed after a clean status check"
	}
	return result.Metadata.Worktree
}

func chooseReader(candidate, fallback io.Reader) io.Reader {
	if candidate != nil {
		return candidate
	}
	return fallback
}

func chooseWriter(candidate, fallback io.Writer) io.Writer {
	if candidate != nil {
		return candidate
	}
	return fallback
}
