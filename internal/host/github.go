// Package host implements post-container host-side GitHub operations.
//
// It deliberately accepts only validated session metadata and reads remote
// configuration from the original source repository. Session clones are never
// consulted: they are agent-writable and may already have been removed.
package host

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/codegenbox/codegenbox/internal/session"
)

const remoteName = "origin"

// Summary is the host-side view of a completed or partially completed session.
type Summary struct {
	Repository     string
	BaseBranch     string
	BaseCommit     string
	SessionBranch  string
	ImportedCommit string
	Commits        []Commit
	ChangedFiles   int
	Insertions     int
	Deletions      int
}

// Commit is intentionally limited to an object ID and its first-line subject.
type Commit struct {
	ID      string
	Subject string
}

// GitHubRemote is a recognized github.com repository remote.
type GitHubRemote struct {
	URL   string
	Owner string
	Repo  string
}

// PushResult records whether a successful fixed-ref push has a GitHub remote.
// GitHub is nil for a non-GitHub source remote, where compare/PR helpers are
// intentionally unavailable. The raw origin URL is intentionally not exposed.
type PushResult struct {
	GitHub *GitHubRemote
}

// CompareHandoff is the result of the opt-in host-side GitHub handoff. URL is
// returned even when opening the browser fails, so callers can show a safe
// recovery link without retrying the push.
type CompareHandoff struct {
	URL string
}

// CommandRunner lets tests observe argument boundaries without a shell.
type CommandRunner interface {
	Run(context.Context, string, []string, string) (string, error)
}

type execRunner struct{}

var findExecutable = exec.LookPath

func (execRunner) Run(ctx context.Context, binary string, arguments []string, directory string) (string, error) {
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Dir = directory
	command.Env = hostEnvironment()
	output, err := command.CombinedOutput()
	if err != nil {
		// In particular, do not echo a source remote URL here: although uncommon,
		// a URL can contain embedded credentials. The user can run the explicit
		// host command themselves for detailed diagnostics if needed.
		return "", fmt.Errorf("%s command failed: %w", binary, err)
	}
	return strings.TrimSpace(string(output)), nil
}

// Summarize calculates commit and changed-file information from the trusted
// source repository. It never opens the session workspace.
func Summarize(ctx context.Context, metadata session.Metadata) (Summary, error) {
	return SummarizeWithRunner(ctx, metadata, execRunner{})
}

func SummarizeWithRunner(ctx context.Context, metadata session.Metadata, runner CommandRunner) (Summary, error) {
	if err := validateMetadata(metadata, false); err != nil {
		return Summary{}, err
	}
	if runner == nil {
		return Summary{}, fmt.Errorf("host command runner is required")
	}
	if err := validateSourceRepository(ctx, metadata.Repository, runner); err != nil {
		return Summary{}, err
	}

	commitOutput, err := runGit(ctx, runner, metadata.Repository, "log", "--format=%H%x00%s", metadata.BaseCommit+".."+metadata.ImportedCommit)
	if err != nil {
		return Summary{}, fmt.Errorf("read session commits: %w", err)
	}
	commits, err := parseCommits(commitOutput)
	if err != nil {
		return Summary{}, err
	}
	numstat, err := runGit(ctx, runner, metadata.Repository, "diff", "--numstat", "--no-renames", metadata.BaseCommit, metadata.ImportedCommit)
	if err != nil {
		return Summary{}, fmt.Errorf("read changed-file statistics: %w", err)
	}
	files, additions, deletions, err := parseNumstat(numstat)
	if err != nil {
		return Summary{}, err
	}
	return Summary{
		Repository: metadata.Repository, BaseBranch: metadata.BaseBranch,
		BaseCommit: metadata.BaseCommit, SessionBranch: metadata.SessionBranch,
		ImportedCommit: metadata.ImportedCommit, Commits: commits,
		ChangedFiles: files, Insertions: additions, Deletions: deletions,
	}, nil
}

// DetectGitHubRemote returns the origin push URL only when it is a github.com
// owner/repository remote. The lookup is made exclusively in sourceRepository.
func DetectGitHubRemote(ctx context.Context, sourceRepository string) (GitHubRemote, error) {
	return DetectGitHubRemoteWithRunner(ctx, sourceRepository, execRunner{})
}

