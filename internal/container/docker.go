// Package container builds the narrow Docker invocation used by Codegenbox.
package container

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const workspace = "/workspace"

type Invocation struct {
	Binary string
	Args   []string
}

type hostIdentity struct {
	uid int
	gid int
}

// Docker's numeric --user fields are signed 32-bit IDs. Reject root too:
// invoking Codegenbox through sudo must not override the image's non-root
// runtime identity.
const maxHostID = int64(1<<31 - 1)

// processHostIdentity is deliberately the only source for --user. It reads
// the identity of this process, rather than accepting a value from a command,
// environment variable, or other caller-controlled input.
var processHostIdentity = func() (hostIdentity, error) {
	identity := hostIdentity{uid: os.Getuid(), gid: os.Getgid()}
	if identity.uid <= 0 || identity.gid <= 0 || int64(identity.uid) > maxHostID || int64(identity.gid) > maxHostID {
		return hostIdentity{}, fmt.Errorf("current host UID/GID is unavailable")
	}
	return identity, nil
}

func (identity hostIdentity) dockerUser() (string, error) {
	if identity.uid <= 0 || identity.gid <= 0 || int64(identity.uid) > maxHostID || int64(identity.gid) > maxHostID {
		return "", fmt.Errorf("current host UID/GID is invalid")
	}
	return fmt.Sprintf("%d:%d", identity.uid, identity.gid), nil
}

// StateMount is trusted adapter-derived data. BuildRunInvocation validates it
// again so a future caller cannot turn it into a general mount escape hatch.
type StateMount struct {
	Agent, Source, Destination string
	ReadOnly                   bool
}

var allowedDestinations = map[string]map[string]bool{
	"claude":   {"/home/agent/.claude": true},
	"codex":    {"/home/agent/.codex": true},
	"opencode": {"/home/agent/.config/opencode": true, "/home/agent/.local/share/opencode": true},
}

func BuildRunInvocation(binary, image, workspacePath string, command []string, environment []string, selectedAgent string, protectedSources []string, stateMounts []StateMount) (Invocation, error) {
	if strings.TrimSpace(binary) == "" {
		return Invocation{}, fmt.Errorf("Docker binary is required")
	}
	if strings.TrimSpace(image) == "" || strings.HasPrefix(image, "-") {
		return Invocation{}, fmt.Errorf("a non-option Docker image is required")
	}
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return Invocation{}, fmt.Errorf("agent command is required")
	}
	if allowedDestinations[selectedAgent] == nil {
		return Invocation{}, fmt.Errorf("unsupported state-mount agent %q", selectedAgent)
	}
	workspacePath, err := cleanMountSource(workspacePath)
	if err != nil {
		return Invocation{}, fmt.Errorf("resolve session workspace path: %w", err)
	}
	identity, err := processHostIdentity()
	if err != nil {
		return Invocation{}, fmt.Errorf("resolve current host identity: %w", err)
	}
	dockerUser, err := identity.dockerUser()
	if err != nil {
		return Invocation{}, err
	}
	args := []string{"run", "--rm", "--interactive", "--tty", "--user", dockerUser, "--workdir", workspace, "--mount", mountArgument(workspacePath, workspace, false), "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true"}
	seen := map[string]bool{workspace: true}
	for _, value := range environment {
		if strings.TrimSpace(value) == "" || strings.Contains(value, "\x00") {
			return Invocation{}, fmt.Errorf("invalid container environment")
		}
		args = append(args, "--env", value)
	}
	for _, mount := range stateMounts {
		canonicalSource, err := cleanMountSource(mount.Source)
		if err != nil {
			return Invocation{}, fmt.Errorf("invalid state mount source: %w", err)
		}
		mount.Source = canonicalSource
		if err := validateStateMount(selectedAgent, mount, workspacePath, protectedSources, seen); err != nil {
			return Invocation{}, err
		}
		seen[mount.Destination] = true
		args = append(args, "--mount", mountArgument(mount.Source, mount.Destination, mount.ReadOnly))
	}
	args = append(args, image)
	args = append(args, command...)
	return Invocation{Binary: binary, Args: args}, nil
}

func cleanMountSource(source string) (string, error) {
	if strings.TrimSpace(source) == "" {
		return "", fmt.Errorf("mount source is required")
	}
	for _, part := range strings.FieldsFunc(source, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return "", fmt.Errorf("mount sources containing path traversal are not supported")
		}
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if strings.Contains(abs, ",") || strings.Contains(abs, "\x00") {
		return "", fmt.Errorf("mount sources containing commas or NUL are not supported")
	}
	return canonicalExistingPath(abs)
}

