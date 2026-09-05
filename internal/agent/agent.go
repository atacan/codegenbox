// Package agent describes the fixed, per-agent command and persistence surface.
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/codegenbox/codegenbox/internal/container"
)

const (
	Claude   = "claude"
	Codex    = "codex"
	OpenCode = "opencode"
)

const containerHome = "/home/agent"

// Adapter owns a fixed executable, its environment, and the state locations it
// needs. Callers cannot add command arguments or mounts.
type Adapter struct {
	Name        string
	Command     []string
	Environment map[string]string
	State       []StateLocation
	Description string
}

// StateLocation maps one deliberately narrow host directory to a CLI's normal
// in-container state location. Agent state is writable because authentication
// refresh, settings, and conversation history are all written by these CLIs.
type StateLocation struct {
	Key         string
	Destination string
}

// Lookup returns one of the Phase 2 adapters. The proof image remains Node
// based, so npm-published CLIs are launched with npx until Phase 3 supplies an
// image with all three commands installed.
func Lookup(name string) (Adapter, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case Claude:
		return Adapter{Name: Claude, Command: []string{"npx", "--yes", "@anthropic-ai/claude-code"}, Environment: homeEnvironment(), State: []StateLocation{{Key: "CLAUDE_STATE_DIR", Destination: containerHome + "/.claude"}}, Description: "Runs Claude Code with only ~/.claude state mounted at the synthetic container home."}, nil
	case Codex:
		environment := homeEnvironment()
		environment["CODEX_HOME"] = containerHome + "/.codex"
		return Adapter{Name: Codex, Command: []string{"npx", "--yes", "@openai/codex", "--dangerously-bypass-approvals-and-sandbox"}, Environment: environment, State: []StateLocation{{Key: "CODEX_STATE_DIR", Destination: containerHome + "/.codex"}}, Description: "Runs Codex through npx with its current --yolo-compatible bypass mode and only CODEX_HOME state mounted."}, nil
	case OpenCode:
		environment := homeEnvironment()
		environment["XDG_CONFIG_HOME"] = containerHome + "/.config"
		environment["XDG_DATA_HOME"] = containerHome + "/.local/share"
		return Adapter{Name: OpenCode, Command: []string{"npx", "--yes", "opencode-ai"}, Environment: environment, State: []StateLocation{{Key: "OPENCODE_CONFIG_DIR", Destination: containerHome + "/.config/opencode"}, {Key: "OPENCODE_DATA_DIR", Destination: containerHome + "/.local/share/opencode"}}, Description: "Runs OpenCode with its explicit XDG config and data directories mounted."}, nil
	default:
		return Adapter{}, fmt.Errorf("unsupported agent %q (supported: %s)", name, strings.Join(Supported(), ", "))
	}
}

func homeEnvironment() map[string]string { return map[string]string{"HOME": containerHome} }

// Supported returns names in stable CLI/display order.
func Supported() []string { return []string{Claude, Codex, OpenCode} }

// ResolveState creates and validates only the selected adapter's required
// direct host paths. Environment overrides intentionally exist per agent, not
// as a generic mount mechanism.
func ResolveState(adapter Adapter) ([]container.StateMount, error) {
	homeRaw, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory for %s state: %w", adapter.Name, err)
	}
	home, err := canonicalExistingPath(homeRaw)
	if err != nil {
		return nil, fmt.Errorf("canonicalize home directory for %s state: %w", adapter.Name, err)
	}
	xdgConfig := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if xdgConfig == "" {
		xdgConfig = filepath.Join(homeRaw, ".config")
	}
	xdgData := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if xdgData == "" {
		xdgData = filepath.Join(homeRaw, ".local", "share")
	}
	xdgConfig, err = canonicalExistingPath(xdgConfig)
	if err != nil {
		return nil, fmt.Errorf("canonicalize XDG config directory: %w", err)
	}
	xdgData, err = canonicalExistingPath(xdgData)
	if err != nil {
		return nil, fmt.Errorf("canonicalize XDG data directory: %w", err)
	}
	defaults := map[string]string{"CLAUDE_STATE_DIR": filepath.Join(home, ".claude"), "CODEX_STATE_DIR": filepath.Join(home, ".codex"), "OPENCODE_CONFIG_DIR": filepath.Join(xdgConfig, "opencode"), "OPENCODE_DATA_DIR": filepath.Join(xdgData, "opencode")}
	for key, path := range defaults {
		canonical, err := canonicalExistingPath(path)
		if err != nil {
			return nil, fmt.Errorf("canonicalize %s default: %w", key, err)
		}
		defaults[key] = canonical
	}
	parents := []string{home, xdgConfig, xdgData, filepath.Join(home, ".local")}
	mounts := make([]container.StateMount, 0, len(adapter.State))
	for _, location := range adapter.State {
		path := strings.TrimSpace(os.Getenv("CODEGENBOX_" + location.Key))
		if path == "" {
			path = defaults[location.Key]
		}
		if hasPathTraversal(path) {
			return nil, fmt.Errorf("%s paths containing traversal are not supported", location.Key)
		}
		canonical, err := canonicalExistingPath(path)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", location.Key, err)
		}
		if err := validateStatePath(adapter.Name, location.Key, canonical, parents, defaults); err != nil {
			return nil, err
		}
		_, statErr := os.Stat(canonical)
		created := os.IsNotExist(statErr)
		if statErr != nil && !created {
			return nil, fmt.Errorf("inspect %s state directory: %w", adapter.Name, statErr)
		}
		if err := os.MkdirAll(canonical, 0o700); err != nil {
			return nil, fmt.Errorf("create %s state directory: %w", adapter.Name, err)
		}
		if created {
			if err := os.Chmod(canonical, 0o700); err != nil {
				return nil, fmt.Errorf("restrict %s state directory: %w", adapter.Name, err)
			}
		}
		canonical, err = filepath.EvalSymlinks(canonical)
		if err != nil {
			return nil, fmt.Errorf("resolve %s state directory: %w", adapter.Name, err)
		}
		canonical = filepath.Clean(canonical)
		if err := validateStatePath(adapter.Name, location.Key, canonical, parents, defaults); err != nil {
			return nil, err
		}
		for _, mount := range mounts {
			if canonical == mount.Source || isWithin(canonical, mount.Source) || isWithin(mount.Source, canonical) {
				return nil, fmt.Errorf("%s state source duplicates or nests another selected state source", location.Key)
			}
		}
		mounts = append(mounts, container.StateMount{Agent: adapter.Name, Source: canonical, Destination: location.Destination})
	}
	return mounts, nil
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
