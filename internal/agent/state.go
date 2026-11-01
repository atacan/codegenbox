package agent

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/codegenbox/codegenbox/internal/container"
)

const seedMarker = ".codegenbox-seeded-v1"
const maxSeedFile = 16 << 20
const bundledSkillTarget = "/opt/codegenbox/skills/srcmv"

var privateStateNames = map[string]string{
	"CLAUDE_STATE_DIR": "claude", "CODEX_STATE_DIR": "codex",
	"OPENCODE_CONFIG_DIR": "opencode-config", "OPENCODE_DATA_DIR": "opencode-data",
	"ORI_STATE_DIR": "ori", "AGENT_SKILLS_DIR": "skills",
}

func seedSources() (map[string]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	config := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if config == "" {
		config = filepath.Join(home, ".config")
	}
	data := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	claude := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	if claude == "" {
		claude = filepath.Join(home, ".claude")
	}
	codex := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if codex == "" {
		codex = filepath.Join(home, ".codex")
	}
	result := map[string]string{
		"CLAUDE_STATE_DIR": claude, "CODEX_STATE_DIR": codex,
		"OPENCODE_CONFIG_DIR": filepath.Join(config, "opencode"),
		"OPENCODE_DATA_DIR":   filepath.Join(data, "opencode"),
		"ORI_STATE_DIR":       filepath.Join(home, ".ori"),
		"AGENT_SKILLS_DIR":    filepath.Join(home, ".agents", "skills"),
	}
	defaults := make(map[string]string, len(result))
	for key, path := range result {
		canonical, err := canonicalExistingPath(path)
		if err != nil {
			return nil, err
		}
		defaults[key] = canonical
	}
	// Shared authored skills may legitimately alias a selected harness's skills
	// child. They are copied separately, never mounted from that source.
	delete(defaults, "AGENT_SKILLS_DIR")
	parents := []string{home, config, data, filepath.Join(home, ".local")}
	for index, path := range parents {
		canonical, err := canonicalExistingPath(path)
		if err != nil {
			return nil, err
		}
		parents[index] = canonical
	}
	for key, path := range result {
		if value := strings.TrimSpace(os.Getenv("CODEGENBOX_" + key)); value != "" {
			path = value
		}
		canonical, err := canonicalExistingPath(path)
		if err != nil {
			return nil, fmt.Errorf("resolve %s seed source: %w", key, err)
		}
		result[key] = canonical
		if key != "AGENT_SKILLS_DIR" {
			if err := validateStatePath("agent", key, canonical, parents, defaults); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

func resolveSessionState(adapter Adapter, root string) ([]container.StateMount, error) {
	if root == "" {
		return nil, fmt.Errorf("%s requires private per-session state", adapter.Name)
	}
	if err := validatePrivateDirectory(root); err != nil {
		return nil, err
	}
	sources, err := seedSources()
	if err != nil {
		return nil, err
	}
	if adapter.Name == Codex || adapter.Name == OriCodex {
		sources["LEGACY_CODEX_SKILLS"] = filepath.Join(sources["CODEX_STATE_DIR"], "skills")
	}
	mounts := make([]container.StateMount, 0, len(adapter.State))
	for _, location := range adapter.State {
		name, ok := privateStateNames[location.Key]
		if !ok {
			return nil, fmt.Errorf("unsupported private agent state location")
		}
		destination := filepath.Join(root, name)
		if err := ensurePrivateDirectory(destination); err != nil {
			return nil, err
		}
		marker := filepath.Join(destination, seedMarker)
		initialized, err := privateFileExists(marker)
		if err != nil {
			return nil, err
		}
		if err := validatePrivateSelectedFiles(location.Key, destination); err != nil {
			return nil, err
		}
		if !initialized {
			if err := seedLocation(location.Key, sources, destination); err != nil {
				return nil, fmt.Errorf("initialize %s: %w", location.Key, err)
			}
			if err := writePrivateFile(marker, strings.NewReader("1\n")); err != nil {
				return nil, err
			}
		}
		mounts = append(mounts, container.StateMount{Agent: adapter.Name, Source: destination, Destination: location.Destination})
	}
	return mounts, nil
}

func validatePrivateDirectory(path string) error {
	canonical, err := canonicalExistingPath(path)
	if err != nil {
		return fmt.Errorf("resolve private agent directory: %w", err)
	}
	if !filepath.IsAbs(path) || canonical != filepath.Clean(path) || hasPathTraversal(path) || strings.ContainsAny(path, ",\x00") {
		return fmt.Errorf("private agent state requires an absolute directory without symlink aliases, traversal, or mount delimiters")
	}
	return container.ValidatePrivateStatePath(canonical)
}
func ensurePrivateDirectory(path string) error {
	if err := validatePrivateDirectory(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create private agent directory: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("restrict private agent directory: %w", err)
	}
	return nil
}

func validatePrivateSelectedFiles(key, destination string) error {
	files := map[string][]string{
		"CLAUDE_STATE_DIR":    {".credentials.json", "settings.json", "CLAUDE.md"},
		"CODEX_STATE_DIR":     {"auth.json", "config.toml", "AGENTS.md"},
		"OPENCODE_CONFIG_DIR": {"opencode.json", "opencode.jsonc", "tui.json", "tui.jsonc"},
		"OPENCODE_DATA_DIR":   {"auth.json"},
		"ORI_STATE_DIR":       {"credentials.json", "config.json"},
	}
	for _, name := range files[key] {
		if _, err := privateFileExists(filepath.Join(destination, name)); err != nil {
			return err
		}
	}
	if key == "AGENT_SKILLS_DIR" {
		link := filepath.Join(destination, "srcmv")
		if info, err := os.Lstat(link); err == nil && info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(link)
			if err != nil || target != bundledSkillTarget {
				return fmt.Errorf("private bundled skill link has an unexpected target")
			}
		}
	}
	return nil
}

// This manifest is intentionally explicit. Runtime databases, conversations,
// plugin installations, caches, shell snapshots, and host executable helpers
// are never roots of a snapshot.
func seedLocation(key string, sources map[string]string, destination string) error {
	source := sources[key]
	files := []string{}
	trees := []string{}
	switch key {
	case "CLAUDE_STATE_DIR":
		if err := seedAuth("CLAUDE", filepath.Join(source, ".credentials.json"), filepath.Join(destination, ".credentials.json")); err != nil {
			return err
		}
		files = []string{"settings.json", "CLAUDE.md"}
		trees = []string{"skills", "commands", "agents", "rules"}
	case "CODEX_STATE_DIR":
		auth, explicit, err := codexAuthSource()
		if err != nil {
			return err
		}
		if err := copySeedFile(auth, filepath.Join(destination, "auth.json"), explicit, nil); err != nil {
			return fmt.Errorf("Codex auth seed failed (use CODEGENBOX_CODEX_AUTH_FILE=none for independent login): %w", err)
		}
		files = []string{"config.toml", "AGENTS.md"}
		profiles, err := filepath.Glob(filepath.Join(source, "*.config.toml"))
		if err != nil {
			return fmt.Errorf("enumerate Codex profiles")
		}
		for _, path := range profiles {
			files = append(files, filepath.Base(path))
		}
	case "OPENCODE_CONFIG_DIR":
		files = []string{"opencode.json", "opencode.jsonc", "tui.json", "tui.jsonc"}
		trees = []string{"skills", "skill", "agents", "agent", "commands", "command", "rules"}
	case "OPENCODE_DATA_DIR":
		return seedAuth("OPENCODE", filepath.Join(source, "auth.json"), filepath.Join(destination, "auth.json"))
	case "ORI_STATE_DIR":
		auth := filepath.Join(source, "credentials.json")
		if _, err := os.Stat(auth); os.IsNotExist(err) && os.Getenv("CODEGENBOX_ORI_STATE_DIR") == "" {
			home, _ := os.UserHomeDir()
			auth = filepath.Join(home, ".openrouter", "credentials.json")
		}
		if err := seedAuth("ORI", auth, filepath.Join(destination, "credentials.json")); err != nil {
			return err
		}
		files = []string{"config.json"}
	case "AGENT_SKILLS_DIR":
		if err := copySkills(source, destination); err != nil {
			return err
		}
		// Retain legacy Codex skills without relying on deprecated runtime discovery.
		if legacy := sources["LEGACY_CODEX_SKILLS"]; legacy != "" {
			if err := copySkills(legacy, destination); err != nil {
				return err
			}
		}
		link := filepath.Join(destination, "srcmv")
		if _, err := os.Lstat(link); os.IsNotExist(err) {
			return os.Symlink(bundledSkillTarget, link)
		}
		return nil
	}
	for _, file := range files {
		transform := seedTransform(key, file)
		if err := copySeedFile(filepath.Join(source, file), filepath.Join(destination, file), false, transform); err != nil {
			return err
		}
	}
	if key == "CODEX_STATE_DIR" {
		config := filepath.Join(destination, "config.toml")
		if exists, err := privateFileExists(config); err != nil {
			return err
		} else if !exists {
			if err := writePrivateFile(config, strings.NewReader("cli_auth_credentials_store = \"file\"\n")); err != nil {
				return err
			}
		}
	}
	for _, tree := range trees {
		if tree == "skills" || tree == "skill" {
			if err := copySkills(filepath.Join(source, tree), filepath.Join(destination, tree)); err != nil {
				return err
			}
		} else if err := copyAuthoredTree(filepath.Join(source, tree), filepath.Join(destination, tree)); err != nil {
			return err
		}
	}
	return nil
}

func seedAuth(family, source, destination string) error {
	explicit := false
	if value := strings.TrimSpace(os.Getenv("CODEGENBOX_" + family + "_AUTH_FILE")); value != "" {
		if value == "none" {
			return nil
		}
		source, explicit = value, true
	}
	return copySeedFile(source, destination, explicit, nil)
}

func copySeedFile(source, destination string, required bool, transform func([]byte) ([]byte, error)) error {
	if exists, err := privateFileExists(destination); err != nil {
		return err
	} else if exists {
		return nil
	}
	if source == "" {
		return nil
	}
	data, err := readSeedFile(source, required)
	if err != nil {
		return err
	}
	if data == nil {
		return nil
	}
	if transform != nil {
		data, err = transform(data)
		if err != nil {
			return fmt.Errorf("could not parse portable %s settings (file contents omitted)", filepath.Base(source))
		}
	}
	return writePrivateFile(destination, bytes.NewReader(data))
}