func DetectGitHubRemoteWithRunner(ctx context.Context, sourceRepository string, runner CommandRunner) (GitHubRemote, error) {
	if runner == nil {
		return GitHubRemote{}, fmt.Errorf("host command runner is required")
	}
	if err := validateSourceRepository(ctx, sourceRepository, runner); err != nil {
		return GitHubRemote{}, err
	}
	remoteURL, err := SourcePushURLWithRunner(ctx, sourceRepository, runner)
	if err != nil {
		return GitHubRemote{}, err
	}
	remote, err := ParseGitHubRemote(remoteURL)
	if err != nil {
		return GitHubRemote{}, err
	}
	return remote, nil
}

// SourcePushURL reads only the source repository's origin push URL. It does
// not inspect the session clone or use a user-provided remote name.
func SourcePushURL(ctx context.Context, sourceRepository string) (string, error) {
	return SourcePushURLWithRunner(ctx, sourceRepository, execRunner{})
}

func SourcePushURLWithRunner(ctx context.Context, sourceRepository string, runner CommandRunner) (string, error) {
	if runner == nil {
		return "", fmt.Errorf("host command runner is required")
	}
	if err := validateSourceRepository(ctx, sourceRepository, runner); err != nil {
		return "", err
	}
	remoteURL, err := runGit(ctx, runner, sourceRepository, "remote", "get-url", "--push", remoteName)
	if err != nil {
		return "", fmt.Errorf("read source repository %s remote: %w", remoteName, err)
	}
	if remoteURL == "" || strings.ContainsAny(remoteURL, "\x00\r\n") {
		return "", fmt.Errorf("invalid source repository remote URL")
	}
	return remoteURL, nil
}

// ParseGitHubRemote recognizes the standard HTTPS, SSH URL, and SCP-like
// github.com remote forms. Enterprise URLs are intentionally out of scope.
func ParseGitHubRemote(remoteURL string) (GitHubRemote, error) {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" || strings.ContainsAny(remoteURL, "\x00\r\n") {
		return GitHubRemote{}, fmt.Errorf("invalid GitHub remote URL")
	}
	path := ""
	switch {
	case strings.HasPrefix(strings.ToLower(remoteURL), "https://github.com/"):
		path = remoteURL[len("https://github.com/"):]
	case strings.HasPrefix(strings.ToLower(remoteURL), "http://github.com/"):
		path = remoteURL[len("http://github.com/"):]
	case strings.HasPrefix(strings.ToLower(remoteURL), "ssh://"):
		withoutScheme := remoteURL[len("ssh://"):]
		at := strings.LastIndex(withoutScheme, "@")
		if at < 0 {
			return GitHubRemote{}, fmt.Errorf("source origin is not a github.com repository")
		}
		hostAndPath := withoutScheme[at+1:]
		if !strings.HasPrefix(strings.ToLower(hostAndPath), "github.com/") {
			return GitHubRemote{}, fmt.Errorf("source origin is not a github.com repository")
		}
		path = hostAndPath[len("github.com/"):]
	case strings.HasPrefix(strings.ToLower(remoteURL), "git@github.com:"):
		path = remoteURL[len("git@github.com:"):]
	default:
		return GitHubRemote{}, fmt.Errorf("source origin is not a github.com repository")
	}
	path = strings.TrimSuffix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || !validGitHubName(parts[0]) || !validGitHubName(parts[1]) {
		return GitHubRemote{}, fmt.Errorf("source origin is not a github.com owner/repository")
	}
	return GitHubRemote{URL: remoteURL, Owner: parts[0], Repo: parts[1]}, nil
}

// CompareURL returns the browser-safe compare URL for this specific session.
func CompareURL(metadata session.Metadata, remote GitHubRemote) (string, error) {
	if err := validateMetadata(metadata, true); err != nil {
		return "", err
	}
	if !validGitHubName(remote.Owner) || !validGitHubName(remote.Repo) {
		return "", fmt.Errorf("invalid GitHub repository")
	}
	return fmt.Sprintf("https://github.com/%s/%s/compare/%s...%s?expand=1", remote.Owner, remote.Repo, metadata.BaseBranch, metadata.SessionBranch), nil
}

// PushSessionBranch pushes exactly the generated session branch to origin's
// configured push URL. It does not use an agent clone, force mode, or a remote
// configured refspec.
func PushSessionBranch(ctx context.Context, metadata session.Metadata) (PushResult, error) {
	return PushSessionBranchWithRunner(ctx, metadata, execRunner{})
}

