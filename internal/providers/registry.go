// Package providers lists every scanner of lu-cleaner.
package providers

import (
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/aitools"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/android"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/apple"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/artifacts"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/catalog"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/jsdev"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/system"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/worktrees"
)

// All returns every provider, in display priority order.
func All() []core.Provider {
	return []core.Provider{
		worktrees.New(),
		artifacts.New(),
		apple.New(),
		android.New(),
		aitools.New(),
		jsdev.New(),
		system.New(),
		catalog.New(),
	}
}

// For returns the providers that may emit at least one of cats (all when cats is empty).
func For(cats []core.Category) []core.Provider {
	all := All()
	if len(cats) == 0 {
		return all
	}
	var out []core.Provider
	for _, p := range all {
		for _, pc := range p.Categories() {
			if contains(cats, pc) {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

func contains(cats []core.Category, c core.Category) bool {
	for _, x := range cats {
		if x == c {
			return true
		}
	}
	return false
}
