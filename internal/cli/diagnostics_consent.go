package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ludwig-pro/lu-cleaner/internal/diagnostics"
	"golang.org/x/sys/unix"
)

// Ask before creating the reporting session or starting application work.
// Redirected streams and --yes are deliberately non-interactive.
func (c *cli) prepareDiagnosticsConsent(ctx context.Context, store diagnostics.Store) (diagnostics.Store, error) {
	if err := ctx.Err(); err != nil {
		return store, err
	}
	if !c.interactive() || !c.StderrTTY || c.f.json || c.f.yes {
		return store, nil
	}
	needs, err := store.NeedsConsent()
	if err != nil {
		return c.consentUnavailable(store, "read"), nil
	}
	if !needs {
		return store, nil
	}
	if err := ctx.Err(); err != nil {
		return store, err
	}
	c.printConsentNotice(store.Destination())
	accepted, err := c.readConsentAnswer(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return store, ctx.Err()
		}
		if errors.Is(err, io.EOF) {
			return store, nil // No answer: leave the preference undecided.
		}
		return c.consentUnavailable(store, "read"), nil
	}
	if err := ctx.Err(); err != nil {
		return store, err
	}
	if err := store.Decide(accepted); err != nil {
		return c.consentUnavailable(store, "save"), nil
	}
	printConsentResult(c.consentOutput(), accepted)
	return store, nil
}

func (c *cli) consentUnavailable(store diagnostics.Store, action string) diagnostics.Store {
	fmt.Fprintf(c.Stderr, "warning: could not %s technical-report preference; reports stay off for this run\n", action)
	// A persistence error must not enable reporting in this invocation, even if
	// the preference file became visible before its final disk synchronization.
	store.DSN = ""
	return store
}

// Read exactly the answer line, without buffering the TUI's subsequent input.
// Native stdin is polled so Ctrl-C cancels without a reader goroutine surviving.
func (c *cli) readConsentAnswer(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	c.printConsentPrompt()
	var answer strings.Builder
	var bytebuf [1]byte
	tooLong := false
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if file, ok := c.Stdin.(*os.File); ok {
			fd := int32(file.Fd())
			if fd < 0 {
				return false, os.ErrClosed
			}
			fds := []unix.PollFd{{Fd: fd, Events: unix.POLLIN}}
			n, err := unix.Poll(fds, 50)
			if errors.Is(err, unix.EINTR) || err == nil && n == 0 {
				continue
			}
			if err != nil {
				return false, err
			}
			if fds[0].Revents&unix.POLLNVAL != 0 {
				return false, fmt.Errorf("consent input is unavailable")
			}
			if err := ctx.Err(); err != nil {
				return false, err
			}
		}
		n, err := c.Stdin.Read(bytebuf[:])
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if n > 0 && bytebuf[0] != '\n' {
			if answer.Len() < 64 {
				answer.WriteByte(bytebuf[0])
			} else {
				tooLong = true
			}
		}
		if n > 0 && bytebuf[0] == '\n' || errors.Is(err, io.EOF) && answer.Len() > 0 {
			value := strings.ToLower(strings.TrimSpace(answer.String()))
			return !tooLong && (value == "y" || value == "yes" || value == "oui"), nil
		}
		if err != nil {
			return false, err
		}
		if n == 0 {
			return false, io.ErrNoProgress
		}
	}
}
