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
	"github.com/codegenbox/codegenbox/internal/host"
	"github.com/codegenbox/codegenbox/internal/session"
	buildversion "github.com/codegenbox/codegenbox/internal/version"
)

type Environment struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getwd  func() (string, error)
	Config func() (config.Config, error)
	Runner container.Runner
}

func Run(ctx context.Context, arguments []string, environment Environment) error {
	output := chooseWriter(environment.Stdout, os.Stdout)
	if len(arguments) == 1 && (arguments[0] == "version" || arguments[0] == "--version") {
		_, err := fmt.Fprintf(output, "codegenbox %s\n", buildversion.Version)
		return err
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
	switch {
	case len(arguments) == 1 && arguments[0] == "sessions":
		return printSessions(output, configured.DataRoot)
	case len(arguments) == 2 && arguments[0] == "push":
		metadata, err := session.LoadMetadata(configured.DataRoot, arguments[1])
		if err != nil {
			return err
		}
		result, err := host.PushSessionBranch(ctx, metadata)
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "Pushed %s to its source origin.\n", metadata.SessionBranch)
		if result.GitHub != nil {
			address, urlErr := host.CompareURL(metadata, *result.GitHub)
			if urlErr == nil {
				fmt.Fprintf(output, "Open a compare/PR page: codegenbox compare %s\n%s\n", metadata.ID, address)
			}
		}
		return nil
	case len(arguments) == 2 && arguments[0] == "compare":
		metadata, err := session.LoadMetadata(configured.DataRoot, arguments[1])
		if err != nil {
			return err
		}
		remote, err := host.DetectGitHubRemote(ctx, metadata.Repository)
		if err != nil {
			return err
		}
		address, err := host.CompareURL(metadata, remote)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(output, address); err != nil {
			return err
		}
		return host.OpenBrowser(ctx, address)
	case len(arguments) == 2 && arguments[0] == "pr":
		metadata, err := session.LoadMetadata(configured.DataRoot, arguments[1])
		if err != nil {
			return err
		}
		created, err := host.CreatePullRequest(ctx, metadata)
		if err != nil {
			return err
		}
		if created != "" {
			_, err = fmt.Fprintln(output, created)
		}
		return err
	case len(arguments) >= 1 && arguments[0] == "resume":
		if len(arguments) != 2 || arguments[1] == "" {
			return fmt.Errorf("usage: codegenbox resume <session-id>")
		}
		result, runErr := manager.Resume(ctx, arguments[1], configured.Image, configured.DockerBinary)
		if result.Metadata.ID != "" {
			printResult(ctx, output, result)
		}
		if runErr != nil {
			return fmt.Errorf("Codegenbox session did not complete: %w", runErr)
		}
		return nil
	default:
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
		result, runErr := manager.Start(ctx, workingDirectory, adapter, configured.Image, configured.DockerBinary)
		if result.Metadata.ID != "" {
			printResult(ctx, output, result)
		}
		if runErr != nil {
			return fmt.Errorf("Codegenbox session did not complete: %w", runErr)
		}
		return nil
	}
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
	return "", fmt.Errorf("usage: codegenbox <agent> | codegenbox run <agent> | codegenbox resume <session-id> | codegenbox sessions | codegenbox push <session-id> | codegenbox compare <session-id> | codegenbox pr <session-id> | codegenbox version\nsupported agents: claude, codex, opencode")
}

func printResult(ctx context.Context, output io.Writer, result session.Result) {
	metadata := result.Metadata
	switch metadata.State {
	case session.StateCompleted:
		fmt.Fprintf(output, "Codegenbox session complete.\n\nBranch: %s\nTemporary workspace: removed\n", metadata.SessionBranch)
	case session.StateDirty:
		fmt.Fprintf(output, "Codegenbox session stopped with uncommitted changes.\n\nWorkspace preserved: %s\nResume: codegenbox resume %s\n", metadata.Worktree, metadata.ID)
	case session.StateInterrupted:
		fmt.Fprintf(output, "Codegenbox session was interrupted.\n\nWorkspace: %s\n", workspaceMessage(result))
	case session.StateClean:
		fmt.Fprintf(output, "Codegenbox found a clean workspace but could not remove it.\n\nWorkspace preserved: %s\n", metadata.Worktree)
	}
	printHostSummary(ctx, output, metadata)
}

func printHostSummary(ctx context.Context, output io.Writer, metadata session.Metadata) {
	if metadata.Repository == "" || metadata.BaseBranch == "" || metadata.BaseCommit == "" || metadata.SessionBranch == "" {
		return
	}
	fmt.Fprintln(output, "\nHost session summary:")
	fmt.Fprintf(output, "Source repository: %s\nBase: %s (%s)\nSession branch: %s\n", metadata.Repository, metadata.BaseBranch, metadata.BaseCommit, metadata.SessionBranch)
	if metadata.ImportedCommit == "" {
		fmt.Fprintln(output, "Imported commit: unavailable (the session branch was not imported)")
		return
	}
	summary, err := host.Summarize(ctx, metadata)
	if err != nil {
		fmt.Fprintf(output, "Imported commit: %s\nSummary details unavailable: %v\n", metadata.ImportedCommit, err)
		return
	}
	fmt.Fprintf(output, "Imported commit: %s\nCommits added: %d\n", summary.ImportedCommit, len(summary.Commits))
	for _, commit := range summary.Commits {
		fmt.Fprintf(output, "  %s %s\n", commit.ID, commit.Subject)
	}
	fmt.Fprintf(output, "Changed files: %d (+%d -%d)\n", summary.ChangedFiles, summary.Insertions, summary.Deletions)
	fmt.Fprintf(output, "Push this exact branch: codegenbox push %s\n", metadata.ID)
	if remote, remoteErr := host.DetectGitHubRemote(ctx, metadata.Repository); remoteErr == nil {
		if address, addressErr := host.CompareURL(metadata, remote); addressErr == nil {
			fmt.Fprintf(output, "Open a GitHub compare/PR page: codegenbox compare %s\n%s\n", metadata.ID, address)
		}
	}
}

func printSessions(output io.Writer, dataRoot string) error {
	metadata, err := session.ListMetadata(dataRoot)
	if err != nil {
		return err
	}
	if len(metadata) == 0 {
		_, err := fmt.Fprintln(output, "No Codegenbox sessions.")
		return err
	}
	for _, item := range metadata {
		_, statErr := os.Stat(item.Worktree)
		workspace := "missing"
		if statErr == nil {
			workspace = "present"
		} else if !os.IsNotExist(statErr) {
			workspace = "unavailable"
		}
		if _, err := fmt.Fprintf(output, "%s\tagent=%s\tstate=%s\tworkspace=%s\tbranch=%s\n", item.ID, item.Agent, item.State, workspace, item.SessionBranch); err != nil {
			return err
		}
	}
	return nil
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
