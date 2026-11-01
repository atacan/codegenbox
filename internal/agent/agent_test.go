package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAdaptersHaveDistinctCommandsStateAndEnvironment(t *testing.T) {
	tests := []struct {
		name         string
		command      []string
		destinations []string
		environment  []string
	}{
		{Claude, []string{"claude"}, []string{"/home/agent/.claude"}, []string{"CLAUDE_CONFIG_DIR=/home/agent/.claude", "HOME=/home/agent"}},
		{Codex, []string{"codex", "--dangerously-bypass-approvals-and-sandbox"}, []string{"/home/agent/.codex", "/home/agent/.agents/skills"}, []string{"CODEX_HOME=/home/agent/.codex", "HOME=/home/agent"}},
		{OpenCode, []string{"opencode"}, []string{"/home/agent/.config/opencode", "/home/agent/.local/share/opencode", "/home/agent/.agents/skills"}, []string{"HOME=/home/agent", "XDG_CONFIG_HOME=/home/agent/.config", "XDG_DATA_HOME=/home/agent/.local/share"}},
		{OriClaude, []string{"ori", "claude"}, []string{"/home/agent/.ori", "/home/agent/.claude"}, []string{"CLAUDE_CONFIG_DIR=/home/agent/.claude", "HOME=/home/agent"}},
		{OriCodex, []string{"ori", "codex", "--dangerously-bypass-approvals-and-sandbox"}, []string{"/home/agent/.ori", "/home/agent/.codex", "/home/agent/.agents/skills"}, []string{"CODEX_HOME=/home/agent/.codex", "HOME=/home/agent"}},
		{OriOpenCode, []string{"ori", "opencode"}, []string{"/home/agent/.ori", "/home/agent/.config/opencode", "/home/agent/.local/share/opencode", "/home/agent/.agents/skills"}, []string{"HOME=/home/agent", "XDG_CONFIG_HOME=/home/agent/.config", "XDG_DATA_HOME=/home/agent/.local/share"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, err := Lookup(test.name)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(adapter.Command, test.command) {
				t.Fatalf("command = %#v, want %#v", adapter.Command, test.command)
			}
			if EnvironmentPairs(adapter) == nil || !reflect.DeepEqual(EnvironmentPairs(adapter), test.environment) {
				t.Fatalf("environment = %#v, want %#v", EnvironmentPairs(adapter), test.environment)
			}
			var got []string
			for _, state := range adapter.State {
				got = append(got, state.Destination)
			}
			if !reflect.DeepEqual(got, test.destinations) {
				t.Fatalf("state destinations = %#v, want %#v", got, test.destinations)
			}
		})
	}
	if _, err := Lookup("unknown"); err == nil {
		t.Fatal("unknown adapter was accepted")
	}
}

func TestResolveAddsValidatedModelOnlyToOriCommands(t *testing.T) {
	for _, test := range []struct {
		name    string
		command []string
	}{
		{OriClaude, []string{"ori", "claude", "--model", "anthropic/claude-sonnet-4"}},
		{OriCodex, []string{"ori", "codex", "--model", "openai/gpt-5.2", "--dangerously-bypass-approvals-and-sandbox"}},
		{OriOpenCode, []string{"ori", "opencode", "--model", "openrouter/auto"}},
	} {
		adapter, err := Resolve(test.name, test.command[3])
		if err != nil {
			t.Fatal(err)
		}
		if adapter.Model != test.command[3] || !reflect.DeepEqual(adapter.Command, test.command) {
			t.Fatalf("Resolve(%q) = command %#v model %q", test.name, adapter.Command, adapter.Model)
		}
	}
	for _, test := range []struct{ name, model string }{
		{Claude, "openrouter/auto"},
		{OriClaude, "--bad"},
		{OriClaude, "two models"},
		{OriClaude, "line\nbreak"},
	} {
		if _, err := Resolve(test.name, test.model); err == nil {
			t.Fatalf("Resolve(%q, %q) accepted invalid model", test.name, test.model)
		}
	}
}

func TestResolveStateRequiresPrivateSessionRoot(t *testing.T) {
	for _, name := range Supported() {
		adapter, _ := Lookup(name)
		if _, err := ResolveState(adapter); err == nil {
			t.Fatalf("%s accepted state resolution without private session root", name)
		}
	}
}

func TestResolveStateForOriIncludesOnlyOriAndSelectedHarnessState(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	config := filepath.Join(home, ".config")
	data := filepath.Join(home, ".local", "share")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("XDG_DATA_HOME", data)

	adapter, err := Lookup(OriOpenCode)
	if err != nil {
		t.Fatal(err)
	}
	canonicalBase, _ := filepath.EvalSymlinks(base)
	mounts, err := ResolveSessionState(adapter, filepath.Join(canonicalBase, "private"))
	if err != nil {
		t.Fatal(err)
	}
	wantDestinations := []string{"/home/agent/.ori", "/home/agent/.config/opencode", "/home/agent/.local/share/opencode", "/home/agent/.agents/skills"}
	if len(mounts) != len(wantDestinations) {
		t.Fatalf("mounts = %#v", mounts)
	}
	for index, mount := range mounts {
		if mount.Agent != OriOpenCode || mount.Destination != wantDestinations[index] {
			t.Fatalf("mount %d = %#v", index, mount)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".ori")); !os.IsNotExist(err) {
		t.Fatalf("host Ori state was created: %v", err)
	}

	t.Setenv("CODEGENBOX_ORI_STATE_DIR", filepath.Join(home, ".codex"))
	if _, err := ResolveSessionState(adapter, filepath.Join(canonicalBase, "other-private")); err == nil {
		t.Fatal("Ori state override accepted Codex state")
	}
}

func TestValidateStatePathRejectsCrossAgentAndBroadPaths(t *testing.T) {
	home := "/Users/example"
	defaults := map[string]string{"CLAUDE_STATE_DIR": home + "/.claude", "CODEX_STATE_DIR": home + "/.codex", "OPENCODE_CONFIG_DIR": home + "/.config/opencode", "OPENCODE_DATA_DIR": home + "/.local/share/opencode", "ORI_STATE_DIR": home + "/.ori"}
	for _, path := range []string{home, home + "/.config", home + "/.local/share", home + "/.codex", home + "/.ori", home + "/state,bad"} {
		if err := validateStatePath(Claude, "CLAUDE_STATE_DIR", path, []string{home, home + "/.config", home + "/.local/share", home + "/.local"}, defaults); err == nil {
			t.Fatalf("accepted hostile state path %q", path)
		}
	}
}
