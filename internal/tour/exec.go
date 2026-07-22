package tour

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// runDemoctl re-invokes the currently running democtl binary with args (e.g.
// "cut", "--cabinet", "cab-i85-001"), so the tour's wan-cut/restore phases
// reuse cmd/democtl's already-hardened cut/restore logic (leaf-container and
// "vikasa" WAN-network resolution via internal/verify's docker helpers, the
// DNS-alias-on-reconnect fix, and so on) in one place, instead of
// duplicating or refactoring it. os.Executable resolves correctly whether
// democtl is running as a built binary or under `go run` (which compiles to
// a temp binary and execs that).
func runDemoctl(ctx context.Context, args ...string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("tour: resolve democtl executable: %w", err)
	}
	cmd := exec.CommandContext(ctx, self, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tour: %s %v: %w", self, args, err)
	}
	return nil
}
