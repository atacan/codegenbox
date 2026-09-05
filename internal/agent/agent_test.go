package agent

import (
	"reflect"
	"testing"
)

func TestLookupCodexIsTheOnlyPhaseOneAdapter(t *testing.T) {
	adapter, err := Lookup("CODEX")
	if err != nil {
		t.Fatalf("Lookup(codex): %v", err)
	}
	if adapter.Name != Codex {
		t.Fatalf("adapter name = %q, want %q", adapter.Name, Codex)
	}
	wantCommand := []string{"npx", "--yes", "@openai/codex", "--yolo"}
	if !reflect.DeepEqual(adapter.Command, wantCommand) {
		t.Fatalf("command = %#v, want %#v", adapter.Command, wantCommand)
	}
	if _, err := Lookup("claude"); err == nil {
		t.Fatal("Lookup(claude) succeeded; Phase 1 must expose only Codex")
	}
}
