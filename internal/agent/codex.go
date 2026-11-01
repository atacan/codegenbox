package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

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

// privateFileExists never follows a destination link, including hardlinks.
func privateFileExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect private agent file: %w", err)
	}
	if !info.Mode().IsRegular() || hasMultipleLinks(info) {
		return false, fmt.Errorf("private agent %s must be a regular file without filesystem links", filepath.Base(path))
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return false, fmt.Errorf("restrict private agent file: %w", err)
	}
	return true, nil
}

func writePrivateFile(path string, source io.Reader) (err error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create private agent file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	written, copyErr := io.Copy(file, source)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written > maxSeedFile {
		return fmt.Errorf("could not copy private agent file (maximum %d bytes)", maxSeedFile)
	}
	return nil
}

// Source reads are read-only and nonblocking: FIFOs/devices cannot stall login
// setup. Error messages never include configuration or credential contents.
func readSeedFile(path string, required bool) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) && !required {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open agent seed file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSeedFile {
		return nil, fmt.Errorf("agent seed must be a regular file no larger than %d bytes", maxSeedFile)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSeedFile+1))
	if err != nil || len(data) > maxSeedFile {
		return nil, fmt.Errorf("could not read bounded agent seed file")
	}
	return data, nil
}
