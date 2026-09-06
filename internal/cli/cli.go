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
	"github.com/codegenbox/codegenbox/internal/doctor"
	"github.com/codegenbox/codegenbox/internal/host"
	"github.com/codegenbox/codegenbox/internal/session"
	"github.com/codegenbox/codegenbox/internal/terminal"
	buildversion "github.com/codegenbox/codegenbox/internal/version"
)

type Environment struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getwd  func() (string, error)
	Config func() (config.Config, error)
	Runner container.Runner
	// ImageChecker lets tests replace the host Docker inspection. Production
	// leaves it nil and uses the real inspector below.
	ImageChecker func(context.Context, string, string) error
}

func Run(ctx context.Context, arguments []string, environment Environment) error {
	output := chooseWriter(environment.Stdout, os.Stdout)
	style := terminal.ForWriter(output)
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
	usingDefaultRunner := runner == nil
	if runner == nil {
		runner = container.ExecRunner{
			Stdin:  chooseReader(environment.Stdin, os.Stdin),
			Stdout: chooseWriter(environment.Stdout, os.Stdout),
			Stderr: chooseWriter(environment.Stderr, os.Stderr),
		}
	}

	imageChecker := environment.ImageChecker
	if imageChecker == nil && usingDefaultRunner {
		imageChecker = func(ctx context.Context, binary, image string) error {
			return container.EnsureImageCompatibility(ctx, container.ExecInspector{}, binary, image)
		}
	}
	manager := session.Manager{DataRoot: configured.DataRoot, Runner: runner, Limits: container.ResourceLimits{PIDs: configured.Limits.PIDs, Memory: configured.Limits.Memory, CPUs: configured.Limits.CPUs}, ImageChecker: imageChecker}
	switch {
	case len(arguments) == 1 && arguments[0] == "doctor":
		checks := doctor.Run(ctx, doctor.ExecCommand{}, configured.DockerBinary, configured.Image, configured.DataRoot)
		failed := false
		for _, check := range checks {
			if check.Err != nil {
				failed = true
				fmt.Fprintf(output, "✗ %s: %v\n", check.Name, check.Err)
			} else {
				fmt.Fprintf(output, "✓ %s\n", check.Name)
			}
		}
		if failed {
			return fmt.Errorf("Codegenbox is not ready")
		}
		_, err := fmt.Fprintln(output, "Codegenbox is ready.")
		return err
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
		fmt.Fprintln(output, style.Success("Pushed "+metadata.SessionBranch+" to its source origin."))
		if result.GitHub != nil {
			address, urlErr := host.CompareURL(metadata, *result.GitHub)
			if urlErr == nil {
				fmt.Fprintln(output, "\nNext action")
				printAction(output, style, "Open comparison", "codegenbox compare "+metadata.ID)
				printAction(output, style, "Create pull request", "codegenbox pr "+metadata.ID)
				printSummaryLine(output, "Comparison URL", address)
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
		if _, err := manager.RecoverOrphans(ctx); err != nil {
			return fmt.Errorf("recover interrupted sessions: %w", err)
		}
		result, runErr := manager.Resume(ctx, arguments[1], configured.Image, configured.DockerBinary)
		if result.Metadata.ID != "" {
			printResult(ctx, output, style, result)
		}
		if runErr != nil {
			return fmt.Errorf("Codegenbox session did not complete: %w", runErr)
		}
		return nil
	case len(arguments) >= 1 && arguments[0] == "continue":
		if len(arguments) != 2 || arguments[1] == "" {
			return fmt.Errorf("usage: codegenbox continue <session-id>")
		}
		if _, err := manager.RecoverOrphans(ctx); err != nil {
			return fmt.Errorf("recover interrupted sessions: %w", err)
		}
		result, runErr := manager.Continue(ctx, arguments[1], configured.Image, configured.DockerBinary)
		if result.Metadata.ID != "" {
			printResult(ctx, output, style, result)
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
		if _, err := manager.RecoverOrphans(ctx); err != nil {
			return fmt.Errorf("recover interrupted sessions: %w", err)
		}
		result, runErr := manager.Start(ctx, workingDirectory, adapter, configured.Image, configured.DockerBinary)
		if result.Metadata.ID != "" {
			printResult(ctx, output, style, result)
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
	return "", fmt.Errorf("usage: codegenbox <agent> | codegenbox run <agent> | codegenbox resume <session-id> | codegenbox continue <session-id> | codegenbox sessions | codegenbox doctor | codegenbox push <session-id> | codegenbox compare <session-id> | codegenbox pr <session-id> | codegenbox version\nsupported agents: claude, codex, opencode")
}

func printResult(ctx context.Context, output io.Writer, style terminal.Style, result session.Result) {
	metadata := result.Metadata
	switch metadata.State {
	case session.StateCompleted:
		fmt.Fprintln(output, style.Success("Codegenbox session complete."))
		fmt.Fprintln(output, "\nSession")
		printSummaryLine(output, "ID", metadata.ID)
		printSummaryLine(output, "Branch", metadata.SessionBranch)
		printSummaryLine(output, "Workspace", "removed")
		fmt.Fprintln(output, "\nNext action")
		printAction(output, style, "Continue", "codegenbox continue "+metadata.ID)
	case session.StateDirty:
		fmt.Fprintln(output, style.Warning("Codegenbox session stopped with uncommitted changes."))
		fmt.Fprintln(output, "\nSession")
		printSummaryLine(output, "ID", metadata.ID)
		printSummaryLine(output, "Workspace", metadata.Worktree)
		fmt.Fprintln(output, "\nNext action")
		printAction(output, style, "Resume", "codegenbox resume "+metadata.ID)
	case session.StateInterrupted:
		fmt.Fprintln(output, style.Warning("Codegenbox session was interrupted."))
		fmt.Fprintln(output, "\nSession")
		printSummaryLine(output, "ID", metadata.ID)
		printSummaryLine(output, "Workspace", workspaceMessage(result))
		if !result.WorkspaceRemoved {
			fmt.Fprintln(output, "\nNext action")
			printAction(output, style, "Resume", "codegenbox resume "+metadata.ID)
		}
	case session.StateClean:
		fmt.Fprintln(output, style.Warning("Codegenbox found a clean workspace but could not remove it."))
		fmt.Fprintln(output, "\nSession")
		printSummaryLine(output, "ID", metadata.ID)
		printSummaryLine(output, "Workspace", metadata.Worktree)
		fmt.Fprintln(output, "\nNext action")
		printAction(output, style, "Resume", "codegenbox resume "+metadata.ID)
	}
	printHostSummary(ctx, output, style, metadata)
}

func printHostSummary(ctx context.Context, output io.Writer, style terminal.Style, metadata session.Metadata) {
	if metadata.Repository == "" || metadata.BaseBranch == "" || metadata.BaseCommit == "" || metadata.SessionBranch == "" {
		return
	}
	fmt.Fprintln(output, "\nHost session summary:")
	printSummaryLine(output, "Source repository", metadata.Repository)
	printSummaryLine(output, "Base", fmt.Sprintf("%s (%s)", metadata.BaseBranch, metadata.BaseCommit))
	printSummaryLine(output, "Session branch", metadata.SessionBranch)
	if metadata.ImportedCommit == "" {
		printSummaryLine(output, "Imported commit", "unavailable (the session branch was not imported)")
		return
	}
	summary, err := host.Summarize(ctx, metadata)
	if err != nil {
		printSummaryLine(output, "Imported commit", metadata.ImportedCommit)
		printSummaryLine(output, "Summary details unavailable", err.Error())
		return
	}
	printSummaryLine(output, "Imported commit", summary.ImportedCommit)
	printSummaryLine(output, "Commits added", fmt.Sprintf("%d", len(summary.Commits)))
	for _, commit := range summary.Commits {
		printSummaryLine(output, "Commit", commit.ID+" "+commit.Subject)
	}
	printSummaryLine(output, "Changed files", fmt.Sprintf("%d (+%d -%d)", summary.ChangedFiles, summary.Insertions, summary.Deletions))
	printAction(output, style, "Push branch", "codegenbox push "+metadata.ID)
	if remote, remoteErr := host.DetectGitHubRemote(ctx, metadata.Repository); remoteErr == nil {
		if address, addressErr := host.CompareURL(metadata, remote); addressErr == nil {
			printAction(output, style, "Open comparison", "codegenbox compare "+metadata.ID)
			printAction(output, style, "Create pull request", "codegenbox pr "+metadata.ID)
			printSummaryLine(output, "Comparison URL", address)
		}
	}
}

func printSummaryLine(output io.Writer, label, value string) {
	fmt.Fprintf(output, "- %s: %s\n", label, value)
}

func printAction(output io.Writer, style terminal.Style, label, command string) {
	printSummaryLine(output, label, style.Command(command))
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
