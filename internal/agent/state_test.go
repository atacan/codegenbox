package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func seedTestEnvironment(t *testing.T) string {
	t.Helper()
	home := isolatedCodexTestHome(t)
	for _, key := range []string{"CODEGENBOX_CLAUDE_AUTH_FILE", "CODEGENBOX_OPENCODE_AUTH_FILE", "CODEGENBOX_ORI_AUTH_FILE"} {
		t.Setenv(key, "")
	}
	return home
}
func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestEveryAdapterSeedsSelectedArtifactsIntoPrivateStateOnce(t *testing.T) {
	for _, name := range Supported() {
		t.Run(name, func(t *testing.T) {
			home := seedTestEnvironment(t)
			fixtures := map[string]string{
				".claude/.credentials.json":                       `{"fake":"claude-login"}`,
				".claude/settings.json":                           `{"model":"fixture-claude","hooks":{"Stop":[]},"env":{"HOME":"/host","CLAUDE_CONFIG_DIR":"/host/claude","EDITOR_MODE":"vim"}}`,
				".claude/CLAUDE.md":                               "Claude instructions",
				".claude/skills/claude-skill/SKILL.md":            "Claude skill",
				".claude/agents/review.md":                        "Claude agent",
				".claude/commands/review.md":                      "Claude command",
				".claude/rules/style.md":                          "Claude rules",
				".codex/auth.json":                                `{"OPENAI_API_KEY":"fake-codex-key"}`,
				".codex/config.toml":                              "model = \"fixture-codex\"\nsqlite_home = \"/host/db\"\nlog_dir = \"/host/log\"\nnotify = [\"/host/notify\"]\n[hooks]\n[projects.\"/host/repo\"]\ntrust_level = \"trusted\"\n",
				".codex/review.config.toml":                       "model_reasoning_effort = \"high\"\ncli_auth_credentials_store = \"keyring\"\n",
				".codex/AGENTS.md":                                "Codex instructions",
				".codex/skills/legacy-skill/SKILL.md":             "Legacy skill",
				".config/opencode/opencode.jsonc":                 "{ // preserve preferences, omit host integrations\n \"model\":\"fixture/opencode\", \"plugin\":[\"file:///host/plugin\"], \"mcp\": {\"local\":{\"type\":\"local\",\"command\":[\"/host/server\"]},\"remote\":{\"type\":\"remote\",\"url\":\"https://example.invalid/mcp\"}}, }",
				".config/opencode/tui.json":                       `{"theme":"fixture-theme"}`,
				".config/opencode/skills/opencode-skill/SKILL.md": "OpenCode skill",
				".config/opencode/agents/review.md":               "OpenCode agent",
				".local/share/opencode/auth.json":                 `{"fake":"opencode-login"}`,
				".ori/credentials.json":                           `{"key":"fake-ori-key"}`,
				".ori/config.json":                                `{"loginMode":"api-key","history":"/host/history"}`,
				".agents/skills/shared-skill/SKILL.md":            "Shared skill",
			}
			for path, contents := range fixtures {
				writeCodexFixture(t, filepath.Join(home, path), contents)
			}
			for _, directory := range []string{".claude", ".codex", ".config/opencode", ".local/share/opencode", ".ori"} {
				for _, runtime := range []string{"history.jsonl", "state.sqlite", "cache/data", "sessions/transcript.json", "plugins/cache.json"} {
					writeCodexFixture(t, filepath.Join(home, directory, runtime), "host-runtime-sentinel")
				}
			}
			adapter, _ := Lookup(name)
			root := filepath.Join(home, "codegenbox", "agent-state", "session-one")
			mounts, err := ResolveSessionState(adapter, root)
			if err != nil {
				t.Fatal(err)
			}
			if len(mounts) != len(adapter.State) {
				t.Fatalf("mount count %d", len(mounts))
			}
			for index, mount := range mounts {
				key := adapter.State[index].Key
				expected := filepath.Join(root, privateStateNames[key])
				if mount.Source != expected || mount.ReadOnly {
					t.Fatalf("unsafe mount %#v", mount)
				}
				for _, runtime := range []string{"history.jsonl", "state.sqlite", "cache", "sessions", "plugins"} {
					if _, err := os.Lstat(filepath.Join(expected, runtime)); !os.IsNotExist(err) {
						t.Fatalf("runtime state copied into %s", key)
					}
				}
				switch key {
				case "CLAUDE_STATE_DIR":
					settings := readTestFile(t, filepath.Join(expected, "settings.json"))
					if strings.Contains(settings, "/host") || strings.Contains(settings, "hooks") || !strings.Contains(settings, "fixture-claude") {
						t.Fatal("Claude settings were not portable")
					}
					if readTestFile(t, filepath.Join(expected, "CLAUDE.md")) != "Claude instructions" {
						t.Fatal("Claude instructions missing")
					}
					_ = readTestFile(t, filepath.Join(expected, "skills/claude-skill/SKILL.md"))
				case "CODEX_STATE_DIR":
					settings := readTestFile(t, filepath.Join(expected, "config.toml"))
					if strings.Contains(settings, "/host") || strings.Contains(settings, "notify") || !strings.Contains(settings, "fixture-codex") || !strings.Contains(settings, "/home/agent/.codex") {
						t.Fatal("Codex settings were not portable")
					}
					profile := readTestFile(t, filepath.Join(expected, "review.config.toml"))
					if strings.Contains(profile, "keyring") || !strings.Contains(profile, "high") {
						t.Fatal("Codex profile not sanitized")
					}
				case "OPENCODE_CONFIG_DIR":
					settings := readTestFile(t, filepath.Join(expected, "opencode.jsonc"))
					if strings.Contains(settings, "/host") || strings.Contains(settings, "plugin") || !strings.Contains(settings, "fixture/opencode") || !strings.Contains(settings, "example.invalid") {
						t.Fatal("OpenCode settings were not portable")
					}
					_ = readTestFile(t, filepath.Join(expected, "skills/opencode-skill/SKILL.md"))
				case "ORI_STATE_DIR":
					if strings.Contains(readTestFile(t, filepath.Join(expected, "config.json")), "/host") {
						t.Fatal("unknown Ori settings were copied")
					}
				case "AGENT_SKILLS_DIR":
					_ = readTestFile(t, filepath.Join(expected, "shared-skill/SKILL.md"))
					target, err := os.Readlink(filepath.Join(expected, "srcmv"))
					if err != nil || target != bundledSkillTarget {
						t.Fatal("image bundled skill fallback lost")
					}
				}
				auth := map[string]string{"CLAUDE_STATE_DIR": ".credentials.json", "CODEX_STATE_DIR": "auth.json", "OPENCODE_DATA_DIR": "auth.json", "ORI_STATE_DIR": "credentials.json"}[key]
				if auth != "" {
					destination := filepath.Join(expected, auth)
					original, _ := os.Stat(destination)
					sourceName := auth
					sourcePath := filepath.Join(map[string]string{"CLAUDE_STATE_DIR": filepath.Join(home, ".claude"), "CODEX_STATE_DIR": filepath.Join(home, ".codex"), "OPENCODE_DATA_DIR": filepath.Join(home, ".local/share/opencode"), "ORI_STATE_DIR": filepath.Join(home, ".ori")}[key], sourceName)
					source, _ := os.Stat(sourcePath)
					if original == nil || source == nil || os.SameFile(original, source) {
						t.Fatal("auth was not copied to an independent file")
					}
					writeCodexFixture(t, destination, "private-refreshed-fixture")
				}
			}
			if _, err := ResolveSessionState(adapter, root); err != nil {
				t.Fatal(err)
			}
			// A deliberate logout/deletion must not restore the host login or removed settings.
			for _, mount := range mounts {
				for _, file := range []string{"auth.json", ".credentials.json", "credentials.json", "settings.json", "config.toml", "opencode.jsonc"} {
					if err := os.Remove(filepath.Join(mount.Source, file)); err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
				}
			}
			if _, err := ResolveSessionState(adapter, root); err != nil {
				t.Fatal(err)
			}
			for _, mount := range mounts {
				for _, file := range []string{"auth.json", ".credentials.json", "credentials.json", "settings.json", "config.toml", "opencode.jsonc"} {
					if _, err := os.Stat(filepath.Join(mount.Source, file)); !os.IsNotExist(err) {
						t.Fatalf("deleted private file %s was reseeded", file)
					}
				}
			}
			other := filepath.Join(home, "codegenbox", "agent-state", "session-two")
			second, err := ResolveSessionState(adapter, other)
			if err != nil {
				t.Fatal(err)
			}
			for index, mount := range second {
				if mount.Source == mounts[index].Source {
					t.Fatal("sessions share state")
				}
			}
			for path, contents := range fixtures {
				if readTestFile(t, filepath.Join(home, path)) != contents {
					t.Fatalf("host fixture changed: %s", path)
				}
			}
		})
	}
}

