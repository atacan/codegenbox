// Package agent describes the fixed, per-agent command and persistence surface.
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/codegenbox/codegenbox/internal/container"
)

const (
	Claude      = "claude"
	Codex       = "codex"
	OpenCode    = "opencode"
	OriClaude   = "ori/claude"
	OriCodex    = "ori/codex"
	OriOpenCode = "ori/opencode"
)

const containerHome = "/home/agent"

// Adapter owns a resolved executable command, its environment, and the state
// locations it needs. Callers cannot append arbitrary arguments or mounts.
type Adapter struct {
	Name        string
	Command     []string
	Environment map[string]string
	State       []StateLocation
	Description string
	Model       string
}

// StateLocation describes a CLI's in-container state location. Every location
// is backed by private session state, never its host seed directory.
type StateLocation struct {
	Key         string
	Destination string
}

// Lookup resolves an adapter without a per-session model override.
func Lookup(name string) (Adapter, error) {
	return Resolve(name, "")
}

// Resolve returns a direct or composed adapter. A model is accepted only for
// Ori adapters and becomes part of their fixed, recorded command.
func Resolve(name, model string) (Adapter, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if err := validateModel(name, model); err != nil {
		return Adapter{}, err
	}
	switch name {
	case Claude:
		environment := homeEnvironment()
		environment["CLAUDE_CONFIG_DIR"] = containerHome + "/.claude"
		return Adapter{Name: Claude, Command: []string{"claude"}, Environment: environment, State: []StateLocation{{Key: "CLAUDE_STATE_DIR", Destination: containerHome + "/.claude"}}, Description: "Runs Claude Code with private session credentials, settings, skills, and runtime state."}, nil
	case Codex:
		environment := homeEnvironment()
		environment["CODEX_HOME"] = containerHome + "/.codex"
		return Adapter{Name: Codex, Command: []string{"codex", "--dangerously-bypass-approvals-and-sandbox"}, Environment: environment, State: []StateLocation{{Key: "CODEX_STATE_DIR", Destination: containerHome + "/.codex"}, {Key: "AGENT_SKILLS_DIR", Destination: containerHome + "/.agents/skills"}}, Description: "Runs Codex with private session state seeded with authentication, portable settings, and skills."}, nil
	case OpenCode:
		environment := homeEnvironment()
		environment["XDG_CONFIG_HOME"] = containerHome + "/.config"
		environment["XDG_DATA_HOME"] = containerHome + "/.local/share"
		return Adapter{Name: OpenCode, Command: []string{"opencode"}, Environment: environment, State: []StateLocation{{Key: "OPENCODE_CONFIG_DIR", Destination: containerHome + "/.config/opencode"}, {Key: "OPENCODE_DATA_DIR", Destination: containerHome + "/.local/share/opencode"}, {Key: "AGENT_SKILLS_DIR", Destination: containerHome + "/.agents/skills"}}, Description: "Runs OpenCode with private session config, credentials, runtime state, and skills."}, nil
	case OriClaude:
		return oriAdapter(OriClaude, Claude, model), nil
	case OriCodex:
		return oriAdapter(OriCodex, Codex, model), nil
	case OriOpenCode:
		return oriAdapter(OriOpenCode, OpenCode, model), nil
	default:
		return Adapter{}, fmt.Errorf("unsupported agent %q (supported: %s)", name, strings.Join(Supported(), ", "))
	}
}

func oriAdapter(name, child, model string) Adapter {
	command := []string{"ori", child}
	if model != "" {
		command = append(command, "--model", model)
	}
	environment := homeEnvironment()
	state := []StateLocation{{Key: "ORI_STATE_DIR", Destination: containerHome + "/.ori"}}
	switch child {
	case Claude:
		environment["CLAUDE_CONFIG_DIR"] = containerHome + "/.claude"
		state = append(state, StateLocation{Key: "CLAUDE_STATE_DIR", Destination: containerHome + "/.claude"})
	case Codex:
		environment["CODEX_HOME"] = containerHome + "/.codex"
		state = append(state, StateLocation{Key: "CODEX_STATE_DIR", Destination: containerHome + "/.codex"})
		state = append(state, StateLocation{Key: "AGENT_SKILLS_DIR", Destination: containerHome + "/.agents/skills"})
		command = append(command, "--dangerously-bypass-approvals-and-sandbox")
	case OpenCode:
		environment["XDG_CONFIG_HOME"] = containerHome + "/.config"
		environment["XDG_DATA_HOME"] = containerHome + "/.local/share"
		state = append(state, StateLocation{Key: "OPENCODE_CONFIG_DIR", Destination: containerHome + "/.config/opencode"}, StateLocation{Key: "OPENCODE_DATA_DIR", Destination: containerHome + "/.local/share/opencode"}, StateLocation{Key: "AGENT_SKILLS_DIR", Destination: containerHome + "/.agents/skills"})
	}
	return Adapter{Name: name, Command: command, Environment: environment, State: state, Model: model, Description: "Runs " + child + " through Ori with private per-session state for Ori and its selected harness."}
}

func validateModel(name, model string) error {
	if model == "" {
		return nil
	}
	if !strings.HasPrefix(name, "ori/") {
		return fmt.Errorf("--model is supported only for Ori harnesses")
	}
	if strings.HasPrefix(model, "-") {
		return fmt.Errorf("Ori model must not begin with an option prefix")
	}
	for _, character := range model {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return fmt.Errorf("Ori model must be one non-empty command argument without whitespace or control characters")
		}
	}
	return nil
}

func homeEnvironment() map[string]string { return map[string]string{"HOME": containerHome} }

// Supported returns names in stable CLI/display order.
func Supported() []string { return []string{Claude, Codex, OpenCode, OriClaude, OriCodex, OriOpenCode} }

// ResolveState refuses state resolution without an explicit private session root.
func ResolveState(adapter Adapter) ([]container.StateMount, error) {
	return nil, fmt.Errorf("%s requires private per-session state", adapter.Name)
}

// ResolveSessionState snapshots only selected authentication, portable settings,
// and authored skills into private session directories. Host state is never mounted.
func ResolveSessionState(adapter Adapter, sessionRoot string) ([]container.StateMount, error) {
	return resolveSessionState(adapter, sessionRoot)
}

func hasPathTraversal(path string) bool {
	for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return true
		}
	}
	return false
}

func validateStatePath(agentName, key, path string, parents []string, defaults map[string]string) error {
	if strings.Contains(path, ",") {
		return fmt.Errorf("%s paths containing commas are not supported", key)
	}
	for _, parent := range parents {
		if path == filepath.Clean(parent) {
			return fmt.Errorf("%s must name a direct %s state directory, not a host home/config parent", key, agentName)
		}
	}
	for otherKey, otherPath := range defaults {
		if otherKey != key && (path == filepath.Clean(otherPath) || isWithin(path, otherPath) || isWithin(otherPath, path)) {
			return fmt.Errorf("%s must not use %s state", key, otherKey)
		}
	}
	return nil
}

func canonicalExistingPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var suffix []string
	current := filepath.Clean(abs)
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func isWithin(path, parent string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}

// EnvironmentPairs returns deterministic Docker environment arguments.
func EnvironmentPairs(adapter Adapter) []string {
	keys := make([]string, 0, len(adapter.Environment))
	for key := range adapter.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+adapter.Environment[key])
	}
	return pairs
}
