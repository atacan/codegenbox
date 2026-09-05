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
	invocation := Invocation{Binary: "docker", Args: []string{"run", "--rm", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--mount", "type=bind,src=/safe,dst=/workspace", "--privileged", "image", "codex"}}
	if err := AuditInvocation(invocation, "/safe", "codex"); err == nil {
		t.Fatal("dangerous appended argument accepted")
	}
}
