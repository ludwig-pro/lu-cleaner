package android

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
)

func TestProjectDiscoveryCancellationWhileWaitingForIOIsIncomplete(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "android"), 0o755); err != nil {
		t.Fatal(err)
	}
	limits, _, _ := scanctl.Resolve("fast", "1")
	controller := scanctl.New(limits)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = scanctl.With(ctx, controller)
	release, err := controller.AcquireIO(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	env := &core.Env{Home: root, Roots: []string{root}, MaxDepth: 8}
	p := &Provider{}
	p.defaults(env)
	s := newScan(ctx, p, env, nil)
	done := make(chan *projectInfo, 1)
	go func() { done <- s.scanProjects() }()
	cancel()
	select {
	case pi := <-done:
		if pi.complete || pi.known() || pi.roots != 1 {
			t.Fatalf("cancelled discovery classified references as unused: %+v", pi)
		}
	case <-time.After(time.Second):
		t.Fatal("project discovery did not cancel while waiting for admission")
	}
}
