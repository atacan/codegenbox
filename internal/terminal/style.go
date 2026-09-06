// Package terminal provides restrained, capability-aware terminal styling for
// human-facing CLI output.
package terminal

import (
	"io"
	"os"
)

const (
	reset      = "\x1b[0m"
	boldCyan   = "\x1b[1;36m"
	boldGreen  = "\x1b[1;32m"
	boldYellow = "\x1b[1;33m"
)

// Style applies ANSI decoration only when it is safe and useful. It keeps
// every value intact when disabled, so text output never depends on color.
type Style struct {
	enabled bool
}

// ForWriter selects styling for an interactive terminal. Redirected output,
// TERM=dumb, and the NO_COLOR convention always use plain text.
func ForWriter(output io.Writer) Style {
	file, ok := output.(*os.File)
	if !ok {
		return Style{}
	}
	info, err := file.Stat()
	return Style{enabled: colorEnabled(err == nil && info.Mode()&os.ModeCharDevice != 0, os.Getenv("NO_COLOR"), os.Getenv("TERM"))}
}

// New is useful when a caller already knows whether ANSI is supported, and
// keeps styled output deterministic in tests.
func New(enabled bool) Style {
	return Style{enabled: enabled}
}

func (s Style) Command(value string) string {
	return s.wrap(boldCyan, value)
}

func (s Style) Success(value string) string {
	return s.wrap(boldGreen, value)
}

func (s Style) Warning(value string) string {
	return s.wrap(boldYellow, value)
}

func (s Style) wrap(prefix, value string) string {
	if !s.enabled || value == "" {
		return value
	}
	return prefix + value + reset
}

func colorEnabled(isTerminal bool, noColor, term string) bool {
	return isTerminal && noColor == "" && term != "dumb"
}
