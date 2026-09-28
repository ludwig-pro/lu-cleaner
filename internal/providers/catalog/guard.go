package catalog

import (
	"strings"

	"github.com/ludwig-pro/lu-cleaner/internal/sysx"
)

// runningGuard returns the running process names among guard (comma separated).
func runningGuard(guard []string) string {
	if len(guard) == 0 {
		return ""
	}
	return strings.Join(sysx.Running(guard...), ", ")
}
