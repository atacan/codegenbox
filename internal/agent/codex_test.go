package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolatedCodexTestHome(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	for _, key := range []string{"CODEX_HOME", "CLAUDE_CONFIG_DIR", "CODEGENBOX_CODEX_STATE_DIR", "CODEGENBOX_CODEX_AUTH_FILE", "CODEGENBOX_ORI_STATE_DIR", "CODEGENBOX_AGENT_SKILLS_DIR", "CODEGENBOX_CLAUDE_STATE_DIR", "CODEGENBOX_OPENCODE_CONFIG_DIR", "CODEGENBOX_OPENCODE_DATA_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME"} {
		t.Setenv(key, "")
	}
	return home
}

func writeCodexFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCodexCopiesOnlyAuthenticationAndRetainsPrivateRefresh(t *testing.T) {
	for _, name := range []string{Codex, OriCodex} {
		t.Run(name, func(t *testing.T) {
			home := isolatedCodexTestHome(t)
			host := filepath.Join(home, ".codex")
			fakeAuth := `{"OPENAI_API_KEY":"fake-codegenbox-test-key"}`
			for _, file := range []string{"auth.json", "config.toml", "hooks.json", "thread_history_1.sqlite", "state_5.sqlite", "sessions/private.jsonl", "plugins/cache"} {
				contents := "host-only sentinel"
				if file == "auth.json" {
					contents = fakeAuth
				} else if file == "config.toml" {
					contents = "model = \"fixture-model\"\n"
				}
				writeCodexFixture(t, filepath.Join(host, file), contents)
			}
			if err := os.Chmod(filepath.Join(host, "auth.json"), 0o400); err != nil {
				t.Fatal(err)
			}
			private := filepath.Join(home, "codegenbox", "agent-state", "session-one", "codex")
			adapter, _ := Lookup(name)
			if _, err := ResolveState(adapter); err == nil {
				t.Fatal("Codex accepted state resolution without a private session home")
			}
			mounts, err := ResolveSessionState(adapter, filepath.Dir(private))
			if err != nil {
				t.Fatal(err)
			}
			mount := mounts[0]
			for _, candidate := range mounts {
				if candidate.Destination == "/home/agent/.codex" {
					mount = candidate
				}
			}
			if mount.Source != private || mount.Destination != "/home/agent/.codex" || mount.ReadOnly {
				t.Fatalf("private mount = %#v", mount)
			}
			entries, err := os.ReadDir(private)
			if err != nil || len(entries) != 3 || entries[0].Name() != seedMarker || entries[1].Name() != "auth.json" || entries[2].Name() != "config.toml" {
				t.Fatalf("private state contains extra host files: %#v, %v", entries, err)
			}
			copied, _ := os.ReadFile(filepath.Join(private, "auth.json"))
			if string(copied) != fakeAuth {
				t.Fatal("authentication was not copied")
			}
			sourceInfo, _ := os.Stat(filepath.Join(host, "auth.json"))
			copyInfo, _ := os.Stat(filepath.Join(private, "auth.json"))
			homeInfo, _ := os.Stat(private)
			if os.SameFile(sourceInfo, copyInfo) || sourceInfo.Mode().Perm() != 0o400 || copyInfo.Mode().Perm() != 0o600 || homeInfo.Mode().Perm() != 0o700 {
				t.Fatal("private auth is linked or permissions are too broad")
			}
			writeCodexFixture(t, filepath.Join(private, "auth.json"), "private-refreshed-fixture")
			writeCodexFixture(t, filepath.Join(private, "thread_history_1.sqlite"), "container-history")
			if _, err := ResolveSessionState(adapter, filepath.Dir(private)); err != nil {
				t.Fatal(err)
			}
			refreshed, _ := os.ReadFile(filepath.Join(private, "auth.json"))
			original, _ := os.ReadFile(filepath.Join(host, "auth.json"))
			database, _ := os.ReadFile(filepath.Join(host, "thread_history_1.sqlite"))
			if string(refreshed) != "private-refreshed-fixture" || string(original) != fakeAuth || string(database) != "host-only sentinel" {
				t.Fatal("resume replaced refreshed credentials or wrote host state")
			}
			other := filepath.Join(home, "codegenbox", "agent-state", "session-two", "codex")
			if _, err := ResolveSessionState(adapter, filepath.Dir(other)); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(other, "thread_history_1.sqlite")); !os.IsNotExist(err) {
				t.Fatal("another session received the first session's database")
			}
		})
	}
}

