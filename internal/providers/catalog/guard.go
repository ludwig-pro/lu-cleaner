package catalog

import (
	"context"
	"strings"

	"github.com/ludwig-pro/lu-cleaner/internal/sysx"
)

// runningGuard returns the running process names among guard (comma separated).
func runningGuard(ctx context.Context, guard []string) (string, error) {
	if len(guard) == 0 {
		return "", nil
	}
	names, err := sysx.RunningContext(ctx, guard...)
	return strings.Join(names, ", "), err
}
