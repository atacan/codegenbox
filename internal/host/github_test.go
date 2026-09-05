package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codegenbox/codegenbox/internal/session"
)

func TestParseGitHubRemote(t *testing.T) {
	for _, test := range []struct {
		url       string
		owner     string
		repo      string
		wantError bool
	}{
		{"https://github.com/acme/project.git", "acme", "project", false},
		{"git@github.com:acme/project.git", "acme", "project", false},
		{"ssh://git@github.com/acme/project", "acme", "project", false},
		{"https://github.com/acme/project/extra", "", "", true},
		{"https://example.com/acme/project.git", "", "", true},
		{"git@github.com:acme/../project", "", "", true},
		{"git@github.com:acme/project\n--upload-pack=x", "", "", true},
	} {
		t.Run(test.url, func(t *testing.T) {
			remote, err := ParseGitHubRemote(test.url)
			if test.wantError {
				if err == nil {
					t.Fatal("ParseGitHubRemote accepted an invalid remote")
				}
				return
			}
			if err != nil || remote.Owner != test.owner || remote.Repo != test.repo {
				t.Fatalf("ParseGitHubRemote(%q) = %#v, %v", test.url, remote, err)
			}
		})
	}
}

func TestCompareURLRejectsUnsafeMetadata(t *testing.T) {
	metadata := testMetadata(t.TempDir())
	remote := GitHubRemote{Owner: "acme", Repo: "project"}
	address, err := CompareURL(metadata, remote)
	if err != nil || address != "https://github.com/acme/project/compare/main...codegenbox/example-20260905-010203-a1b2?expand=1" {
		t.Fatalf("CompareURL = %q, %v", address, err)
	}
	metadata.SessionBranch = "main:refs/heads/main"
	if _, err := CompareURL(metadata, remote); err == nil {
		t.Fatal("unsafe session branch accepted")
	}
}

func TestCommitSummaryRejectsTerminalControlCharacters(t *testing.T) {
	if _, err := parseCommits(strings.Repeat("a", 40) + "\x00safe\x1b[2J"); err == nil {
		t.Fatal("terminal control character accepted in commit subject")
	}
}

func TestSummarizeAndPushOnlyFixedSessionRef(t *testing.T) {
	source, metadata := repositoryWithSessionCommit(t)
	bare := filepath.Join(t.TempDir(), "remote.git")
	runTestGit(t, "init", "--bare", bare)
	runTestGit(t, "-C", source, "remote", "add", "origin", bare)
	runTestGit(t, "-C", source, "tag", "protected", metadata.BaseCommit)
	runTestGit(t, "-C", source, "branch", "unrelated", metadata.BaseCommit)
	runTestGit(t, "-C", source, "push", "origin", "refs/heads/main:refs/heads/main", "refs/heads/unrelated:refs/heads/unrelated", "refs/tags/protected:refs/tags/protected")
	mainBefore := runTestGit(t, "--git-dir", bare, "rev-parse", "refs/heads/main")
	unrelatedBefore := runTestGit(t, "--git-dir", bare, "rev-parse", "refs/heads/unrelated")
	tagBefore := runTestGit(t, "--git-dir", bare, "rev-parse", "refs/tags/protected")

	summary, err := Summarize(context.Background(), metadata)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Commits) != 1 || summary.Commits[0].Subject != "session change" || summary.ChangedFiles != 1 || summary.Insertions != 1 || summary.Deletions != 0 {
		t.Fatalf("unexpected summary: %#v", summary)
	}

	pushed, err := PushSessionBranch(context.Background(), metadata)
	if err != nil {
		t.Fatal(err)
	}
	if pushed.GitHub != nil {
		t.Fatalf("unexpected push result: %#v", pushed)
	}
	if got := runTestGit(t, "--git-dir", bare, "rev-parse", "refs/heads/"+metadata.SessionBranch); got != metadata.ImportedCommit {
		t.Fatalf("pushed session ref = %s, want %s", got, metadata.ImportedCommit)
	}
	if got := runTestGit(t, "--git-dir", bare, "rev-parse", "refs/heads/main"); got != mainBefore {
		t.Fatalf("main changed from %s to %s", mainBefore, got)
	}
	if got := runTestGit(t, "--git-dir", bare, "rev-parse", "refs/heads/unrelated"); got != unrelatedBefore {
		t.Fatalf("unrelated branch changed from %s to %s", unrelatedBefore, got)
	}
	if got := runTestGit(t, "--git-dir", bare, "rev-parse", "refs/tags/protected"); got != tagBefore {
		t.Fatalf("tag changed from %s to %s", tagBefore, got)
	}
}

