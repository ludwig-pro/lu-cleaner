package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/ludwig-pro/lu-cleaner/internal/update"
	"github.com/spf13/cobra"
)

func (c *cli) prepareStartupUpdate(cmd *cobra.Command) error {
	if c.StartupUpdate == nil || !c.interactive() || !c.StderrTTY || c.f.yes || c.f.json || c.f.dryRun || c.Getenv("CI") != "" || c.Getenv("LU_NO_UPDATE") != "" || os.Geteuid() == 0 {
		return nil
	}
	if flag := cmd.Flags().Lookup("list"); flag != nil && flag.Value.String() == "true" {
		return nil
	}
	switch cmd.Name() {
	case "lu-cleaner", "clean", "artifacts", "worktrees", "devices", "analyze":
	default:
		return nil
	}
	err := c.StartupUpdate(cmd.Context(), c)
	if cmd.Context().Err() != nil {
		return cmd.Context().Err()
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func (c *cli) Confirm(ctx context.Context, offer update.Offer) (bool, error) {
	fmt.Fprintf(c.Stderr, "lu-cleaner %s is available (current %s).\n", offer.Next, offer.Current)
	if offer.GoInstall {
		fmt.Fprintln(c.Stderr, "This replaces your go install binary with the precompiled GitHub release.")
	}
	fmt.Fprintf(c.Stderr, "Update %s now? [y/N] ", sanitizeLines(offer.Target, " "))
	return c.readYesNoAnswer(ctx, false)
}

func (c *cli) Notice(message string) { fmt.Fprintln(c.Stderr, sanitizeLines(message, "  ")) }