func PushSessionBranchWithRunner(ctx context.Context, metadata session.Metadata, runner CommandRunner) (PushResult, error) {
	if err := validateMetadata(metadata, true); err != nil {
		return PushResult{}, err
	}
	if runner == nil {
		return PushResult{}, fmt.Errorf("host command runner is required")
	}
	remoteURL, err := SourcePushURLWithRunner(ctx, metadata.Repository, runner)
	if err != nil {
		return PushResult{}, err
	}
	if err := validateImportedBranch(ctx, metadata, runner); err != nil {
		return PushResult{}, err
	}
	refspec := "refs/heads/" + metadata.SessionBranch + ":refs/heads/" + metadata.SessionBranch
	if _, err := runGit(ctx, runner, metadata.Repository, "push", "--porcelain", "--no-verify", "--", remoteURL, refspec); err != nil {
		return PushResult{}, fmt.Errorf("push generated session branch: %w", err)
	}
	result := PushResult{}
	if remote, parseErr := ParseGitHubRemote(remoteURL); parseErr == nil {
		result.GitHub = &remote
	}
	return result, nil
}

// PushAndOpenCompare is the opt-in automatic counterpart to running push and
// compare manually. It first requires a standard github.com origin, then uses
// the existing fixed-ref non-force push before opening its compare page.
func PushAndOpenCompare(ctx context.Context, metadata session.Metadata) (CompareHandoff, error) {
	return PushAndOpenCompareWithRunner(ctx, metadata, execRunner{}, OpenBrowser)
}

// PushAndOpenCompareWithRunner keeps the handoff testable while preserving the
// command boundaries used by the production host implementation.
func PushAndOpenCompareWithRunner(ctx context.Context, metadata session.Metadata, runner CommandRunner, openBrowser func(context.Context, string) error) (CompareHandoff, error) {
	if err := validateMetadata(metadata, true); err != nil {
		return CompareHandoff{}, err
	}
	if metadata.State != session.StateCompleted || metadata.ImportedCommit == metadata.BaseCommit {
		return CompareHandoff{}, fmt.Errorf("automatic GitHub handoff requires a completed session with a new imported commit")
	}
	if runner == nil {
		return CompareHandoff{}, fmt.Errorf("host command runner is required")
	}
	if openBrowser == nil {
		return CompareHandoff{}, fmt.Errorf("browser opener is required")
	}
	// Automatic publishing intentionally does not fall back to non-GitHub
	// origins, even though the explicit push command supports them.
	if _, err := DetectGitHubRemoteWithRunner(ctx, metadata.Repository, runner); err != nil {
		return CompareHandoff{}, err
	}
	pushed, err := PushSessionBranchWithRunner(ctx, metadata, runner)
	if err != nil {
		return CompareHandoff{}, err
	}
	if pushed.GitHub == nil {
		return CompareHandoff{}, fmt.Errorf("source origin changed to a non-GitHub remote during automatic handoff")
	}
	address, err := CompareURL(metadata, *pushed.GitHub)
	if err != nil {
		return CompareHandoff{}, err
	}
	handoff := CompareHandoff{URL: address}
	if err := openBrowser(ctx, address); err != nil {
		return handoff, err
	}
	return handoff, nil
}

// CreatePullRequest uses the host gh CLI only after the explicit pr command.
// It never asks gh to push a local branch; callers must push first.
func CreatePullRequest(ctx context.Context, metadata session.Metadata) (string, error) {
	return CreatePullRequestWithRunner(ctx, metadata, execRunner{})
}

func CreatePullRequestWithRunner(ctx context.Context, metadata session.Metadata, runner CommandRunner) (string, error) {
	if err := validateMetadata(metadata, true); err != nil {
		return "", err
	}
	if runner == nil {
		return "", fmt.Errorf("host command runner is required")
	}
	remote, err := DetectGitHubRemoteWithRunner(ctx, metadata.Repository, runner)
	if err != nil {
		return "", err
	}
	if err := validateImportedBranch(ctx, metadata, runner); err != nil {
		return "", err
	}
	if _, err := findExecutable("gh"); err != nil {
		return "", fmt.Errorf("host gh CLI is required to create a pull request: %w", err)
	}
	output, err := runner.Run(ctx, "gh", []string{"pr", "create", "--repo", remote.Owner + "/" + remote.Repo, "--base", metadata.BaseBranch, "--head", metadata.SessionBranch, "--fill"}, metadata.Repository)
	if err != nil {
		return "", fmt.Errorf("create GitHub pull request: %w", err)
	}
	return output, nil
}

