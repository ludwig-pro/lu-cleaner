package core

import (
	"testing"
	"time"
)

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

func TestRecommend(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour
	mk := func(r Risk, cat Category, age time.Duration, rec bool, warn string) *Item {
		return &Item{Risk: r, Category: cat, Size: 10 << 20, LastUsed: now.Add(-age), Recommended: rec, Warn: warn, Selectable: true}
	}
	cases := []struct {
		name string
		it   *Item
		want bool
	}{
		{"safe cache", mk(RiskSafe, CatJS, time.Hour, false, ""), true},
		{"safe with warning", mk(RiskSafe, CatJS, 10*day, false, "Xcode is running"), false},
		{"forced but warned", mk(RiskModerate, CatAndroid, 10*day, true, "running"), false},
		{"forced caution", mk(RiskCaution, CatAI, 90*day, true, ""), false},
		{"moderate fresh", mk(RiskModerate, CatJS, 2*day, false, ""), false},
		{"moderate stale", mk(RiskModerate, CatJS, 30*day, false, ""), true},
		{"active project build", mk(RiskSafe, CatArtifacts, 2*time.Hour, true, ""), false},
		{"idle project build", mk(RiskSafe, CatArtifacts, 3*day, false, ""), true},
	}
	for _, c := range cases {
		if got := Recommend(c.it, now, 14*day); got != c.want {
			t.Errorf("%s: Recommend = %v, want %v", c.name, got, c.want)
		}
	}
}
