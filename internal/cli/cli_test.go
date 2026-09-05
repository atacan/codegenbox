package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codegenbox/codegenbox/internal/config"
	"github.com/codegenbox/codegenbox/internal/session"
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
}