func TestAuthoredSkillSnapshotsDereferenceOnlySafeFiles(t *testing.T) {
	home := seedTestEnvironment(t)
	external := filepath.Join(home, "authored-skill")
	writeCodexFixture(t, filepath.Join(external, "SKILL.md"), "Symlinked skill")
	writeCodexFixture(t, filepath.Join(external, "references/guide.md"), "Reference")
	writeCodexFixture(t, filepath.Join(external, "scripts/run.sh"), "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(external, "scripts/run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCodexFixture(t, filepath.Join(external, "cache/private.db"), "Runtime secret")
	writeCodexFixture(t, filepath.Join(home, "outside-secret"), "Do not snapshot")
	for _, link := range []struct{ target, path string }{
		{"references/guide.md", filepath.Join(external, "guide.md")},
		{filepath.Join(home, "outside-secret"), filepath.Join(external, "escape.md")},
		{".", filepath.Join(external, "cycle")},
		{external, filepath.Join(home, ".agents/skills/linked-skill")},
	} {
		if err := os.MkdirAll(filepath.Dir(link.path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(link.target, link.path); err != nil {
			t.Fatal(err)
		}
	}
	adapter, _ := Lookup(Codex)
	root := filepath.Join(home, "codegenbox/session")
	if _, err := ResolveSessionState(adapter, root); err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(root, "skills/linked-skill")
	for _, file := range []string{"SKILL.md", "references/guide.md", "guide.md", "scripts/run.sh"} {
		info, err := os.Lstat(filepath.Join(copied, file))
		if err != nil || !info.Mode().IsRegular() || hasMultipleLinks(info) {
			t.Fatalf("skill file %s was not independently copied", file)
		}
	}
	info, _ := os.Stat(filepath.Join(copied, "scripts/run.sh"))
	if info.Mode().Perm() != 0o700 {
		t.Fatal("script executable permission lost")
	}
	for _, file := range []string{"escape.md", "cycle", "cache"} {
		if _, err := os.Lstat(filepath.Join(copied, file)); !os.IsNotExist(err) {
			t.Fatalf("unsafe skill artifact copied: %s", file)
		}
	}
}

func TestPortableSettingsRemoveHostIntegrationsAndPreservePreferences(t *testing.T) {
	codex := []byte(`model="fixture-model"
cli_auth_credentials_store="keyring"
sqlite_home="/host/db"
notify=["/host/notify"]
[profiles.review]
model_reasoning_effort="high"
sqlite_home="/host/profile-db"
[profiles.review.hooks]
[mcp_servers.local]
command="/host/mcp"
[mcp_servers.remote]
url="https://example.invalid/mcp"
[shell_environment_policy.set]
HOME="/host/home"
MODE="fixture"
`)
	data, err := portableCodexSettings(codex)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := toml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "/host") || config["cli_auth_credentials_store"] != "file" || config["model"] != "fixture-model" || !strings.Contains(string(data), "example.invalid") {
		t.Fatal("Codex portability transform failed")
	}
	claude := []byte(`{"model":"fixture","hooks":{},"statusLine":{},"apiKeyHelper":"/host/helper","enabledPlugins":{},"env":{"HOME":"/host","CLAUDE_CONFIG_DIR":"/host","ANTHROPIC_MODEL":"fixture"},"permissions":{"additionalDirectories":["/host"],"allow":["Read"]}}`)
	data, err = portableClaudeSettings(claude)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "/host") || strings.Contains(string(data), "hooks") || !strings.Contains(string(data), "ANTHROPIC_MODEL") {
		t.Fatal("Claude portability transform failed")
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
}

func TestOriCredentialFallbackAndMissingAuthDoNotCreateHostState(t *testing.T) {
	home := seedTestEnvironment(t)
	writeCodexFixture(t, filepath.Join(home, ".openrouter/credentials.json"), "fallback-fixture")
	adapter, _ := Lookup(OriClaude)
	root := filepath.Join(home, "codegenbox/session")
	if _, err := ResolveSessionState(adapter, root); err != nil {
		t.Fatal(err)
	}
	if readTestFile(t, filepath.Join(root, "ori/credentials.json")) != "fallback-fixture" {
		t.Fatal("Ori global fallback missing")
	}
	if _, err := os.Stat(filepath.Join(home, ".ori")); !os.IsNotExist(err) {
		t.Fatal("host Ori state created")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Fatal("host Claude state created")
	}
}

func TestSharedSkillsRootMayAliasLegacyCodexSkills(t *testing.T) {
	home := seedTestEnvironment(t)
	legacy := filepath.Join(home, ".codex/skills")
	writeCodexFixture(t, filepath.Join(legacy, "alias-skill/SKILL.md"), "Shared authored skill")
	if err := os.MkdirAll(filepath.Join(home, ".agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(legacy, filepath.Join(home, ".agents/skills")); err != nil {
		t.Fatal(err)
	}
	adapter, _ := Lookup(Codex)
	root := filepath.Join(home, "codegenbox/session")
	if _, err := ResolveSessionState(adapter, root); err != nil {
		t.Fatal(err)
	}
	if readTestFile(t, filepath.Join(root, "skills/alias-skill/SKILL.md")) != "Shared authored skill" {
		t.Fatal("aliased shared skills root was not snapshotted")
	}
}
