package core

import (
	"testing"
	"time"
)

// A provider veto (NoRecommend) must win over the moderate "stale" rule and
// over Recommended: the Trash, orphan data of uncertain origin... must never
// be preselected by smart select, however old.
func TestRecommendHonoursProviderVeto(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour
	stale := now.Add(-60 * day)
	trash := &Item{Kind: "trash", Category: CatSystem, Risk: RiskModerate, Method: MethodDelete, Size: 5 << 30, LastUsed: stale, Selectable: true, NoRecommend: true}
	if Recommend(trash, now.Add(30*day), 14*day) {
		t.Error("vetoed moderate item preselected once stale")
	}
	safe := &Item{Category: CatJS, Risk: RiskSafe, Size: 1 << 30, LastUsed: stale, Selectable: true, NoRecommend: true}
	if Recommend(safe, now, 14*day) {
		t.Error("vetoed safe item preselected")
	}
	forced := &Item{Category: CatAI, Risk: RiskModerate, Size: 1 << 30, LastUsed: stale, Selectable: true, Recommended: true, NoRecommend: true}
	if Recommend(forced, now, 14*day) {
		t.Error("veto must win over Recommended")
	}
	// Control: without the veto the same items are recommended.
	for _, it := range []*Item{trash, safe, forced} {
		c := it.Clone()
		c.NoRecommend = false
		if !Recommend(c, now.Add(30*day), 14*day) {
			t.Errorf("control: %+v not recommended without the veto", c)
		}
	}
}

// Siblings whose name continues with a byte lower than '/' (' ', '-', '.')
// sort between a directory and its children with a plain string order.
func TestTopLevelInterleavedSiblings(t *testing.T) {
	cases := [][3]string{
		{"/h/dev/app", "/h/dev/app-web", "/h/dev/app/node_modules"},
		{"/h/My App", "/h/My App 2", "/h/My App/Pods"},
		{"/h/dev/app-feat", "/h/dev/app-feat-2", "/h/dev/app-feat/node_modules"},
		{"/h/x", "/h/x.old", "/h/x/y/z"},
	}
	for _, c := range cases {
		parent := &Item{ID: "parent", Path: c[0], Size: 100, Selectable: true}
		sib := &Item{ID: "sib", Path: c[1], Size: 100, Selectable: true}
		child := &Item{ID: "child", Path: c[2], Size: 50, Selectable: true}
		for _, order := range [][]*Item{{parent, sib, child}, {child, sib, parent}, {sib, child, parent}} {
			got := TopLevel(order)
			ids := map[string]bool{}
			for _, it := range got {
				ids[it.ID] = true
			}
			if len(got) != 2 || !ids["parent"] || !ids["sib"] {
				t.Errorf("%v: TopLevel kept %v, want parent and sibling only", c, ids)
			}
			if n := Total(order); n != 200 {
				t.Errorf("%v: Total = %d, want 200 (child double counted)", c, n)
			}
		}
	}
	// A group item whose paths are all inside other items is dropped too.
	grp := &Item{ID: "g", Paths: []string{"/h/dev/app/a", "/h/dev/app-web/b"}, Selectable: true}
	got := TopLevel([]*Item{{ID: "p", Path: "/h/dev/app"}, {ID: "s", Path: "/h/dev/app-web"}, grp})
	for _, it := range got {
		if it.ID == "g" {
			t.Error("group covered by two items must be dropped")
		}
	}
	// Display order is component-aware too: a parent comes before its sibling "app-web".
	got = TopLevel([]*Item{{ID: "s", Path: "/h/dev/app-web"}, {ID: "p", Path: "/h/dev/app"}})
	if got[0].ID != "p" {
		t.Errorf("order: %s first", got[0].ID)
	}
}

func TestPathLess(t *testing.T) {
	sorted := []string{"/", "/a", "/a/app", "/a/app/node_modules", "/a/app/z", "/a/app 2", "/a/app-web", "/a/app.old", "/a/apps", "/b"}
	for i := range sorted {
		for j := range sorted {
			if got, want := pathLess(sorted[i], sorted[j]), i < j; got != want {
				t.Errorf("pathLess(%q, %q) = %v, want %v", sorted[i], sorted[j], got, want)
			}
		}
	}
}
