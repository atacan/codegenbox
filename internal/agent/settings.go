package agent

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/tidwall/jsonc"
)

func seedTransform(key, name string) func([]byte) ([]byte, error) {
	switch key {
	case "CODEX_STATE_DIR":
		if strings.HasSuffix(name, ".toml") {
			return portableCodexSettings
		}
	case "CLAUDE_STATE_DIR":
		if name == "settings.json" {
			return portableClaudeSettings
		}
	case "OPENCODE_CONFIG_DIR":
		return portableOpenCodeSettings
	case "ORI_STATE_DIR":
		if name == "config.json" {
			return portableOriSettings
		}
	}
	return nil
}
func parseJSONSettings(data []byte) (map[string]any, error) {
	result := map[string]any{}
	err := json.Unmarshal(jsonc.ToJSON(data), &result)
	return result, err
}
func portableClaudeSettings(data []byte) ([]byte, error) {
	config, err := parseJSONSettings(data)
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"hooks", "statusLine", "apiKeyHelper", "otelHeadersHelper", "fileSuggestion", "enabledPlugins", "extraKnownMarketplaces"} {
		delete(config, key)
	}
	sanitizeEnvironment(config["env"])
	if permissions, ok := config["permissions"].(map[string]any); ok {
		delete(permissions, "additionalDirectories")
	}
	return json.MarshalIndent(config, "", "  ")
}
func portableOpenCodeSettings(data []byte) ([]byte, error) {
	config, err := parseJSONSettings(data)
	if err != nil {
		return nil, err
	}
	// Installed plugin packages, local MCP processes, and external instruction
	// paths are machine-specific. Project-owned equivalents still load normally.
	for _, key := range []string{"plugin", "plugins", "instructions", "watcher", "snapshot"} {
		delete(config, key)
	}
	portableMCP(config, "mcp", "command")
	if skills, ok := config["skills"].(map[string]any); ok {
		delete(skills, "paths")
	}
	return json.MarshalIndent(config, "", "  ")
}
func portableOriSettings(data []byte) ([]byte, error) {
	config, err := parseJSONSettings(data)
	if err != nil {
		return nil, err
	}
	// This is the only global config field established by the pinned Ori build.
	result := map[string]any{}
	if mode, ok := config["loginMode"].(string); ok {
		result["loginMode"] = mode
	}
	return json.MarshalIndent(result, "", "  ")
}
func portableCodexSettings(data []byte) ([]byte, error) {
	config := map[string]any{}
	if err := toml.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	sanitizeCodexTable(config)
	config["cli_auth_credentials_store"] = "file"
	config["sqlite_home"] = "/home/agent/.codex"
	config["log_dir"] = "/home/agent/.codex/log"
	return toml.Marshal(config)
}
func sanitizeCodexTable(config map[string]any) {
	for _, key := range []string{"hooks", "notify", "projects", "plugins", "plugin_marketplaces", "skills", "model_instructions_file", "experimental_instructions_file", "sqlite_home", "log_dir", "otel"} {
		delete(config, key)
	}
	portableMCP(config, "mcp_servers", "command")
	if policy, ok := config["shell_environment_policy"].(map[string]any); ok {
		sanitizeEnvironment(policy["set"])
	}
	if agents, ok := config["agents"].(map[string]any); ok {
		for name, value := range agents {
			if agent, ok := value.(map[string]any); ok {
				if _, exists := agent["config_file"]; exists {
					delete(agents, name)
				}
			}
		}
	}
	if profiles, ok := config["profiles"].(map[string]any); ok {
		for _, value := range profiles {
			if profile, ok := value.(map[string]any); ok {
				sanitizeCodexTable(profile)
			}
		}
	}
	// Profile selection may name a host-specific profile. Profile files are
	// copied separately, but a selected file may include local path settings.
	// Its own sanitized values cannot override these persistence locations.
	delete(config, "cli_auth_credentials_store")
}
func portableMCP(config map[string]any, key, commandKey string) {
	if servers, ok := config[key].(map[string]any); ok {
		for name, value := range servers {
			server, ok := value.(map[string]any)
			if !ok {
				delete(servers, name)
				continue
			}
			if _, hasCommand := server[commandKey]; hasCommand {
				delete(servers, name)
				continue
			}
			if kind, _ := server["type"].(string); kind == "local" {
				delete(servers, name)
			}
		}
	}
}
func sanitizeEnvironment(value any) {
	env, ok := value.(map[string]any)
	if !ok {
		return
	}
	for key, value := range env {
		upper := strings.ToUpper(key)
		if upper == "HOME" || upper == "PATH" || upper == "SHELL" || upper == "CODEX_HOME" || upper == "CLAUDE_CONFIG_DIR" || upper == "CLAUDE_SECURESTORAGE_CONFIG_DIR" || strings.HasPrefix(upper, "XDG_") || strings.HasPrefix(upper, "OPENCODE_CONFIG") || strings.HasPrefix(upper, "CODEGENBOX_") {
			delete(env, key)
			continue
		}
		// Environment-backed settings pointing at host files do not become portable
		// by copying their containing config. Keep ordinary provider/model values.
		if text, ok := value.(string); ok && (filepath.IsAbs(text) || strings.HasPrefix(text, "~/")) {
			delete(env, key)
		}
	}
}
