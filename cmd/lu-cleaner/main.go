// Command lu-cleaner frees disk space on a macOS developer machine:
// worktrees, node_modules, iOS/Android builds, simulators, emulators,
// package-manager caches and AI tools data.
package main

import (
	"os"

	"github.com/ludwig-pro/lu-cleaner/internal/cli"
)

// version is set with -ldflags "-X main.version=..."
var version = "dev"

func main() {
	os.Exit(cli.Execute(version))
}