func validateStateMount(agent string, mount StateMount, workspacePath string, protectedSources []string, seen map[string]bool) error {
	if mount.Agent != agent {
		return fmt.Errorf("state mount for %q cannot be used by %q", mount.Agent, agent)
	}
	if !allowedDestinations[agent][mount.Destination] {
		return fmt.Errorf("state mount destination %q is not allowed for %q", mount.Destination, agent)
	}
	if mount.Destination == workspace || seen[mount.Destination] || strings.Contains(mount.Destination, ",") || !strings.HasPrefix(mount.Destination, "/home/agent/") {
		return fmt.Errorf("invalid or duplicate state mount destination %q", mount.Destination)
	}
	source, err := cleanMountSource(mount.Source)
	if err != nil {
		return fmt.Errorf("invalid state mount source: %w", err)
	}
	socket, socketErr := canonicalExistingPath("/var/run/docker.sock")
	if socketErr == nil && source == socket {
		return fmt.Errorf("refusing Docker socket mount")
	}
	for _, forbidden := range canonicalStateParents() {
		if source == forbidden {
			return fmt.Errorf("refusing host home or generic state-parent mount")
		}
	}
	for name, path := range canonicalAgentStatePaths() {
		if name == agent || (agent == "opencode" && strings.HasPrefix(name, "opencode-")) {
			continue
		}
		if source == path || isWithin(source, path) || isWithin(path, source) {
			return fmt.Errorf("state mount source aliases %s state", name)
		}
	}
	seenSources := seen["source:"+source]
	if seenSources {
		return fmt.Errorf("duplicate state mount source %q", source)
	}
	for destination := range seen {
		if !strings.HasPrefix(destination, "source:") || destination == "source:"+source {
			continue
		}
		other := strings.TrimPrefix(destination, "source:")
		if isWithin(source, other) || isWithin(other, source) {
			return fmt.Errorf("nested state mount sources are not allowed")
		}
	}
	if source == workspacePath || isWithin(source, workspacePath) || isWithin(workspacePath, source) {
		return fmt.Errorf("state mount source collides with workspace")
	}
	for _, protected := range protectedSources {
		protected, err := cleanMountSource(protected)
		if err != nil {
			return fmt.Errorf("invalid protected mount source: %w", err)
		}
		if source == protected || isWithin(source, protected) || isWithin(protected, source) {
			return fmt.Errorf("state mount source collides with protected source repository path")
		}
	}
	seen["source:"+source] = true
	return nil
}

func canonicalStateParents() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	xdgConfig := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if xdgConfig == "" {
		xdgConfig = filepath.Join(home, ".config")
	}
	xdgData := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if xdgData == "" {
		xdgData = filepath.Join(home, ".local", "share")
	}
	paths := []string{home, xdgConfig, xdgData, filepath.Join(home, ".local")}
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if canonical, err := canonicalExistingPath(path); err == nil {
			result = append(result, canonical)
		}
	}
	return result
}

func canonicalAgentStatePaths() map[string]string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	xdgConfig := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if xdgConfig == "" {
		xdgConfig = filepath.Join(home, ".config")
	}
	xdgData := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if xdgData == "" {
		xdgData = filepath.Join(home, ".local", "share")
	}
	raw := map[string]string{"claude": filepath.Join(home, ".claude"), "codex": filepath.Join(home, ".codex"), "opencode-config": filepath.Join(xdgConfig, "opencode"), "opencode-data": filepath.Join(xdgData, "opencode")}
	result := make(map[string]string, len(raw))
	for name, path := range raw {
		if canonical, err := canonicalExistingPath(path); err == nil {
			result[name] = canonical
		}
	}
	return result
}

// canonicalExistingPath resolves symlink aliases even when the terminal path
// does not yet exist, by resolving its closest existing ancestor.
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

func mountArgument(source, destination string, readOnly bool) string {
	if readOnly {
		return "type=bind,src=" + source + ",dst=" + destination + ",readonly"
	}
	return "type=bind,src=" + source + ",dst=" + destination
}

func isWithin(path, parent string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}

type Runner interface {
	Run(context.Context, Invocation) error
}

type ExecRunner struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

func (r ExecRunner) Run(ctx context.Context, invocation Invocation) error {
	cmd := exec.CommandContext(ctx, invocation.Binary, invocation.Args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = r.Stdin, r.Stdout, r.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker run: %w", err)
	}
	return nil
}
