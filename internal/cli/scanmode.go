package cli

import (
	"context"

	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
)

// startScan runs before scan setup inventories and binds the completed guard
// afterward. The pure newSetup path never changes runtime or process priority.
func (c *cli) startScan(ctx context.Context, s *setup) (context.Context, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.scanCtl == nil {
		if c.ActivateScan != nil {
			restore, err := c.ActivateScan(s.limits)
			c.restoreScan = restore
			if err != nil {
				// Resource quotas still apply when an OS priority is unavailable.
				c.errw.printf("warning: scan priorities: %s\n", sanitizeLines(err.Error(), "  "))
			}
		}
		c.scanCtl = scanctl.New(s.limits)
		c.logf("scan limits: mode=%s io=%d commands=%d prefetch=%d batch=%d pause=%s",
			s.limits.Mode, s.limits.IO, s.limits.Commands, s.limits.Prefetch, s.limits.BatchSize, s.limits.Pause)
	}
	scanCtx := scanctl.With(ctx, c.scanCtl)
	if s.guard != nil {
		s.env.ProtectedContext = s.guard.ProtectedContext
	}
	return scanCtx, nil
}

func (c *cli) finishScan() error {
	if c.scanCtl != nil {
		c.scanCtl.Close()
		stats, limits := c.scanCtl.Snapshot(), c.scanCtl.Limits()
		c.logf("scan resources: io_max=%d/%d commands_max=%d/%d io_wait_cumulative=%s pause_cumulative=%s command_wait_cumulative=%s files=%d dirs=%d cache_hits=%d cache_misses=%d cancel_join=%s",
			stats.IOMax, limits.IO, stats.CommandMax, limits.Commands,
			stats.IOWait, stats.ThrottleWait, stats.CommandWait, stats.Files, stats.Dirs,
			stats.CacheHits, stats.CacheMisses, stats.CancelLatency)
	}
	if c.restoreScan != nil {
		return c.restoreScan()
	}
	return nil
}
