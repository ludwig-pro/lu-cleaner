package apple

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// shutdownRecheck returns a core.Item.Recheck that re-reads `simctl list`
// right before cleaning and refuses when one of the simulators is no longer
// shut down (it may have been booted since the scan).
func shutdownRecheck(runner core.Runner, udids []string) func(context.Context) error {
	want := map[string]bool{}
	for _, u := range udids {
		want[strings.ToUpper(u)] = true
	}
	return func(ctx context.Context) error {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		out, err := runner.Output(cctx, "", "xcrun", "simctl", "list", "-j", "devices")
		if err != nil {
			return fmt.Errorf("cannot verify simulator state: %v", err)
		}
		var l simList
		if err := json.Unmarshal(jsonPayload(out), &l); err != nil {
			return fmt.Errorf("cannot verify simulator state: %v", err)
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
