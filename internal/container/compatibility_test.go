package container

import (
	"context"
	"errors"
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