func validateMetadata(metadata session.Metadata, requireImported bool) error {
	if metadata.ID == "" || metadata.SessionBranch != "codegenbox/"+metadata.ID || !validSessionID(metadata.ID) {
		return fmt.Errorf("invalid generated session branch metadata")
	}
	if metadata.Repository == "" || !filepath.IsAbs(metadata.Repository) {
		return fmt.Errorf("invalid source repository metadata")
	}
	if !validRefName(metadata.BaseBranch) || !validOID(metadata.BaseCommit) {
		return fmt.Errorf("invalid base metadata")
	}
	if requireImported && !validOID(metadata.ImportedCommit) {
		return fmt.Errorf("missing or invalid imported commit")
	}
	return nil
}

func validateSourceRepository(ctx context.Context, repository string, runner CommandRunner) error {
	canonical, err := filepath.EvalSymlinks(repository)
	if err != nil {
		return fmt.Errorf("resolve source repository: %w", err)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return fmt.Errorf("resolve source repository: %w", err)
	}
	root, err := runGit(ctx, runner, canonical, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("validate source repository: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve source repository root: %w", err)
	}
	if filepath.Clean(root) != filepath.Clean(canonical) {
		return fmt.Errorf("recorded source repository is not its Git root")
	}
	return nil
}

// validateImportedBranch ensures an explicit host write can only publish the
// exact local session ref that post-exit import recorded. It prevents a later
// local ref change or malformed metadata from turning a fixed ref name into a
// push of an unrelated commit.
func validateImportedBranch(ctx context.Context, metadata session.Metadata, runner CommandRunner) error {
	commit, err := runGit(ctx, runner, metadata.Repository, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+metadata.SessionBranch+"^{commit}")
	if err != nil {
		return fmt.Errorf("validate generated session branch: %w", err)
	}
	if commit != metadata.ImportedCommit {
		return fmt.Errorf("generated session branch no longer matches its imported commit")
	}
	return nil
}

func runGit(ctx context.Context, runner CommandRunner, directory string, arguments ...string) (string, error) {
	args := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-C", directory}
	args = append(args, arguments...)
	return runner.Run(ctx, "git", args, directory)
}

func parseCommits(output string) ([]Commit, error) {
	if output == "" {
		return nil, nil
	}
	records := strings.Split(output, "\n")
	commits := make([]Commit, 0, len(records))
	for _, record := range records {
		parts := strings.SplitN(record, "\x00", 2)
		if len(parts) != 2 || !validOID(parts[0]) || !safeCommitSubject(parts[1]) {
			return nil, fmt.Errorf("malformed commit summary from source repository")
		}
		commits = append(commits, Commit{ID: parts[0], Subject: parts[1]})
	}
	return commits, nil
}

func safeCommitSubject(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func parseNumstat(output string) (files, insertions, deletions int, err error) {
	if output == "" {
		return 0, 0, 0, nil
	}
	for _, line := range strings.Split(output, "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) < 3 || !validStatNumber(parts[0]) || !validStatNumber(parts[1]) || parts[2] == "" {
			return 0, 0, 0, fmt.Errorf("malformed changed-file statistics from source repository")
		}
		files++
		if parts[0] != "-" {
			var count int
			if _, scanErr := fmt.Sscanf(parts[0], "%d", &count); scanErr != nil {
				return 0, 0, 0, fmt.Errorf("malformed insertion count")
			}
			insertions += count
		}
		if parts[1] != "-" {
			var count int
			if _, scanErr := fmt.Sscanf(parts[1], "%d", &count); scanErr != nil {
				return 0, 0, 0, fmt.Errorf("malformed deletion count")
			}
			deletions += count
		}
	}
	return files, insertions, deletions, nil
}

func validStatNumber(value string) bool {
	if value == "-" {
		return true
	}
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func validGitHubName(value string) bool {
	if value == "" || len(value) > 100 {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.') {
			return false
		}
	}
	return true
}

func validSessionID(value string) bool {
	if value == "" || strings.Contains(value, "..") {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return value[0] >= 'a' && value[0] <= 'z' || value[0] >= '0' && value[0] <= '9'
}

func validOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validRefName(value string) bool {
	if value == "" || value == "@" || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.HasSuffix(value, ".") || strings.Contains(value, "//") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	for _, character := range value {
		if unicode.IsSpace(character) || unicode.IsControl(character) || strings.ContainsRune("~^:?*[\\", character) {
			return false
		}
	}
	return true
}

func hostEnvironment() []string {
	environment := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		// Source operations must not inherit a redirected Git directory, index,
		// alternate object store, config, or hook/program override. Host auth is
		// still available through the normal credential helper, SSH agent, and
		// host configuration files; none of those are mounted into Docker.
		if strings.HasPrefix(name, "GIT_") {
			continue
		}
		environment = append(environment, entry)
	}
	return environment
}
