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
	Binary            string
	Args              []string
	SecretEnvironment map[string]string
}

// ResourceLimits are trusted, parsed host configuration. They are deliberately
// not command-line options accepted from an agent.
type ResourceLimits struct {
	PIDs         int
	Memory, CPUs string
}

// PrivateDependencyAuthorization is deliberately limited to a separate,
// read-only GitHub dependency token. It is not a host GitHub/gh credential and
// it is never serialized or added to Docker command arguments.
type PrivateDependencyAuthorization struct {
	GitHubReadToken string
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

func BuildRunInvocation(binary, image, workspacePath string, command []string, environment []string, selectedAgent string, protectedSources []string, stateMounts []StateMount, options ...any) (Invocation, error) {
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
	var privateDependencies []PrivateDependencyAuthorization
	for _, option := range options {
		switch value := option.(type) {
		case PrivateDependencyAuthorization:
			privateDependencies = append(privateDependencies, value)
		case ResourceLimits:
			if value.PIDs > 0 {
				args = append(args, "--pids-limit", fmt.Sprintf("%d", value.PIDs))
			}
			if value.Memory != "" {
				args = append(args, "--memory", value.Memory)
			}
			if value.CPUs != "" {
				args = append(args, "--cpus", value.CPUs)
			}
		default:
			return Invocation{}, fmt.Errorf("unsupported Docker invocation option")
		}
	}
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
	if len(privateDependencies) > 1 {
		return Invocation{}, fmt.Errorf("only one private dependency authorization is supported")
	}
	var privateDependencyAuthorization PrivateDependencyAuthorization
	if len(privateDependencies) == 1 {
		privateDependencyAuthorization = privateDependencies[0]
	}
	secretEnvironment, err := addPrivateDependencyAuthorization(&args, privateDependencyAuthorization)
	if err != nil {
		return Invocation{}, err
	}
	args = append(args, image)
	args = append(args, command...)
	invocation := Invocation{Binary: binary, Args: args, SecretEnvironment: secretEnvironment}
	if err := AuditInvocation(invocation, workspacePath, selectedAgent); err != nil {
		return Invocation{}, err
	}
	return invocation, nil
}

// AuditInvocation is a defense-in-depth assertion over the final Docker argv.
// It is intentionally independent of construction so tests and future callers
// cannot bypass the security contract by appending arguments later.
func AuditInvocation(invocation Invocation, workspacePath, selectedAgent string) error {
	if invocation.Binary == "" || len(invocation.Args) < 2 || invocation.Args[0] != "run" {
		return fmt.Errorf("invalid Docker invocation")
	}
	for index, argument := range invocation.Args {
		if argument == "--privileged" || strings.HasPrefix(argument, "--privileged=") || argument == "--volume" || argument == "-v" || strings.HasPrefix(argument, "--pid=host") || strings.HasPrefix(argument, "--network=host") {
			return fmt.Errorf("Docker invocation contains forbidden capability %q", argument)
		}
		if (argument == "--pid" || argument == "--network") && index+1 < len(invocation.Args) && invocation.Args[index+1] == "host" {
			return fmt.Errorf("Docker invocation contains forbidden host namespace")
		}
	}
	if !hasArgumentPair(invocation.Args, "--cap-drop", "ALL") || !hasArgumentPair(invocation.Args, "--security-opt", "no-new-privileges=true") || !hasArgument(invocation.Args, "--rm") {
		return fmt.Errorf("Docker invocation is missing required hardening")
	}
	mounts := valuesAfterFlag(invocation.Args, "--mount")
	if len(mounts) < 1 || mounts[0] != mountArgument(workspacePath, workspace, false) {
		return fmt.Errorf("Docker invocation does not mount only the validated workspace at %s", workspace)
	}
	for _, mount := range mounts[1:] {
		if strings.Contains(mount, "/var/run/docker.sock") || !validAuditedStateMount(mount, selectedAgent) {
			return fmt.Errorf("Docker invocation has an unexpected host mount")
		}
	}
	return nil
}

func validAuditedStateMount(mount, selectedAgent string) bool {
	parts := strings.Split(mount, ",")
	if len(parts) < 3 || parts[0] != "type=bind" || !strings.HasPrefix(parts[1], "src=") || parts[1] == "src=" || !strings.HasPrefix(parts[2], "dst=") {
		return false
	}
	return allowedDestinations[selectedAgent][strings.TrimPrefix(parts[2], "dst=")]
}
func hasArgument(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func hasArgumentPair(values []string, flag, want string) bool {
	for i := 0; i+1 < len(values); i++ {
		if values[i] == flag && values[i+1] == want {
			return true
		}
	}
	return false
}
func valuesAfterFlag(values []string, flag string) []string {
	var result []string
	for i := 0; i+1 < len(values); i++ {
		if values[i] == flag {
			result = append(result, values[i+1])
		}
	}
	return result
}

// addPrivateDependencyAuthorization configures Git only inside the disposable
// container. Git's command-line configuration takes precedence over an
// agent-controlled clone config, while the helper is URL-scoped to github.com.
// The token itself is inherited by Docker's child process via --env NAME, so
// it cannot appear in ps-visible Docker arguments or Invocation.Args.
func addPrivateDependencyAuthorization(args *[]string, authorization PrivateDependencyAuthorization) (map[string]string, error) {
	if authorization.GitHubReadToken == "" {
		return nil, nil
	}
	if err := validateGitHubReadToken(authorization.GitHubReadToken); err != nil {
		return nil, fmt.Errorf("invalid private dependency authorization: %w", err)
	}
	const helper = `!f() { test "$1" = get || exit 0; printf 'username=x-access-token\npassword=%s\n\n' "$CODEGENBOX_GITHUB_READ_TOKEN"; }; f`
	for _, value := range []string{
		"CODEGENBOX_GITHUB_READ_TOKEN",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_COUNT=3",
		"GIT_CONFIG_KEY_0=credential.https://github.com.helper",
		"GIT_CONFIG_VALUE_0=" + helper,
		"GIT_CONFIG_KEY_1=url.https://github.com/.insteadOf",
		"GIT_CONFIG_VALUE_1=git@github.com:",
		"GIT_CONFIG_KEY_2=url.https://github.com/.insteadOf",
		"GIT_CONFIG_VALUE_2=ssh://git@github.com/",
	} {
		*args = append(*args, "--env", value)
	}
	return map[string]string{"CODEGENBOX_GITHUB_READ_TOKEN": authorization.GitHubReadToken}, nil
}

func validateGitHubReadToken(token string) error {
	if len(token) == 0 || len(token) > 1024 {
		return fmt.Errorf("token length is invalid")
	}
	if !strings.HasPrefix(token, "github_pat_") {
		return fmt.Errorf("token must be a fine-grained GitHub token")
	}
	for _, character := range token {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-') {
			return fmt.Errorf("token contains unsupported characters")
		}
	}
	return nil
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
	if len(invocation.SecretEnvironment) != 0 {
		cmd.Env = environmentWithoutKeys(os.Environ(), invocation.SecretEnvironment)
		for key, value := range invocation.SecretEnvironment {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker run: %w", err)
	}
	return nil
}

func environmentWithoutKeys(environment []string, secrets map[string]string) []string {
	result := make([]string, 0, len(environment)+len(secrets))
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if found {
			if _, secret := secrets[key]; secret {
				continue
			}
		}
		result = append(result, entry)
	}
	return result
}
