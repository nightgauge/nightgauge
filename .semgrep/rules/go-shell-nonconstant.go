// Fixture for go-shell-nonconstant, checked by `semgrep --test .semgrep/rules`
// (scripts/semgrep-check.sh). Go tooling ignores this dot-directory.
package fixture

import (
	"context"
	"os/exec"
)

func unsafe(ctx context.Context, x string) {
	// ruleid: go-shell-nonconstant
	exec.Command("sh", "-c", "echo "+x)
	// ruleid: go-shell-nonconstant
	exec.CommandContext(ctx, "bash", "-c", x)
	// ruleid: go-shell-nonconstant
	exec.Command("/bin/sh", "-lc", x)
}

func safe(ctx context.Context, x string) {
	// ok: go-shell-nonconstant
	exec.Command("sh", "-c", `printf '%s' "$1"`, "sh", x)
	// ok: go-shell-nonconstant
	exec.CommandContext(ctx, "git", "-c", "core.quotePath=false", x)
	// ok: go-shell-nonconstant
	exec.Command("echo", x)
}
