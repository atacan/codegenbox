package dependency

import (
	"strings"
	"testing"
)

func TestResolveGitHubReadTokenIsOptInAndValidatesInput(t *testing.T) {
	t.Run("absent disables forwarding", func(t *testing.T) {
		t.Setenv(GitHubReadTokenEnvironment, "")
		authorization, err := Resolve()
		if err != nil || authorization.GitHubReadToken != "" {
			t.Fatalf("Resolve() = %#v, %v; want disabled authorization", authorization, err)
		}
	})
	t.Run("fine grained token shape", func(t *testing.T) {
		t.Setenv(GitHubReadTokenEnvironment, "github_pat_0123456789_ABCDEFGHIJKLMNOPQRSTUVWXYZ")
		authorization, err := Resolve()
		if err != nil || authorization.GitHubReadToken != "github_pat_0123456789_ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
			t.Fatalf("Resolve() = %#v, %v", authorization, err)
		}
	})
	for _, token := range []string{"ghp_classicToken", "token with spaces", "token\nnext", "token$command", strings.Repeat("a", 1025)} {
		t.Run("reject invalid token", func(t *testing.T) {
			t.Setenv(GitHubReadTokenEnvironment, token)
			if _, err := Resolve(); err == nil {
				t.Fatalf("Resolve accepted %q", token)
			}
		})
	}
}
