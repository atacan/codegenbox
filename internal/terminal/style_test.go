package terminal

import "testing"

func TestStyleDecoratesOnlyWhenEnabled(t *testing.T) {
	plain := New(false)
	if got := plain.Command("codegenbox resume abc123"); got != "codegenbox resume abc123" {
		t.Fatalf("plain command = %q", got)
	}

	styled := New(true)
	if got, want := styled.Command("codegenbox resume abc123"), "\x1b[1;36mcodegenbox resume abc123\x1b[0m"; got != want {
		t.Fatalf("styled command = %q, want %q", got, want)
	}
	if got, want := styled.Success("complete"), "\x1b[1;32mcomplete\x1b[0m"; got != want {
		t.Fatalf("styled success = %q, want %q", got, want)
	}
	if got, want := styled.Warning("stopped"), "\x1b[1;33mstopped\x1b[0m"; got != want {
		t.Fatalf("styled warning = %q, want %q", got, want)
	}
}

func TestColorEnabledRespectsPlainOutputPreferences(t *testing.T) {
	for _, test := range []struct {
		name       string
		isTerminal bool
		noColor    string
		term       string
		want       bool
	}{
		{name: "interactive terminal", isTerminal: true, term: "xterm-256color", want: true},
		{name: "redirected output", isTerminal: false, term: "xterm-256color"},
		{name: "NO_COLOR", isTerminal: true, noColor: "1", term: "xterm-256color"},
		{name: "dumb terminal", isTerminal: true, term: "dumb"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := colorEnabled(test.isTerminal, test.noColor, test.term); got != test.want {
				t.Fatalf("colorEnabled(%t, %q, %q) = %t, want %t", test.isTerminal, test.noColor, test.term, got, test.want)
			}
		})
	}
}
