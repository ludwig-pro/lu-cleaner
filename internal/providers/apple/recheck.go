package apple

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// listDevices re-reads `simctl list -j devices` (for Recheck functions).
func listDevices(ctx context.Context, runner core.Runner) (*simList, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := runner.Output(cctx, "", "xcrun", "simctl", "list", "-j", "devices")
	if err != nil {
		return nil, fmt.Errorf("cannot verify simulator state: %v", err)
	}
	var l simList
	if err := json.Unmarshal(jsonPayload(out), &l); err != nil {
		return nil, fmt.Errorf("cannot verify simulator state: %v", err)
	}
	if l.Devices == nil {
		return nil, fmt.Errorf("cannot verify simulator state: simctl list: no devices section")
	}
	return &l, nil
}

// shutdownRecheck returns a core.Item.Recheck that re-reads `simctl list`
// right before cleaning and refuses when one of the simulators is no longer
// shut down (it may have been booted since the scan).
func shutdownRecheck(runner core.Runner, udids []string) func(context.Context) error {
	want := map[string]bool{}
	for _, u := range udids {
		want[strings.ToUpper(u)] = true
	}
	return func(ctx context.Context) error {
		l, err := listDevices(ctx, runner)
		if err != nil {
			return err
		}
		for _, devs := range l.Devices {
			for _, d := range devs {
				if want[strings.ToUpper(d.UDID)] && d.State != "Shutdown" {
					return fmt.Errorf("simulator %s is %s now — shut it down first", d.Name, strings.ToLower(d.State))
				}
			}
		}
		return nil
	}
}

// unavailableRecheck returns a core.Item.Recheck for `simctl delete <udid>`
// of an unavailable simulator: right before cleaning, the device must still
// be listed, still unavailable, still shut down and still attached to the
// same runtime. A device that became available again (Xcode switched back,
// runtime re-installed or re-mounted) is kept.
func unavailableRecheck(runner core.Runner, udid, runtime string) func(context.Context) error {
	return func(ctx context.Context) error {
		l, err := listDevices(ctx, runner)
		if err != nil {
			return err
		}
		for rt, devs := range l.Devices {
			for _, d := range devs {
				if !strings.EqualFold(d.UDID, udid) {
					continue
				}
				switch {
				case d.IsAvailable:
					return fmt.Errorf("simulator %s is available again — rescan", d.Name)
				case rt != runtime:
					return fmt.Errorf("simulator %s changed runtime since the scan — rescan", d.Name)
				case d.State != "" && d.State != "Shutdown":
					return fmt.Errorf("simulator %s is %s now", d.Name, strings.ToLower(d.State))
				}
				return nil
			}
		}
		return fmt.Errorf("simulator %s is no longer listed by simctl — rescan", udid)
	}
}