func TestPushRejectionPreservesRemoteSessionBranch(t *testing.T) {
	source, metadata := repositoryWithSessionCommit(t)
	bare := filepath.Join(t.TempDir(), "remote.git")
	runTestGit(t, "init", "--bare", bare)
	runTestGit(t, "-C", source, "remote", "add", "origin", bare)
	runTestGit(t, "-C", source, "checkout", "--orphan", "rogue")
	runTestGit(t, "-C", source, "rm", "-rf", ".")
	if err := os.WriteFile(filepath.Join(source, "rogue.txt"), []byte("rogue\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, "-C", source, "add", "rogue.txt")
	runTestGit(t, "-C", source, "commit", "-m", "rogue")
	rogue := runTestGit(t, "-C", source, "rev-parse", "HEAD")
	runTestGit(t, "-C", source, "push", "origin", "HEAD:refs/heads/"+metadata.SessionBranch)

	if _, err := PushSessionBranch(context.Background(), metadata); err == nil {
		t.Fatal("non-fast-forward session push unexpectedly succeeded")
	}
	if got := runTestGit(t, "--git-dir", bare, "rev-parse", "refs/heads/"+metadata.SessionBranch); got != rogue {
		t.Fatalf("rejected push changed remote branch from %s to %s", rogue, got)
	}
}

func TestPushRejectsBranchThatNoLongerMatchesImportedCommit(t *testing.T) {
	source, metadata := repositoryWithSessionCommit(t)
	bare := filepath.Join(t.TempDir(), "remote.git")
	runTestGit(t, "init", "--bare", bare)
	runTestGit(t, "-C", source, "remote", "add", "origin", bare)
	runTestGit(t, "-C", source, "checkout", "main")
	runTestGit(t, "-C", source, "branch", "-f", metadata.SessionBranch, metadata.BaseCommit)
	if _, err := PushSessionBranch(context.Background(), metadata); err == nil || !strings.Contains(err.Error(), "no longer matches") {
		t.Fatalf("mismatched imported branch error = %v", err)
	}
	if _, err := exec.Command("git", "--git-dir", bare, "rev-parse", "--verify", "refs/heads/"+metadata.SessionBranch).CombinedOutput(); err == nil {
		t.Fatal("mismatched session branch was pushed")
	}
}

func TestRemoteErrorsAndGHFailures(t *testing.T) {
	source, metadata := repositoryWithSessionCommit(t)
	if _, err := SourcePushURL(context.Background(), source); err == nil {
		t.Fatal("missing origin accepted")
	}
	bare := filepath.Join(t.TempDir(), "remote.git")
	runTestGit(t, "init", "--bare", bare)
	runTestGit(t, "-C", source, "remote", "add", "origin", bare)
	if _, err := DetectGitHubRemote(context.Background(), source); err == nil {
		t.Fatal("non-GitHub remote accepted as GitHub")
	}

	githubRunner := &scriptRunner{repository: source, remoteURL: "git@github.com:acme/project.git", commit: metadata.ImportedCommit}
	oldFind := findExecutable
	findExecutable = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { findExecutable = oldFind })
	if _, err := CreatePullRequestWithRunner(context.Background(), metadata, githubRunner); err == nil || !strings.Contains(err.Error(), "host gh CLI") {
		t.Fatalf("missing gh error = %v", err)
	}
	findExecutable = func(string) (string, error) { return "/fake/gh", nil }
	githubRunner.ghError = errors.New("API failure")
	if _, err := CreatePullRequestWithRunner(context.Background(), metadata, githubRunner); err == nil || !strings.Contains(err.Error(), "API failure") {
		t.Fatalf("gh failure = %v", err)
	}
}

func TestPushArgumentsAreFixed(t *testing.T) {
	metadata := testMetadata(t.TempDir())
	runner := &scriptRunner{repository: metadata.Repository, remoteURL: "https://github.com/acme/project.git", commit: metadata.ImportedCommit}
	if _, err := PushSessionBranchWithRunner(context.Background(), metadata, runner); err != nil {
		t.Fatal(err)
	}
	var push []string
	for _, call := range runner.calls {
		if call.binary == "git" && contains(call.arguments, "push") {
			push = call.arguments
		}
	}
	wantRefspec := "refs/heads/codegenbox/example-20260905-010203-a1b2:refs/heads/codegenbox/example-20260905-010203-a1b2"
	if !contains(push, "--no-verify") || !contains(push, "--") || !contains(push, wantRefspec) || contains(push, "--force") {
		t.Fatalf("unsafe push arguments: %#v", push)
	}
}

type commandCall struct {
	binary    string
	arguments []string
}

type scriptRunner struct {
	repository string
	remoteURL  string
	commit     string
	ghError    error
	calls      []commandCall
}

func (r *scriptRunner) Run(_ context.Context, binary string, arguments []string, _ string) (string, error) {
	r.calls = append(r.calls, commandCall{binary: binary, arguments: append([]string(nil), arguments...)})
	if binary == "gh" {
		return "", r.ghError
	}
	if contains(arguments, "--show-toplevel") {
		return r.repository, nil
	}
	if contains(arguments, "get-url") {
		return r.remoteURL, nil
	}
	if contains(arguments, "--verify") {
		return r.commit, nil
	}
	return "", nil
}

func repositoryWithSessionCommit(t *testing.T) (string, session.Metadata) {
	t.Helper()
	source := t.TempDir()
	runTestGit(t, "init", "-b", "main", source)
	runTestGit(t, "-C", source, "config", "user.name", "Test User")
	runTestGit(t, "-C", source, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(source, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, "-C", source, "add", "base.txt")
	runTestGit(t, "-C", source, "commit", "-m", "base")
	base := runTestGit(t, "-C", source, "rev-parse", "HEAD")
	id := "example-20260905-010203-a1b2"
	branch := "codegenbox/" + id
	runTestGit(t, "-C", source, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(source, "change.txt"), []byte("change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, "-C", source, "add", "change.txt")
	runTestGit(t, "-C", source, "commit", "-m", "session change")
	imported := runTestGit(t, "-C", source, "rev-parse", "HEAD")
	return source, session.Metadata{ID: id, Repository: source, BaseBranch: "main", BaseCommit: base, SessionBranch: branch, ImportedCommit: imported}
}

func testMetadata(repository string) session.Metadata {
	return session.Metadata{ID: "example-20260905-010203-a1b2", Repository: repository, BaseBranch: "main", BaseCommit: strings.Repeat("a", 40), SessionBranch: "codegenbox/example-20260905-010203-a1b2", ImportedCommit: strings.Repeat("b", 40)}
}

func runTestGit(t *testing.T, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func ExampleCompareURL() {
	metadata := session.Metadata{ID: "demo-20260905-010203-a1b2", Repository: "/tmp/repo", BaseBranch: "main", BaseCommit: strings.Repeat("a", 40), SessionBranch: "codegenbox/demo-20260905-010203-a1b2", ImportedCommit: strings.Repeat("b", 40)}
	address, _ := CompareURL(metadata, GitHubRemote{Owner: "acme", Repo: "project"})
	fmt.Println(address)
	// Output: https://github.com/acme/project/compare/main...codegenbox/demo-20260905-010203-a1b2?expand=1
}
