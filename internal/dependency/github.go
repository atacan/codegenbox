// Package dependency resolves the deliberately narrow private-dependency
// authorization surface. It never reads host Git, GitHub, or SSH state.
package dependency

import (
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/codegenbox/codegenbox/internal/container"
)

// GitHubReadTokenEnvironment is intentionally opt-in. The value must be a
// dedicated fine-grained GitHub token with only read access to selected
// dependency repositories; it must not be the token used by host gh or Git.
const GitHubReadTokenEnvironment = "CODEGENBOX_GITHUB_READ_TOKEN"

// Resolve returns disabled authorization when the opt-in environment variable
// is absent. The token is kept only in the in-memory Invocation and is never
// recorded in session metadata or Docker command arguments.
func Resolve() (container.PrivateDependencyAuthorization, error) {
	token := os.Getenv(GitHubReadTokenEnvironment)
	if token == "" {
		return container.PrivateDependencyAuthorization{}, nil
	}
	if err := validateGitHubToken(token); err != nil {
		return container.PrivateDependencyAuthorization{}, fmt.Errorf("invalid %s: %w", GitHubReadTokenEnvironment, err)
	}
	return container.PrivateDependencyAuthorization{GitHubReadToken: token}, nil
}

func validateGitHubToken(token string) error {
	if !strings.HasPrefix(token, "github_pat_") {
		return fmt.Errorf("token must be a fine-grained GitHub token")
	}
	if len(token) > 1024 {
		return fmt.Errorf("token is too long")
	}
	for _, character := range token {
		if character > unicode.MaxASCII || !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-') {
			return fmt.Errorf("token contains unsupported characters")
		}
	}
	return nil
}
