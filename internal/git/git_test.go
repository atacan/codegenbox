package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateImportedSessionTipRequiresExactLocalTip(t *testing.T) {
	repository := newTestRepository(t)
	branch := "codegenbox/project-20260903-193012-a82f"
	base := testGit(t, repository, "rev-parse", "HEAD")
	testGit(t, repository, "branch", branch, base)
	if err := ValidateImportedSessionTip(context.Background(), repository, branch, base); err != nil {
		t.Fatalf("exact tip: %v", err)
	}
	if err := ValidateImportedSessionTip(context.Background(), repository, "codegenbox/../bad", base); err == nil {
		t.Fatal("malformed branch accepted")
	}
	if err := ValidateImportedSessionTip(context.Background(), repository, branch, "not-an-oid"); err == nil {
		t.Fatal("malformed OID accepted")
	}
	if err := ValidateImportedSessionTip(context.Background(), repository, "codegenbox/missing-20260903-193012-a82f", base); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing branch error = %v", err)
	}
	writeTestFile(t, filepath.Join(repository, "next.txt"), "next\n")
	testGit(t, repository, "add", "next.txt")
	testGit(t, repository, "commit", "-m", "next")
	advanced := testGit(t, repository, "rev-parse", "HEAD")
	testGit(t, repository, "update-ref", "refs/heads/"+branch, advanced)
	if err := ValidateImportedSessionTip(context.Background(), repository, branch, base); err == nil || !strings.Contains(err.Error(), "no longer matches") {
		t.Fatalf("advanced branch error = %v", err)
	}
	if got := testGit(t, repository, "rev-parse", branch); got != advanced {
		t.Fatalf("validation changed ref to %s, want %s", got, advanced)
	}
}

func newTestRepository(t *testing.T) string {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	testGit(t, repository, "init", "-b", "main")
	testGit(t, repository, "config", "user.name", "Test")
	testGit(t, repository, "config", "user.email", "test@example.invalid")
	writeTestFile(t, filepath.Join(repository, "README.md"), "initial\n")
	testGit(t, repository, "add", "README.md")
	testGit(t, repository, "commit", "-m", "initial")
	return repository
}

func testGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	output, err := exec.Command("git", append([]string{"-C", directory}, arguments...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveBaseBranchRejectsNonLocalBranches(t *testing.T) {
	repository := newTestRepository(t)
	base, err := Discover(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	testGit(t, repository, "tag", "only-tag")
	testGit(t, repository, "update-ref", "refs/remotes/origin/main", base.BaseCommit)
	for _, branch := range []string{"", "missing", "only-tag", "origin/main", base.BaseCommit, "main~0", "main^{commit}", "../main", "--help", "refs/heads/main"} {
		if _, err := ResolveBaseBranch(context.Background(), base, branch); err == nil {
			t.Errorf("accepted non-local branch %q", branch)
		}
	}
}

func TestShallowSessionFromPartialSourceImportsAndContinues(t *testing.T) {
	for _, shallow := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial", true: "shallow-partial"}[shallow], func(t *testing.T) {
			ctx := context.Background()
			origin := newTestRepository(t)
			testGit(t, origin, "config", "uploadpack.allowFilter", "true")
			writeTestFile(t, filepath.Join(origin, "historical.txt"), strings.Repeat("old data\n", 1000))
			testGit(t, origin, "add", ".")
			testGit(t, origin, "commit", "-m", "historical file")
			historicalBlob := testGit(t, origin, "rev-parse", "HEAD:historical.txt")
			testGit(t, origin, "rm", "historical.txt")
			writeTestFile(t, filepath.Join(origin, "main-only.txt"), "selected snapshot\n")
			testGit(t, origin, "add", ".")
			testGit(t, origin, "commit", "-m", "main snapshot")
			base := testGit(t, origin, "rev-parse", "HEAD")
			parent := testGit(t, origin, "rev-parse", "HEAD^")
			snapshotBlob := testGit(t, origin, "rev-parse", "HEAD:main-only.txt")
			testGit(t, origin, "checkout", "-b", "feature")
			testGit(t, origin, "rm", "main-only.txt")
			writeTestFile(t, filepath.Join(origin, "feature.txt"), "feature\n")
			testGit(t, origin, "add", ".")
			testGit(t, origin, "commit", "-m", "feature snapshot")

			source := filepath.Join(t.TempDir(), "source")
			args := []string{"clone", "--no-local", "--filter=blob:none", "--no-checkout", "--no-tags", "--no-single-branch"}
			if shallow {
				args = append(args, "--depth=2")
			}
			args = append(args, origin, source)
			testGit(t, origin, args...)
			testGit(t, source, "checkout", "feature")
			testGit(t, source, "branch", "main", "origin/main")
			writeTestFile(t, filepath.Join(source, "feature.txt"), "uncommitted work\n")
			configBefore, err := os.ReadFile(filepath.Join(source, ".git", "config"))
			if err != nil {
				t.Fatal(err)
			}
			shallowBefore, _ := os.ReadFile(filepath.Join(source, ".git", "shallow"))
			refsBefore := testGit(t, source, "show-ref")
			statusBefore := testGit(t, source, "status", "--porcelain")
			featureTip := testGit(t, source, "rev-parse", "HEAD")
			assertObjectAbsent(t, source, snapshotBlob)
			assertObjectAbsent(t, source, historicalBlob)

			state := t.TempDir()
			branch := "codegenbox/shallow-test"
			workspace := filepath.Join(state, "workspace")
			if err := CreateSessionClone(ctx, source, workspace, branch, base); err != nil {
				t.Fatal(err)
			}
			if got := testGit(t, workspace, "rev-list", "--count", "HEAD"); got != "1" {
				t.Fatalf("session commit count = %s, want 1", got)
			}
			if got := testGit(t, workspace, "show-ref"); got != base+" refs/heads/"+branch {
				t.Fatalf("unexpected session refs: %s", got)
			}
			if got := testGit(t, workspace, "remote"); got != "" {
				t.Fatalf("session has remote: %s", got)
			}
			contents, _ := os.ReadFile(filepath.Join(workspace, "main-only.txt"))
			if string(contents) != "selected snapshot\n" {
				t.Fatalf("selected snapshot file = %q", contents)
			}
			assertObjectAbsent(t, workspace, historicalBlob)
			assertObjectAbsent(t, workspace, parent)
			assertObjectAbsent(t, workspace, featureTip)
			assertObjectAbsent(t, source, historicalBlob)
			// The complete session snapshot and subsequent imports must work
			// offline without the promisor remote or historical blobs.
			if err := os.Rename(origin, origin+"-offline"); err != nil {
				t.Fatal(err)
			}
			if tip, err := ImportSessionBranch(ctx, source, workspace, branch, base, "", state); err != nil || tip != base {
				t.Fatalf("no-op import = %s, %v", tip, err)
			}
			writeTestFile(t, filepath.Join(workspace, "new.txt"), "session change\n")
			testGit(t, workspace, "add", ".")
			testGit(t, workspace, "commit", "-m", "first session change")
			firstTip := testGit(t, workspace, "rev-parse", "HEAD")
			if tip, err := ImportSessionBranch(ctx, source, workspace, branch, base, base, state); err != nil || tip != firstTip {
				t.Fatalf("first import = %s, %v", tip, err)
			}
			// A resumed clone cannot rewind already-imported work even if it
			// still descends from the original base.
			testGit(t, workspace, "update-ref", "refs/heads/"+branch, base)
			if _, err := ImportSessionBranch(ctx, source, workspace, branch, base, firstTip, state); err == nil || !strings.Contains(err.Error(), "import policy rejected") {
				t.Fatalf("rewound import error = %v", err)
			}
			if got := testGit(t, source, "rev-parse", branch); got != firstTip {
				t.Fatalf("rejected import changed source tip: %s", got)
			}
			continued := filepath.Join(state, "continued")
			if err := CreateSessionClone(ctx, source, continued, branch, firstTip); err != nil {
				t.Fatal(err)
			}
			if got := testGit(t, continued, "rev-list", "--count", "HEAD"); got != "1" {
				t.Fatalf("continued commit count = %s", got)
			}
			if _, err := ImportSessionBranch(ctx, source, continued, branch, base, firstTip, state); err != nil {
				t.Fatalf("continued no-op: %v", err)
			}
			writeTestFile(t, filepath.Join(continued, "next.txt"), "continued change\n")
			testGit(t, continued, "add", ".")
			testGit(t, continued, "commit", "-m", "continued session change")
			tip, err := ImportSessionBranch(ctx, source, continued, branch, base, firstTip, state)
			if err != nil {
				t.Fatalf("continued import: %v", err)
			}
			if got := testGit(t, source, "show", tip+":next.txt"); got != "continued change" {
				t.Fatalf("imported file = %q", got)
			}
			if got := testGit(t, source, "rev-list", "--count", base+".."+tip); got != "2" {
				t.Fatalf("imported commit count = %s", got)
			}
			configAfter, _ := os.ReadFile(filepath.Join(source, ".git", "config"))
			shallowAfter, _ := os.ReadFile(filepath.Join(source, ".git", "shallow"))
			if string(configBefore) != string(configAfter) || string(shallowBefore) != string(shallowAfter) {
				t.Fatal("source partial/shallow configuration changed")
			}
			if testGit(t, source, "branch", "--show-current") != "feature" || testGit(t, source, "rev-parse", "HEAD") != featureTip || testGit(t, source, "status", "--porcelain") != statusBefore {
				t.Fatal("source checkout changed")
			}
			refsAfter := testGit(t, source, "show-ref")
			refsAfter = strings.TrimSpace(strings.ReplaceAll(refsAfter, tip+" refs/heads/"+branch, ""))
			if refsAfter != refsBefore {
				t.Fatalf("unrelated source refs changed:\n%s\n%s", refsBefore, refsAfter)
			}
			assertObjectAbsent(t, source, historicalBlob)
		})
	}
}

func assertObjectAbsent(t *testing.T, repository, oid string) {
	t.Helper()
	cmd := exec.Command("git", "-C", repository, "cat-file", "-e", oid)
	cmd.Env = append(safeGitEnvironment(), "GIT_NO_LAZY_FETCH=1")
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("object %s unexpectedly present in %s: %s", oid, repository, output)
	}
}
