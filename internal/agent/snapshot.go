package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type snapshotBudget struct {
	files int
	bytes int64
}

func copySkills(source, destination string) error {
	entries, err := os.ReadDir(source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("enumerate authored skills: %w", err)
	}
	budget := &snapshotBudget{}
	for _, entry := range entries {
		if excludedSnapshotName(entry.Name()) {
			continue
		}
		skill := filepath.Join(source, entry.Name())
		root, err := filepath.EvalSymlinks(skill)
		if err != nil {
			continue
		} // Broken host-only links are not portable skills.
		info, err := os.Stat(filepath.Join(root, "SKILL.md"))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		target := filepath.Join(destination, entry.Name())
		if err := copySnapshotTree(root, target, root, map[string]bool{}, budget); err != nil {
			return err
		}
	}
	return nil
}

func copyAuthoredTree(source, destination string) error {
	root, err := filepath.EvalSymlinks(source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolve authored agent definitions: %w", err)
	}
	return copySnapshotTree(root, destination, root, map[string]bool{}, &snapshotBudget{})
}

// A top-level authored-directory symlink is resolved once. Nested symlinks are
// followed only inside that resolved root; broken, escaping, and cyclic links
// are skipped. Every emitted file is a new copy, never a filesystem link.
func copySnapshotTree(source, destination, root string, ancestors map[string]bool, budget *snapshotBudget) error {
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil || !isWithin(resolved, root) || ancestors[resolved] {
		return nil
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("inspect authored snapshot file: %w", err)
	}
	if info.IsDir() {
		if err := ensurePrivateDirectory(destination); err != nil {
			return err
		}
		ancestors[resolved] = true
		defer delete(ancestors, resolved)
		entries, err := os.ReadDir(resolved)
		if err != nil {
			return fmt.Errorf("enumerate authored snapshot files: %w", err)
		}
		for _, entry := range entries {
			if excludedSnapshotName(entry.Name()) {
				continue
			}
			if err := copySnapshotTree(filepath.Join(resolved, entry.Name()), filepath.Join(destination, entry.Name()), root, ancestors, budget); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	budget.files++
	budget.bytes += info.Size()
	if budget.files > 10000 || budget.bytes > 128<<20 || info.Size() > maxSeedFile {
		return fmt.Errorf("authored agent snapshot exceeds 10000 files, 128 MiB total, or 16 MiB per file")
	}
	if err := copySeedFile(resolved, destination, false, nil); err != nil {
		return err
	}
	// Scripts remain executable in their private copy; all other files are 0600.
	if info.Mode().Perm()&0o111 != 0 {
		if err := os.Chmod(destination, 0o700); err != nil {
			return fmt.Errorf("set private script permissions: %w", err)
		}
	}
	return nil
}

func excludedSnapshotName(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasPrefix(name, ".") {
		return true
	}
	for _, suffix := range []string{".sqlite", ".sqlite3", ".sqlite-wal", ".sqlite-shm", ".sqlite3-wal", ".sqlite3-shm", ".sqlite-journal", ".sqlite3-journal", ".db", ".db-wal", ".db-shm", ".db-journal", ".jsonl", ".jsonl.zst", ".jsonl.gz", ".pyc"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	switch lower {
	case "node_modules", "__pycache__", "cache", "caches", "logs", "sessions", "history", "conversations", "backups", "auth.json", "credentials.json":
		return true
	}
	return false
}
