package fsevents

import "context"

// watchCancellation notifies once when ctx ends. Its returned function stops
// the watcher and joins any notification already running; call it once before
// releasing the native memory that notify may access.
func watchCancellation(ctx context.Context, notify func()) func() {
	finished, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-ctx.Done():
			notify()
		case <-finished:
		}
	}()
	return func() {
		close(finished)
		<-joined
	}
}
