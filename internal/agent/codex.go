package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/codegenbox/codegenbox/internal/container"
)

// prepareCodexHome reads host authentication only. Nothing in the host Codex
// home is mounted, modified, linked, or recursively copied. Refreshes stay in
// this session's writable home, including across resume/continue.
func prepareCodexHome(path string) (string, error) {
	canonical, err := canonicalExistingPath(path)
	if err != nil {
		return "", fmt.Errorf("resolve private Codex home: %w", err)
	}
	if !filepath.IsAbs(path) || canonical != filepath.Clean(path) || hasPathTraversal(path) || strings.ContainsAny(path, ",\x00") {
		return "", fmt.Errorf("private Codex home must be an absolute path without symlink aliases, traversal, or mount delimiters")
	}
	if err := container.ValidatePrivateCodexPath(canonical); err != nil {
		return "", err
	}
	if err := os.MkdirAll(canonical, 0o700); err != nil {
		return "", fmt.Errorf("create private Codex home: %w", err)
	}
	if err := os.Chmod(canonical, 0o700); err != nil {
		return "", fmt.Errorf("restrict private Codex home: %w", err)
	}
	// A container-owned config explicitly selects the file cache supported by
	// Codex. Existing private configuration is preserved; host config is ignored.
	config := filepath.Join(canonical, "config.toml")
	if exists, err := privateCodexFileExists(config); err != nil {
		return "", err
	} else if !exists {
		if err := writePrivateFile(config, strings.NewReader("cli_auth_credentials_store = \"file\"\n")); err != nil {
			return "", err
		}
	}
	auth := filepath.Join(canonical, "auth.json")
	if exists, err := privateCodexFileExists(auth); err != nil {
		return "", err
	} else if exists {
		return canonical, nil // Never replace Codex's refreshed credentials.
	}
	source, explicit, err := codexAuthSource()
	if err != nil {
		return "", err
	}
	if source == "" {
		return canonical, nil
	}
	file, err := os.Open(source) // Read-only snapshot, including atomic host replacements.
	if os.IsNotExist(err) && !explicit {
		return canonical, nil // First run or keyring-only host: use container login.
	}
	if err != nil {
		return "", fmt.Errorf("open Codex authentication source (use CODEGENBOX_CODEX_AUTH_FILE=none for independent login): %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return "", fmt.Errorf("Codex authentication source must be a regular file no larger than 1 MiB")
	}
	if err := writePrivateFile(auth, io.LimitReader(file, (1<<20)+1)); err != nil {
		return "", err
	}
	return canonical, nil
}

func codexAuthSource() (path string, explicit bool, err error) {
	if value := strings.TrimSpace(os.Getenv("CODEGENBOX_CODEX_AUTH_FILE")); value != "" {
		if value == "none" {
			return "", false, nil
		}
		return value, true, nil
	}
	directory := strings.TrimSpace(os.Getenv("CODEGENBOX_CODEX_STATE_DIR"))
	if directory == "" {
		directory = strings.TrimSpace(os.Getenv("CODEX_HOME"))
	}
	if directory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false, fmt.Errorf("resolve host Codex authentication: %w", err)
		}
		directory = filepath.Join(home, ".codex")
	}
	return filepath.Join(directory, "auth.json"), false, nil
}

func hasMultipleLinks(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink > 1
}

func privateCodexFileExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect private Codex file: %w", err)
	}
	if !info.Mode().IsRegular() || hasMultipleLinks(info) {
		return false, fmt.Errorf("private Codex %s must be a regular file without filesystem links", filepath.Base(path))
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return false, fmt.Errorf("restrict private Codex file: %w", err)
	}
	return true, nil
}

func writePrivateFile(path string, source io.Reader) (err error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create private Codex file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	written, copyErr := io.Copy(file, source)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written > 1<<20 {
		return fmt.Errorf("could not copy private Codex file (maximum 1 MiB)")
	}
	return nil
}
