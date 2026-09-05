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
		{Claude, []string{"claude"}, []string{"/home/agent/.claude"}, []string{"HOME=/home/agent"}},
		{Codex, []string{"codex", "--dangerously-bypass-approvals-and-sandbox"}, []string{"/home/agent/.codex"}, []string{"CODEX_HOME=/home/agent/.codex", "HOME=/home/agent"}},
		{OpenCode, []string{"opencode"}, []string{"/home/agent/.config/opencode", "/home/agent/.local/share/opencode"}, []string{"HOME=/home/agent", "XDG_CONFIG_HOME=/home/agent/.config", "XDG_DATA_HOME=/home/agent/.local/share"}},
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

func TestResolveStateCanonicalizesAliasesAndRejectsCrossAgentOrDuplicateSources(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	homeAlias := filepath.Join(base, "home-alias")
	if err := os.Symlink(home, homeAlias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeAlias)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(homeAlias, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(homeAlias, ".local", "share"))
	claude, _ := Lookup(Claude)
	t.Setenv("CODEGENBOX_CLAUDE_STATE_DIR", homeAlias)
	if _, err := ResolveState(claude); err == nil {
		t.Fatal("host-home symlink alias accepted")
	}
	config := filepath.Join(base, "config")
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	configAlias := filepath.Join(base, "config-alias")
	if err := os.Symlink(config, configAlias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEGENBOX_CLAUDE_STATE_DIR", configAlias)
	t.Setenv("XDG_CONFIG_HOME", configAlias)
	if _, err := ResolveState(claude); err == nil {
		t.Fatal("generic config symlink alias accepted")
	}
	codexAlias := filepath.Join(base, "codex-alias")
	if err := os.Symlink(filepath.Join(home, ".codex"), codexAlias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEGENBOX_CLAUDE_STATE_DIR", codexAlias)
	if _, err := ResolveState(claude); err == nil {
		t.Fatal("cross-agent symlink alias accepted")
	}
	custom := filepath.Join(base, "custom")
	if err := os.MkdirAll(custom, 0o700); err != nil {
		t.Fatal(err)
	}
	customAlias := filepath.Join(base, "custom-alias")
	if err := os.Symlink(custom, customAlias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEGENBOX_CLAUDE_STATE_DIR", customAlias)
	canonicalCustom, err := filepath.EvalSymlinks(custom)
	if err != nil {
		t.Fatal(err)
	}
	mounts, err := ResolveState(claude)
	if err != nil || len(mounts) != 1 || mounts[0].Source != canonicalCustom {
		t.Fatalf("valid canonical symlink state = %#v, %v", mounts, err)
	}
	openCode, _ := Lookup(OpenCode)
	t.Setenv("CODEGENBOX_OPENCODE_CONFIG_DIR", customAlias)
	t.Setenv("CODEGENBOX_OPENCODE_DATA_DIR", customAlias)
	if _, err := ResolveState(openCode); err == nil {
		t.Fatal("duplicate canonical OpenCode state sources accepted")
	}
}

func TestValidateStatePathRejectsCrossAgentAndBroadPaths(t *testing.T) {
	home := "/Users/example"
	defaults := map[string]string{"CLAUDE_STATE_DIR": home + "/.claude", "CODEX_STATE_DIR": home + "/.codex", "OPENCODE_CONFIG_DIR": home + "/.config/opencode", "OPENCODE_DATA_DIR": home + "/.local/share/opencode"}
	for _, path := range []string{home, home + "/.config", home + "/.local/share", home + "/.codex", home + "/state,bad"} {
		if err := validateStatePath(Claude, "CLAUDE_STATE_DIR", path, []string{home, home + "/.config", home + "/.local/share", home + "/.local"}, defaults); err == nil {
			t.Fatalf("accepted hostile state path %q", path)
		}
	}
}
