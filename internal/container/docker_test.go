package container

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildRunInvocationHasOnlyTheSecureWorktreeMount(t *testing.T) {
	worktree := filepath.Join(t.TempDir(), "worktree")
	invocation, err := BuildRunInvocation("docker", "node:22-bookworm", worktree, []string{"npx", "--yes", "@openai/codex"})
	if err != nil {
		t.Fatalf("BuildRunInvocation: %v", err)
	}
	if invocation.Binary != "docker" {
		t.Fatalf("binary = %q, want docker", invocation.Binary)
	}

	assertArgumentPair(t, invocation.Args, "--cap-drop", "ALL")
	assertArgumentPair(t, invocation.Args, "--security-opt", "no-new-privileges=true")
	assertArgumentPair(t, invocation.Args, "--workdir", "/workspace")
	assertContains(t, invocation.Args, "--rm")
	assertContains(t, invocation.Args, "--interactive")
	assertContains(t, invocation.Args, "--tty")
	assertContains(t, invocation.Args, "node:22-bookworm")

	mounts := valuesAfter(invocation.Args, "--mount")
	if len(mounts) != 1 {
		t.Fatalf("mounts = %#v, want exactly one", mounts)
	}
	wantMount := "type=bind,src=" + filepath.Clean(worktree) + ",dst=/workspace"
	if mounts[0] != wantMount {
		t.Fatalf("mount = %q, want %q", mounts[0], wantMount)
	}

	command := strings.Join(invocation.Args, "\x00")
	for _, forbidden := range []string{
		"--privileged",
		"/var/run/docker.sock",
		"/Users/example",
		"/home/example",
		".ssh",
		".codex",
	} {
		if strings.Contains(command, forbidden) {
			t.Errorf("Docker invocation unexpectedly contains %q: %#v", forbidden, invocation.Args)
		}
	}
}

func TestBuildRunInvocationRejectsAmbiguousInputs(t *testing.T) {
	if _, err := BuildRunInvocation("docker", "--privileged", "/tmp/worktree", []string{"codex"}); err == nil {
		t.Fatal("option-like image was accepted")
	}
	if _, err := BuildRunInvocation("docker", "image", "/tmp/a,b", []string{"codex"}); err == nil {
		t.Fatal("comma-containing mount source was accepted")
	}
}

func assertContains(t *testing.T, values []string, want string) {
	t.Helper()
	for _, value := range values {
		if value == want {
			return
		}
	}
	t.Fatalf("%q not found in %#v", want, values)
}

func assertArgumentPair(t *testing.T, values []string, flag, want string) {
	t.Helper()
	for index, value := range values[:len(values)-1] {
		if value == flag && values[index+1] == want {
			return
		}
	}
	t.Fatalf("%s %s not found in %#v", flag, want, values)
}

func valuesAfter(values []string, flag string) []string {
	var results []string
	for index, value := range values[:len(values)-1] {
		if value == flag {
			results = append(results, values[index+1])
		}
	}
	return results
}
