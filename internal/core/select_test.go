package core

import "testing"

func TestTopLevel(t *testing.T) {
	a := &Item{ID: "a", Path: "/h/proj/node_modules"}
	b := &Item{ID: "b", Path: "/h/proj/node_modules/react"}
	wt := &Item{ID: "wt", Path: "/h/wt/x"}
	wtNM := &Item{ID: "wtnm", Path: "/h/wt/x/node_modules"}
	group := &Item{ID: "g", Paths: []string{"/h/avd/p.avd", "/h/avd/p.ini"}}
	snap := &Item{ID: "s", Path: "/h/avd/p.avd/snapshots"}
	partial := &Item{ID: "p", Paths: []string{"/h/proj/node_modules/x", "/h/other"}}
	dup1 := &Item{ID: "d1", Path: "/h/dup"}
	dup2 := &Item{ID: "d2", Path: "/h/dup"}
	cmd1 := &Item{ID: "c1", Command: []string{"xcrun", "simctl", "delete", "unavailable"}}
	cmd2 := &Item{ID: "c2", Command: []string{"xcrun", "simctl", "delete", "unavailable"}}
	got := TopLevel([]*Item{b, a, wtNM, wt, snap, group, partial, dup1, dup2, cmd1, cmd2})
	ids := map[string]bool{}
	for _, it := range got {
		ids[it.ID] = true
	}
	for _, want := range []string{"a", "wt", "g", "p", "d1", "c1"} {
		if !ids[want] {
			t.Errorf("missing %s in %v", want, ids)
		}
	}
	for _, drop := range []string{"b", "wtnm", "s", "d2", "c2"} {
		if ids[drop] {
			t.Errorf("%s should have been dropped", drop)
		}
	}
	sim := &Item{ID: "sim", Command: []string{"xcrun", "simctl", "delete", "U1"}, Covers: "/h/Devices/U1", Size: 10, Method: MethodCommand, Selectable: true}
	att := &Item{ID: "att", Paths: []string{"/h/Devices/U1/data/tmp/a.mov"}, Size: 7, Selectable: true}
	got = TopLevel([]*Item{att, sim})
	if len(got) != 1 || got[0].ID != "sim" {
		t.Errorf("command covering a device must absorb its nested items: %v", got)
	}
	if Total([]*Item{att, sim}) != 10 {
		t.Errorf("Total = %d, want 10", Total([]*Item{att, sim}))
	}
	if Total([]*Item{{Path: "/x", Size: 10, Selectable: true}, {Path: "/x/y", Size: 5, Selectable: true}, {Paths: []string{"/z"}, Size: 3, Selectable: true}, {Path: "/r", Size: 99, Method: MethodReport, Selectable: true}}) != 13 {
		t.Errorf("Total double counts nested items")
	}
}
