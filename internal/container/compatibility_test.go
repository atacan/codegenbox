package container

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type inspectFunc func(context.Context, string, ...string) (string, error)

func (f inspectFunc) Output(ctx context.Context, binary string, args ...string) (string, error) {
	return f(ctx, binary, args...)
}

func TestCheckImageCompatibility(t *testing.T) {
	for _, test := range []struct {
		name, value string
		err         error
		wantErr     bool
	}{{"compatible", "1\n", nil, false}, {"legacy unlabeled", "<no value>\n", nil, false}, {"incompatible", "2\n", nil, true}, {"inspect failure", "", errors.New("daemon unavailable"), true}} {
		t.Run(test.name, func(t *testing.T) {
			err := CheckImageCompatibility(context.Background(), inspectFunc(func(context.Context, string, ...string) (string, error) { return test.value, test.err }), "docker", "image:test")
			if (err != nil) != test.wantErr {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestEnsureImageCompatibilityPullsOnlyWhenImageIsMissing(t *testing.T) {
	for _, test := range []struct {
		name       string
		inspectErr error
		wantPull   bool
		wantErr    bool
	}{
		{name: "already available"},
		{name: "missing", inspectErr: errors.New("exit status 1: Error response from daemon: No such image: image:test"), wantPull: true},
		{name: "daemon unavailable", inspectErr: errors.New("exit status 1: Cannot connect to the Docker daemon"), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			inspector := inspectFunc(func(_ context.Context, _ string, args ...string) (string, error) {
				calls = append(calls, strings.Join(args, " "))
				if strings.Contains(strings.Join(args, " "), "image inspect") && len(calls) == 1 && test.inspectErr != nil {
					return test.inspectErr.Error(), test.inspectErr
				}
				return "1\n", nil
			})
			err := EnsureImageCompatibility(context.Background(), inspector, "docker", "image:test")
			if (err != nil) != test.wantErr {
				t.Fatalf("EnsureImageCompatibility error = %v", err)
			}
			gotPull := false
			for _, call := range calls {
				gotPull = gotPull || strings.Contains(call, "image pull")
			}
			if gotPull != test.wantPull {
				t.Fatalf("pull called = %v, calls = %s", gotPull, fmt.Sprintf("%q", calls))
			}
		})
	}
}

func TestAuditInvocationRejectsDangerousAppend(t *testing.T) {
	workspace := t.TempDir()
	invocation, err := BuildRunInvocation("docker", "image", workspace, []string{"codex"}, nil, "codex", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, added := range [][]string{{"--cap-add", "SYS_ADMIN"}, {"--device", "/dev/null"}, {"--security-opt", "seccomp=unconfined"}, {"--mount", "type=bind,src=/host-home,dst=/home/agent/.codex"}} {
		mutated := invocation
		mutated.Args = append(append([]string{}, invocation.Args[:len(invocation.Args)-2]...), append(added, invocation.Args[len(invocation.Args)-2:]...)...)
		if err := AuditInvocation(mutated, workspace, "codex"); err == nil {
			t.Fatalf("accepted appended %#v", added)
		}
	}
}
