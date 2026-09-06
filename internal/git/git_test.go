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
