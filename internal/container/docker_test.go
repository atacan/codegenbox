package container

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildRunInvocationContainsOnlySelectedAdapterState(t *testing.T) {
	stubHostIdentity(t, hostIdentity{uid: 501, gid: 20}, nil)
	workspace, state := filepath.Join(t.TempDir(), "workspace"), filepath.Join(t.TempDir(), "codex")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	invocation, err := BuildRunInvocation("docker", "docker.io/atacandur/codegenbox:0.1.0", workspace, []string{"codex", "--dangerously-bypass-approvals-and-sandbox"}, []string{"CODEX_HOME=/home/agent/.codex", "HOME=/home/agent"}, "codex", nil, []StateMount{{Agent: "codex", Source: state, Destination: "/home/agent/.codex"}})
	if err != nil {
		t.Fatalf("BuildRunInvocation: %v", err)
	}
	assertArgumentPair(t, invocation.Args, "--cap-drop", "ALL")
	assertArgumentPair(t, invocation.Args, "--security-opt", "no-new-privileges=true")
	assertArgumentPair(t, invocation.Args, "--user", "501:20")
	assertArgumentPair(t, invocation.Args, "--workdir", "/workspace")
	assertContains(t, invocation.Args, "--rm")
	mounts := valuesAfter(invocation.Args, "--mount")
	if len(mounts) != 2 {
		t.Fatalf("mounts = %#v, want workspace plus codex state", mounts)
	}
	canonicalWorkspace, err := canonicalExistingPath(workspace)
	if err != nil {
		t.Fatal(err)
	}
	canonicalState, err := canonicalExistingPath(state)
	if err != nil {
		t.Fatal(err)
	}
	if mounts[0] != "type=bind,src="+canonicalWorkspace+",dst=/workspace" || mounts[1] != "type=bind,src="+canonicalState+",dst=/home/agent/.codex" {
		t.Fatalf("unexpected mounts: %#v; want workspace=%q state=%q", mounts, canonicalWorkspace, canonicalState)
	}
	command := strings.Join(invocation.Args, "\x00")
	for _, forbidden := range []string{"--privileged", "/var/run/docker.sock", "/Users/atacan", "/home/agent/.claude", "/home/agent/.config/opencode"} {
		if strings.Contains(command, forbidden) {
			t.Errorf("Docker invocation unexpectedly contains %q: %#v", forbidden, invocation.Args)
		}
	}
}

func TestBuildRunInvocationRejectsUnavailableOrInvalidHostIdentity(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	for _, test := range []struct {
		name     string
		identity hostIdentity
		err      error
	}{
		{name: "unavailable", err: os.ErrNotExist},
		{name: "root UID", identity: hostIdentity{uid: 0, gid: 20}},
		{name: "root GID", identity: hostIdentity{uid: 501, gid: 0}},
		{name: "negative UID", identity: hostIdentity{uid: -1, gid: 20}},
		{name: "negative GID", identity: hostIdentity{uid: 501, gid: -1}},
		{name: "UID too large", identity: hostIdentity{uid: int(maxHostID + 1), gid: 20}},
		{name: "GID too large", identity: hostIdentity{uid: 501, gid: int(maxHostID + 1)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			stubHostIdentity(t, test.identity, test.err)
			if _, err := BuildRunInvocation("docker", "image", workspace, []string{"codex"}, nil, "codex", nil, nil); err == nil {
				t.Fatal("unavailable or invalid host identity was accepted")
			}
		})
	}
}

