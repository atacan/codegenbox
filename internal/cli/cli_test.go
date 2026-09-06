package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codegenbox/codegenbox/internal/config"
	"github.com/codegenbox/codegenbox/internal/host"
	"github.com/codegenbox/codegenbox/internal/session"
	"github.com/codegenbox/codegenbox/internal/terminal"
	buildversion "github.com/codegenbox/codegenbox/internal/version"
)

func TestVersionDoesNotRequireRuntimeConfiguration(t *testing.T) {
	original := buildversion.Version
	buildversion.Version = "0.1.0"
	t.Cleanup(func() { buildversion.Version = original })

	var output bytes.Buffer
	configured := false
	err := Run(context.Background(), []string{"version"}, Environment{
		Stdout: &output,
		Config: func() (config.Config, error) {
			configured = true
			return config.Config{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if configured {
		t.Fatal("version unexpectedly loaded runtime configuration")
	}
	if output.String() != "codegenbox 0.1.0\n" {
		t.Fatalf("version output = %q", output.String())
	}
}

func TestSessionsListsNonSensitiveRecordsInStableOrder(t *testing.T) {
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"beta-20260903-193012-a82f", "alpha-20260903-193012-a82f"} {
		if err := os.MkdirAll(filepath.Join(root, "sessions", id), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := session.WriteMetadata(root, session.Metadata{ID: id, Repository: "/source", Worktree: filepath.Join(root, "sessions", id), Agent: "codex", BaseBranch: "main", BaseCommit: "abcdef", SessionBranch: "codegenbox/" + id, State: session.StateDirty}); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	err = Run(context.Background(), []string{"sessions"}, Environment{Stdout: &output, Config: func() (config.Config, error) {
		return config.Config{DataRoot: root, Image: "image", DockerBinary: "docker"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.HasPrefix(text, "alpha-20260903-193012-a82f\tagent=codex") || strings.Contains(text, "token") || strings.Contains(text, "/source") {
		t.Fatalf("unexpected sessions output: %q", text)
	}
	if err := Run(context.Background(), []string{"resume"}, Environment{Config: func() (config.Config, error) { return config.Config{DataRoot: root}, nil }}); err == nil {
		t.Fatal("resume without an ID accepted")
	}
	for _, arguments := range [][]string{{"continue"}, {"continue", ""}, {"continue", "one", "two"}} {
		err := Run(context.Background(), arguments, Environment{Config: func() (config.Config, error) { return config.Config{DataRoot: root}, nil }})
		if err == nil || err.Error() != "usage: codegenbox continue <session-id>" {
			t.Fatalf("continue arguments %q error = %v", arguments, err)
		}
	}
	if _, err := parseArguments([]string{"unexpected", "argument"}); err == nil || !strings.Contains(err.Error(), "codegenbox continue <session-id>") {
		t.Fatalf("general usage = %v", err)
	}
}

func TestSessionSummaryUsesPortablePlainTextBullets(t *testing.T) {
	var output bytes.Buffer
	printResult(context.Background(), &output, terminal.New(false), session.Result{
		Metadata: session.Metadata{
			ID:            "abc123",
			SessionBranch: "codegenbox/abc123",
			State:         session.StateCompleted,
		},
		WorkspaceRemoved: true,
	})

	want := "Codegenbox session complete.\n\nSession\n- ID: abc123\n- Branch: codegenbox/abc123\n- Workspace: removed\n\nNext action\n- Continue: codegenbox continue abc123\n"
	if got := output.String(); got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

func TestSessionSummaryStylesOnlyActionableCommandValue(t *testing.T) {
	var output bytes.Buffer
	printResult(context.Background(), &output, terminal.New(true), session.Result{
		Metadata: session.Metadata{
			ID:            "abc123",
			SessionBranch: "codegenbox/abc123",
			State:         session.StateCompleted,
		},
		WorkspaceRemoved: true,
	})

	got := output.String()
	if !strings.Contains(got, "- Continue: \x1b[1;36mcodegenbox continue abc123\x1b[0m\n") {
		t.Fatalf("action command was not styled: %q", got)
	}
	if strings.Contains(got, "\x1b[1;36m- Continue:") || strings.Contains(got, "\x1b[1;36mcodegenbox/abc123") {
		t.Fatalf("labels or informational values were styled as commands: %q", got)
	}
}

func TestActionRowsUseOneCommandStylingConvention(t *testing.T) {
	var output bytes.Buffer
	style := terminal.New(true)
	for _, action := range []struct {
		label   string
		command string
	}{
		{"Push branch", "codegenbox push abc123"},
		{"Open comparison", "codegenbox compare abc123"},
		{"Create pull request", "codegenbox pr abc123"},
		{"Resume", "codegenbox resume abc123"},
		{"Continue", "codegenbox continue abc123"},
	} {
		printAction(&output, style, action.label, action.command)
	}

	for _, line := range strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n") {
		if !strings.HasPrefix(line, "- ") || !strings.Contains(line, ": \x1b[1;36mcodegenbox ") || !strings.HasSuffix(line, "\x1b[0m") {
			t.Fatalf("action line does not use the shared command convention: %q", line)
		}
	}
}

func TestParseStartArgumentsAcceptsOnlyExplicitOpenPRFlag(t *testing.T) {
	for _, test := range []struct {
		arguments  []string
		wantAgent  string
		wantAction session.PostExitAction
		wantError  bool
	}{
		{[]string{"codex"}, "codex", session.PostExitActionNone, false},
		{[]string{"codex", "--open-pr"}, "codex", session.PostExitActionOpenCompare, false},
		{[]string{"run", "claude", "--open-pr"}, "claude", session.PostExitActionOpenCompare, false},
		{[]string{"codex", "--open-pr", "--open-pr"}, "", session.PostExitActionNone, true},
		{[]string{"run", "codex", "--unknown"}, "", session.PostExitActionNone, true},
	} {
		t.Run(strings.Join(test.arguments, " "), func(t *testing.T) {
			start, err := parseStartArguments(test.arguments)
			if test.wantError {
				if err == nil {
					t.Fatal("parseStartArguments unexpectedly succeeded")
				}
				return
			}
			if err != nil || start.Agent != test.wantAgent || start.PostExitAction != test.wantAction {
				t.Fatalf("parseStartArguments(%q) = %#v, %v", test.arguments, start, err)
			}
		})
	}
}

func TestRequestedPostExitActionRunsOnlyForCompletedNewCommit(t *testing.T) {
	metadata := session.Metadata{
		ID:             "abc123",
		BaseCommit:     "base",
		ImportedCommit: "imported",
		SessionBranch:  "codegenbox/abc123",
		State:          session.StateCompleted,
		PostExitAction: session.PostExitActionOpenCompare,
	}
	var output bytes.Buffer
	calls := 0
	err := runRequestedPostExitAction(context.Background(), &output, terminal.New(false), metadata, func(_ context.Context, got session.Metadata) (host.CompareHandoff, error) {
		calls++
		if got != metadata {
			t.Fatalf("handoff metadata = %#v, want %#v", got, metadata)
		}
		return host.CompareHandoff{URL: "https://github.com/acme/project/compare/main...codegenbox/abc123?expand=1"}, nil
	})
	if err != nil || calls != 1 || !strings.Contains(output.String(), "Comparison URL: https://github.com/") || !strings.Contains(output.String(), "Pushed codegenbox/abc123 and opened") {
		t.Fatalf("completed handoff output=%q calls=%d err=%v", output.String(), calls, err)
	}

	for _, ineligible := range []session.Metadata{
		{PostExitAction: session.PostExitActionNone, State: session.StateCompleted, BaseCommit: "base", ImportedCommit: "imported"},
		{PostExitAction: session.PostExitActionOpenCompare, State: session.StateDirty, BaseCommit: "base", ImportedCommit: "imported"},
		{PostExitAction: session.PostExitActionOpenCompare, State: session.StateInterrupted, BaseCommit: "base", ImportedCommit: "imported"},
		{PostExitAction: session.PostExitActionOpenCompare, State: session.StateCompleted, BaseCommit: "base", ImportedCommit: "base"},
	} {
		if err := runRequestedPostExitAction(context.Background(), &output, terminal.New(false), ineligible, func(context.Context, session.Metadata) (host.CompareHandoff, error) {
			calls++
			return host.CompareHandoff{}, nil
		}); err != nil {
			t.Fatalf("ineligible handoff = %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("ineligible session ran automatic handoff %d times", calls)
	}
}

func TestRequestedPostExitActionPrintsRecoveryURLOnBrowserFailure(t *testing.T) {
	metadata := session.Metadata{BaseCommit: "base", ImportedCommit: "imported", State: session.StateCompleted, PostExitAction: session.PostExitActionOpenCompare}
	var output bytes.Buffer
	err := runRequestedPostExitAction(context.Background(), &output, terminal.New(false), metadata, func(context.Context, session.Metadata) (host.CompareHandoff, error) {
		return host.CompareHandoff{URL: "https://github.com/acme/project/compare/main...codegenbox/abc123?expand=1"}, errors.New("desktop unavailable")
	})
	if err == nil || !strings.Contains(output.String(), "Comparison URL: https://github.com/") || !strings.Contains(output.String(), "Automatic GitHub handoff failed") {
		t.Fatalf("browser failure output=%q err=%v", output.String(), err)
	}
}
