// Package doctor contains non-mutating host readiness checks.
package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/codegenbox/codegenbox/internal/container"
)

type Command interface {
	Output(context.Context, string, ...string) (string, error)
}
type ExecCommand struct{}

func (ExecCommand) Output(ctx context.Context, name string, args ...string) (string, error) {
	result, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(result), err
}

type Check struct {
	Name string
	Err  error
}

func Run(ctx context.Context, command Command, dockerBinary, image, dataRoot string) []Check {
	checks := []Check{{"Git", probe(ctx, command, "git", "--version")}, {"Docker CLI", probe(ctx, command, dockerBinary, "--version")}, {"Docker daemon", probe(ctx, command, dockerBinary, "info", "--format", "{{.ServerVersion}}")}}
	checks = append(checks, Check{"compatible image", container.CheckImageCompatibility(ctx, inspector{command}, dockerBinary, image)})
	checks = append(checks, Check{"Codegenbox storage", writableDirectory(dataRoot)})
	return checks
}

type inspector struct{ Command }

func (i inspector) Output(ctx context.Context, binary string, args ...string) (string, error) {
	return i.Command.Output(ctx, binary, args...)
}
func probe(ctx context.Context, command Command, name string, args ...string) error {
	_, err := command.Output(ctx, name, args...)
	return err
}
func writableDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory")
	}
	test, err := os.CreateTemp(filepath.Clean(path), ".doctor-")
	if err != nil {
		return err
	}
	name := test.Name()
	if err := test.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Remove(name)
}
