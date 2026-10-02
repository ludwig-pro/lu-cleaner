package tui

import (
	"context"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
)

// scanContext shares the CLI controller when one is present. Direct callers
// may supply limits through Env; internal callers without them keep fast mode.
func scanContext(ctx context.Context, env *core.Env) context.Context {
	if scanctl.From(ctx) != nil {
		return ctx
	}
	if env != nil && env.ScanLimits.Mode != "" {
		return scanctl.With(ctx, scanctl.New(env.ScanLimits))
	}
	return scanctl.Ensure(ctx)
}

func scanMode(ctx context.Context) string {
	if ctrl := scanctl.From(ctx); ctrl != nil {
		return string(ctrl.Limits().Mode)
	}
	return string(scanctl.Fast)
}