func TestCodexMissingCacheDoesNotCreateHostState(t *testing.T) {
	home := isolatedCodexTestHome(t)
	adapter, _ := Lookup(Codex)
	private := filepath.Join(home, "codegenbox", "session", "codex")
	if _, err := ResolveSessionState(adapter, filepath.Dir(private)); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(home, ".codex"), filepath.Join(private, "auth.json")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unexpected state at %s", path)
		}
	}
}

func TestCodexAuthSourceOverridesNeverMountSource(t *testing.T) {
	for _, sourceKind := range []string{"legacy", "codex-home", "auth-file", "none", "explicit-missing"} {
		t.Run(sourceKind, func(t *testing.T) {
			home := isolatedCodexTestHome(t)
			source := filepath.Join(home, "custom-login", "auth.json")
			writeCodexFixture(t, source, "custom-auth-fixture")
			switch sourceKind {
			case "legacy":
				t.Setenv("CODEGENBOX_CODEX_STATE_DIR", filepath.Dir(source))
			case "codex-home":
				t.Setenv("CODEX_HOME", filepath.Dir(source))
			case "auth-file":
				t.Setenv("CODEGENBOX_CODEX_STATE_DIR", filepath.Join(home, "unused"))
				t.Setenv("CODEGENBOX_CODEX_AUTH_FILE", source)
			case "none":
				t.Setenv("CODEGENBOX_CODEX_STATE_DIR", filepath.Dir(source))
				t.Setenv("CODEGENBOX_CODEX_AUTH_FILE", "none")
			case "explicit-missing":
				t.Setenv("CODEGENBOX_CODEX_AUTH_FILE", filepath.Join(home, "missing.json"))
			}
			private := filepath.Join(home, "codegenbox", "session", "codex")
			adapter, _ := Lookup(Codex)
			mounts, err := ResolveSessionState(adapter, filepath.Dir(private))
			if sourceKind == "explicit-missing" {
				if err == nil || !strings.Contains(err.Error(), "CODEGENBOX_CODEX_AUTH_FILE=none") {
					t.Fatalf("missing explicit authentication error = %v", err)
				}
				return
			}
			if err != nil || len(mounts) != 2 || mounts[0].Source != private {
				t.Fatalf("state = %#v, %v", mounts, err)
			}
			contents, err := os.ReadFile(filepath.Join(private, "auth.json"))
			if sourceKind == "none" {
				if !os.IsNotExist(err) {
					t.Fatal("independent login copied host credentials")
				}
			} else if err != nil || string(contents) != "custom-auth-fixture" {
				t.Fatal("selected authentication source was not copied")
			}
		})
	}
}

func TestPrivateCodexStateRejectsAliasesAndLinkedFiles(t *testing.T) {
	for _, kind := range []string{"home", "child", "parent", "directory-symlink", "auth-symlink", "auth-hardlink", "config-hardlink"} {
		t.Run(kind, func(t *testing.T) {
			home := isolatedCodexTestHome(t)
			host := filepath.Join(home, ".codex")
			original := filepath.Join(host, "auth.json")
			writeCodexFixture(t, original, "untouched-fixture")
			private := filepath.Join(home, "codegenbox", "session", "codex")
			switch kind {
			case "home":
				private = host
			case "child":
				private = filepath.Join(host, "child")
			case "parent":
				private = home
			case "directory-symlink":
				private = filepath.Join(home, "alias")
				if err := os.Symlink(host, private); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.MkdirAll(private, 0o700); err != nil {
					t.Fatal(err)
				}
				destination := filepath.Join(private, "auth.json")
				if kind == "config-hardlink" {
					destination = filepath.Join(private, "config.toml")
				}
				link := os.Link
				if kind == "auth-symlink" {
					link = os.Symlink
				}
				if err := link(original, destination); err != nil {
					t.Fatal(err)
				}
			}
			adapter, _ := Lookup(Codex)
			if _, err := ResolveSessionState(adapter, filepath.Dir(private)); err == nil {
				t.Fatal("unsafe private state accepted")
			}
			contents, _ := os.ReadFile(original)
			if string(contents) != "untouched-fixture" {
				t.Fatal("host authentication changed")
			}
		})
	}
}
