package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// macOS 14+ protects the data of other apps (~/Library/Containers/<app>,
// ~/Library/Group Containers/<group>): the first access from a process
// without Full Disk Access opens a system permission prompt and the
// open()/stat() call BLOCKS until someone answers it. A scanner must never
// trigger that, so without Full Disk Access those folders are not entered.

// ErrNeedsFullDiskAccess is returned for paths inside other apps' containers
// when the process has no Full Disk Access.
var ErrNeedsFullDiskAccess = errors.New("needs Full Disk Access (System Settings › Privacy & Security › Full Disk Access)")

var appData struct {
	once  sync.Once
	roots []string // lowercased container roots
	fda   bool
}

func initAppData() {
	appData.once.Do(func() {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return
		}
		for _, r := range []string{"Library/Containers", "Library/Group Containers"} {
			appData.roots = append(appData.roots, strings.ToLower(filepath.Join(home, r)))
		}
		// The TCC database is only readable with Full Disk Access; trying to
		// open it fails silently (no prompt) otherwise.
		if f, err := os.Open(filepath.Join(home, "Library/Application Support/com.apple.TCC/TCC.db")); err == nil {
			f.Close()
			appData.fda = true
		}
		if os.Getenv("LU_ASSUME_FULL_DISK_ACCESS") == "1" {
			appData.fda = true
		}
	})
}

// FullDiskAccess reports whether this process has Full Disk Access.
func FullDiskAccess() bool {
	initAppData()
	return appData.fda
}

// AppDataProtected reports whether reading p could block on the macOS
// app-data permission prompt: p is inside another app's container (below
// ~/Library/Containers/<app> or ~/Library/Group Containers/<group>) and the
// process has no Full Disk Access. The container roots themselves may be
// listed safely.
func AppDataProtected(p string) bool {
	initAppData()
	if appData.fda {
		return false
	}
	k := strings.ToLower(filepath.Clean(p))
	for _, r := range appData.roots {
		if strings.HasPrefix(k, r+"/") {
			return true
		}
	}
	return false
}

// GlobPrefixProtected reports whether a glob pattern starts inside a
// protected container (checked on its static, metacharacter-free prefix).
func GlobPrefixProtected(pattern string) bool {
	p := pattern
	for strings.ContainsAny(p, "*?[") {
		p = filepath.Dir(p)
	}
	if AppDataProtected(p) {
		return true
	}
	// "~/Library/Containers/*/Data/…" would list every container: protected
	// as soon as the pattern goes below a container root.
	initAppData()
	if appData.fda {
		return false
	}
	k := strings.ToLower(filepath.Clean(pattern))
	for _, r := range appData.roots {
		if strings.HasPrefix(k, r+"/") {
			return true
		}
	}
	return false
}
