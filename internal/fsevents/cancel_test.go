package fsevents

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCancellationWatcherJoinsNotificationBeforeReleasingMemory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releases sync.Once
	defer releases.Do(func() { close(release) })
	var memoryReleased, wroteAfterRelease atomic.Bool
	stop := watchCancellation(ctx, func() {
		close(started)
		<-release // represent a notification still accessing native memory
		wroteAfterRelease.Store(memoryReleased.Load())
	})
	cancel()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cancellation notification did not start")
	}
	go func() {
		stop()
		memoryReleased.Store(true)
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("native memory released while its notification was still running")
	case <-time.After(25 * time.Millisecond):
	}
	releases.Do(func() { close(release) })
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("watcher did not join the finished notification")
	}
	if wroteAfterRelease.Load() {
		t.Fatal("notification accessed released native memory")
	}
}

func TestStoppedCancellationWatcherDoesNotNotify(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var notified atomic.Bool
	stop := watchCancellation(ctx, func() { notified.Store(true) })
	stop()
	cancel()
	if notified.Load() {
		t.Fatal("stopped watcher notified after its native memory was released")
	}
}