func TestBuildRunInvocationRejectsHostileAndCrossAgentMounts(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	for _, test := range []struct {
		name   string
		mounts []StateMount
	}{
		{"cross-agent", []StateMount{{Agent: "claude", Source: "/tmp/state", Destination: "/home/agent/.claude"}}},
		{"wrong-destination", []StateMount{{Agent: "codex", Source: "/tmp/state", Destination: "/workspace"}}},
		{"docker-socket", []StateMount{{Agent: "codex", Source: "/var/run/docker.sock", Destination: "/home/agent/.codex"}}},
		{"comma-injection", []StateMount{{Agent: "codex", Source: "/tmp/a,readonly=false", Destination: "/home/agent/.codex"}}},
		{"path-traversal", []StateMount{{Agent: "codex", Source: "/tmp/../state", Destination: "/home/agent/.codex"}}},
		{"duplicate", []StateMount{{Agent: "codex", Source: "/tmp/a", Destination: "/home/agent/.codex"}, {Agent: "codex", Source: "/tmp/b", Destination: "/home/agent/.codex"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildRunInvocation("docker", "image", workspace, []string{"codex"}, nil, "codex", nil, test.mounts); err == nil {
				t.Fatal("hostile state mount was accepted")
			}
		})
	}
	if _, err := BuildRunInvocation("docker", "image", workspace, []string{"codex"}, nil, "codex", []string{"/source/repository", "/source"}, []StateMount{{Agent: "codex", Source: "/source/repository/state", Destination: "/home/agent/.codex"}}); err == nil {
		t.Fatal("source repository mount accepted")
	}
	if _, err := BuildRunInvocation("docker", "--privileged", workspace, []string{"codex"}, nil, "codex", nil, nil); err == nil {
		t.Fatal("option-like image accepted")
	}
	if _, err := BuildRunInvocation("docker", "image", "/tmp/a,b", []string{"codex"}, nil, "codex", nil, nil); err == nil {
		t.Fatal("comma workspace accepted")
	}
}

func TestBuildRunInvocationCanonicalizesAliasedSources(t *testing.T) {
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
	config := filepath.Join(base, "config")
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	configAlias := filepath.Join(base, "config-alias")
	if err := os.Symlink(config, configAlias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configAlias)
	workspace := filepath.Join(base, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildRunInvocation("docker", "image", workspace, []string{"claude"}, nil, "claude", nil, []StateMount{{Agent: "claude", Source: homeAlias, Destination: "/home/agent/.claude"}}); err == nil {
		t.Fatal("aliased host home accepted")
	}
	if _, err := BuildRunInvocation("docker", "image", workspace, []string{"claude"}, nil, "claude", nil, []StateMount{{Agent: "claude", Source: configAlias, Destination: "/home/agent/.claude"}}); err == nil {
		t.Fatal("aliased generic config parent accepted")
	}
	codexAlias := filepath.Join(base, "codex-alias")
	if err := os.Symlink(filepath.Join(home, ".codex"), codexAlias); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildRunInvocation("docker", "image", workspace, []string{"claude"}, nil, "claude", nil, []StateMount{{Agent: "claude", Source: codexAlias, Destination: "/home/agent/.claude"}}); err == nil {
		t.Fatal("aliased cross-agent state accepted")
	}
	state := filepath.Join(base, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	stateAlias := filepath.Join(base, "state-alias")
	if err := os.Symlink(state, stateAlias); err != nil {
		t.Fatal(err)
	}
	invocation, err := BuildRunInvocation("docker", "image", workspace, []string{"claude"}, nil, "claude", nil, []StateMount{{Agent: "claude", Source: stateAlias, Destination: "/home/agent/.claude"}})
	canonicalState, canonicalErr := filepath.EvalSymlinks(state)
	if canonicalErr != nil {
		t.Fatal(canonicalErr)
	}
	if err != nil || !strings.Contains(strings.Join(invocation.Args, "\x00"), "src="+canonicalState+",dst=/home/agent/.claude") {
		t.Fatalf("canonical valid alias = %#v, %v", invocation, err)
	}
	if _, err := BuildRunInvocation("docker", "image", workspace, []string{"opencode"}, nil, "opencode", nil, []StateMount{{Agent: "opencode", Source: state, Destination: "/home/agent/.config/opencode"}, {Agent: "opencode", Source: stateAlias, Destination: "/home/agent/.local/share/opencode"}}); err == nil {
		t.Fatal("duplicate canonical source accepted")
	}
	repository := filepath.Join(base, "repository")
	if err := os.MkdirAll(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	repositoryAlias := filepath.Join(base, "repository-alias")
	if err := os.Symlink(repository, repositoryAlias); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildRunInvocation("docker", "image", workspace, []string{"claude"}, nil, "claude", []string{repositoryAlias, base}, []StateMount{{Agent: "claude", Source: repositoryAlias, Destination: "/home/agent/.claude"}}); err == nil {
		t.Fatal("aliased source repository accepted")
	}
}

func assertContains(t *testing.T, values []string, want string) {
	t.Helper()
	for _, value := range values {
		if value == want {
			return
		}
	}
	t.Fatalf("%q not found in %#v", want, values)
}
func assertArgumentPair(t *testing.T, values []string, flag, want string) {
	t.Helper()
	for index, value := range values[:len(values)-1] {
		if value == flag && values[index+1] == want {
			return
		}
	}
	t.Fatalf("%s %s not found in %#v", flag, want, values)
}
func valuesAfter(values []string, flag string) []string {
	var results []string
	for index, value := range values[:len(values)-1] {
		if value == flag {
			results = append(results, values[index+1])
		}
	}
	return results
}

func stubHostIdentity(t *testing.T, identity hostIdentity, wantErr error) {
	t.Helper()
	original := processHostIdentity
	processHostIdentity = func() (hostIdentity, error) { return identity, wantErr }
	t.Cleanup(func() { processHostIdentity = original })
}
