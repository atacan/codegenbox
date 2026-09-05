package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type commandFunc func(context.Context, string, ...string) (string, error)

func (f commandFunc) Output(ctx context.Context, name string, args ...string) (string, error) {
	return f(ctx, name, args...)
}
func TestRunReportsEachReadinessCheck(t *testing.T) {
	checks := Run(context.Background(), commandFunc(func(_ context.Context, _ string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "inspect") {
			return "1\n", nil
		}
		return "ok", nil
	}), "docker", "image", t.TempDir())
	if len(checks) != 5 {
		t.Fatalf("checks = %#v", checks)
	}
	for _, check := range checks {
		if check.Err != nil {
			t.Fatalf("%s: %v", check.Name, check.Err)
		}
	}
	checks = Run(context.Background(), commandFunc(func(context.Context, string, ...string) (string, error) { return "", errors.New("no") }), "docker", "image", t.TempDir())
	if checks[0].Err == nil {
		t.Fatal("failed Git check not reported")
	}
}
