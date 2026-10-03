package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/diagnostics"
	"github.com/spf13/cobra"
)

func (c *cli) diagnosticsStore() (diagnostics.Store, error) {
	if c.Diagnostics == nil {
		return diagnostics.Store{}, fmt.Errorf("diagnostics storage is unavailable")
	}
	return c.Diagnostics(), nil
}

func (c *cli) diagnosticsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "diagnostics", Short: "Manage optional technical error reports (off until explicit consent)", Args: cobra.NoArgs}
	status := &cobra.Command{Use: "status", Short: "Show consent and the configured recipient", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		s, err := c.diagnosticsStore()
		if err != nil {
			return err
		}
		consent, err := s.Consent()
		if err != nil {
			return err
		}
		decision := consent.Decision
		if decision == "" {
			decision = "undecided"
		}
		if c.f.json {
			return c.writeJSON(map[string]any{"enabled": s.Enabled(), "decision": decision, "notice": diagnostics.NoticeVersion, "destination": s.Destination()})
		}
		fmt.Fprintf(c.Stdout, "Technical reports enabled: %t\nConsent: %s\nRecipient: %s\nNotice: %s\n", s.Enabled(), decision, s.Destination(), diagnostics.NoticeVersion)
		return nil
	}}
	var accept bool
	enable := &cobra.Command{Use: "enable", Short: "Read the notice and explicitly consent to technical reports", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		s, err := c.diagnosticsStore()
		if err != nil {
			return err
		}
		fmt.Fprintf(c.Stderr, "%s\nConfigured recipient: %s\n", diagnostics.Notice, s.Destination())
		if s.Destination() == "not configured" {
			return usageErr("set LU_DIAGNOSTICS_DSN to the maintainer's public HTTPS Sentry DSN first")
		}
		if !accept {
			if !c.StdinTTY || c.f.json {
				return usageErr("consent required: use diagnostics enable --accept-notice %s after reading the notice; --yes never gives consent", diagnostics.NoticeVersion)
			}
			fmt.Fprint(c.Stderr, "Allow these optional reports? [y/N] ")
			answer, readErr := bufio.NewReader(c.Stdin).ReadString('\n')
			if readErr != nil && len(answer) == 0 {
				return readErr
			}
			answer = strings.ToLower(strings.TrimSpace(answer))
			accept = answer == "y" || answer == "yes" || answer == "oui"
		}
		if err := s.Decide(accept); err != nil {
			return err
		}
		if c.f.json {
			return c.writeJSON(map[string]bool{"enabled": accept})
		}
		fmt.Fprintf(c.Stdout, "Technical reports enabled: %t\n", accept)
		return nil
	}}
	var notice string
	enable.Flags().StringVar(&notice, "accept-notice", "", "explicitly accept the displayed notice version (for non-interactive use)")
	enable.PreRunE = func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("accept-notice") {
			if notice != diagnostics.NoticeVersion {
				return usageErr("notice version must be %s", diagnostics.NoticeVersion)
			}
			accept = true
		}
		return nil
	}
	disable := &cobra.Command{Use: "disable", Short: "Revoke consent and remove the local report", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		s, err := c.diagnosticsStore()
		if err != nil {
			return err
		}
		if err := s.Decide(false); err != nil {
			return err
		}
		if c.f.json {
			return c.writeJSON(map[string]bool{"enabled": false})
		}
		fmt.Fprintln(c.Stdout, "Technical reports disabled; local report removed.")
		return nil
	}}
	export := &cobra.Command{Use: "export", Short: "Print the last sanitized local report as JSON (no upload)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		s, err := c.diagnosticsStore()
		if err != nil {
			return err
		}
		data, err := s.Export()
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no recent local error report")
		}
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(c.Stdout, string(data))
		return err
	}}
	cmd.AddCommand(status, enable, disable, export)
	cmd.RunE = status.RunE
	return cmd
}

func (c *cli) cleanWarnings(sum *clean.Summary) {
	if sum.HistoryError != nil {
		c.errw.printf("warning: history could not be recorded: %s\n", sanitizeLines(sum.HistoryError.Error(), "  "))
	}
	if sum.InternalError != nil {
		c.errw.printf("warning: %s; remaining cleanup stopped\n", sum.InternalError.Error())
	}
}
