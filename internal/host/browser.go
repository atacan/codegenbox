package host

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// OpenBrowser opens a validated GitHub compare URL with the host desktop
// handler. It is called only by an explicit CLI command or an opt-in completed
// session handoff. No shell is involved.
func OpenBrowser(ctx context.Context, address string) error {
	if len(address) == 0 || len(address) > 2048 || !strings.HasPrefix(address, "https://github.com/") || strings.ContainsAny(address, "\x00\r\n") {
		return fmt.Errorf("invalid browser address")
	}
	var binary string
	var arguments []string
	switch runtime.GOOS {
	case "darwin":
		binary, arguments = "open", []string{address}
	case "linux":
		binary, arguments = "xdg-open", []string{address}
	default:
		return fmt.Errorf("opening a browser is not supported on %s; open %s manually", runtime.GOOS, address)
	}
	if err := exec.CommandContext(ctx, binary, arguments...).Run(); err != nil {
		return fmt.Errorf("open GitHub compare page: %w", err)
	}
	return nil
}
